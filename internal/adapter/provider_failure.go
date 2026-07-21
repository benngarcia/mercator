package adapter

import "fmt"

// ProviderFailureKind is Mercator's provider-neutral classification for a
// failed adapter operation. Provider-native codes remain separate so public
// consumers never need to understand one provider's vocabulary.
type ProviderFailureKind string

const (
	ProviderFailureCapacityUnavailable ProviderFailureKind = "capacity_unavailable"
	ProviderFailureInvalidRequest      ProviderFailureKind = "invalid_request"
	ProviderFailureAuthentication      ProviderFailureKind = "authentication"
	ProviderFailureRateLimited         ProviderFailureKind = "rate_limited"
	ProviderFailureTransport           ProviderFailureKind = "transport"
	ProviderFailureInternal            ProviderFailureKind = "provider_internal"
)

// SideEffectCertainty records what Mercator knows about the failed operation's
// external effect. Retryability is deliberately independent: a capacity
// rejection is retryable with no object, while a failed Create can be
// retryable only after reconciliation resolves its indeterminate side effect.
type SideEffectCertainty string

const (
	SideEffectNone          SideEffectCertainty = "none"
	SideEffectIndeterminate SideEffectCertainty = "indeterminate"
)

// ProviderFailure carries private provider diagnostics through the adapter
// boundary. ResponseBody is sanitized and bounded by the adapter before this
// value leaves the provider package.
type ProviderFailure struct {
	Kind              ProviderFailureKind
	Status            int
	ProviderCode      string
	Retryable         bool
	SideEffect        SideEffectCertainty
	ResponseBody      string
	RetryCount        int
	ResponseTruncated bool
}

// ProviderFailureDiagnostic is the private, correlated record for one final
// failed provider operation. Reporters must select fields from this value and
// must never serialize the originating provider request.
type ProviderFailureDiagnostic struct {
	WorkspaceID     string
	RunID           string
	AttemptID       string
	ConnectionID    string
	AdapterType     string
	Operation       string
	OfferSnapshotID string
	OfferNativeRef  string
	Failure         ProviderFailure
}

// ProviderOperationContext carries the stable Run and provider correlation
// shared by cancellation and cleanup requests.
type ProviderOperationContext struct {
	WorkspaceID     string
	RunID           string
	AttemptID       string
	ConnectionID    string
	AdapterType     string
	OfferSnapshotID string
	OfferNativeRef  string
}

// FailureDiagnostic names the operation that failed without copying request
// payload or ownership material into the diagnostic.
func (c ProviderOperationContext) FailureDiagnostic(operation string) ProviderFailureDiagnostic {
	return ProviderFailureDiagnostic{
		WorkspaceID:     c.WorkspaceID,
		RunID:           c.RunID,
		AttemptID:       c.AttemptID,
		ConnectionID:    c.ConnectionID,
		AdapterType:     c.AdapterType,
		Operation:       operation,
		OfferSnapshotID: c.OfferSnapshotID,
		OfferNativeRef:  c.OfferNativeRef,
	}
}

func (f *ProviderFailure) Error() string {
	if f == nil {
		return "provider operation failed"
	}
	return fmt.Sprintf("provider operation failed: %s", f.Kind)
}

// Is preserves the existing adapter sentinel contract for callers that need
// to branch on retryability or reconcile a possibly-created external object.
func (f *ProviderFailure) Is(target error) bool {
	if f == nil {
		return false
	}
	return target == ErrLaunchIndeterminate && f.SideEffect == SideEffectIndeterminate ||
		target == ErrRetryableFailure && f.Retryable
}
