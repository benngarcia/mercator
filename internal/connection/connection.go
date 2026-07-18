package connection

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"sort"
	"time"

	"github.com/benngarcia/mercator/internal/credential"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
)

const (
	EventConnectionCreated              = "compute.connection.created.v1"
	EventConnectionAuthorizationUpdated = "compute.connection.authorization_updated.v1"
)

type Service struct {
	log eventlog.EventLog
	now func() time.Time
}

type Record struct {
	ID                  string                `json:"id"`
	WorkspaceID         string                `json:"workspace_id"`
	AdapterType         string                `json:"adapter_type"`
	AuthorizationSchema map[string]string     `json:"authorization_schema,omitempty"`
	Authorized          bool                  `json:"authorized"`
	Config              map[string]string     `json:"config,omitempty"`
	Credential          credential.Credential `json:"credential,omitempty"`
	// CreatedBy and AuthorizedBy are the audited principals of the create and
	// authorize commands, derived from the event envelopes at read time (never
	// part of the stored event data or the idempotency hash).
	CreatedBy    string `json:"created_by,omitempty"`
	AuthorizedBy string `json:"authorized_by,omitempty"`
}

type CreateRequest struct {
	WorkspaceID         string
	ConnectionID        string
	AdapterType         string
	AuthorizationSchema map[string]string
	Config              map[string]string
	Credential          credential.Credential
	// Actor is the event-envelope principal recorded on the created fact.
	Actor json.RawMessage
}

type UpdateAuthorizationRequest struct {
	WorkspaceID  string
	ConnectionID string
	Authorized   bool
	// Actor is the event-envelope principal recorded on the authorization fact.
	Actor json.RawMessage
}

func New(log eventlog.EventLog) *Service {
	return &Service{log: log, now: time.Now}
}

func (s *Service) Create(ctx context.Context, req CreateRequest) (Record, error) {
	if req.WorkspaceID == "" || req.ConnectionID == "" || req.AdapterType == "" {
		return Record{}, fmt.Errorf("connection: workspace_id, connection_id, and adapter_type are required")
	}
	record := Record{
		ID:                  req.ConnectionID,
		WorkspaceID:         req.WorkspaceID,
		AdapterType:         req.AdapterType,
		AuthorizationSchema: maps.Clone(req.AuthorizationSchema),
		Config:              maps.Clone(req.Config),
		Credential:          req.Credential,
	}
	data, err := json.Marshal(record)
	if err != nil {
		return Record{}, err
	}
	hash, err := domain.CanonicalHash(record)
	if err != nil {
		return Record{}, err
	}
	_, err = s.log.Append(ctx, eventlog.AppendRequest{
		Stream:                connectionStream(req.WorkspaceID, req.ConnectionID),
		ExpectedStreamVersion: 0,
		CommandKey:            "connection:create:" + req.ConnectionID,
		RequestHash:           hash,
		Actor:                 req.Actor,
		CorrelationID:         req.ConnectionID,
		CausationID:           "connection:create:" + req.ConnectionID,
		Events: []eventlog.NewEvent{{
			ID:            fmt.Sprintf("evt_connection_%s_%s_created", req.WorkspaceID, req.ConnectionID),
			Type:          EventConnectionCreated,
			SchemaVersion: 1,
			OccurredAt:    s.now().UTC(),
			Visibility:    eventlog.VisibilityPublic,
			Data:          data,
		}},
	})
	if err != nil {
		return Record{}, err
	}
	return record, nil
}

func (s *Service) UpdateAuthorization(ctx context.Context, req UpdateAuthorizationRequest) error {
	events, err := s.log.ReadStream(ctx, connectionStream(req.WorkspaceID, req.ConnectionID), 0, 1000)
	if err != nil {
		return err
	}
	if len(events) == 0 {
		return fmt.Errorf("connection: not found")
	}
	data, err := json.Marshal(map[string]any{"authorized": req.Authorized})
	if err != nil {
		return err
	}
	// The idempotency hash covers WHAT was commanded, not WHO commanded it: the
	// same authorization from a different principal (e.g. console after boot
	// bootstrap) must replay, not conflict. The anonymous struct keeps the hash
	// byte-identical to logs written before the Actor field existed.
	hash, err := domain.CanonicalHash(struct {
		WorkspaceID  string
		ConnectionID string
		Authorized   bool
	}{req.WorkspaceID, req.ConnectionID, req.Authorized})
	if err != nil {
		return err
	}
	_, err = s.log.Append(ctx, eventlog.AppendRequest{
		Stream:                connectionStream(req.WorkspaceID, req.ConnectionID),
		ExpectedStreamVersion: uint64(len(events)),
		CommandKey:            fmt.Sprintf("connection:authorization:%s:%t", req.ConnectionID, req.Authorized),
		RequestHash:           hash,
		Actor:                 req.Actor,
		CorrelationID:         req.ConnectionID,
		CausationID:           "connection:authorization:" + req.ConnectionID,
		Events: []eventlog.NewEvent{{
			ID:            fmt.Sprintf("evt_connection_%s_%s_authorized_%t", req.WorkspaceID, req.ConnectionID, req.Authorized),
			Type:          EventConnectionAuthorizationUpdated,
			SchemaVersion: 1,
			OccurredAt:    s.now().UTC(),
			Visibility:    eventlog.VisibilityPublic,
			Data:          data,
		}},
	})
	return err
}

func (s *Service) Get(ctx context.Context, workspaceID, connectionID string) (Record, error) {
	events, err := s.log.ReadStream(ctx, connectionStream(workspaceID, connectionID), 0, 1000)
	if err != nil {
		return Record{}, err
	}
	return reduceConnection(events)
}

func (s *Service) List(ctx context.Context, workspaceID string) ([]Record, error) {
	events, err := s.log.ReadAll(ctx, 0, 1000, eventlog.EventFilter{WorkspaceID: workspaceID, StreamTypes: []string{"connection"}, EventTypes: []string{EventConnectionCreated}})
	if err != nil {
		return nil, err
	}
	records := make([]Record, 0, len(events))
	for _, event := range events {
		record, err := s.Get(ctx, workspaceID, event.StreamID)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].ID < records[j].ID })
	return records, nil
}

func reduceConnection(events []eventlog.StoredEvent) (Record, error) {
	var record Record
	for _, event := range events {
		switch event.Type {
		case EventConnectionCreated:
			if err := json.Unmarshal(event.Data, &record); err != nil {
				return Record{}, err
			}
			record.CreatedBy = actorSubject(event.Actor)
		case EventConnectionAuthorizationUpdated:
			var data struct {
				Authorized bool `json:"authorized"`
			}
			if err := json.Unmarshal(event.Data, &data); err != nil {
				return Record{}, err
			}
			record.Authorized = data.Authorized
			if data.Authorized {
				record.AuthorizedBy = actorSubject(event.Actor)
			} else {
				record.AuthorizedBy = ""
			}
		}
	}
	if record.ID == "" {
		return Record{}, fmt.Errorf("connection: not found")
	}
	return record, nil
}

// actorSubject extracts the audited subject from an event envelope's actor
// ({"subject": ...}). Empty when the event was recorded without a principal.
func actorSubject(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var actor struct {
		Subject string `json:"subject"`
	}
	if err := json.Unmarshal(raw, &actor); err != nil {
		return ""
	}
	return actor.Subject
}

func connectionStream(workspaceID, connectionID string) eventlog.StreamKey {
	return eventlog.StreamKey{WorkspaceID: workspaceID, Type: "connection", ID: connectionID}
}
