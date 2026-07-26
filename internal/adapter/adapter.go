package adapter

import (
	"errors"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
)

var ErrIdempotencyConflict = errors.New("adapter: idempotency conflict")
var ErrLaunchTimeout = errors.New("adapter: launch timeout")
var ErrLaunchIndeterminate = errors.New("adapter: launch indeterminate")
var ErrNotFound = errors.New("adapter: not found")
var ErrRetryableFailure = errors.New("adapter: retryable failure")
var ErrRegistryAuthentication = errors.New("adapter: registry authentication failed")

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

// Exited reports whether the phase represents a container that ran to an
// exit — the only phases where an exit code carries meaning. Docker reports
// .State.ExitCode as 0 for containers that are still running, so consumers
// must never treat an exit code on a non-exited observation as authoritative.
func (p ExternalPhase) Exited() bool {
	return p == ExternalPhaseSucceeded || p == ExternalPhaseFailed
}

func (p ExternalPhase) Valid() bool {
	switch p {
	case ExternalPhaseQueued,
		ExternalPhaseRunning,
		ExternalPhaseSucceeded,
		ExternalPhaseFailed,
		ExternalPhaseCancelled,
		ExternalPhaseReleased:
		return true
	default:
		return false
	}
}

type OfferRequest struct {
	WorkspaceID string
	Resources   domain.ResourceRequirements
}

type LaunchRequest struct {
	OperationKey       string                      `json:"operation_key"`
	RequestHash        string                      `json:"request_hash"`
	WorkspaceID        string                      `json:"workspace_id"`
	RunID              string                      `json:"run_id"`
	AttemptID          string                      `json:"attempt_id"`
	WorkloadID         string                      `json:"workload_id"`
	WorkloadRevisionID string                      `json:"workload_revision_id"`
	OwnershipToken     string                      `json:"ownership_token"`
	LaunchKey          string                      `json:"launch_key"`
	CleanupLocator     string                      `json:"cleanup_locator"`
	Image              string                      `json:"image"`
	Platform           domain.Platform             `json:"platform"`
	Entrypoint         *[]string                   `json:"entrypoint,omitempty"`
	Args               []string                    `json:"args,omitempty"`
	Environment        []EnvironmentBinding        `json:"environment,omitempty"`
	Ports              []domain.PortSpec           `json:"ports,omitempty"`
	Resources          domain.ResourceRequirements `json:"resources"`
	// CacheMounts is the mutable state this launch asks its host to attach, as
	// the workload declared it. It is recorded on the launch intent rather than
	// re-read from the workload later, for the same reason the selected offer's
	// lane is: what a Run was launched with is a fact about that launch.
	CacheMounts []domain.CacheMountRequirement `json:"cache_mounts,omitempty"`
	// MaxRuntimeSeconds is the run's execution bound from the workload's
	// ExecutionPolicy. Adapters that support provider-side reclamation (e.g.
	// Shadeform auto_delete) derive their TTL backstop from it.
	MaxRuntimeSeconds         int64  `json:"max_runtime_seconds,omitempty"`
	SelectedOfferSnapshotID   string `json:"selected_offer_snapshot_id"`
	SelectedOfferConnectionID string `json:"selected_offer_connection_id"`
	SelectedOfferAdapterType  string `json:"selected_offer_adapter_type"`
	SelectedOfferNativeRef    string `json:"selected_offer_native_ref"`
	// SelectedOfferLane is the RECORDED reuse semantics of the selected offer.
	// The run lifecycle dispatches on this value, never on a lane re-read from
	// live offers, so a Run that landed on an enrolled node still reaches that
	// node after a restart or an offer catalog change.
	SelectedOfferLane domain.ExecutionLane `json:"selected_offer_lane,omitempty"`
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
	// Lane, NativeRef, RunID, and AttemptID come from the recorded launch
	// intent. A reusable-lane observation asks the node that holds the
	// container; an ephemeral one asks the provider.
	Lane      domain.ExecutionLane
	NativeRef string
	RunID     string
	AttemptID string
}

type ExternalObservation struct {
	ExternalID string        `json:"external_id"`
	LaunchKey  string        `json:"launch_key"`
	Phase      ExternalPhase `json:"phase"`
	ObservedAt time.Time     `json:"observed_at"`
	// StartedAt is when the workload's process actually began, as the thing
	// holding it reports the moment: a container runtime's own start time, or a
	// provider's where that provider publishes a moment about the process rather
	// than about the machine. It is not ObservedAt, which is when Mercator looked,
	// and it is not the moment the launch was accepted, which is when the machine
	// started getting ready. The whole point of predicting a start latency is that
	// it is calibrated against started minus accepted, and nothing could subtract
	// those until this field existed.
	//
	// It is a pointer because a holder that cannot say is common and must stay
	// distinguishable from one that says now. A provider whose API publishes no
	// start moment leaves it nil, and the record states the moment absent rather
	// than deriving one from acceptance.
	StartedAt  *time.Time `json:"started_at,omitempty"`
	ExitCode   *int       `json:"exit_code,omitempty"`
	NativeJSON string     `json:"native_json,omitempty"`
}

// EstablishedStart is the moment this observation establishes the workload began,
// and whether it establishes one at all. It is the single place a foreign moment
// becomes a moment Mercator will act on: the run stream's start and the Booking's
// runtime clock both read it, because a law with two adoption sites is a law one
// of them will be missing.
//
// An observation establishes a start when the holder said the work had begun and
// said when, by the moment Mercator read it. Two moments a holder can publish are
// not that.
//
// A moment later than the read that carried it is a clock Mercator does not share
// rather than anything it saw: a host running an hour ahead publishes a start an
// hour in the future, and Mercator recording it would file a start latency an hour
// too large as a measurement, and stamp a Booking's runtime clock an hour into
// Mercator's own future, where the bound it enforces expires an hour after the
// capacity was really spent. The comparison only means something because ObservedAt
// is Mercator's own clock on every seam that fills it in, which is what
// broker.observeOnNode had to be corrected to do: the node published both moments
// off its own clock, so the two agreed with each other and neither agreed with
// Mercator.
//
// A moment carried by a phase saying the work has not begun is the claim every
// provider in this tree makes from the moment it accepts a launch. What the phase
// cannot do is repair a moment that was never about the process: a provider that
// dates a pod when it places it publishes the same stale moment once the pod is
// running, so the field itself has to be absent where the holder cannot see the
// process, which is why the RunPod and Vast adapters publish none.
//
// The observation still carries what the holder said either way. This decides what
// Mercator adopts as the Run's own start, not what the provider is recorded saying.
func (o ExternalObservation) EstablishedStart() (time.Time, bool) {
	if o.StartedAt == nil || o.StartedAt.IsZero() || o.StartedAt.After(o.ObservedAt) {
		return time.Time{}, false
	}
	if o.Phase != ExternalPhaseRunning && !o.Phase.Exited() {
		return time.Time{}, false
	}
	return o.StartedAt.UTC(), true
}

type ReleaseRequest struct {
	WorkspaceID       string
	ConnectionID      string
	OperationKey      string
	RequestHash       string
	LaunchKey         string
	OwnershipToken    string
	LaunchRequestHash string
	// Lane, NativeRef, and RunID come from the recorded launch intent, so
	// releasing a container from a node Mercator keeps never turns into
	// destroying the machine.
	Lane      domain.ExecutionLane
	NativeRef string
	RunID     string
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
	ExternalID  string
	WorkspaceID string
	// ConnectionID names the connection the object was listed through.
	// Individual adapters may leave it empty; the Broker stamps it during
	// aggregation so callers (e.g. the janitor) can route Release/Terminate
	// back through the right connection.
	ConnectionID   string
	RunID          string
	AttemptID      string
	OwnershipToken string
	LaunchKey      string
	CleanupLocator string
	RequestHash    string
	Phase          ExternalPhase
}
