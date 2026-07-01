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

// Token mints an HMAC token that authorizes a workload to report for exactly
// one run in exactly one workspace. Binding the workspace (not just the run ID)
// means a leaked token can never be replayed against another workspace's view
// of the same run, and the workspace_id a reporter sends must be the one the
// token was minted for.
func (s *Signer) Token(workspaceID, runID string) string {
	if !s.Enabled() {
		return ""
	}
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(workspaceID))
	mac.Write([]byte{0})
	mac.Write([]byte(runID))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Verify checks whether token authorizes reporting for runID in workspaceID.
func (s *Signer) Verify(workspaceID, runID, token string) bool {
	if !s.Enabled() || token == "" {
		return false
	}
	want := s.Token(workspaceID, runID)
	return subtle.ConstantTimeCompare([]byte(want), []byte(token)) == 1
}
