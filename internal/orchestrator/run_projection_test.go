package orchestrator

import (
	"context"
	"encoding/json"
	"sort"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/adapter/fake"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
	"github.com/benngarcia/mercator/internal/runprojection"
	"github.com/benngarcia/mercator/internal/scheduler"
)

func TestRebuildRunProjectionReproducesClosedRunRecords(t *testing.T) {
	ctx := context.Background()
	log := openOrchestratorLog(t)
	projection := newMemoryRunProjection(log)
	orch := New(
		log,
		scheduler.New(),
		fake.New(
			fake.WithOffers([]domain.OfferSnapshot{orchOffer("off_1", time.Now().UTC())}),
			fake.WithLaunchOutcome(adapter.ExternalPhaseSucceeded),
		),
		WithRunProjection(projection),
	)
	createRun(t, ctx, orch)
	for attempt := 0; attempt < 10; attempt++ {
		if err := orch.AdvanceRun(ctx, "ws_1", "run_1"); err != nil {
			t.Fatalf("advance Run: %v", err)
		}
		record, err := orch.GetRun(ctx, "ws_1", "run_1")
		if err != nil {
			t.Fatalf("get Run: %v", err)
		}
		if record.Closed {
			break
		}
	}
	historyReader := New(log, scheduler.New(), fake.New())
	before, err := historyReader.ListRuns(ctx, "ws_1", runprojection.PageRequest{})
	if err != nil {
		t.Fatalf("list before rebuild: %v", err)
	}
	beforeJSON, err := json.Marshal(before.Records)
	if err != nil {
		t.Fatalf("encode records before rebuild: %v", err)
	}
	if len(before.Records) != 1 || !before.Records[0].Closed {
		t.Fatalf("records before rebuild = %+v, want one closed Run", before.Records)
	}

	projection.records = map[string]domain.RunRecord{}
	if err := orch.RebuildRunProjection(ctx, "ws_1"); err != nil {
		t.Fatalf("rebuild Run projection: %v", err)
	}
	after, err := orch.ListRuns(ctx, "ws_1", runprojection.PageRequest{})
	if err != nil {
		t.Fatalf("list after rebuild: %v", err)
	}
	afterJSON, err := json.Marshal(after.Records)
	if err != nil {
		t.Fatalf("encode records after rebuild: %v", err)
	}
	if string(afterJSON) != string(beforeJSON) {
		t.Fatalf("rebuilt records = %s, want byte-equivalent %s", afterJSON, beforeJSON)
	}
}

type memoryRunProjection struct {
	log     eventlog.WorkspaceEventLog
	records map[string]domain.RunRecord
}

func newMemoryRunProjection(log eventlog.WorkspaceEventLog) *memoryRunProjection {
	return &memoryRunProjection{log: log, records: map[string]domain.RunRecord{}}
}

func (projection *memoryRunProjection) Append(
	ctx context.Context,
	request eventlog.AppendRequest,
	next domain.RunRecord,
) (eventlog.AppendResult, error) {
	result, err := projection.log.Append(ctx, request)
	projection.record(result, err, next)
	return result, err
}

func (projection *memoryRunProjection) AppendIfWorkspaceActive(
	ctx context.Context,
	request eventlog.AppendRequest,
	next domain.RunRecord,
) (eventlog.AppendResult, error) {
	result, err := projection.log.AppendIfWorkspaceActive(ctx, request)
	projection.record(result, err, next)
	return result, err
}

func (projection *memoryRunProjection) record(result eventlog.AppendResult, err error, next domain.RunRecord) {
	if err == nil && !result.Duplicate {
		projection.records[next.ID] = next
	}
}

func (projection *memoryRunProjection) List(
	_ context.Context,
	_ string,
	request runprojection.PageRequest,
) (runprojection.Page, error) {
	request, err := request.Validated()
	if err != nil {
		return runprojection.Page{}, err
	}
	records := make([]domain.RunRecord, 0, len(projection.records))
	for _, record := range projection.records {
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].ID < records[j].ID })
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

func (projection *memoryRunProjection) ListOpenIDs(context.Context, string) ([]string, error) {
	var runIDs []string
	for _, record := range projection.records {
		if !record.Closed {
			runIDs = append(runIDs, record.ID)
		}
	}
	sort.Strings(runIDs)
	return runIDs, nil
}

func (projection *memoryRunProjection) Replace(
	_ context.Context,
	_ string,
	records []domain.RunRecord,
) error {
	projection.records = make(map[string]domain.RunRecord, len(records))
	for _, record := range records {
		projection.records[record.ID] = record
	}
	return nil
}
