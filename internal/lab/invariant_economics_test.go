package lab

import (
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
