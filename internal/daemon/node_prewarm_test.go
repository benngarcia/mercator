package daemon_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/daemon"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/orchestrator"
)

// TestAQueuedRunPreparesTheMachineItIsGoingTo is the production half of the
// prewarming slice, through the real node protocol to a real agent. The prepare
// path has existed since phase 2 and had no caller: nothing in the orchestrator
// or the Broker ever issued one, and broker.Nodes did not declare it, so an
// enrolled node holding an idle host runtime waited out a queued Run's whole
// pull at the moment that Run finally reached it.
//
// The machine is warm for the first Run, so its own preparation is over as soon
// as it is launched and the second Run's content is the only thing left to
// fetch. That is deliberate: a host still getting ready for admitted work is one
// Mercator must not prepare, and this case is about the prepare rather than
// about the restraint.
func TestAQueuedRunPreparesTheMachineItIsGoingTo(t *testing.T) {
	fleet := startFleet(t)
	fleet.holdsImageAlready(t, trainerIndexDigest)

	running := fleet.submitRun(t)
	fleet.runtime.awaitLaunch(t, running)
	queued := fleet.submitRunFor(t, fleet.rebuiltImage)

	fleet.prepareUntil(t, func() bool {
		return len(fleet.runtime.preparedImages()) > 0
	}, "the queued Run's host was never asked to prepare anything")

	if prepared := fleet.runtime.preparedImages(); len(prepared) != 1 || prepared[0] != rebuiltIndexDigest {
		t.Fatalf("the machine was asked to prepare %v, want the queued Run's image once", prepared)
	}
	if launched := fleet.runtime.launchedRuns(); len(launched) != 1 {
		t.Fatalf("the machine ran %v, and the queued Run has not been dispatched: preparation is not execution", launched)
	}
	waitFor(t, func() bool {
		return fleet.nodeOffer(t).Images.Holds(rebuiltIndexDigest)
	}, "the machine never reported holding the image it was asked to prepare")
	if queued == "" {
		t.Fatal("no Run was queued, so nothing was being prepared for")
	}
}

// TestAQueuedRunIsPreparedForWithoutWaitingForASweep is the production trigger.
// Preparation used to happen only on the reconcile sweep, once a minute, so a Run
// queued half a second after one waited out the rest of the minute before a byte
// moved for it, and the operator's own bound on how often preparation may begin
// could never hold anything back: two sweeps are never closer together than the
// sweep's cadence, which is slower than any interval anyone would state. Nothing
// here sweeps. The Run is submitted over HTTP and the machine is asked to prepare
// because the Booking that named it was recorded.
func TestAQueuedRunIsPreparedForWithoutWaitingForASweep(t *testing.T) {
	fleet := startFleet(t)
	fleet.holdsImageAlready(t, trainerIndexDigest)
	running := fleet.submitRun(t)
	fleet.runtime.awaitLaunch(t, running)
	// A machine still getting ready for work Mercator has admitted there is one
	// nothing speculative may touch, and that readiness is a moment in wall-clock
	// time this Run's own decision named. It passes on a clock rather than on an
	// event, which is why the sweep stays: what this case is about is the Run that
	// arrives afterwards.
	fleet.awaitPredictedStart(t, running)

	queued := fleet.submitRunFor(t, fleet.rebuiltImage)

	// The Booking is what the trigger is, so a case that cannot see one is not
	// making a statement about preparation at all: it is reporting that this Run
	// was never given the machine. Those are two different answers and they used
	// to arrive as one message.
	fleet.awaitQueuedOn(t, queued, fleet.nodeID)
	waitFor(t, func() bool {
		return len(fleet.runtime.preparedImages()) > 0
	}, "the queued Run's host was never asked to prepare anything, and this case never swept")
	if prepared := fleet.runtime.preparedImages(); len(prepared) != 1 || prepared[0] != rebuiltIndexDigest {
		t.Fatalf("the machine was asked to prepare %v, want the queued Run's image once", prepared)
	}
	if launched := fleet.runtime.launchedRuns(); len(launched) != 1 {
		t.Fatalf("the machine ran %v, and the queued Run has not been dispatched: preparation is not execution", launched)
	}
}

// awaitPredictedStart waits out the start Mercator predicted for a Run it has
// just launched. Nothing below the control plane reports that a host has finished
// getting ready, so this is the same number the placement was made on, measured
// from the same moment the rule measures it from.
//
// Both halves have to come off the record rather than off this harness's own
// clock. A node reports a workload running and the control plane records the
// launch when that report lands, so a case watching its own scripted runtime sees
// the container start first, by however long one heartbeat and one event write
// take. Sleeping a fixed margin past that was the case racing that lag: on a
// machine where the report took longer than the margin, the next Run's Booking
// landed while the window was still open, the one trigger it gets asked for
// nothing, and nothing here sweeps, so the case failed on a slow host and passed
// on a fast one.
func (f *fleet) awaitPredictedStart(t *testing.T, runID string) {
	t.Helper()
	ready := f.launchRecordedAt(t, runID).Add(f.predictedStart(t, runID))
	for time.Now().Before(ready) {
		time.Sleep(10 * time.Millisecond)
	}
}

// awaitQueuedOn waits until the Run's Booking names the machine and is waiting
// for it, which is the only state anything is prepared for. A Run Mercator found
// nowhere to put records no Booking at all, so the absence of one is reported as
// what it is rather than as a failure to prepare.
func (f *fleet) awaitQueuedOn(t *testing.T, runID, offerSnapshotID string) {
	t.Helper()
	placed := ""
	waitFor(t, func() bool {
		placed = f.placedOn(t, runID)
		return placed == offerSnapshotID
	}, fmt.Sprintf(
		"Run %s was never queued on %s, so no preparation was owed for it: its Booking names %q",
		runID, offerSnapshotID, placed,
	))
}

// placedOn is the machine this Run's Booking names, and the empty string while
// Mercator has recorded no Booking for it.
func (f *fleet) placedOn(t *testing.T, runID string) string {
	t.Helper()
	var response struct {
		Decision bookingDecision `json:"decision"`
	}
	path := "/v1/runs/" + runID + "/decision?workspace_id=" + daemon.DefaultWorkspaceID
	if status := f.get(t, path, &response); status != http.StatusOK {
		return ""
	}
	return response.Decision.SelectedOfferSnapshotID
}

// launchRecordedAt is the moment Mercator recorded this Run's launch, which is
// when the node's report of it landed and is the only clock the restraint has.
func (f *fleet) launchRecordedAt(t *testing.T, runID string) time.Time {
	t.Helper()
	var recorded time.Time
	waitFor(t, func() bool {
		var response struct {
			Events []struct {
				Type string    `json:"type"`
				Time time.Time `json:"time"`
			} `json:"events"`
		}
		f.call(t, http.MethodGet, "/v1/runs/"+runID+"/events?workspace_id="+daemon.DefaultWorkspaceID, nil, &response, http.StatusOK)
		for _, event := range response.Events {
			if event.Type != orchestrator.EventLaunchAccepted {
				continue
			}
			recorded = event.Time
			return true
		}
		return false
	}, "Mercator never recorded the launch of Run "+runID)
	return recorded
}

// predictedStart is how long Mercator said the machine it chose would take to
// have this Run running. It is the number its own restraint is measured with, so
// a case waiting out that restraint has to wait out this and not a guess.
func (f *fleet) predictedStart(t *testing.T, runID string) time.Duration {
	t.Helper()
	decision := f.decision(t, runID)
	for _, candidate := range decision.Candidates {
		if candidate.OfferSnapshotID != decision.SelectedOfferSnapshotID {
			continue
		}
		return time.Duration(candidate.Estimates.StartSeconds.Expected * float64(time.Second))
	}
	t.Fatalf("Run %q has no candidate for the machine it was launched on", runID)
	return 0
}

// TestNothingIsPreparedOnAMachineStillGettingReadyForItsOwnRun is the restraint
// half, through the production daemon. This machine holds none of the first
// Run's image, so Mercator's own decision says it is minutes from starting, and
// a speculative pull issued now would be Mercator delaying the Run it just
// admitted: a node performs one command at a time and both fetches cross one
// link.
//
// Nothing below the control plane can say this. A provider and a node both
// report a workload running from the moment the launch is accepted, so how much
// longer a host is still getting ready is the prediction the placement was made
// on and nothing else.
func TestNothingIsPreparedOnAMachineStillGettingReadyForItsOwnRun(t *testing.T) {
	fleet := startFleet(t)

	running := fleet.submitRun(t)
	fleet.runtime.awaitLaunch(t, running)
	fleet.submitRunFor(t, fleet.rebuiltImage)

	// Sweep for a second, which is a fraction of the pull this machine was
	// predicted to owe. Nothing speculative may start inside it.
	stable := time.Now().Add(time.Second)
	for time.Now().Before(stable) {
		fleet.prepare(t)
		if prepared := fleet.runtime.preparedImages(); len(prepared) > 0 {
			t.Fatalf(
				"the machine was asked to prepare %v while the Run it was just given is still fetching its own image",
				prepared,
			)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestAPreparedMachineIsWarmForARunThatNeverExecutedThere is the capability's
// whole point, stated in the record an operator reads. The third Run's Booking
// Decision prices this host at zero pull seconds and records it hot, and the
// only workload it has ever run used a different image.
func TestAPreparedMachineIsWarmForARunThatNeverExecutedThere(t *testing.T) {
	fleet := startFleet(t)
	fleet.holdsImageAlready(t, trainerIndexDigest)

	running := fleet.submitRun(t)
	fleet.runtime.awaitLaunch(t, running)
	fleet.submitRunFor(t, fleet.rebuiltImage)
	fleet.prepareUntil(t, func() bool {
		return fleet.nodeOffer(t).Images.Holds(rebuiltIndexDigest)
	}, "the machine never reported holding the image it was asked to prepare")

	later := fleet.submitRunFor(t, fleet.rebuiltImage)

	decision := fleet.decision(t, later)
	if decision.SelectedOfferSnapshotID != fleet.nodeID {
		t.Fatalf("the Run landed on %q, want the prepared machine %q", decision.SelectedOfferSnapshotID, fleet.nodeID)
	}
	if decision.imageLocality() != domain.LocalityHot {
		t.Fatalf("the prepared machine was recorded %q, want hot", decision.imageLocality())
	}
	if seconds := decision.pullEstimate().Expected; seconds != 0 {
		t.Fatalf("the prepared machine was priced %.2f pull seconds, want zero", seconds)
	}
	if launched := fleet.runtime.launchedRuns(); len(launched) != 1 {
		t.Fatalf("the machine ran %v, and the image it is warm for was never one of them", launched)
	}
}

// holdsImageAlready puts content on the machine by hand and waits for the fleet
// to have heard about it, which is what an operator's own `docker pull` leaves
// behind.
func (f *fleet) holdsImageAlready(t *testing.T, digest string) {
	t.Helper()
	f.runtime.hold(digest, domain.Platform{OS: "linux", Architecture: "amd64"}, []string{trainerBaseDiffID, trainerTopDiffID})
	waitFor(t, func() bool {
		return f.nodeOffer(t).Images.Holds(digest)
	}, "the machine never reported the image it was given")
}

// prepare drives one reconciliation of the desired preparation set, which is what
// the production sweep does once a minute. Preparation also happens on its own
// when a Booking, a launch, a cancellation, or a closure changes what Mercator
// wants prepared, so a case that sweeps is stating that the answer holds however
// often Mercator looks rather than that looking is the only way it happens.
func (f *fleet) prepare(t *testing.T) {
	t.Helper()
	if _, err := f.control.ReconcileWorkspace(context.Background(), daemon.DefaultWorkspaceID); err != nil {
		t.Fatalf("reconcile the workspace: %v", err)
	}
}

// prepareUntil sweeps until the condition holds. Mercator refuses to prepare a
// machine still getting ready for work it has already admitted there, and that
// readiness is a moment in wall-clock time, so the first sweeps after a launch
// are expected to ask for nothing.
func (f *fleet) prepareUntil(t *testing.T, satisfied func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		f.prepare(t)
		if satisfied() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal(message)
}

// TestAMachineIsAskedAgainForContentItRefused is the production half of the
// state-aware dedupe, through the real node protocol and the SQLite operation
// store the control plane keeps its commands in.
//
// The machine turns the pull away and says so, which leaves nothing on its disk.
// The Run that wanted the content is withdrawn and another arrives wanting the
// same content on the same machine, so Mercator asks again under the identity it
// asked under the first time: the identity is the machine and the content, and
// nothing about it changed. That ask has to reach the agent. The record of a pull
// that failed answering it Duplicate is the defect this closes, and it is
// invisible from outside except as a machine that never becomes warm.
func TestAMachineIsAskedAgainForContentItRefused(t *testing.T) {
	fleet := startFleet(t, preparingAt(orchestrator.PrewarmPolicy{MaxConcurrent: 1}))
	fleet.holdsImageAlready(t, trainerIndexDigest)
	running := fleet.submitRun(t)
	fleet.runtime.awaitLaunch(t, running)
	fleet.runtime.refuseNextPullOf(rebuiltIndexDigest)

	withdrawn := fleet.submitRunFor(t, fleet.rebuiltImage)
	fleet.prepareUntil(t, func() bool {
		return len(fleet.runtime.preparedImages()) > 0
	}, "the queued Run's host was never asked to prepare anything")
	fleet.cancelRun(t, withdrawn)
	fleet.prepare(t)
	fleet.submitRunFor(t, fleet.rebuiltImage)

	fleet.prepareUntil(t, func() bool {
		return fleet.nodeOffer(t).Images.Holds(rebuiltIndexDigest)
	}, "the machine never held content it refused once and was asked for again")

	asked := fleet.runtime.preparedImages()
	if len(asked) != 2 || asked[0] != rebuiltIndexDigest || asked[1] != rebuiltIndexDigest {
		t.Fatalf("the machine was asked to prepare %v, want the refused content asked for twice", asked)
	}
}

// cancelRun withdraws work the way an operator does, which is what makes the
// desired preparation set change: content nothing is waiting for is content
// Mercator stops asking for.
func (f *fleet) cancelRun(t *testing.T, runID string) {
	t.Helper()
	f.call(t, http.MethodPost, "/v1/runs/"+runID+"/cancel?workspace_id="+daemon.DefaultWorkspaceID, nil, nil, http.StatusOK)
}
