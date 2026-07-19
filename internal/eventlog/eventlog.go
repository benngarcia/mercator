package eventlog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type GlobalPosition uint64

type Visibility string

const (
	VisibilityPublic  Visibility = "public"
	VisibilityPrivate Visibility = "private"
)

var (
	ErrConcurrencyConflict = errors.New("eventlog: concurrency conflict")
	ErrIdempotencyConflict = errors.New("eventlog: idempotency conflict")
)

type StreamKey struct {
	WorkspaceID string
	Type        string
	ID          string
}

func (s StreamKey) validate() error {
	if s.WorkspaceID == "" || s.Type == "" || s.ID == "" {
		return fmt.Errorf("eventlog: stream workspace_id, type, and id are required")
	}
	return nil
}

type AppendRequest struct {
	Stream                StreamKey
	ExpectedStreamVersion uint64
	CommandKey            string
	RequestHash           string
	Actor                 json.RawMessage
	CorrelationID         string
	CausationID           string
	Events                []NewEvent
}

type NewEvent struct {
	ID            string
	Type          string
	SchemaVersion int
	OccurredAt    time.Time
	Visibility    Visibility
	Data          json.RawMessage
	PrivateData   []byte
}

type AppendResult struct {
	FirstPosition     GlobalPosition
	LastPosition      GlobalPosition
	NextStreamVersion uint64
	Duplicate         bool
	Events            []StoredEvent
}

type StoredEvent struct {
	GlobalPosition GlobalPosition
	ID             string
	WorkspaceID    string
	StreamType     string
	StreamID       string
	StreamVersion  uint64
	Type           string
	SchemaVersion  int
	OccurredAt     time.Time
	Actor          json.RawMessage
	CorrelationID  string
	CausationID    string
	CommandKey     string
	RequestHash    string
	Visibility     Visibility
	Data           json.RawMessage
	PrivateData    []byte
}

func (e StoredEvent) Stream() StreamKey {
	return StreamKey{WorkspaceID: e.WorkspaceID, Type: e.StreamType, ID: e.StreamID}
}

func (e StoredEvent) CloudEvent() CloudEvent {
	return CloudEvent{
		SpecVersion:    "1.0",
		ID:             e.ID,
		Source:         "compute-control-plane/workspaces/" + e.WorkspaceID,
		Type:           e.Type,
		Subject:        e.StreamType + "s/" + e.StreamID,
		Time:           e.OccurredAt.UTC().Format(time.RFC3339Nano),
		WorkspaceID:    e.WorkspaceID,
		StreamVersion:  e.StreamVersion,
		GlobalPosition: e.GlobalPosition,
		CorrelationID:  e.CorrelationID,
		CausationID:    e.CausationID,
		Data:           e.Data,
	}
}

type CloudEvent struct {
	SpecVersion    string          `json:"specversion"`
	ID             string          `json:"id"`
	Source         string          `json:"source"`
	Type           string          `json:"type"`
	Subject        string          `json:"subject"`
	Time           string          `json:"time"`
	WorkspaceID    string          `json:"workspaceid"`
	StreamVersion  uint64          `json:"streamversion"`
	GlobalPosition GlobalPosition  `json:"globalposition"`
	CorrelationID  string          `json:"correlationid,omitempty"`
	CausationID    string          `json:"causationid,omitempty"`
	Data           json.RawMessage `json:"data"`
}

type EventFilter struct {
	WorkspaceID string
	StreamTypes []string
	EventTypes  []string
}

type SubscriptionRequest struct {
	SubscriptionID string
	After          GlobalPosition
	Filter         EventFilter
}

type Delivery struct {
	SubscriptionID string
	Event          StoredEvent
}

type EventLog interface {
	Append(ctx context.Context, req AppendRequest) (AppendResult, error)
	ReadStream(ctx context.Context, stream StreamKey, afterVersion uint64, limit int) ([]StoredEvent, error)
	ReadAll(ctx context.Context, after GlobalPosition, limit int, filter EventFilter) ([]StoredEvent, error)
	ListWorkspaceIDs(ctx context.Context, filter EventFilter) ([]string, error)
	Offset(ctx context.Context, subscriptionID string) (GlobalPosition, bool, error)
	Subscribe(ctx context.Context, req SubscriptionRequest) (<-chan Delivery, error)
	Ack(ctx context.Context, subscriptionID string, position GlobalPosition) error
}
