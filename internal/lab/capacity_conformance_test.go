package lab

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/scenario"
)

// TestProvisionedCapacityBecomesAMachineMercatorHolds drives the transition the
// placement corpus can only state the failure half of, through the real
// orchestrator, the real event log, and this world's own provider and registry.
//
// It reads the Effect Ledger, because the ledger is the only account of what
// really happened to the machine. Mercator's record can say a Run launched; only
// this says whether there was ever a session to launch it through. Three entries
// have to be there and they have to be in this order: the provision Mercator
// commanded, the enrolment the machine made on its own account against the same
// Rental, and the launch after both.
//
// The order is the whole claim. A launch before the enrolment is a container
// created through a session Mercator does not have, and an enrolment against a
// Rental nothing provisioned is a node this world invented.
func TestProvisionedCapacityBecomesAMachineMercatorHolds(t *testing.T) {
	execution := openConformanceExecution(t, "provisioned-capacity-becomes-a-machine-mercator-holds")
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	for range 4 {
		if _, err := execution.Drive(context.Background(), Advance(3*time.Minute)); err != nil {
			t.Fatalf("drive the arrival: %v", err)
		}
	}

	ledger := execution.runtime.world.effectRecords()
	provisioned, provisionedAt := firstAcceptedCapacityEntry(t, ledger, OperationCapacityProvision)
	enrolled, enrolledAt := firstAcceptedCapacityEntry(t, ledger, OperationNodeEnrolled)
	launchedAt, launched := firstLaunchSequence(ledger)

	if !launched {
		t.Fatal("nothing was ever launched, and a machine Mercator holds is one it can execute on")
	}
	if enrolled != provisioned {
		t.Fatalf("the agent enrolled under Rental %q and the machine was allocated for %q", enrolled, provisioned)
	}
	if !(provisionedAt < enrolledAt && enrolledAt < launchedAt) {
		t.Fatalf("the ledger holds provision at %d, enrolment at %d, and the launch at %d, and a container is created through a session that already exists",
			provisionedAt, enrolledAt, launchedAt)
	}
	for _, effect := range ledger {
		if effect.Operation == OperationCapacityTerminate {
			t.Fatalf("the machine was destroyed by %s, and its agent arrived", effect.ID)
		}
	}
}

// TestALabMachineWhoseAgentNeverArrivesEnrolsNothing is what replaced Compile
// refusing a bootstrap it would not perform. The refusal was correct while this
// world built no machine from a listing; now that it does, the statement has to be
// honoured rather than turned away, and the honouring is what a fixture about a
// stranded machine rests on.
//
// It is stated as the absence it is. No agent opens a session, so no node is
// enrolled and nothing is ever launched, and the machine the provider allocated is
// still there being billed for until somebody gives up on it.
func TestALabMachineWhoseAgentNeverArrivesEnrolsNothing(t *testing.T) {
	blueprint, err := scenario.LoadBlueprint("../scenario/scenarios/conformance/provisioned-capacity-becomes-a-machine-mercator-holds.json")
	if err != nil {
		t.Fatalf("load Blueprint: %v", err)
	}
	blueprint.World.Marketplace[0].Provisioning.AgentReady = nil
	blueprint.World.Marketplace[0].Bootstrap.NeverEnrolls = true
	execution := openCompiledExecution(t, blueprint)
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	for range 4 {
		if _, err := execution.Drive(context.Background(), Advance(3*time.Minute)); err != nil {
			t.Fatalf("drive the arrival: %v", err)
		}
	}

	ledger := execution.runtime.world.effectRecords()
	if _, _, allocated := acceptedCapacityEntry(ledger, OperationCapacityProvision); !allocated {
		t.Fatal("nothing was allocated, and this world's provider takes the machine before its agent fails to arrive")
	}
	if rental, _, enrolled := acceptedCapacityEntry(ledger, OperationNodeEnrolled); enrolled {
		t.Fatalf("an agent opened a session under Rental %q on a machine whose listing says none ever does", rental)
	}
	if _, launched := firstLaunchSequence(ledger); launched {
		t.Fatal("a workload was launched on a machine Mercator has no session to")
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

func firstAcceptedCapacityEntry(t *testing.T, ledger []EffectRecord, operation string) (string, uint64) {
	t.Helper()
	rentalID, sequence, found := acceptedCapacityEntry(ledger, operation)
	if !found {
		t.Fatalf("the ledger holds no accepted %s", operation)
	}
	return rentalID, sequence
}

// acceptedCapacityEntry is the first entry of one operation the world accepted,
// by the lease it was about. It reads the Rental out of the request projection for
// the reason every rule over this ledger does: the lease is what a machine, an
// invitation, and a reclamation are all filed under, and a reader keying on
// anything else could not tie the three together.
func acceptedCapacityEntry(ledger []EffectRecord, operation string) (string, uint64, bool) {
	for _, effect := range ledger {
		if effect.Operation != operation || effect.Command != EffectCommandAccepted {
			continue
		}
		var facts struct {
			RentalID string `json:"rental_id"`
		}
		if err := json.Unmarshal(effect.Request, &facts); err != nil {
			continue
		}
		return facts.RentalID, effect.Sequence, true
	}
	return "", 0, false
}

func firstLaunchSequence(ledger []EffectRecord) (uint64, bool) {
	for _, effect := range ledger {
		if effect.Operation == OperationProviderLaunch && effect.Command == EffectCommandAccepted {
			return effect.Sequence, true
		}
	}
	return 0, false
}
