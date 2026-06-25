// Package connbroker adapts the connection.Service registry to the
// broker.Connections interface the Broker consumes.
package connbroker

import (
	"context"

	"github.com/benngarcia/mercator/internal/broker"
	"github.com/benngarcia/mercator/internal/connection"
)

type service struct{ svc *connection.Service }

func New(svc *connection.Service) broker.Connections { return service{svc: svc} }

func (s service) List(ctx context.Context, workspaceID string) ([]broker.ConnRef, error) {
	records, err := s.svc.List(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	refs := make([]broker.ConnRef, 0, len(records))
	for _, r := range records {
		refs = append(refs, broker.ConnRef{
			ID: r.ID, AdapterType: r.AdapterType, Config: r.Config,
			Credential: r.Credential, Authorized: r.Authorized,
		})
	}
	return refs, nil
}
