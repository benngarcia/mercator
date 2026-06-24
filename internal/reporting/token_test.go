package reporting

import "testing"

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
