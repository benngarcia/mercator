package adapter

import (
	"context"
	"errors"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
)

var ErrIdempotencyConflict = errors.New("adapter: idempotency conflict")
var ErrLaunchTimeout = errors.New("adapter: launch timeout")
var ErrLaunchIndeterminate = errors.New("adapter: launch indeterminate")
var ErrNotFound = errors.New("adapter: not found")
var ErrRetryableFailure = errors.New("adapter: retryable failure")

// ErrTerminateUnsupported is returned by an adapter whose backing capacity is a
// standing pool (e.g. local Docker): there is no host the broker owns to
// destroy. A run placed on a standing offer records disposition=release, so the
// orchestrator never routes terminate to such an adapter; receiving Terminate
// indicates a misrouted cleanup and the adapter surfaces it explicitly rather
// than silently destroying or no-op'ing.
var ErrTerminateUnsupported = errors.New("adapter: terminate unsupported for standing capacity")

type ExternalPhase string

const (
	ExternalPhaseQueued    ExternalPhase = "queued"
	ExternalPhaseRunning   ExternalPhase = "running"
	ExternalPhaseSucceeded ExternalPhase = "succeeded"
	ExternalPhaseFailed    ExternalPhase = "failed"
	ExternalPhaseCancelled ExternalPhase = "cancelled"
	ExternalPhaseReleased  ExternalPhase = "released"
)

type Adapter interface {
	// Verify performs a cheap credential/reachability check for the authorize
	// flow. It does not launch anything.
	Verify(ctx context.Context) error
	ListOffers(ctx context.Context, req OfferRequest) ([]domain.OfferSnapshot, error)
	Launch(ctx context.Context, req LaunchRequest) (LaunchReceipt, error)
	Observe(ctx context.Context, req ObserveRequest) (ExternalObservation, error)
	Cancel(ctx context.Context, req CancelRequest) (CancelReceipt, error)
	// Release removes only our job/container from a pool we DON'T own (standing
	// capacity). It never touches the host. Used for disposition=release.
	Release(ctx context.Context, req ReleaseRequest) (ReleaseReceipt, error)
	// Terminate destroys a resource WE OWN (a provisioned host/instance). Used
	// for disposition=terminate. Standing-pool adapters return
	// ErrTerminateUnsupported.
	Terminate(ctx context.Context, req TerminateRequest) (TerminateReceipt, error)
	ListOwned(ctx context.Context, req OwnershipQuery) ([]OwnedExternalObject, error)
}

type OfferRequest struct {
	WorkspaceID string
}

type LaunchRequest struct {
	OperationKey              string                      `json:"operation_key"`
	RequestHash               string                      `json:"request_hash"`
	WorkspaceID               string                      `json:"workspace_id"`
	RunID                     string                      `json:"run_id"`
	AttemptID                 string                      `json:"attempt_id"`
	WorkloadID                string                      `json:"workload_id"`
	WorkloadRevisionID        string                      `json:"workload_revision_id"`
	OwnershipToken            string                      `json:"ownership_token"`
	LaunchKey                 string                      `json:"launch_key"`
	CleanupLocator            string                      `json:"cleanup_locator"`
	Image                     string                      `json:"image"`
	Platform                  domain.Platform             `json:"platform"`
	Entrypoint                *[]string                   `json:"entrypoint,omitempty"`
	Args                      []string                    `json:"args,omitempty"`
	Environment               []EnvironmentBinding        `json:"environment,omitempty"`
	Ports                     []domain.PortSpec           `json:"ports,omitempty"`
	Resources                 domain.ResourceRequirements `json:"resources"`
	SelectedOfferSnapshotID   string                      `json:"selected_offer_snapshot_id"`
	SelectedOfferConnectionID string                      `json:"selected_offer_connection_id"`
	SelectedOfferAdapterType  string                      `json:"selected_offer_adapter_type"`
	SelectedOfferNativeRef    string                      `json:"selected_offer_native_ref"`
	// Disposition is the RECORDED cleanup intent, derived from the selected
	// offer's Kind at launch time (provisionable->terminate, standing->release)
	// and persisted on the launch_intent_recorded event. Cleanup dispatches on
	// this recorded value; it is never re-inferred from live offers.
	Disposition domain.Disposition `json:"disposition,omitempty"`
}

type EnvironmentBinding struct {
	Name  string  `json:"name"`
	Value *string `json:"value,omitempty"`
}

type LaunchReceipt struct {
	ExternalID     string        `json:"external_id"`
	LaunchKey      string        `json:"launch_key"`
	OwnershipToken string        `json:"ownership_token"`
	CleanupLocator string        `json:"cleanup_locator"`
	Phase          ExternalPhase `json:"phase"`
	AcceptedAt     time.Time     `json:"accepted_at"`
	Duplicate      bool          `json:"duplicate"`
}

type ObserveRequest struct {
	WorkspaceID    string
	ConnectionID   string
	LaunchKey      string
	OwnershipToken string
	RequestHash    string
}

type ExternalObservation struct {
	ExternalID string        `json:"external_id"`
	LaunchKey  string        `json:"launch_key"`
	Phase      ExternalPhase `json:"phase"`
	ObservedAt time.Time     `json:"observed_at"`
	ExitCode   *int          `json:"exit_code,omitempty"`
	NativeJSON string        `json:"native_json,omitempty"`
}

type CancelRequest struct {
	WorkspaceID  string
	ConnectionID string
	OperationKey string
	RequestHash  string
	LaunchKey    string
}

type CancelReceipt struct {
	Cancelled bool
	Duplicate bool
}

type ReleaseRequest struct {
	WorkspaceID       string
	ConnectionID      string
	OperationKey      string
	RequestHash       string
	LaunchKey         string
	OwnershipToken    string
	LaunchRequestHash string
}

type ReleaseReceipt struct {
	Released  bool
	Duplicate bool
}

// TerminateRequest destroys a resource the broker owns (a provisioned host).
// It carries the same idempotency machinery (OperationKey/RequestHash) and
// ownership material (OwnershipToken/LaunchRequestHash) as ReleaseRequest so
// the no-orphan reconciliation path is identical.
type TerminateRequest struct {
	WorkspaceID       string
	ConnectionID      string
	OperationKey      string
	RequestHash       string
	LaunchKey         string
	OwnershipToken    string
	LaunchRequestHash string
}

type TerminateReceipt struct {
	Terminated bool
	Duplicate  bool
}

type OwnershipQuery struct {
	WorkspaceID string
}

type OwnedExternalObject struct {
	ExternalID     string
	WorkspaceID    string
	RunID          string
	AttemptID      string
	OwnershipToken string
	LaunchKey      string
	CleanupLocator string
	RequestHash    string
	Phase          ExternalPhase
}
