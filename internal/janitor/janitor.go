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
		// Reclaim a recorded run through its recorded ownership action. Objects
		// without run history are unattributed orphans and release only their slot.
		switch disposition {
		case domain.DispositionTerminate:
			req := adapter.TerminateRequest{
				WorkspaceID:       object.WorkspaceID,
				ConnectionID:      object.ConnectionID,
				OperationKey:      "janitor:terminate:" + object.LaunchKey,
				LaunchKey:         object.LaunchKey,
				OwnershipToken:    object.OwnershipToken,
				LaunchRequestHash: object.RequestHash,
			}
			hash, err := domain.CanonicalHash(req)
			if err != nil {
				return result, err
			}
			req.RequestHash = hash
			if _, err := j.adapter.Terminate(ctx, req); err != nil {
				return result, err
			}
		case domain.DispositionRelease:
			req := adapter.ReleaseRequest{
				WorkspaceID:       object.WorkspaceID,
				ConnectionID:      object.ConnectionID,
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
		default:
			return result, fmt.Errorf("janitor: cleanup requires a valid recorded disposition, got %q", disposition)
		}
		result.Released++
	}
	return result, nil
}

// releasable reports whether an owned object should be reclaimed and returns
// the run's recorded cleanup disposition. An object with no run history is an
// unattributed orphan, so the janitor may only release its slot.
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
	var disposition domain.Disposition
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
			if err := json.Unmarshal(payload, &intent); err != nil {
				return false, "", fmt.Errorf("janitor: decode recorded launch intent: %w", err)
			}
			disposition = intent.Disposition
		case "compute.run.cleanup_requested.v1", "compute.run.cleanup_confirmed.v1":
			reclaim = true
		}
	}
	if reclaim && !disposition.Valid() {
		return false, "", fmt.Errorf("janitor: cleanup requires a valid recorded disposition, got %q", disposition)
	}
	return reclaim, disposition, nil
}
