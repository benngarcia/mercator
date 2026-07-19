package connection

import (
	"context"
	"encoding/json"
	"errors"
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
	EventConnectionDeleted              = "compute.connection.deleted.v1"
)

var ErrNotFound = fmt.Errorf("connection: not found")

var ErrSecretStoreDisabled = errors.New("connection: secret store disabled")
var ErrSecretStore = errors.New("connection: secret store failure")

type CredentialWrite struct {
	WorkspaceID  string
	ConnectionID string
	Secret       []byte
}

type CredentialRef struct {
	WorkspaceID  string
	ConnectionID string
}

type CredentialRepository interface {
	eventlog.EventLog
	CreateCredential(context.Context, eventlog.AppendRequest, CredentialWrite) (eventlog.AppendResult, error)
	DeleteCredential(context.Context, eventlog.AppendRequest, CredentialRef) (eventlog.AppendResult, error)
}

type Service struct {
	log         eventlog.EventLog
	credentials CredentialRepository
	now         func() time.Time
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
	// Secret is write-only credential material. It is sealed before storage and
	// never enters the connection record or event log.
	Secret []byte
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

type DeleteRequest struct {
	WorkspaceID  string
	ConnectionID string
	// Actor is the event-envelope principal recorded on the deleted fact.
	Actor json.RawMessage
}

func New(log eventlog.EventLog) *Service {
	return &Service{log: log, now: time.Now}
}

func NewWithCredentials(repository CredentialRepository) *Service {
	return &Service{log: repository, credentials: repository, now: time.Now}
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
	credentialWrite, err := s.prepareCredential(&record, req.Secret)
	if err != nil {
		return Record{}, err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return Record{}, err
	}
	hash, err := domain.CanonicalHash(record)
	if err != nil {
		return Record{}, err
	}
	appendRequest := eventlog.AppendRequest{
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
	}
	if len(credentialWrite.Secret) == 0 {
		_, err = s.log.Append(ctx, appendRequest)
	} else {
		_, err = s.credentials.CreateCredential(ctx, appendRequest, credentialWrite)
	}
	if err != nil {
		return Record{}, err
	}
	return record, nil
}

func (s *Service) prepareCredential(record *Record, secret []byte) (CredentialWrite, error) {
	if record.Credential.Source != credential.SourceMercator || len(secret) == 0 {
		return CredentialWrite{}, nil
	}
	if s.credentials == nil {
		return CredentialWrite{}, fmt.Errorf("%w: configure MERCATOR_SECRET_KEY and transactional SQLite credential storage", ErrSecretStoreDisabled)
	}
	record.Credential.Ref = record.ID
	return CredentialWrite{
		WorkspaceID:  record.WorkspaceID,
		ConnectionID: record.ID,
		Secret:       secret,
	}, nil
}

func credentialRef(record Record) (CredentialRef, bool) {
	if record.Credential.Source != credential.SourceMercator || record.Credential.Ref == "" {
		return CredentialRef{}, false
	}
	return CredentialRef{WorkspaceID: record.WorkspaceID, ConnectionID: record.ID}, true
}

func (s *Service) UpdateAuthorization(ctx context.Context, req UpdateAuthorizationRequest) error {
	events, err := s.log.ReadStream(ctx, connectionStream(req.WorkspaceID, req.ConnectionID), 0, 1000)
	if err != nil {
		return err
	}
	if len(events) == 0 {
		return ErrNotFound
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
	appendRequest := eventlog.AppendRequest{
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
	}
	_, err = s.log.Append(ctx, appendRequest)
	return err
}

// Delete appends the deleted fact. The stream (and its events) remain in the
// log; Get and List treat a deleted connection as gone. Deleting twice is an
// idempotent replay. A deleted connection's id cannot be reused: recreate
// under a fresh id.
func (s *Service) Delete(ctx context.Context, req DeleteRequest) error {
	events, err := s.log.ReadStream(ctx, connectionStream(req.WorkspaceID, req.ConnectionID), 0, 1000)
	if err != nil {
		return err
	}
	record, deleted, err := reduceConnection(events)
	if err != nil {
		return err
	}
	if deleted {
		return nil
	}
	hash, err := domain.CanonicalHash(struct {
		WorkspaceID  string
		ConnectionID string
	}{req.WorkspaceID, req.ConnectionID})
	if err != nil {
		return err
	}
	appendRequest := eventlog.AppendRequest{
		Stream:                connectionStream(req.WorkspaceID, req.ConnectionID),
		ExpectedStreamVersion: uint64(len(events)),
		CommandKey:            "connection:delete:" + req.ConnectionID,
		RequestHash:           hash,
		Actor:                 req.Actor,
		CorrelationID:         req.ConnectionID,
		CausationID:           "connection:delete:" + req.ConnectionID,
		Events: []eventlog.NewEvent{{
			ID:            fmt.Sprintf("evt_connection_%s_%s_deleted", req.WorkspaceID, req.ConnectionID),
			Type:          EventConnectionDeleted,
			SchemaVersion: 1,
			OccurredAt:    s.now().UTC(),
			Visibility:    eventlog.VisibilityPublic,
			Data:          json.RawMessage(`{"deleted":true}`),
		}},
	}
	if ref, ok := credentialRef(record); ok {
		if s.credentials == nil {
			return ErrSecretStoreDisabled
		}
		_, err = s.credentials.DeleteCredential(ctx, appendRequest, ref)
	} else {
		_, err = s.log.Append(ctx, appendRequest)
	}
	return err
}

func (s *Service) Get(ctx context.Context, workspaceID, connectionID string) (Record, error) {
	events, err := s.log.ReadStream(ctx, connectionStream(workspaceID, connectionID), 0, 1000)
	if err != nil {
		return Record{}, err
	}
	record, deleted, err := reduceConnection(events)
	if err != nil {
		return Record{}, err
	}
	if deleted {
		return Record{}, ErrNotFound
	}
	return record, nil
}

func (s *Service) List(ctx context.Context, workspaceID string) ([]Record, error) {
	events, err := s.log.ReadAll(ctx, 0, 1000, eventlog.EventFilter{WorkspaceID: workspaceID, StreamTypes: []string{"connection"}, EventTypes: []string{EventConnectionCreated}})
	if err != nil {
		return nil, err
	}
	records := make([]Record, 0, len(events))
	for _, event := range events {
		record, err := s.Get(ctx, workspaceID, event.StreamID)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].ID < records[j].ID })
	return records, nil
}

// reduceConnection folds a connection stream. deleted is reported separately
// so Delete can distinguish "already deleted" (idempotent no-op) from "never
// existed" (an error) — callers exposing reads map deleted to ErrNotFound.
func reduceConnection(events []eventlog.StoredEvent) (record Record, deleted bool, err error) {
	for _, event := range events {
		switch event.Type {
		case EventConnectionCreated:
			if err := json.Unmarshal(event.Data, &record); err != nil {
				return Record{}, false, err
			}
			record.CreatedBy = actorSubject(event.Actor)
		case EventConnectionAuthorizationUpdated:
			var data struct {
				Authorized bool `json:"authorized"`
			}
			if err := json.Unmarshal(event.Data, &data); err != nil {
				return Record{}, false, err
			}
			record.Authorized = data.Authorized
			if data.Authorized {
				record.AuthorizedBy = actorSubject(event.Actor)
			} else {
				record.AuthorizedBy = ""
			}
		case EventConnectionDeleted:
			deleted = true
		}
	}
	if record.ID == "" {
		return Record{}, false, ErrNotFound
	}
	return record, deleted, nil
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
