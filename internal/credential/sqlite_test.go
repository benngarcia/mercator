package credential

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

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
