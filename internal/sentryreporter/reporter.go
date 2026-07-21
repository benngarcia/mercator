package sentryreporter

import (
	"context"
	"fmt"
	"strconv"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/getsentry/sentry-go"
)

// Reporter sends actionable provider failures to Sentry when configured.
type Reporter struct {
	client *sentry.Client
}

type options struct {
	transport sentry.Transport
}

// Option changes the Sentry client boundary used by a Reporter.
type Option func(*options)

// WithTransport supplies a Sentry transport. Production uses the SDK's async
// HTTP transport; tests use a recording transport at the same boundary.
func WithTransport(transport sentry.Transport) Option {
	return func(options *options) {
		options.transport = transport
	}
}

// New configures provider-failure reporting from the process environment.
func New(values map[string]string, opts ...Option) (*Reporter, error) {
	if values["SENTRY_DSN"] == "" {
		return &Reporter{}, nil
	}
	for _, required := range []string{"SENTRY_ENVIRONMENT", "SENTRY_RELEASE"} {
		if values[required] == "" {
			return nil, fmt.Errorf("%s is required when SENTRY_DSN is set", required)
		}
	}
	configured := options{}
	for _, option := range opts {
		option(&configured)
	}
	client, err := sentry.NewClient(sentry.ClientOptions{
		Dsn:                    values["SENTRY_DSN"],
		Environment:            values["SENTRY_ENVIRONMENT"],
		Release:                values["SENTRY_RELEASE"],
		ServerName:             "mercator",
		SendDefaultPII:         false,
		Transport:              configured.transport,
		DisableLogs:            true,
		DisableMetrics:         true,
		DisableTelemetryBuffer: true,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize Sentry client: %w", err)
	}
	return &Reporter{client: client}, nil
}

// Enabled reports whether SENTRY_DSN enabled delivery.
func (r *Reporter) Enabled() bool {
	return r.client != nil
}

// CaptureProviderFailure records a whitelisted event without provider response
// bodies, workload environment values, credentials, headers, or request data.
func (r *Reporter) CaptureProviderFailure(_ context.Context, diagnostic adapter.ProviderFailureDiagnostic) {
	if !r.Enabled() {
		return
	}
	failure := diagnostic.Failure
	event := sentry.NewEvent()
	event.Message = "provider operation failed"
	event.Level = level(diagnostic)
	event.Fingerprint = []string{
		"provider_failure",
		diagnostic.AdapterType,
		diagnostic.Operation,
		string(failure.Kind),
		failure.ProviderCode,
		strconv.Itoa(failure.Status),
	}
	event.Tags = map[string]string{
		"adapter_type":  diagnostic.AdapterType,
		"failure_kind":  string(failure.Kind),
		"http_status":   strconv.Itoa(failure.Status),
		"operation":     diagnostic.Operation,
		"provider_code": failure.ProviderCode,
		"retryable":     strconv.FormatBool(failure.Retryable),
		"side_effect":   string(failure.SideEffect),
	}
	event.Contexts["provider_failure"] = sentry.Context{
		"alternatives_exhausted": diagnostic.AlternativesExhausted,
		"adapter_type":           diagnostic.AdapterType,
		"attempt_id":             diagnostic.AttemptID,
		"connection_id":          diagnostic.ConnectionID,
		"failure_kind":           string(failure.Kind),
		"http_status":            failure.Status,
		"offer_native_ref":       diagnostic.OfferNativeRef,
		"offer_snapshot_id":      diagnostic.OfferSnapshotID,
		"operation":              diagnostic.Operation,
		"provider_code":          failure.ProviderCode,
		"response_truncated":     failure.ResponseTruncated,
		"retry_count":            failure.RetryCount,
		"retryable":              failure.Retryable,
		"run_id":                 diagnostic.RunID,
		"side_effect":            string(failure.SideEffect),
		"workspace_id":           diagnostic.WorkspaceID,
	}
	r.client.CaptureEvent(event, nil, nil)
}

func level(diagnostic adapter.ProviderFailureDiagnostic) sentry.Level {
	if diagnostic.Failure.Kind == adapter.ProviderFailureCapacityUnavailable && !diagnostic.AlternativesExhausted {
		return sentry.LevelWarning
	}
	return sentry.LevelError
}

// Flush waits for queued events until ctx expires. Disabled reporters have no
// queue and complete immediately.
func (r *Reporter) Flush(ctx context.Context) bool {
	if !r.Enabled() {
		return true
	}
	return r.client.FlushWithContext(ctx)
}

// Close releases the SDK transport after the caller has flushed it.
func (r *Reporter) Close() {
	if r.Enabled() {
		r.client.Close()
	}
}
