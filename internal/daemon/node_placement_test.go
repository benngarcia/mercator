package daemon_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/capability"
	"github.com/benngarcia/mercator/internal/daemon"
	"github.com/benngarcia/mercator/internal/nodeagent"
)

const trainerImage = "ghcr.io/acme/trainer@sha256:1111111111111111111111111111111111111111111111111111111111111111"

// TestOneEnrolledNodeRunsTwoWorkloadsInSequence is the claim the reusable lane
// exists to make. The same machine executes a second Run without anything being
// provisioned, because the node held the host runtime open between them. On the
// second Run, Placement records that it reused the Rental rather than creating
// one.
func TestOneEnrolledNodeRunsTwoWorkloadsInSequence(t *testing.T) {
	fleet := startFleet(t)

	first := fleet.submitRun(t)
	fleet.completeWorkload(t, first, 0)
	fleet.awaitOutcome(t, first, "succeeded")

	second := fleet.submitRun(t)
	fleet.completeWorkload(t, second, 0)
	fleet.awaitOutcome(t, second, "succeeded")

	for _, runID := range []string{first, second} {
		decision := fleet.decision(t, runID)
		if decision.SelectedOfferSnapshotID != fleet.nodeID {
			t.Fatalf("Run %s landed on %q, want the enrolled node %q", runID, decision.SelectedOfferSnapshotID, fleet.nodeID)
		}
	}
	if launched := fleet.runtime.launchedRuns(); len(launched) != 2 {
		t.Fatalf("the node ran %d workloads, want two in sequence: %v", len(launched), launched)
	}
	if reused := fleet.decision(t, second); reused.disposition() != "run_now_existing_rental" {
		t.Fatalf("the second Run recorded disposition %q, want it to reuse the Rental it already had", reused.disposition())
	}
}

// TestAWorkloadThatFailsOnANodeClosesTheRunFailed holds the node's authority
// over the exit: nothing the application says is involved, and the run still
// reaches a terminal failure.
func TestAWorkloadThatFailsOnANodeClosesTheRunFailed(t *testing.T) {
	fleet := startFleet(t)

	runID := fleet.submitRun(t)
	fleet.completeWorkload(t, runID, 137)

	fleet.awaitOutcome(t, runID, "failed")
}

// TestANodeThatGoesQuietStopsBeingOffered keeps Placement from choosing a
// machine Mercator has stopped hearing from. The node is refused as expired
// rather than silently preferred.
func TestANodeThatGoesQuietStopsBeingOffered(t *testing.T) {
	fleet := startFleet(t)
	fleet.stopAgent()

	// Offers expire on the age of the node's last facts, which is sooner than
	// the lease, so the catalog stops advertising it without waiting for the
	// control plane to give up entirely.
	waitFor(t, func() bool {
		offers := fleet.offers(t)
		for _, offer := range offers {
			if offer.ID == fleet.nodeID && offer.ExpiresAt.After(time.Now().UTC()) {
				return false
			}
		}
		return true
	}, "a node that stopped heartbeating was still being offered as fresh capacity")
}

// Fleet wiring below: one production daemon, one real agent over the real node
// protocol, and a runtime that records what it was asked to run.

type fleet struct {
	address   string
	token     string
	nodeID    string
	runtime   *scriptedRuntime
	stop      context.CancelFunc
	submitted int
}

func startFleet(t *testing.T) *fleet {
	t.Helper()
	// No Docker on PATH, so the daemon seeds no local connection and the
	// enrolled node is the only capacity in play. The point of these cases is
	// where a Run lands, not how offers are aggregated.
	t.Setenv("PATH", t.TempDir())
	address := startRuntimeWithLease(t, 900*time.Millisecond)
	harness := &fleet{address: address, token: "operator-token", runtime: newScriptedRuntime()}
	bootstrap := harness.invite(t)
	harness.nodeID = bootstrap.NodeID
	harness.startAgent(t, bootstrap)
	// Placement can only choose a node it has facts for, so the fleet is not
	// ready until the first heartbeat lands.
	waitFor(t, func() bool {
		for _, offer := range harness.offers(t) {
			if offer.ID == harness.nodeID {
				return true
			}
		}
		return false
	}, "the enrolled node never appeared as placeable capacity")
	return harness
}

func (f *fleet) invite(t *testing.T) capability.NodeBootstrap {
	t.Helper()
	var response struct {
		ControlPlaneURL string `json:"control_plane_url"`
		NodeID          string `json:"node_id"`
		RentalID        string `json:"rental_id"`
		Generation      uint64 `json:"generation"`
		EnrollmentToken string `json:"enrollment_token"`
		AgentVersion    string `json:"agent_version"`
	}
	f.call(t, http.MethodPost, "/v1/nodes", map[string]any{
		"workspace_id":              daemon.DefaultWorkspaceID,
		"shadow_price_usd_per_hour": 1.25,
	}, &response, http.StatusCreated)
	if response.EnrollmentToken == "" {
		t.Fatal("an invitation must return enrollment material exactly once")
	}
	return capability.NodeBootstrap{
		ControlPlaneURL: "http://" + f.address,
		NodeID:          response.NodeID,
		RentalID:        response.RentalID,
		Generation:      response.Generation,
		EnrollmentToken: response.EnrollmentToken,
		AgentVersion:    response.AgentVersion,
	}
}

func (f *fleet) startAgent(t *testing.T, bootstrap capability.NodeBootstrap) {
	t.Helper()
	state, err := nodeagent.OpenState(filepath.Join(t.TempDir(), "node-state.json"), bootstrap.NodeID)
	if err != nil {
		t.Fatalf("open agent state: %v", err)
	}
	agent := nodeagent.New(
		nodeagent.Identity{
			ControlPlaneURL: bootstrap.ControlPlaneURL,
			NodeID:          bootstrap.NodeID,
			RentalID:        bootstrap.RentalID,
			Generation:      bootstrap.Generation,
			EnrollmentToken: bootstrap.EnrollmentToken,
			AgentVersion:    "test",
		},
		f.runtime,
		nodeagent.NewHTTPTransport(bootstrap.ControlPlaneURL, nil),
		state,
		nodeagent.WithHeartbeat(20*time.Millisecond),
		nodeagent.WithReconnectBackoff(5*time.Millisecond),
	)
	ctx, cancel := context.WithCancel(context.Background())
	f.stop = cancel
	t.Cleanup(cancel)
	go func() { _ = agent.Run(ctx) }()
}

func (f *fleet) stopAgent() { f.stop() }

func (f *fleet) submitRun(t *testing.T) string {
	t.Helper()
	f.submitted++
	name := fmt.Sprintf("run-%d", f.submitted)
	var created struct {
		Run struct {
			ID string `json:"id"`
		} `json:"run"`
	}
	// A full workload spec is submitted rather than the image shorthand: the
	// shorthand resolves a digest against the broker host's Docker daemon, and
	// this case is about where a Run lands, not about resolution.
	f.call(t, http.MethodPost, "/v1/runs", map[string]any{
		"workspace_id": daemon.DefaultWorkspaceID,
		"workload":     workloadRevision(name),
	}, &created, http.StatusAccepted)
	if created.Run.ID == "" {
		t.Fatal("create run returned no run id")
	}
	return created.Run.ID
}

// completeWorkload has the machine report the container's exit on its own
// authority, then drives the run forward the way the reconcile sweep does.
func (f *fleet) completeWorkload(t *testing.T, runID string, exitCode int) {
	t.Helper()
	f.runtime.awaitLaunch(t, runID)
	f.runtime.exit(runID, exitCode)
	waitFor(t, func() bool {
		var refreshed struct {
			Run struct {
				Outcome string `json:"outcome"`
			} `json:"run"`
		}
		f.call(t, http.MethodPost, "/v1/runs/"+runID+"/refresh?workspace_id="+daemon.DefaultWorkspaceID, nil, &refreshed, http.StatusOK)
		return refreshed.Run.Outcome != ""
	}, "the run never reached a terminal outcome after the node reported its exit")
}

func (f *fleet) awaitOutcome(t *testing.T, runID, want string) {
	t.Helper()
	var run struct {
		Run struct {
			Outcome string `json:"outcome"`
			Closed  bool   `json:"closed"`
		} `json:"run"`
	}
	waitFor(t, func() bool {
		f.call(t, http.MethodGet, "/v1/runs/"+runID+"?workspace_id="+daemon.DefaultWorkspaceID, nil, &run, http.StatusOK)
		return run.Run.Outcome == want
	}, fmt.Sprintf("Run %s never reached outcome %q (last outcome %q)", runID, want, run.Run.Outcome))
}

// workloadRevision is one digest-pinned container the enrolled node can run.
// Each submission is its own revision, so a second Run is genuinely a second
// Run rather than an idempotent replay of the first.
func workloadRevision(name string) map[string]any {
	return map[string]any{
		"id":           "wlr_" + name,
		"workspace_id": daemon.DefaultWorkspaceID,
		"workload_id":  "wl_" + name,
		"digest":       "sha256:1111111111111111111111111111111111111111111111111111111111111111",
		"spec": map[string]any{
			"containers": []map[string]any{{
				"name":     "main",
				"image":    trainerImage,
				"platform": map[string]any{"os": "linux", "architecture": "amd64"},
				"args":     []string{"train"},
			}},
			"resources": map[string]any{
				"cpu":            map[string]any{"min_millis": 1000},
				"memory":         map[string]any{"min_bytes": 1 << 30},
				"ephemeral_disk": map[string]any{"min_bytes": 1 << 30},
			},
			"network":   map[string]any{"inbound": "none"},
			"placement": map[string]any{"objective": "balanced", "expected_runtime_seconds": 60},
			"execution": map[string]any{"max_runtime_seconds": 600, "max_pre_start_attempts": 3},
		},
	}
}

type bookingDecision struct {
	SelectedOfferSnapshotID string `json:"selected_offer_snapshot_id"`
	Candidates              []struct {
		OfferSnapshotID string `json:"offer_snapshot_id"`
		Disposition     string `json:"disposition"`
	} `json:"candidates"`
}

func (decision bookingDecision) disposition() string {
	for _, candidate := range decision.Candidates {
		if candidate.OfferSnapshotID == decision.SelectedOfferSnapshotID {
			return candidate.Disposition
		}
	}
	return ""
}

func (f *fleet) decision(t *testing.T, runID string) bookingDecision {
	t.Helper()
	var response struct {
		Decision bookingDecision `json:"decision"`
	}
	f.call(t, http.MethodGet, "/v1/runs/"+runID+"/decision?workspace_id="+daemon.DefaultWorkspaceID, nil, &response, http.StatusOK)
	return response.Decision
}

type offerSnapshot struct {
	ID        string    `json:"id"`
	Lane      string    `json:"lane"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (f *fleet) offers(t *testing.T) []offerSnapshot {
	t.Helper()
	var response struct {
		Offers []offerSnapshot `json:"offers"`
	}
	f.call(t, http.MethodGet, "/v1/offers?workspace_id="+daemon.DefaultWorkspaceID, nil, &response, http.StatusOK)
	return response.Offers
}

func (f *fleet) call(t *testing.T, method, path string, body, into any, wantStatus int) {
	t.Helper()
	var payload io.Reader = http.NoBody
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode %s body: %v", path, err)
		}
		payload = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, "http://"+f.address+path, payload)
	if err != nil {
		t.Fatalf("build %s %s: %v", method, path, err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+f.token)
	request.Header.Set("Idempotency-Key", fmt.Sprintf("%s %s %v", method, path, body))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("call %s %s: %v", method, path, err)
	}
	defer func() { _ = response.Body.Close() }()
	raw, _ := io.ReadAll(response.Body)
	if response.StatusCode != wantStatus {
		t.Fatalf("%s %s = %d, want %d: %s", method, path, response.StatusCode, wantStatus, raw)
	}
	if into == nil {
		return
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("decode %s %s: %v: %s", method, path, err, raw)
	}
}

func waitFor(t *testing.T, satisfied func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if satisfied() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(message)
}

// scriptedRuntime stands in for Docker. It records what it was asked to run and
// reports exits when the test says so, which is how a container's lifecycle is
// driven without a daemon.
type scriptedRuntime struct {
	mu           sync.Mutex
	launched     []string
	observations map[string]capability.WorkloadObservation
}

func newScriptedRuntime() *scriptedRuntime {
	return &scriptedRuntime{observations: map[string]capability.WorkloadObservation{}}
}

func (runtime *scriptedRuntime) Facts(context.Context) (capability.NodeFacts, error) {
	return capability.NodeFacts{
		ObservedAt: time.Now().UTC(),
		Host: capability.HostFacts{
			OS:               "linux",
			Architecture:     "amd64",
			ContainerRuntime: "docker",
			RuntimeVersion:   "27.0.0",
			CPUMillis:        8000,
			MemoryBytes:      32 << 30,
			DiskTotalBytes:   500 << 30,
			DiskFreeBytes:    400 << 30,
		},
		Images: []capability.ImageLocality{{
			ManifestDigest: "sha256:1111111111111111111111111111111111111111111111111111111111111111",
			State:          capability.LocalityHot,
			Unpacked:       true,
		}},
	}, nil
}

func (runtime *scriptedRuntime) PrepareImage(context.Context, capability.PrepareImageCommand) error {
	return nil
}

func (runtime *scriptedRuntime) PrepareArtifact(context.Context, capability.PrepareArtifactCommand) error {
	return nil
}

func (runtime *scriptedRuntime) LaunchWorkload(_ context.Context, command capability.LaunchWorkloadCommand) error {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.launched = append(runtime.launched, command.RunID)
	runtime.observations[command.RunID] = capability.WorkloadObservation{
		RunID:      command.RunID,
		AttemptID:  command.AttemptID,
		Phase:      capability.WorkloadPhaseRunning,
		ObservedAt: time.Now().UTC(),
	}
	return nil
}

func (runtime *scriptedRuntime) StopWorkload(context.Context, capability.StopWorkloadCommand) error {
	return nil
}

func (runtime *scriptedRuntime) Observe(context.Context) ([]capability.WorkloadObservation, error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	observations := make([]capability.WorkloadObservation, 0, len(runtime.observations))
	for _, observation := range runtime.observations {
		observations = append(observations, observation)
	}
	return observations, nil
}

func (runtime *scriptedRuntime) exit(runID string, code int) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	observation := runtime.observations[runID]
	observation.RunID = runID
	observation.Phase = capability.WorkloadPhaseExited
	observation.ExitCode = &code
	observation.ObservedAt = time.Now().UTC()
	runtime.observations[runID] = observation
}

func (runtime *scriptedRuntime) launchedRuns() []string {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return append([]string(nil), runtime.launched...)
}

func (runtime *scriptedRuntime) awaitLaunch(t *testing.T, runID string) {
	t.Helper()
	waitFor(t, func() bool {
		runtime.mu.Lock()
		defer runtime.mu.Unlock()
		_, launched := runtime.observations[runID]
		return launched
	}, "the node was never asked to run "+runID)
}
