package orchestrator

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter/fake"
	"github.com/benngarcia/mercator/internal/eventlog"
	"github.com/benngarcia/mercator/internal/scheduler"
)

func TestGetRunRejectsInvalidPersistedEvents(t *testing.T) {
	tests := []struct {
		name          string
		eventType     string
		schemaVersion int
		data          json.RawMessage
	}{
		{name: "malformed external observation", eventType: EventExternalStateObserved, schemaVersion: 1, data: json.RawMessage(`{`)},
		{name: "malformed run report", eventType: EventRunReported, schemaVersion: 1, data: json.RawMessage(`{`)},
		{name: "malformed outcome", eventType: EventRunOutcomeRecorded, schemaVersion: 1, data: json.RawMessage(`{`)},
		{name: "unsupported outcome schema", eventType: EventRunOutcomeRecorded, schemaVersion: 2, data: json.RawMessage(`{"outcome":"succeeded"}`)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			log := openOrchestratorLog(t)
			orch := New(log, scheduler.New(), fake.New())
			createRun(t, ctx, orch)

			eventID := "evt_run_1_invalid"
			_, err := log.Append(ctx, eventlog.AppendRequest{
				Stream:                runStream("ws_1", "run_1"),
				ExpectedStreamVersion: 1,
				CommandKey:            "inject-invalid-event",
				RequestHash:           "invalid-event-fixture",
				Events: []eventlog.NewEvent{{
					ID:            eventID,
					Type:          test.eventType,
					SchemaVersion: test.schemaVersion,
					OccurredAt:    time.Now().UTC(),
					Data:          test.data,
				}},
			})
			if err != nil {
				t.Fatalf("append invalid event fixture: %v", err)
			}

			_, err = orch.GetRun(ctx, "ws_1", "run_1")
			if err == nil {
				t.Fatal("GetRun() accepted an invalid persisted event")
			}
			for _, detail := range []string{eventID, test.eventType, "schema"} {
				if !strings.Contains(err.Error(), detail) {
					t.Fatalf("GetRun() error = %q, want event detail %q", err, detail)
				}
			}
		})
	}
}

func TestGetRunRejectsClosedRunWithoutOutcome(t *testing.T) {
	ctx := context.Background()
	log := openOrchestratorLog(t)
	orch := New(log, scheduler.New(), fake.New())
	createRun(t, ctx, orch)

	_, err := log.Append(ctx, eventlog.AppendRequest{
		Stream:                runStream("ws_1", "run_1"),
		ExpectedStreamVersion: 1,
		CommandKey:            "inject-invalid-close",
		RequestHash:           "invalid-close-fixture",
		Events: []eventlog.NewEvent{{
			ID:            "evt_run_1_closed_without_outcome",
			Type:          EventRunClosed,
			SchemaVersion: 1,
			OccurredAt:    time.Now().UTC(),
			Data:          json.RawMessage(`{"closed":true}`),
		}},
	})
	if err != nil {
		t.Fatalf("append invalid close fixture: %v", err)
	}

	_, err = orch.GetRun(ctx, "ws_1", "run_1")
	if err == nil || !strings.Contains(err.Error(), "closed without a recorded outcome") {
		t.Fatalf("GetRun() error = %v, want closed-without-outcome invariant failure", err)
	}
}
