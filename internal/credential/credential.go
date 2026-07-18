// Package credential resolves connection credentials from a {source, ref}
// reference. Secrets are never stored in the event log; the mercator source
// keeps ciphertext in a SecretStore, encrypted under a process master key.
package credential

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
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

// sealKeyInfo is the HKDF domain-separation label for credential sealing.
// MERCATOR_SECRET_KEY is a master key shared across purposes (reporting tokens
// derive their own HMAC subkey); the raw key must never be used directly as a
// cipher key. Changing this label orphans every sealed credential.
const sealKeyInfo = "mercator/credential-seal/v1"

// DeriveSealKey derives the AES-256 credential-sealing key from the master
// key via HKDF-SHA256 under sealKeyInfo. Nil/empty master key → nil (sealing
// disabled), matching the master key's presence semantics everywhere else.
func DeriveSealKey(masterKey []byte) []byte {
	if len(masterKey) == 0 {
		return nil
	}
	key, err := hkdf.Key(sha256.New, masterKey, nil, sealKeyInfo, 32)
	if err != nil {
		// Only reachable with a broken hash or absurd length request; neither
		// can happen with the fixed parameters above.
		panic(fmt.Sprintf("credential: derive seal key: %v", err))
	}
	return key
}

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

type Resolver struct {
	getenv  func(string) string
	store   SecretStore
	sealKey []byte
}

// NewResolver derives the sealing subkey from the master key immediately; the
// raw master key is never retained here.
func NewResolver(getenv func(string) string, store SecretStore, masterKey []byte) *Resolver {
	return &Resolver{getenv: getenv, store: store, sealKey: DeriveSealKey(masterKey)}
}

// Seal encrypts plaintext under the derived sealing key. Returns the sealed
// blob and true on success, or nil and false if no master key is configured.
func (r *Resolver) Seal(plaintext []byte) ([]byte, bool) {
	if len(r.sealKey) == 0 {
		return nil, false
	}
	blob, err := Seal(r.sealKey, plaintext)
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
		if len(r.sealKey) == 0 {
			return "", errors.New("credential: mercator source disabled (set MERCATOR_SECRET_KEY)")
		}
		blob, err := r.store.Get(ctx, workspaceID, c.Ref)
		if err != nil {
			return "", err
		}
		plain, err := Open(r.sealKey, blob)
		if err != nil {
			return "", err
		}
		return string(plain), nil
	default:
		return "", fmt.Errorf("credential: unknown source %q", c.Source)
	}
}
