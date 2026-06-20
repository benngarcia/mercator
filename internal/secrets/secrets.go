package secrets

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/bengarcia/mercator/internal/domain"
	"github.com/bengarcia/mercator/internal/eventlog"
)

const (
	EventSecretVersionCreated = "compute.secret.version_created.v1"
	EventSecretGrantCreated   = "compute.secret.grant_created.v1"
	EventSecretGrantRevoked   = "compute.secret.grant_revoked.v1"
)

type Vault struct {
	log eventlog.EventLog
	key []byte
	now func() time.Time
}

type CreateVersionRequest struct {
	WorkspaceID string
	SecretID    string
	Plaintext   []byte
}

type SecretVersion struct {
	SecretID   string `json:"secret_id"`
	Version    int    `json:"version"`
	Ciphertext []byte `json:"ciphertext,omitempty"`
	Nonce      []byte `json:"nonce,omitempty"`
}

type SecretMetadata struct {
	SecretID string `json:"secret_id"`
	Version  int    `json:"version"`
}

type GrantRequest struct {
	WorkspaceID string
	SecretID    string
	Version     int
	ScopeType   string
	ScopeID     string
}

type RevokeRequest struct {
	WorkspaceID string
	GrantID     string
}

type Grant struct {
	ID        string `json:"id"`
	SecretID  string `json:"secret_id"`
	Version   int    `json:"version"`
	ScopeType string `json:"scope_type"`
	ScopeID   string `json:"scope_id"`
	Revoked   bool   `json:"revoked"`
}

type versionPublicData struct {
	SecretID string `json:"secret_id"`
	Version  int    `json:"version"`
}

func New(log eventlog.EventLog, key []byte) *Vault {
	cloned := append([]byte(nil), key...)
	return &Vault{log: log, key: cloned, now: time.Now}
}

func (v *Vault) CreateVersion(ctx context.Context, req CreateVersionRequest) (SecretVersion, error) {
	if req.WorkspaceID == "" || req.SecretID == "" || len(req.Plaintext) == 0 {
		return SecretVersion{}, fmt.Errorf("secrets: workspace_id, secret_id, and plaintext are required")
	}
	existing, err := v.log.ReadStream(ctx, secretStream(req.WorkspaceID, req.SecretID), 0, 1000)
	if err != nil {
		return SecretVersion{}, err
	}
	version := 1
	for _, event := range existing {
		if event.Type == EventSecretVersionCreated {
			version++
		}
	}
	ciphertext, nonce, err := encrypt(v.key, req.Plaintext)
	if err != nil {
		return SecretVersion{}, err
	}
	secretVersion := SecretVersion{SecretID: req.SecretID, Version: version, Ciphertext: ciphertext, Nonce: nonce}
	publicData, err := json.Marshal(versionPublicData{SecretID: req.SecretID, Version: version})
	if err != nil {
		return SecretVersion{}, err
	}
	privateData, err := json.Marshal(secretVersion)
	if err != nil {
		return SecretVersion{}, err
	}
	hash, err := domain.CanonicalHash(versionPublicData{SecretID: req.SecretID, Version: version})
	if err != nil {
		return SecretVersion{}, err
	}
	_, err = v.log.Append(ctx, eventlog.AppendRequest{
		Stream:                secretStream(req.WorkspaceID, req.SecretID),
		ExpectedStreamVersion: uint64(len(existing)),
		CommandKey:            fmt.Sprintf("secret:version:%s:%d", req.SecretID, version),
		RequestHash:           hash,
		CorrelationID:         req.SecretID,
		CausationID:           fmt.Sprintf("secret:version:%s:%d", req.SecretID, version),
		Events: []eventlog.NewEvent{{
			ID:            fmt.Sprintf("evt_secret_%s_v%d_created", req.SecretID, version),
			Type:          EventSecretVersionCreated,
			SchemaVersion: 1,
			OccurredAt:    v.now().UTC(),
			Visibility:    eventlog.VisibilityPublic,
			Data:          publicData,
			PrivateData:   privateData,
		}},
	})
	if err != nil {
		return SecretVersion{}, err
	}
	return secretVersion, nil
}

func (v *Vault) ListMetadata(ctx context.Context, workspaceID string) ([]SecretMetadata, error) {
	events, err := v.log.ReadAll(ctx, 0, 1000, eventlog.EventFilter{WorkspaceID: workspaceID, StreamTypes: []string{"secret"}, EventTypes: []string{EventSecretVersionCreated}})
	if err != nil {
		return nil, err
	}
	latest := map[string]int{}
	for _, event := range events {
		var data versionPublicData
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return nil, err
		}
		if data.Version > latest[data.SecretID] {
			latest[data.SecretID] = data.Version
		}
	}
	out := make([]SecretMetadata, 0, len(latest))
	for secretID, version := range latest {
		out = append(out, SecretMetadata{SecretID: secretID, Version: version})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SecretID < out[j].SecretID })
	return out, nil
}

func (v *Vault) Grant(ctx context.Context, req GrantRequest) (Grant, error) {
	if req.WorkspaceID == "" || req.SecretID == "" || req.Version <= 0 || req.ScopeType == "" || req.ScopeID == "" {
		return Grant{}, fmt.Errorf("secrets: workspace, secret, version, and scope are required")
	}
	grant := Grant{ID: grantID(req), SecretID: req.SecretID, Version: req.Version, ScopeType: req.ScopeType, ScopeID: req.ScopeID}
	return grant, v.appendGrantEvent(ctx, req.WorkspaceID, grant, EventSecretGrantCreated)
}

func (v *Vault) Revoke(ctx context.Context, req RevokeRequest) error {
	grants, err := v.ListGrants(ctx, req.WorkspaceID)
	if err != nil {
		return err
	}
	for _, grant := range grants {
		if grant.ID == req.GrantID {
			grant.Revoked = true
			return v.appendGrantEvent(ctx, req.WorkspaceID, grant, EventSecretGrantRevoked)
		}
	}
	return fmt.Errorf("secrets: grant not found")
}

func (v *Vault) ListGrants(ctx context.Context, workspaceID string) ([]Grant, error) {
	events, err := v.log.ReadAll(ctx, 0, 1000, eventlog.EventFilter{WorkspaceID: workspaceID, StreamTypes: []string{"secret-grant"}})
	if err != nil {
		return nil, err
	}
	grants := map[string]Grant{}
	for _, event := range events {
		var grant Grant
		if err := json.Unmarshal(event.Data, &grant); err != nil {
			return nil, err
		}
		grants[grant.ID] = grant
	}
	out := make([]Grant, 0, len(grants))
	for _, grant := range grants {
		out = append(out, grant)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (v *Vault) appendGrantEvent(ctx context.Context, workspaceID string, grant Grant, eventType string) error {
	data, err := json.Marshal(grant)
	if err != nil {
		return err
	}
	hash, err := domain.CanonicalHash(grant)
	if err != nil {
		return err
	}
	stream := eventlog.StreamKey{WorkspaceID: workspaceID, Type: "secret-grant", ID: grant.ID}
	existing, err := v.log.ReadStream(ctx, stream, 0, 1000)
	if err != nil {
		return err
	}
	_, err = v.log.Append(ctx, eventlog.AppendRequest{
		Stream:                stream,
		ExpectedStreamVersion: uint64(len(existing)),
		CommandKey:            "secret:grant:" + grant.ID + ":" + eventType,
		RequestHash:           hash,
		CorrelationID:         grant.SecretID,
		CausationID:           "secret:grant:" + grant.ID,
		Events: []eventlog.NewEvent{{
			ID:            fmt.Sprintf("evt_secret_grant_%s_%d", grant.ID, len(existing)+1),
			Type:          eventType,
			SchemaVersion: 1,
			OccurredAt:    v.now().UTC(),
			Visibility:    eventlog.VisibilityPublic,
			Data:          data,
		}},
	})
	return err
}

func secretStream(workspaceID, secretID string) eventlog.StreamKey {
	return eventlog.StreamKey{WorkspaceID: workspaceID, Type: "secret", ID: secretID}
}

func grantID(req GrantRequest) string {
	return hex.EncodeToString([]byte(req.SecretID + ":" + fmt.Sprint(req.Version) + ":" + req.ScopeType + ":" + req.ScopeID))
}

func encrypt(key, plaintext []byte) ([]byte, []byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, err
	}
	return gcm.Seal(nil, nonce, plaintext, nil), nonce, nil
}
