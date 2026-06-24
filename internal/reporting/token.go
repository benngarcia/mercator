// Package reporting mints and verifies per-run HMAC tokens that authorize a
// workload to report events for its own run, and nothing else.
package reporting

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
)

// DeriveKey produces a domain-separated subkey for the report-token signer from
// a master key. It returns HMAC-SHA256(masterKey, "mercator-report-token-v1") so
// the signer never uses the raw AES master key directly. An empty/nil masterKey
// returns nil, which leaves the signer disabled.
func DeriveKey(masterKey []byte) []byte {
	if len(masterKey) == 0 {
		return nil
	}
	mac := hmac.New(sha256.New, masterKey)
	mac.Write([]byte("mercator-report-token-v1"))
	return mac.Sum(nil)
}

type Signer struct{ key []byte }

func NewSigner(key []byte) *Signer { return &Signer{key: key} }

func (s *Signer) Enabled() bool { return len(s.key) > 0 }

// Token mints an HMAC token that authorizes a workload to report for runID.
// NOTE: The token binds only the runID (not the workspace). This is safe under
// the current single-operator model where run IDs are globally unique (uuidv7),
// but a future multi-workspace/multi-tenant deployment should bind the workspace
// into the token as well to prevent cross-workspace confusion.
func (s *Signer) Token(runID string) string {
	if !s.Enabled() {
		return ""
	}
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(runID))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Verify checks whether token is a valid token for the given runID.
// NOTE: The token binds only the runID (not the workspace). This is safe under
// the current single-operator model where run IDs are globally unique (uuidv7),
// but a future multi-workspace/multi-tenant deployment should bind the workspace
// into the token as well to prevent cross-workspace confusion.
func (s *Signer) Verify(runID, token string) bool {
	if !s.Enabled() || token == "" {
		return false
	}
	want := s.Token(runID)
	return subtle.ConstantTimeCompare([]byte(want), []byte(token)) == 1
}
