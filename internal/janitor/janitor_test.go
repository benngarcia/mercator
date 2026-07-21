package janitor

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/adapter/fake"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
)

func TestJanitorReleasesOwnedResources(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ad := fake.New()
	_, err := ad.Launch(ctx, adapter.LaunchRequest{
		OperationKey:       "launch_orphan",
		RequestHash:        "sha256:orphan",
		WorkspaceID:        "ws_1",
		RunID:              "run_orphan",
		AttemptID:          "att_orphan",
		OwnershipToken:     "own_orphan",
		LaunchKey:          "launch_orphan",
		CleanupLocator:     "cleanup_orphan",
		WorkloadID:         "wl_1",
		WorkloadRevisionID: "wrev_1",
	})
	if err != nil {
		t.Fatalf("seed orphan: %v", err)
	}

	log := openJanitorTestLog(t)

	result, err := New(ad, WithEventLog(log)).Sweep(ctx, "ws_1")
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if result.Found != 1 || result.Released != 1 {
		t.Fatalf("unexpected sweep result: %+v", result)
	}
	owned, err := ad.ListOwned(ctx, adapter.OwnershipQuery{WorkspaceID: "ws_1"})
	if err != nil {
		t.Fatalf("list owned: %v", err)
	}
	if len(owned) != 0 {
		t.Fatalf("expected owned resources released, got %+v", owned)
	}
}

func TestJanitorSkipsActiveRunResources(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ad := fake.New()
	_, err := ad.Launch(ctx, adapter.LaunchRequest{
		OperationKey:       "launch_active",
		RequestHash:        "sha256:active",
		WorkspaceID:        "ws_1",
		RunID:              "run_active",
		AttemptID:          "att_active",
		OwnershipToken:     "own_active",
		LaunchKey:          "launch_active",
		CleanupLocator:     "cleanup_active",
		WorkloadID:         "wl_1",
		WorkloadRevisionID: "wrev_1",
	})
	if err != nil {
		t.Fatalf("seed active object: %v", err)
	}
	log := openJanitorTestLog(t)
	appendRunEvent(t, log, "ws_1", "run_active", "compute.run.requested.v1")

	result, err := New(ad, WithEventLog(log)).Sweep(ctx, "ws_1")
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if result.Found != 1 || result.Released != 0 {
		t.Fatalf("active resource should be found but not released: %+v", result)
	}
	owned, err := ad.ListOwned(ctx, adapter.OwnershipQuery{WorkspaceID: "ws_1"})
	if err != nil {
		t.Fatalf("list owned: %v", err)
	}
	if len(owned) != 1 {
		t.Fatalf("expected active resource to remain, got %+v", owned)
	}
}

func TestJanitorRequiresEventLog(t *testing.T) {
	t.Parallel()
	_, err := New(fake.New()).Sweep(context.Background(), "ws_1")
	if err == nil {
		t.Fatalf("expected missing event log error")
	}
}

func openJanitorTestLog(t *testing.T) *eventlog.SQLiteEventLog {
	t.Helper()
	log, err := eventlog.OpenSQLite(context.Background(), "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open event log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	return log
}

func appendRunEvent(t *testing.T, log eventlog.EventLog, workspaceID, runID, eventType string) {
	t.Helper()
	_, err := log.Append(context.Background(), eventlog.AppendRequest{
		Stream:                eventlog.StreamKey{WorkspaceID: workspaceID, Type: "run", ID: runID},
		ExpectedStreamVersion: 0,
		CommandKey:            "seed:" + eventType,
		RequestHash:           "sha256:seed",
		CorrelationID:         runID,
		CausationID:           "seed",
		Events: []eventlog.NewEvent{{
			ID:            "evt_" + workspaceID + "_" + runID + "_seed",
			Type:          eventType,
			SchemaVersion: 1,
			OccurredAt:    time.Now().UTC(),
			Visibility:    eventlog.VisibilityPublic,
			Data:          []byte(`{}`),
		}},
	})
	if err != nil {
		t.Fatalf("append run event: %v", err)
	}
}

func TestJanitorReclaimsViaRecordedTerminateDisposition(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ad := fake.New()
	_, err := ad.Launch(ctx, adapter.LaunchRequest{
		OperationKey:       "launch_term",
		RequestHash:        "sha256:term",
		WorkspaceID:        "ws_1",
		RunID:              "run_term",
		AttemptID:          "att_term",
		OwnershipToken:     "own_term",
		LaunchKey:          "launch_term",
		CleanupLocator:     "cleanup_term",
		WorkloadID:         "wl_1",
		WorkloadRevisionID: "wrev_1",
		Disposition:        domain.DispositionTerminate,
	})
	if err != nil {
		t.Fatalf("seed terminate orphan: %v", err)
	}
	log := openJanitorTestLog(t)
	appendLaunchIntent(t, log, "ws_1", "run_term", domain.DispositionTerminate)

	result, err := New(ad, WithEventLog(log)).Sweep(ctx, "ws_1")
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if result.Released != 1 {
		t.Fatalf("expected one reclaim, got %+v", result)
	}
	if ad.TerminateCount() != 1 {
		t.Fatalf("janitor must reclaim a provisioned run via Terminate, terminate count=%d", ad.TerminateCount())
	}
	if ad.ReleaseCount() != 0 {
		t.Fatalf("janitor must not Release a provisioned run, release count=%d", ad.ReleaseCount())
	}
}

func TestJanitorCarriesRecordedOfferIdentityIntoCleanup(t *testing.T) {
	t.Parallel()
	cleanup := &captureCleanupAdapter{object: adapter.OwnedExternalObject{
		ExternalID:     "ext_1",
		WorkspaceID:    "ws_1",
		ConnectionID:   "conn_1",
		AdapterType:    "shadeform",
		RunID:          "run_term",
		AttemptID:      "att_run_term",
		OwnershipToken: "own_term",
		LaunchKey:      "launch_run_term",
		RequestHash:    "sha256:launch",
	}}
	log := openJanitorTestLog(t)
	appendLaunchIntent(t, log, "ws_1", "run_term", domain.DispositionTerminate)

	result, err := New(cleanup, WithEventLog(log)).Sweep(t.Context(), "ws_1")
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if result.Released != 1 || cleanup.terminateRequest == nil {
		t.Fatalf("cleanup result/request = %+v/%+v, want one termination", result, cleanup.terminateRequest)
	}
	context := cleanup.terminateRequest.DiagnosticContext
	if context.OfferSnapshotID != "off_run_term" || context.OfferNativeRef != "cloud/region/run_term" {
		t.Fatalf("cleanup diagnostic context = %+v, want recorded Offer identity", context)
	}
}

func TestJanitorRejectsMalformedRecordedLaunchIntent(t *testing.T) {
	t.Parallel()
	cleanup := &captureCleanupAdapter{object: adapter.OwnedExternalObject{
		WorkspaceID:  "ws_1",
		ConnectionID: "conn_1",
		RunID:        "run_malformed",
		LaunchKey:    "launch_malformed",
	}}
	log := openJanitorTestLog(t)
	payload, err := os.ReadFile("testdata/malformed_launch_intent.json")
	if err != nil {
		t.Fatalf("read malformed launch intent: %v", err)
	}
	appendLaunchIntentEvents(t, log, "ws_1", "run_malformed", payload)

	_, err = New(cleanup, WithEventLog(log)).Sweep(t.Context(), "ws_1")

	if err == nil {
		t.Fatal("sweep error = nil, want malformed launch intent rejected")
	}
}

type captureCleanupAdapter struct {
	object           adapter.OwnedExternalObject
	terminateRequest *adapter.TerminateRequest
}

func (a *captureCleanupAdapter) ListOwned(context.Context, adapter.OwnershipQuery) ([]adapter.OwnedExternalObject, error) {
	return []adapter.OwnedExternalObject{a.object}, nil
}

func (a *captureCleanupAdapter) Release(context.Context, adapter.ReleaseRequest) (adapter.ReleaseReceipt, error) {
	return adapter.ReleaseReceipt{}, nil
}

func (a *captureCleanupAdapter) Terminate(_ context.Context, request adapter.TerminateRequest) (adapter.TerminateReceipt, error) {
	a.terminateRequest = &request
	return adapter.TerminateReceipt{Terminated: true}, nil
}

func appendLaunchIntent(t *testing.T, log eventlog.EventLog, workspaceID, runID string, disposition domain.Disposition) {
	t.Helper()
	intent := adapter.LaunchRequest{
		WorkspaceID:               workspaceID,
		RunID:                     runID,
		AttemptID:                 "att_" + runID,
		LaunchKey:                 "launch_" + runID,
		SelectedOfferConnectionID: "conn_1",
		SelectedOfferSnapshotID:   "off_" + runID,
		SelectedOfferNativeRef:    "cloud/region/" + runID,
		SelectedOfferAdapterType:  "shadeform",
		Disposition:               disposition,
	}
	private, err := json.Marshal(intent)
	if err != nil {
		t.Fatalf("marshal intent: %v", err)
	}
	appendLaunchIntentEvents(t, log, workspaceID, runID, private)
}

func appendLaunchIntentEvents(t *testing.T, log eventlog.EventLog, workspaceID, runID string, private []byte) {
	t.Helper()
	_, err := log.Append(context.Background(), eventlog.AppendRequest{
		Stream:                eventlog.StreamKey{WorkspaceID: workspaceID, Type: "run", ID: runID},
		ExpectedStreamVersion: 0,
		CommandKey:            "seed:intent:" + runID,
		RequestHash:           "sha256:seed_intent",
		CorrelationID:         runID,
		CausationID:           "seed",
		Events: []eventlog.NewEvent{
			{
				ID:            "evt_" + workspaceID + "_" + runID + "_intent",
				Type:          "compute.run.launch_intent_recorded.v1",
				SchemaVersion: 1,
				OccurredAt:    time.Now().UTC(),
				Visibility:    eventlog.VisibilityPublic,
				Data:          []byte(`{}`),
				PrivateData:   private,
			},
			{
				ID:            "evt_" + workspaceID + "_" + runID + "_cleanup",
				Type:          "compute.run.cleanup_requested.v1",
				SchemaVersion: 1,
				OccurredAt:    time.Now().UTC(),
				Visibility:    eventlog.VisibilityPublic,
				Data:          []byte(`{}`),
			},
		},
	})
	if err != nil {
		t.Fatalf("append launch intent: %v", err)
	}
}
