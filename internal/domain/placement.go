package domain

import "math"

// This file is the score. One number decides a placement, and the whole of it is
// stated here: the dollars the Run will be billed, plus the dollars its
// ServiceClass says it would rather pay than wait, plus the dollars it would
// rather pay than act on an answer nobody stands behind.
//
// Before a class declared those rates, the second and third terms were
// multiplied by zero for every Run in production, so the score was the price and
// nothing else, and the Run's stated objective had to order candidates on its
// own to mean anything at all. The exchange rate is what removes that
// indirection: the class states what waiting costs it, and cost and waiting
// become comparable quantities rather than two rankings.

// ScoreWeights is what one ServiceClass declares its seconds and its doubts are
// worth. It is stated by the class rather than passed into Placement, because a
// weight nothing populates is a term multiplied by zero, and this whole file was
// dead code for exactly that reason.
type ScoreWeights struct {
	StartLatencyUSDPerSecond      float64 `json:"start_latency_usd_per_second,omitempty"`
	CompletionLatencyUSDPerSecond float64 `json:"completion_latency_usd_per_second,omitempty"`
	// UncertaintyPenaltyUSD is what a whole point of doubt costs. A point is one
	// answer worth nothing; see CandidateDecision.Uncertainty for what counts.
	UncertaintyPenaltyUSD float64 `json:"uncertainty_penalty_usd,omitempty"`
}

// ScoreUSD is what this candidate is worth to a Run whose class declared these
// weights, in dollars, lowest first.
//
// It reads nothing but the candidate record the decision keeps, which is what
// makes the score reproducible: a reader with the decision in front of them can
// re-derive this number, and a scoring term whose input is not recorded cannot be
// added without the Lab noticing. Two definitions of uncertainty drifted apart
// under exactly that blind spot, one of them reading facts off the offer that no
// decision carried, and neither could be caught while both were multiplied by
// zero.
//
// An infeasible candidate scores nothing. It has no price because it is not for
// sale, and ranking it beside the others would have the cheapest refusal win.
//
// A candidate nobody quoted scores its waiting and its doubt and no dollars,
// because there are none to state and inventing a zero would price the absence.
// That number is not comparable with a priced candidate's, which is why Preferred
// asks Priced first and the decision records which rule ranked them.
func (weights ScoreWeights) ScoreUSD(candidate CandidateDecision, expectedRuntimeSeconds float64) float64 {
	if !candidate.Feasible {
		return 0
	}
	start := candidate.Estimates.StartSeconds.Expected
	return round(candidate.Estimates.CostUSD.Expected+
		weights.StartLatencyUSDPerSecond*start+
		weights.CompletionLatencyUSDPerSecond*(start+expectedRuntimeSeconds)+
		weights.UncertaintyPenaltyUSD*candidate.Uncertainty(), 6)
}

// Uncertainty is how far this candidate's own answers fall short of certainty,
// summed over the confidences the decision recorded beside it.
//
// It counts confidences and never facts. A host that could not say what it holds
// is already priced twice for that silence, once as the whole content it might
// have to fetch and once as the confidence cap on the seconds that takes, so
// adding a point for the unknown inventory on top was charging the same doubt a
// third time. What is left is the honest question: how much is each answer this
// candidate was scored on worth?
//
// An answer nobody stated a confidence for states no opinion and counts nothing.
// Zero is silence rather than worthlessness, and a doubt about a silence is not
// two doubts.
//
// Which means an answer may only be doubted here if the score reads the answer
// itself. Otherwise the term runs backwards: a publisher that measures a machine
// and states its own confidence is charged, a publisher that says nothing is not,
// and a publisher certain of the worst news is not either, so the score improves
// the less anybody stands behind and improves again when nobody speaks. That is
// the inverse of modelling the unknown as uncertainty, and it is what happened
// while the published reliability history was doubted here and priced nowhere.
// ScoredAnswers is that rule written down, and
// safety.doubt_only_the_answers_the_score_reads is what holds every recorded
// decision to it.
func (candidate CandidateDecision) Uncertainty() float64 {
	shortfall := 0.0
	for _, confidence := range candidate.Confidences {
		if confidence.Value > 0 && confidence.Value < 1 {
			shortfall += 1 - confidence.Value
		}
	}
	return shortfall
}

// AnswerCapacity is the claim that this machine can be had at all, named here
// because the score reads it: an unavailable machine is infeasible and an
// infeasible candidate is not for sale. It is a constant rather than a word
// spelled at each site for the reason a stage's answer is derived from the
// stage, which is that the same answer spelled independently in two places lets
// a rule stated over one of them pass while the other says something else.
const AnswerCapacity = "capacity"

// ScoredAnswers is every question this score reads an answer to, and therefore
// the whole of what Uncertainty may charge doubt about. The capacity claim
// decides whether a candidate is for sale, and each stage of a launch is a term
// of the start the two latency rates are multiplied by, so a shortfall in any of
// them is doubt about a number the score used.
//
// Nothing else may appear beside a candidate as a confidence. An answer the score
// does not read charges its publisher for having answered and charges silence
// nothing, which ranks the machine nobody measured above the machine measured and
// never seen to fail. A published reliability history was doubted here for a
// phase on exactly that footing, and the term was invisible while every weight
// that multiplied it was zero.
func ScoredAnswers() []string {
	answers := make([]string, 0, len(LaunchStages)+1)
	answers = append(answers, AnswerCapacity)
	for _, stage := range LaunchStages {
		answers = append(answers, stage.ConfidenceAnswer())
	}
	return answers
}

// Priced reports whether the dollars in this candidate's cost estimate are a
// price somebody quoted. It reads the record rather than the offer, like every
// other input to the ranking, and the record states it as the source of the cost
// estimate, which is where every other missing answer here says it is missing.
func (candidate CandidateDecision) Priced() bool {
	return candidate.Estimates.CostUSD.Source != CostUnpriced
}

// Preferred reports whether this candidate is the better placement. It is a
// total order: candidates that tie on dollars take the one that is ready sooner,
// and candidates that tie on both fall back to the offer snapshot ID, so one
// offer set produces one decision however the offers arrived.
//
// A machine nobody priced ranks behind every machine somebody did, and that is
// asked first because it is not a comparison of dollars at all. The score is in
// dollars and a candidate with no price has none: scoring the absence as zero
// made the unpriced machine the cheapest thing in the fleet, so a Run that allowed
// unknown pricing took it every time, however much later it would start and with
// no price at all. AllowUnknownPricing says a caller would rather run on a machine
// nobody priced than not run, which is what this ordering means, and never that
// they prefer one.
//
// Otherwise there is one rule for every class, because the class is already in the
// score. A ranking per class was what the objective needed while the exchange
// rates were dead; now that a second of waiting has a price, a Run that hates
// waiting and a Run that does not are comparing the same quantity in the same
// units.
func (candidate CandidateDecision) Preferred(over CandidateDecision) bool {
	if candidate.Priced() != over.Priced() {
		return candidate.Priced()
	}
	if candidate.ScoreUSD != over.ScoreUSD {
		return candidate.ScoreUSD < over.ScoreUSD
	}
	if candidate.Estimates.StartSeconds.Expected != over.Estimates.StartSeconds.Expected {
		return candidate.Estimates.StartSeconds.Expected < over.Estimates.StartSeconds.Expected
	}
	return candidate.OfferSnapshotID < over.OfferSnapshotID
}

// round is how precisely a score is stated. Six places is well below a cent and
// well above the noise of summing four terms, and it is applied in one place so
// the reference model and the record cannot disagree about the last digit.
func round(value float64, places int) float64 {
	factor := math.Pow10(places)
	return math.Round(value*factor) / factor
}
