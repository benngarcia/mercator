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
	// The three Artifact operations are three different facts, and collapsing
	// any two of them is how a local copy starts standing in for durable
	// content. OperationArtifactRead is a consuming launch resolving one input,
	// and says whether it read a verified local copy or fetched from the object
	// store. OperationArtifactReplicated is a verified copy landing on a host,
	// whether a Run wrote it or the object store served it.
	// OperationArtifactPublished is the durable copy reaching the object store,
	// which is the only thing that makes an Artifact consumable.
	OperationArtifactRead       = "artifact.read"
	OperationArtifactReplicated = "artifact.replicated"
	OperationArtifactPublished  = "artifact.published"
	OperationCacheMountWrite    = "cache_mount.write"
	OperationImagePull          = "image.pull"
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
