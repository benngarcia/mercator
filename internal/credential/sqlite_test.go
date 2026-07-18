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
	if err := store.Put(context.Background(), "ws_1", "conn_x", []byte{1, 2, 3}); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := store.Get(context.Background(), "ws_1", "conn_x")
	if err != nil || string(got) != string([]byte{1, 2, 3}) {
		t.Fatalf("get: %v err=%v", got, err)
	}
	if _, err := store.Get(context.Background(), "ws_1", "missing"); err != ErrNotFound {
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
	if err := store.Put(ctx, "ws_1", "conn_legacy", legacy); err != nil {
		t.Fatalf("put legacy: %v", err)
	}
	current, err := Seal(DeriveSealKey(key32()), []byte("current-secret"))
	if err != nil {
		t.Fatalf("seal current: %v", err)
	}
	if err := store.Put(ctx, "ws_1", "conn_current", current); err != nil {
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
		got, err := r.Resolve(ctx, "ws_1", Credential{Source: SourceMercator, Ref: id})
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
	if err := store.Put(ctx, "ws_1", "conn_bad", []byte("not-a-ciphertext")); err != nil {
		t.Fatalf("put: %v", err)
	}

	_, err := store.MigrateSealKey(ctx, key32())
	if err == nil {
		t.Fatal("expected migration to fail on an undecryptable row")
	}
	if !strings.Contains(err.Error(), "ws_1/conn_bad") {
		t.Fatalf("error must name the affected connection, got: %v", err)
	}
}

// TestMigrateSealKeyNoMasterKeyIsNoop: without a master key there is nothing
// to derive; the store may hold rows from a previously-keyed process, and
// refusing to boot here would brick the token-only path.
func TestMigrateSealKeyNoMasterKeyIsNoop(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	if err := store.Put(ctx, "ws_1", "conn_x", []byte("whatever")); err != nil {
		t.Fatalf("put: %v", err)
	}
	migrated, err := store.MigrateSealKey(ctx, nil)
	if err != nil || migrated != 0 {
		t.Fatalf("expected no-op, got migrated=%d err=%v", migrated, err)
	}
}
