package adapter

import (
	"context"
	"errors"
	"time"

	"github.com/bengarcia/mercator/internal/domain"
)

var ErrIdempotencyConflict = errors.New("adapter: idempotency conflict")

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
	ListOffers(ctx context.Context, req OfferRequest) ([]domain.OfferSnapshot, error)
	Launch(ctx context.Context, req LaunchRequest) (LaunchReceipt, error)
	Observe(ctx context.Context, req ObserveRequest) (ExternalObservation, error)
	Cancel(ctx context.Context, req CancelRequest) (CancelReceipt, error)
	Release(ctx context.Context, req ReleaseRequest) (ReleaseReceipt, error)
	ListOwned(ctx context.Context, req OwnershipQuery) ([]OwnedExternalObject, error)
}

type OfferRequest struct {
	WorkspaceID string
}

type LaunchRequest struct {
	OperationKey   string
	RequestHash    string
	WorkspaceID    string
	RunID          string
	AttemptID      string
	OwnershipToken string
	LaunchKey      string
	Image          string
	Environment    map[string]string
}

type LaunchReceipt struct {
	ExternalID     string
	LaunchKey      string
	OwnershipToken string
	Phase          ExternalPhase
	AcceptedAt     time.Time
	Duplicate      bool
}

type ObserveRequest struct {
	LaunchKey string
}

type ExternalObservation struct {
	ExternalID string
	LaunchKey  string
	Phase      ExternalPhase
	ObservedAt time.Time
	ExitCode   *int
	NativeJSON string
}

type CancelRequest struct {
	OperationKey string
	RequestHash  string
	LaunchKey    string
}

type CancelReceipt struct {
	Cancelled bool
	Duplicate bool
}

type ReleaseRequest struct {
	OperationKey string
	RequestHash  string
	LaunchKey    string
}

type ReleaseReceipt struct {
	Released  bool
	Duplicate bool
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
	Phase          ExternalPhase
}
