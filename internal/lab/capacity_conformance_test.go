package lab

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/orchestrator"
	"github.com/benngarcia/mercator/internal/scenario"
)

// labReconcileInterval is how often these cases look at the machine being built.
// It is fine enough to see each provisioning stage finish on its own, which is
// what makes the seconds the record carries the world's own spend rather than the
// interval between two looks: a control plane that finds two stages complete in
// one look measures the second at zero, and there is no honest way to split an
// interval nothing observed.
const labReconcileInterval = 15 * time.Second

// TestProvisionedCapacityBecomesAMachineMercatorHolds drives the transition the
// placement corpus can only state the failure half of, through the real
// orchestrator, the real event log, and this world's own provider and registry.
//
// It reads the Effect Ledger, because the ledger is the only account of what
// really happened to the machine. Mercator's record can say a Run launched; only
// this says whether there was ever a session to launch it through. Three entries
// have to be there and they have to be in this order: the provision Mercator
// commanded, the enrolment the machine made on its own account against the same
// Rental and the same generation, and the launch after both.
//
// The order is the whole claim. A launch before the enrolment is a container
// created through a session Mercator does not have, and an enrolment against a
// Rental nothing provisioned is a node this world invented. The generation is the
// other half of it: a lease may invite a second machine when a generation ends,
// and a session filed under a generation the machine was never invited for is one
// Mercator would address every later act about to the wrong machine.
func TestProvisionedCapacityBecomesAMachineMercatorHolds(t *testing.T) {
	execution := openConformanceExecution(t, "provisioned-capacity-becomes-a-machine-mercator-holds")
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	driveFor(t, execution, 12*time.Minute)

	ledger := execution.runtime.world.effectRecords()
	provisioned := firstAcceptedCapacityEntry(t, ledger, OperationCapacityProvision)
	enrolled := firstAcceptedCapacityEntry(t, ledger, OperationNodeEnrolled)
	launchedAt, launched := firstLaunchSequence(ledger)

	if !launched {
		t.Fatal("nothing was ever launched, and a machine Mercator holds is one it can execute on")
	}
	if enrolled.lease != provisioned.lease {
		t.Fatalf("the agent enrolled under Rental %q generation %d and the machine was allocated for Rental %q generation %d",
			enrolled.lease.RentalID, enrolled.lease.Generation, provisioned.lease.RentalID, provisioned.lease.Generation)
	}
	if !(provisioned.sequence < enrolled.sequence && enrolled.sequence < launchedAt) {
		t.Fatalf("the ledger holds provision at %d, enrolment at %d, and the launch at %d, and a container is created through a session that already exists",
			provisioned.sequence, enrolled.sequence, launchedAt)
	}
	for _, effect := range ledger {
		if effect.Operation == OperationCapacityTerminate {
			t.Fatalf("the machine was destroyed by %s, and its agent arrived", effect.ID)
		}
	}
}

// TestALostProvisionAnswerCostsOneRepeatAndNotTheMachine drives the state a
// provider honouring no idempotency key really leaves behind: the create landed,
// the answer never came back, and nothing Mercator can ask names the machine
// that is already billing.
//
// Two things have to follow and the second is the one nothing used to hold. One
// machine, because asking again under the lease finds what is there rather than
// renting a second host beside it. And that machine has to be able to enrol.
//
// It holds one invitation, written into the bootstrap when it was created, and
// there is no way to tell it anything else: Mercator opens no connection to a
// rented machine. Provisioning asks for that material before the provider
// answers, because until it answers nothing knows whether an earlier attempt
// landed a host, so the repeat asks the registry for the identity again. If the
// registry mints a fresh invitation there, the adopted machine is holding one the
// registry no longer names, is refused when it presents it, and has nothing else
// to try. Everything else looks healthy: the provider says the machine is up, the
// ledger says the allocation was accepted, and the only symptom is silence where
// the agent should be, for the whole of the enrolment patience, on a bill.
func TestALostProvisionAnswerCostsOneRepeatAndNotTheMachine(t *testing.T) {
	execution := openConformanceExecution(t, "a-lost-provision-answer-costs-one-repeat-and-not-the-machine")
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	driveFor(t, execution, 12*time.Minute)

	if _, err := execution.Check(context.Background()); err != nil {
		t.Fatalf("the execution violates a standing rule: %v", err)
	}
	ledger := execution.runtime.world.effectRecords()
	if machines := len(execution.runtime.world.leases); machines != 1 {
		t.Fatalf("the world rented %d machines, and one lost answer must cost one repeat rather than one extra host", machines)
	}
	if !slices.ContainsFunc(ledger, func(effect EffectRecord) bool {
		return effect.Operation == OperationCapacityProvision && effect.Response == EffectResponseLost
	}) {
		t.Fatal("the ledger holds no lost provision, so this case is not exercising the world it is about")
	}
	provisioned := firstAcceptedCapacityEntry(t, ledger, OperationCapacityProvision)
	enrolled := firstAcceptedCapacityEntry(t, ledger, OperationNodeEnrolled)
	if enrolled.lease != provisioned.lease {
		t.Fatalf("the agent enrolled under Rental %q generation %d and the machine was allocated for Rental %q generation %d",
			enrolled.lease.RentalID, enrolled.lease.Generation, provisioned.lease.RentalID, provisioned.lease.Generation)
	}
	if _, launched := firstLaunchSequence(ledger); !launched {
		t.Fatal("nothing was ever launched on the adopted machine, and a machine nothing can execute on is one nobody should be paying for")
	}
	for _, effect := range ledger {
		if effect.Operation == OperationCapacityTerminate {
			t.Fatalf("the machine was handed back by %s, and its agent arrived", effect.ID)
		}
	}
}

// TestEveryProvisioningStageIsRecordedAtWhatTheWorldSpent reads the other account
// of the same machine: the three stage observations Mercator wrote as it watched.
//
// Each has to carry what this world really spent on that stage. The provider owns
// acquisition and boot, the registry owns whether an agent opened a session, and
// a control plane that reported the gap between two of its own looks would be
// recording its polling interval as a property of the machine. A calibration
// trained on that would learn the reconcile cadence.
//
// Not one of the three durations is a multiple of labReconcileInterval, which is
// what makes the assertion capable of failing. Every stage of this machine ends
// between two looks, so a record carrying the look would read 45, 255 and 60
// against the 37, 247 and 51 the world spends, and each of the three is off by a
// different amount. The record is also required to say it was measured: a bound
// that happened to equal the spend would be right by luck, and the flag is what
// the calibration reads to know whether it may learn from the number at all.
func TestEveryProvisioningStageIsRecordedAtWhatTheWorldSpent(t *testing.T) {
	execution := openConformanceExecution(t, "provisioned-capacity-becomes-a-machine-mercator-holds")
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	driveFor(t, execution, 12*time.Minute)

	recorded := capacityStages(t, execution)
	// The listing's own three stages: thirty seven seconds to allocate the machine,
	// four minutes and seven to boot it, fifty one for its agent to open a session.
	for stage, spent := range map[domain.LaunchStage]float64{
		domain.StageAcquisition: 37,
		domain.StageBoot:        247,
		domain.StageAgentReady:  51,
	} {
		observed, present := recorded[stage]
		if !present {
			t.Fatalf("the record holds no %s stage, and this machine went through it", stage)
		}
		if observed.Seconds != spent {
			t.Fatalf("%s is recorded at %.0fs and this world spends %.0fs on it", stage, observed.Seconds, spent)
		}
		if observed.Bounded {
			t.Fatalf("%s is recorded as a bound, and both this world's provider and its registry date what they answer", stage)
		}
	}
}

// TestALabMachineWhoseAgentNeverArrivesIsHandedBackAtTheStatedPatience is what
// replaced Compile refusing a bootstrap it would not perform. The refusal was
// correct while this world built no machine from a listing; now that it does, the
// statement has to be honoured rather than turned away, and the honouring is what
// a fixture about a stranded machine rests on.
//
// Two things are honoured and both are asserted here. No agent opens a session,
// so no node is enrolled and nothing is ever launched. And the machine is handed
// back at the patience this listing published rather than at Mercator's own,
// which is the only evidence that a provider's stated patience reaches the offer
// at all: the fixture says eight minutes and Mercator's own default is fifteen,
// so a control plane reading past the listing would hold the machine for seven
// minutes longer and nothing else in the tree would notice.
func TestALabMachineWhoseAgentNeverArrivesIsHandedBackAtTheStatedPatience(t *testing.T) {
	blueprint, err := scenario.LoadBlueprint("../scenario/scenarios/conformance/provisioned-capacity-becomes-a-machine-mercator-holds.json")
	if err != nil {
		t.Fatalf("load Blueprint: %v", err)
	}
	blueprint.World.Marketplace[0].Provisioning.AgentReady = nil
	blueprint.World.Marketplace[0].Bootstrap.NeverEnrolls = true
	stated := blueprint.World.Marketplace[0].Bootstrap.Deadline.Duration()
	execution := openCompiledExecution(t, blueprint)
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	driveFor(t, execution, 12*time.Minute)

	ledger := execution.runtime.world.effectRecords()
	provisioned := firstAcceptedCapacityEntry(t, ledger, OperationCapacityProvision)
	if _, _, enrolled := acceptedCapacityEntry(ledger, OperationNodeEnrolled); enrolled {
		t.Fatal("an agent opened a session on a machine whose listing says none ever does")
	}
	if _, launched := firstLaunchSequence(ledger); launched {
		t.Fatal("a workload was launched on a machine Mercator has no session to")
	}
	reclaimed := firstAcceptedCapacityEntry(t, ledger, OperationCapacityTerminate)
	if reclaimed.lease.RentalID != provisioned.lease.RentalID {
		t.Fatalf("Rental %q was handed back and Rental %q was the one allocated",
			reclaimed.lease.RentalID, provisioned.lease.RentalID)
	}
	// One look wide, because a deadline is a moment and a reconcile is a look: the
	// machine is handed back at the first look past the patience this listing
	// stated. Read against Mercator's own fifteen minutes, which is what a listing
	// whose bootstrap never reached the offer would be held to.
	if held := reclaimed.at.Sub(provisioned.at); held < stated || held >= stated+labReconcileInterval {
		t.Fatalf("the machine was held %s and the listing says Mercator waits %s for the agent", held, stated)
	}
}

func driveFor(t *testing.T, execution *Execution, span time.Duration) {
	t.Helper()
	for range int(span / labReconcileInterval) {
		if _, err := execution.Drive(context.Background(), Advance(labReconcileInterval)); err != nil {
			t.Fatalf("drive the arrival: %v", err)
		}
	}
}

func openCompiledExecution(t *testing.T, blueprint scenario.Blueprint) *Execution {
	t.Helper()
	tape, samples, err := Compile(blueprint, CompileOptions{})
	if err != nil {
		t.Fatalf("compile Blueprint: %v", err)
	}
	execution, err := Open(context.Background(), Config{
		Blueprint:        blueprint,
		Tape:             tape,
		Samples:          samples,
		Limits:           testLimits(),
		Policy:           "policy:test",
		MercatorRevision: "revision:test",
	})
	if err != nil {
		t.Fatalf("open execution: %v", err)
	}
	return execution
}

// capacityEntry is one ledger record read as the act it is: the lease it was
// about, where it sits in the order, and when it happened.
type capacityEntry struct {
	lease    capacityLeaseRef
	sequence uint64
	at       time.Time
}

func firstAcceptedCapacityEntry(t *testing.T, ledger []EffectRecord, operation string) capacityEntry {
	t.Helper()
	lease, entry, found := acceptedCapacityEntry(ledger, operation)
	if !found {
		t.Fatalf("the ledger holds no accepted %s", operation)
	}
	return capacityEntry{lease: lease, sequence: entry.Sequence, at: entry.At}
}

// acceptedCapacityEntry is the first entry of one operation the world accepted,
// with the lease it was about. The lease is read out of the request projection for
// the reason every rule over this ledger does: it is what a machine, an
// invitation, and a reclamation are all filed under, and a reader keying on
// anything else could not tie the three together.
func acceptedCapacityEntry(ledger []EffectRecord, operation string) (capacityLeaseRef, EffectRecord, bool) {
	for _, effect := range ledger {
		if effect.Operation != operation || effect.Command != EffectCommandAccepted {
			continue
		}
		lease, err := capacityLeaseOf(effect)
		if err != nil {
			continue
		}
		return lease, effect, true
	}
	return capacityLeaseRef{}, EffectRecord{}, false
}

func firstLaunchSequence(ledger []EffectRecord) (uint64, bool) {
	for _, effect := range ledger {
		if effect.Operation == OperationProviderLaunch && effect.Command == EffectCommandAccepted {
			return effect.Sequence, true
		}
	}
	return 0, false
}

// recordedStage is one provisioning stage as Mercator wrote it down: how long it
// took, and whether that is the duration its authority dated or the bound one
// look established.
type recordedStage struct {
	Stage   domain.LaunchStage `json:"stage"`
	Seconds float64            `json:"seconds"`
	Bounded bool               `json:"bounded"`
}

// capacityStages is what Mercator wrote down about each stage of the machine
// being built, read off its own event log rather than the world's ledger: these
// are the control plane's observations, and whether they match what the world
// spent is the whole question.
func capacityStages(t *testing.T, execution *Execution) map[domain.LaunchStage]recordedStage {
	t.Helper()
	events, err := execution.runtime.mercatorEvents(context.Background())
	if err != nil {
		t.Fatalf("read Mercator events: %v", err)
	}
	stages := map[domain.LaunchStage]recordedStage{}
	for _, event := range events {
		if event.Type != orchestrator.EventCapacityStageObserved {
			continue
		}
		var observed recordedStage
		if err := json.Unmarshal(event.Data, &observed); err != nil {
			t.Fatalf("decode capacity stage %s: %v", event.ID, err)
		}
		stages[observed.Stage] = observed
	}
	return stages
}
