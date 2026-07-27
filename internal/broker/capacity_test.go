package broker

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/capability"
	"github.com/benngarcia/mercator/internal/connection"
	"github.com/benngarcia/mercator/internal/domain"
)

// TestACapacityConnectionPublishesNoCandidateForAMachineNobodyIsOn is the rule
// that keeps a lane from becoming a licence to place work. A provider that
// allocates machines and executes nothing is the only shape a provider adapter
// has, and the machines it lists are capacity to acquire: until an agent enrolls
// on one, nothing on it can run a container, so an offer built from the listing
// would state a container runtime, an idempotent launch and free capacity that are
// the node's facts and not the provider's.
//
// The machine an agent is on is published once, by the node registry, carrying the
// Rental its invitation named. Publishing the provider's own listing of that same
// machine beside it counted one host twice under two Rental identities.
func TestACapacityConnectionPublishesNoCandidateForAMachineNobodyIsOn(t *testing.T) {
	machines := &capacityBackend{
		negotiated: negotiatedCapacity(),
		listed: []domain.OfferSnapshot{
			{ID: "held", Kind: domain.OfferKindStanding, NativeRef: "i-held"},
			{ID: "catalog", Kind: domain.OfferKindProvisionable, NativeRef: "a6000"},
		},
	}
	fleet := enrolledOn("i-held", "rnt_1")
	broker := brokerServing(t, fleet, map[string]capability.Backend{"machines": machines})

	collection, err := broker.CollectOffers(t.Context(), adapter.OfferRequest{WorkspaceID: "ws_1"})

	if err != nil {
		t.Fatalf("collect offers from a capacity provider: %v", err)
	}
	if !slices.Contains(collection.Queried, "conn_machines") {
		t.Errorf("connections queried = %v, want the capacity connection among them", collection.Queried)
	}
	if len(collection.Offers) != 1 {
		t.Fatalf("offers = %#v, want only the machine an agent is enrolled on", collection.Offers)
	}
	candidate := collection.Offers[0]
	if candidate.NativeRef != "i-held" || candidate.ConnectionID != "connection:nodes" {
		t.Fatalf("candidate = %+v, want the enrolled node's own offer for the machine", candidate)
	}
	if candidate.RentalID != "rnt_1" {
		t.Errorf("Rental identity = %q, want the one the node's invitation named", candidate.RentalID)
	}
}

// TestNoAdapterListingCarriesARentalIdentityIntoPlacement holds the identity
// Mercator alone mints. Rental identity used to be minted from OfferKind, which
// answers who owns the host rather than whether Mercator holds it, so a
// marketplace listing of somebody else's idle machine earned a lease nobody had
// allocated and Runs queued behind it waited for a Rental that never existed.
func TestNoAdapterListingCarriesARentalIdentityIntoPlacement(t *testing.T) {
	broker := brokerServing(t, enrolledOn("i-held", "rnt_1"), map[string]capability.Backend{
		"marketplace": listingBackend{listed: []domain.OfferSnapshot{{
			ID:        "someone-elses-idle-box",
			Kind:      domain.OfferKindStanding,
			NativeRef: "vast-4471",
			RentalID:  "rnt_adapter_invented",
		}}},
	})

	collection, err := broker.CollectOffers(t.Context(), adapter.OfferRequest{WorkspaceID: "ws_1"})

	if err != nil {
		t.Fatalf("collect offers from a marketplace listing: %v", err)
	}
	held := map[string]string{}
	for _, offer := range collection.Offers {
		held[offer.NativeRef] = offer.RentalID
	}
	if held["vast-4471"] != "" {
		t.Errorf("a listing of a machine nobody allocated claimed Rental %q", held["vast-4471"])
	}
	if held["i-held"] != "rnt_1" {
		t.Errorf("the enrolled machine's Rental = %q, want the one its invitation named", held["i-held"])
	}
}

// TestASweepOfAWorkspaceHoldingCapacityConvergesTheWorkloadsItLeaked is the sweep.
// It converges one-shot executions Mercator lost track of, deciding each against
// its own record of the Run it was launched for, and a capacity connection is
// running none of those: it holds machines, and a machine carries no Run because a
// Rental outlives the Run placed on it.
//
// Reporting machines here read every one of them as capacity nobody could account
// for, recorded a durable decision to destroy it, and then could not carry the
// decision out, which aborted the sweep before it reached the executions that were
// genuinely billing. Asking a capacity connection for machines at all is therefore
// the wrong question, and the answer is that it is running no workloads.
func TestASweepOfAWorkspaceHoldingCapacityConvergesTheWorkloadsItLeaked(t *testing.T) {
	machines := &capacityBackend{
		negotiated: negotiatedCapacity(),
		held: []capability.OwnedCapacity{{
			NativeRef:      "i-held",
			WorkspaceID:    "ws_1",
			OwnershipToken: "own_1",
			State:          capability.CapacityStateActive,
			CreatedAt:      time.Unix(1_700_000_000, 0).UTC(),
		}},
	}
	broker := brokerServing(t, nil, map[string]capability.Backend{
		"machines": machines,
		"oneshot":  ownedAdapter{id: "oneshot"},
	})

	owned, err := broker.ListOwned(t.Context(), adapter.OwnershipQuery{WorkspaceID: "ws_1"})

	if err != nil {
		t.Fatalf("sweep a workspace holding a capacity connection: %v", err)
	}
	found := make([]string, 0, len(owned))
	for _, object := range owned {
		found = append(found, object.ExternalID)
	}
	if !slices.Equal(found, []string{"ext_oneshot"}) {
		t.Fatalf("owned = %v, want only the one-shot execution the sweep can converge", found)
	}
	if machines.enumerated {
		t.Fatal("the workload sweep asked a capacity connection for the machines it holds")
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
	broker := brokerServing(t, nil, map[string]capability.Backend{"machines": machines})

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
	broker := brokerServing(t, nil, map[string]capability.Backend{"machines": machines})

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

// enrolledFleet is the deployment's enrolled nodes as the Broker sees them. It
// embeds Nodes for the same reason the capacity double embeds its contract: a
// command no case here sends must fail loudly if something sends it.
//
// Its offer is the one Mercator holds a Rental for, minted from the invitation
// that named the Rental rather than from anything an adapter said.
type enrolledFleet struct {
	Nodes
	machine  string
	rentalID string
}

func enrolledOn(machine, rentalID string) enrolledFleet {
	return enrolledFleet{machine: machine, rentalID: rentalID}
}

func (fleet enrolledFleet) Offers(context.Context, string) ([]domain.OfferSnapshot, error) {
	return []domain.OfferSnapshot{{
		ID:           fleet.machine,
		RentalID:     fleet.rentalID,
		ConnectionID: "connection:nodes",
		AdapterType:  "nodes",
		Kind:         domain.OfferKindStanding,
		Lane:         domain.LaneReusable,
		MachineID:    fleet.machine,
		NativeRef:    fleet.machine,
	}}, nil
}

// listingBackend is a one-shot executor whose catalog states a Rental identity of
// its own, which is the thing no adapter is allowed to do.
type listingBackend struct {
	capability.EphemeralExecutor
	listed []domain.OfferSnapshot
}

func (listingBackend) EphemeralSupport() capability.EphemeralSupport {
	return capability.EphemeralSupport{IdempotentLaunch: "launch_key"}
}

func (listingBackend) Verify(context.Context) error { return nil }

func (backend listingBackend) ListOffers(context.Context, adapter.OfferRequest) ([]domain.OfferSnapshot, error) {
	return backend.listed, nil
}

func (listingBackend) ListOwned(context.Context, adapter.OwnershipQuery) ([]adapter.OwnedExternalObject, error) {
	return nil, nil
}

// brokerServing is one workspace's fleet: a connection per backend, all of one
// registered adapter type, and the deployment's enrolled nodes or nothing where
// the case is about a Mercator that holds no machines of its own.
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
