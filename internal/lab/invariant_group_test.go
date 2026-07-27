package lab

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/orchestrator"
	"github.com/benngarcia/mercator/internal/scenario"
)

// TestAFamilyOfEightRunsThreeAtATime is the claim the placement corpus states one
// moment of, driven to the end under the real control plane. A fixture can show the
// fourth member being told to wait; only an execution can show the family draining
// three at a time and every member of it running.
//
// The peak is read off the launch ledger rather than off anything Mercator keeps,
// which is what the law does and what makes the assertion worth making: a counter
// that agreed with itself would prove nothing about how many containers existed.
func TestAFamilyOfEightRunsThreeAtATime(t *testing.T) {
	execution := openConformanceExecution(t, "a-group-of-eight-runs-three-at-a-time")
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	if _, err := execution.DriveToCompletion(context.Background()); err != nil {
		t.Fatalf("drive the execution: %v", err)
	}

	// Every member ran. A family held to its width by never running is a family
	// starved rather than bounded, which is the failure mode the bound has to be
	// held apart from.
	for index := 1; index <= 8; index++ {
		member := memberRunID(index)
		record := projectedRun(t, execution, member)
		if record.Outcome != domain.RunOutcomeSucceeded {
			t.Fatalf("member %q ended %q in phase %q, and every member of this family is supposed to run", member, record.Outcome, record.Phase)
		}
	}
	// And never more than three at once, counted over the launches the world
	// accepted and the cleanups that ended them.
	if peak := peakConcurrentLaunches(t, execution); peak != 3 {
		t.Fatalf("the ledger says %d members held capacity at once, and the family declared three", peak)
	}
	// The wait is the bound rather than a fleet that ran out, and the record says so
	// in the words an operator reads: the family's own name for what is holding it,
	// and the three siblings that hold it.
	waiting := deferralRecord(t, execution, "run-member-004")
	if waiting.Reason != domain.DeferredGroupAtParallelism {
		t.Fatalf("the fourth member waited on %q, and what held it is the width its caller declared", waiting.Reason)
	}
	if waiting.Fleet != nil {
		t.Fatalf("the fourth member's wait carries a fleet answer weighing %d machines, and no machine was weighed for it", waiting.Fleet.Weighed)
	}
	holders := queuedAheadRunIDs(waiting.Behind)
	if !slices.Equal(holders, []string{"run-member-001", "run-member-002", "run-member-003"}) {
		t.Fatalf("the fourth member is recorded waiting behind %v, and what holds it is the three members with capacity", holders)
	}
	// A machine was standing idle throughout, which is what makes this a bound on
	// the family rather than on the fleet.
	if launched := launchedOffers(t, execution); slices.Contains(launched, "rental-4") {
		t.Fatalf("the family ran on %v, and the fourth machine is the capacity it was not allowed to use", launched)
	}
	if _, err := execution.Check(context.Background()); err != nil {
		t.Fatalf("check invariants: %v", err)
	}
	if result := invariantResultByID(t, latestInvariantResults(execution.invariants), "safety.group_parallelism_respected"); result.Status != InvariantPassed {
		t.Fatalf("the group law reports %+v", result)
	}
}

// TestAFamilyNarrowerThanItsClassPatienceStillDrains is the wait a caller's own
// declaration causes, held to the bound that belongs to the caller and not to the
// one Mercator states about capacity. A family of three one wide, forty minutes a
// member, takes two hours to drain, and its members declared a class that says
// Mercator may keep work waiting an hour. So the third member is held by its own
// siblings for longer than that bound, and it waits and then runs.
//
// The sweep is what makes this an execution rather than a moment. Admission refuses
// a wait past its bound only when it is asked again while the wait is still on, and
// nothing in the corpus reaches that: the driver advances to the next thing the
// world does, which is always the moment room appears, so a held member is asked
// exactly when its family makes way for it. Advancing to the middle of a member's
// runtime is the minute sweep production runs, and it is the only way this tree can
// ask a held Run a question at a moment nothing else is happening.
func TestAFamilyNarrowerThanItsClassPatienceStillDrains(t *testing.T) {
	execution := openConformanceExecution(t, "a-family-narrower-than-its-class-patience-still-drains")
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	// Forty-five minutes in, the first member is over and the second holds the
	// family's one place. The third has waited three quarters of an hour, which is
	// inside the hour its class states.
	if _, err := execution.Drive(context.Background(), Advance(45*time.Minute)); err != nil {
		t.Fatalf("drive the family to its second member: %v", err)
	}
	held := deferralRecord(t, execution, memberRunID(3))
	if held.Reason != domain.DeferredGroupAtParallelism {
		t.Fatalf("the third member waits on %q, and what holds it is the width its caller declared", held.Reason)
	}
	// Seventy minutes in, it has waited past the whole queue delay its class states,
	// and this is the sweep that used to close it as a failed Run.
	checkpoint, err := execution.Drive(context.Background(), Advance(25*time.Minute))
	if err != nil {
		t.Fatalf("drive the family past its class's queue delay: %v", err)
	}
	record := projectedRun(t, execution, memberRunID(3))
	if record.QueuedSince == nil {
		t.Fatalf("the third member is %q and the record says nothing about it waiting", record.Phase)
	}
	waited := checkpoint.Now.Sub(record.QueuedSince.UTC()).Seconds()
	bound := record.ServiceClass.Admission().MaxQueueDelaySeconds
	if waited <= bound {
		t.Fatalf("the third member had waited %.0fs against the %.0fs its class allows, and this case is about a wait longer than that", waited, bound)
	}
	if record.Closed {
		t.Fatalf("the third member is %q with outcome %q after waiting %.0fs, and what kept it waiting is its own family rather than any promise Mercator made",
			record.Phase, record.Outcome, waited)
	}

	if _, err := execution.DriveToCompletion(context.Background()); err != nil {
		t.Fatalf("drive the execution: %v", err)
	}

	// Every member ran, one at a time, and the second machine was never taken:
	// the family was held by its own width throughout and by nothing else.
	for index := 1; index <= 3; index++ {
		member := memberRunID(index)
		record := projectedRun(t, execution, member)
		if record.Outcome != domain.RunOutcomeSucceeded {
			t.Fatalf("member %q ended %q in phase %q, and a family narrower than its class's patience still runs", member, record.Outcome, record.Phase)
		}
	}
	if peak := peakConcurrentLaunches(t, execution); peak != 1 {
		t.Fatalf("the ledger says %d members held capacity at once, and the family declared one", peak)
	}
	if launched := launchedOffers(t, execution); slices.Contains(launched, "rental-2") {
		t.Fatalf("the family ran on %v, and the second machine is the capacity it was not allowed to use", launched)
	}
	if _, err := execution.Check(context.Background()); err != nil {
		t.Fatalf("check invariants: %v", err)
	}
	if result := invariantResultByID(t, latestInvariantResults(execution.invariants), "safety.group_parallelism_respected"); result.Status != InvariantPassed {
		t.Fatalf("the group law reports %+v", result)
	}
}

// TestAPreemptibleRunIsTheOneInterrupted is the world taking capacity back, which
// is the first thing in this corpus that happens to Mercator rather than because of
// it. Two Runs differ in one thing, what their class says about being interrupted,
// and the fleet offers a cheap machine that can be reclaimed and a dear one that
// cannot.
//
// The interruption is read out of the world's own ledger, and the permission out of
// the workload Mercator recorded. Neither Run's outcome would say it: an execution
// whose machine went away and an execution that failed on its own look the same from
// the Run's record, which is exactly why the reclamation is a fact the ledger states.
func TestAPreemptibleRunIsTheOneInterrupted(t *testing.T) {
	execution := openConformanceExecution(t, "a-preemptible-run-is-the-one-interrupted")
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	if _, err := execution.DriveToCompletion(context.Background()); err != nil {
		t.Fatalf("drive the execution: %v", err)
	}

	// The Run that may not be interrupted was refused both machines that can be
	// taken back, and the refusal names what it was refused for.
	decision := bookingDecisions(t, execution)["run-trainer"]
	for _, offerID := range []string{"rental-spot-a", "rental-spot-b"} {
		candidate := candidateFor(t, decision, offerID)
		if candidate.Feasible {
			t.Fatalf("the Run that may not be interrupted was offered %q, which its provider can take back", offerID)
		}
		if candidate.Rejections[0].Code != "INTERRUPTION_NOT_PERMITTED" || candidate.Rejections[0].Path != "reclaimable" {
			t.Fatalf("%q was refused as %+v, and what refused it is the term the capacity was sold on", offerID, candidate.Rejections[0])
		}
	}
	if decision.SelectedOfferSnapshotID != "rental-held" {
		t.Fatalf("the Run that may not be interrupted was placed on %q, and the only machine nobody can reclaim is rental-held", decision.SelectedOfferSnapshotID)
	}
	// The world took both reclaimable machines back, and the only work it
	// interrupted is the Run whose class permits that.
	interrupted := interruptedRunIDs(t, execution)
	if !slices.Equal(interrupted, []string{"run-sweeper"}) {
		t.Fatalf("the reclamation interrupted %v, and the only Run that may be interrupted is run-sweeper", interrupted)
	}
	// The case is only about permission if the Run that had it lost something. A
	// reclamation that took an idle machine asserts nothing.
	sweeper := projectedRun(t, execution, "run-sweeper")
	if sweeper.Outcome == domain.RunOutcomeSucceeded {
		t.Fatalf("the interrupted Run ended %q, so this execution reclaimed a machine that had already finished its work", sweeper.Outcome)
	}
	trainer := projectedRun(t, execution, "run-trainer")
	if trainer.Outcome != domain.RunOutcomeSucceeded {
		t.Fatalf("the Run on capacity nobody can reclaim ended %q in phase %q", trainer.Outcome, trainer.Phase)
	}
	if _, err := execution.Check(context.Background()); err != nil {
		t.Fatalf("check invariants: %v", err)
	}
	if result := invariantResultByID(t, latestInvariantResults(execution.invariants), "safety.interruption_was_permitted"); result.Status != InvariantPassed {
		t.Fatalf("the interruption law reports %+v", result)
	}
}

// TestAFamilyWiderThanItDeclaredIsAViolation is the deliberate failure of the
// second half of the group law: four members of a family of three holding capacity
// at one moment, which is what admission not asking about the family produces.
func TestAFamilyWiderThanItDeclaredIsAViolation(t *testing.T) {
	observation := familyObservation(map[string]domain.RunGroup{
		"run-a": {ID: "sweep", MaxParallel: 3},
		"run-b": {ID: "sweep", MaxParallel: 3},
		"run-c": {ID: "sweep", MaxParallel: 3},
		"run-d": {ID: "sweep", MaxParallel: 3},
	}, launchEffect(1, "run-a"), launchEffect(2, "run-b"), launchEffect(3, "run-c"), launchEffect(4, "run-d"))

	err := groupParallelismRespected(observation)
	if err == nil {
		t.Fatal("four members of a family of three held capacity at once, and the rule allowed it")
	}
	t.Logf("violation: %v", err)
}

// TestAFamilyThatMadeRoomForItsNextMemberIsNoViolation is what the rule has to
// leave alone, and it is the whole of the execution above: a member finishes, its
// capacity is released, and the next one launches into the room it left.
func TestAFamilyThatMadeRoomForItsNextMemberIsNoViolation(t *testing.T) {
	observation := familyObservation(map[string]domain.RunGroup{
		"run-a": {ID: "sweep", MaxParallel: 2},
		"run-b": {ID: "sweep", MaxParallel: 2},
		"run-c": {ID: "sweep", MaxParallel: 2},
	}, launchEffect(1, "run-a"), launchEffect(2, "run-b"), releaseEffect(3, "run-a"), launchEffect(4, "run-c"))

	if err := groupParallelismRespected(observation); err != nil {
		t.Fatalf("a family that waited for its own capacity to come back was read as one that ran too wide: %v", err)
	}
}

// TestAFamilyDeclarationMercatorNeverRecordedIsAViolation is the deliberate failure
// of the first half, and it is the state this bound was in for a phase: the family
// reached the World Tape and the workload the control plane stored had no field to
// carry it, so nothing downstream could hold anything to it.
func TestAFamilyDeclarationMercatorNeverRecordedIsAViolation(t *testing.T) {
	observation := familyObservation(map[string]domain.RunGroup{"run-a": {}})
	observation.RunRequirements = map[string]RunArrival{
		"run-a": {Name: "a", Request: scenarioRequestInFamily("sweep", 3)},
	}

	err := groupParallelismRespected(observation)
	if err == nil {
		t.Fatal("a Run was submitted into a family of three and recorded as a member of nothing, and the rule allowed it")
	}
	t.Logf("violation: %v", err)
}

// TestAnInterruptionTheClassForbadeIsAViolation is the deliberate failure of the
// interruption law: the world reclaims a machine and the work on it belongs to a
// class that said it may not be interrupted, which is what dropping the feasibility
// refusal produces.
func TestAnInterruptionTheClassForbadeIsAViolation(t *testing.T) {
	observation := classObservation(map[string]domain.ServiceClass{"run-watched": domain.ClassInteractive})
	observation.Effects = []EffectRecord{preemptionEffect(1, "rental-spot", "run-watched")}

	err := interruptionWasPermitted(observation)
	if err == nil {
		t.Fatal("interactive work was interrupted by a reclamation, and the rule allowed it")
	}
	t.Logf("violation: %v", err)
}

// TestAnInterruptionTheClassPermittedIsNoViolation is the other side of the same
// reclamation. A class that would rather be cheap than certain is a caller saying
// the work can be redone, and taking its machine back is the bargain rather than a
// breach of it.
func TestAnInterruptionTheClassPermittedIsNoViolation(t *testing.T) {
	observation := classObservation(map[string]domain.ServiceClass{"run-spare": domain.ClassBatch})
	observation.Effects = []EffectRecord{preemptionEffect(1, "rental-spot", "run-spare")}

	if err := interruptionWasPermitted(observation); err != nil {
		t.Fatalf("batch work permits interruption and the rule called its reclamation a violation: %v", err)
	}
}

func memberRunID(index int) string {
	return fmt.Sprintf("run-member-%03d", index)
}

// deferralRecord is the last thing admission said about one Run waiting.
func deferralRecord(t *testing.T, execution *Execution, runID string) domain.AdmissionDeferral {
	t.Helper()
	var latest *domain.AdmissionDeferral
	for _, event := range publicRunEvents(t, execution) {
		if event.Type != orchestrator.EventAdmissionDeferred || !strings.HasSuffix(event.StreamID, runID) {
			continue
		}
		var payload struct {
			Deferral domain.AdmissionDeferral `json:"deferral"`
		}
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			t.Fatalf("read the deferral: %v", err)
		}
		deferral := payload.Deferral
		latest = &deferral
	}
	if latest == nil {
		t.Fatalf("admission never told Run %q to wait", runID)
	}
	return *latest
}

// workloadInFamily is a recorded workload whose Run belongs to one family.
func workloadInFamily(id string, width int) domain.WorkloadRevision {
	return domain.WorkloadRevision{
		Spec: domain.WorkloadSpec{Placement: domain.PlacementPolicy{
			Class: domain.ClassBatch,
			Group: domain.RunGroup{ID: id, MaxParallel: width},
		}},
	}
}

// scenarioRequestInFamily is what a Blueprint submits for a member of one family.
func scenarioRequestInFamily(id string, width int) scenario.RequestSpec {
	return scenario.RequestSpec{Group: domain.RunGroup{ID: id, MaxParallel: width}}
}

// peakConcurrentLaunches is the most executions the world held at once, counted the
// way the law counts them: opened by a launch the world accepted and closed by the
// cleanup that ended it.
func peakConcurrentLaunches(t *testing.T, execution *Execution) int {
	t.Helper()
	holding := map[string]bool{}
	peak := 0
	for _, effect := range execution.runtime.world.effectRecords() {
		if effect.Command != EffectCommandAccepted {
			continue
		}
		switch effect.Operation {
		case OperationProviderLaunch:
			holding[effect.CorrelationID] = true
			peak = max(peak, len(holding))
		case OperationProviderRelease, OperationProviderTerminate:
			delete(holding, effect.CorrelationID)
		}
	}
	return peak
}

// launchedOffers is every machine this execution ran something on.
func launchedOffers(t *testing.T, execution *Execution) []string {
	t.Helper()
	var offers []string
	for _, effect := range execution.runtime.world.effectRecords() {
		if effect.Operation != OperationProviderLaunch || effect.Command != EffectCommandAccepted {
			continue
		}
		var request struct {
			OfferID string `json:"offer_id"`
		}
		if err := json.Unmarshal(effect.Request, &request); err != nil {
			t.Fatalf("read the launch request: %v", err)
		}
		if !slices.Contains(offers, request.OfferID) {
			offers = append(offers, request.OfferID)
		}
	}
	slices.Sort(offers)
	return offers
}

// interruptedRunIDs is every Run the world's reclamations took a machine away from.
func interruptedRunIDs(t *testing.T, execution *Execution) []string {
	t.Helper()
	var runIDs []string
	for _, effect := range execution.runtime.world.effectRecords() {
		if effect.Operation != OperationCapacityPreempted || effect.Command != EffectCommandAccepted {
			continue
		}
		interrupted, err := interruptedRuns(effect)
		if err != nil {
			t.Fatalf("read the reclamation: %v", err)
		}
		for _, lost := range interrupted {
			runIDs = append(runIDs, lost.RunID)
		}
	}
	slices.Sort(runIDs)
	return runIDs
}

func queuedAheadRunIDs(ahead []domain.QueuedAhead) []string {
	runIDs := make([]string, 0, len(ahead))
	for _, holder := range ahead {
		runIDs = append(runIDs, holder.RunID)
	}
	slices.Sort(runIDs)
	return runIDs
}

// familyObservation is a record in which each Run belongs to the family named, and
// the ledger holds the effects given. The submitted declaration is built from the
// recorded one, because every rule but the first here is about what Mercator did
// with a declaration it holds.
func familyObservation(groups map[string]domain.RunGroup, effects ...EffectRecord) InvariantObservation {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	workloads := map[string]domain.WorkloadRevision{}
	requirements := map[string]RunArrival{}
	for runID, group := range groups {
		workloads[runID] = domain.WorkloadRevision{
			ID:   "wrev_" + runID,
			Spec: domain.WorkloadSpec{Placement: domain.PlacementPolicy{Class: domain.ClassBatch, Group: group}},
		}
		requirements[runID] = RunArrival{
			Name:    runID,
			Request: scenarioRequestInFamily(group.ID, group.MaxParallel),
		}
	}
	return InvariantObservation{
		StartedAt:       now,
		Now:             now,
		World:           WorldTruthSnapshot{At: now},
		Workloads:       workloads,
		RunRequirements: requirements,
		Effects:         effects,
	}
}

// classObservation is a record in which each Run is of the class named.
func classObservation(classes map[string]domain.ServiceClass) InvariantObservation {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	workloads := map[string]domain.WorkloadRevision{}
	for runID, class := range classes {
		workloads[runID] = domain.WorkloadRevision{
			ID:   "wrev_" + runID,
			Spec: domain.WorkloadSpec{Placement: domain.PlacementPolicy{Class: class}},
		}
	}
	return InvariantObservation{
		StartedAt: now,
		Now:       now,
		World:     WorldTruthSnapshot{At: now},
		Workloads: workloads,
	}
}

func launchEffect(sequence uint64, runID string) EffectRecord {
	return EffectRecord{
		Sequence:      sequence,
		Operation:     OperationProviderLaunch,
		Command:       EffectCommandAccepted,
		CorrelationID: runID,
		Request:       mustJSON(map[string]any{"run_id": runID}),
		Consequence:   mustJSON(map[string]any{"launch_key": "launch/" + runID}),
	}
}

func releaseEffect(sequence uint64, runID string) EffectRecord {
	return EffectRecord{
		Sequence:      sequence,
		Operation:     OperationProviderRelease,
		Command:       EffectCommandAccepted,
		CorrelationID: runID,
		Request:       mustJSON(map[string]any{"launch_key": "launch/" + runID}),
		Consequence:   mustJSON(map[string]any{"removed": true}),
	}
}

func preemptionEffect(sequence uint64, rentalID string, runIDs ...string) EffectRecord {
	interrupted := make([]map[string]any, 0, len(runIDs))
	for _, runID := range runIDs {
		interrupted = append(interrupted, map[string]any{"run_id": runID, "started": true})
	}
	return EffectRecord{
		Sequence:      sequence,
		Operation:     OperationCapacityPreempted,
		Command:       EffectCommandAccepted,
		CorrelationID: rentalID,
		Request:       mustJSON(map[string]any{"offer_id": rentalID}),
		Consequence:   mustJSON(map[string]any{"interrupted": interrupted}),
	}
}
