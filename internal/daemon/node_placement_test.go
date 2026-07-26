package daemon_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/capability"
	"github.com/benngarcia/mercator/internal/daemon"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/nodeagent"
)

// Two versions of one image, served by a registry the test starts on loopback,
// so the daemon resolves real manifests over the real registry v2 protocol
// without reaching the network. Both are multi-platform, which is the shape
// that separates the digest a Run is pinned to from the platform manifest
// underneath it, and they share an 18GB base layer: the second version is what
// makes a host warm without holding the image whole, which is the only case the
// diff-ID bridge is load-bearing for.
const (
	trainerIndexDigest    = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	trainerManifestDigest = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	trainerConfigDigest   = "sha256:3333333333333333333333333333333333333333333333333333333333333333"
	trainerBaseBlob       = "sha256:4444444444444444444444444444444444444444444444444444444444444444"
	trainerBaseDiffID     = "sha256:5555555555555555555555555555555555555555555555555555555555555555"
	trainerTopBlob        = "sha256:6666666666666666666666666666666666666666666666666666666666666666"
	trainerTopDiffID      = "sha256:7777777777777777777777777777777777777777777777777777777777777777"

	// What the arm64 build of the trainer image unpacks to. It lives under the
	// same index digest as the amd64 build and shares none of its bytes, which
	// is what makes the digest alone an unsafe answer to "is this here".
	trainerArmBaseDiffID = "sha256:dddd444444444444444444444444444444444444444444444444444444444444"
	trainerArmTopDiffID  = "sha256:eeee555555555555555555555555555555555555555555555555555555555555"

	rebuiltIndexDigest    = "sha256:8888888888888888888888888888888888888888888888888888888888888888"
	rebuiltManifestDigest = "sha256:9999999999999999999999999999999999999999999999999999999999999999"
	rebuiltConfigDigest   = "sha256:aaaa111111111111111111111111111111111111111111111111111111111111"
	rebuiltTopBlob        = "sha256:bbbb222222222222222222222222222222222222222222222222222222222222"
	rebuiltTopDiffID      = "sha256:cccc333333333333333333333333333333333333333333333333333333333333"

	// What the rebuilt version adds over the base layer both versions share.
	rebuiltTopBytes = 60_000_000
)

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

// TestANodeHoldsTheImageItRan is the production half of what the corpus proves
// under simulation: running a workload is what makes a machine warm, and the
// offer catalog Placement reads says so. Nothing seeds this node; the only way
// the image reaches its inventory is by having been run.
// The identity is the digest and never the reference carrying it: a container
// daemon records what it pulled by, so a control plane comparing a whole
// reference against it would find no host warm anywhere.
func TestANodeHoldsTheImageItRan(t *testing.T) {
	fleet := startFleet(t)
	if fleet.nodeOffer(t).Images.Holds(trainerIndexDigest) {
		t.Fatal("the node reports holding an image it has never run")
	}

	runID := fleet.submitRun(t)
	fleet.completeWorkload(t, runID, 0)
	fleet.awaitOutcome(t, runID, "succeeded")

	waitFor(t, func() bool {
		return fleet.nodeOffer(t).Images.Holds(trainerIndexDigest)
	}, "the node never reported holding the image it ran, so a second Run would be priced a pull it does not owe")
}

// TestANodeThatCannotEnumerateCopiesOffersNoArtifactClaim is the Artifact half
// of the same authority, through the production daemon. No runtime in this tree
// replicates an Artifact yet, so an enrolled node has no replica store to look
// in and reports no Artifact inventory at all. What the offer must then say is
// nothing: an inventory marked enumerated from the heartbeat's own timestamp
// would publish "I hold no copy" as a fact for every machine in the fleet, and a
// Run with a start bound would be told there is no capacity for content this
// machine never looked for.
func TestANodeThatCannotEnumerateCopiesOffersNoArtifactClaim(t *testing.T) {
	fleet := startFleet(t)

	inventory := fleet.nodeOffer(t).Artifacts

	if inventory.Known {
		t.Fatalf("the offer claims this node enumerated its Artifact copies: %+v", inventory)
	}
	if len(inventory.Replicas) > 0 {
		t.Fatalf("the offer lists copies no runtime here can hold: %+v", inventory.Replicas)
	}
}

// TestANodeIsAskedToAttachTheCachesTheWorkloadDeclared is the only path a Cache
// Mount has from a Run to a real container runtime, driven end to end: the
// public API, the orchestrator's launch request, the Broker's node lane, the
// node protocol, and the agent that hands the command to its runtime. Nothing
// below the control plane can derive a cache, so a launch that arrives without
// one runs with no storage attached while the fleet keeps advertising
// cache_mounts and every decision keeps recording cache evidence: a permanently
// cold cache, and no fault reported anywhere.
//
// The workspace is asserted beside the mounts because it is half of a cache's
// identity. A command carrying the right cache under the wrong tenant is the
// leak this whole slice exists to make impossible.
func TestANodeIsAskedToAttachTheCachesTheWorkloadDeclared(t *testing.T) {
	fleet := startFleet(t)
	declared := domain.CacheMountRequirement{Name: "compiler-cache", CompatibilityKey: "cuda-12.4", SizeBytes: 8 << 30}

	runID := fleet.submitRunWithCaches(t, declared)
	fleet.completeWorkload(t, runID, 0)
	fleet.awaitOutcome(t, runID, "succeeded")

	attached := fleet.runtime.attachedCaches(runID)
	if !slices.Equal(attached, []domain.CacheMountRequirement{declared}) {
		t.Fatalf("the node was asked to attach %+v, and the workload declared %+v", attached, declared)
	}
	if workspace := fleet.runtime.launchWorkspace(runID); workspace != daemon.DefaultWorkspaceID {
		t.Fatalf("the launch reached the node under workspace %q, and the Run belongs to %q", workspace, daemon.DefaultWorkspaceID)
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
	address string
	token   string
	nodeID  string
	// image is the digest-pinned reference every fleet Run places, served by
	// this fleet's own registry. rebuiltImage is the next version of it, which
	// shares the base layer and nothing else.
	image        string
	rebuiltImage string
	runtime      *scriptedRuntime
	// agentRuntime is what the agent actually drives, which is the scripted
	// machine for every case about where a Run lands and this host's own Docker
	// daemon for the one case about what a real machine reports.
	agentRuntime nodeagent.Runtime
	heartbeat    time.Duration
	stop         context.CancelFunc
	submitted    int
}

// fleetOption changes what this fleet's one machine is before its agent starts
// reporting, so a case can state the host it needs rather than the one every
// other case happens to use.
type fleetOption func(*fleet)

// reporting is what this machine's agent establishes about its disk. A real
// agent answers from statfs on the daemon's own root, and returns no disk fact
// at all when it cannot see it.
func reporting(disk capability.DiskFacts) fleetOption {
	return func(f *fleet) { f.runtime.disk = disk }
}

// runningOn hands the agent a real container runtime instead of the scripted
// one, at a heartbeat a machine can keep up with: reading a whole daemon's
// image inventory fifty times a second is a load test of Docker rather than a
// case about Mercator.
func runningOn(runtime nodeagent.Runtime) fleetOption {
	return func(f *fleet) {
		f.agentRuntime = runtime
		f.heartbeat = 250 * time.Millisecond
	}
}

func startFleet(t *testing.T, options ...fleetOption) *fleet {
	t.Helper()
	// No Docker on PATH, so the daemon seeds no local connection and the
	// enrolled node is the only capacity in play. The point of these cases is
	// where a Run lands, not how offers are aggregated.
	t.Setenv("PATH", t.TempDir())
	registry := startTrainerRegistry(t)
	address := startRuntimeWithLease(t, 900*time.Millisecond)
	harness := &fleet{
		address:      address,
		token:        "operator-token",
		image:        registry + "/acme/trainer@" + trainerIndexDigest,
		rebuiltImage: registry + "/acme/trainer@" + rebuiltIndexDigest,
		// Running an image leaves its unpacked layers behind, named the only way
		// a container daemon can name them.
		runtime: newScriptedRuntime(map[string][]string{
			trainerIndexDigest: {trainerBaseDiffID, trainerTopDiffID},
			rebuiltIndexDigest: {trainerBaseDiffID, rebuiltTopDiffID},
		}),
		heartbeat: 20 * time.Millisecond,
	}
	harness.agentRuntime = harness.runtime
	for _, option := range options {
		option(harness)
	}
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
		f.agentRuntime,
		nodeagent.NewHTTPTransport(bootstrap.ControlPlaneURL, nil),
		state,
		nodeagent.WithHeartbeat(f.heartbeat),
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
	return f.submitRunFor(t, f.image)
}

func (f *fleet) submitRunFor(t *testing.T, image string) string {
	t.Helper()
	return f.submitWorkload(t, func(name string) map[string]any { return workloadRevision(name, image) })
}

// submitRunWithCaches submits a Run whose workload declares mutable state, which
// is the only way a Cache Mount enters Mercator at all.
func (f *fleet) submitRunWithCaches(t *testing.T, caches ...domain.CacheMountRequirement) string {
	t.Helper()
	return f.submitWorkload(t, func(name string) map[string]any {
		revision := workloadRevision(name, f.image)
		revision["spec"].(map[string]any)["caches"] = caches
		return revision
	})
}

// submitRunNeedingDisk submits a Run that states how much room it needs, which
// is a requirement rather than a preference: a machine that cannot meet it is
// struck out however warm it is.
func (f *fleet) submitRunNeedingDisk(t *testing.T, bytes int64) string {
	t.Helper()
	return f.submitWorkload(t, func(name string) map[string]any {
		revision := workloadRevision(name, f.image)
		resources := revision["spec"].(map[string]any)["resources"].(map[string]any)
		resources["ephemeral_disk"] = map[string]any{"min_bytes": bytes}
		return revision
	})
}

func (f *fleet) submitWorkload(t *testing.T, revision func(name string) map[string]any) string {
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
		"workload":     revision(name),
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
func workloadRevision(name, image string) map[string]any {
	return map[string]any{
		"id":           "wlr_" + name,
		"workspace_id": daemon.DefaultWorkspaceID,
		"workload_id":  "wl_" + name,
		"digest":       "sha256:1111111111111111111111111111111111111111111111111111111111111111",
		"spec": map[string]any{
			"containers": []map[string]any{{
				"name":     "main",
				"image":    image,
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
		OfferSnapshotID string                    `json:"offer_snapshot_id"`
		Disposition     string                    `json:"disposition"`
		ImageLocality   domain.LocalityState      `json:"image_locality"`
		Estimates       domain.CandidateEstimates `json:"estimates"`
	} `json:"candidates"`
}

func (decision bookingDecision) imageLocality() domain.LocalityState {
	for _, candidate := range decision.Candidates {
		if candidate.OfferSnapshotID == decision.SelectedOfferSnapshotID {
			return candidate.ImageLocality
		}
	}
	return ""
}

func (decision bookingDecision) disposition() string {
	for _, candidate := range decision.Candidates {
		if candidate.OfferSnapshotID == decision.SelectedOfferSnapshotID {
			return candidate.Disposition
		}
	}
	return ""
}

func (decision bookingDecision) pullEstimate() domain.Estimate {
	for _, candidate := range decision.Candidates {
		if candidate.OfferSnapshotID == decision.SelectedOfferSnapshotID {
			return candidate.Estimates.PullSeconds
		}
	}
	return domain.Estimate{}
}

// refuseToPlace drives one Run forward the way the reconcile sweep does and
// expects the daemon to answer that it found nowhere to put it.
func (f *fleet) refuseToPlace(t *testing.T, runID string) {
	t.Helper()
	f.call(t, http.MethodPost, "/v1/runs/"+runID+"/refresh?workspace_id="+daemon.DefaultWorkspaceID, nil, nil, http.StatusBadGateway)
}

// nodes is the operator's own view of the fleet, kept as the typed answer and
// as the JSON it arrived in. A number left off the wire decodes into the same
// Go zero as a number stated as zero, so a listing that omits the room a full
// machine has left is indistinguishable, in a struct, from one that says the
// machine is full.
func (f *fleet) nodes(t *testing.T) []nodeSummary {
	t.Helper()
	var response struct {
		Nodes []json.RawMessage `json:"nodes"`
	}
	f.call(t, http.MethodGet, "/v1/nodes?workspace_id="+daemon.DefaultWorkspaceID, nil, &response, http.StatusOK)
	summaries := make([]nodeSummary, 0, len(response.Nodes))
	for _, listed := range response.Nodes {
		var summary nodeSummary
		if err := json.Unmarshal(listed, &summary); err != nil {
			t.Fatalf("decode node summary %s: %v", listed, err)
		}
		if err := json.Unmarshal(listed, &summary.stated); err != nil {
			t.Fatalf("decode node summary fields %s: %v", listed, err)
		}
		summaries = append(summaries, summary)
	}
	return summaries
}

type nodeSummary struct {
	ID            string `json:"id"`
	State         string `json:"state"`
	DiskReport    string `json:"disk_report"`
	DiskFreeBytes int64  `json:"disk_free_bytes"`
	// stated is every field this summary actually carried, so a case can ask
	// whether the answer was on the wire rather than whether it decoded to a
	// zero.
	stated map[string]json.RawMessage
}

func (summary nodeSummary) states(field string) bool {
	_, stated := summary.stated[field]
	return stated
}

func (summary nodeSummary) fields() []string { return slices.Sorted(maps.Keys(summary.stated)) }

func (summary nodeSummary) String() string {
	return fmt.Sprintf("node %s state=%s disk_report=%s disk_free_bytes=%d stating %v",
		summary.ID, summary.State, summary.DiskReport, summary.DiskFreeBytes, summary.fields())
}

// summaryFor is one machine's line in the listing, found by the identity the
// operator was given for it.
func (f *fleet) summaryFor(t *testing.T, nodeID string) nodeSummary {
	t.Helper()
	listed := f.nodes(t)
	for _, summary := range listed {
		if summary.ID == nodeID {
			return summary
		}
	}
	t.Fatalf("the fleet listing has no line for node %q: %+v", nodeID, listed)
	return nodeSummary{}
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
	ID        string                   `json:"id"`
	Lane      string                   `json:"lane"`
	ExpiresAt time.Time                `json:"expires_at"`
	Resources domain.ResourceInventory `json:"resources"`
	Images    domain.ImageInventory    `json:"images"`
	Artifacts domain.ArtifactInventory `json:"artifacts"`
}

func (f *fleet) offers(t *testing.T) []offerSnapshot {
	t.Helper()
	var response struct {
		Offers []offerSnapshot `json:"offers"`
	}
	f.call(t, http.MethodGet, "/v1/offers?workspace_id="+daemon.DefaultWorkspaceID, nil, &response, http.StatusOK)
	return response.Offers
}

// nodeOffer is the enrolled node as Placement sees it, through the same catalog
// the scheduler reads.
func (f *fleet) nodeOffer(t *testing.T) offerSnapshot {
	t.Helper()
	for _, offer := range f.offers(t) {
		if offer.ID == f.nodeID {
			return offer
		}
	}
	t.Fatalf("the enrolled node %q is not being offered", f.nodeID)
	return offerSnapshot{}
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
	mu       sync.Mutex
	launched []string
	// held is every image manifest this machine holds, which is what running a
	// workload leaves behind and what the node reports on its next heartbeat.
	held []string
	// unpacks is what this machine ends up with for each image it runs: the
	// uncompressed layer identities its runtime unpacked, which is the only
	// vocabulary a container daemon has. It can never name the compressed blobs
	// the registry served.
	unpacks map[string][]string
	// platforms is which build of each digest this machine holds. Anything it
	// ran is a build it can run; anything an operator fetched by hand may not
	// be, which is why this is stated per image rather than assumed from the
	// host.
	platforms map[string]domain.Platform
	// unassembled is every image whose content is here and whose layer chain
	// this runtime never built, which it reports as held and not runnable.
	unassembled []string
	// undescribed is every image this runtime listed and could not account for,
	// which is what a daemon that will not describe an image leaves behind.
	undescribed  []string
	observations map[string]capability.WorkloadObservation
	// launches is the command each Run arrived with, kept whole so a case can
	// ask what this machine was actually told to attach and under whose
	// workspace. Everything a container runtime mounts has to be in there:
	// nothing below the control plane can derive a cache.
	launches map[string]capability.LaunchWorkloadCommand
	// disk is what this machine's agent established about the filesystem its
	// daemon keeps content on. A runtime that could only ever answer one way
	// leaves the two cases an operator most needs told apart, a full machine
	// and an unmeasurable one, unreachable from any fixture.
	disk capability.DiskFacts
}

func newScriptedRuntime(unpacks map[string][]string) *scriptedRuntime {
	return &scriptedRuntime{
		unpacks:      unpacks,
		platforms:    map[string]domain.Platform{},
		observations: map[string]capability.WorkloadObservation{},
		launches:     map[string]capability.LaunchWorkloadCommand{},
		disk:         capability.DiskFacts{Known: true, TotalBytes: 500 << 30, FreeBytes: 400 << 30},
	}
}

// hold puts an image on this machine without Mercator having run it, which is
// what `docker pull` on the host does.
func (runtime *scriptedRuntime) hold(digest string, platform domain.Platform, diffIDs []string) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.held = append(runtime.held, digest)
	runtime.platforms[digest] = platform
	runtime.unpacks[digest] = diffIDs
}

// holdUnassembled puts an image on this machine that arrived and was never
// unpacked: every byte is here and no container can be started on it. That is
// where a container runtime sits whenever content has landed and the snapshot
// chain has not been built, and it is the state this node used to report as hot.
// The node says partial for it, and so does the decision: one vocabulary, one
// meaning, on both surfaces an operator can read.
func (runtime *scriptedRuntime) holdUnassembled(digest string, platform domain.Platform) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.held = append(runtime.held, digest)
	runtime.platforms[digest] = platform
	runtime.unassembled = append(runtime.unassembled, digest)
}

// holdUndescribed puts an image on this machine that its runtime cannot account
// for: the daemon lists it and will not say what it is, or reports part of its
// content present and can name none of it. The node says so rather than leaving
// the image out of a report that enumerated everything else, because leaving it
// out is a confident claim that none of it is here.
func (runtime *scriptedRuntime) holdUndescribed(digest string) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.held = append(runtime.held, digest)
	runtime.undescribed = append(runtime.undescribed, digest)
}

// platformOf is the build this machine holds one image as: what it was told
// when the image was placed there by hand, and the host's own build for
// anything it ran itself.
func (runtime *scriptedRuntime) platformOf(digest string) domain.Platform {
	if platform, stated := runtime.platforms[digest]; stated {
		return platform
	}
	return domain.Platform{OS: "linux", Architecture: "amd64"}
}

// Facts reports what this machine holds now, which is nothing until it has run
// something or an operator put something there. A runtime that answers with a
// fixed inventory could never show a node becoming warm by running a workload,
// which is the whole claim.
func (runtime *scriptedRuntime) Facts(context.Context) (capability.NodeFacts, error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	images := make([]capability.ImageLocality, 0, len(runtime.held))
	for _, digest := range runtime.held {
		if slices.Contains(runtime.undescribed, digest) {
			images = append(images, capability.ImageLocality{
				ManifestDigest: digest,
				State:          domain.LocalityUnknown,
			})
			continue
		}
		if slices.Contains(runtime.unassembled, digest) {
			images = append(images, capability.ImageLocality{
				ManifestDigest: digest,
				Platform:       runtime.platformOf(digest),
				ContentPresent: true,
				State:          domain.LocalityPartial,
			})
			continue
		}
		images = append(images, capability.ImageLocality{
			ManifestDigest: digest,
			Platform:       runtime.platformOf(digest),
			LayerDiffIDs:   runtime.unpacks[digest],
			ContentPresent: true,
			State:          domain.LocalityHot,
		})
	}
	return capability.NodeFacts{
		ObservedAt: time.Now().UTC(),
		Host: capability.HostFacts{
			OS:               "linux",
			Architecture:     "amd64",
			ContainerRuntime: "docker",
			RuntimeVersion:   "27.0.0",
			CPUMillis:        8000,
			MemoryBytes:      32 << 30,
			Disk:             runtime.disk,
		},
		Images: images,
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
	runtime.launches[command.RunID] = command
	if !slices.Contains(runtime.held, command.ManifestDigest) {
		runtime.held = append(runtime.held, command.ManifestDigest)
	}
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

// attachedCaches is the mutable state this machine was asked to mount for one
// Run, which is what a real runtime would open a volume per.
func (runtime *scriptedRuntime) attachedCaches(runID string) []domain.CacheMountRequirement {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.launches[runID].CacheMounts
}

// launchWorkspace is the tenant the command reached this node under, which is
// the other half of every cache identity it would derive.
func (runtime *scriptedRuntime) launchWorkspace(runID string) string {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.launches[runID].WorkspaceID
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

// startTrainerRegistry serves both versions of the image over the registry v2
// protocol on loopback: a token challenge, an index per version, the platform
// manifest under each, and the config blob that names the uncompressed layers.
// It is what makes the daemon's manifest resolution exercisable without
// reaching the network.
func startTrainerRegistry(t *testing.T) string {
	t.Helper()
	var realm string
	documents := map[string]string{
		trainerIndexDigest: `{"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[
			{"digest":"` + trainerManifestDigest + `","platform":{"os":"linux","architecture":"amd64"}}
		]}`,
		trainerManifestDigest: `{"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"` + trainerConfigDigest + `"},"layers":[
			{"digest":"` + trainerBaseBlob + `","size":18000000000},
			{"digest":"` + trainerTopBlob + `","size":40000000}
		]}`,
		trainerConfigDigest: `{"rootfs":{"type":"layers","diff_ids":["` + trainerBaseDiffID + `","` + trainerTopDiffID + `"]}}`,
		rebuiltIndexDigest: `{"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[
			{"digest":"` + rebuiltManifestDigest + `","platform":{"os":"linux","architecture":"amd64"}}
		]}`,
		rebuiltManifestDigest: `{"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"` + rebuiltConfigDigest + `"},"layers":[
			{"digest":"` + trainerBaseBlob + `","size":18000000000},
			{"digest":"` + rebuiltTopBlob + `","size":60000000}
		]}`,
		rebuiltConfigDigest: `{"rootfs":{"type":"layers","diff_ids":["` + trainerBaseDiffID + `","` + rebuiltTopDiffID + `"]}}`,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "anonymous"})
	})
	mux.HandleFunc("/v2/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			w.Header().Set("Www-Authenticate", `Bearer realm="`+realm+`",service="fleet",scope="repository:acme/trainer:pull"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, digest, _ := strings.Cut(r.URL.Path, "sha256:")
		document, ok := documents["sha256:"+digest]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(document))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	realm = server.URL + "/token"
	return strings.TrimPrefix(server.URL, "http://")
}

// TestPlacementPricesAWarmNodeFromTheResolvedManifest is what the manifest
// resolver exists for, driven through the production daemon, on the case where
// nothing else could answer. The node has run one version of the image and is
// asked to run the next, so it holds most of what the new one needs and does
// not hold the new image at all: the whole-image shortcut cannot fire, and the
// only way to know the host is warm is to recognise the compressed blob digests
// in the registry manifest as the uncompressed diff IDs the daemon unpacked.
// Without the bridge the decision reads "the host holds nothing" and prices a
// fresh 18GB pull.
func TestPlacementPricesAWarmNodeFromTheResolvedManifest(t *testing.T) {
	fleet := startFleet(t)
	first := fleet.submitRun(t)
	fleet.completeWorkload(t, first, 0)
	fleet.awaitOutcome(t, first, "succeeded")
	waitFor(t, func() bool {
		return len(fleet.nodeOffer(t).Images.LayerDiffIDs) == 2
	}, "the node never reported the layers it unpacked")

	rebuilt := fleet.submitRunFor(t, fleet.rebuiltImage)
	fleet.completeWorkload(t, rebuilt, 0)
	fleet.awaitOutcome(t, rebuilt, "succeeded")

	pull := fleet.decision(t, rebuilt).pullEstimate()
	if pull.Source != "image_inventory" {
		t.Fatalf("pull estimate = %+v, want an answer sourced from the host's own inventory", pull)
	}
	// Only the layer this version added crosses the network. The seconds it
	// takes are worth less than the bytes are: nothing has measured this host's
	// link to a registry.
	want := float64(rebuiltTopBytes*8) / 1_000_000 / domain.DefaultRegistryDownloadMbps
	if pull.Expected < want || pull.Expected > want+1 {
		t.Fatalf("pull expected = %v seconds, want about %v: only the rebuilt layer is missing", pull.Expected, want)
	}
	if pull.Confidence != domain.AssumedLinkConfidence {
		t.Fatalf("pull confidence = %v, want %v: the bytes are counted and the link they cross is assumed",
			pull.Confidence, domain.AssumedLinkConfidence)
	}
}

// TestPlacementChargesNothingForAnImageTheNodeAlreadyHolds is the other half:
// a node that ran an image is charged nothing to run it again, at full
// confidence, because no link speed enters an answer about content that does
// not move. It is the end-to-end statement and not the proof of digest
// identity: this host holds every layer as well as the image, so the layer
// subtraction would reach zero too. What holds the two digest spaces to one
// string is TestResolverStatesEveryLayerInBothDigestSpaces on the registry
// side, TestANodeHoldsTheImageItRan on the machine side, and the Docker
// conformance cases against each.
func TestPlacementChargesNothingForAnImageTheNodeAlreadyHolds(t *testing.T) {
	fleet := startFleet(t)
	first := fleet.submitRun(t)
	fleet.completeWorkload(t, first, 0)
	fleet.awaitOutcome(t, first, "succeeded")
	waitFor(t, func() bool {
		return fleet.nodeOffer(t).Images.Holds(trainerIndexDigest)
	}, "the node never reported holding the image it ran")

	second := fleet.submitRun(t)
	fleet.completeWorkload(t, second, 0)
	fleet.awaitOutcome(t, second, "succeeded")

	pull := fleet.decision(t, second).pullEstimate()
	if pull.Source != "image_inventory" || pull.Confidence != 1 || pull.Expected != 0 {
		t.Fatalf("pull estimate = %+v, want nothing to fetch and no doubt about it", pull)
	}
}

// TestPlacementChargesAssemblyForAnImageTheNodeHasNotUnpacked is the node half
// of "unpacked is not the same as pulled", driven through the production daemon
// against a real registry. Every byte of the image is on this machine and no
// container can start on it, which is exactly what the node used to report as
// hot and unpacked for anything it could list. The decision now records partial
// and charges the local assembly that is left, at less than full confidence,
// rather than either an instant start or a pull of content already on the disk.
func TestPlacementChargesAssemblyForAnImageTheNodeHasNotUnpacked(t *testing.T) {
	fleet := startFleet(t)
	fleet.runtime.holdUnassembled(trainerIndexDigest, domain.Platform{OS: "linux", Architecture: "amd64"})
	waitFor(t, func() bool {
		return fleet.nodeOffer(t).Images.Pulled(trainerIndexDigest)
	}, "the node never reported the image it fetched and never assembled")
	if fleet.nodeOffer(t).Images.Holds(trainerIndexDigest) {
		t.Fatal("the node reports being able to run an image whose layer chain it never built")
	}

	runID := fleet.submitRun(t)
	fleet.completeWorkload(t, runID, 0)
	fleet.awaitOutcome(t, runID, "succeeded")

	decision := fleet.decision(t, runID)
	if decision.imageLocality() != domain.LocalityPartial {
		t.Fatalf("image locality = %q, want partial: every byte is here and none of it is ready", decision.imageLocality())
	}
	pull := decision.pullEstimate()
	want := float64(18_000_000_000+40_000_000) / 1_000_000 / domain.AssumedUnpackMBps
	if pull.Expected < want || pull.Expected > want+1 {
		t.Fatalf("pull expected = %v seconds, want about %v: the bytes are here and the chain is not", pull.Expected, want)
	}
	if pull.Confidence != domain.AssumedLinkConfidence {
		t.Fatalf("pull confidence = %v, want %v: nothing has measured how fast this machine unpacks",
			pull.Confidence, domain.AssumedLinkConfidence)
	}
}

// TestPlacementRecordsWhatANodeCouldNotSayAsSilence is the last place a node's
// own uncertainty could be laundered into a fact. An enrolled node enumerates,
// so an image missing from every list in its inventory reads as "none of it is
// here" and the decision states it as an inventory measurement at the full
// confidence of a machine whose link is known. This node held an image it could
// not describe: the decision records the silence, with the source that names
// whose silence it was, and prices it as the fetch it may well be.
func TestPlacementRecordsWhatANodeCouldNotSayAsSilence(t *testing.T) {
	fleet := startFleet(t)
	fleet.runtime.holdUndescribed(trainerIndexDigest)
	waitFor(t, func() bool {
		return fleet.nodeOffer(t).Images.Undescribed(trainerIndexDigest)
	}, "the node never reported the image it could not account for")

	runID := fleet.submitRun(t)
	fleet.completeWorkload(t, runID, 0)
	fleet.awaitOutcome(t, runID, "succeeded")

	decision := fleet.decision(t, runID)
	if decision.imageLocality() != domain.LocalityUnknown {
		t.Fatalf("image locality = %q, want unknown: this machine looked and could not answer", decision.imageLocality())
	}
	pull := decision.pullEstimate()
	if pull.Source != "image_undescribed" {
		t.Fatalf("pull source = %q, want the silence to name itself: the node answered about every other image", pull.Source)
	}
	if pull.Confidence > domain.AssumedLinkConfidence {
		t.Fatalf("pull confidence = %v, want no more than %v for an answer nobody established",
			pull.Confidence, domain.AssumedLinkConfidence)
	}
}

// TestPlacementChargesTheWholePullForAnotherPlatformsBuild is where holding an
// image whole stops being a question about a name. An index digest names one
// image per platform, so an operator who pulled the arm64 build by hand leaves
// this amd64 machine reporting exactly the digest an amd64 Run is pinned to,
// holding none of the bytes that Run needs. Reading the digest alone priced an
// 18GB fetch as nothing to do, at full confidence.
func TestPlacementChargesTheWholePullForAnotherPlatformsBuild(t *testing.T) {
	fleet := startFleet(t)
	fleet.runtime.hold(trainerIndexDigest, domain.Platform{OS: "linux", Architecture: "arm64"},
		[]string{trainerArmBaseDiffID, trainerArmTopDiffID})
	waitFor(t, func() bool {
		return slices.Contains(fleet.nodeOffer(t).Images.LayerDiffIDs, trainerArmBaseDiffID)
	}, "the node never reported the build an operator put on it")
	if fleet.nodeOffer(t).Images.Holds(trainerIndexDigest) {
		t.Fatal("the node reports holding an image whole on the strength of a name it shares with another platform's build")
	}

	runID := fleet.submitRun(t)
	fleet.completeWorkload(t, runID, 0)
	fleet.awaitOutcome(t, runID, "succeeded")

	pull := fleet.decision(t, runID).pullEstimate()
	want := float64((18_000_000_000+40_000_000)*8) / 1_000_000 / domain.DefaultRegistryDownloadMbps
	if pull.Expected < want || pull.Expected > want+1 {
		t.Fatalf("pull expected = %v seconds, want about %v: none of the amd64 build is here", pull.Expected, want)
	}
	if pull.Confidence != domain.AssumedLinkConfidence {
		t.Fatalf("pull confidence = %v, want %v", pull.Confidence, domain.AssumedLinkConfidence)
	}
}

// TestANodeOffersTheDiskItsHostReported is the seam the whole disk requirement
// runs through. The disk a host reports was declared and never populated, and
// the offer projection maps it straight onto the resource a workload's disk
// minimum is compared against, so every enrolled node advertised zero bytes and
// every Run declaring any disk at all was refused on all of them. The node
// runtime fills the fact in now; this holds the projection that carries it.
func TestANodeOffersTheDiskItsHostReported(t *testing.T) {
	fleet := startFleet(t)

	offered := fleet.nodeOffer(t).Resources.EphemeralDiskBytes

	if offered != 400<<30 {
		t.Fatalf("the node offered %d bytes of ephemeral disk, want the 400GiB free its host reported", offered)
	}
}

// TestTheNodeListingReportsTheRoomAMeasuredNodeHasLeft is what an operator has
// to be able to read. A node that offers no room wins no placement that declares
// a floor, which every Run does, so a fleet listing that showed only "ready"
// would leave a working machine looking idle for no stated reason.
func TestTheNodeListingReportsTheRoomAMeasuredNodeHasLeft(t *testing.T) {
	fleet := startFleet(t)

	listed := fleet.nodes(t)

	if len(listed) != 1 {
		t.Fatalf("the fleet listed %d nodes, want the one enrolled: %+v", len(listed), listed)
	}
	if listed[0].DiskReport != "measured" {
		t.Fatalf("the node measured its disk and the listing says %q: %+v", listed[0].DiskReport, listed[0])
	}
	if listed[0].DiskFreeBytes != 400<<30 {
		t.Fatalf("the listing reports %d bytes free, and its host reported 400GiB", listed[0].DiskFreeBytes)
	}
}

// TestTheNodeListingStatesTheRoomOnAMachineThatIsFull is the one value of the
// number this field exists for. A machine with nothing left is the case an
// operator most needs to find, and it is the value that disappears from a JSON
// document the moment the field is written as optional: a reader then gets the
// same answer for "this disk is full" and "this server said nothing about
// room", and the two send them to different places.
func TestTheNodeListingStatesTheRoomOnAMachineThatIsFull(t *testing.T) {
	fleet := startFleet(t, reporting(capability.DiskFacts{Known: true, TotalBytes: 500 << 30, FreeBytes: 0}))

	summary := fleet.summaryFor(t, fleet.nodeID)

	if summary.DiskReport != "measured" {
		t.Fatalf("a full machine measured its disk and the listing says %q: %+v", summary.DiskReport, summary)
	}
	if !summary.states("disk_free_bytes") {
		t.Fatalf("the listing left the room off the wire for the machine that has none, stating only %v", summary.fields())
	}
	if summary.DiskFreeBytes != 0 {
		t.Fatalf("the listing reports %d bytes free on a machine with none", summary.DiskFreeBytes)
	}
}

// TestTheNodeListingTellsAnUnmeasurableDiskFromANodeNobodyHasHeardFrom is the
// third answer a boolean cannot carry. A node that reported and could not
// measure has a daemon its agent cannot see, and an identity that was invited
// and never enrolled has had nothing asked of it at all: sending the second
// operator after a daemon states a fact about a machine Mercator has never
// heard from. Both machines offer no room, so the room alone cannot tell them
// apart either.
func TestTheNodeListingTellsAnUnmeasurableDiskFromANodeNobodyHasHeardFrom(t *testing.T) {
	fleet := startFleet(t, reporting(capability.DiskFacts{}))
	silent := fleet.invite(t)

	unmeasurable := fleet.summaryFor(t, fleet.nodeID)
	unheard := fleet.summaryFor(t, silent.NodeID)

	if unmeasurable.DiskReport != "unmeasurable" {
		t.Fatalf("a node whose agent cannot see its daemon is listed %q: %+v", unmeasurable.DiskReport, unmeasurable)
	}
	if unmeasurable.DiskFreeBytes != 0 {
		t.Fatalf("a node that measured nothing offers %d bytes of room", unmeasurable.DiskFreeBytes)
	}
	if unheard.DiskReport != "never_reported" {
		t.Fatalf("an identity nobody has heard from is listed %q, which is a claim about a daemon: %+v",
			unheard.DiskReport, unheard)
	}
	if unheard.State != "enrolling" {
		t.Fatalf("the invited identity is %q, want it still enrolling", unheard.State)
	}
}

// TestANodeThatCannotMeasureItsDiskWinsNoPlacement is what the listing exists to
// explain, driven end to end. The machine is enrolled, alive, and reporting its
// containers, and every Run declares a disk floor, so a machine that established
// no room is struck out of all of them. An operator reading only "ready" would
// see a healthy node that never runs anything.
func TestANodeThatCannotMeasureItsDiskWinsNoPlacement(t *testing.T) {
	fleet := startFleet(t, reporting(capability.DiskFacts{}))

	if offered := fleet.nodeOffer(t).Resources.EphemeralDiskBytes; offered != 0 {
		t.Fatalf("a node that could not measure its disk offered %d bytes of room", offered)
	}
	runID := fleet.submitRun(t)
	fleet.refuseToPlace(t, runID)

	if launched := fleet.runtime.launchedRuns(); slices.Contains(launched, runID) {
		t.Fatalf("a Run was sent to a machine whose room nobody established: %v", launched)
	}
}

// TestARunPlacesOnANodeWithRoomForItAndNotOnOneWithout is the observable half
// of the same bug, end to end through the public API. One Run asks for less
// disk than the node has and lands on it, which before this commit no Run
// declaring any disk at all could do. One asks for more than the machine has,
// and the daemon answers that it found nowhere to put it and never asks the node
// to run it.
//
// The refusal is read from the daemon's answer rather than from a recorded
// rejection because a Run that finds no feasible offer records no Booking
// Decision at all today, which is its own gap in the explanation record. What
// the refusal names is asserted where a decision exists: the Blueprint
// a-host-that-cannot-hold-the-data-is-not-warm holds RESOURCE_INSUFFICIENT
// against the disk the Run asked for.
func TestARunPlacesOnANodeWithRoomForItAndNotOnOneWithout(t *testing.T) {
	fleet := startFleet(t)

	fits := fleet.submitRunNeedingDisk(t, 100<<30)
	fleet.completeWorkload(t, fits, 0)
	fleet.awaitOutcome(t, fits, "succeeded")
	oversized := fleet.submitRunNeedingDisk(t, 900<<30)
	fleet.refuseToPlace(t, oversized)

	if selected := fleet.decision(t, fits).SelectedOfferSnapshotID; selected != fleet.nodeID {
		t.Fatalf("a Run needing 100GiB landed on %q, and the node has 400GiB free", selected)
	}
	if launched := fleet.runtime.launchedRuns(); slices.Contains(launched, oversized) {
		t.Fatalf("a Run needing 900GiB was sent to a machine with 400GiB free: %v", launched)
	}
}

// TestTheFleetListingReportsTheRoomThisMachineReallyHas is the whole chain
// against a real container daemon: this host's Docker names the filesystem it
// keeps content on, the production agent measures it, the node protocol carries
// it, the registry stores it, and an operator reads it over the public API. Every
// case above scripts the machine, so all of them would stay green with the
// measurement dropped anywhere between the kernel and the listing, and the number
// an operator acts on is worth exactly what that path is.
//
// It is stated as a range because free disk moves under a working machine. What
// is being held is the room this filesystem has, not the instant it was read.
func TestTheFleetListingReportsTheRoomThisMachineReallyHas(t *testing.T) {
	docker := requireDockerBinary(t)
	fleet := startFleet(t, runningOn(nodeagent.NewDockerRuntime(docker)))

	summary := fleet.summaryFor(t, fleet.nodeID)

	if summary.DiskReport != "measured" {
		t.Fatalf("a node on this machine's own Docker daemon is listed %q: %+v", summary.DiskReport, summary)
	}
	total, free := dockerRootFilesystem(t, docker)
	if summary.DiskFreeBytes <= 0 || summary.DiskFreeBytes > total {
		t.Fatalf("the listing reports %d bytes free on a %d byte filesystem", summary.DiskFreeBytes, total)
	}
	if drift := summary.DiskFreeBytes - free; drift > total/1000 || drift < -total/1000 {
		t.Fatalf("the listing reports %d bytes free, and the daemon's own filesystem has %d", summary.DiskFreeBytes, free)
	}
}

// requireDockerBinary resolves Docker before the fleet clears PATH, and skips
// where there is no daemon to answer.
func requireDockerBinary(t *testing.T) string {
	t.Helper()
	binary, err := exec.LookPath("docker")
	if err != nil {
		t.Skipf("no Docker client on this machine: %v", err)
	}
	if output, err := exec.Command(binary, "info").CombinedOutput(); err != nil {
		t.Skipf("no reachable Docker daemon to report a node's disk from: %v\n%s", err, output)
	}
	return binary
}

// dockerRootFilesystem is the independent answer: the daemon says which
// directory it keeps content in, and the kernel says how big that filesystem is
// and how much of it a workload of this node's could still use.
func dockerRootFilesystem(t *testing.T, docker string) (total, free int64) {
	t.Helper()
	output, err := exec.Command(docker, "info", "--format", "{{.DockerRootDir}}").Output()
	if err != nil {
		t.Fatalf("ask the daemon where it keeps its content: %v", err)
	}
	root := strings.TrimSpace(string(output))
	var filesystem syscall.Statfs_t
	if err := syscall.Statfs(root, &filesystem); err != nil {
		t.Fatalf("measure the filesystem holding %s: %v", root, err)
	}
	block := int64(filesystem.Bsize)
	return int64(filesystem.Blocks) * block, int64(filesystem.Bavail) * block
}
