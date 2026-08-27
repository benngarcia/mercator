package orchestrator

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/benngarcia/mercator/internal/capability"
	"github.com/benngarcia/mercator/internal/node"
)

// instantCapacity is the capacity lease and the node registry as the unit tests
// in this package need them: a machine allocated the moment it is asked for,
// with an agent already on it. Every test here is about what happens to a Run
// once it has a machine, so a double that spends time on the three provisioning
// stages would only make each of them drive a clock they are not about.
//
// What it does not do is fake the shape of the contract. It allocates one
// machine per Rental, answers a repeat under the same key as a duplicate, and
// enumerates what it holds, because those are the promises the orchestrator acts
// on and a double that broke them would let a defect through.
type instantCapacity struct {
	mu     sync.Mutex
	leases map[string]*instantLease
	nodes  map[string]instantNode
}

// instantNode is one identity this double invited, with the moment its agent
// opened a session. The moment is the invitation's own, because that is the
// claim: the machine was allocated and its agent was already there.
type instantNode struct {
	rentalID   string
	enrolledAt time.Time
}

type instantLease struct {
	rentalID     string
	nativeRef    string
	connectionID string
	terminated   bool
}

// withTestCapacity gives one orchestrator both seams from one double, because a
// placement that chooses to provision is an act and these tests drive Runs past
// that point.
func withTestCapacity() Option {
	seam := &instantCapacity{leases: map[string]*instantLease{}, nodes: map[string]instantNode{}}
	return func(o *Orchestrator) {
		WithCapacity(seam)(o)
		WithInviter(seam)(o)
	}
}

func (c *instantCapacity) ProvisionCapacity(_ context.Context, command capability.ProvisionCommand) (capability.CapacityReceipt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if held, exists := c.leases[command.RentalID]; exists && !held.terminated {
		return capability.CapacityReceipt{NativeRef: held.nativeRef, State: capability.CapacityStateActive, Duplicate: true}, nil
	}
	held := &instantLease{
		rentalID:     command.RentalID,
		nativeRef:    "machine-" + command.RentalID,
		connectionID: command.ConnectionID,
	}
	c.leases[command.RentalID] = held
	return capability.CapacityReceipt{NativeRef: held.nativeRef, State: capability.CapacityStateActive}, nil
}

func (c *instantCapacity) ObserveCapacity(_ context.Context, ref capability.CapacityRef) (capability.CapacityObservation, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	held, exists := c.leases[ref.RentalID]
	if !exists {
		return capability.CapacityObservation{}, fmt.Errorf("nothing allocated for Rental %q", ref.RentalID)
	}
	state := capability.CapacityStateActive
	if held.terminated {
		state = capability.CapacityStateTerminated
	}
	return capability.CapacityObservation{NativeRef: held.nativeRef, State: state}, nil
}

func (c *instantCapacity) TerminateCapacity(_ context.Context, command capability.CapacityCommand) (capability.CapacityReceipt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	held, exists := c.leases[command.RentalID]
	if !exists {
		return capability.CapacityReceipt{}, fmt.Errorf("nothing allocated for Rental %q", command.RentalID)
	}
	held.terminated = true
	return capability.CapacityReceipt{NativeRef: held.nativeRef, State: capability.CapacityStateTerminated}, nil
}

func (c *instantCapacity) ListOwnedCapacity(_ context.Context, _ capability.OwnershipQuery) ([]capability.OwnedCapacity, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var owned []capability.OwnedCapacity
	for _, held := range c.leases {
		if held.terminated {
			continue
		}
		owned = append(owned, capability.OwnedCapacity{
			NativeRef:    held.nativeRef,
			ConnectionID: held.connectionID,

			RentalID: held.rentalID,
			State:    capability.CapacityStateActive,
		})
	}
	return owned, nil
}

func (c *instantCapacity) Invite(_ context.Context, invitation node.Invitation) (capability.NodeBootstrap, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.nodes[invitation.NodeID]; exists {
		return capability.NodeBootstrap{}, fmt.Errorf("%w: %s", node.ErrIdentityExists, invitation.NodeID)
	}
	c.nodes[invitation.NodeID] = instantNode{rentalID: invitation.RentalID, enrolledAt: time.Now().UTC()}
	return capability.NodeBootstrap{
		NodeID:          invitation.NodeID,
		RentalID:        invitation.RentalID,
		Generation:      invitation.Generation,
		EnrollmentToken: "enrol-" + invitation.NodeID,
	}, nil
}

func (c *instantCapacity) Reinvite(_ context.Context, nodeID string, _ time.Time) (capability.NodeBootstrap, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	invited, exists := c.nodes[nodeID]
	if !exists {
		return capability.NodeBootstrap{}, fmt.Errorf("%w: %s", node.ErrNotFound, nodeID)
	}
	return capability.NodeBootstrap{NodeID: nodeID, RentalID: invited.rentalID, Generation: 1, EnrollmentToken: "enrol-" + nodeID}, nil
}

// EnrolledAt dates the session from the invitation for any identity this double
// invited, because the agent on a machine it allocated is already there.
func (c *instantCapacity) EnrolledAt(_ context.Context, ref capability.NodeRef) (time.Time, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.nodes[ref.NodeID].enrolledAt, nil
}
