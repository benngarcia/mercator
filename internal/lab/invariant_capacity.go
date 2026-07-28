package lab

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"time"
)

// provisionedCapacityBound is how long a machine a provider allocated may go on
// existing with nothing that will come for it. It is deliberately longer than
// any patience a listing in this corpus states, because this rule is not the
// deadline: the deadline is Mercator's own, stated per listing, and this is the
// backstop that catches a control plane that never applied one at all.
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
		rentalID, err := capacityRentalOf(effect)
		if err != nil {
			return nil, err
		}
		if existing, seen := allocated[rentalID]; seen && !effect.At.Before(existing) {
			continue
		}
		allocated[rentalID] = effect.At
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
		rentalID, err := capacityRentalOf(effect)
		if err != nil {
			return nil, err
		}
		settled[rentalID] = true
	}
	return settled, nil
}

// capacityRentalOf is the lease one capacity entry is about. An entry that names
// none is refused rather than skipped: this rule is keyed on the lease, and a
// record read as an entry about the empty lease would settle or allocate a
// machine nobody meant.
func capacityRentalOf(effect EffectRecord) (string, error) {
	var facts struct {
		RentalID string `json:"rental_id"`
	}
	if err := json.Unmarshal(effect.Request, &facts); err != nil {
		return "", fmt.Errorf("decode capacity entry %s: %w", effect.ID, err)
	}
	if facts.RentalID == "" {
		return "", fmt.Errorf("capacity entry %s is a %s naming no Rental", effect.ID, effect.Operation)
	}
	return facts.RentalID, nil
}
