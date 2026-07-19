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
		{name: "unknown external phase", eventType: EventExternalStateObserved, schemaVersion: 1, data: json.RawMessage(`{"launch_key":"launch_1","phase":"future","observed_at":"2026-07-18T12:00:00Z"}`)},
		{name: "unknown outcome", eventType: EventRunOutcomeRecorded, schemaVersion: 1, data: json.RawMessage(`{"outcome":"future"}`)},
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

func TestGetRunRejectsWrongPayloadShapeForEveryKnownEvent(t *testing.T) {
	for _, eventType := range knownRunEventTypes() {
		t.Run(eventType, func(t *testing.T) {
			assertGetRunRejectsStoredEvent(t, eventType, json.RawMessage(`[]`))
		})
	}
}

func TestGetRunRejectsMissingRequiredDataForEveryKnownEvent(t *testing.T) {
	for _, eventType := range knownRunEventTypes() {
		t.Run(eventType, func(t *testing.T) {
			assertGetRunRejectsStoredEvent(t, eventType, json.RawMessage(`{}`))
		})
	}
}

func TestGetRunRejectsInvalidPrivatePayloads(t *testing.T) {
	tests := []struct {
		name        string
		eventType   string
		privateData json.RawMessage
	}{
		{name: "wrong run requested shape", eventType: EventRunRequested, privateData: json.RawMessage(`[]`)},
		{name: "missing run requested data", eventType: EventRunRequested, privateData: json.RawMessage(`{}`)},
		{name: "wrong launch intent shape", eventType: EventLaunchIntentRecorded, privateData: json.RawMessage(`[]`)},
		{name: "missing launch intent data", eventType: EventLaunchIntentRecorded, privateData: json.RawMessage(`{}`)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertGetRunRejectsStoredEventPayloads(t, test.eventType, json.RawMessage(`{}`), test.privateData)
		})
	}
}

func assertGetRunRejectsStoredEvent(t *testing.T, eventType string, data json.RawMessage) {
	t.Helper()
	assertGetRunRejectsStoredEventPayloads(t, eventType, data, nil)
}

func assertGetRunRejectsStoredEventPayloads(t *testing.T, eventType string, data, privateData json.RawMessage) {
	t.Helper()
	ctx := context.Background()
	log := openOrchestratorLog(t)
	orch := New(log, scheduler.New(), fake.New())
	createRun(t, ctx, orch)

	eventID := "evt_run_1_invalid_" + strings.ReplaceAll(eventType, ".", "_")
	_, err := log.Append(ctx, eventlog.AppendRequest{
		Stream:                runStream("ws_1", "run_1"),
		ExpectedStreamVersion: 1,
		CommandKey:            "inject-invalid-event-" + eventType,
		RequestHash:           "invalid-event-fixture-" + eventType,
		Events: []eventlog.NewEvent{{
			ID:            eventID,
			Type:          eventType,
			SchemaVersion: 1,
			OccurredAt:    time.Now().UTC(),
			Data:          data,
			PrivateData:   privateData,
		}},
	})
	if err != nil {
		t.Fatalf("append invalid event fixture: %v", err)
	}

	_, err = orch.GetRun(ctx, "ws_1", "run_1")
	if err == nil {
		t.Fatal("GetRun() accepted an invalid persisted event")
	}
	for _, detail := range []string{eventID, eventType} {
		if !strings.Contains(err.Error(), detail) {
			t.Fatalf("GetRun() error = %q, want event detail %q", err, detail)
		}
	}
}

func knownRunEventTypes() []string {
	return []string{
		EventRunRequested,
		EventPlacementDecided,
		EventAttemptCreated,
		EventLaunchIntentRecorded,
		EventLaunchAccepted,
		EventLaunchIndeterminate,
		EventLaunchFailed,
		EventCancelRequested,
		EventCancelAccepted,
		EventExternalStateObserved,
		EventRunOutcomeRecorded,
		EventCleanupRequested,
		EventCleanupConfirmed,
		EventRunClosed,
		EventRunReported,
	}
}
