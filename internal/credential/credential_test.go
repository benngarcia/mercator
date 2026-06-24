package credential

import (
	"context"
	"strings"
	"testing"
)

func key32() []byte { return []byte("0123456789abcdef0123456789abcdef") }

func TestSealOpenRoundTrip(t *testing.T) {
	blob, err := Seal(key32(), []byte("rp_secret"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if strings.Contains(string(blob), "rp_secret") {
		t.Fatal("ciphertext must not contain the plaintext")
	}
	got, err := Open(key32(), blob)
	if err != nil || string(got) != "rp_secret" {
		t.Fatalf("open: %q err=%v", got, err)
	}
}

func TestOpenWithWrongKeyFails(t *testing.T) {
	blob, _ := Seal(key32(), []byte("x"))
	wrong := []byte("ffffffffffffffffffffffffffffffff")
	if _, err := Open(wrong, blob); err == nil {
		t.Fatal("expected open with wrong key to fail closed")
	}
}

func TestResolveEnvSource(t *testing.T) {
	r := NewResolver(func(k string) string {
		if k == "MY_KEY" {
			return "from-env"
		}
		return ""
	}, NewMemoryStore(), nil)
	got, err := r.Resolve(context.Background(), "ws_1", Credential{Source: SourceEnv, Ref: "MY_KEY"})
	if err != nil || got != "from-env" {
		t.Fatalf("env resolve: %q err=%v", got, err)
	}
}

func TestResolveMercatorSource(t *testing.T) {
	store := NewMemoryStore()
	blob, _ := Seal(key32(), []byte("stored-secret"))
	if err := store.Put(context.Background(), "ws_1", "conn_x", blob); err != nil {
		t.Fatalf("put: %v", err)
	}
	r := NewResolver(nil, store, key32())
	got, err := r.Resolve(context.Background(), "ws_1", Credential{Source: SourceMercator, Ref: "conn_x"})
	if err != nil || got != "stored-secret" {
		t.Fatalf("mercator resolve: %q err=%v", got, err)
	}
}

func TestResolveMercatorWithoutKeyDisabled(t *testing.T) {
	r := NewResolver(nil, NewMemoryStore(), nil) // no master key
	_, err := r.Resolve(context.Background(), "ws_1", Credential{Source: SourceMercator, Ref: "conn_x"})
	if err == nil {
		t.Fatal("expected mercator source to be disabled without a master key")
	}
}
