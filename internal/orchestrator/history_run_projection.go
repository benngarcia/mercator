package orchestrator

import (
	"context"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
	"github.com/benngarcia/mercator/internal/runprojection"
)

// historyRunProjection keeps focused and in-memory compositions lightweight.
// Production injects the durable SQLite Run projection.
type historyRunProjection struct {
	log eventlog.EventLog
}

func (projection historyRunProjection) Append(
	ctx context.Context,
	request eventlog.AppendRequest,
	_ domain.RunRecord,
) (eventlog.AppendResult, error) {
	return projection.log.Append(ctx, request)
}

func (projection historyRunProjection) List(
	ctx context.Context,

	request runprojection.PageRequest,
) (runprojection.Page, error) {
	request, err := request.Validated()
	if err != nil {
		return runprojection.Page{}, err
	}
	records, err := (&Orchestrator{log: projection.log}).runRecordsFromHistory(ctx)
	if err != nil {
		return runprojection.Page{}, err
	}
	start := 0
	for start < len(records) && records[start].ID <= request.After {
		start++
	}
	end := min(start+request.Limit, len(records))
	page := runprojection.Page{Records: records[start:end]}
	if end < len(records) {
		page.NextCursor = page.Records[len(page.Records)-1].ID
	}
	return page, nil
}

func (projection historyRunProjection) ListOpenIDs(ctx context.Context) ([]string, error) {
	filter := eventlog.EventFilter{

		StreamTypes: []string{"run"},
		EventTypes:  []string{EventRunRequested, EventRunClosed},
	}
	head, err := projection.log.LatestPosition(ctx, filter)
	if err != nil {
		return nil, err
	}
	var requested []string
	closed := map[string]bool{}
	for event, err := range eventlog.ScanAll(ctx, projection.log, head, filter) {
		if err != nil {
			return nil, err
		}
		switch event.Type {
		case EventRunRequested:
			requested = append(requested, event.StreamID)
		case EventRunClosed:
			closed[event.StreamID] = true
		}
	}
	var runIDs []string
	for _, runID := range requested {
		if !closed[runID] {
			runIDs = append(runIDs, runID)
		}
	}
	return runIDs, nil
}

func (historyRunProjection) Replace(context.Context, []domain.RunRecord) error {
	return nil
}
