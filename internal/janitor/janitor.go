package janitor

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
)

type Adapter interface {
	ListOwned(ctx context.Context, req adapter.OwnershipQuery) ([]adapter.OwnedExternalObject, error)
	Release(ctx context.Context, req adapter.ReleaseRequest) (adapter.ReleaseReceipt, error)
	Terminate(ctx context.Context, req adapter.TerminateRequest) (adapter.TerminateReceipt, error)
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
		if object.WorkspaceID == "" {
			// An orphan listed without workspace labels still belongs to the
			// swept workspace; requests need it to route through the broker.
			object.WorkspaceID = workspaceID
		}
		releasable, disposition, err := j.releasable(ctx, object)
		if err != nil {
			return result, err
		}
		if !releasable {
			continue
		}
		// Reclaim via the RECORDED disposition: a run that provisioned a host we
		// own (terminate) must have that host destroyed, while a borrowed standing
		// slot (release) only loses our job. An orphan with no recorded intent
		// defaults to release, the safe option that never destroys a host.
		if disposition == domain.DispositionTerminate {
			req := adapter.TerminateRequest{
				ProviderOperationContext: cleanupContext(object),
				OperationKey:             "janitor:terminate:" + object.LaunchKey,
				LaunchKey:                object.LaunchKey,
				OwnershipToken:           object.OwnershipToken,
				LaunchRequestHash:        object.RequestHash,
			}
			hash, err := domain.CanonicalHash(req)
			if err != nil {
				return result, err
			}
			req.RequestHash = hash
			if _, err := j.adapter.Terminate(ctx, req); err != nil {
				return result, err
			}
		} else {
			req := adapter.ReleaseRequest{
				ProviderOperationContext: cleanupContext(object),
				OperationKey:             "janitor:release:" + object.LaunchKey,
				LaunchKey:                object.LaunchKey,
				OwnershipToken:           object.OwnershipToken,
				LaunchRequestHash:        object.RequestHash,
			}
			hash, err := domain.CanonicalHash(req)
			if err != nil {
				return result, err
			}
			req.RequestHash = hash
			if _, err := j.adapter.Release(ctx, req); err != nil {
				return result, err
			}
		}
		result.Released++
	}
	return result, nil
}

func cleanupContext(object adapter.OwnedExternalObject) adapter.ProviderOperationContext {
	return adapter.ProviderOperationContext{
		WorkspaceID:  object.WorkspaceID,
		RunID:        object.RunID,
		AttemptID:    object.AttemptID,
		ConnectionID: object.ConnectionID,
		AdapterType:  object.AdapterType,
	}
}

// releasable reports whether an owned object should be reclaimed and, if so, the
// RECORDED cleanup disposition to reclaim it with (defaulting to release when no
// launch intent was recorded — an orphan or a pre-change event log).
func (j *Janitor) releasable(ctx context.Context, object adapter.OwnedExternalObject) (bool, domain.Disposition, error) {
	if object.RunID == "" {
		return true, domain.DispositionRelease, nil
	}
	history, err := eventlog.ReadFullStream(ctx, j.log, eventlog.StreamKey{WorkspaceID: object.WorkspaceID, Type: "run", ID: object.RunID})
	if err != nil {
		return false, "", err
	}
	if len(history.Events) == 0 {
		return true, domain.DispositionRelease, nil
	}
	disposition := domain.DispositionRelease
	reclaim := false
	for _, event := range history.Events {
		switch event.Type {
		case "compute.run.launch_intent_recorded.v1":
			payload := event.PrivateData
			if len(payload) == 0 {
				payload = event.Data
			}
			var intent struct {
				Disposition domain.Disposition `json:"disposition"`
			}
			if err := json.Unmarshal(payload, &intent); err == nil && intent.Disposition != "" {
				disposition = intent.Disposition
			}
		case "compute.run.cleanup_requested.v1", "compute.run.cleanup_confirmed.v1":
			reclaim = true
		}
	}
	return reclaim, disposition, nil
}
