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

// SelectionReason names the class whose exchange rates ranked the candidates, so
// the decision record says what it was scored for. A Run that asked for an
// interactive start and got the costliest machine is explained by its own class,
// and recording that as LOWEST_SCORE alone would describe the arithmetic while
// leaving out whose arithmetic it was.
func (class ServiceClass) SelectionReason() string {
	return "SERVICE_CLASS_" + strings.ToUpper(string(class))
}
