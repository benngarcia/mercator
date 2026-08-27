package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestARestoredDatabaseServesOnlyUnderTheKeyItWasSealedWith is the restore
// drill in docs/production/backup-recovery.md, performed rather than described.
// A backup of the database alone does not restore: every stored connection
// credential in it is sealed under a subkey of MERCATOR_SECRET_KEY, so the key
// is restore-critical state that lives nowhere in the files being copied.
func TestARestoredDatabaseServesOnlyUnderTheKeyItWasSealedWith(t *testing.T) {
	// Arrange: a server holding a provider connection whose credential Mercator
	// stores, then stopped cleanly and copied the way the runbook copies it.
	original := filepath.Join(t.TempDir(), "mercator.db")
	live := serveDatabase(t, "file:"+original, hex.EncodeToString(retiredMasterKey))
	live.post(t, "/v1/connections", "restore-drill-1", map[string]any{
		"connection_id": "runpod",
		"adapter_type":  "runpod",
		"credential":    map[string]any{"source": "mercator"},
		"secret":        "rpa_backed_up",
	})
	live.stop()
	restored := copyDatabase(t, original, filepath.Join(t.TempDir(), "mercator-restore.db"))

	// Act: start the copy under the key its rows were sealed with.
	restoredServer := serveDatabase(t, "file:"+restored, hex.EncodeToString(retiredMasterKey))

	// Assert: the connection is served from the restored copy, and a server that
	// could not open its sealed credential would not be listening at all.
	if connections := restoredServer.get(t, "/v1/connections"); !strings.Contains(connections, `"runpod"`) {
		t.Fatalf("restored connections = %s, want the runpod connection", connections)
	}
	restoredServer.stop()

	// Assert: a different key, which is what a freshly generated one is, refuses
	// to start on the same copy and names the credential it cannot read.
	refusal := captureStartupLog(t)
	exitCode := run(context.Background(), []string{"mercator", "serve"}, map[string]string{
		"MERCATOR_ADDR":       "127.0.0.1:0",
		"MERCATOR_API_TOKEN":  operatorToken,
		"MERCATOR_SECRET_KEY": hex.EncodeToString(newMasterKey),
		"MERCATOR_SQLITE_DSN": "file:" + restored,
	}, &bytes.Buffer{}, &bytes.Buffer{})
	if exitCode != 1 {
		t.Fatalf("run() = %d, want 1", exitCode)
	}
	if !strings.Contains(refusal.String(), "cannot be decrypted with the configured MERCATOR_SECRET_KEY") {
		t.Fatalf("startup log = %q, want the unreadable credential named", refusal.String())
	}
}

// copyDatabase copies a stopped database and whatever SQLite left beside it,
// which is what "copy the db, WAL, and shm files together" means.
func copyDatabase(t *testing.T, from, to string) string {
	t.Helper()
	for _, suffix := range []string{"", "-wal", "-shm"} {
		content, err := os.ReadFile(from + suffix)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatalf("read %s: %v", from+suffix, err)
		}
		if err := os.WriteFile(to+suffix, content, 0o600); err != nil {
			t.Fatalf("write %s: %v", to+suffix, err)
		}
	}
	return to
}
