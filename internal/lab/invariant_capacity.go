package lab

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"time"
)

// provisionedCapacityBound is how long a machine a provider allocated may go on
// existing with nothing that will come for it. It is longer than any patience a
// listing in this corpus states, because this rule is not the deadline: the
// deadline is Mercator's own, stated per listing, and this is the backstop that
// catches a control plane that never applied one at all.
//
// That ordering is enforced rather than observed. Compile refuses a Blueprint
// stating more patience than this, because a listing telling Mercator to wait
// longer than the harness allows would make this rule accuse a control plane that
// is obeying the fixture. See patienceStaysInsideTheLabsOwnBound.
const provisionedCapacityBound = 30 * time.Minute

// provisionedCapacityEnrolsOrIsReclaimed is the liveness rule on the capacity
// lease: a provision that reached a provider ends as an enrolled node, as a
// terminated machine, or as a refusal the provider recorded, and never as a
// machine billing with nothing that will come for it.
//
// It is stated over the effect ledger and nothing else, because the ledger is
// the only account of what really happened to a machine. Mercator's own record
// can say a Run moved on; only this says whether the machine it moved off is
// still allocated. That gap is exactly the failure this exists to catch, and it
// is the one a provider bills for: a control plane that decides again and leaves
// the first machine to the provider's own backstop is one an operator pays twice
// for every attempt.
//
// A provision the provider rejected is an ending. Nothing was allocated, so
// there is no machine to account for, and requiring a terminate for it would be
// requiring Mercator to destroy something that does not exist.
func provisionedCapacityEnrolsOrIsReclaimed(observation InvariantObservation) error {
	allocated, err := allocatedCapacity(observation.Effects)
	if err != nil {
		return err
	}
	settled, err := settledCapacity(observation.Effects)
	if err != nil {
		return err
	}
	for _, rentalID := range slices.Sorted(maps.Keys(allocated)) {
		accepted := allocated[rentalID]
		if settled[rentalID] {
			continue
		}
		// Still inside the backstop, so nothing has gone wrong yet: a machine being
		// built is a machine nothing has come for and everything is still going to.
		if observation.Now.Sub(accepted) <= provisionedCapacityBound {
			continue
		}
		return fmt.Errorf(
			"capacity allocated for Rental %q at %s has been billing for %s with no node enrolled on it and no terminate against it",
			rentalID, accepted.Format(time.RFC3339), observation.Now.Sub(accepted).Round(time.Second),
		)
	}
	return nil
}

// allocatedCapacity is every machine a provider accepted a provision for, by the
// lease it was allocated against and the moment it was accepted.
//
// A duplicate is an allocation too, and it is the first acceptance that dates
// it: a provider answering the same operation key twice is one machine that has
// been billing since the first answer, and dating it from the repeat would reset
// the clock every time a reconciler asked.
func allocatedCapacity(effects []EffectRecord) (map[string]time.Time, error) {
	allocated := map[string]time.Time{}
	for _, effect := range effects {
		if effect.Operation != OperationCapacityProvision {
			continue
		}
		if effect.Command == EffectCommandRejected {
			continue
		}
		lease, err := capacityLeaseOf(effect)
		if err != nil {
			return nil, err
		}
		if existing, seen := allocated[lease.RentalID]; seen && !effect.At.Before(existing) {
			continue
		}
		allocated[lease.RentalID] = effect.At
	}
	return allocated, nil
}

// settledCapacity is every lease something has happened to that ends Mercator's
// question about it: an agent enrolled on the machine, which makes it capacity
// Mercator can execute on, or a terminate the provider accepted, which ends the
// bill.
func settledCapacity(effects []EffectRecord) (map[string]bool, error) {
	settled := map[string]bool{}
	for _, effect := range effects {
		switch effect.Operation {
		case OperationNodeEnrolled, OperationCapacityTerminate:
		default:
			continue
		}
		if effect.Command == EffectCommandRejected {
			continue
		}
		lease, err := capacityLeaseOf(effect)
		if err != nil {
			return nil, err
		}
		settled[lease.RentalID] = true
	}
	return settled, nil
}

// enrolmentNamesTheGenerationItWasInvitedFor holds the one property that keeps a
// Node bound to a single Rental generation: an agent that opened a session on a
// machine a provider allocated enrolled under the generation that machine was
// invited for, and never under another.
//
// A generation is what fences a lease. The machine allocated for generation two
// is a different machine from the one allocated for generation one, and an
// enrolment filed under the wrong one is a session Mercator would tie to a
// machine that no longer exists: every later act keyed on the pair, the fencing
// token the node redeems included, would be addressed to the wrong one.
//
// A lease with no provision behind it is exempt, and that is standing capacity
// rather than a hole in the rule. A world that seeds a machine and enrols the
// agent Mercator holds it through never allocated anything, so there is no
// invitation to be right or wrong about.
func enrolmentNamesTheGenerationItWasInvitedFor(observation InvariantObservation) error {
	invited, err := invitedGenerations(observation.Effects)
	if err != nil {
		return err
	}
	for _, effect := range observation.Effects {
		if effect.Operation != OperationNodeEnrolled || effect.Command == EffectCommandRejected {
			continue
		}
		lease, err := capacityLeaseOf(effect)
		if err != nil {
			return err
		}
		generations, allocated := invited[lease.RentalID]
		if !allocated || generations[lease.Generation] {
			continue
		}
		return fmt.Errorf(
			"enrolment %s opened a session under Rental %q generation %d, and the machines allocated for that lease were invited for %v",
			effect.ID, lease.RentalID, lease.Generation, slices.Sorted(maps.Keys(generations)),
		)
	}
	return nil
}

// invitedGenerations is every generation of every lease a provider accepted a
// machine for. A lease may hold more than one: a generation that ends invites a
// second machine under the same lease, and both are enrolments this rule allows.
func invitedGenerations(effects []EffectRecord) (map[string]map[uint64]bool, error) {
	invited := map[string]map[uint64]bool{}
	for _, effect := range effects {
		if effect.Operation != OperationCapacityProvision || effect.Command == EffectCommandRejected {
			continue
		}
		lease, err := capacityLeaseOf(effect)
		if err != nil {
			return nil, err
		}
		if invited[lease.RentalID] == nil {
			invited[lease.RentalID] = map[uint64]bool{}
		}
		invited[lease.RentalID][lease.Generation] = true
	}
	return invited, nil
}

// capacityLeaseRef is the lease and generation one capacity entry is about, which
// is the pair every act against a machine is addressed to. An entry naming no
// lease is refused rather than skipped: every rule here is keyed on the lease, and
// a record read as an entry about the empty lease would settle or allocate a
// machine nobody meant.
type capacityLeaseRef struct {
	RentalID   string `json:"rental_id"`
	Generation uint64 `json:"generation"`
}

func capacityLeaseOf(effect EffectRecord) (capacityLeaseRef, error) {
	var lease capacityLeaseRef
	if err := json.Unmarshal(effect.Request, &lease); err != nil {
		return capacityLeaseRef{}, fmt.Errorf("decode capacity entry %s: %w", effect.ID, err)
	}
	if lease.RentalID == "" {
		return capacityLeaseRef{}, fmt.Errorf("capacity entry %s is a %s naming no Rental", effect.ID, effect.Operation)
	}
	return lease, nil
}
