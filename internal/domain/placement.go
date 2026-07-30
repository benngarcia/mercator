package domain

import "slices"

// This file is what a Run's stated objective does to a placement. The objective
// is public API, so "fastest_start" is a promise Mercator makes about where the
// Run lands, and until this existed it was a word that changed nothing: every
// candidate was ranked on one blended dollar score whose only time term was a
// weight nothing populated outside the balanced objective, so a Run that asked
// to start soonest was placed on whichever machine was a fraction of a cent
// cheaper and, when prices tied, on whichever offer ID sorted first.
//
// An objective is a ranking rather than an exchange rate. Converting a second
// of waiting into dollars needs a number nobody has measured, and inventing one
// per objective would be the unmeasured constant this codebase keeps deleting.
// What a Run did state is which quantity it wants least of, and that orders
// candidates on its own.

// BalancedWaitingUSDPerSecond is what the balanced objective presumes a second
// spent waiting to start is worth: 1.80 USD an hour, roughly the rent on the
// machine doing the waiting. It is a stated assumption rather than a
// measurement, said once here so the scheduler and the Lab's reference model
// cannot disagree about a number neither of them measured.
const BalancedWaitingUSDPerSecond = 0.0005

// Prefers reports whether this candidate is the better placement under the
// objective the Run stated. It is a total order: candidates that tie on every
// term the objective names fall back to the offer snapshot ID, so one offer set
// produces one decision however the offers arrived.
func (policy PlacementPolicy) Prefers(candidate, incumbent CandidateDecision) bool {
	if order := slices.Compare(policy.rank(candidate), policy.rank(incumbent)); order != 0 {
		return order < 0
	}
	return candidate.OfferSnapshotID < incumbent.OfferSnapshotID
}

// rank is what this objective orders candidates by, most significant term
// first. Every objective ends in the term another one leads with: a Run that
// asked for speed still takes the cheaper of two equally quick machines, and a
// Run that asked for price still takes the quicker of two equally cheap ones,
// which is the only thing that lets locality decide a placement between offers
// at one price.
//
// Completion is start plus the runtime the Run expects. That runtime is the
// same for every candidate today, so this ranks exactly as fastest_start does
// until something predicts per-candidate throughput, which is phase 4. It is
// written as the sum it means rather than as the shortcut it currently equals.
func (policy PlacementPolicy) rank(candidate CandidateDecision) []float64 {
	cost := candidate.ScoreUSD
	start := candidate.Estimates.StartSeconds.Expected
	switch policy.Objective {
	case ObjectiveFastestStart:
		return []float64{start, cost}
	case ObjectiveFastestCompletion:
		return []float64{start + policy.ExpectedRuntimeSeconds, cost}
	default:
		return []float64{cost, start}
	}
}

// SelectionReason names the rule that chose the winner, so the decision record
// says what it ranked on. A Run that asked for the earliest start and got the
// costliest machine is explained by its own objective, and recording that as
// LOWEST_SCORE would describe a comparison Mercator did not make.
func (policy PlacementPolicy) SelectionReason() string {
	switch policy.Objective {
	case ObjectiveFastestStart:
		return "EARLIEST_START"
	case ObjectiveFastestCompletion:
		return "EARLIEST_COMPLETION"
	default:
		return "LOWEST_SCORE"
	}
}
