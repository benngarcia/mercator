package janitor

import (
	"context"
	"fmt"

	"github.com/bengarcia/mercator/internal/adapter"
	"github.com/bengarcia/mercator/internal/domain"
	"github.com/bengarcia/mercator/internal/eventlog"
)

type Adapter interface {
	ListOwned(ctx context.Context, req adapter.OwnershipQuery) ([]adapter.OwnedExternalObject, error)
	Release(ctx context.Context, req adapter.ReleaseRequest) (adapter.ReleaseReceipt, error)
}

type Janitor struct {
	adapter Adapter
	log     eventlog.EventLog
}

type Result struct {
	Found    int `json:"found"`
	Released int `json:"released"`
}

type Option func(*Janitor)

func WithEventLog(log eventlog.EventLog) Option {
	return func(j *Janitor) {
		j.log = log
	}
}

func New(adapter Adapter, options ...Option) *Janitor {
	j := &Janitor{adapter: adapter}
	for _, option := range options {
		option(j)
	}
	return j
}

func (j *Janitor) Sweep(ctx context.Context, workspaceID string) (Result, error) {
	if j.adapter == nil {
		return Result{}, fmt.Errorf("janitor: adapter is required")
	}
	if j.log == nil {
		return Result{}, fmt.Errorf("janitor: event log is required")
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
		releasable, err := j.releasable(ctx, object)
		if err != nil {
			return result, err
		}
		if !releasable {
			continue
		}
		req := adapter.ReleaseRequest{
			OperationKey:      "janitor:release:" + object.LaunchKey,
			LaunchKey:         object.LaunchKey,
			OwnershipToken:    object.OwnershipToken,
			LaunchRequestHash: object.RequestHash,
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

func (j *Janitor) releasable(ctx context.Context, object adapter.OwnedExternalObject) (bool, error) {
	if object.RunID == "" {
		return true, nil
	}
	events, err := j.log.ReadStream(ctx, eventlog.StreamKey{WorkspaceID: object.WorkspaceID, Type: "run", ID: object.RunID}, 0, 1000)
	if err != nil {
		return false, err
	}
	if len(events) == 0 {
		return true, nil
	}
	for _, event := range events {
		switch event.Type {
		case "compute.run.cleanup_requested.v1", "compute.run.cleanup_confirmed.v1":
			return true, nil
		}
	}
	return false, nil
}
