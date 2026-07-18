package broker

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/connection"
	"github.com/benngarcia/mercator/internal/credential"
	"github.com/benngarcia/mercator/internal/domain"
)

var ErrConnectionNotFound = errors.New("broker: connection not found")

// Connections lists the registered connections for a workspace.
// *connection.Service satisfies it directly.
type Connections interface {
	List(ctx context.Context, workspaceID string) ([]connection.Record, error)
}

type Resolver interface {
	Resolve(ctx context.Context, workspaceID string, c credential.Credential) (string, error)
}

type Broker struct {
	conns    Connections
	factory  *Factory
	resolver Resolver
}

func NewBroker(conns Connections, factory *Factory, resolver Resolver) *Broker {
	return &Broker{conns: conns, factory: factory, resolver: resolver}
}

// Manifests exposes the registered adapters' onboarding manifests for the
// HTTP API's GET /v1/adapters.
func (b *Broker) Manifests() []adapter.Manifest { return b.factory.Manifests() }

// build constructs the adapter for one connection (no caching yet — YAGNI).
func (b *Broker) build(ctx context.Context, workspaceID string, c connection.Record) (adapter.Provider, error) {
	secret := ""
	if c.Credential.Source != "" {
		s, err := b.resolver.Resolve(ctx, workspaceID, c.Credential)
		if err != nil {
			return nil, fmt.Errorf("broker: resolve credential for %s: %w", c.ID, err)
		}
		secret = s
	}
	return b.factory.Build(c.AdapterType, c.Config, secret)
}

// connByID retrieves a connection by ID and builds its adapter.
// Unlike ListOffers and ListOwned, this intentionally does NOT filter on Authorized.
// Post-launch operations (Observe/Cancel/Release/Terminate) must still reach a run that was
// launched on a connection which has since been de-authorized, so cleanup is never stranded.
func (b *Broker) connByID(ctx context.Context, workspaceID, connectionID string) (connection.Record, adapter.Provider, error) {
	recs, err := b.conns.List(ctx, workspaceID)
	if err != nil {
		return connection.Record{}, nil, err
	}
	for _, c := range recs {
		if c.ID == connectionID {
			ad, err := b.build(ctx, workspaceID, c)
			return c, ad, err
		}
	}
	return connection.Record{}, nil, fmt.Errorf("%w: %s", ErrConnectionNotFound, connectionID)
}

func (b *Broker) ListOffers(ctx context.Context, req adapter.OfferRequest) ([]domain.OfferSnapshot, error) {
	recs, err := b.conns.List(ctx, req.WorkspaceID)
	if err != nil {
		return nil, err
	}
	results, failures := fanOut(ctx, b, req.WorkspaceID, recs, func(ctx context.Context, provider adapter.Provider) ([]domain.OfferSnapshot, error) {
		return provider.ListOffers(ctx, req)
	})
	var all []domain.OfferSnapshot
	for _, result := range results {
		for _, offer := range result.items {
			offer.ConnectionID = result.connection.ID
			offer.AdapterType = result.connection.AdapterType
			all = append(all, offer)
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].ConnectionID != all[j].ConnectionID {
			return all[i].ConnectionID < all[j].ConnectionID
		}
		return all[i].ID < all[j].ID
	})
	return all, failures.OrNil()
}

func (b *Broker) Launch(ctx context.Context, req adapter.LaunchRequest) (adapter.LaunchReceipt, error) {
	_, ad, err := b.connByID(ctx, req.WorkspaceID, req.SelectedOfferConnectionID)
	if err != nil {
		return adapter.LaunchReceipt{}, err
	}
	return ad.Launch(ctx, req)
}

func (b *Broker) Observe(ctx context.Context, req adapter.ObserveRequest) (adapter.ExternalObservation, error) {
	_, ad, err := b.connByID(ctx, req.WorkspaceID, req.ConnectionID)
	if err != nil {
		return adapter.ExternalObservation{}, err
	}
	return ad.Observe(ctx, req)
}

func (b *Broker) Cancel(ctx context.Context, req adapter.CancelRequest) (adapter.CancelReceipt, error) {
	_, ad, err := b.connByID(ctx, req.WorkspaceID, req.ConnectionID)
	if err != nil {
		return adapter.CancelReceipt{}, err
	}
	return ad.Cancel(ctx, req)
}

func (b *Broker) Release(ctx context.Context, req adapter.ReleaseRequest) (adapter.ReleaseReceipt, error) {
	_, ad, err := b.connByID(ctx, req.WorkspaceID, req.ConnectionID)
	if err != nil {
		return adapter.ReleaseReceipt{}, err
	}
	return ad.Release(ctx, req)
}

func (b *Broker) Terminate(ctx context.Context, req adapter.TerminateRequest) (adapter.TerminateReceipt, error) {
	_, ad, err := b.connByID(ctx, req.WorkspaceID, req.ConnectionID)
	if err != nil {
		return adapter.TerminateReceipt{}, err
	}
	return ad.Terminate(ctx, req)
}

func (b *Broker) ListOwned(ctx context.Context, req adapter.OwnershipQuery) ([]adapter.OwnedExternalObject, error) {
	recs, err := b.conns.List(ctx, req.WorkspaceID)
	if err != nil {
		return nil, err
	}
	results, failures := fanOut(ctx, b, req.WorkspaceID, recs, func(ctx context.Context, provider adapter.Provider) ([]adapter.OwnedExternalObject, error) {
		return provider.ListOwned(ctx, req)
	})
	var all []adapter.OwnedExternalObject
	for _, result := range results {
		for _, object := range result.items {
			object.ConnectionID = result.connection.ID
			all = append(all, object)
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].ConnectionID != all[j].ConnectionID {
			return all[i].ConnectionID < all[j].ConnectionID
		}
		return all[i].ExternalID < all[j].ExternalID
	})
	return all, failures.OrNil()
}

// VerifyConnection builds the adapter for one connection (regardless of its
// current Authorized state — authorize runs before the flag is set) and calls
// its cheap Verify check. Used by the connection authorize flow.
func (b *Broker) VerifyConnection(ctx context.Context, workspaceID, connectionID string) error {
	_, ad, err := b.connByID(ctx, workspaceID, connectionID)
	if err != nil {
		return err
	}
	return ad.Verify(ctx)
}
