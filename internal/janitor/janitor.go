package janitor

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/bengarcia/mercator/internal/adapter"
	"github.com/bengarcia/mercator/internal/domain"
	"github.com/bengarcia/mercator/internal/eventlog"
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
		} else {
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
		}
		result.Released++
	}
	return result, nil
}

// releasable reports whether an owned object should be reclaimed and, if so, the
// RECORDED cleanup disposition to reclaim it with (defaulting to release when no
// launch intent was recorded — an orphan or a pre-change event log).
func (j *Janitor) releasable(ctx context.Context, object adapter.OwnedExternalObject) (bool, domain.Disposition, error) {
	if object.RunID == "" {
		return true, domain.DispositionRelease, nil
	}
	events, err := j.log.ReadStream(ctx, eventlog.StreamKey{WorkspaceID: object.WorkspaceID, Type: "run", ID: object.RunID}, 0, 1000)
	if err != nil {
		return false, "", err
	}
	if len(events) == 0 {
		return true, domain.DispositionRelease, nil
	}
	disposition := domain.DispositionRelease
	reclaim := false
	for _, event := range events {
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
