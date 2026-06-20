package janitor

import (
	"context"
	"fmt"

	"github.com/bengarcia/mercator/internal/adapter"
	"github.com/bengarcia/mercator/internal/domain"
)

type Adapter interface {
	ListOwned(ctx context.Context, req adapter.OwnershipQuery) ([]adapter.OwnedExternalObject, error)
	Release(ctx context.Context, req adapter.ReleaseRequest) (adapter.ReleaseReceipt, error)
}

type Janitor struct {
	adapter Adapter
}

type Result struct {
	Found    int `json:"found"`
	Released int `json:"released"`
}

func New(adapter Adapter) *Janitor {
	return &Janitor{adapter: adapter}
}

func (j *Janitor) Sweep(ctx context.Context, workspaceID string) (Result, error) {
	if j.adapter == nil {
		return Result{}, fmt.Errorf("janitor: adapter is required")
	}
	if workspaceID == "" {
		return Result{}, fmt.Errorf("janitor: workspace_id is required")
	}
	owned, err := j.adapter.ListOwned(ctx, adapter.OwnershipQuery{WorkspaceID: workspaceID})
	if err != nil {
		return Result{}, err
	}
	result := Result{Found: len(owned)}
	for _, object := range owned {
		req := adapter.ReleaseRequest{
			OperationKey: "janitor:release:" + object.LaunchKey,
			LaunchKey:    object.LaunchKey,
		}
		hash, err := domain.CanonicalHash(req)
		if err != nil {
			return result, err
		}
		req.RequestHash = hash
		if _, err := j.adapter.Release(ctx, req); err != nil {
			return result, err
		}
		result.Released++
	}
	return result, nil
}
