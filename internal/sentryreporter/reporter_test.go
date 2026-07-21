package sentryreporter

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/getsentry/sentry-go"
)

func TestAbsentDSNDisablesReporting(t *testing.T) {
	transport := &recordingTransport{}
	reporter, err := New(map[string]string{
		"SENTRY_ENVIRONMENT": "production",
		"SENTRY_RELEASE":     "mercator@v0.5.0",
	}, WithTransport(transport))
	if err != nil {
		t.Fatalf("configure reporter: %v", err)
	}

	if reporter.Enabled() {
		t.Fatal("reporter enabled without SENTRY_DSN")
	}
	reporter.CaptureProviderFailure(t.Context(), adapter.ProviderFailureDiagnostic{
		AdapterType: "shadeform",
		Operation:   "launch",
		Failure:     adapter.ProviderFailure{Kind: adapter.ProviderFailureAuthentication},
	})
	if events := transport.recordedEvents(); len(events) != 0 {
		t.Fatalf("disabled reporter recorded %d events", len(events))
	}
}

func TestPresentDSNRequiresEnvironmentAndRelease(t *testing.T) {
	tests := []struct {
		name   string
		values map[string]string
		want   string
	}{
		{
			name: "environment",
			values: map[string]string{
				"SENTRY_DSN":     "https://public@example.com/1",
				"SENTRY_RELEASE": "mercator@v0.5.0",
			},
			want: "SENTRY_ENVIRONMENT",
		},
		{
			name: "release",
			values: map[string]string{
				"SENTRY_DSN":         "https://public@example.com/1",
				"SENTRY_ENVIRONMENT": "production",
			},
			want: "SENTRY_RELEASE",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := New(test.values)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want missing %s diagnostic", err, test.want)
			}
		})
	}
}

func TestProviderCapacityFailureRecordsSafeCorrelatedWarning(t *testing.T) {
	transport := &recordingTransport{}
	reporter, err := New(map[string]string{
		"SENTRY_DSN":         "https://public@example.com/1",
		"SENTRY_ENVIRONMENT": "staging",
		"SENTRY_RELEASE":     "mercator@v0.5.0",
	}, WithTransport(transport))
	if err != nil {
		t.Fatalf("configure reporter: %v", err)
	}
	t.Cleanup(reporter.Close)

	reporter.CaptureProviderFailure(t.Context(), adapter.ProviderFailureDiagnostic{
		WorkspaceID:     "ws_1",
		RunID:           "run_1",
		AttemptID:       "att_1",
		ConnectionID:    "conn_shadeform",
		AdapterType:     "shadeform",
		Operation:       "launch",
		OfferSnapshotID: "off_1",
		OfferNativeRef:  "lambdalabs/us-west/rtx6000ada",
		Failure: adapter.ProviderFailure{
			Kind:              adapter.ProviderFailureCapacityUnavailable,
			Status:            409,
			ProviderCode:      "OUT_OF_STOCK",
			Retryable:         true,
			SideEffect:        adapter.SideEffectNone,
			ResponseBody:      `{"authorization":"Bearer provider-secret","env":{"TOKEN":"workload-secret"}}`,
			RetryCount:        0,
			ResponseTruncated: false,
		},
	})

	event := transport.singleEvent(t)
	if event.Level != sentry.LevelWarning {
		t.Errorf("level = %q, want warning", event.Level)
	}
	if event.Environment != "staging" || event.Release != "mercator@v0.5.0" {
		t.Errorf("environment/release = %q/%q", event.Environment, event.Release)
	}
	wantFingerprint := []string{"provider_failure", "shadeform", "launch", "capacity_unavailable", "OUT_OF_STOCK", "409"}
	if strings.Join(event.Fingerprint, "|") != strings.Join(wantFingerprint, "|") {
		t.Errorf("fingerprint = %#v, want %#v", event.Fingerprint, wantFingerprint)
	}
	wantContext := map[string]any{
		"adapter_type":      "shadeform",
		"attempt_id":        "att_1",
		"connection_id":     "conn_shadeform",
		"failure_kind":      "capacity_unavailable",
		"http_status":       409,
		"offer_native_ref":  "lambdalabs/us-west/rtx6000ada",
		"offer_snapshot_id": "off_1",
		"operation":         "launch",
		"provider_code":     "OUT_OF_STOCK",
		"retry_count":       0,
		"retryable":         true,
		"run_id":            "run_1",
		"side_effect":       "none",
		"workspace_id":      "ws_1",
	}
	gotContext := event.Contexts["provider_failure"]
	for key, want := range wantContext {
		if got := gotContext[key]; got != want {
			t.Errorf("provider_failure[%q] = %#v, want %#v", key, got, want)
		}
	}
	eventJSON, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	for _, forbidden := range []string{"provider-secret", "workload-secret", "authorization", "response_body"} {
		if strings.Contains(string(eventJSON), forbidden) {
			t.Errorf("event contains forbidden value %q: %s", forbidden, eventJSON)
		}
	}
}

func TestProviderFailureGroupingAndSeverity(t *testing.T) {
	transport := &recordingTransport{}
	reporter, err := New(map[string]string{
		"SENTRY_DSN":         "https://public@example.com/1",
		"SENTRY_ENVIRONMENT": "production",
		"SENTRY_RELEASE":     "mercator@v0.5.0",
	}, WithTransport(transport))
	if err != nil {
		t.Fatalf("configure reporter: %v", err)
	}
	t.Cleanup(reporter.Close)

	base := adapter.ProviderFailureDiagnostic{
		WorkspaceID:  "ws_1",
		RunID:        "run_1",
		AttemptID:    "att_1",
		ConnectionID: "conn_1",
		AdapterType:  "shadeform",
		Operation:    "launch",
		Failure: adapter.ProviderFailure{
			Kind:         adapter.ProviderFailureCapacityUnavailable,
			Status:       409,
			ProviderCode: "OUT_OF_STOCK",
			Retryable:    true,
			SideEffect:   adapter.SideEffectNone,
		},
	}
	reporter.CaptureProviderFailure(t.Context(), base)
	equivalent := base
	equivalent.WorkspaceID = "ws_2"
	equivalent.RunID = "run_2"
	equivalent.AttemptID = "att_2"
	equivalent.ConnectionID = "conn_2"
	reporter.CaptureProviderFailure(t.Context(), equivalent)
	differentAdapter := base
	differentAdapter.AdapterType = "runpod"
	reporter.CaptureProviderFailure(t.Context(), differentAdapter)
	differentOperation := base
	differentOperation.Operation = "terminate"
	reporter.CaptureProviderFailure(t.Context(), differentOperation)
	differentCode := base
	differentCode.Failure.ProviderCode = "QUOTA_EXCEEDED"
	reporter.CaptureProviderFailure(t.Context(), differentCode)
	exhausted := base
	exhausted.Failure.RetryCount = 3
	reporter.CaptureProviderFailure(t.Context(), exhausted)
	authentication := base
	authentication.Failure.Kind = adapter.ProviderFailureAuthentication
	authentication.Failure.Status = 401
	authentication.Failure.ProviderCode = "UNAUTHORIZED"
	authentication.Failure.Retryable = false
	reporter.CaptureProviderFailure(t.Context(), authentication)

	events := transport.recordedEvents()
	if len(events) != 7 {
		t.Fatalf("recorded events = %d, want 7", len(events))
	}
	fingerprint := func(event *sentry.Event) string { return strings.Join(event.Fingerprint, "|") }
	if fingerprint(events[0]) != fingerprint(events[1]) {
		t.Errorf("equivalent failures split: %q != %q", fingerprint(events[0]), fingerprint(events[1]))
	}
	for i := 2; i <= 4; i++ {
		if fingerprint(events[0]) == fingerprint(events[i]) {
			t.Errorf("distinct failure %d shares fingerprint %q", i, fingerprint(events[0]))
		}
	}
	if events[0].Level != sentry.LevelWarning {
		t.Errorf("individual capacity level = %q, want warning", events[0].Level)
	}
	if events[5].Level != sentry.LevelError {
		t.Errorf("exhausted capacity level = %q, want error", events[5].Level)
	}
	if events[6].Level != sentry.LevelError {
		t.Errorf("authentication level = %q, want error", events[6].Level)
	}
}

func TestFlushStopsAtCallerDeadline(t *testing.T) {
	transport := &blockingTransport{}
	reporter, err := New(map[string]string{
		"SENTRY_DSN":         "https://public@example.com/1",
		"SENTRY_ENVIRONMENT": "production",
		"SENTRY_RELEASE":     "mercator@v0.5.0",
	}, WithTransport(transport))
	if err != nil {
		t.Fatalf("configure reporter: %v", err)
	}
	t.Cleanup(reporter.Close)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	if reporter.Flush(ctx) {
		t.Fatal("flush succeeded after transport blocked through deadline")
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("flush took %s, want caller-bounded shutdown", elapsed)
	}
}

type recordingTransport struct {
	mu     sync.Mutex
	events []*sentry.Event
}

func (*recordingTransport) Configure(sentry.ClientOptions) {}

func (r *recordingTransport) SendEvent(event *sentry.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (*recordingTransport) Flush(time.Duration) bool              { return true }
func (*recordingTransport) FlushWithContext(context.Context) bool { return true }
func (*recordingTransport) Close()                                {}

func (r *recordingTransport) singleEvent(t *testing.T) *sentry.Event {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.events) != 1 {
		t.Fatalf("recorded events = %d, want 1", len(r.events))
	}
	return r.events[0]
}

func (r *recordingTransport) recordedEvents() []*sentry.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*sentry.Event(nil), r.events...)
}

type blockingTransport struct{}

func (*blockingTransport) Configure(sentry.ClientOptions) {}
func (*blockingTransport) SendEvent(*sentry.Event)        {}
func (*blockingTransport) Flush(time.Duration) bool       { return false }
func (*blockingTransport) Close()                         {}

func (*blockingTransport) FlushWithContext(ctx context.Context) bool {
	<-ctx.Done()
	return false
}
