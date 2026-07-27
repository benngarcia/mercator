package lab

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
)

// economicsClock is the moment the records in this file were decided at. It is
// stated once because the whole question the second law asks is which seconds of
// one interval each placement was charged for, and seconds are only comparable
// against one clock.
var economicsClock = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)

// ownedMachinePricedAtNothing is the record safety.no_capacity_is_free forbids: a
// node Mercator holds, weighed for a Run, and priced at nothing because the hour
// it is sitting inside was already paid for.
//
// It is the honest answer to the wrong question. Nothing new is billed, and the
// seconds are still the only ones this machine has, so a candidate offered free
// wins every placement it is weighed in and the fleet reports that it spent
// nothing putting all its work on one box.
func ownedMachinePricedAtNothing() eventlog.CloudEvent {
	return bookingDecidedEvent("evt_free_node", domain.BookingDecision{
		ID:                      "dec_free_node",
		RunID:                   "run-thrifty",
		EvaluatedAt:             economicsClock,
		SelectedOfferSnapshotID: "node-owned",
		Candidates: []domain.CandidateDecision{{
			OfferSnapshotID: "node-owned",
			Candidate:       domain.CandidateIdentity{Lane: domain.LaneReusable, Machine: "machine-owned", Provider: "node"},
			Feasible:        true,
			Estimates: domain.CandidateEstimates{
				CostUSD:   domain.Estimate{Source: "price_model"},
				CostTerms: []domain.CostTerm{{Name: domain.CostTermCommittedRent}},
			},
		}},
	})
}

// oneCommittedHourSoldTwice is the record safety.committed_cost_is_not_double_counted
// forbids: two Runs placed on one machine inside one committed hour, each charged
// for the whole of what was still outstanding when its own decision was taken.
//
// It is what pricing a commitment from the moment of the decision produces rather
// than from the moment the Run gets the machine. Each record is defensible on its
// own, and together they report a machine costing twice what one hour of it costs.
func oneCommittedHourSoldTwice() []eventlog.CloudEvent {
	return []eventlog.CloudEvent{
		committedPlacement("evt_first_hour", "run-first", economicsClock, 0, 3600),
		committedPlacement("evt_second_hour", "run-second", economicsClock.Add(10*time.Minute), 0, 3000),
	}
}

// committedPlacement is one placement on the machine inside the committed hour,
// charged from fromSeconds after its own decision for as long as it says.
func committedPlacement(eventID, runID string, at time.Time, fromSeconds, seconds float64) eventlog.CloudEvent {
	rate := 1.0 / 3600
	return bookingDecidedEvent(eventID, domain.BookingDecision{
		ID:                      "dec_" + runID,
		RunID:                   runID,
		EvaluatedAt:             at,
		SelectedOfferSnapshotID: "node-owned",
		Booking:                 &domain.Booking{ID: "bkg_" + runID, RunID: runID, RentalID: "rental-owned"},
		Candidates: []domain.CandidateDecision{{
			OfferSnapshotID: "node-owned",
			Candidate:       domain.CandidateIdentity{Lane: domain.LaneReusable, Machine: "machine-owned", Provider: "node"},
			Feasible:        true,
			Estimates: domain.CandidateEstimates{
				CostUSD:   domain.Estimate{Expected: rate * seconds, Source: "price_model"},
				CostTerms: []domain.CostTerm{{Name: domain.CostTermCommittedRent, USD: rate * seconds}},
				Committed: domain.CommittedInterval{
					Until:       economicsClock.Add(time.Hour),
					FromSeconds: fromSeconds,
					Seconds:     seconds,
				},
			},
		}},
	})
}

// TestTwoRunsMaySpendOneCommittedHourBetweenThem is the lawful half of the same
// rule, and it is what stops the law being a ban on committed rent altogether. One
// machine, one hour Mercator owes for, and two Runs that hold it one after the
// other: the second is charged from where the first left off, so the hour is spent
// once and both placements are charged for it.
func TestTwoRunsMaySpendOneCommittedHourBetweenThem(t *testing.T) {
	observation := InvariantObservation{
		MercatorEvents: []eventlog.CloudEvent{
			committedPlacement("evt_early", "run-early", economicsClock, 0, 1800),
			committedPlacement("evt_late", "run-late", economicsClock.Add(5*time.Minute), 1500, 1800),
		},
	}

	if err := committedCostIsNotDoubleCounted(observation); err != nil {
		t.Fatalf("two Runs spending disjoint halves of one committed hour were reported as one hour charged twice: %v", err)
	}
}

// TestCommittedRentStopsAtTheEndOfTheInterval is the other way one interval can be
// oversold, and it needs only one placement: rent charged past the moment the
// commitment ends is rent for seconds Mercator has not committed to, which is the
// keep-alive term wearing the committed term's discount.
func TestCommittedRentStopsAtTheEndOfTheInterval(t *testing.T) {
	observation := InvariantObservation{
		MercatorEvents: []eventlog.CloudEvent{
			committedPlacement("evt_overrun", "run-overrun", economicsClock.Add(50*time.Minute), 0, 1800),
		},
	}

	err := committedCostIsNotDoubleCounted(observation)

	if err == nil {
		t.Fatal("a placement charged committed rent for half an hour past the end of the committed hour raised nothing")
	}
}

// TestAnUnquotedMachineCarriesNoPriceToAccountFor is the exemption
// safety.no_capacity_is_free states out loud. A machine nobody quoted has no
// dollars, and the law must not push the tree into inventing some: pricing the
// absence of a price is the fabrication the whole rule is against.
func TestAnUnquotedMachineCarriesNoPriceToAccountFor(t *testing.T) {
	observation := InvariantObservation{
		MercatorEvents: []eventlog.CloudEvent{bookingDecidedEvent("evt_unquoted", domain.BookingDecision{
			ID:    "dec_unquoted",
			RunID: "run-unquoted",
			Candidates: []domain.CandidateDecision{{
				OfferSnapshotID: "host-unquoted",
				Feasible:        true,
				Estimates:       domain.CandidateEstimates{CostUSD: domain.Estimate{Source: domain.CostUnpriced}},
			}},
		})},
	}

	if err := noCapacityIsFree(observation); err != nil {
		t.Fatalf("a candidate nobody quoted was reported as capacity somebody says is free: %v", err)
	}
}

// TestAPriceItsOwnTermsDoNotAddUpToIsRefused is the accounting half of the law. A
// total nothing explains cannot be argued with by the operator who has to pay it,
// and a term quietly dropped from the sum is how a price that looks derived stops
// being derivable.
func TestAPriceItsOwnTermsDoNotAddUpToIsRefused(t *testing.T) {
	observation := InvariantObservation{
		MercatorEvents: []eventlog.CloudEvent{bookingDecidedEvent("evt_unaccounted", domain.BookingDecision{
			ID:    "dec_unaccounted",
			RunID: "run-unaccounted",
			Candidates: []domain.CandidateDecision{{
				OfferSnapshotID: "ask-minute",
				Feasible:        true,
				Estimates: domain.CandidateEstimates{
					CostUSD:   domain.Estimate{Expected: 0.85, Source: "price_model"},
					CostTerms: []domain.CostTerm{{Name: domain.CostTermKeepAlive, USD: 0.8}},
				},
			}},
		})},
	}

	err := noCapacityIsFree(observation)

	if err == nil {
		t.Fatal("a candidate priced at 0.85 USD out of terms adding up to 0.80 raised nothing")
	}
}

// TestAnIdleMachineIsNotFreeAtL1 is owned capacity economics under the real
// control plane. The placement corpus shows the decision; this shows the Run
// running where the terms of the sale sent it, through the offer catalog, the real
// orchestrator, and every law in the registry.
//
// The case is only about the terms if the shadow price alone would have bought the
// node, which is what the last assertion pins: half an hour of the node at its own
// rate is 0.50 USD against the 0.85 the Run actually spends, so a model billing
// the seconds one Run occupies sends it to the node and this one does not.
func TestAnIdleMachineIsNotFreeAtL1(t *testing.T) {
	execution := openConformanceExecution(t, "an-owned-hour-is-charged-to-somebody")
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	if _, err := execution.DriveToCompletion(context.Background()); err != nil {
		t.Fatalf("drive a fleet whose owned machine is billed by the hour: %v", err)
	}

	decision := bookingDecisions(t, execution)["run-atlas"]
	if decision.SelectedOfferSnapshotID != "ask-minute" {
		t.Fatalf("the half-hour Run landed on %q, and the node's hour costs more than the machine billed by the minute",
			decision.SelectedOfferSnapshotID)
	}
	node := candidateFor(t, decision, "node-owned")
	if !node.Feasible {
		t.Fatalf("the node was refused as %+v, and a machine that costs more than another is not an unusable one", node.Rejections)
	}
	// Every term of the node's price, because the total is the number two opposite
	// mistakes agree on: ten minutes of the hour already owed, twenty minutes this
	// placement is what commits Mercator to, and the forty nothing will use.
	for _, term := range []struct {
		name string
		usd  float64
	}{
		{domain.CostTermCommittedRent, 0.1664},
		{domain.CostTermKeepAlive, 0.3336},
		{domain.CostTermIdleTail, 0.6664},
	} {
		charged, recorded := node.Estimates.CostTermUSD(term.name)
		if !recorded {
			t.Fatalf("the node's price is not made of %q at all: %+v", term.name, node.Estimates.CostTerms)
		}
		if math.Abs(charged-term.usd) > 0.001 {
			t.Fatalf("the node was charged %.4f USD of %s, and the hour it is bought in makes that %.4f", charged, term.name, term.usd)
		}
	}
	if fee, charged := node.Estimates.CostTermUSD(domain.CostTermSetupFee); !charged || fee != 0 {
		t.Fatalf("a machine Mercator already holds was charged %.4f USD to hand it over", fee)
	}
	winner := candidateFor(t, decision, "ask-minute")
	if node.ScoreUSD <= winner.ScoreUSD {
		t.Fatalf("the node scored %.4f against the machine that won at %.4f, and this case is about the node costing more",
			node.ScoreUSD, winner.ScoreUSD)
	}
	// What the node would have cost billed by the second one Run occupies it, which
	// is the rent for the seconds it spends and nothing else: the committed and
	// keep-alive terms are that rent split at the end of the hour already owed, so
	// they add up to it without this case restating the rate.
	shadowPriceOnly := dollarsOf(t, node, domain.CostTermCommittedRent) + dollarsOf(t, node, domain.CostTermKeepAlive)
	if shadowPriceOnly >= winner.Estimates.CostUSD.Expected {
		t.Fatalf("half an hour of the node at its own rate is %.4f USD against the winner's %.4f, so the shadow price alone would not have bought the node and this world states nothing about the terms",
			shadowPriceOnly, winner.Estimates.CostUSD.Expected)
	}

	// The sweep is the refusal. A machine an operator holds for watched work is not a
	// machine a batch Run has to wait for, so the refusal is about what the machine is.
	sweep := bookingDecisions(t, execution)["run-sweeper"]
	held := candidateFor(t, sweep, "node-owned")
	if held.Feasible {
		t.Fatalf("a batch Run was offered a machine its operator holds for interactive and standard work, priced at %.4f USD",
			held.Estimates.CostUSD.Expected)
	}
	if held.Rejections[0].Code != "CLASS_NOT_ELIGIBLE" || held.Rejections[0].Path != "capacity_terms.eligible_classes" {
		t.Fatalf("the record says the node was refused as %+v, and what it was refused for is the classes it is held for", held.Rejections[0])
	}
	if held.Standing() != domain.StandingNeverHolds {
		t.Fatalf("a machine reserved for other work counts as %v for this Run, and no amount of waiting makes a class eligible",
			held.Standing())
	}

	if _, err := execution.Check(context.Background()); err != nil {
		t.Fatalf("check invariants: %v", err)
	}
	results := latestInvariantResults(execution.invariants)
	for _, id := range []string{"safety.no_capacity_is_free", "safety.committed_cost_is_not_double_counted"} {
		if result := invariantResultByID(t, results, id); result.Status != InvariantPassed {
			t.Fatalf("%s reports %+v", id, result)
		}
	}
}

// dollarsOf is what one named term of a candidate's price came to, and a failure
// where the price is not made of it at all.
func dollarsOf(t *testing.T, candidate domain.CandidateDecision, name string) float64 {
	t.Helper()
	usd, charged := candidate.Estimates.CostTermUSD(name)
	if !charged {
		t.Fatalf("the price of %q is not made of %q: %+v", candidate.OfferSnapshotID, name, candidate.Estimates.CostTerms)
	}
	return usd
}
