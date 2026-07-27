package domain

import (
	"math"
	"slices"
	"time"
)

// This file is what capacity costs. A rate per second is one term of it and
// never the whole, because a machine Mercator already holds is not free and a
// machine Mercator does not hold yet is not billed by the second it uses.
//
// Before this, one number was multiplied by one Run's seconds and that was the
// whole price of every candidate in the fleet. An enrolled node inside an hour
// Mercator has already committed to came out at the shadow price of the half
// hour one Run occupied, and the rest of the hour, which nothing else could then
// use and which Mercator pays for either way, was charged to nobody. A
// marketplace machine billed by the minute was charged for a fractional minute
// no provider sells. Both mistakes point the same way: they make owned and
// already-committed capacity look cheaper than it is, which is exactly the
// direction that spends money.

// CapacityTerms is what a machine is sold on beyond its rate: the interval
// Mercator has already committed to paying for, the classes of work it may be
// used for at all, and the moment it stops being available.
//
// They are terms of a sale rather than facts about a moment, so a backend states
// them once and republishes them with every snapshot. A listing for a machine
// that does not exist yet states none of them: nothing is owed on capacity
// nobody has allocated, it is reserved for nobody, and it is available for as
// long as the listing is.
type CapacityTerms struct {
	// CommittedUntil is the moment the interval Mercator already owes rent for
	// ends. Rent inside it is money already spent, so the seconds of it a Run
	// occupies are the cheapest seconds in the fleet, and the seconds of it
	// nothing occupies are the most wasted.
	//
	// Zero is capacity nothing is owed on, which is every machine nobody has
	// allocated. It is not a commitment that has already lapsed: a machine whose
	// interval ended is a machine whose next second forces a fresh one.
	CommittedUntil time.Time `json:"committed_until,omitzero"`
	// EligibleClasses is which kinds of work may run here, and empty is every
	// kind. It is how reserved capacity is stated: an operator who holds a machine
	// for the work somebody is waiting on says so here, and a batch sweep is
	// refused the machine rather than priced on it.
	EligibleClasses []ServiceClass `json:"eligible_classes,omitempty"`
	// AvailableUntil is the moment this capacity stops being Mercator's to use.
	// It is a window an operator or a provider declared, which is a different
	// thing from capacity that may be reclaimed without notice: the moment is
	// known, so work that finishes inside it is never at risk and work that could
	// outlive it is refused before it starts.
	AvailableUntil time.Time `json:"available_until,omitzero"`
}

// Admits reports whether work of this class may run on this capacity at all.
// Capacity reserved for nobody in particular admits everything.
func (terms CapacityTerms) Admits(class ServiceClass) bool {
	return len(terms.EligibleClasses) == 0 || slices.Contains(terms.EligibleClasses, class)
}

// Occupancy is when a Run would hold a machine and for how long. Both ends
// matter and they answer different questions: what a placement costs is decided
// by the seconds it expects to occupy, and whether it may be placed at all is
// decided by the seconds Mercator would enforce, because a machine has to be
// available for the whole of what a Run is allowed to take.
type Occupancy struct {
	// At is the moment the decision is being made, which is what every offset
	// below is measured from.
	At time.Time
	// StartSeconds is how long before this Run begins occupying the machine: the
	// wait in front of it plus the stages of its own launch. It is why one
	// commitment can be spent by two Runs without being charged twice, because
	// the seconds each of them occupies begin where the other's end.
	StartSeconds float64
	// RuntimeSeconds is how long the Run expects to hold the machine.
	RuntimeSeconds float64
	// MaxRuntimeSeconds is the longest Mercator would let it, which is the bound
	// the Rental Schedule reserves against.
	MaxRuntimeSeconds float64
}

// Begins is the moment this Run would start occupying the machine.
func (occupancy Occupancy) Begins() time.Time {
	return occupancy.At.Add(seconds(occupancy.StartSeconds))
}

// LatestEnd is the moment this Run must be off the machine by, which is its own
// start plus the runtime Mercator enforces.
func (occupancy Occupancy) LatestEnd() time.Time {
	return occupancy.Begins().Add(seconds(occupancy.MaxRuntimeSeconds))
}

// CommittedSeconds is how many of the seconds this Run would occupy are already
// paid for. It is the overlap of the occupancy with the committed interval, and
// it is asked from the Run's own start rather than from now: two Runs queued on
// one machine occupy different seconds of one interval, and a model that charged
// each of them everything still outstanding would count the same money twice and
// report a fleet costing more than the invoices it will get.
func (terms CapacityTerms) CommittedSeconds(occupancy Occupancy) float64 {
	if terms.CommittedUntil.IsZero() {
		return 0
	}
	outstanding := terms.CommittedUntil.Sub(occupancy.Begins()).Seconds()
	return math.Max(0, math.Min(occupancy.RuntimeSeconds, outstanding))
}

// OutlivesWindow reports whether this Run could still be holding the machine
// after the moment the machine stops being available. It asks about the runtime
// Mercator enforces rather than the one the Run expects, because the expectation
// is what the caller guessed and the bound is what Mercator would actually
// allow: admitting on the guess puts work on capacity that disappears underneath
// it whenever the guess is short.
func (terms CapacityTerms) OutlivesWindow(occupancy Occupancy) bool {
	return !terms.AvailableUntil.IsZero() && occupancy.LatestEnd().After(terms.AvailableUntil)
}

// BilledSeconds is what a publisher charges for holding this machine for these
// seconds: the increment it sells rounded up to, because a provider that bills
// by the minute bills a whole minute for a second of use.
//
// A publisher that states no increment is billing continuously, and the seconds
// stand as they are. Inventing an increment there would charge a tail nobody
// sells; every backend in this tree states one.
func (price PriceModel) BilledSeconds(seconds float64) float64 {
	if price.GranularitySeconds <= 0 || seconds <= 0 {
		return math.Max(0, seconds)
	}
	increment := float64(price.GranularitySeconds)
	return math.Ceil(seconds/increment) * increment
}

// CostTerm is one part of what a placement costs and what that part is worth. A
// price recorded as one number cannot be argued with: an operator reading a
// candidate that came out at 1.17 USD cannot tell rent from the tail of an
// interval nothing will use, and neither can a rule about whether owned capacity
// was charged for at all.
type CostTerm struct {
	Name string  `json:"name"`
	USD  float64 `json:"usd"`
}

// The names a placement's dollars are filed under. They are constants because
// two spellings of one term is a term a rule stated over one of them cannot
// see.
const (
	// CostTermSetupFee is what a provider charges to hand over a machine, paid by
	// capacity Mercator has to acquire and already paid by capacity it holds.
	CostTermSetupFee = "setup_fee"
	// CostTermCommittedRent is rent for the seconds of this occupancy that fall
	// inside an interval Mercator has already committed to. The money is spent
	// whatever this decision does, and the seconds are not: work placed here
	// spends seconds nothing else can then have, which is what an owned machine's
	// shadow price is a statement of.
	CostTermCommittedRent = "committed_rent"
	// CostTermKeepAlive is rent for the seconds of this occupancy beyond that
	// interval. It is the money this placement is what commits Mercator to.
	CostTermKeepAlive = "keep_alive"
	// CostTermIdleTail is rent for seconds this placement forces Mercator to buy
	// and nothing uses: the remainder of the last increment its publisher sells.
	// A machine billed in hours, asked for twenty minutes past its commitment,
	// costs the hour.
	CostTermIdleTail = "idle_tail"
)

// CostTermNames is every name a placement's dollars may be filed under, in the
// order a placement incurs them. It exists so a Blueprint asserting a term and a
// rule reading one cannot name a term nothing writes.
func CostTermNames() []string {
	return []string{
		CostTermSetupFee,
		CostTermCommittedRent,
		CostTermKeepAlive,
		CostTermIdleTail,
	}
}

// CostTermUSD is what one named term of this candidate's price came to, and
// nothing where the candidate was not charged for it.
func (estimates CandidateEstimates) CostTermUSD(name string) (float64, bool) {
	for _, term := range estimates.CostTerms {
		if term.Name == name {
			return term.USD, true
		}
	}
	return 0, false
}

// CostTermTotalUSD is what the terms recorded beside this candidate add up to.
// It is what the total is checked against: a price nothing accounts for is a
// number nobody can argue with, and safety.no_capacity_is_free holds every
// recorded candidate to the sum.
func (estimates CandidateEstimates) CostTermTotalUSD() float64 {
	total := 0.0
	for _, term := range estimates.CostTerms {
		total += term.USD
	}
	return total
}
