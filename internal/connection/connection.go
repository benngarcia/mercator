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
	eventlog.WorkspaceEventLog
	CreateCredential(context.Context, eventlog.AppendRequest, CredentialWrite) (eventlog.AppendResult, error)
	DeleteCredential(context.Context, eventlog.AppendRequest, CredentialRef) (eventlog.AppendResult, error)
}

type Service struct {
	log         eventlog.WorkspaceEventLog
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
	Credential          credential.Credential `json:"credential,omitzero"`
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

func New(log eventlog.WorkspaceEventLog) *Service {
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
	requestHashes, err := createRequestHashes(record)
	if err != nil {
		return Record{}, err
	}
	appendRequest := eventlog.AppendRequest{
		Stream:                connectionStream(req.WorkspaceID, req.ConnectionID),
		ExpectedStreamVersion: 0,
		CommandKey:            "connection:create:" + req.ConnectionID,
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
	for _, requestHash := range requestHashes {
		appendRequest.RequestHash = requestHash
		err = s.appendCreate(ctx, appendRequest, credentialWrite)
		if !errors.Is(err, eventlog.ErrIdempotencyConflict) {
			break
		}
	}
	if err != nil {
		return Record{}, err
	}
	return record, nil
}

func (s *Service) appendCreate(ctx context.Context, request eventlog.AppendRequest, credentialWrite CredentialWrite) error {
	if len(credentialWrite.Secret) == 0 {
		_, err := s.log.AppendIfWorkspaceActive(ctx, request)
		return err
	}
	_, err := s.credentials.CreateCredential(ctx, request, credentialWrite)
	return err
}

// createRequestHashes keeps the durable command hash independent of Record's
// presentation tags. v0.4.0 briefly hashed Record after its zero credential
// changed from `omitempty` to `omitzero`, so accept that transitional hash
// after trying the stable pre-v0.4 shape used for new commands.
func createRequestHashes(record Record) ([]string, error) {
	stable, err := domain.CanonicalHash(struct {
		ID                  string                `json:"id"`
		WorkspaceID         string                `json:"workspace_id"`
		AdapterType         string                `json:"adapter_type"`
		AuthorizationSchema map[string]string     `json:"authorization_schema,omitempty"`
		Authorized          bool                  `json:"authorized"`
		Config              map[string]string     `json:"config,omitempty"`
		Credential          credential.Credential `json:"credential"`
	}{
		ID:                  record.ID,
		WorkspaceID:         record.WorkspaceID,
		AdapterType:         record.AdapterType,
		AuthorizationSchema: record.AuthorizationSchema,
		Authorized:          record.Authorized,
		Config:              record.Config,
		Credential:          record.Credential,
	})
	if err != nil {
		return nil, err
	}
	transitional, err := domain.CanonicalHash(record)
	if err != nil {
		return nil, err
	}
	if stable == transitional {
		return []string{stable}, nil
	}
	return []string{stable, transitional}, nil
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
	history, err := eventlog.ReadFullStream(ctx, s.log, connectionStream(req.WorkspaceID, req.ConnectionID))
	if err != nil {
		return err
	}
	if len(history.Events) == 0 {
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
		ExpectedStreamVersion: history.LastVersion,
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
	history, err := eventlog.ReadFullStream(ctx, s.log, connectionStream(req.WorkspaceID, req.ConnectionID))
	if err != nil {
		return err
	}
	record, deleted, err := reduceConnection(history.Events)
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
		ExpectedStreamVersion: history.LastVersion,
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
	history, err := eventlog.ReadFullStream(ctx, s.log, connectionStream(workspaceID, connectionID))
	if err != nil {
		return Record{}, err
	}
	record, deleted, err := reduceConnection(history.Events)
	if err != nil {
		return Record{}, err
	}
	if deleted {
		return Record{}, ErrNotFound
	}
	return record, nil
}

func (s *Service) List(ctx context.Context, workspaceID string) ([]Record, error) {
	states := make(map[string]*connectionState)
	for event, err := range eventlog.ScanAll(ctx, s.log, eventlog.EventFilter{WorkspaceID: workspaceID, StreamTypes: []string{"connection"}}) {
		if err != nil {
			return nil, err
		}
		state := states[event.StreamID]
		if state == nil {
			state = &connectionState{}
			states[event.StreamID] = state
		}
		if err := state.apply(event); err != nil {
			return nil, err
		}
	}
	records := make([]Record, 0, len(states))
	for _, state := range states {
		record, deleted, err := state.result()
		if errors.Is(err, ErrNotFound) || deleted {
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

type connectionState struct {
	record  Record
	deleted bool
}

func (state *connectionState) apply(event eventlog.StoredEvent) error {
	switch event.Type {
	case EventConnectionCreated:
		if err := json.Unmarshal(event.Data, &state.record); err != nil {
			return err
		}
		state.record.CreatedBy = actorSubject(event.Actor)
	case EventConnectionAuthorizationUpdated:
		var data struct {
			Authorized bool `json:"authorized"`
		}
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return err
		}
		state.record.Authorized = data.Authorized
		if data.Authorized {
			state.record.AuthorizedBy = actorSubject(event.Actor)
		} else {
			state.record.AuthorizedBy = ""
		}
	case EventConnectionDeleted:
		state.deleted = true
	}
	return nil
}

func (state connectionState) result() (Record, bool, error) {
	if state.record.ID == "" {
		return Record{}, false, ErrNotFound
	}
	return state.record, state.deleted, nil
}

// reduceConnection folds a connection stream. deleted is reported separately
// so Delete can distinguish "already deleted" (idempotent no-op) from "never
// existed" (an error) — callers exposing reads map deleted to ErrNotFound.
func reduceConnection(events []eventlog.StoredEvent) (record Record, deleted bool, err error) {
	var state connectionState
	for _, event := range events {
		if err := state.apply(event); err != nil {
			return Record{}, false, err
		}
	}
	return state.result()
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
