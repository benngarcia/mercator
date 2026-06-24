package broker

import (
	"context"
	"errors"
	"testing"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/credential"
	"github.com/benngarcia/mercator/internal/domain"
)

type fakeConns struct{ recs []ConnRef }

func (f fakeConns) List(context.Context, string) ([]ConnRef, error) { return f.recs, nil }

type nilResolver struct{}

func (nilResolver) Resolve(context.Context, string, credential.Credential) (string, error) {
	return "secret", nil
}

// recording adapter that reports which connection launched or observed.
type recAdapter struct {
	adapter.Adapter
	id       string
	launched *string
	observed *string
}

func (a recAdapter) ListOffers(context.Context, adapter.OfferRequest) ([]domain.OfferSnapshot, error) {
	return []domain.OfferSnapshot{{ID: "offer_" + a.id, ConnectionID: a.id, AdapterType: "stub"}}, nil
}
func (a recAdapter) Launch(_ context.Context, req adapter.LaunchRequest) (adapter.LaunchReceipt, error) {
	*a.launched = a.id
	return adapter.LaunchReceipt{LaunchKey: req.LaunchKey}, nil
}
func (a recAdapter) Observe(_ context.Context, _ adapter.ObserveRequest) (adapter.ExternalObservation, error) {
	if a.observed != nil {
		*a.observed = a.id
	}
	return adapter.ExternalObservation{}, nil
}
func (recAdapter) Verify(context.Context) error { return nil }

func TestBrokerAggregatesOffersAcrossConnections(t *testing.T) {
	conns := fakeConns{recs: []ConnRef{
		{ID: "conn_a", AdapterType: "stub", Authorized: true},
		{ID: "conn_b", AdapterType: "stub", Authorized: true},
		{ID: "conn_unauth", AdapterType: "stub", Authorized: false},
	}}
	f := NewFactory()
	f.Register("stub", func(map[string]string, string) (adapter.Adapter, error) {
		return recAdapter{id: "x"}, nil
	})
	b := NewBroker(conns, f, nilResolver{})
	offers, err := b.ListOffers(context.Background(), adapter.OfferRequest{WorkspaceID: "ws_1"})
	if err != nil {
		t.Fatalf("list offers: %v", err)
	}
	if len(offers) != 2 {
		t.Fatalf("expected 2 offers (authorized only), got %d", len(offers))
	}
}

func TestBrokerRoutesLaunchByConnection(t *testing.T) {
	var launchedBy string
	f := NewFactory()
	f.Register("stub", func(cfg map[string]string, _ string) (adapter.Adapter, error) {
		return recAdapter{id: cfg["id"], launched: &launchedBy}, nil
	})
	conns := fakeConns{recs: []ConnRef{
		{ID: "conn_a", AdapterType: "stub", Authorized: true, Config: map[string]string{"id": "conn_a"}},
		{ID: "conn_b", AdapterType: "stub", Authorized: true, Config: map[string]string{"id": "conn_b"}},
	}}
	b := NewBroker(conns, f, nilResolver{})
	_, err := b.Launch(context.Background(), adapter.LaunchRequest{
		LaunchKey:                 "lk1",
		SelectedOfferConnectionID: "conn_b",
		SelectedOfferAdapterType:  "stub",
	})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if launchedBy != "conn_b" {
		t.Fatalf("expected launch routed to conn_b, got %q", launchedBy)
	}
}

func TestBrokerLaunchUnknownConnectionErrors(t *testing.T) {
	b := NewBroker(fakeConns{}, NewFactory(), nilResolver{})
	_, err := b.Launch(context.Background(), adapter.LaunchRequest{SelectedOfferConnectionID: "nope"})
	if err == nil || !errors.Is(err, ErrConnectionNotFound) {
		t.Fatalf("expected ErrConnectionNotFound, got %v", err)
	}
}

// ownedAdapter is a stub adapter whose ListOwned returns one object tagged with its id.
type ownedAdapter struct {
	adapter.Adapter
	id string
}

func (a ownedAdapter) ListOwned(_ context.Context, _ adapter.OwnershipQuery) ([]adapter.OwnedExternalObject, error) {
	return []adapter.OwnedExternalObject{{ExternalID: "ext_" + a.id}}, nil
}

func TestBrokerListOwnedFansOut(t *testing.T) {
	f := NewFactory()
	f.Register("stub", func(cfg map[string]string, _ string) (adapter.Adapter, error) {
		return ownedAdapter{id: cfg["id"]}, nil
	})
	conns := fakeConns{recs: []ConnRef{
		{ID: "conn_a", AdapterType: "stub", Authorized: true, Config: map[string]string{"id": "a"}},
		{ID: "conn_b", AdapterType: "stub", Authorized: true, Config: map[string]string{"id": "b"}},
	}}
	b := NewBroker(conns, f, nilResolver{})
	owned, err := b.ListOwned(context.Background(), adapter.OwnershipQuery{WorkspaceID: "ws_1"})
	if err != nil {
		t.Fatalf("list owned: %v", err)
	}
	if len(owned) != 2 {
		t.Fatalf("expected owned objects from both connections, got %d", len(owned))
	}
}

func TestBrokerRoutesObserveByConnection(t *testing.T) {
	var observedBy string
	f := NewFactory()
	f.Register("stub", func(cfg map[string]string, _ string) (adapter.Adapter, error) {
		return recAdapter{id: cfg["id"], observed: &observedBy}, nil
	})
	conns := fakeConns{recs: []ConnRef{
		{ID: "conn_a", AdapterType: "stub", Authorized: true, Config: map[string]string{"id": "conn_a"}},
		{ID: "conn_b", AdapterType: "stub", Authorized: true, Config: map[string]string{"id": "conn_b"}},
	}}
	b := NewBroker(conns, f, nilResolver{})
	_, err := b.Observe(context.Background(), adapter.ObserveRequest{
		WorkspaceID:  "ws_1",
		ConnectionID: "conn_b",
	})
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if observedBy != "conn_b" {
		t.Fatalf("expected observe routed to conn_b, got %q", observedBy)
	}
}
