package broker

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/connection"
	"github.com/benngarcia/mercator/internal/credential"
	"github.com/benngarcia/mercator/internal/domain"
)

type fakeConns struct{ recs []connection.Record }

func (f fakeConns) List(context.Context, string) ([]connection.Record, error) { return f.recs, nil }

type nilResolver struct{}

func (nilResolver) Resolve(context.Context, string, credential.Credential) (string, error) {
	return "secret", nil
}

type fanoutAdapter struct {
	adapter.Provider
	listOffers func(context.Context) ([]domain.OfferSnapshot, error)
	listOwned  func(context.Context) ([]adapter.OwnedExternalObject, error)
}

func (a fanoutAdapter) ListOffers(ctx context.Context, _ adapter.OfferRequest) ([]domain.OfferSnapshot, error) {
	return a.listOffers(ctx)
}

func (a fanoutAdapter) ListOwned(ctx context.Context, _ adapter.OwnershipQuery) ([]adapter.OwnedExternalObject, error) {
	return a.listOwned(ctx)
}

func fanoutBroker(t *testing.T, adapters map[string]adapter.Provider) *Broker {
	t.Helper()
	factory := NewFactory()
	factory.Register(adapter.Manifest{Type: "stub"}, func(config map[string]string, _ string) (adapter.Provider, error) {
		return adapters[config["id"]], nil
	})
	records := make([]connection.Record, 0, len(adapters))
	for id := range adapters {
		records = append(records, connection.Record{ID: "conn_" + id, AdapterType: "stub", Authorized: true, Config: map[string]string{"id": id}})
	}
	return NewBroker(fakeConns{recs: records}, factory, nilResolver{})
}

func TestBrokerListOffersReturnsPartialResultsAndConnectionErrors(t *testing.T) {
	providerErr := errors.New("provider unavailable")
	broker := fanoutBroker(t, map[string]adapter.Provider{
		"good": fanoutAdapter{listOffers: func(context.Context) ([]domain.OfferSnapshot, error) {
			return []domain.OfferSnapshot{{ID: "offer_good"}}, nil
		}},
		"bad": fanoutAdapter{listOffers: func(context.Context) ([]domain.OfferSnapshot, error) {
			return nil, providerErr
		}},
	})

	offers, err := broker.ListOffers(t.Context(), adapter.OfferRequest{WorkspaceID: "ws_1"})

	if len(offers) != 1 || offers[0].ID != "offer_good" {
		t.Fatalf("offers = %#v, want the successful connection's offer", offers)
	}
	var connectionErrors ConnectionErrors
	if !errors.As(err, &connectionErrors) {
		t.Fatalf("error = %v, want ConnectionErrors", err)
	}
	if len(connectionErrors) != 1 || connectionErrors[0].ConnectionID != "conn_bad" || !errors.Is(connectionErrors[0], providerErr) {
		t.Fatalf("connection errors = %#v, want conn_bad provider error", connectionErrors)
	}
}

func TestBrokerListOffersQueriesConnectionsConcurrently(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	query := func(id string) func(context.Context) ([]domain.OfferSnapshot, error) {
		return func(context.Context) ([]domain.OfferSnapshot, error) {
			started <- id
			<-release
			return []domain.OfferSnapshot{{ID: "offer_" + id}}, nil
		}
	}
	broker := fanoutBroker(t, map[string]adapter.Provider{
		"a": fanoutAdapter{listOffers: query("a")},
		"b": fanoutAdapter{listOffers: query("b")},
	})
	done := make(chan error, 1)
	go func() {
		_, err := broker.ListOffers(t.Context(), adapter.OfferRequest{WorkspaceID: "ws_1"})
		done <- err
	}()

	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("provider queries were serialized")
		}
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("list offers: %v", err)
	}
}

func TestBrokerListOffersSortsConcurrentResultsDeterministically(t *testing.T) {
	broker := fanoutBroker(t, map[string]adapter.Provider{
		"b": fanoutAdapter{listOffers: func(context.Context) ([]domain.OfferSnapshot, error) {
			return []domain.OfferSnapshot{{ID: "offer_z"}, {ID: "offer_a"}}, nil
		}},
		"a": fanoutAdapter{listOffers: func(context.Context) ([]domain.OfferSnapshot, error) {
			return []domain.OfferSnapshot{{ID: "offer_m"}}, nil
		}},
	})

	offers, err := broker.ListOffers(t.Context(), adapter.OfferRequest{WorkspaceID: "ws_1"})

	if err != nil {
		t.Fatalf("list offers: %v", err)
	}
	got := make([]string, len(offers))
	for i, offer := range offers {
		got[i] = offer.ConnectionID + "/" + offer.ID
	}
	want := []string{"conn_a/offer_m", "conn_b/offer_a", "conn_b/offer_z"}
	if !slices.Equal(got, want) {
		t.Fatalf("offers = %v, want %v", got, want)
	}
}

func TestBrokerListOffersPropagatesCancellation(t *testing.T) {
	broker := fanoutBroker(t, map[string]adapter.Provider{
		"slow": fanoutAdapter{listOffers: func(ctx context.Context) ([]domain.OfferSnapshot, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}},
	})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := broker.ListOffers(ctx, adapter.OfferRequest{WorkspaceID: "ws_1"})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestBrokerListOwnedReturnsPartialResultsAndConnectionErrors(t *testing.T) {
	providerErr := errors.New("ownership lookup failed")
	broker := fanoutBroker(t, map[string]adapter.Provider{
		"good": fanoutAdapter{listOwned: func(context.Context) ([]adapter.OwnedExternalObject, error) {
			return []adapter.OwnedExternalObject{{ExternalID: "external_good"}}, nil
		}},
		"bad": fanoutAdapter{listOwned: func(context.Context) ([]adapter.OwnedExternalObject, error) {
			return nil, providerErr
		}},
	})

	objects, err := broker.ListOwned(t.Context(), adapter.OwnershipQuery{WorkspaceID: "ws_1"})

	if len(objects) != 1 || objects[0].ExternalID != "external_good" || objects[0].ConnectionID != "conn_good" {
		t.Fatalf("objects = %#v, want the successful connection's object", objects)
	}
	var connectionErrors ConnectionErrors
	if !errors.As(err, &connectionErrors) || len(connectionErrors) != 1 || connectionErrors[0].ConnectionID != "conn_bad" {
		t.Fatalf("error = %#v, want conn_bad ConnectionError", err)
	}
}

// recording adapter that reports which connection launched or observed.
type recAdapter struct {
	adapter.Provider
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
	conns := fakeConns{recs: []connection.Record{
		{ID: "conn_a", AdapterType: "stub", Authorized: true},
		{ID: "conn_b", AdapterType: "stub", Authorized: true},
		{ID: "conn_unauth", AdapterType: "stub", Authorized: false},
	}}
	f := NewFactory()
	f.Register(adapter.Manifest{Type: "stub"}, func(map[string]string, string) (adapter.Provider, error) {
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
	f.Register(adapter.Manifest{Type: "stub"}, func(cfg map[string]string, _ string) (adapter.Provider, error) {
		return recAdapter{id: cfg["id"], launched: &launchedBy}, nil
	})
	conns := fakeConns{recs: []connection.Record{
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
	adapter.Provider
	id string
}

func (a ownedAdapter) ListOwned(_ context.Context, _ adapter.OwnershipQuery) ([]adapter.OwnedExternalObject, error) {
	return []adapter.OwnedExternalObject{{ExternalID: "ext_" + a.id}}, nil
}

func TestBrokerListOwnedFansOut(t *testing.T) {
	f := NewFactory()
	f.Register(adapter.Manifest{Type: "stub"}, func(cfg map[string]string, _ string) (adapter.Provider, error) {
		return ownedAdapter{id: cfg["id"]}, nil
	})
	conns := fakeConns{recs: []connection.Record{
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
	f.Register(adapter.Manifest{Type: "stub"}, func(cfg map[string]string, _ string) (adapter.Provider, error) {
		return recAdapter{id: cfg["id"], observed: &observedBy}, nil
	})
	conns := fakeConns{recs: []connection.Record{
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

// verifyAdapter is a stub adapter that records which connection had its Verify called.
type verifyAdapter struct {
	adapter.Provider
	id       string
	verified *string
}

func (a verifyAdapter) Verify(context.Context) error {
	*a.verified = a.id
	return nil
}

func TestBrokerVerifyConnectionBuildsAndVerifies(t *testing.T) {
	var verified string
	f := NewFactory()
	f.Register(adapter.Manifest{Type: "stub"}, func(cfg map[string]string, _ string) (adapter.Provider, error) {
		return verifyAdapter{id: cfg["id"], verified: &verified}, nil
	})
	conns := fakeConns{recs: []connection.Record{
		{ID: "conn_a", AdapterType: "stub", Authorized: false, Config: map[string]string{"id": "conn_a"}},
	}}
	b := NewBroker(conns, f, nilResolver{})
	if err := b.VerifyConnection(context.Background(), "ws_1", "conn_a"); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if verified != "conn_a" {
		t.Fatalf("expected Verify on conn_a, got %q", verified)
	}
	if err := b.VerifyConnection(context.Background(), "ws_1", "nope"); !errors.Is(err, ErrConnectionNotFound) {
		t.Fatalf("expected ErrConnectionNotFound, got %v", err)
	}
}
