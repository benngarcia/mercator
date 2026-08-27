package credential

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func newTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewSQLiteStore(context.Background(), db)
	if err != nil {
		t.Fatalf("init store: %v", err)
	}
	return store
}

func TestSQLiteStorePutGet(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewSQLiteStore(context.Background(), db)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if err := store.Put(context.Background(), "conn_x", []byte{1, 2, 3}); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := store.Get(context.Background(), "conn_x")
	if err != nil || string(got) != string([]byte{1, 2, 3}) {
		t.Fatalf("get: %v err=%v", got, err)
	}
	if _, err := store.Get(context.Background(), "missing"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// TestMigrateSealKeyResealsLegacyRows: a row sealed under the raw master key
// (the pre-HKDF format) is re-sealed under the derived key and stays
// resolvable; a row already sealed under the derived key is untouched.
func TestMigrateSealKeyResealsLegacyRows(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	legacy, err := Seal(key32(), []byte("legacy-secret"))
	if err != nil {
		t.Fatalf("seal legacy: %v", err)
	}
	if err := store.Put(ctx, "conn_legacy", legacy); err != nil {
		t.Fatalf("put legacy: %v", err)
	}
	current, err := Seal(DeriveSealKey(key32()), []byte("current-secret"))
	if err != nil {
		t.Fatalf("seal current: %v", err)
	}
	if err := store.Put(ctx, "conn_current", current); err != nil {
		t.Fatalf("put current: %v", err)
	}

	migrated, err := store.MigrateSealKey(ctx, key32())
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if migrated != 1 {
		t.Fatalf("expected 1 re-sealed row, got %d", migrated)
	}

	r := NewResolver(nil, store, key32())
	for id, want := range map[string]string{"conn_legacy": "legacy-secret", "conn_current": "current-secret"} {
		got, err := r.Resolve(ctx, Credential{Source: SourceMercator, Ref: id})
		if err != nil || got != want {
			t.Errorf("resolve %s after migration: %q err=%v", id, got, err)
		}
	}

	// Second run is a no-op: migration is idempotent.
	migrated, err = store.MigrateSealKey(ctx, key32())
	if err != nil || migrated != 0 {
		t.Fatalf("second migrate: migrated=%d err=%v", migrated, err)
	}
}

// TestMigrateSealKeyRefusesUndecryptableRows: a blob no key opens names the
// affected connection and fails the migration (startup-fatal for the caller).
func TestMigrateSealKeyRefusesUndecryptableRows(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	if err := store.Put(ctx, "conn_bad", []byte("not-a-ciphertext")); err != nil {
		t.Fatalf("put: %v", err)
	}

	_, err := store.MigrateSealKey(ctx, key32())
	if err == nil {
		t.Fatal("expected migration to fail on an undecryptable row")
	}
	if !strings.Contains(err.Error(), "conn_bad") {
		t.Fatalf("error must name the affected connection, got: %v", err)
	}
}

// TestMigrateSealKeyNoMasterKeyIsNoop: without a master key there is nothing
// to derive; the store may hold rows from a previously-keyed process, and
// refusing to boot here would brick the token-only path.
func TestMigrateSealKeyNoMasterKeyIsNoop(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	if err := store.Put(ctx, "conn_x", []byte("whatever")); err != nil {
		t.Fatalf("put: %v", err)
	}
	migrated, err := store.MigrateSealKey(ctx, nil)
	if err != nil || migrated != 0 {
		t.Fatalf("expected no-op, got migrated=%d err=%v", migrated, err)
	}
}

func retiredKey32() []byte { return []byte("fedcba9876543210fedcba9876543210") }

// TestRekeyMovesEveryRowToTheNewKey: after rotation the store answers under the
// new master key and refuses the retired one. This is the property an operator
// rotating a leaked key is buying, so it is asserted by resolving real rows and
// not by counting writes.
func TestRekeyMovesEveryRowToTheNewKey(t *testing.T) {
	// Arrange: two credentials sealed under the key about to be retired.
	ctx := context.Background()
	store := newTestStore(t)
	retired := NewResolver(nil, store, retiredKey32())
	sealed := map[string]string{"conn_a": "vast-token", "conn_b": "runpod-token"}
	for id, secret := range sealed {
		blob, ok := retired.Seal([]byte(secret))
		if !ok {
			t.Fatalf("seal %s under the retired key", id)
		}
		if err := store.Put(ctx, id, blob); err != nil {
			t.Fatalf("put %s: %v", id, err)
		}
	}

	// Act
	resealed, err := store.Rekey(ctx, retiredKey32(), key32())

	// Assert
	if err != nil {
		t.Fatalf("rekey: %v", err)
	}
	if resealed != len(sealed) {
		t.Fatalf("re-sealed %d rows, want %d", resealed, len(sealed))
	}
	current := NewResolver(nil, store, key32())
	for id, want := range sealed {
		got, err := current.Resolve(ctx, Credential{Source: SourceMercator, Ref: id})
		if err != nil || got != want {
			t.Errorf("resolve %s under the new key = %q, err = %v; want %q", id, got, err, want)
		}
		if _, err := retired.Resolve(ctx, Credential{Source: SourceMercator, Ref: id}); err == nil {
			t.Errorf("%s still resolves under the retired key", id)
		}
	}

	// A second rotation finds nothing left to move, so an interrupted operator
	// can simply run it again.
	resealed, err = store.Rekey(ctx, retiredKey32(), key32())
	if err != nil || resealed != 0 {
		t.Fatalf("second rekey: resealed = %d, err = %v", resealed, err)
	}
}

// TestRekeyRefusesTheWrongRetiredKey: a row neither key opens names the
// connection and aborts the whole rotation, leaving the store as it was.
func TestRekeyRefusesTheWrongRetiredKey(t *testing.T) {
	// Arrange: a credential sealed under a key the operator will not supply.
	ctx := context.Background()
	store := newTestStore(t)
	stranger := []byte("aaaaaaaaaaaaaaaabbbbbbbbbbbbbbbb")
	blob, ok := NewResolver(nil, store, stranger).Seal([]byte("orphan"))
	if !ok {
		t.Fatal("seal under the stranger key")
	}
	if err := store.Put(ctx, "conn_orphan", blob); err != nil {
		t.Fatalf("put: %v", err)
	}

	// Act
	_, err := store.Rekey(ctx, retiredKey32(), key32())

	// Assert
	if err == nil {
		t.Fatal("a row neither key opens must abort the rotation")
	}
	if !strings.Contains(err.Error(), "conn_orphan") {
		t.Fatalf("error = %v, want the affected connection named", err)
	}
	stored, err := store.Get(ctx, "conn_orphan")
	if err != nil || string(stored) != string(blob) {
		t.Fatalf("aborted rotation must leave the row untouched: err = %v", err)
	}
}
