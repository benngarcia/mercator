// Package runprojection defines the durable, indexed Run read-model boundary.
package runprojection

import (
	"context"
	"fmt"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
)

const (
	DefaultPageSize = 50
	MaxPageSize     = 100
)

type PageRequest struct {
	After string
	Limit int
}

func (request PageRequest) Validated() (PageRequest, error) {
	if request.Limit == 0 {
		request.Limit = DefaultPageSize
	}
	if request.Limit < 1 || request.Limit > MaxPageSize {
		return PageRequest{}, fmt.Errorf("Run page limit must be between 1 and %d", MaxPageSize)
	}
	return request, nil
}

type Page struct {
	Records    []domain.RunRecord
	NextCursor string
}

// Store atomically commits Run facts with their public read model and serves
// bounded indexed reads. The event stream remains the source of truth.
type Store interface {
	Append(ctx context.Context, request eventlog.AppendRequest, next domain.RunRecord) (eventlog.AppendResult, error)
	AppendIfWorkspaceActive(ctx context.Context, request eventlog.AppendRequest, next domain.RunRecord) (eventlog.AppendResult, error)
	List(ctx context.Context, workspaceID string, page PageRequest) (Page, error)
	ListOpenIDs(ctx context.Context, workspaceID string) ([]string, error)
	Replace(ctx context.Context, workspaceID string, records []domain.RunRecord) error
}
