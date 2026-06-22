package projection

import (
	"context"
	"fmt"

	"github.com/benngarcia/mercator/internal/eventlog"
)

type EventLog interface {
	ReadAll(ctx context.Context, after eventlog.GlobalPosition, limit int, filter eventlog.EventFilter) ([]eventlog.StoredEvent, error)
	Offset(ctx context.Context, subscriptionID string) (eventlog.GlobalPosition, bool, error)
	Ack(ctx context.Context, subscriptionID string, position eventlog.GlobalPosition) error
}

type Handler func(context.Context, eventlog.StoredEvent) error

type Runner struct {
	Log          EventLog
	ProjectionID string
	Filter       eventlog.EventFilter
	BatchSize    int
	Disposable   bool
	Handler      Handler
}

type Result struct {
	Processed    int                     `json:"processed"`
	LastPosition eventlog.GlobalPosition `json:"last_position"`
}

func (r Runner) RunOnce(ctx context.Context) (Result, error) {
	if r.Log == nil {
		return Result{}, fmt.Errorf("projection: log is required")
	}
	if r.ProjectionID == "" {
		return Result{}, fmt.Errorf("projection: projection_id is required")
	}
	if r.Handler == nil {
		return Result{}, fmt.Errorf("projection: handler is required")
	}
	limit := r.BatchSize
	if limit <= 0 {
		limit = 100
	}
	var after eventlog.GlobalPosition
	if !r.Disposable {
		stored, ok, err := r.Log.Offset(ctx, r.ProjectionID)
		if err != nil {
			return Result{}, err
		}
		if ok {
			after = stored
		}
	}
	events, err := r.Log.ReadAll(ctx, after, limit, r.Filter)
	if err != nil {
		return Result{}, err
	}
	result := Result{LastPosition: after}
	for _, event := range events {
		if err := r.Handler(ctx, event); err != nil {
			return result, err
		}
		result.Processed++
		result.LastPosition = event.GlobalPosition
	}
	if !r.Disposable && result.LastPosition > after {
		if err := r.Log.Ack(ctx, r.ProjectionID, result.LastPosition); err != nil {
			return result, err
		}
	}
	return result, nil
}
