package lab

import (
	"bytes"
	"context"
	"testing"
	"time"
)

// TestAMachineKeepsWorkingPastItsFirstSession drives the half of bootstrapping
// every earlier fixture finished too early to reach. The machine is provisioned,
// its agent enrols about four minutes in, and the Run it is then given takes
// forty five minutes, so the credential the agent joined with lapses while the
// container is still running.
//
// Three things have to be true of the ledger at the end, and the ledger is the
// only account of what really happened on the machine. The invitation was
// redeemed once, which is what makes a bootstrap single-use rather than a
// password. The session was renewed at least once, under its own operation,
// which is what a machine that outlives one session credential does. And the
// renewal landed while the work was still running rather than after it, because
// a renewal that waited for the Run to end would not have kept anything alive.
func TestAMachineKeepsWorkingPastItsFirstSession(t *testing.T) {
	execution := openConformanceExecution(t, "a-machine-keeps-working-past-its-first-session")
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	driveFor(t, execution, 40*time.Minute)

	ledger := execution.runtime.world.effectRecords()
	enrolled := acceptedEntries(ledger, OperationNodeEnrolled)
	renewed := acceptedEntries(ledger, OperationNodeSessionRenewed)
	launchedAt, launched := firstLaunchSequence(ledger)

	if !launched {
		t.Fatal("nothing was ever launched, and a machine that keeps working is one that was working")
	}
	if len(enrolled) != 1 {
		t.Fatalf("the ledger holds %d enrolments, and one machine joins the fleet once", len(enrolled))
	}
	if len(renewed) == 0 {
		t.Fatal("the machine outlived its first session credential and the ledger holds no renewal, so it went on working on a credential nothing renewed")
	}
	if renewed[0].Sequence < launchedAt {
		t.Fatalf("the session was renewed at %d and the workload was launched at %d, so nothing was kept alive by it",
			renewed[0].Sequence, launchedAt)
	}
	if len(execution.runtime.world.truthSnapshot().ActiveExecutions) == 0 {
		t.Fatal("the Run this machine was renewed for had already finished, so the renewal held nothing open")
	}
}

// TestNoExportOfThisExecutionCarriesTheInvitationItBootstrappedWith is the leak
// half of the same claim, read over the artifact an operator actually receives.
// A Run Bundle is the whole of an execution as it leaves this process: the event
// log, the Effect Ledger, the World Tape, the decisions, and the invariant
// results. A bootstrap credential in any of them is a credential in every copy of
// that bundle for as long as the bundle exists.
//
// It scans the bundle's own bytes rather than asking the rule again. The rule
// reads two collections in memory, and a byte scan of what is written down is the
// only thing that can catch a credential reaching an export through some third
// path nobody thought to hold a rule over.
func TestNoExportOfThisExecutionCarriesTheInvitationItBootstrappedWith(t *testing.T) {
	execution := openConformanceExecution(t, "a-machine-keeps-working-past-its-first-session")
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()
	driveFor(t, execution, 40*time.Minute)

	bundle, err := execution.Export(context.Background())
	if err != nil {
		t.Fatalf("export the Run Bundle: %v", err)
	}
	exported, err := bundle.Bytes()
	if err != nil {
		t.Fatalf("encode the Run Bundle: %v", err)
	}

	credentials := execution.runtime.world.invariantFacts().BootstrapCredentials
	if len(credentials) == 0 {
		t.Fatal("this execution bootstrapped no machine, so the scan would pass on an execution that never provisioned one")
	}
	for _, credential := range credentials {
		if bytes.Contains(exported, []byte(credential.Token)) {
			t.Fatalf("the Run Bundle carries the invitation %s was bootstrapped with", credential.NodeID)
		}
	}
}

// acceptedEntries is every record of one operation this world accepted, in ledger
// order.
func acceptedEntries(ledger []EffectRecord, operation string) []EffectRecord {
	var accepted []EffectRecord
	for _, effect := range ledger {
		if effect.Operation == operation && effect.Command == EffectCommandAccepted {
			accepted = append(accepted, effect)
		}
	}
	return accepted
}
