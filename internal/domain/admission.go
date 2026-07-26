package domain

// This file is the queue Mercator did not have. A Run nothing could place was
// retried on the next minute tick for ever and recorded nothing, so the only
// queue in the system was an implicit loop and nobody could ask a Run what it
// was waiting for. A deferral is that answer written down: why the Run is
// waiting, what it is waiting behind, and what its class was worth at the
// moment it was asked.

const (
	// DeferredNoFeasibleOffer is a Run nothing would take, waiting for capacity to
	// come free. Every candidate was struck out, and what it waits behind is the
	// work occupying the capacity those candidates were refused from. A fleet that
	// published nothing at all is the same wait: nothing was weighed against this
	// Run, so nothing has been established about what would hold it.
	DeferredNoFeasibleOffer = "NO_FEASIBLE_OFFER"
	// DeferredNoCapacityFits is a Run nothing would take, waiting for capacity to
	// be added. Every machine the fleet published was weighed against it, and none
	// of them could hold it once the capacity it is spending now comes back, so
	// waiting for the fleet as it stands changes nothing.
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
	// Weighed is how many machines the fleet published that this Run was measured
	// against, and CouldHold how many of those could take it once the capacity
	// they are spending now comes back.
	//
	// They are the evidence the reason above rests on, and they are recorded
	// because the reason alone cannot be checked. A wait for capacity to be added
	// and a fleet that published nothing read identically to an operator who is
	// told only that no offer was feasible, and a rule that has to police which of
	// the two waits Mercator recorded has nothing to police it against.
	//
	// A wait admission caused rather than Placement weighed nothing, and says so
	// with a zero: what a Run held behind work that outranks it is waiting for is
	// the queue, and the fleet was never asked.
	Weighed   int           `json:"weighed"`
	CouldHold int           `json:"could_hold"`
	Behind    []QueuedAhead `json:"behind,omitempty"`
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
