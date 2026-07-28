package lab

import (
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
)

// TestAReusableProvisionedRunReleasesItsWorkloadAndLeavesItsHost drives the
// machine of provisioned-capacity-becomes-a-machine-mercator-holds past the end
// of the Run it was allocated for, which the other cases in that world stop
// short of. Everything up to the launch is that fixture's claim; what happens
// when the workload exits is this one's.
//
// The whole of the reusable lane is here. Mercator asked a provider for a
// machine, an agent enrolled on it, a container ran and finished, and the
// machine is still standing afterwards with its session open. A cleanup that
// terminated instead would leave the next Run allocating and booting a second
// machine, and an operator paying twice for the four minutes this one spent
// getting ready, which is the reusable lane deleted at the moment it starts
// paying off.
//
// It is read from the Effect Ledger, because Mercator's own record cannot
// answer it. The record says a cleanup was confirmed under a recorded
// disposition; only the ledger says which command the world actually received
// and what became of the machine.
func TestAReusableProvisionedRunReleasesItsWorkloadAndLeavesItsHost(t *testing.T) {
	execution := openConformanceExecution(t, "provisioned-capacity-becomes-a-machine-mercator-holds")
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	driveFor(t, execution, 45*time.Minute)

	run := projectedRun(t, execution, "run-builder")
	if run.Outcome != domain.RunOutcomeSucceeded || run.Cleanup != domain.CleanupConfirmed {
		t.Fatalf("the Run ended %q with cleanup %q, and this case is about a Run that finished and was cleaned up",
			run.Outcome, run.Cleanup)
	}
	if run.Disposition != domain.DispositionRelease {
		t.Fatalf("the Run recorded disposition %q, and a machine held under a lease is not a Run's to destroy", run.Disposition)
	}

	ledger := execution.runtime.world.effectRecords()
	provisioned := firstAcceptedCapacityEntry(t, ledger, OperationCapacityProvision)
	if _, released := acceptedEffect(ledger, OperationProviderRelease); !released {
		t.Fatal("nothing released the workload, so nothing took Mercator's container off a machine it means to keep")
	}
	for _, destroyer := range []string{OperationProviderTerminate, OperationCapacityTerminate} {
		if effect, found := acceptedEffect(ledger, destroyer); found {
			t.Fatalf("%s destroyed the machine, and the Run ending is not the lease ending", effect.ID)
		}
	}

	lease, held := execution.runtime.world.leaseState(provisioned.lease.RentalID)
	switch {
	case !held:
		t.Fatalf("this world holds nothing for Rental %q, and it allocated a machine for it", provisioned.lease.RentalID)
	case lease.Terminated:
		t.Fatalf("Rental %q lost its machine at %s, and its Run only finished", lease.RentalID, lease.TerminatedAt)
	case !lease.Enrolled:
		t.Fatalf("Rental %q holds a machine with no session open on it, so nothing could be launched there next", lease.RentalID)
	}
}

// TestAOneShotExecutionStillTakesItsHostWithIt is the same question asked of the
// other lane, in the world where an ephemeral listing is the machine a Run wins.
// Nothing survives a one-shot product, so its cleanup destroys it, and this is
// what the rule above must not have swallowed: a disposition that read the offer
// kind alone answered terminate for both lanes, and one that read nothing at all
// would answer release for both. Only a reading of the lane gets both right, and
// only the two cases together can tell those three apart.
func TestAOneShotExecutionStillTakesItsHostWithIt(t *testing.T) {
	execution := openConformanceExecution(t, "an-owned-hour-is-charged-to-somebody")
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	driveFor(t, execution, 45*time.Minute)

	run := projectedRun(t, execution, "run-atlas")
	if run.Outcome != domain.RunOutcomeSucceeded || run.Cleanup != domain.CleanupConfirmed {
		t.Fatalf("the Run ended %q with cleanup %q, and this case is about a Run that finished and was cleaned up",
			run.Outcome, run.Cleanup)
	}
	if run.Disposition != domain.DispositionTerminate {
		t.Fatalf("the Run recorded disposition %q on a one-shot execution, which holds nothing once its workload exits", run.Disposition)
	}
	ledger := execution.runtime.world.effectRecords()
	if _, terminated := acceptedEffect(ledger, OperationProviderTerminate); !terminated {
		t.Fatal("the one-shot execution was never terminated, so a product that holds nothing was left holding a machine")
	}
}

// acceptedEffect is the first effect of one operation this world carried out.
// Rejected and duplicate records are not it: the question every caller here asks
// is whether the world ever really did the thing.
func acceptedEffect(ledger []EffectRecord, operation string) (EffectRecord, bool) {
	for _, effect := range ledger {
		if effect.Operation == operation && effect.Command == EffectCommandAccepted {
			return effect, true
		}
	}
	return EffectRecord{}, false
}
