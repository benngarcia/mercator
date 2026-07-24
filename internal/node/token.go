package node

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// DeriveKey produces a domain-separated subkey for node credentials from the
// master key, so node identity never shares signing material with run reporting
// or anything else. An empty master key leaves the signer disabled, and a
// disabled signer refuses to mint or verify rather than accepting everything.
func DeriveKey(masterKey []byte) []byte {
	if len(masterKey) == 0 {
		return nil
	}
	mac := hmac.New(sha256.New, masterKey)
	mac.Write([]byte("mercator-node-identity-v1"))
	return mac.Sum(nil)
}

// Signer mints the two short-lived credentials a node ever holds. An
// enrollment token proves a machine was invited to be one specific node
// generation, and is redeemable once. A session token authenticates one
// authenticated outbound session and expires with it. Neither is a long-lived
// credential, and neither authorizes anything beyond its own node.
type Signer struct{ key []byte }

func NewSigner(key []byte) *Signer { return &Signer{key: key} }

func (signer *Signer) Enabled() bool { return len(signer.key) > 0 }

// Enrollment mints the invitation a machine presents to join. The token binds
// the node identity, its Rental generation, and an expiry, so a leaked token
// cannot enroll a different node, a different generation, or enroll late.
func (signer *Signer) Enrollment(nodeID, rentalID string, generation uint64, expires time.Time) (string, error) {
	return signer.mint("enroll", expires, nodeID, rentalID, strconv.FormatUint(generation, 10))
}

// VerifyEnrollment reports whether token invites this exact node identity and
// generation, and has not expired at now.
func (signer *Signer) VerifyEnrollment(nodeID, rentalID string, generation uint64, token string, now time.Time) bool {
	return signer.verify(token, now, "enroll", nodeID, rentalID, strconv.FormatUint(generation, 10))
}

// Session mints the credential for one authenticated session. It binds the
// fencing token, so a session token from a superseded enrollment cannot
// authenticate against the current one.
func (signer *Signer) Session(nodeID string, fencingToken uint64, expires time.Time) (string, error) {
	return signer.mint("session", expires, nodeID, strconv.FormatUint(fencingToken, 10))
}

// VerifySession reports whether token authenticates this node at this fencing
// token, and has not expired at now.
func (signer *Signer) VerifySession(nodeID string, fencingToken uint64, token string, now time.Time) bool {
	return signer.verify(token, now, "session", nodeID, strconv.FormatUint(fencingToken, 10))
}

// TokenID is the stable identity of one minted token, safe to persist. It is a
// digest, so recording which invitation was redeemed never stores the
// credential itself.
func TokenID(token string) string {
	sum := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (signer *Signer) mint(purpose string, expires time.Time, parts ...string) (string, error) {
	if !signer.Enabled() {
		return "", fmt.Errorf("node: signing key is not configured, so no node credential can be minted")
	}
	if expires.IsZero() {
		return "", fmt.Errorf("node: %s credentials must expire", purpose)
	}
	expiry := strconv.FormatInt(expires.UTC().Unix(), 10)
	return expiry + "." + signer.sign(purpose, expiry, parts...), nil
}

func (signer *Signer) verify(token string, now time.Time, purpose string, parts ...string) bool {
	if !signer.Enabled() || token == "" {
		return false
	}
	expiry, mac, found := strings.Cut(token, ".")
	if !found {
		return false
	}
	seconds, err := strconv.ParseInt(expiry, 10, 64)
	if err != nil {
		return false
	}
	if !now.UTC().Before(time.Unix(seconds, 0).UTC()) {
		return false
	}
	want := signer.sign(purpose, expiry, parts...)
	return subtle.ConstantTimeCompare([]byte(want), []byte(mac)) == 1
}

func (signer *Signer) sign(purpose, expiry string, parts ...string) string {
	mac := hmac.New(sha256.New, signer.key)
	mac.Write([]byte(purpose))
	for _, part := range append([]string{expiry}, parts...) {
		mac.Write([]byte{0})
		mac.Write([]byte(part))
	}
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
