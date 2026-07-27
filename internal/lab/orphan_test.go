package lab

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/janitor"
)

const orphanPolicyBlueprint = "an-orphan-is-adopted-or-destroyed-by-policy"

// TestOneOrphanIsAdoptedAndTheOtherIsDestroyed is the policy half of
// reconciliation. All three machines are holding something this control plane
// never launched and does not recognise, and they are converged in opposite
// directions because Mercator's own record says different things about them: the
// capacity whose launch says the machine outlives its workload keeps its machine,
// and the capacity nothing can be bound to loses it.
//
// The fleet afterwards is the claim. An adoption that quietly destroyed the
// machine and a termination that quietly kept it would both read the same in a
// count of things reclaimed.
func TestOneOrphanIsAdoptedAndTheOtherIsDestroyed(t *testing.T) {
	execution := driveOrphanPolicyExecution(t)

	decisions := orphanDecisions(t, execution)
	if len(decisions) != 3 {
		t.Fatalf("the record holds %d orphan decisions, want one for each machine: %+v", len(decisions), decisions)
	}
	adopted := decisionFor(t, decisions, "orphan-of-ghost")
	if adopted.Outcome != janitor.OrphanAdopted || adopted.Reason != "recorded_disposition_release" {
		t.Fatalf("the capacity Mercator could account for was converged as %+v, want it adopted on its recorded disposition", adopted)
	}
	destroyed := decisionFor(t, decisions, "orphan-nobody-claims")
	if destroyed.Outcome != janitor.OrphanTerminated || destroyed.Reason != "unattributed" {
		t.Fatalf("the capacity nothing can be bound to was converged as %+v, want it terminated as unattributed", destroyed)
	}
	standing := offerIDs(execution.runtime.world.truthSnapshot().Offers)
	if !slices.Contains(standing, "stranded") {
		t.Fatalf("the fleet is %v, and the machine the policy adopted is not in it", standing)
	}
	if slices.Contains(standing, "forgotten") {
		t.Fatalf("the fleet is %v, and the machine the policy terminated is still billing", standing)
	}
}

// TestAMachineIsKeptWhenTheRunOnItGaveUpBeforeAnybodyAskedForItBack is the
// combination the cleanup request says nothing about. The provider refused this
// Run's start until its attempts ran out, so it ended with no cleanup ever
// requested, and its launch recorded that the machine is handed back by releasing
// its slot.
//
// The machine is what the claim is about. A policy that read whether cleanup had
// been asked for before reading what the launch recorded destroys a rented
// machine every time a launch gives up, and every other Booking on it loses its
// host.
func TestAMachineIsKeptWhenTheRunOnItGaveUpBeforeAnybodyAskedForItBack(t *testing.T) {
	execution := driveOrphanPolicyExecution(t)

	adopted := decisionFor(t, orphanDecisions(t, execution), "orphan-of-refused")
	if adopted.Outcome != janitor.OrphanAdopted || adopted.Reason != "recorded_disposition_release" {
		t.Fatalf("the capacity of a Run that gave up was converged as %+v, want it adopted on its recorded disposition", adopted)
	}
	standing := offerIDs(execution.runtime.world.truthSnapshot().Offers)
	if !slices.Contains(standing, "abandoned") {
		t.Fatalf("the fleet is %v, and the machine whose launch said it outlives the workload is not in it", standing)
	}
}

// TestEveryOrphanDecisionNamesThePolicyThatMadeIt reads the same execution through
// the standing rule rather than through this fixture's own names, which is what
// makes safety.orphan_policy_is_explicit hold over every world rather than one.
func TestEveryOrphanDecisionNamesThePolicyThatMadeIt(t *testing.T) {
	execution := driveOrphanPolicyExecution(t)

	if _, err := execution.Check(context.Background()); err != nil {
		t.Fatalf("the execution violates a standing rule: %v", err)
	}
	for _, decision := range orphanDecisions(t, execution) {
		if decision.Policy != janitor.OrphanPolicy {
			t.Fatalf("a machine was converged under policy %q, want the one this control plane states", decision.Policy)
		}
	}
}

func driveOrphanPolicyExecution(t *testing.T) *Execution {
	t.Helper()
	execution := openConformanceExecution(t, orphanPolicyBlueprint)
	t.Cleanup(func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	})
	for range 20 {
		if _, err := execution.Drive(context.Background(), Advance(time.Minute)); err != nil {
			t.Fatalf("drive the execution: %v", err)
		}
	}
	return execution
}

// orphanDecisions is every policy decision Mercator's own public record holds. It
// is read off the log rather than off the sweep's return value, because the record
// is what an operator sees and the whole point of the rule is that it is there.
func orphanDecisions(t *testing.T, execution *Execution) []janitor.OrphanConvergence {
	t.Helper()
	stored, err := execution.runtime.mercatorEvents(context.Background())
	if err != nil {
		t.Fatalf("read Mercator's record: %v", err)
	}
	var decisions []janitor.OrphanConvergence
	for _, event := range stored {
		if event.Type != janitor.EventOrphanConverged {
			continue
		}
		var convergence janitor.OrphanConvergence
		if err := json.Unmarshal(event.Data, &convergence); err != nil {
			t.Fatalf("decode orphan decision %s: %v", event.ID, err)
		}
		decisions = append(decisions, convergence)
	}
	return decisions
}

func decisionFor(t *testing.T, decisions []janitor.OrphanConvergence, launchKey string) janitor.OrphanConvergence {
	t.Helper()
	for _, decision := range decisions {
		if decision.LaunchKey == launchKey {
			return decision
		}
	}
	t.Fatalf("no decision names orphaned capacity %q", launchKey)
	return janitor.OrphanConvergence{}
}
