// Package capability declares the three contracts a backend can implement and
// the capabilities it negotiates through them.
//
// Mercator brokers two materially different things and must never confuse
// them. A CapacityProvider allocates and holds machine capacity. A NodeRuntime
// executes successive workloads on capacity Mercator controls through an
// enrolled agent. An EphemeralExecutor runs one workload on a provider-native
// execution product Mercator does not control between workloads.
//
// The lane a backend declares is a claim about reuse, and a claim Mercator
// verifies: reusable capacity requires a NodeRuntime, because without one there
// is no host runtime capable of executing a second workload. Declare returns an
// error rather than letting a backend advertise reuse it cannot perform.
package capability

import (
	"fmt"

	"github.com/benngarcia/mercator/internal/domain"
)

// Backend is one built connection's implementation. It satisfies at least one
// of the three contracts in this package; Declare reports which, and refuses
// combinations that would claim semantics the implementation cannot deliver.
type Backend any

// Declaration is what one backend claims it can do. It is checked against the
// interfaces the backend actually implements, so a lane is evidence rather
// than an assertion.
type Declaration struct {
	// Type is the adapter type string ("docker", "shadeform", …).
	Type string               `json:"type"`
	Lane domain.ExecutionLane `json:"lane"`
	// Capacity is present exactly when the backend implements CapacityProvider.
	Capacity *CapacitySupport `json:"capacity,omitempty"`
	// Node is present exactly when the backend implements NodeRuntime.
	Node *NodeSupport `json:"node,omitempty"`
	// Ephemeral is present exactly when the backend implements
	// EphemeralExecutor.
	Ephemeral *EphemeralSupport `json:"ephemeral,omitempty"`
}

// Declare derives a backend's Declaration from the contracts it implements and
// refuses any claim the implementation cannot support.
func Declare(adapterType string, backend any) (Declaration, error) {
	declaration := Declaration{Type: adapterType}
	if provider, ok := backend.(CapacityProvider); ok {
		support := provider.CapacitySupport()
		declaration.Capacity = &support
	}
	if runtime, ok := backend.(NodeRuntime); ok {
		support := runtime.NodeSupport()
		declaration.Node = &support
	}
	if executor, ok := backend.(EphemeralExecutor); ok {
		support := executor.EphemeralSupport()
		declaration.Ephemeral = &support
	}
	switch {
	case declaration.Capacity == nil && declaration.Ephemeral == nil:
		return Declaration{}, fmt.Errorf(
			"capability: %q implements neither CapacityProvider nor EphemeralExecutor",
			adapterType,
		)
	case declaration.Capacity != nil && declaration.Node != nil:
		declaration.Lane = domain.LaneReusable
	case declaration.Capacity != nil:
		return Declaration{}, fmt.Errorf(
			"capability: %q provides capacity without a NodeRuntime, so nothing can execute successive workloads on it",
			adapterType,
		)
	case declaration.Node != nil && declaration.Ephemeral != nil:
		return Declaration{}, fmt.Errorf(
			"capability: %q implements NodeRuntime and EphemeralExecutor, which claims one backend both controls and does not control its host runtime",
			adapterType,
		)
	default:
		declaration.Lane = domain.LaneEphemeral
	}
	return declaration, nil
}

// StampLane marks every offer with the lane its backend actually serves, and
// strips the Rental identity from offers that cannot become Rentals. Every
// aggregation path calls it, which is what stops an adapter from advertising
// reuse it cannot perform.
func StampLane(declaration Declaration, offers []domain.OfferSnapshot) []domain.OfferSnapshot {
	for index := range offers {
		offers[index].Lane = declaration.Lane
		if !declaration.Lane.Reusable() {
			offers[index].RentalID = ""
		}
	}
	return offers
}
