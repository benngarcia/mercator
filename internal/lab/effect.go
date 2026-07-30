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
	OperationArtifactGet         = "artifact.get"
	OperationArtifactPut         = "artifact.put"
	OperationCacheMountWrite     = "cache_mount.write"
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
