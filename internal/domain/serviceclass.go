package domain

import (
	"slices"
	"strings"
)

// This file is what a Run says about the kind of work it is. A ServiceClass is
// the whole of that statement: the class is what decides how a candidate is
// scored, because it is the only thing in the system that can say what a second
// of waiting is worth. A caller knows whether somebody is watching this Run
// start; nothing Mercator could measure knows that.
//
// It replaced PlacementObjective outright, with no shim and nothing derived
// between them. An objective named a quantity to minimise and left the exchange
// rate unstated, so the terms that would have converted seconds into dollars
// were multiplied by zero and the objective had to be honoured as a ranking
// instead. A class states the rate, which is what lets one score be computed
// over cost, waiting, and doubt together.

// ServiceClass is the kind of work a Run is, as its caller declared it.
type ServiceClass string

const (
	// ClassInteractive is work somebody is waiting on right now.
	ClassInteractive ServiceClass = "interactive"
	// ClassStandard is work that should get on with it and has nobody watching.
	ClassStandard ServiceClass = "standard"
	// ClassBatch is work whose value is in the result and not in when it began.
	ClassBatch ServiceClass = "batch"
	// ClassExperimental is a researcher's iteration: the answer is the point, and
	// the sooner it lands the sooner the next one starts.
	ClassExperimental ServiceClass = "experimental"
	// ClassOpportunistic is work that runs when capacity is going spare. It
	// states that waiting costs it nothing, which is what makes it the one class
	// ranked on price alone.
	ClassOpportunistic ServiceClass = "opportunistic"
)

// KnownServiceClasses is every class Mercator can price, and the only list of
// them. A class outside it is refused where the Run enters rather than defaulted
// to the cheapest ranking, because silently placing work whose urgency nobody
// stated is how a caller learns their word was ignored from the bill.
var KnownServiceClasses = []ServiceClass{
	ClassInteractive,
	ClassStandard,
	ClassBatch,
	ClassExperimental,
	ClassOpportunistic,
}

// Known reports whether this is a class Mercator can price.
func (class ServiceClass) Known() bool {
	return slices.Contains(KnownServiceClasses, class)
}

// WaitingUSDPerSecond is what one second of waiting costs the machine that is
// doing the waiting: 1.80 USD an hour, roughly the rent on a small accelerated
// box. It is a stated assumption rather than a measurement, and it is the only
// one of its kind here: every class declares its own multiple of this number, so
// there is one rate to argue with rather than five.
const WaitingUSDPerSecond = 0.0005

// UncertaintySeconds is how long a class is willing to wait rather than be told
// something nobody stands behind. It converts a confidence shortfall into
// dollars at the class's own rate, so a class that hates waiting hates doubt in
// the same proportion, and the penalty is derived from the exchange rate the
// class already declared instead of being a second number invented beside it.
const UncertaintySeconds = 60.0

// Weights is what this class says a second of waiting and an unreliable answer
// are worth. Every class measures waiting to the moment that matters to it: to
// the start, for work with something watching it, and to the finish, for work
// whose value is the result.
//
// A class Mercator does not know declares nothing, and nothing asks it to:
// CreateRun refuses such a Run at the door and Placement refuses to rank one.
func (class ServiceClass) Weights() ScoreWeights {
	switch class {
	case ClassInteractive:
		// Somebody is watching this start, so their time is what waiting spends.
		// Thirty-six dollars an hour is a deliberately conservative statement of
		// what that is worth beside the machine's own rent.
		return waitingToStart(20 * WaitingUSDPerSecond)
	case ClassStandard:
		// Waiting costs what the machine doing it costs.
		return waitingToStart(WaitingUSDPerSecond)
	case ClassExperimental:
		// The answer is the point, and the next iteration is blocked behind it,
		// so this class pays twice the machine's rent to finish sooner.
		return waitingToFinish(2 * WaitingUSDPerSecond)
	case ClassBatch:
		// Batch work would rather be cheap than early, and says so: a fifth of
		// the rent, counted to the finish.
		return waitingToFinish(WaitingUSDPerSecond / 5)
	case ClassOpportunistic:
		// Waiting is free and so is doubt. This class takes whatever costs least
		// and has no opinion about when it runs.
		return ScoreWeights{}
	}
	return ScoreWeights{}
}

// waitingToStart prices the seconds until this Run can begin.
func waitingToStart(usdPerSecond float64) ScoreWeights {
	return ScoreWeights{
		StartLatencyUSDPerSecond: usdPerSecond,
		UncertaintyPenaltyUSD:    UncertaintySeconds * usdPerSecond,
	}
}

// waitingToFinish prices the seconds until this Run has an answer, which is its
// start plus the runtime it declared. That runtime is the same for every
// candidate until something predicts per-candidate throughput, so this orders
// candidates exactly as pricing the start does today. It is written as the sum
// it means rather than as the shortcut it currently equals.
func waitingToFinish(usdPerSecond float64) ScoreWeights {
	return ScoreWeights{
		CompletionLatencyUSDPerSecond: usdPerSecond,
		UncertaintyPenaltyUSD:         UncertaintySeconds * usdPerSecond,
	}
}

// TopClassPriority is what the most urgent class starts at, and the only number
// the aging rate below is derived from. It exists so a class cannot be given a
// priority nothing can ever overtake: every rate here is stated as "how long
// until this class outranks anything that could arrive".
const TopClassPriority = 100.0

// Admission is what a class says about waiting: where it starts in the queue,
// how long Mercator may make it wait at all, the moment it must have started by,
// and whether it may go past work that is already waiting.
//
// It is the class's statement rather than the Run's for the same reason the
// exchange rates above are. A caller knows whether somebody is watching this
// Run; nothing Mercator could measure knows it, and a per-Run priority is a
// number every caller sets to the maximum.
type Admission struct {
	Priority             float64
	MaxQueueDelaySeconds float64
	// DeadlineSeconds is how long after admission first deferred it a Run of
	// this class may still be started. Zero is a class that states no such
	// moment, which is not a deadline of nothing: it is work whose value does
	// not expire.
	DeadlineSeconds float64
	// BackfillEligible is the one thing that lets a Run past work already
	// waiting that outranks it. It belongs to the class going past rather than
	// to the class being passed, because taking capacity that is going spare is
	// a statement about what this work is worth, not about what the other work
	// is.
	BackfillEligible bool
}

// Admission is what this class says about waiting.
func (class ServiceClass) Admission() Admission {
	switch class {
	case ClassInteractive:
		// Somebody is watching. Five minutes unplaced is already a failure, and
		// ten is long past the point the answer was worth having.
		return Admission{Priority: TopClassPriority, MaxQueueDelaySeconds: 5 * 60, DeadlineSeconds: 10 * 60}
	case ClassExperimental:
		// The next iteration is blocked behind this one, so a quarter of an hour
		// of queue is the most it is worth, and an hour is the whole afternoon.
		return Admission{Priority: 70, MaxQueueDelaySeconds: 15 * 60, DeadlineSeconds: 60 * 60}
	case ClassStandard:
		// Nobody is watching, and it should still get on with it.
		return Admission{Priority: 50, MaxQueueDelaySeconds: 30 * 60, DeadlineSeconds: 4 * 60 * 60}
	case ClassBatch:
		// The value is in the result rather than in when it began, so an hour of
		// queue is tolerable and a day is the point it stops being this run.
		return Admission{Priority: 20, MaxQueueDelaySeconds: 60 * 60, DeadlineSeconds: 24 * 60 * 60}
	case ClassOpportunistic:
		// Waiting is free and the work does not expire, so this class states no
		// deadline and is the only one that may take capacity going spare. Two
		// hours is not what it is worth waiting: it is the longest Mercator will
		// let anything sit unplaced without saying it has a starvation problem.
		return Admission{Priority: 0, MaxQueueDelaySeconds: 2 * 60 * 60, BackfillEligible: true}
	}
	return Admission{}
}

// AgingPerSecond is how fast waiting promotes a Run of this class. It is derived
// rather than declared: a Run outranks every class once it has waited half its
// own maximum queue delay, which leaves the other half of the bound for capacity
// to come free.
//
// Deriving it is what makes the bound a promise rather than a hope. A rate
// chosen independently of the bound can be too slow for it, and the class would
// then declare a maximum queue delay nothing in the ordering ever works toward.
func (policy Admission) AgingPerSecond() float64 {
	if policy.MaxQueueDelaySeconds <= 0 {
		return 0
	}
	return (TopClassPriority + 1 - policy.Priority) / (policy.MaxQueueDelaySeconds / 2)
}

// EffectivePriority is what a Run of this class is worth after waiting this long.
func (policy Admission) EffectivePriority(queuedSeconds float64) float64 {
	if queuedSeconds <= 0 {
		return policy.Priority
	}
	return policy.Priority + policy.AgingPerSecond()*queuedSeconds
}

// Starved reports whether this Run has waited longer than its class allows, which
// is the one bound with a consequence at both ends of the queue.
//
// Nothing may be admitted past a Run in that state, backfill included: the
// exemption that lets spare capacity be taken is about capacity going spare, and
// capacity a starved Run is waiting for is not spare.
//
// The Run itself is refused rather than kept, because the wait is already longer
// than the class agreed to and going on with it promises nothing. That is the whole
// of what the number means: a class states how long Mercator may make it wait, and
// a bound with no consequence is a comment.
func (policy Admission) Starved(queuedSeconds float64) bool {
	return policy.MaxQueueDelaySeconds > 0 && queuedSeconds > policy.MaxQueueDelaySeconds
}

// DeadlinePassed reports whether the moment this class says a Run of it must have
// started by is already behind Mercator. It needs nothing predicted and nothing
// measured: a deadline that has elapsed is a fact about the clock.
//
// It is the half of the rule that governs a Run about to be placed. A Run whose
// capacity arrives after its own moment is a Run whose answer stopped being worth
// having before the machine was there, and starting it then spends the money to
// produce an answer nobody is waiting for.
func (policy Admission) DeadlinePassed(queuedSeconds float64) bool {
	return policy.DeadlineSeconds > 0 && queuedSeconds >= policy.DeadlineSeconds
}

// DeadlineUnreachable reports whether this Run can no longer start in time, from
// what has already elapsed and what the record says is in front of it.
//
// It is asked of a projected wait rather than of a guess, and where nothing
// projected one it answers no: a Run refused capacity by machines that publish
// no schedule is a Run whose wait nobody measured, and refusing it would turn
// that silence into a missed deadline by arithmetic. The elapsed half needs
// nothing measured at all, which is DeadlinePassed above.
func (policy Admission) DeadlineUnreachable(queuedSeconds, waitSeconds float64, projected bool) bool {
	switch {
	case policy.DeadlinePassed(queuedSeconds):
		return true
	case policy.DeadlineSeconds <= 0:
		return false
	default:
		return projected && queuedSeconds+waitSeconds > policy.DeadlineSeconds
	}
}

// SelectionReason names the class whose exchange rates ranked the candidates, so
// the decision record says what it was scored for. A Run that asked for an
// interactive start and got the costliest machine is explained by its own class,
// and recording that as LOWEST_SCORE alone would describe the arithmetic while
// leaving out whose arithmetic it was.
func (class ServiceClass) SelectionReason() string {
	return "SERVICE_CLASS_" + strings.ToUpper(string(class))
}
