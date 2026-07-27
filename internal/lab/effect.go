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
	// The six capacity operations are Mercator allocating and holding a machine,
	// which is a different contract from launching a workload on one. The four
	// provider operations above are an execution: a one-shot product Mercator
	// starts and cannot hold. These are the lease. Collapsing the two would file a
	// stop that suspends a machine and a release that ends a workload under one
	// name, and every rule about what Mercator owns reads the ledger.
	//
	// Resume is named for the act rather than for the method that performs it
	// (CapacityProvider.StartCapacity), because a capability set negotiates stop
	// and resume and a reader of the ledger is asking which promise was exercised.
	// Observe and the owned listing are reads: they allocate nothing, they change
	// nothing, and two of them answering differently is a machine whose state moved
	// rather than a command applied twice.
	OperationCapacityProvision = "capacity.provision"
	OperationCapacityObserve   = "capacity.observe"
	OperationCapacityStop      = "capacity.stop"
	OperationCapacityResume    = "capacity.resume"
	OperationCapacityTerminate = "capacity.terminate"
	OperationCapacityListOwned = "capacity.list_owned"
	// OperationNodeEnrolled is a node agent opening its authenticated session for
	// the first time and being admitted, which is the moment provisioned capacity
	// becomes capacity Mercator can execute on. It is the consequence of a
	// bootstrap rather than a command Mercator issued, and it is recorded here
	// because the ledger is the only account of what really happened on the
	// machine: Mercator's own record can say a node is ready, and only this says
	// which enrollment made it so, under which Rental generation, against a token
	// that may be redeemed once.
	OperationNodeEnrolled = "node.enrolled"
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
	// OperationCapacityPreempted is a provider taking back a machine, and the
	// executions that went with it. It is the one entry here Mercator did not
	// command: every other operation records a crossing from the control plane into
	// the world, and this records the world acting on its own account.
	//
	// It is in the ledger because the ledger is the only account of what really
	// happened to an execution. Mercator's own record can say a Run stopped; only
	// this says the machine was reclaimed and names the work that was interrupted by
	// it, which is what a rule about permission has to read.
	OperationCapacityPreempted = "capacity.preempted"
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
