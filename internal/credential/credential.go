// Package credential resolves connection credentials from a {source, ref}
// reference. Secrets are never stored in the event log; the mercator source
// keeps ciphertext in a SecretStore, encrypted under a process master key.
package credential

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
)

const (
	SourceEnv      = "env"
	SourceMercator = "mercator"
)

var ErrNotFound = errors.New("credential: secret not found")

type Credential struct {
	Source string `json:"source"`
	Ref    string `json:"ref"`
}

// Seal encrypts plaintext with AES-256-GCM; the nonce is prepended to the blob.
func Seal(key, plaintext []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// Open reverses Seal.
func Open(key, blob []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(blob) < gcm.NonceSize() {
		return nil, errors.New("credential: ciphertext too short")
	}
	nonce, ct := blob[:gcm.NonceSize()], blob[gcm.NonceSize():]
	return gcm.Open(nil, nonce, ct, nil)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

type SecretStore interface {
	Put(ctx context.Context, workspaceID, connectionID string, blob []byte) error
	Get(ctx context.Context, workspaceID, connectionID string) ([]byte, error)
	// Delete removes the sealed blob. Deleting an absent row is a no-op.
	Delete(ctx context.Context, workspaceID, connectionID string) error
}

type MemoryStore struct {
	mu sync.Mutex
	m  map[string][]byte
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{m: map[string][]byte{}} }

func memKey(ws, id string) string { return ws + "/" + id }

func (s *MemoryStore) Put(_ context.Context, ws, id string, blob []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]byte, len(blob))
	copy(cp, blob)
	s.m[memKey(ws, id)] = cp
	return nil
}

func (s *MemoryStore) Get(_ context.Context, ws, id string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	blob, ok := s.m[memKey(ws, id)]
	if !ok {
		return nil, ErrNotFound
	}
	return append([]byte(nil), blob...), nil
}

func (s *MemoryStore) Delete(_ context.Context, ws, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, memKey(ws, id))
	return nil
}

type Resolver struct {
	getenv    func(string) string
	store     SecretStore
	masterKey []byte
}

func NewResolver(getenv func(string) string, store SecretStore, masterKey []byte) *Resolver {
	return &Resolver{getenv: getenv, store: store, masterKey: masterKey}
}

// Seal encrypts plaintext using the resolver's master key. Returns the sealed
// blob and true on success, or nil and false if no master key is configured.
func (r *Resolver) Seal(plaintext []byte) ([]byte, bool) {
	if len(r.masterKey) == 0 {
		return nil, false
	}
	blob, err := Seal(r.masterKey, plaintext)
	if err != nil {
		return nil, false
	}
	return blob, true
}

// Resolve returns the plaintext credential value from the {source, ref} tuple.
// An empty Source is treated as SourceEnv.
func (r *Resolver) Resolve(ctx context.Context, workspaceID string, c Credential) (string, error) {
	switch c.Source {
	case "", SourceEnv:
		if r.getenv == nil {
			return "", errors.New("credential: env source unavailable")
		}
		// The broker's own configuration (MERCATOR_SECRET_KEY, MERCATOR_API_TOKEN,
		// …) must never be readable as a provider credential.
		if strings.HasPrefix(strings.ToUpper(c.Ref), "MERCATOR_") {
			return "", fmt.Errorf("credential: env var %q is reserved broker configuration and cannot be used as a credential ref", c.Ref)
		}
		v := r.getenv(c.Ref)
		if v == "" {
			return "", fmt.Errorf("credential: env var %q is empty", c.Ref)
		}
		return v, nil
	case SourceMercator:
		if len(r.masterKey) == 0 {
			return "", errors.New("credential: mercator source disabled (set MERCATOR_SECRET_KEY)")
		}
		blob, err := r.store.Get(ctx, workspaceID, c.Ref)
		if err != nil {
			return "", err
		}
		plain, err := Open(r.masterKey, blob)
		if err != nil {
			return "", err
		}
		return string(plain), nil
	default:
		return "", fmt.Errorf("credential: unknown source %q", c.Source)
	}
}
