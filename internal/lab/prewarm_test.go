package lab

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
)

const (
	prewarmBlueprint     = "prewarming-never-starves-real-work"
	rateBoundBlueprint   = "prewarming-holds-its-own-rate-bound"
	fleetBoundBlueprint  = "prewarming-bounds-the-whole-fleet"
	fleetBudgetBlueprint = "prewarming-spends-one-budget-across-tenants"
	analystImage         = "analyst@sha256:7a1c4e9b2d6f8a0c3e5b7d9f1a3c5e7b9d1f3a5c7e9b1d3f5a7c9e1b3d5f7a9c"
	bulkyImage           = "bulky@sha256:1b3d5f7a9c1e3b5d7f9a1c3e5b7d9f1a3c5e7b9d1f3a5c7e9b1d3f5a7c9e1b3d"
	auditorImage         = "auditor@sha256:5c7e9b1d3f5a7c9e1b3d5f7a9c1e3b5d7f9a1c3e5b7d9f1a3c5e7b9d1f3a5c7e"
)

// driveBlueprintForEightyMinutes runs a Blueprint at the cadence a control plane
// reconciles at, one virtual minute at a time. Preparation is a controller rather
// than a step in any Run's lifecycle, so nothing about it happens unless the
// clock moves and Mercator looks again. Every fixture here puts the moments it is
// about between two of those ticks, so the harness cannot produce them.
func driveBlueprintForEightyMinutes(t *testing.T, name string) *Execution {
	t.Helper()
	execution := openConformanceExecution(t, name)
	t.Cleanup(func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	})
	for range 80 {
		if _, err := execution.Drive(context.Background(), Advance(time.Minute)); err != nil {
			t.Fatalf("drive the execution: %v", err)
		}
	}
	return execution
}

// TestPreparationWaitsForTheRunAlreadyAdmittedThere is the safety claim, read
// off the ledger rather than off the rule that polices it. The admitted Run's
// forty gigabytes are moving from the moment it is launched; nothing speculative
// may be moving onto that machine until they have landed, because a node
// performs its commands in order and both fetches cross one link.
func TestPreparationWaitsForTheRunAlreadyAdmittedThere(t *testing.T) {
	execution := driveBlueprintForEightyMinutes(t, prewarmBlueprint)

	prefetches := prefetchStarts(t, execution)
	if len(prefetches) != 3 {
		t.Fatalf("the ledger records %d preparations, want the image, the dataset, and the withdrawn one: %+v", len(prefetches), prefetches)
	}
	admitted := admittedPulls(t, execution)
	if len(admitted) == 0 {
		t.Fatal("no Run in this execution ever fetched anything, so nothing could have been starved")
	}
	for _, prefetch := range prefetches {
		for _, pull := range admitted {
			if prefetch.OfferID != pull.OfferID {
				continue
			}
			if prefetch.At.Before(pull.CompletesAt) {
				t.Fatalf(
					"%q began preparing %q at %s while an admitted Run was waiting for %q until %s",
					prefetch.OfferID, prefetch.Content, prefetch.At, pull.Image, pull.CompletesAt,
				)
			}
		}
	}
}

// TestOnePieceOfContentIsPreparedAtATime is the depth half. The Blueprint allows
// one speculative transfer in flight and nothing below the control plane
// enforces that: a machine asked for both at once fetches both.
//
// The rate half is deliberately not read here. This world's Runs arrive on
// minute boundaries and the harness advances a minute at a time, so a gap of a
// minute between two preparations is something the cadence produces rather than
// something MinInterval holds: an assertion on it would pass with the bound
// switched off. The bound is stated where it can fail, in
// prewarming-holds-its-own-rate-bound.
func TestOnePieceOfContentIsPreparedAtATime(t *testing.T) {
	execution := driveBlueprintForEightyMinutes(t, prewarmBlueprint)

	if _, err := execution.Check(context.Background()); err != nil {
		t.Fatalf("the execution violates a standing rule: %v", err)
	}
}

// TestASecondSpeculativeFetchWaitsOutTheRateBound is the rate claim, on a world
// built so nothing but the bound can produce the gap. The machine already holds
// the image, so the first desired set is one Artifact and Mercator asks for it a
// minute in. The third Run arrives ninety seconds later wanting a version whose
// name is a prefix of the one already asked for, which is the shape that made a
// control plane comparing desires as text read new content as content it had
// already requested and skip the bound. It arrives between two ticks, so the
// harness cannot supply the gap either.
func TestASecondSpeculativeFetchWaitsOutTheRateBound(t *testing.T) {
	execution := driveBlueprintForEightyMinutes(t, rateBoundBlueprint)

	starts := prefetchStarts(t, execution)
	if len(starts) != 2 {
		t.Fatalf("the ledger records %d preparations, want one per corpus version: %+v", len(starts), starts)
	}
	if starts[0].Content != "artifact:corpus:v70" || starts[1].Content != "artifact:corpus:v7" {
		t.Fatalf("the machine prepared %q then %q, want the queued Run's version first", starts[0].Content, starts[1].Content)
	}
	if gap := starts[1].At.Sub(starts[0].At); gap < 5*time.Minute {
		t.Fatalf(
			"preparation of %q started %s after preparation of %q, and this world allows one no sooner than 5m",
			starts[1].Content, gap, starts[0].Content,
		)
	}
	if _, err := execution.Check(context.Background()); err != nil {
		t.Fatalf("the execution violates a standing rule: %v", err)
	}
}

// TestTheSecondTenantWaitsForTheFleetsOneTransfer is the multi-tenant claim.
// Both bounds belong to the fleet: what they protect is a machine's link and
// this process's egress, and a second tenant arriving ninety seconds after the
// first shares both. Its corpus is wanted on a different machine, and it still
// waits.
func TestTheSecondTenantWaitsForTheFleetsOneTransfer(t *testing.T) {
	execution := driveBlueprintForEightyMinutes(t, fleetBoundBlueprint)

	starts := prefetchStarts(t, execution)
	if len(starts) != 2 {
		t.Fatalf("the ledger records %d preparations, want one per tenant: %+v", len(starts), starts)
	}
	if starts[0].Content != "artifact:corpus-alpha:v1" || starts[1].Content != domain.ReferenceDigest(auditorImage) {
		t.Fatalf(
			"the fleet prepared %q then %q, want the tenant whose Run starts soonest first",
			starts[0].Content, starts[1].Content,
		)
	}
	if starts[0].OfferID == starts[1].OfferID {
		t.Fatalf("both preparations landed on %q, and this fixture is about two machines", starts[0].OfferID)
	}
	if gap := starts[1].At.Sub(starts[0].At); gap < 5*time.Minute {
		t.Fatalf(
			"the second tenant's corpus started %s after the first tenant's, and this world allows one no sooner than 5m",
			gap,
		)
	}
	if _, err := execution.Check(context.Background()); err != nil {
		t.Fatalf("the execution violates a standing rule: %v", err)
	}
}

// TestOneDepthBudgetIsSpentAcrossTenants is the depth half of the same claim,
// on a world that states no interval at all. Two tenants want twenty gigabytes
// each on their own machine at the same minute, and one slot exists: the Run
// that starts soonest gets it, and the other tenant's transfer begins when that
// one has landed. Nothing here is the rate bound holding it, because this world
// states none.
func TestOneDepthBudgetIsSpentAcrossTenants(t *testing.T) {
	execution := driveBlueprintForEightyMinutes(t, fleetBudgetBlueprint)

	starts := prefetchStarts(t, execution)
	if len(starts) != 2 {
		t.Fatalf("the ledger records %d preparations, want one per tenant: %+v", len(starts), starts)
	}
	if starts[0].Content != "artifact:corpus-alpha:v1" || starts[1].Content != domain.ReferenceDigest(auditorImage) {
		t.Fatalf(
			"the fleet prepared %q then %q, want the tenant whose Run starts soonest first",
			starts[0].Content, starts[1].Content,
		)
	}
	if _, err := execution.Check(context.Background()); err != nil {
		t.Fatalf("the execution violates a standing rule: %v", err)
	}
}

// TestAPreparedHostIsWarmForARunThatNeverExecutedThere is the whole point of the
// capability. The third Run's decision prices this machine at zero pull seconds
// and a checked copy of its dataset, and the machine has run neither: what it
// holds arrived because Mercator asked for it on behalf of a Run still waiting.
func TestAPreparedHostIsWarmForARunThatNeverExecutedThere(t *testing.T) {
	execution := driveBlueprintForEightyMinutes(t, prewarmBlueprint)

	decision := bookingDecisions(t, execution)["run-curious"]
	if decision.SelectedOfferSnapshotID != "builder" {
		t.Fatalf("the third Run landed on %q, want the prepared machine", decision.SelectedOfferSnapshotID)
	}
	candidate := candidateDecision(t, decision, "builder")
	if candidate.Estimates.PullSeconds.Expected != 0 || candidate.ImageLocality != domain.LocalityHot {
		t.Fatalf(
			"the prepared machine was priced %.2f pull seconds and recorded %q, want zero on a host holding the image whole",
			candidate.Estimates.PullSeconds.Expected, candidate.ImageLocality,
		)
	}
	if candidate.Estimates.ArtifactSeconds.Expected != 0 {
		t.Fatalf(
			"the prepared machine was priced %.2f seconds of Artifact read, want zero on a host holding a checked copy",
			candidate.Estimates.ArtifactSeconds.Expected,
		)
	}
	// Warming by preparation and warming by execution are different facts about
	// a machine, and the ledger has to be able to tell them apart: this host ran
	// one workload and it was neither of the two the third Run needs.
	for _, retained := range retentions(t, execution) {
		if retained.Image == analystImage && retained.Source != "prewarm" {
			t.Fatalf("the machine holds %q because of a %q, want it prepared rather than executed", retained.Image, retained.Source)
		}
	}
}

// TestPreparationStopsWhenTheRunThatWantedItGoesAway is the third claim. The
// fourth Run is withdrawn eight minutes into a sixteen-minute fetch, and the
// machine stops: the room goes back, the link goes quiet, and nothing of that
// image is ever held here.
func TestPreparationStopsWhenTheRunThatWantedItGoesAway(t *testing.T) {
	execution := driveBlueprintForEightyMinutes(t, prewarmBlueprint)

	abandoned := abandonedPreparations(t, execution)
	if len(abandoned) != 1 {
		t.Fatalf("the ledger records %d withdrawals, want the one Run whose caller withdrew it: %+v", len(abandoned), abandoned)
	}
	if abandoned[0].Content != domain.ReferenceDigest(bulkyImage) {
		t.Fatalf("the withdrawal stopped %q, want the image only the cancelled Run needed", abandoned[0].Content)
	}
	if abandoned[0].ReleasedBytes <= 0 {
		t.Fatalf("the withdrawal released %d bytes, and this transfer had reserved room", abandoned[0].ReleasedBytes)
	}
	truth := execution.runtime.world.truthSnapshot()
	for _, ledger := range truth.Disk {
		if ledger.OfferID != "builder" {
			continue
		}
		if ledger.ReservedBytes != 0 {
			t.Fatalf("the machine still reserves %d bytes after the work that wanted them went away", ledger.ReservedBytes)
		}
		if ledger.holds(ResidentLayer, "sha256:bb22cc33dd44ee55ff66aa77bb88cc99dd00ee11ff22aa33bb44cc55dd66aa77") {
			t.Fatal("the machine kept the layer of a Run that never ran")
		}
	}
}

// TestABackgroundPreparationLoopTripsNoDispatcherDetector is the claim that a
// controller nothing is waiting on can run beside the Runs without making the
// execution undecidable. The dispatcher refuses a livelock, a timestamp it
// cannot get past, and a transition budget it has spent, and a loop that
// reconciled a desired set on every tick is exactly the shape that trips all
// three. This execution runs under the tight limits every Lab case here uses,
// which are a hundredth of the production ones.
func TestABackgroundPreparationLoopTripsNoDispatcherDetector(t *testing.T) {
	execution := openConformanceExecution(t, prewarmBlueprint)
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	for range 80 {
		checkpoint, err := execution.Drive(context.Background(), Advance(time.Minute))
		for _, detector := range []error{ErrLivelock, ErrSameTimestampLimit, ErrTransitionLimit, ErrVirtualTimeLimit} {
			if errors.Is(err, detector) {
				t.Fatalf("the preparation loop tripped %v at %s", detector, checkpoint.Now)
			}
		}
		if err != nil {
			t.Fatalf("drive the execution: %v", err)
		}
	}
}

// preparationStart is one speculative fetch as the ledger records it.
type preparationStart struct {
	At      time.Time `json:"-"`
	OfferID string    `json:"offer_id"`
	Content string    `json:"content"`
	RunID   string    `json:"run_id"`
}

func prefetchStarts(t *testing.T, execution *Execution) []preparationStart {
	t.Helper()
	var starts []preparationStart
	for _, effect := range execution.runtime.world.effectRecords() {
		if effect.Operation != OperationNodePrepareImage && effect.Operation != OperationNodePrepareArtifact {
			continue
		}
		if effect.Command != EffectCommandAccepted {
			continue
		}
		var request preparationStart
		if err := json.Unmarshal(effect.Request, &request); err != nil {
			t.Fatalf("decode preparation %s: %v", effect.ID, err)
		}
		request.At = effect.At
		starts = append(starts, request)
	}
	return starts
}

type admittedPull struct {
	OfferID     string    `json:"offer_id"`
	Image       string    `json:"image"`
	CompletesAt time.Time `json:"-"`
}

func admittedPulls(t *testing.T, execution *Execution) []admittedPull {
	t.Helper()
	var pulls []admittedPull
	for _, effect := range execution.runtime.world.effectRecords() {
		if effect.Operation != OperationImagePull || effect.Command != EffectCommandAccepted {
			continue
		}
		var request admittedPull
		var moved struct {
			CompletesAt  time.Time `json:"completes_at"`
			FetchedBytes int64     `json:"fetched_bytes"`
		}
		if err := json.Unmarshal(effect.Request, &request); err != nil {
			t.Fatalf("decode pull %s: %v", effect.ID, err)
		}
		if err := json.Unmarshal(effect.Consequence, &moved); err != nil {
			t.Fatalf("decode pull consequence %s: %v", effect.ID, err)
		}
		if moved.FetchedBytes == 0 {
			continue
		}
		request.CompletesAt = moved.CompletesAt
		pulls = append(pulls, request)
	}
	return pulls
}

type retention struct {
	Image   string `json:"image"`
	OfferID string `json:"offer_id"`
	Source  string `json:"source"`
}

func retentions(t *testing.T, execution *Execution) []retention {
	t.Helper()
	var kept []retention
	for _, effect := range execution.runtime.world.effectRecords() {
		if effect.Operation != OperationImageRetained || effect.Command != EffectCommandAccepted {
			continue
		}
		var request retention
		if err := json.Unmarshal(effect.Request, &request); err != nil {
			t.Fatalf("decode retention %s: %v", effect.ID, err)
		}
		kept = append(kept, request)
	}
	return kept
}

type abandonedPreparation struct {
	OfferID       string `json:"offer_id"`
	Content       string `json:"content"`
	ReleasedBytes int64  `json:"-"`
}

func abandonedPreparations(t *testing.T, execution *Execution) []abandonedPreparation {
	t.Helper()
	var abandoned []abandonedPreparation
	for _, effect := range execution.runtime.world.effectRecords() {
		if effect.Operation != OperationNodePrepareAbandoned {
			continue
		}
		var request abandonedPreparation
		var released struct {
			ReleasedBytes int64 `json:"released_bytes"`
		}
		if err := json.Unmarshal(effect.Request, &request); err != nil {
			t.Fatalf("decode withdrawal %s: %v", effect.ID, err)
		}
		if err := json.Unmarshal(effect.Consequence, &released); err != nil {
			t.Fatalf("decode withdrawal consequence %s: %v", effect.ID, err)
		}
		request.ReleasedBytes = released.ReleasedBytes
		abandoned = append(abandoned, request)
	}
	return abandoned
}

func candidateDecision(t *testing.T, decision domain.BookingDecision, offerID string) domain.CandidateDecision {
	t.Helper()
	for _, candidate := range decision.Candidates {
		if candidate.OfferSnapshotID == offerID {
			return candidate
		}
	}
	t.Fatalf("Run %q has no candidate for offer %q", decision.RunID, offerID)
	return domain.CandidateDecision{}
}
