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
	// RefusedDeadlineUnreachable is a Run whose class states a moment it must
	// have started by that the queue in front of it is already past. It is
	// refused rather than queued, because queueing it would be promising a
	// start the record already says cannot happen.
	RefusedDeadlineUnreachable = "DEADLINE_UNREACHABLE"
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
// Only the fleet's own answer can say it, and it says it about the moment it was
// given. A deferral carrying no answer establishes no exemption: a Run held
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
	return deferral.Fleet != nil && deferral.Fleet.HoldsNothing()
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
