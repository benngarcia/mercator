package lab

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
)

// This file is the laws about money. What a machine costs is the one term of the
// score that is not a prediction: it is arithmetic over a published price and the
// terms the capacity was sold on, so it can be checked rather than calibrated,
// and the two ways of getting it wrong both point the same direction.
//
// A machine priced at nothing wins every placement it is weighed in, and the
// machines that get priced at nothing are the ones Mercator already holds. A
// committed interval charged to everything that could use it makes the fleet
// report a bill nobody will send, which is the same error read the other way: the
// money is real, so counting it twice is as wrong as counting it never.

// noCapacityIsFree is the law that every machine somebody quoted costs something,
// including the ones Mercator already pays for.
//
// An owned machine is the case it exists for. A node an operator enrolled sends
// no invoice per Run, and an interval Mercator has already committed to is money
// already gone, so a model asking "what does this decision add to the bill" can
// reach zero honestly and be wrong: the seconds are the scarce thing, nothing
// else can have them, and a candidate offered at no cost wins every placement it
// is weighed in whatever else is true about it. That is how a fleet ends up
// putting everything on one machine and reporting that it spent nothing.
//
// It reads the terms as well as the total, because a total is the number two
// opposite mistakes agree on. A machine charged the shadow price of one Run's
// seconds and a machine charged the whole hour it is committed to are the same
// kind of claim to a reader who only sees dollars, and the difference is which
// term they are in. A price whose parts do not add up to it is a price nobody can
// argue with, so the sum is checked too.
//
// A machine nobody quoted is exempt and carries no terms at all. That is the
// stated absence CandidateDecision.Priced reads, and pricing it at zero to
// satisfy this law would be the exact fabrication the law is about.
func noCapacityIsFree(observation InvariantObservation) error {
	decisions, err := recordedDecisions(observation)
	if err != nil {
		return err
	}
	for _, decision := range decisions {
		for _, candidate := range decision.Candidates {
			if err := candidateCostIsAccountedFor(decision, candidate); err != nil {
				return err
			}
		}
	}
	return nil
}

func candidateCostIsAccountedFor(decision domain.BookingDecision, candidate domain.CandidateDecision) error {
	cost, terms := candidate.Estimates.CostUSD, candidate.Estimates.CostTerms
	if !candidate.Priced() {
		if len(terms) > 0 {
			return fmt.Errorf(
				"Run %q: candidate %q states that nobody quoted it and then accounts for %.6f USD of price: %s",
				decision.RunID, candidate.OfferSnapshotID, candidate.Estimates.CostTermTotalUSD(), describeCostTerms(candidate),
			)
		}
		return nil
	}
	switch {
	case cost.Expected <= 0:
		return fmt.Errorf(
			"Run %q: candidate %q is priced at %.6f USD from %q, and no capacity is free: %s",
			decision.RunID, candidate.OfferSnapshotID, cost.Expected, cost.Source, describeCostTerms(candidate),
		)
	case len(terms) == 0:
		return fmt.Errorf(
			"Run %q: candidate %q is priced at %.6f USD and the record says nothing about what that price is made of",
			decision.RunID, candidate.OfferSnapshotID, cost.Expected,
		)
	case math.Abs(candidate.Estimates.CostTermTotalUSD()-cost.Expected) > arithmeticTolerance:
		return fmt.Errorf(
			"Run %q: candidate %q is priced at %.6f USD and the terms recorded beside it add up to %.6f: %s",
			decision.RunID, candidate.OfferSnapshotID, cost.Expected, candidate.Estimates.CostTermTotalUSD(), describeCostTerms(candidate),
		)
	}
	return nil
}

// committedCostIsNotDoubleCounted is the other half of the same rule. Rent inside
// an interval Mercator has already committed to is charged to the Run that spends
// those seconds, and one second of one interval belongs to one Run.
//
// It is stated over the placements Mercator took rather than over every candidate
// it weighed, because candidates are alternatives: two machines weighed for one
// Run are two ways of spending the same money, and neither of them has spent it.
// What may not happen is two placements on one machine, live at the same time,
// each charged for the same second of the same interval, which is a fleet
// reporting a bill nobody will send.
//
// A model that priced a commitment from the moment of the decision instead of
// from the moment the Run gets the machine does exactly that: everything queued
// behind an hour of work is charged the whole hour still outstanding, and every
// one of those records looks right on its own. The overlap is only visible across
// them, which is why the record carries when each placement's charged stretch
// begins as well as how long it is.
//
// A superseded decision is left out. An appended decision that names the one it
// replaces has replaced it, so the two are one placement in two records, and
// reading both would report a machine double-charged by the audit trail that
// exists to say it was not.
func committedCostIsNotDoubleCounted(observation InvariantObservation) error {
	decisions, err := recordedDecisions(observation)
	if err != nil {
		return err
	}
	charged := map[string][]chargedInterval{}
	for _, decision := range effectiveDecisions(decisions) {
		selected, taken := placedCandidate(decision)
		if !taken {
			continue
		}
		committed := selected.Estimates.Committed
		if committed.Until.IsZero() || committed.Seconds <= 0 {
			continue
		}
		stretch := chargedInterval{
			runID: decision.RunID,
			from:  decision.EvaluatedAt.Add(secondsOf(committed.FromSeconds)),
		}
		stretch.until = stretch.from.Add(secondsOf(committed.Seconds))
		// A millisecond of slack, because the charged stretch is derived from seconds
		// stated as floating point and an interval boundary read to the nanosecond
		// would report the rounding as a fleet overcharging itself.
		if stretch.until.After(committed.Until.Add(time.Millisecond)) {
			return fmt.Errorf(
				"Run %q: the placement on %q is charged committed rent through %s, and the interval Mercator owes on that machine ends at %s",
				decision.RunID, selected.OfferSnapshotID, stretch.until.UTC(), committed.Until.UTC(),
			)
		}
		machine := chargedMachine(selected, committed)
		if clash, overlaps := overlapping(charged[machine], stretch); overlaps {
			return fmt.Errorf(
				"Run %q: the placement on %q is charged the seconds from %s to %s of one committed interval, and Run %q was charged from %s to %s of the same one",
				decision.RunID, selected.OfferSnapshotID,
				stretch.from.UTC(), stretch.until.UTC(), clash.runID, clash.from.UTC(), clash.until.UTC(),
			)
		}
		charged[machine] = append(charged[machine], stretch)
	}
	return nil
}

// chargedInterval is one placement's claim on one machine's already-owed rent.
type chargedInterval struct {
	runID string
	from  time.Time
	until time.Time
}

// chargedMachine is what the charged rent is charged against: the machine the
// backend named, and the interval on it. Two listings of one machine are one
// machine, which is what the candidate identity is for; capacity whose backend
// names no machine falls back to the offer, because a listing nobody can name a
// machine behind is a machine nobody else's listing can be.
func chargedMachine(candidate domain.CandidateDecision, committed domain.CommittedInterval) string {
	name := candidate.Candidate.Machine
	if name == "" {
		name = candidate.OfferSnapshotID
	}
	return name + "@" + committed.Until.UTC().String()
}

// overlapping reports whether this stretch of committed rent shares a second with
// one already charged to somebody else.
func overlapping(already []chargedInterval, stretch chargedInterval) (chargedInterval, bool) {
	for _, charged := range already {
		if stretch.from.Before(charged.until) && charged.from.Before(stretch.until) {
			return charged, true
		}
	}
	return chargedInterval{}, false
}

// effectiveDecisions are the decisions that still stand: every recorded one
// except those a later decision says it replaces.
func effectiveDecisions(decisions []domain.BookingDecision) []domain.BookingDecision {
	superseded := map[string]bool{}
	for _, decision := range decisions {
		if decision.Supersedes != "" {
			superseded[decision.Supersedes] = true
		}
	}
	standing := make([]domain.BookingDecision, 0, len(decisions))
	for _, decision := range decisions {
		if !superseded[decision.ID] {
			standing = append(standing, decision)
		}
	}
	sort.SliceStable(standing, func(i, j int) bool { return standing[i].EvaluatedAt.Before(standing[j].EvaluatedAt) })
	return standing
}

// placedCandidate is the candidate a decision took capacity on, and nothing for a
// decision that placed the Run nowhere. It reads the Booking as well as the
// selection, because a decision with no Booking spent nothing.
func placedCandidate(decision domain.BookingDecision) (domain.CandidateDecision, bool) {
	if decision.SelectedOfferSnapshotID == "" || decision.Booking == nil {
		return domain.CandidateDecision{}, false
	}
	index := slices.IndexFunc(decision.Candidates, func(candidate domain.CandidateDecision) bool {
		return candidate.OfferSnapshotID == decision.SelectedOfferSnapshotID
	})
	if index < 0 {
		return domain.CandidateDecision{}, false
	}
	return decision.Candidates[index], true
}

// describeCostTerms is what a candidate's price is made of, in the words the
// record files each part under, so a violation says which term the dollars were
// in rather than only that they were wrong.
func describeCostTerms(candidate domain.CandidateDecision) string {
	if len(candidate.Estimates.CostTerms) == 0 {
		return "no terms recorded"
	}
	parts := make([]string, 0, len(candidate.Estimates.CostTerms))
	for _, term := range candidate.Estimates.CostTerms {
		parts = append(parts, fmt.Sprintf("%s %.6f USD", term.Name, term.USD))
	}
	return "priced as " + strings.Join(parts, ", ")
}

func secondsOf(count float64) time.Duration {
	return time.Duration(count * float64(time.Second))
}
