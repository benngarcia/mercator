package capability

import (
	"context"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/domain"
)

// EphemeralExecutor runs one workload on a provider-native execution product.
// Mercator does not control a host runtime between workloads here, so an
// ephemeral execution never becomes a Rental, never carries a Rental Schedule,
// and its locality is not observable from one Run to the next.
//
// The methods are the existing provider seam, unchanged: what changes is that
// they now name one lane instead of standing for every backend Mercator has.
// A provider that also rents durable machines implements CapacityProvider
// instead, and its machines execute through a NodeRuntime.
type EphemeralExecutor interface {
	// EphemeralSupport reports what this executor can do, including the
	// lifecycle facts that are weaker here than on reusable capacity.
	EphemeralSupport() EphemeralSupport
	Verify(ctx context.Context) error
	ListOffers(ctx context.Context, req adapter.OfferRequest) ([]domain.OfferSnapshot, error)
	Launch(ctx context.Context, req adapter.LaunchRequest) (adapter.LaunchReceipt, error)
	Observe(ctx context.Context, req adapter.ObserveRequest) (adapter.ExternalObservation, error)
	// Release removes only Mercator's execution from a pool it does not own.
	Release(ctx context.Context, req adapter.ReleaseRequest) (adapter.ReleaseReceipt, error)
	// Terminate destroys a one-shot resource Mercator created for this
	// execution. Standing-pool executors return adapter.ErrTerminateUnsupported.
	Terminate(ctx context.Context, req adapter.TerminateRequest) (adapter.TerminateReceipt, error)
	ListOwned(ctx context.Context, req adapter.OwnershipQuery) ([]adapter.OwnedExternalObject, error)
}

// EphemeralSupport states the weaker lifecycle and locality guarantees of the
// ephemeral lane explicitly, so no caller has to infer them from silence.
type EphemeralSupport struct {
	// ReusableBetweenRuns is always false. It exists so the negotiated
	// capability set answers the reuse question directly rather than by
	// omission, and so a test can assert no executor ever claims otherwise.
	ReusableBetweenRuns bool `json:"reusable_between_runs"`
	// ObservableLocality reports whether Mercator can learn what content the
	// execution environment already holds. Ephemeral products generally cannot
	// tell it, which makes their locality unknown rather than cold.
	ObservableLocality bool `json:"observable_locality"`
	// CancelQueued reports whether an execution can be cancelled before it
	// starts running.
	CancelQueued bool `json:"cancel_queued"`
	// ProviderTTL reports whether the provider reclaims the execution on its
	// own, which bounds the cost of a lost response.
	ProviderTTL bool `json:"provider_ttl"`
	// IdempotentLaunch names the mechanism the provider honors for repeated
	// launch commands: "operation_key", "launch_key", or "none".
	IdempotentLaunch string `json:"idempotent_launch"`
	ListOwned        bool   `json:"list_owned"`
	// ExactPricing reports whether the provider states a billable rate.
	ExactPricing bool `json:"exact_pricing"`
}
