package broker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/capability"
	"github.com/benngarcia/mercator/internal/connection"
	"github.com/benngarcia/mercator/internal/domain"
)

// TestCapacityOffersReachPlacementInTheReusableLane is the seam this slice
// exists for. A provider that allocates machines and executes nothing implements
// CapacityProvider alone, which is the only shape a provider adapter can have: a
// node enrolls with the control plane rather than with the connection that rented
// the machine. Its listings have to reach a Booking Decision, stamped with the
// lane the deployment's own runtime earns them, and carrying a Rental identity
// only where one is earned.
func TestCapacityOffersReachPlacementInTheReusableLane(t *testing.T) {
	machines := &capacityBackend{
		negotiated: negotiatedCapacity(),
		listed: []domain.OfferSnapshot{
			{ID: "held", Kind: domain.OfferKindStanding, NativeRef: "i-held"},
			{ID: "catalog", Kind: domain.OfferKindProvisionable, NativeRef: "a6000"},
		},
	}
	broker := brokerServing(t, enrolledFleet{}, map[string]capability.Backend{"machines": machines})

	collection, err := broker.CollectOffers(t.Context(), adapter.OfferRequest{WorkspaceID: "ws_1"})

	if err != nil {
		t.Fatalf("collect offers from a capacity provider: %v", err)
	}
	if machines.queried.WorkspaceID != "ws_1" {
		t.Fatalf("the capacity query = %+v, want the workspace being placed", machines.queried)
	}
	if len(collection.Offers) != 2 {
		t.Fatalf("offers = %#v, want both machines the provider listed", collection.Offers)
	}
	for _, offer := range collection.Offers {
		if offer.Lane != domain.LaneReusable {
			t.Errorf("offer %q lane = %q, want %q", offer.NativeRef, offer.Lane, domain.LaneReusable)
		}
	}
	held, catalog := collection.Offers[0], collection.Offers[1]
	if held.NativeRef != "i-held" || catalog.NativeRef != "a6000" {
		held, catalog = catalog, held
	}
	if held.RentalID != held.ID {
		t.Errorf("a machine the connection already holds got Rental identity %q, want its own %q", held.RentalID, held.ID)
	}
	if catalog.RentalID != "" {
		t.Errorf("a catalog listing of a machine nobody has allocated claimed Rental %q", catalog.RentalID)
	}
}

// TestCapacityIsRefusedWhereNothingCouldExecuteOnIt states the other half of the
// same rule. A Mercator with no enrolled node runtime has nothing that could run
// a second workload on a rented machine, so the connection is refused where it is
// built rather than publishing offers whose lane is a guess.
func TestCapacityIsRefusedWhereNothingCouldExecuteOnIt(t *testing.T) {
	broker := brokerServing(t, nil, map[string]capability.Backend{
		"machines": &capacityBackend{negotiated: negotiatedCapacity()},
	})

	aggregation, err := broker.AggregateOffers(t.Context(), adapter.OfferRequest{WorkspaceID: "ws_1"})

	if err != nil {
		t.Fatalf("aggregate offers: %v", err)
	}
	if len(aggregation.Failures) != 1 || aggregation.Failures[0].ConnectionID != "conn_machines" {
		t.Fatalf("failures = %#v, want the capacity connection refused", aggregation.Failures)
	}
	if !strings.Contains(aggregation.Failures[0].Error(), "node runtime") {
		t.Fatalf("refusal = %q, want it to name the runtime nothing can execute without", aggregation.Failures[0].Error())
	}
}

// TestListOwnedReportsMachinesBesideOneShotExecutions is the sweep. A capacity
// connection holds machines and no executions, and asking it for executions used
// to fail the whole workspace's sweep: one connection with no EphemeralExecutor
// meant nothing anywhere in the fleet could be reconciled.
func TestListOwnedReportsMachinesBesideOneShotExecutions(t *testing.T) {
	broker := brokerServing(t, enrolledFleet{}, map[string]capability.Backend{
		"machines": &capacityBackend{
			negotiated: negotiatedCapacity(),
			held: []capability.OwnedCapacity{{
				NativeRef:      "i-orphan",
				WorkspaceID:    "ws_1",
				OwnershipToken: "own_1",
				State:          capability.CapacityStateActive,
				CreatedAt:      time.Unix(1_700_000_000, 0).UTC(),
			}},
		},
		"oneshot": ownedAdapter{id: "oneshot"},
	})

	owned, err := broker.ListOwned(t.Context(), adapter.OwnershipQuery{WorkspaceID: "ws_1"})

	if err != nil {
		t.Fatalf("sweep a workspace holding a capacity connection: %v", err)
	}
	found := map[string]adapter.OwnedExternalObject{}
	for _, object := range owned {
		found[object.ExternalID] = object
	}
	machine, listed := found["i-orphan"]
	if !listed {
		t.Fatalf("owned = %#v, want the machine the capacity connection holds", owned)
	}
	if machine.ConnectionID != "conn_machines" || machine.OwnershipToken != "own_1" {
		t.Errorf("machine = %+v, want the connection that holds it and the token proving it", machine)
	}
	if machine.RunID != "" || machine.LaunchKey != "" {
		t.Errorf("a machine was reported as a workload: %+v", machine)
	}
	if _, listed := found["ext_oneshot"]; !listed {
		t.Fatalf("owned = %#v, want the one-shot execution as well", owned)
	}
}

// TestAProviderThatEnumeratesNothingReportsNothingToTheSweep holds the negotiated
// answer apart from a failure. A provider that deduplicates every provision on an
// operation key loses no machine to a lost response, so there is nothing for a
// sweep to discover, and CapacitySupport.Validate already refuses the one set
// where silence here would hide a leak.
func TestAProviderThatEnumeratesNothingReportsNothingToTheSweep(t *testing.T) {
	machines := &capacityBackend{negotiated: capability.CapacitySupport{
		IdempotentProvision: capability.IdempotentProvisionOperationKey,
	}}
	broker := brokerServing(t, enrolledFleet{}, map[string]capability.Backend{"machines": machines})

	owned, err := broker.ListOwned(t.Context(), adapter.OwnershipQuery{WorkspaceID: "ws_1"})

	if err != nil {
		t.Fatalf("sweep a workspace whose provider enumerates nothing: %v", err)
	}
	if len(owned) != 0 {
		t.Fatalf("owned = %#v, want nothing from a provider that promised no listing", owned)
	}
	if machines.enumerated {
		t.Fatal("the sweep asked a provider for a listing it never promised")
	}
}

// TestAStopIsRefusedAtTheSeamByAProviderThatPromisedNone is what negotiation is
// for. Suspending a machine is a promise a provider makes or does not, and a
// caller that sends one anyway has to be refused before any request leaves
// Mercator: a provider API asked to do something it cannot may still have changed
// something by the time it says so.
func TestAStopIsRefusedAtTheSeamByAProviderThatPromisedNone(t *testing.T) {
	machines := &capacityBackend{negotiated: capability.CapacitySupport{
		IdempotentProvision: capability.IdempotentProvisionOperationKey,
		ListOwned:           true,
	}}
	broker := brokerServing(t, enrolledFleet{}, map[string]capability.Backend{"machines": machines})

	_, err := broker.StopCapacity(t.Context(), capability.CapacityCommand{
		CapacityRef: capability.CapacityRef{
			WorkspaceID:  "ws_1",
			ConnectionID: "conn_machines",
			RentalID:     "rnt_1",
			NativeRef:    "i-held",
		},
		OperationKey: "op_stop_1",
	})

	if !errors.Is(err, capability.ErrCapabilityUnsupported) {
		t.Fatalf("error = %#v, want capability.ErrCapabilityUnsupported", err)
	}
	if !strings.Contains(err.Error(), "provisioner") || !strings.Contains(err.Error(), "stop") {
		t.Fatalf("refusal = %q, want it to name the provider and the operation", err.Error())
	}
	if machines.stops != 0 {
		t.Fatalf("the stop reached the provider %d time(s) despite being unsupported", machines.stops)
	}
}

func TestAStopReachesAProviderThatPromisedOne(t *testing.T) {
	machines := &capacityBackend{negotiated: negotiatedCapacity()}
	broker := brokerServing(t, enrolledFleet{}, map[string]capability.Backend{"machines": machines})

	receipt, err := broker.StopCapacity(t.Context(), capability.CapacityCommand{
		CapacityRef: capability.CapacityRef{
			WorkspaceID:  "ws_1",
			ConnectionID: "conn_machines",
			RentalID:     "rnt_1",
			NativeRef:    "i-held",
		},
		OperationKey: "op_stop_1",
	})

	if err != nil {
		t.Fatalf("stop a machine a provider promised it can suspend: %v", err)
	}
	if receipt.State != capability.CapacityStateStopping {
		t.Fatalf("receipt = %+v, want the provider's own answer", receipt)
	}
	if machines.stops != 1 {
		t.Fatalf("the provider was stopped %d time(s), want once", machines.stops)
	}
}

// negotiatedCapacity is the set a provider that holds machines properly answers
// with: it suspends and resumes one machine under one identity, keeps its disk,
// deduplicates a repeated provision, and can say what this connection owns.
func negotiatedCapacity() capability.CapacitySupport {
	return capability.CapacitySupport{
		Stop:                true,
		Resume:              true,
		PersistentDisk:      true,
		ExactPricing:        true,
		IdempotentProvision: capability.IdempotentProvisionOperationKey,
		ListOwned:           true,
	}
}

// capacityBackend is a connection that allocates machines and executes nothing,
// which is every provider adapter. It embeds the contract so a call this case is
// not about panics rather than quietly answering.
type capacityBackend struct {
	capability.CapacityProvider
	negotiated capability.CapacitySupport
	listed     []domain.OfferSnapshot
	held       []capability.OwnedCapacity

	queried    capability.CapacityQuery
	enumerated bool
	stops      int
}

func (backend *capacityBackend) CapacitySupport() capability.CapacitySupport {
	return backend.negotiated
}

func (backend *capacityBackend) Verify(context.Context) error { return nil }

func (backend *capacityBackend) ListCapacity(_ context.Context, query capability.CapacityQuery) ([]domain.OfferSnapshot, error) {
	backend.queried = query
	return backend.listed, nil
}

func (backend *capacityBackend) ListOwnedCapacity(context.Context, capability.OwnershipQuery) ([]capability.OwnedCapacity, error) {
	backend.enumerated = true
	return backend.held, nil
}

func (backend *capacityBackend) StopCapacity(_ context.Context, command capability.CapacityCommand) (capability.CapacityReceipt, error) {
	backend.stops++
	return capability.CapacityReceipt{NativeRef: command.NativeRef, State: capability.CapacityStateStopping}, nil
}

// enrolledFleet is the deployment's enrolled node runtime as the Broker sees it.
// It embeds Nodes for the same reason the capacity double embeds its contract: a
// command no case here sends must fail loudly if something sends it.
type enrolledFleet struct{ Nodes }

func (enrolledFleet) NodeSupport() capability.NodeSupport {
	return capability.NodeSupport{ContainerRuntime: "docker", MaxConcurrentWorkloads: 1}
}

func (enrolledFleet) Offers(context.Context, string) ([]domain.OfferSnapshot, error) {
	return nil, nil
}

// brokerServing is one workspace's fleet: a connection per backend, all of one
// registered adapter type, and the deployment's enrolled node runtime or nothing
// where the case is about a Mercator that holds no machines of its own.
func brokerServing(t *testing.T, fleet Nodes, backends map[string]capability.Backend) *Broker {
	t.Helper()
	factory := NewFactory()
	factory.Register(adapter.Manifest{Type: "provisioner"}, func(config map[string]string, _ string) (capability.Backend, error) {
		return backends[config["id"]], nil
	})
	records := make([]connection.Record, 0, len(backends))
	for id := range backends {
		records = append(records, connection.Record{
			ID:          "conn_" + id,
			AdapterType: "provisioner",
			Authorized:  true,
			Config:      map[string]string{"id": id},
		})
	}
	options := []Option{}
	if fleet != nil {
		options = append(options, WithNodes(fleet))
	}
	return NewBroker(fakeConns{recs: records}, factory, nilResolver{}, options...)
}
