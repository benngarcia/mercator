package broker

import (
	"context"
	"errors"
	"fmt"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/credential"
	"github.com/benngarcia/mercator/internal/domain"
)

var ErrConnectionNotFound = errors.New("broker: connection not found")

type ConnRef struct {
	ID          string
	AdapterType string
	Config      map[string]string
	Credential  credential.Credential
	Authorized  bool
}

type Connections interface {
	List(ctx context.Context, workspaceID string) ([]ConnRef, error)
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

// build constructs the adapter for one connection (no caching yet — YAGNI;
// providers' ListOffers are cached upstream by the offer service).
func (b *Broker) build(ctx context.Context, workspaceID string, c ConnRef) (adapter.Adapter, error) {
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

func (b *Broker) connByID(ctx context.Context, workspaceID, connectionID string) (ConnRef, adapter.Adapter, error) {
	recs, err := b.conns.List(ctx, workspaceID)
	if err != nil {
		return ConnRef{}, nil, err
	}
	for _, c := range recs {
		if c.ID == connectionID {
			ad, err := b.build(ctx, workspaceID, c)
			return c, ad, err
		}
	}
	return ConnRef{}, nil, fmt.Errorf("%w: %s", ErrConnectionNotFound, connectionID)
}

func (b *Broker) ListOffers(ctx context.Context, req adapter.OfferRequest) ([]domain.OfferSnapshot, error) {
	recs, err := b.conns.List(ctx, req.WorkspaceID)
	if err != nil {
		return nil, err
	}
	var all []domain.OfferSnapshot
	for _, c := range recs {
		if !c.Authorized {
			continue
		}
		ad, err := b.build(ctx, req.WorkspaceID, c)
		if err != nil {
			continue // a broken connection should not sink the whole list
		}
		offers, err := ad.ListOffers(ctx, req)
		if err != nil {
			continue
		}
		for i := range offers {
			offers[i].ConnectionID = c.ID
			offers[i].AdapterType = c.AdapterType
			all = append(all, offers[i])
		}
	}
	return all, nil
}

func (b *Broker) Launch(ctx context.Context, req adapter.LaunchRequest) (adapter.LaunchReceipt, error) {
	_, ad, err := b.connByID(ctx, req.WorkspaceID, req.SelectedOfferConnectionID)
	if err != nil {
		return adapter.LaunchReceipt{}, err
	}
	return ad.Launch(ctx, req)
}

func (b *Broker) Verify(ctx context.Context) error { return nil } // per-connection verify is in Plan 1B
