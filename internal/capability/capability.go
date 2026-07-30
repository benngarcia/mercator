// Package capability declares the three contracts a backend can implement and
// the capabilities it negotiates through them.
//
// Mercator brokers two materially different things and must never confuse
// them. A CapacityProvider allocates and holds machine capacity. A NodeRuntime
// executes successive workloads on capacity Mercator controls through an
// enrolled agent. An EphemeralExecutor runs one workload on a provider-native
// execution product Mercator does not control between workloads.
//
// The lane a backend declares is what the connection sells. A CapacityProvider
// sells machines that outlive the workloads run on them, which is the reusable
// lane; an EphemeralExecutor sells one execution that holds nothing afterwards,
// which is the ephemeral one.
//
// A lane is not a licence to place work. Whether a workload can actually run on
// one machine is a fact about that machine, established by the node runtime
// enrolled on it, and no per-connection declaration can state it: a node enrolls
// with the control plane rather than with the connection that rented the machine
// it runs on. That is why the offers Mercator places against are the enrolled
// nodes' own, and why a capacity connection publishes no placement candidate
// until a Rental lifecycle can provision a machine and bootstrap an agent onto
// it. See broker.Backend.ListOffers.
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
	// Node is present exactly when the backend implements NodeRuntime, which is
	// a runtime bound to one host. It is absent for a provider adapter, because
	// the runtime that executes on a rented machine enrolls with the control
	// plane rather than with the connection that rented it, and absent for every
	// one-shot connection, because nothing executes a second workload there.
	Node *NodeSupport `json:"node,omitempty"`
	// Ephemeral is present exactly when the backend implements
	// EphemeralExecutor.
	Ephemeral *EphemeralSupport `json:"ephemeral,omitempty"`
}

// Declare derives a backend's Declaration from the contracts it implements.
//
// Capacity is the reusable lane, because a machine a provider allocates and
// holds is exactly capacity that outlives the workload run on it. There is no
// second condition on it: a deployment-wide "does anything execute here" check
// was one this deployment could always answer yes to while owning no runtime on
// the rented machine at all, so it refused nothing and licensed a Rental
// identity for a machine Mercator had not allocated. The falsifiable claim is
// per machine, and the enrolled node runtime is what makes it, which is why a
// capacity connection publishes no placement candidate.
func Declare(adapterType string, backend Backend) (Declaration, error) {
	declaration, err := implemented(adapterType, backend)
	if err != nil {
		return Declaration{}, err
	}
	if declaration.Capacity != nil {
		declaration.Lane = domain.LaneReusable
	} else {
		declaration.Lane = domain.LaneEphemeral
	}
	return declaration, nil
}

// implemented is what the backend itself satisfies, with the combinations no one
// implementation can mean refused before any lane is derived from them.
func implemented(adapterType string, backend Backend) (Declaration, error) {
	declaration := Declaration{Type: adapterType}
	if provider, ok := backend.(CapacityProvider); ok {
		support := provider.CapacitySupport()
		if err := support.Validate(); err != nil {
			return Declaration{}, fmt.Errorf("capability: %q negotiates a capacity set no provider could keep: %w", adapterType, err)
		}
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
	case declaration.Node != nil && declaration.Ephemeral != nil:
		return Declaration{}, fmt.Errorf(
			"capability: %q implements NodeRuntime and EphemeralExecutor, which claims one backend both controls and does not control its host runtime",
			adapterType,
		)
	case declaration.Capacity != nil && declaration.Ephemeral != nil:
		return Declaration{}, fmt.Errorf(
			"capability: %q provides capacity and one-shot execution on one connection, and one lane is stamped on every offer a connection publishes, so nothing could say which of the two an offer came from",
			adapterType,
		)
	}
	return declaration, nil
}

// StampLane marks every offer with the lane its backend actually serves and
// clears whatever Rental identity the adapter stated.
//
// A Rental is Mercator's own lease record, so only Mercator mints one, and it
// mints it where it holds the machine: the offers that carry a Rental identity
// are the enrolled nodes' own, from the invitation that named the Rental, and
// they never pass through here. An adapter that filled the field in from its
// instance type or its contract id would publish a Rental Mercator does not hold
// on /v1/offers, and a Booking bound to it would let a second Run queue behind a
// lease that never existed. Clearing it unconditionally is why no lane needs
// checking here.
func StampLane(declaration Declaration, offers []domain.OfferSnapshot) []domain.OfferSnapshot {
	for index := range offers {
		offers[index].Lane = declaration.Lane
		offers[index].RentalID = ""
	}
	return offers
}
