package domain

// This file is the queue Mercator did not have. A Run nothing could place was
// retried on the next minute tick for ever and recorded nothing, so the only
// queue in the system was an implicit loop and nobody could ask a Run what it
// was waiting for. A deferral is that answer written down: why the Run is
// waiting, what it is waiting behind, and what its class was worth at the
// moment it was asked.

const (
	// DeferredNoFeasibleOffer is a Run nothing would take, waiting for capacity to
	// come free. Every candidate was struck out and at least one of them could take
	// the Run once the capacity it is spending comes back, so what this Run waits
	// behind is the work occupying that machine.
	DeferredNoFeasibleOffer = "NO_FEASIBLE_OFFER"
	// DeferredNoCapacityFits is a Run nothing would take, waiting for capacity to
	// be added. Every machine the fleet published was weighed against it, and none
	// of them could hold it once the capacity it is spending now comes back, so
	// waiting for the fleet as it stands changes nothing.
	//
	// A fleet that published nothing this Run's ask even matches is the same wait,
	// stated as strongly as a fleet can state it. An offer query is a search on the
	// shape asked for, so a marketplace that answered with nothing has said it sells
	// no machine of that shape at all, and no amount of waiting for the fleet as it
	// stands turns that into a machine. Calling that a wait for capacity to come
	// free is what let the most impossible ask of all be the one wait nothing
	// exempted: it kept the queue, aged past every other class, and stalled a
	// workspace whose fleet was selling exactly what the work behind it asked for.
	//
	// It is decided from what each machine refused the Run for and never from what
	// each machine happens to be doing. A refusal that names capacity somebody is
	// spending ends when they stop spending it; a refusal that names what the
	// machine is does not end at all. Reading a Booking as the difference made
	// every occupied machine look like a wait, so one ask nothing could hold
	// emptied a fleet as soon as any other Run was running on it.
	//
	// It is a reason of its own because the queue turns on the difference. Work
	// behind a Run waiting for a machine to come free is waiting for that same
	// machine. Work behind a Run waiting for a machine to arrive is only being
	// stopped by it, and one impossible submission would otherwise empty a fleet.
	DeferredNoCapacityFits = "NO_CAPACITY_FITS"
	// DeferredCapacityUnstated is a Run nothing would take, where what stopped at
	// least one machine from taking it is a fact nobody published. A node that
	// could not measure its disk is the case: it is not a machine with no room
	// and it is not a machine holding capacity somebody else is spending, so
	// neither of the two waits above describes it and answering with either would
	// be Mercator deciding a question the fleet left open.
	//
	// It holds the queue, because the machine may turn out to be able to take this
	// Run the moment it answers, and work behind it would then be competing for
	// exactly that machine. Uncertainty is not infeasibility here for the same
	// reason it is not when a host cannot say what content it holds.
	DeferredCapacityUnstated = "CAPACITY_UNSTATED"
	// DeferredBehindHigherPriority is a Run that could have been placed and may
	// not be, because work Mercator already owes an answer to outranks it. The
	// ordering is on effective priority and not on the class alone: a Run of the
	// same class that has waited longer outranks a fresh arrival, and a reason
	// naming the class would report that as a class this Run is behind.
	DeferredBehindHigherPriority = "BEHIND_HIGHER_PRIORITY"
	// DeferredGroupAtParallelism is a Run whose own family is already as wide as
	// its caller said it may run. Nothing about the fleet is being asserted: a
	// machine may be standing idle beside this Run, and the bound is the whole
	// reason it is not being used, which is what a caller asks for when it declares
	// one.
	//
	// It is the first thing admission asks, ahead of the ordering and ahead of any
	// machine being weighed, because it is the only answer here that no ordering and
	// no capacity can change. Asking it later recorded a Run as waiting behind work
	// that outranked it while the thing actually holding it was its own declaration,
	// and let it hold the queue against work that had nothing to do with the family.
	DeferredGroupAtParallelism = "GROUP_AT_PARALLELISM"
	// RefusedDeadlineUnreachable is a Run whose class states a moment it must
	// have started by that the queue in front of it is already past. It is
	// refused rather than queued, because queueing it would be promising a
	// start the record already says cannot happen.
	RefusedDeadlineUnreachable = "DEADLINE_UNREACHABLE"
	// RefusedQueueDelayExceeded is a Run Mercator has already kept waiting longer
	// than its class allows. The maximum queue delay was the one bound in the class
	// nothing acted on: the ordering derived its aging rate from it and the Lab held
	// executions to it, while admission itself only ever ended a wait at the class
	// deadline. So work of a class that declares no deadline waited for ever, and
	// work of a class that declares one waited hours past a promise already broken,
	// which is a caller told nothing while Mercator spends the whole interval
	// deciding it cannot help.
	//
	// It is asked only of a Run being told to wait again, because the bound is on
	// waiting and nothing else. A Run whose capacity came free a moment after the
	// bound has stopped waiting, and refusing it there would spend the entire wait
	// and then throw away the answer it was for.
	//
	// It is measured off what has elapsed rather than off a projection, exactly as
	// DeadlinePassed is. A bound that has gone by is a fact about the clock, and
	// refusing a Run for a wait nobody measured is how the deadline rule used to
	// close Runs at their first pass on somebody else's runtime.
	RefusedQueueDelayExceeded = "QUEUE_DELAY_EXCEEDED"
)

// AdmissionDeferral is one moment admission told a Run to wait, or refused to
// let it wait at all. Both are the same account of the same question, which is
// why they are one shape: the reason says which answer it was.
type AdmissionDeferral struct {
	Reason string       `json:"reason"`
	Class  ServiceClass `json:"service_class"`
	// EffectivePriority is what this Run was worth at this moment: what its
	// class starts at plus everything waiting has added to it. It is recorded
	// rather than re-derived on read because it is the number the ordering was
	// decided on, and a reader deriving it from a class table somebody has since
	// edited would be checking today's policy against yesterday's decision.
	EffectivePriority float64 `json:"effective_priority"`
	BasePriority      float64 `json:"base_priority"`
	// QueuedSeconds is how long this Run had been waiting when it was asked,
	// measured from the first time admission deferred it.
	QueuedSeconds        float64 `json:"queued_seconds"`
	MaxQueueDelaySeconds float64 `json:"max_queue_delay_seconds"`
	// ProjectedWaitSeconds is the shortest wait the decision says this Run
	// faces, projected from the Bookings Mercator holds on the capacity that
	// could hold it. It is absent where nothing that could hold this Run carried
	// a schedule to project from, which is a wait nobody measured rather than a
	// wait of nothing.
	ProjectedWaitSeconds float64 `json:"projected_wait_seconds,omitempty"`
	// Fleet is what the fleet said about this Run at this moment, and it is the
	// evidence the reason above rests on. The reason alone cannot be checked
	// against anything, and the classification the whole queue is ordered on is
	// read off this rather than off the word Mercator wrote beside it.
	//
	// It is absent on a wait admission caused on its own account, where no machine
	// was weighed at all. That is the distinction two counts could not carry: a
	// fleet that published nothing an ask matches and a queue that never asked the
	// fleet both weighed nothing, and the first is the strongest thing a fleet can
	// say about an ask while the second says nothing about capacity whatsoever.
	Fleet  *FleetAnswer  `json:"fleet,omitempty"`
	Behind []QueuedAhead `json:"behind,omitempty"`
}

// FleetAnswer is what the fleet said about one Run at one moment: how many
// machines it published that the Run was measured against, how many of those
// could take it once the capacity they are spending now comes back, and how many
// said too little for anybody to tell.
type FleetAnswer struct {
	Weighed   int `json:"weighed"`
	CouldHold int `json:"could_hold"`
	// Unstated is the machines that refused this Run only for facts nobody
	// published. A node that could not measure its disk is what this exists for:
	// it is not a machine with no room, and counting it among the machines that
	// can never hold the work lets one failed measurement say the fleet has
	// nothing to offer, on evidence nobody produced.
	Unstated int `json:"unstated,omitempty"`
}

// HoldsNothing reports whether this is an answer no waiting can change: nothing
// the fleet published could take this Run once the capacity it is spending comes
// back, and no machine was so quiet that nobody can say. A fleet that published
// nothing the ask matches says it most strongly of all, and it is the same
// statement: there is no machine here to wait for.
//
// One machine nobody could measure stops the fleet from saying it. This is the
// strongest statement a fleet can make and it is not one to make over a silence:
// a workspace whose only node failed to stat its filesystem would otherwise
// record every Run in it as work no machine can ever hold, and lose the ordering
// of all of them to a measurement that comes back on the next heartbeat.
func (answer FleetAnswer) HoldsNothing() bool {
	return answer.CouldHold == 0 && answer.Unstated == 0
}

// Reason is the wait this answer puts a Run in, said in the words an operator
// reads. It is derived here rather than decided beside the answer so that the
// word in the record and the classification the queue is ordered on are one
// fact: a fleet that published nothing an ask matches was the case where the two
// came apart, and the strongest refusal a fleet can give was labelled as a wait
// for capacity to come free.
func (answer FleetAnswer) Reason() string {
	switch {
	case answer.CouldHold > 0:
		return DeferredNoFeasibleOffer
	case answer.Unstated > 0:
		return DeferredCapacityUnstated
	default:
		return DeferredNoCapacityFits
	}
}

// HoldsNoQueue reports whether the wait this deferral records is one other work
// does not have to be ordered behind. A Run waiting for a machine to come free
// holds the queue, because whatever is behind it wants that same machine. A Run
// nothing in the fleet can hold is waiting for capacity to arrive, and work that
// fits the fleet as it stands is not competing with it for anything.
//
// A wait its own family's declared width is holding is the other one, and it is
// the only wait here that is not about capacity at all. No machine coming free
// ends it, because the bound counts members rather than machines, so work behind
// it is not competing with it for anything. A group whose members held the queue
// would let one narrow family stop every other Run in the tenant, which is the
// same head-of-line block the fleet exemption exists to prevent.
//
// Otherwise only the fleet's own answer can say it, and it says it about the
// moment it was given. A deferral carrying no answer establishes no exemption: a Run held
// behind work that outranks it was measured against no machine at all, and an
// exemption carried through such a wait is a claim about a fleet nobody asked
// since. That is the direction the carried version got wrong. A Run outranked by
// a steady stream of arrivals never reaches Placement again, so an exemption
// granted once outlived every machine that arrived afterwards, and work that
// arrived later overtook a Run of its own class that the fleet could by then
// have held.
//
// Losing the exemption on such a wait costs nothing that was not already lost.
// A Run only fails to renew it while something outranks it, and whatever that
// Run would have been holding up is held up by the work outranking it anyway.
//
// It is one rule in one place because production orders the queue on it and the
// Lab adjudicates that ordering against it. Two readings of the same evidence
// drifted apart in both directions at once: one of them called a fleet that
// published nothing an ordinary wait, and the other could not see the ask at all.
func (deferral AdmissionDeferral) HoldsNoQueue() bool {
	if deferral.SelfImposed() {
		return true
	}
	return deferral.Fleet != nil && deferral.Fleet.HoldsNothing()
}

// SelfImposed reports whether this wait is one the caller's own declaration is
// holding rather than one Mercator's queue or Mercator's fleet is. A family
// already as wide as its caller said may run is the only one: every other wait
// here is Mercator failing to find the Run a machine, and this one is Mercator
// doing exactly what it was asked.
//
// The difference decides which of the class's two bounds may end the wait, which
// is why it is a rule of its own rather than a comparison at each door. The
// maximum queue delay is Mercator's promise about how long it may keep work
// waiting for capacity, so charging a caller's own declaration against it refused
// the later members of every family narrower than its class's patience: the
// record then said Mercator broke a promise, about a wait the caller asked for,
// while a machine that could have taken the work stood idle. The deadline is a
// different question and still applies, because it asks whether the answer is
// worth producing at all and an answer nobody is waiting for is worth nothing
// however the waiting was caused.
func (deferral AdmissionDeferral) SelfImposed() bool {
	return deferral.Reason == DeferredGroupAtParallelism
}

// QueuedAhead is one piece of work a deferred Run is waiting behind: either a
// Run holding the capacity it wanted, or a Run of a class that outranks it.
type QueuedAhead struct {
	RunID string       `json:"run_id"`
	Class ServiceClass `json:"service_class,omitempty"`
	// EffectivePriority is what the work ahead was worth at the same moment. It
	// is zero for a Run that is already running, which is not a ranking: work
	// that holds the machine is ahead because it is there, not because it
	// outranks anybody.
	EffectivePriority float64 `json:"effective_priority,omitempty"`
}
