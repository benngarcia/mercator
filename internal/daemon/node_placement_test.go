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
	"github.com/benngarcia/mercator/internal/orchestrator"
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

// TestANodeStillRunningPastItsBoundIsNotQueuedBehind is the overrun rule against
// the production daemon, and this is the world that makes the rule real: nothing
// terminates a workload at its enforced maximum, so a container that does not
// exit holds its node while the Booking's remaining runtime reads zero. The
// corpus states such a world by declaring it; here the machine is genuinely still
// running the first Run when the second arrives, through the real API, the real
// storage, and the real node protocol.
//
// A zero remainder is what an idle Rental reports, so the daemon read this node
// as free this instant, queued the arriving Run behind a Booking with nothing
// left to project from, and promised it a start it had already missed. What the
// arriving Run gets instead is the rest of the fleet, and this is the assertion
// the rule needs: an answer of 502 is what an internal placement failure produces
// too, and that is exactly what the domain refusal alone would leave. Placement
// has to reach a decision, refuse the busy machine on its own capacity evidence,
// record the overrun beside the remainder that ran out, and put the Run on the
// expensive cold machine next to it. That is the same sentence
// an-overrun-booking-is-not-an-empty-queue states at L0.
func TestANodeStillRunningPastItsBoundIsNotQueuedBehind(t *testing.T) {
	fleet := startFleet(t)
	spare := fleet.enrollAnother(t, 9.00)

	stuck := fleet.submitWorkload(t, func(name string) map[string]any {
		revision := workloadRevision(name, fleet.image)
		spec := revision["spec"].(map[string]any)
		spec["placement"].(map[string]any)["expected_runtime_seconds"] = 1
		spec["execution"].(map[string]any)["max_runtime_seconds"] = 1
		return revision
	})
	// The runtime Mercator enforces is a bound on a container, so its clock starts
	// when the machine says there is one running. The daemon has to have heard
	// that before the Booking has a bound to be past, which is what waiting for
	// the machine to report itself occupied establishes.
	fleet.runtime.awaitLaunch(t, stuck)
	fleet.awaitOccupied(t, fleet.nodeID)
	fleet.advance(t, stuck)
	// There is no virtual time here: the daemon reads the wall clock, the enforced
	// second has to pass, and this container never exits.
	time.Sleep(1500 * time.Millisecond)

	arriving := fleet.submitRun(t)
	fleet.advance(t, arriving)

	decision := fleet.decision(t, arriving)
	busy := decision.candidate(t, fleet.nodeID)
	if busy.Feasible {
		t.Fatal("the machine still running work past the runtime Mercator enforces was offered as feasible capacity")
	}
	if !refusedAs(busy, "CAPACITY_UNAVAILABLE") {
		t.Fatalf("the busy machine was refused as %+v, want its own capacity evidence", busy.Rejections)
	}
	assertOverrunRecorded(t, busy)
	if decision.SelectedOfferSnapshotID != spare.nodeID {
		t.Fatalf("the Run landed on %q, want the rest of the fleet at %q", decision.SelectedOfferSnapshotID, spare.nodeID)
	}
	if launched := fleet.runtime.launchedRuns(); slices.Contains(launched, arriving) {
		t.Fatalf("a Run was sent to a node still running work past the runtime Mercator enforces: %v", launched)
	}
	spare.runtime.awaitLaunch(t, arriving)
}

// assertOverrunRecorded reads the schedule the decision refused this candidate
// on. The remainder that ran out is the number that made a busy machine look
// idle, and the overrun beside it is what tells an operator which of the two a
// zero is.
func assertOverrunRecorded(t *testing.T, candidate domain.CandidateDecision) {
	t.Helper()
	evidence := candidate.RentalSchedule
	if evidence == nil || evidence.Running == nil {
		t.Fatalf("the decision refused this machine and recorded no schedule saying why: %+v", evidence)
	}
	if evidence.Running.RemainingMaxRuntimeSeconds != 0 {
		t.Fatalf("the record says the Booking has %.2fs of enforced runtime left, and it ran out",
			evidence.Running.RemainingMaxRuntimeSeconds)
	}
	if evidence.Running.OverrunSeconds <= 0 {
		t.Fatalf("the record says the Booking has overrun nothing, and its container is still going past its bound: %+v",
			evidence.Running)
	}
}

func refusedAs(candidate domain.CandidateDecision, code string) bool {
	return slices.ContainsFunc(candidate.Rejections, func(rejection domain.Violation) bool {
		return rejection.Code == code
	})
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

// TestANodeReportsTheMomentItsContainerReallyStarted is the only path a start
// moment has from a container runtime to Mercator's record, driven end to end: the
// node protocol, the Broker's node lane, the orchestrator's observation, and the
// Run read model an operator sees. This machine says its container began ninety
// seconds before the heartbeat that reported it, and that is the moment the Run
// carries; a control plane stamping the moment it looked, or the moment it accepted
// the launch, would record something within milliseconds of either.
//
// It is the end-to-end case because broker.observeOnNode is the whole seam, and
// this is the fourth defect found at it: node.Registry.offer dropped Artifacts,
// NodeFacts.Artifacts manufactured Known, and launchOnNode dropped cache mounts,
// each of them invisible to every test in the tree.
func TestANodeReportsTheMomentItsContainerReallyStarted(t *testing.T) {
	fleet := startFleet(t)

	runID := fleet.submitRun(t)
	fleet.awaitOccupied(t, fleet.nodeID)
	started := fleet.awaitStartMoment(t, runID)

	reported := fleet.runtime.reportedStart(t, runID)
	if !started.Equal(reported) {
		t.Fatalf("the Run records a start of %s and its node's runtime reported %s",
			started.Format(time.RFC3339Nano), reported.Format(time.RFC3339Nano))
	}
	if elapsed := time.Since(started); elapsed < scriptedStartDelay {
		t.Fatalf("the recorded start is %s old and this machine said its container began %s before the report",
			elapsed, scriptedStartDelay)
	}
}

// TestANodeWithASkewedClockDoesNotSetMercatorsOwn is the same seam under the one
// world it could not survive: a machine whose wall clock runs an hour ahead. Its
// runtime reads its container's start and its own observation moment off that
// clock, so the two agree with each other, and the Broker used to copy both into
// the observation. The start rule then compared a foreign clock against itself,
// found the start no later than the read, and Mercator adopted a moment an hour in
// its own future twice over: once as the Run's start, and once as the clock this
// Booking's enforced runtime is measured from.
//
// The consequence this case asserts is the expensive one. The workload declares a
// one second bound and does not exit. A Booking measured from the machine's own
// clock has an hour of enforced runtime left, so the daemon reads a machine that is
// still running work as having capacity to spare, and the arriving Run is queued
// behind a container that will never finish. Measured from the moment Mercator
// received the report, the bound is past, the overrun is recorded, and the Run goes
// to the rest of the fleet.
//
// The Run's own start is absent, and that is the honest record: nothing observed
// this container start on a clock Mercator shares, so the stage has no actual
// rather than an hour of invented start latency.
func TestANodeWithASkewedClockDoesNotSetMercatorsOwn(t *testing.T) {
	fleet := startFleet(t, keepingAClockAhead(time.Hour))
	spare := fleet.enrollAnother(t, 9.00)

	stuck := fleet.submitWorkload(t, func(name string) map[string]any {
		revision := workloadRevision(name, fleet.image)
		spec := revision["spec"].(map[string]any)
		spec["placement"].(map[string]any)["expected_runtime_seconds"] = 1
		spec["execution"].(map[string]any)["max_runtime_seconds"] = 1
		return revision
	})
	fleet.runtime.awaitLaunch(t, stuck)
	fleet.awaitOccupied(t, fleet.nodeID)
	fleet.advance(t, stuck)
	time.Sleep(1500 * time.Millisecond)

	arriving := fleet.submitRun(t)
	fleet.advance(t, arriving)

	if started := fleet.startMoment(t, stuck); started != nil {
		t.Fatalf("the Run records a start of %s, and its machine's clock is an hour ahead of the control plane's",
			started.Format(time.RFC3339Nano))
	}
	decision := fleet.decision(t, arriving)
	assertOverrunRecorded(t, decision.candidate(t, fleet.nodeID))
	if decision.SelectedOfferSnapshotID != spare.nodeID {
		t.Fatalf("the Run landed on %q, want the rest of the fleet at %q", decision.SelectedOfferSnapshotID, spare.nodeID)
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
	fleet := startFleet(t, leasedFor(900*time.Millisecond))
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
	// control is this daemon itself, which is how a case drives the sweep that
	// prepares capacity. Preparation answers no request.
	control *daemon.Runtime
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
	// lease is how long this daemon keeps trusting a node it has stopped hearing
	// from, and a third of it is how long one of its offers stays selectable. It
	// is generous by default because a stalled heartbeat is a fact of a loaded
	// machine rather than a claim any case here makes: a fleet whose only machine
	// went stale mid-case is one where a Run cannot be placed at all, so a short
	// lease turns every case into a bet on how busy the host running the suite
	// is. The one case about a machine going quiet states the lease it needs.
	lease time.Duration
	// soldOn is what an operator states this fleet's machine is bought on beyond its
	// price: the block of time it is billed in, the classes it is held for, and the
	// moment it stops being Mercator's. Empty is a machine bought in no increments,
	// held for nobody in particular, with no window, which is what every case here
	// enrolled before an operator could say otherwise.
	soldOn map[string]any
	// prewarm is what this fleet's control plane is allowed to have in flight for
	// work it has not admitted. Nil is the production default.
	prewarm   *orchestrator.PrewarmPolicy
	stop      context.CancelFunc
	submitted int
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

// leasedFor is how long this fleet's daemon keeps trusting a silent node, which
// is what a case about a machine going quiet is measured in.
func leasedFor(lease time.Duration) fleetOption {
	return func(f *fleet) { f.lease = lease }
}

// runningOn hands the agent a real container runtime instead of the scripted
// one, at a heartbeat a machine can keep up with: reading a whole daemon's
// image inventory fifty times a second is a load test of Docker rather than a
// case about Mercator.
// keepingAClockAhead makes this fleet's machine read a wall clock ahead of the
// control plane's, which is what a host with a skewed clock is. Every moment it
// states, its container's start and its own read alike, comes off that clock.
func keepingAClockAhead(offset time.Duration) fleetOption {
	return func(f *fleet) { f.runtime.clockAhead = offset }
}

// boughtOn enrolls this fleet's machine on the terms an operator states for it, so
// a case about what capacity costs states the sale rather than only the rate.
func boughtOn(terms map[string]any) fleetOption {
	return func(f *fleet) { f.soldOn = terms }
}

// preparingAt replaces this fleet's speculative preparation bounds. A case about
// asking twice cannot live inside the production rate bound, which is half a
// minute and exists to keep a sweep from spending a machine's link: waiting it
// out would make the case a test of the clock.
func preparingAt(policy orchestrator.PrewarmPolicy) fleetOption {
	return func(f *fleet) { f.prewarm = &policy }
}

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
	harness := &fleet{
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
		lease:     30 * time.Second,
	}
	harness.agentRuntime = harness.runtime
	for _, option := range options {
		option(harness)
	}
	harness.address, harness.control = startRuntimeWithLease(t, harness.lease, harness.prewarm)
	bootstrap := harness.invite(t, 1.25)
	harness.nodeID = bootstrap.NodeID
	harness.stop = harness.startAgent(t, bootstrap, harness.agentRuntime)
	harness.awaitOffer(t, harness.nodeID)
	return harness
}

// machine is one enrolled node: the identity the control plane holds it under
// and the runtime its agent drives. A fleet with more than one machine is how a
// case says what Mercator does with the rest of the fleet when one of them
// cannot take the work.
type machine struct {
	nodeID  string
	runtime *scriptedRuntime
}

// enrollAnother adds a machine to this fleet at its own shadow price, with its
// own runtime and its own agent over the same node protocol. It holds none of
// the fleet's image, which is what makes it the expensive cold alternative to
// the warm machine every case starts with.
func (f *fleet) enrollAnother(t *testing.T, priceUSDPerHour float64) machine {
	t.Helper()
	runtime := newScriptedRuntime(map[string][]string{
		trainerIndexDigest: {trainerBaseDiffID, trainerTopDiffID},
	})
	bootstrap := f.invite(t, priceUSDPerHour)
	f.startAgent(t, bootstrap, runtime)
	f.awaitOffer(t, bootstrap.NodeID)
	return machine{nodeID: bootstrap.NodeID, runtime: runtime}
}

// awaitOccupied waits until this machine reports that it is executing a workload.
// What a node is running is its own fact and it travels by heartbeat, so a case
// that needs the control plane to know about a container waits for the machine to
// have said so rather than for the command that asked for it.
func (f *fleet) awaitOccupied(t *testing.T, nodeID string) {
	t.Helper()
	waitFor(t, func() bool {
		for _, offer := range f.offers(t) {
			if offer.ID == nodeID {
				return !offer.Capacity.Available
			}
		}
		return false
	}, "the machine "+nodeID+" never reported the workload it was asked to run")
}

// awaitOffer waits until Placement can choose this machine at all. It can only
// choose a node it has facts for, so a fleet is not ready until the first
// heartbeat lands.
func (f *fleet) awaitOffer(t *testing.T, nodeID string) {
	t.Helper()
	waitFor(t, func() bool {
		for _, offer := range f.offers(t) {
			if offer.ID == nodeID {
				return true
			}
		}
		return false
	}, "the enrolled node "+nodeID+" never appeared as placeable capacity")
}

func (f *fleet) invite(t *testing.T, priceUSDPerHour float64) capability.NodeBootstrap {
	t.Helper()
	var response struct {
		ControlPlaneURL string `json:"control_plane_url"`
		NodeID          string `json:"node_id"`
		RentalID        string `json:"rental_id"`
		Generation      uint64 `json:"generation"`
		EnrollmentToken string `json:"enrollment_token"`
		AgentVersion    string `json:"agent_version"`
	}
	body := map[string]any{
		"workspace_id":              daemon.DefaultWorkspaceID,
		"shadow_price_usd_per_hour": priceUSDPerHour,
	}
	for field, value := range f.soldOn {
		body[field] = value
	}
	f.call(t, http.MethodPost, "/v1/nodes", body, &response, http.StatusCreated)
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

func (f *fleet) startAgent(t *testing.T, bootstrap capability.NodeBootstrap, runtime nodeagent.Runtime) context.CancelFunc {
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
		runtime,
		nodeagent.NewHTTPTransport(bootstrap.ControlPlaneURL, nil),
		state,
		nodeagent.WithHeartbeat(f.heartbeat),
		nodeagent.WithReconnectBackoff(5*time.Millisecond),
	)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = agent.Run(ctx) }()
	return cancel
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

// submitRunUnderBudget submits a Run whose caller bounded what running it once may
// cost. The bound sits on the same policy the class does, and it is the one thing
// the class cannot argue with: a class states what a second of waiting is worth and
// can always be talked into a costlier machine.
func (f *fleet) submitRunUnderBudget(t *testing.T, usd float64) string {
	t.Helper()
	return f.submitWorkload(t, func(name string) map[string]any {
		revision := workloadRevision(name, f.image)
		placement := revision["spec"].(map[string]any)["placement"].(map[string]any)
		placement["max_expected_cost_usd"] = usd
		return revision
	})
}

// submitRunInFamily submits a Run that belongs to a family of work and states how
// wide that family may run. Every member states the width, because a group is a
// label the work carries rather than an object an operator registers first.
func (f *fleet) submitRunInFamily(t *testing.T, id string, width int) string {
	t.Helper()
	return f.submitWorkload(t, func(name string) map[string]any {
		revision := workloadRevision(name, f.image)
		placement := revision["spec"].(map[string]any)["placement"].(map[string]any)
		placement["group"] = map[string]any{"id": id, "max_parallel": width}
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

// awaitAdmission drives one Run forward the way the reconcile sweep does until
// admission stops holding it back and a machine is selected. A Run whose wait ended
// because something else finished is only asked again on the next sweep, so a case
// about a bound lifting refreshes rather than waiting out the minute cadence.
func (f *fleet) awaitAdmission(t *testing.T, runID string) {
	t.Helper()
	var response struct {
		Run struct {
			Phase string `json:"phase"`
		} `json:"run"`
	}
	waitFor(t, func() bool {
		f.call(t, http.MethodPost, "/v1/runs/"+runID+"/refresh?workspace_id="+daemon.DefaultWorkspaceID, nil, &response, http.StatusOK)
		return response.Run.Phase != "queued"
	}, "Run "+runID+" was never admitted after the capacity it was waiting for came back")
}

// awaitStartMoment waits until the Run read model carries the moment its workload
// began, which exists only once something observed one.
func (f *fleet) awaitStartMoment(t *testing.T, runID string) time.Time {
	t.Helper()
	var run struct {
		Run struct {
			StartedAt *time.Time `json:"started_at"`
		} `json:"run"`
	}
	waitFor(t, func() bool {
		// The refresh is an advance, which is what asks the node what its container
		// is doing. Waiting for the minute reconcile sweep instead would make this a
		// case about the sweep's cadence.
		f.call(t, http.MethodPost, "/v1/runs/"+runID+"/refresh?workspace_id="+daemon.DefaultWorkspaceID, nil, &run, http.StatusOK)
		return run.Run.StartedAt != nil
	}, "Run "+runID+" never recorded the moment its workload began")
	return run.Run.StartedAt.UTC()
}

// startMoment is what this Run's record says about when its workload began, right
// now, including saying nothing. It is how a case asserts an absence: awaitStartMoment
// above can only wait for a moment to arrive.
func (f *fleet) startMoment(t *testing.T, runID string) *time.Time {
	t.Helper()
	var run struct {
		Run struct {
			StartedAt *time.Time `json:"started_at"`
		} `json:"run"`
	}
	f.call(t, http.MethodPost, "/v1/runs/"+runID+"/refresh?workspace_id="+daemon.DefaultWorkspaceID, nil, &run, http.StatusOK)
	return run.Run.StartedAt
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
			"placement": map[string]any{"service_class": "standard", "expected_runtime_seconds": 60},
			"execution": map[string]any{"max_runtime_seconds": 600, "max_pre_start_attempts": 3},
		},
	}
}

// bookingDecision is the record the daemon published, read as the type the
// daemon published it as. A case that only needs the winner reads the accessors
// below; a case about why a machine was passed over reads the candidate itself.
type bookingDecision struct {
	ID                      string                     `json:"id"`
	SelectedOfferSnapshotID string                     `json:"selected_offer_snapshot_id"`
	Candidates              []domain.CandidateDecision `json:"candidates"`
	Supersedes              string                     `json:"supersedes"`
	SupersedesReason        string                     `json:"supersedes_reason"`
}

func (decision bookingDecision) candidate(t *testing.T, offerID string) domain.CandidateDecision {
	t.Helper()
	for _, candidate := range decision.Candidates {
		if candidate.OfferSnapshotID == offerID {
			return candidate
		}
	}
	t.Fatalf("the decision weighed %d candidates and none of them was %q", len(decision.Candidates), offerID)
	return domain.CandidateDecision{}
}

func (decision bookingDecision) imageLocality() domain.LocalityState {
	for _, candidate := range decision.Candidates {
		if candidate.OfferSnapshotID == decision.SelectedOfferSnapshotID {
			return candidate.ImageLocality
		}
	}
	return ""
}

func (decision bookingDecision) disposition() domain.CandidateDisposition {
	for _, candidate := range decision.Candidates {
		if candidate.OfferSnapshotID == decision.SelectedOfferSnapshotID {
			return candidate.Disposition
		}
	}
	return ""
}

func (decision bookingDecision) pullEstimate() domain.Estimate {
	return decision.stageEstimate(domain.StageImageFetch)
}

// unpackEstimate is what the selected candidate was predicted to spend turning
// content on its disk into a layer chain, which is the stage a machine that
// fetched an image and never applied it owes on its own.
func (decision bookingDecision) unpackEstimate() domain.Estimate {
	return decision.stageEstimate(domain.StageUnpack)
}

func (decision bookingDecision) stageEstimate(stage domain.LaunchStage) domain.Estimate {
	for _, candidate := range decision.Candidates {
		if candidate.OfferSnapshotID == decision.SelectedOfferSnapshotID {
			return candidate.Estimates.Stages.Stage(stage)
		}
	}
	return domain.Estimate{}
}

// advance drives one Run forward the way the reconcile sweep does, one step, and
// expects the daemon to have taken it.
func (f *fleet) advance(t *testing.T, runID string) {
	t.Helper()
	f.call(t, http.MethodPost, "/v1/runs/"+runID+"/refresh?workspace_id="+daemon.DefaultWorkspaceID, nil, nil, http.StatusOK)
}

// queueForWantOfCapacity drives one Run forward the way the reconcile sweep does
// and expects the daemon to answer that it queued it because no machine in this
// fleet has room for it. It reads the answer the caller gets rather than an error
// code: admission records a Run nothing can take as waiting, so the refresh
// succeeds and what an operator acts on is the phase and the reason on the Run
// itself.
//
// The reason is the one that says the fleet was measured. Every node here was
// weighed against the Run and none of them could hold it, which is a different
// answer from a machine that could hold it being busy: the second is a wait for
// capacity to come free, and it is the only one of the two that work behind this
// Run has to respect.
func (f *fleet) queueForWantOfCapacity(t *testing.T, runID string) {
	t.Helper()
	f.queueWaitingFor(t, runID, domain.DeferredNoCapacityFits)
}

// queueWaitingFor is the same read for a caller that names the wait itself,
// because the fleet's answer decides which wait it is and the two the fleet can
// give about a machine nothing placed a Run on are different facts.
func (f *fleet) queueWaitingFor(t *testing.T, runID, reason string) {
	t.Helper()
	var response struct {
		Run struct {
			Phase     string `json:"phase"`
			Admission struct {
				Reason string `json:"reason"`
			} `json:"admission"`
		} `json:"run"`
	}
	f.call(t, http.MethodPost, "/v1/runs/"+runID+"/refresh?workspace_id="+daemon.DefaultWorkspaceID, nil, &response, http.StatusOK)
	if response.Run.Phase != "queued" || response.Run.Admission.Reason != reason {
		t.Fatalf("run %q is %q waiting for %q, want a Run queued waiting for %q",
			runID, response.Run.Phase, response.Run.Admission.Reason, reason)
	}
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

// decision is the answer that stands for a Run, read off the chain the API
// answers with. The route carries every decision the Run has, oldest first,
// because a decision is appended rather than edited: a caller who wants the
// current answer takes the end of the chain.
func (f *fleet) decision(t *testing.T, runID string) bookingDecision {
	t.Helper()
	chain := f.decisions(t, runID)
	return chain[len(chain)-1]
}

func (f *fleet) decisions(t *testing.T, runID string) []bookingDecision {
	t.Helper()
	var response struct {
		Decisions []bookingDecision `json:"decisions"`
	}
	f.call(t, http.MethodGet, "/v1/runs/"+runID+"/decision?workspace_id="+daemon.DefaultWorkspaceID, nil, &response, http.StatusOK)
	if len(response.Decisions) == 0 {
		t.Fatalf("the decision route answered with no decisions for Run %q", runID)
	}
	return response.Decisions
}

type offerSnapshot struct {
	ID        string                   `json:"id"`
	Lane      string                   `json:"lane"`
	ExpiresAt time.Time                `json:"expires_at"`
	Resources domain.ResourceInventory `json:"resources"`
	Images    domain.ImageInventory    `json:"images"`
	Artifacts domain.ArtifactInventory `json:"artifacts"`
	Capacity  domain.CapacityEvidence  `json:"capacity"`
	// Pricing and Terms are what this machine costs and what it was bought on. They
	// are read off the same catalog Placement reads, because an operator asking what a
	// machine will cost them reads the offer rather than the decision.
	Pricing domain.PriceModel    `json:"pricing"`
	Terms   domain.CapacityTerms `json:"capacity_terms"`
}

func (f *fleet) offers(t *testing.T) []offerSnapshot {
	t.Helper()
	var response struct {
		Offers []offerSnapshot `json:"offers"`
	}
	f.call(t, http.MethodGet, "/v1/offers?workspace_id="+daemon.DefaultWorkspaceID, nil, &response, http.StatusOK)
	return response.Offers
}

// offerFor is one named machine as Placement sees it, which is where an operator
// reads whether capacity is free.
func (f *fleet) offerFor(t *testing.T, nodeID string) offerSnapshot {
	t.Helper()
	listed := f.offers(t)
	for _, offer := range listed {
		if offer.ID == nodeID {
			return offer
		}
	}
	t.Fatalf("the offer catalog has no entry for node %q: %+v", nodeID, listed)
	return offerSnapshot{}
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

// get reads one endpoint and answers with the status instead of failing on it,
// which is how a case asks whether Mercator has recorded something yet. A record
// that does not exist is an answer about the control plane rather than a broken
// call, and a case that cannot tell the two apart reports the wrong one.
func (f *fleet) get(t *testing.T, path string, into any) int {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, "http://"+f.address+path, http.NoBody)
	if err != nil {
		t.Fatalf("build GET %s: %v", path, err)
	}
	request.Header.Set("Authorization", "Bearer "+f.token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("call GET %s: %v", path, err)
	}
	defer func() { _ = response.Body.Close() }()
	raw, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		return response.StatusCode
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("decode GET %s: %v: %s", path, err, raw)
	}
	return response.StatusCode
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
	undescribed []string
	// prepared is every image the control plane asked this machine to fetch for
	// work it had not admitted here.
	prepared []string
	// refusePulls is content this machine cannot fetch, by manifest digest. It is
	// removed the first time it is asked for, so the second ask for the same
	// content succeeds: a failed pull leaves nothing behind and a machine that
	// refused one is not a machine that will refuse it forever.
	refusePulls  map[string]bool
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
	// clockAhead is how far this machine's clock runs ahead of the control plane's.
	// It moves both moments this runtime states, because a host with a skewed clock
	// reads its container's start and its own wall clock off the same clock: two
	// moments that agree with each other and with nothing Mercator knows. Every
	// other runtime in this file dates both from the control plane's own clock, so
	// nothing here could state the world the Broker's read moment exists for.
	clockAhead time.Duration
}

func newScriptedRuntime(unpacks map[string][]string) *scriptedRuntime {
	return &scriptedRuntime{
		unpacks:      unpacks,
		platforms:    map[string]domain.Platform{},
		observations: map[string]capability.WorkloadObservation{},
		launches:     map[string]capability.LaunchWorkloadCommand{},
		refusePulls:  map[string]bool{},
		disk:         capability.DiskFacts{Known: true, TotalBytes: 500 << 30, FreeBytes: 400 << 30},
	}
}

// refuseNextPullOf makes this machine turn away the next request for one piece of
// content, exactly as a daemon whose registry is unreachable does. It leaves
// nothing behind, so a later request for the same content is a first ask.
func (runtime *scriptedRuntime) refuseNextPullOf(digest string) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.refusePulls[digest] = true
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

// PrepareImage is the pull the control plane asked for on behalf of work it has
// not admitted. It leaves the image behind exactly as a real pull does, which is
// what makes the next heartbeat report a machine that is warm for a Run it has
// never executed.
func (runtime *scriptedRuntime) PrepareImage(_ context.Context, command capability.PrepareImageCommand) error {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.prepared = append(runtime.prepared, command.ManifestDigest)
	if runtime.refusePulls[command.ManifestDigest] {
		delete(runtime.refusePulls, command.ManifestDigest)
		return fmt.Errorf("pull failed: registry unreachable")
	}
	if !slices.Contains(runtime.held, command.ManifestDigest) {
		runtime.held = append(runtime.held, command.ManifestDigest)
	}
	return nil
}

func (runtime *scriptedRuntime) PrepareArtifact(context.Context, capability.PrepareArtifactCommand) error {
	return nil
}

// preparedImages is every image this machine was asked to fetch ahead of a Run,
// in the order it was asked. Counting them is how a case says Mercator asked
// once rather than on every sweep.
func (runtime *scriptedRuntime) preparedImages() []string {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return slices.Clone(runtime.prepared)
}

func (runtime *scriptedRuntime) LaunchWorkload(_ context.Context, command capability.LaunchWorkloadCommand) error {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.launched = append(runtime.launched, command.RunID)
	runtime.launches[command.RunID] = command
	if !slices.Contains(runtime.held, command.ManifestDigest) {
		runtime.held = append(runtime.held, command.ManifestDigest)
	}
	// A container runtime knows when it gave this workload a process, and that
	// moment is not the moment anybody later asks. This machine states it a stated
	// distance in the past, because a scripted runtime that answered "now" on every
	// read would let a control plane stamping its own poll pass every case. Both
	// moments carry this machine's own clock offset, because a real one has no other
	// clock to read them off.
	startedAt := runtime.clock().Add(-scriptedStartDelay)
	runtime.observations[command.RunID] = capability.WorkloadObservation{
		RunID:      command.RunID,
		AttemptID:  command.AttemptID,
		Phase:      capability.WorkloadPhaseRunning,
		ObservedAt: runtime.clock(),
		StartedAt:  &startedAt,
	}
	return nil
}

// clock is this machine's own wall clock, which is the control plane's plus
// whatever this host's is out by.
func (runtime *scriptedRuntime) clock() time.Time {
	return time.Now().UTC().Add(runtime.clockAhead)
}

// scriptedStartDelay is how long before the observation this machine says its
// container began. It is larger than any plausible gap between a launch and the
// heartbeat that follows it, so a case can tell the moment the runtime reported
// from the moment the control plane looked.
const scriptedStartDelay = 90 * time.Second

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
	observation.ObservedAt = runtime.clock()
	runtime.observations[runID] = observation
}

// reportedStart is when this machine says it gave one Run's workload a process,
// read out of the runtime rather than out of Mercator so a case compares two
// answers rather than one answer with itself.
func (runtime *scriptedRuntime) reportedStart(t *testing.T, runID string) time.Time {
	t.Helper()
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	observation, known := runtime.observations[runID]
	if !known || observation.StartedAt == nil {
		t.Fatalf("this machine reports no start moment for %q", runID)
	}
	return observation.StartedAt.UTC()
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
	// Assembly is its own stage, and this machine owes that stage and no transfer.
	// A record that folded them together would bill the network for bytes already
	// on the disk and send an operator after a problem that is not there.
	unpack := decision.unpackEstimate()
	want := float64((18_000_000_000+40_000_000)*8) / 1_000_000 / domain.AssumedUnpackMbps
	if unpack.Expected < want || unpack.Expected > want+1 {
		t.Fatalf("unpack expected = %v seconds, want about %v: the bytes are here and the chain is not", unpack.Expected, want)
	}
	if unpack.Confidence != domain.AssumedLinkConfidence {
		t.Fatalf("unpack confidence = %v, want %v: nothing has measured how fast this machine unpacks",
			unpack.Confidence, domain.AssumedLinkConfidence)
	}
	if fetch := decision.pullEstimate(); fetch.Expected != 0 {
		t.Fatalf("a node holding every byte was charged %v seconds of transfer", fetch.Expected)
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
	silent := fleet.invite(t, 1.25)

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
//
// What the Run waits for is the other half, and it is not what the fleet says
// about a machine it measured. The offer states no room and states that nobody
// measured it, so the wait names the silence, and a Run in it keeps its place in
// the queue. Read as a machine with no room, one failed statfs on the only node
// in a workspace made every Run in that workspace a Run no machine can ever
// hold, and every one of them then lost its standing to the next arrival.
func TestANodeThatCannotMeasureItsDiskWinsNoPlacement(t *testing.T) {
	fleet := startFleet(t, reporting(capability.DiskFacts{}))

	offered := fleet.nodeOffer(t).Resources
	if offered.EphemeralDiskBytes != 0 || offered.EphemeralDiskKnown {
		t.Fatalf("a node that could not measure its disk offered %d bytes of room, known=%v",
			offered.EphemeralDiskBytes, offered.EphemeralDiskKnown)
	}
	runID := fleet.submitRun(t)
	fleet.queueWaitingFor(t, runID, domain.DeferredCapacityUnstated)

	if launched := fleet.runtime.launchedRuns(); slices.Contains(launched, runID) {
		t.Fatalf("a Run was sent to a machine whose room nobody established: %v", launched)
	}
	refusal := fleet.decision(t, runID)
	if len(refusal.Candidates) != 1 || refusal.Candidates[0].Disk.FreeBytesKnown {
		t.Fatalf("the decision weighed %d machines and the first states its room as measured", len(refusal.Candidates))
	}
	if code := refusal.Candidates[0].Rejections[0].Code; code != "UNKNOWN_FACT" {
		t.Fatalf("a machine that never measured its disk was refused with %q", code)
	}
}

// TestARunPlacesOnANodeWithRoomForItAndNotOnOneWithout is the observable half
// of the same bug, end to end through the public API. One Run asks for less
// disk than the node has and lands on it, which before this commit no Run
// declaring any disk at all could do. One asks for more than the machine has,
// and the daemon queues it for want of capacity and never asks the node to run
// it.
//
// The Run nothing could place is now explainable from its own record, which is
// what this case could not do before. A Run that found no feasible offer recorded
// no Booking Decision at all, so the wait had to be read from the daemon's answer
// and which machine was struck out and why could only be asserted one layer down,
// in the Blueprint a-host-that-cannot-hold-the-data-is-not-warm. Both are read
// here now, off the decision route, through the real daemon and the real enrolled
// node: the refusal chose nothing, it weighed this fleet's one machine, and it
// says the room was the reason.
func TestARunPlacesOnANodeWithRoomForItAndNotOnOneWithout(t *testing.T) {
	fleet := startFleet(t)

	fits := fleet.submitRunNeedingDisk(t, 100<<30)
	fleet.completeWorkload(t, fits, 0)
	fleet.awaitOutcome(t, fits, "succeeded")
	oversized := fleet.submitRunNeedingDisk(t, 900<<30)
	fleet.queueForWantOfCapacity(t, oversized)

	if selected := fleet.decision(t, fits).SelectedOfferSnapshotID; selected != fleet.nodeID {
		t.Fatalf("a Run needing 100GiB landed on %q, and the node has 400GiB free", selected)
	}
	if launched := fleet.runtime.launchedRuns(); slices.Contains(launched, oversized) {
		t.Fatalf("a Run needing 900GiB was sent to a machine with 400GiB free: %v", launched)
	}
	refusal := fleet.decision(t, oversized)
	if refusal.SelectedOfferSnapshotID != "" {
		t.Fatalf("the queued Run's decision chose %q, and no machine in this fleet has 900GiB", refusal.SelectedOfferSnapshotID)
	}
	refused := refusal.candidate(t, fleet.nodeID)
	if refused.Feasible {
		t.Fatalf("the recorded refusal calls the node feasible for a Run needing 900GiB: %+v", refused)
	}
	if !slices.ContainsFunc(refused.Rejections, func(rejection domain.Violation) bool {
		return rejection.Code == "RESOURCE_INSUFFICIENT" && rejection.Path == "resources.ephemeral_disk"
	}) {
		t.Fatalf("the recorded refusal says %+v, and the reason is the room the node has left", refused.Rejections)
	}
}

// TestTheDecisionRouteAnswersWithTheWholeChain is the read an operator and the
// console both make, through the real daemon. A decision is appended and never
// rewritten, so a Run answered twice has two records and the route hands over
// both: the refusal that came first, then the answer that stands in for it and
// names it.
//
// The first machine in this fleet has fifty gibibytes free and the Run needs two
// hundred, so it waits, and the refusal that says so is the only record of what
// this fleet looked like when it arrived. A second machine is then enrolled with
// room for the work, and the Run takes it. Collapsing the route's answer to its
// last entry shows an operator a Run that had only ever been answered once, with
// the wait and the machine that could not hold it nowhere on the page.
func TestTheDecisionRouteAnswersWithTheWholeChain(t *testing.T) {
	fleet := startFleet(t, reporting(capability.DiskFacts{Known: true, TotalBytes: 100 << 30, FreeBytes: 50 << 30}))

	waiting := fleet.submitRunNeedingDisk(t, 200<<30)
	fleet.queueForWantOfCapacity(t, waiting)
	roomier := fleet.enrollAnother(t, 1.00)
	fleet.advance(t, waiting)

	chain := fleet.decisions(t, waiting)
	if len(chain) != 2 {
		t.Fatalf("the route answered with %d decisions, and this Run was answered once for want of room and again on the machine that had it", len(chain))
	}
	refusal, placement := chain[0], chain[1]
	if refusal.SelectedOfferSnapshotID != "" || refusal.candidate(t, fleet.nodeID).Feasible {
		t.Fatalf("the answer that no longer stands chose %q, and the only machine in this fleet had fifty gibibytes free", refusal.SelectedOfferSnapshotID)
	}
	if placement.SelectedOfferSnapshotID != roomier.nodeID {
		t.Fatalf("the answer that stands says the Run went to %q, and the machine with room for it is %q", placement.SelectedOfferSnapshotID, roomier.nodeID)
	}
	if placement.Supersedes != refusal.ID || placement.SupersedesReason != domain.SupersededSelectedNothing {
		t.Fatalf("the answer that stands replaces %q for %q, and the record before it is %q, which placed the Run nowhere",
			placement.Supersedes, placement.SupersedesReason, refusal.ID)
	}
}

// TestAnImpossibleAskLeavesThisFleetRunning is what queueing a Run nothing can
// place does to the rest of the workspace, end to end through the public API. It
// is the order that makes the case: the impossible Run is queued first, so it is
// the older wait and it outranks every later arrival of its own class, and the
// queue's whole job is to make later work respect a wait like that.
//
// It must not respect this one. The Run in front is waiting for a machine with
// three times this fleet's room to be added, the Run behind it fits the node that
// is here, and they are not waiting for the same thing. Ordering the second behind
// the first left the node idle until the first one's class deadline cleared it,
// four hours later for standard work and never for a class that declares none.
func TestAnImpossibleAskLeavesThisFleetRunning(t *testing.T) {
	fleet := startFleet(t)

	oversized := fleet.submitRunNeedingDisk(t, 900<<30)
	fleet.queueForWantOfCapacity(t, oversized)
	fits := fleet.submitRunNeedingDisk(t, 100<<30)
	fleet.completeWorkload(t, fits, 0)
	fleet.awaitOutcome(t, fits, "succeeded")

	launched := fleet.runtime.launchedRuns()
	if !slices.Contains(launched, fits) {
		t.Fatalf("the node sat idle beside a Run needing 100GiB of its 400GiB: %v", launched)
	}
	if slices.Contains(launched, oversized) {
		t.Fatalf("a Run needing 900GiB was sent to a machine with 400GiB free: %v", launched)
	}
	fleet.queueForWantOfCapacity(t, oversized)
}

// TestAnImpossibleAskLeavesABusyFleetRunning is the same claim about the fleet
// every real workspace has, which is one with something already running on it. The
// case above starts from an idle machine, and an idle machine is the one state in
// which a classification read off the Bookings Mercator holds happens to agree with
// what the machines actually refused.
//
// The node is occupied before the impossible ask arrives here. Its Rental now holds
// a Booking, so a candidate built from it carries a projected start whether it could
// ever run the work or not: reading that as the difference between the two waits
// recorded a Run needing 900GiB of a 400GiB machine as waiting for capacity to come
// free, which is a wait the queue makes later work respect. The Run behind it fits
// the machine and belongs in the queue for it, one Booking behind the work that is
// there, and it was told to wait for a machine three times this fleet's size to be
// bought instead.
func TestAnImpossibleAskLeavesABusyFleetRunning(t *testing.T) {
	fleet := startFleet(t)

	occupies := fleet.submitRun(t)
	fleet.awaitOccupied(t, fleet.nodeID)
	oversized := fleet.submitRunNeedingDisk(t, 900<<30)
	fleet.queueForWantOfCapacity(t, oversized)
	fits := fleet.submitRunNeedingDisk(t, 100<<30)

	if selected := fleet.decision(t, fits).SelectedOfferSnapshotID; selected != fleet.nodeID {
		t.Fatalf("a Run needing 100GiB of this machine's 400GiB was placed on %q, and it belongs in the queue for the node that has the room", selected)
	}
	if launched := fleet.runtime.launchedRuns(); slices.Contains(launched, oversized) {
		t.Fatalf("a Run needing 900GiB was sent to a machine with 400GiB free: %v", launched)
	}
	if occupying := fleet.runtime.launchedRuns(); !slices.Contains(occupying, occupies) {
		t.Fatalf("the machine this case needs busy is running %v", occupying)
	}
	fleet.queueForWantOfCapacity(t, oversized)
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

// TestABoundOnCostRefusesTheOnlyMachineInThisFleet is the caller's maximum cost
// through the public API, against a machine whose price an operator configured when
// they invited it. The placement corpus states the refusal in a recorded decision and
// the Lab states it under the real control plane over a simulated fleet; this states
// it where the price actually comes from, and holds the node to never having been
// asked to run the work.
//
// The two Runs differ in one number. The first allows five cents and runs on this
// node; the second allows one, and a minute at the node's 1.25 USD an hour is two
// cents, so the same machine that just ran the same workload is refused for the
// second one. That is what makes the bound the cause rather than the fleet.
func TestABoundOnCostRefusesTheOnlyMachineInThisFleet(t *testing.T) {
	fleet := startFleet(t)

	affordable := fleet.submitRunUnderBudget(t, 0.05)
	fleet.completeWorkload(t, affordable, 0)
	fleet.awaitOutcome(t, affordable, "succeeded")
	pinched := fleet.submitRunUnderBudget(t, 0.01)
	fleet.queueForWantOfCapacity(t, pinched)

	if selected := fleet.decision(t, affordable).SelectedOfferSnapshotID; selected != fleet.nodeID {
		t.Fatalf("the Run that could afford this node landed on %q", selected)
	}
	refusal := fleet.decision(t, pinched)
	if refusal.SelectedOfferSnapshotID != "" {
		t.Fatalf("a Run allowing one cent was placed on %q, which costs two", refusal.SelectedOfferSnapshotID)
	}
	refused := refusal.candidate(t, fleet.nodeID)
	if refused.Feasible {
		t.Fatalf("the recorded refusal calls the node feasible for a Run that cannot afford it: %+v", refused)
	}
	if !slices.ContainsFunc(refused.Rejections, func(rejection domain.Violation) bool {
		return rejection.Code == "COST_LIMIT_EXCEEDED" && rejection.Path == "placement.max_expected_cost_usd"
	}) {
		t.Fatalf("the recorded refusal says %+v, and what this machine exceeded is the bound on cost", refused.Rejections)
	}
	if launched := fleet.runtime.launchedRuns(); slices.Contains(launched, pinched) {
		t.Fatalf("a Run whose caller allowed one cent was sent to the machine anyway: %v", launched)
	}
}

// TestAFamilyIsHeldToItsWidthWhileAMachineStandsIdle is the group bound where the
// capacity is real. The placement corpus states one moment of the ordering and the
// Lab drives the whole sweep over a simulated fleet; this puts it through the public
// API, the real event log the queue is replayed out of, and two enrolled nodes over
// the node protocol.
//
// The idle machine is the whole case. A family held back by a fleet with nothing
// free proves nothing about a declared width, so the second machine is enrolled,
// warm, and provably available: a Run outside the family takes it while the family's
// own second member is waiting. The bound then lifts on its own, because the thing
// holding the member was its family and the family made room.
func TestAFamilyIsHeldToItsWidthWhileAMachineStandsIdle(t *testing.T) {
	fleet := startFleet(t)
	spare := fleet.enrollAnother(t, 9.00)

	first := fleet.submitRunInFamily(t, "sweep", 1)
	fleet.runtime.awaitLaunch(t, first)
	second := fleet.submitRunInFamily(t, "sweep", 1)
	fleet.queueWaitingFor(t, second, domain.DeferredGroupAtParallelism)

	// No machine was asked to run the second member, and the record says why: the
	// family it belongs to, and the member of it holding capacity.
	for _, launched := range [][]string{fleet.runtime.launchedRuns(), spare.runtime.launchedRuns()} {
		if slices.Contains(launched, second) {
			t.Fatalf("the second member ran while its family was already as wide as its caller declared: %v", launched)
		}
	}
	// And no machine was weighed for it either. A family already as wide as it
	// declared asks the fleet nothing, because no answer the fleet could give would
	// change what is holding the member.
	fleet.call(t, http.MethodGet, "/v1/runs/"+second+"/decision?workspace_id="+daemon.DefaultWorkspaceID, nil, nil, http.StatusNotFound)
	// The other machine was standing idle throughout, as its own heartbeat reports it
	// over the same catalog Placement reads. That is what makes this a bound on the
	// family rather than on a fleet that ran out.
	if idle := fleet.offerFor(t, spare.nodeID); !idle.Capacity.Available {
		t.Fatalf("the second machine reports %+v, and this case needs a machine that could have taken the waiting member", idle.Capacity)
	}

	// And the member runs the moment its own family makes room for it.
	fleet.completeWorkload(t, first, 0)
	fleet.awaitOutcome(t, first, "succeeded")
	fleet.awaitAdmission(t, second)
	fleet.completeWorkload(t, second, 0)
	fleet.awaitOutcome(t, second, "succeeded")
}

// TestANodeBilledInBlocksIsPricedForTheWholeBlock is owned capacity economics
// through the public API, over the real node protocol and the real event log. The
// Lab states the arithmetic against a simulated fleet; this states that an
// operator's own answer about how their machine is bought reaches the offer
// catalogue and the recorded decision.
//
// The machine is bought in ten-minute blocks and the Run wants twenty minutes, so
// it runs past the end of the block Mercator is already inside and commits Mercator
// to whole blocks beyond it. The seconds of those blocks nothing will use are
// charged to this placement, which is the whole difference between this and billing
// the seconds one Run occupies.
func TestANodeBilledInBlocksIsPricedForTheWholeBlock(t *testing.T) {
	fleet := startFleet(t, boughtOn(map[string]any{"billing_interval_seconds": 600}))

	run := fleet.submitRunLasting(t, 1200, 1800)
	fleet.runtime.awaitLaunch(t, run)

	// The offer says what the machine is bought on, in the same catalogue an operator
	// reads to find out what capacity will cost them.
	offer := fleet.nodeOffer(t)
	if offer.Pricing.GranularitySeconds != 600 {
		t.Fatalf("a machine bought in ten-minute blocks publishes a billing increment of %ds", offer.Pricing.GranularitySeconds)
	}
	if offer.Terms.CommittedUntil.IsZero() {
		t.Fatalf("a machine bought in blocks owes rent to no moment at all: %+v", offer.Terms)
	}
	if remaining := time.Until(offer.Terms.CommittedUntil); remaining <= 0 || remaining > 10*time.Minute {
		t.Fatalf("the block this machine is inside ends in %s, and the blocks are ten minutes long", remaining)
	}

	// And the decision charges the placement for the blocks it buys, not for the
	// seconds the Run occupies.
	candidate := fleet.decision(t, run).candidate(t, fleet.nodeID)
	rate := 1.25 / 3600
	occupied, terms := rate*1200, candidate.Estimates.CostTerms
	if len(terms) == 0 {
		t.Fatalf("the node is priced at %.6f USD and the record says nothing about what that is made of", candidate.Estimates.CostUSD.Expected)
	}
	if candidate.Estimates.CostUSD.Expected <= occupied {
		t.Fatalf("twenty minutes on a machine bought in ten-minute blocks costs %.6f USD, and the seconds the Run occupies alone are %.6f: %+v",
			candidate.Estimates.CostUSD.Expected, occupied, terms)
	}
	tail, charged := candidate.Estimates.CostTermUSD(domain.CostTermIdleTail)
	if !charged || tail <= 0 {
		t.Fatalf("the block this Run runs past the end of leaves an idle tail of %.6f USD: %+v", tail, terms)
	}
	if committed := candidate.Estimates.Committed; committed.Until.IsZero() || committed.Seconds <= 0 {
		t.Fatalf("the record says this placement spends %+v of the block Mercator already owes for", committed)
	}
	fleet.completeWorkload(t, run, 0)
	fleet.awaitOutcome(t, run, "succeeded")
}

// TestANodeHeldForOtherWorkIsRefusedRatherThanPriced is reserved capacity through
// the public API. An operator holding a machine for the work somebody is watching
// says so at enrolment, and a batch sweep is then refused that machine rather than
// ranked on it: the refusal is about what the machine is held for, so no amount of
// waiting for this machine produces a placement.
//
// It is the only machine in this fleet, which is what makes the refusal visible: the
// Run waits for capacity, and the decision beside it names the reservation.
func TestANodeHeldForOtherWorkIsRefusedRatherThanPriced(t *testing.T) {
	fleet := startFleet(t, boughtOn(map[string]any{"eligible_service_classes": []string{"interactive"}}))

	watched := fleet.submitRunOfClass(t, "interactive")
	fleet.runtime.awaitLaunch(t, watched)
	fleet.completeWorkload(t, watched, 0)
	fleet.awaitOutcome(t, watched, "succeeded")
	sweep := fleet.submitRunOfClass(t, "batch")
	fleet.queueForWantOfCapacity(t, sweep)

	if selected := fleet.decision(t, watched).SelectedOfferSnapshotID; selected != fleet.nodeID {
		t.Fatalf("the watched Run landed on %q, and this machine is held for exactly that work", selected)
	}
	refusal := fleet.decision(t, sweep)
	if refusal.SelectedOfferSnapshotID != "" {
		t.Fatalf("a batch sweep was placed on %q, which its operator holds for interactive work", refusal.SelectedOfferSnapshotID)
	}
	refused := refusal.candidate(t, fleet.nodeID)
	if refused.Feasible {
		t.Fatalf("the record calls a machine held for other work feasible, priced at %.6f USD", refused.Estimates.CostUSD.Expected)
	}
	if !slices.ContainsFunc(refused.Rejections, func(rejection domain.Violation) bool {
		return rejection.Code == "CLASS_NOT_ELIGIBLE" && rejection.Path == "capacity_terms.eligible_classes"
	}) {
		t.Fatalf("the recorded refusal says %+v, and what this machine is not eligible for is this Run's class", refused.Rejections)
	}
	if refused.Standing() != domain.StandingNeverHolds {
		t.Fatalf("a machine reserved for other work counts as %v for this Run, and waiting does not make a class eligible", refused.Standing())
	}
	if launched := fleet.runtime.launchedRuns(); slices.Contains(launched, sweep) {
		t.Fatalf("a sweep was sent to a machine held for watched work anyway: %v", launched)
	}
}

// submitRunLasting submits a Run that says how long it expects to hold a machine
// and how long Mercator may let it. Both matter to what capacity costs: the
// expectation is what the placement is priced over, and the bound is what an
// availability window is judged against.
func (f *fleet) submitRunLasting(t *testing.T, expected, maximum int) string {
	t.Helper()
	return f.submitWorkload(t, func(name string) map[string]any {
		revision := workloadRevision(name, f.image)
		spec := revision["spec"].(map[string]any)
		spec["placement"].(map[string]any)["expected_runtime_seconds"] = expected
		spec["execution"].(map[string]any)["max_runtime_seconds"] = maximum
		return revision
	})
}

// submitRunOfClass submits a Run that declares what kind of work it is, which is
// what capacity held for particular work is held by.
func (f *fleet) submitRunOfClass(t *testing.T, class string) string {
	t.Helper()
	return f.submitWorkload(t, func(name string) map[string]any {
		revision := workloadRevision(name, f.image)
		revision["spec"].(map[string]any)["placement"].(map[string]any)["service_class"] = class
		return revision
	})
}
