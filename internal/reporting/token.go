// Package reporting mints and verifies per-run HMAC tokens that authorize a
// workload to report events for its own run, and nothing else.
package reporting

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
)

type Signer struct{ key []byte }

func NewSigner(key []byte) *Signer { return &Signer{key: key} }

func (s *Signer) Enabled() bool { return len(s.key) > 0 }

func (s *Signer) Token(runID string) string {
	if !s.Enabled() {
		return ""
	}
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(runID))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Signer) Verify(runID, token string) bool {
	if !s.Enabled() || token == "" {
		return false
	}
	want := s.Token(runID)
	return subtle.ConstantTimeCompare([]byte(want), []byte(token)) == 1
}
