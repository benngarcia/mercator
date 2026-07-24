package capability

import (
	"context"
	"fmt"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
)

// CapacityProvider allocates and holds machine capacity. It knows nothing
// about workloads: what runs on the capacity is the NodeRuntime's business.
// Splitting the two is what lets Mercator keep a machine across Runs without
// the provider knowing, and lets a provider stop or resume a machine without
// the control plane guessing what that does to a running workload.
type CapacityProvider interface {
	// CapacitySupport reports what this provider can actually do. Callers
	// negotiate against it instead of assuming stop, resume, persistent disk,
	// spot behavior, or exact pricing.
	CapacitySupport() CapacitySupport
	// Verify performs a cheap credential and reachability check. It allocates
	// nothing.
	Verify(ctx context.Context) error
	ListCapacity(ctx context.Context, query CapacityQuery) ([]domain.OfferSnapshot, error)
	Provision(ctx context.Context, command ProvisionCommand) (CapacityReceipt, error)
	ObserveCapacity(ctx context.Context, ref CapacityRef) (CapacityObservation, error)
	// StartCapacity resumes stopped capacity. Providers without SupportsResume
	// return ErrCapabilityUnsupported.
	StartCapacity(ctx context.Context, command CapacityCommand) (CapacityReceipt, error)
	// StopCapacity suspends capacity while retaining its identity and, where
	// SupportsPersistentDisk holds, its disk. Providers without SupportsStop
	// return ErrCapabilityUnsupported.
	StopCapacity(ctx context.Context, command CapacityCommand) (CapacityReceipt, error)
	TerminateCapacity(ctx context.Context, command CapacityCommand) (CapacityReceipt, error)
	// ListOwnedCapacity enumerates capacity this connection allocated, so
	// reconciliation can adopt or terminate resources the control plane lost
	// track of.
	ListOwnedCapacity(ctx context.Context, query OwnershipQuery) ([]OwnedCapacity, error)
}

// ErrCapabilityUnsupported is returned when a caller invokes an operation the
// provider's CapacitySupport does not claim. It is a routing bug, never a
// runtime condition to retry, so providers surface it explicitly rather than
// silently succeeding.
var ErrCapabilityUnsupported = fmt.Errorf("capability: operation unsupported by this backend")

// CapacitySupport is one provider's negotiated capability set. Every field is
// a claim the scheduler and reconciler are entitled to rely on.
type CapacitySupport struct {
	// Stop and Resume report whether capacity can be suspended and brought
	// back under the same identity. A provider with Stop but not Resume can
	// only shed cost by destroying capacity.
	Stop   bool `json:"stop"`
	Resume bool `json:"resume"`
	// PersistentDisk reports whether a stopped machine keeps its disk, which
	// is what makes a resumed machine still warm.
	PersistentDisk bool `json:"persistent_disk"`
	// Spot reports whether this provider can allocate interruptible capacity.
	Spot bool `json:"spot"`
	// ExactPricing reports whether the provider states a rate Mercator can
	// bill against, rather than an estimate.
	ExactPricing bool `json:"exact_pricing"`
	// IdempotentProvision names the mechanism the provider honors for repeated
	// provision commands: "operation_key" when the provider deduplicates on a
	// caller-supplied key, "none" when the caller must reconcile by listing
	// owned capacity.
	IdempotentProvision string `json:"idempotent_provision"`
	ListOwned           bool   `json:"list_owned"`
	// ObserveAfterTerminate reports whether a terminated resource remains
	// observable. When false, a not-found observation after terminate is
	// confirmation rather than ambiguity.
	ObserveAfterTerminate bool `json:"observe_after_terminate"`
}

// CapacityQuery scopes a capacity listing to one workspace and requirement.
type CapacityQuery struct {
	WorkspaceID string
	Resources   domain.ResourceRequirements
}

// CapacityRef names allocated capacity well enough to observe it after a
// control-plane restart, without consulting any in-memory state.
type CapacityRef struct {
	WorkspaceID  string
	ConnectionID string
	RentalID     string
	// NativeRef is the provider's own identifier for the machine.
	NativeRef string
	// OwnershipToken proves the capacity belongs to this workspace, so a
	// reconciler never acts on a machine it merely resembles.
	OwnershipToken string
}

// CapacityCommand mutates allocated capacity. OperationKey makes the command
// replayable: the same key must produce the same effect exactly once, and a
// repeat reports Duplicate rather than acting twice.
type CapacityCommand struct {
	CapacityRef
	OperationKey string
	RequestHash  string
	// Generation is the fencing token for this Rental's lifecycle cycle. A
	// provider ignores commands stamped with a superseded generation.
	Generation uint64
}

// ProvisionCommand allocates fresh capacity for one Rental.
type ProvisionCommand struct {
	WorkspaceID  string
	ConnectionID string
	OperationKey string
	RequestHash  string
	// RentalID is the identity Mercator assigns before the provider answers,
	// so an accepted-but-lost response is still reconcilable.
	RentalID        string
	Generation      uint64
	OwnershipToken  string
	OfferSnapshotID string
	NativeRef       string
	Resources       domain.ResourceRequirements
	// Bootstrap carries what the machine needs to enroll its node agent with
	// the control plane. A CapacityProvider delivers it verbatim through
	// whatever mechanism it has (startup script, user data, baked image
	// configuration) and never interprets it.
	Bootstrap NodeBootstrap
	// Interruptible requests spot or preemptible capacity. Only honored when
	// CapacitySupport.Spot holds.
	Interruptible bool
	// MaxLifetimeSeconds is a provider-side reclamation backstop for providers
	// that support one, so lost capacity cannot bill forever.
	MaxLifetimeSeconds int64
}

// NodeBootstrap is the material a fresh machine needs to reach the control
// plane and prove which Rental generation it is. It never carries a long-lived
// credential: the enrollment token is short-lived and single-purpose.
type NodeBootstrap struct {
	ControlPlaneURL string `json:"control_plane_url"`
	NodeID          string `json:"node_id"`
	RentalID        string `json:"rental_id"`
	Generation      uint64 `json:"generation"`
	// EnrollmentToken is short-lived and redeemable once, for this node
	// identity only.
	EnrollmentToken string `json:"enrollment_token"`
	// AgentVersion pins the node agent build the machine should run.
	AgentVersion string `json:"agent_version"`
}

// CapacityState is the provider's own view of one machine's lifecycle.
type CapacityState string

const (
	CapacityStateRequested  CapacityState = "requested"
	CapacityStateStarting   CapacityState = "starting"
	CapacityStateActive     CapacityState = "active"
	CapacityStateStopping   CapacityState = "stopping"
	CapacityStateStopped    CapacityState = "stopped"
	CapacityStateTerminated CapacityState = "terminated"
	// CapacityStateUnknown is an honest answer, not a failure: the provider
	// answered without a state Mercator recognizes, or did not answer at all.
	CapacityStateUnknown CapacityState = "unknown"
)

func (state CapacityState) Valid() bool {
	switch state {
	case CapacityStateRequested,
		CapacityStateStarting,
		CapacityStateActive,
		CapacityStateStopping,
		CapacityStateStopped,
		CapacityStateTerminated,
		CapacityStateUnknown:
		return true
	default:
		return false
	}
}

// Terminal reports whether no further provider-side transition is expected.
func (state CapacityState) Terminal() bool { return state == CapacityStateTerminated }

type CapacityReceipt struct {
	NativeRef  string        `json:"native_ref"`
	State      CapacityState `json:"state"`
	AcceptedAt time.Time     `json:"accepted_at"`
	// Duplicate reports that this OperationKey had already been accepted, so
	// the caller must not count the effect twice.
	Duplicate bool `json:"duplicate"`
	// Interruptible reports what the provider actually allocated, which may
	// differ from what was requested.
	Interruptible bool              `json:"interruptible"`
	Pricing       domain.PriceModel `json:"pricing"`
}

type CapacityObservation struct {
	NativeRef  string        `json:"native_ref"`
	State      CapacityState `json:"state"`
	ObservedAt time.Time     `json:"observed_at"`
	// Endpoint is where the machine can be reached, when the provider exposes
	// one. It is provenance for operators, never a control channel: Mercator
	// reaches nodes only through their outbound agent session.
	Endpoint string `json:"endpoint,omitempty"`
	// Interrupted reports provider-initiated reclamation of interruptible
	// capacity, which is a different fact from a machine an operator stopped.
	Interrupted bool   `json:"interrupted,omitempty"`
	NativeJSON  string `json:"native_json,omitempty"`
}

// OwnershipQuery scopes an owned-capacity listing.
type OwnershipQuery struct {
	WorkspaceID string
}

// OwnedCapacity is one machine the provider says belongs to this connection.
// Reconciliation compares it against known Rentals to find orphans.
type OwnedCapacity struct {
	NativeRef      string        `json:"native_ref"`
	ConnectionID   string        `json:"connection_id"`
	WorkspaceID    string        `json:"workspace_id"`
	RentalID       string        `json:"rental_id,omitempty"`
	Generation     uint64        `json:"generation,omitempty"`
	OwnershipToken string        `json:"ownership_token,omitempty"`
	State          CapacityState `json:"state"`
	CreatedAt      time.Time     `json:"created_at"`
}
