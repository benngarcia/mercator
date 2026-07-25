package daemon_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
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
	stop         context.CancelFunc
	submitted    int
}

func startFleet(t *testing.T) *fleet {
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
	return f.submitRunFor(t, f.image)
}

func (f *fleet) submitRunFor(t *testing.T, image string) string {
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
		"workload":     workloadRevision(name, image),
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
		Estimates       domain.CandidateEstimates `json:"estimates"`
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

func (decision bookingDecision) pullEstimate() domain.Estimate {
	for _, candidate := range decision.Candidates {
		if candidate.OfferSnapshotID == decision.SelectedOfferSnapshotID {
			return candidate.Estimates.PullSeconds
		}
	}
	return domain.Estimate{}
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
	ID        string                `json:"id"`
	Lane      string                `json:"lane"`
	ExpiresAt time.Time             `json:"expires_at"`
	Images    domain.ImageInventory `json:"images"`
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
	unpacks      map[string][]string
	observations map[string]capability.WorkloadObservation
}

func newScriptedRuntime(unpacks map[string][]string) *scriptedRuntime {
	return &scriptedRuntime{unpacks: unpacks, observations: map[string]capability.WorkloadObservation{}}
}

// Facts reports what this machine holds now, which is nothing until it has run
// something. A runtime that answers with a fixed inventory could never show a
// node becoming warm by running a workload, which is the whole claim.
func (runtime *scriptedRuntime) Facts(context.Context) (capability.NodeFacts, error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	images := make([]capability.ImageLocality, 0, len(runtime.held))
	for _, digest := range runtime.held {
		images = append(images, capability.ImageLocality{
			ManifestDigest: digest,
			LayerDiffIDs:   runtime.unpacks[digest],
			State:          capability.LocalityHot,
			Unpacked:       true,
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
			DiskTotalBytes:   500 << 30,
			DiskFreeBytes:    400 << 30,
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
// the host holds the image whole, so the answer is zero seconds and certain,
// because no link speed enters an answer about content that does not move. It
// only holds if the digest the registry names an image by and the digest a node
// reports having pulled are the same string.
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
