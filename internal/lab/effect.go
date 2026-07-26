package lab

import (
	"encoding/json"
	"time"
)

const (
	OperationProviderListOffers  = "provider.list_offers"
	OperationProviderLaunch      = "provider.launch"
	OperationProviderObserve     = "provider.observe"
	OperationProviderRelease     = "provider.release"
	OperationProviderTerminate   = "provider.terminate"
	OperationProviderListOwned   = "provider.list_owned"
	OperationControlPlaneRestart = "control_plane.restart"
	// The four Artifact operations are four different facts, and collapsing any
	// two of them is how a local copy starts standing in for durable content.
	// OperationArtifactRead is a consuming launch resolving one input, and says
	// whether it read a verified local copy or fetched from the object store.
	// OperationArtifactWritten is a workload having produced its output on the
	// host it ran on: bytes nothing of Mercator's fetched, hashed, or filed, which
	// is why it is not a replica and why no offer for that host says anything
	// about it. OperationArtifactReplicated is a verified copy landing on a host,
	// which only a fetch Mercator issued can leave. OperationArtifactPublished is
	// the durable copy reaching the object store, which is the only thing that
	// makes an Artifact consumable.
	OperationArtifactRead       = "artifact.read"
	OperationArtifactWritten    = "artifact.written"
	OperationArtifactReplicated = "artifact.replicated"
	OperationArtifactPublished  = "artifact.published"
	// OperationCacheMountAttach is a container being created with a cache
	// mounted, named by its full workspace-scoped identity. Opening the storage
	// and reading what is in it are one act rather than two, because that is what
	// a container runtime does and the whole of what it can report: it makes the
	// volume the mount point names if this tenant and generation had none, hands
	// the workload whatever is inside, and can say nothing about what the
	// application does with it afterwards. Recording the attachment is what makes
	// a cross-workspace touch of mutable state something the ledger can be caught
	// doing, whether or not the workload went on to write anything.
	OperationCacheMountAttach = "cache_mount.attach"
	// The three preparation operations are Mercator getting a machine ready for
	// work it has not admitted. They are separate from the pull a launch
	// dispatches because they are separate acts with separate justifications: a
	// launch fetches because a Run is starting here now, and a prefetch fetches
	// because a Run may start here later. Only one of them may ever be in the
	// way of the other, which is what the ledger has to be able to show.
	// OperationNodePrepareAbandoned is Mercator stopping one, which is the only
	// answer to speculative work whose reason went away.
	OperationNodePrepareImage     = "node.prepare_image"
	OperationNodePrepareArtifact  = "node.prepare_artifact"
	OperationNodePrepareAbandoned = "node.prepare_abandoned"
	OperationImagePull            = "image.pull"
	// OperationImageRetained is content a host kept, recorded when the bytes
	// landed. The pull is a command with a duration; retention is the fact that
	// outlives it, and only one of the two can explain what a host holds.
	OperationImageRetained = "image.retained"
)

type EffectCommand string

const (
	EffectCommandAccepted  EffectCommand = "accepted"
	EffectCommandRejected  EffectCommand = "rejected"
	EffectCommandDuplicate EffectCommand = "duplicate"
)

type EffectResponse string

const (
	EffectResponseDelivered EffectResponse = "delivered"
	EffectResponseLost      EffectResponse = "lost"
	EffectResponseDelayed   EffectResponse = "delayed"
	EffectResponseDuplicate EffectResponse = "duplicate"
)

// EffectRecord describes one command crossing from Mercator into the simulated
// external world and the consequence that happened there. Request contains a
// bounded, secret-free projection of the command.
type EffectRecord struct {
	ID            string          `json:"id"`
	Sequence      uint64          `json:"sequence"`
	At            time.Time       `json:"at"`
	Operation     string          `json:"operation"`
	OperationID   string          `json:"operation_id"`
	Command       EffectCommand   `json:"command"`
	Response      EffectResponse  `json:"response"`
	CorrelationID string          `json:"correlation_id"`
	CausationID   string          `json:"causation_id"`
	RequestHash   string          `json:"request_hash,omitempty"`
	Request       json.RawMessage `json:"request"`
	Consequence   json.RawMessage `json:"consequence"`
	FaultID       string          `json:"fault_id,omitempty"`
}
