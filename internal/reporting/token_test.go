package reporting

import (
	"bytes"
	"testing"
)

func TestDeriveKey(t *testing.T) {
	master := []byte("0123456789abcdef0123456789abcdef")
	derived := DeriveKey(master)
	if len(derived) == 0 {
		t.Fatal("DeriveKey returned empty key for non-empty master")
	}
	// Derived key must differ from the master key.
	if bytes.Equal(derived, master) {
		t.Fatal("DeriveKey returned the master key unchanged")
	}
	// Deterministic: same input → same output.
	derived2 := DeriveKey(master)
	if !bytes.Equal(derived, derived2) {
		t.Fatal("DeriveKey is not deterministic")
	}
	// Empty master → nil/empty (signer disabled).
	if got := DeriveKey(nil); len(got) != 0 {
		t.Fatalf("DeriveKey(nil) should return empty, got %v", got)
	}
	if got := DeriveKey([]byte{}); len(got) != 0 {
		t.Fatalf("DeriveKey(empty) should return empty, got %v", got)
	}
}

func TestTokenRoundTripAndScoping(t *testing.T) {
	s := NewSigner([]byte("0123456789abcdef0123456789abcdef"))
	if !s.Enabled() { t.Fatal("signer should be enabled with a key") }
	tok := s.Token("run_a")
	if tok == "" { t.Fatal("empty token") }
	if !s.Verify("run_a", tok) { t.Fatal("token should verify for its run") }
	if s.Verify("run_b", tok) { t.Fatal("token must NOT verify for a different run") }
	if s.Verify("run_a", "garbage") { t.Fatal("garbage token must not verify") }
}

func TestDisabledSignerVerifiesNothing(t *testing.T) {
	s := NewSigner(nil)
	if s.Enabled() { t.Fatal("nil key → disabled") }
	if s.Verify("run_a", s.Token("run_a")) { t.Fatal("disabled signer must verify nothing") }
}
