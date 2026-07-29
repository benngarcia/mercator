package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/benngarcia/mercator/internal/credential"
	sqlitestore "github.com/benngarcia/mercator/internal/storage/sqlite"
)

var (
	retiredMasterKey = []byte("fedcba9876543210fedcba9876543210")
	newMasterKey     = []byte("0123456789abcdef0123456789abcdef")
)

// TestRekeyRotatesTheMasterKeyOfARealStore drives the operator command end to
// end against a real SQLite file: a credential is sealed under the old key by
// the same resolver production uses, the command is run exactly as an operator
// runs it, and the row is then read back through the resolver under each key.
func TestRekeyRotatesTheMasterKeyOfARealStore(t *testing.T) {
	// Arrange: a stored credential sealed under the key about to be retired.
	dsn := "file:" + filepath.Join(t.TempDir(), "mercator.db")
	seal(t, dsn, retiredMasterKey, "ws_1", "conn_vast", "vast-token")

	// Act
	var stdout, stderr bytes.Buffer
	exitCode := run(context.Background(), []string{"mercator", "rekey"}, map[string]string{
		"MERCATOR_SQLITE_DSN": dsn,
		"MERCATOR_SECRET_KEY": hex.EncodeToString(newMasterKey),
		previousMasterKeyVar:  hex.EncodeToString(retiredMasterKey),
		"MERCATOR_API_TOKEN":  "operator-token",
	}, &stdout, &stderr)

	// Assert
	if exitCode != 0 {
		t.Fatalf("run() = %d, stderr = %s", exitCode, stderr.String())
	}
	if !strings.Contains(stdout.String(), "re-sealed 1 credential") {
		t.Fatalf("stdout = %q, want the rotated row count", stdout.String())
	}
	if got := resolve(t, dsn, newMasterKey, "ws_1", "conn_vast"); got != "vast-token" {
		t.Fatalf("resolve under the new key = %q, want %q", got, "vast-token")
	}
	if got := resolve(t, dsn, retiredMasterKey, "ws_1", "conn_vast"); got != "" {
		t.Fatalf("the retired key still resolves the credential as %q", got)
	}
}

// TestRekeyRefusesWithoutTheRetiredKey: the command cannot guess what the rows
// were sealed with, so an absent retired key is a refusal naming the variable.
func TestRekeyRefusesWithoutTheRetiredKey(t *testing.T) {
	// Arrange
	dsn := "file:" + filepath.Join(t.TempDir(), "mercator.db")

	// Act
	var stdout, stderr bytes.Buffer
	exitCode := run(context.Background(), []string{"mercator", "rekey"}, map[string]string{
		"MERCATOR_SQLITE_DSN": dsn,
		"MERCATOR_SECRET_KEY": hex.EncodeToString(newMasterKey),
	}, &stdout, &stderr)

	// Assert
	if exitCode != 1 {
		t.Fatalf("run() = %d, want 1", exitCode)
	}
	if !strings.Contains(stderr.String(), previousMasterKeyVar) {
		t.Fatalf("stderr = %q, want %s named", stderr.String(), previousMasterKeyVar)
	}
}

// TestRekeyRefusesWhileAServerIsUsingTheDatabase drives both real commands
// against one real database file. A rotation that ran here would re-seal the
// rows it can see while the running server kept sealing new credentials under
// the key it loaded at boot, and the restart the command tells the operator to
// perform would then refuse to open one of those rows.
func TestRekeyRefusesWhileAServerIsUsingTheDatabase(t *testing.T) {
	// Arrange: a server started the way an operator starts it, on a real file.
	dsn := "file:" + filepath.Join(t.TempDir(), "mercator.db")
	serveUntilCleanup(t, dsn)

	// Act: rotate the master key without stopping it first.
	var stdout, stderr bytes.Buffer
	exitCode := run(context.Background(), []string{"mercator", "rekey"}, map[string]string{
		"MERCATOR_SQLITE_DSN": dsn,
		"MERCATOR_SECRET_KEY": hex.EncodeToString(newMasterKey),
		previousMasterKeyVar:  hex.EncodeToString(retiredMasterKey),
	}, &stdout, &stderr)

	// Assert
	if exitCode != 1 {
		t.Fatalf("run() = %d, want 1; stdout = %q", exitCode, stdout.String())
	}
	if !strings.Contains(stderr.String(), "another mercator process") ||
		!strings.Contains(stderr.String(), "stop the server") {
		t.Fatalf("stderr = %q, want the running server named", stderr.String())
	}
}

// serveUntilCleanup starts the real serve command on dsn and returns once it is
// listening, so a case can state what happens to a command that arrives while a
// server is up. The server is stopped when the case ends.
func serveUntilCleanup(t *testing.T, dsn string) {
	t.Helper()
	startupLog := captureStartupLog(t)
	serveCtx, stopServing := context.WithCancel(context.Background())
	served := make(chan int, 1)
	go func() {
		served <- run(serveCtx, []string{"mercator", "serve"}, map[string]string{
			"MERCATOR_ADDR":       "127.0.0.1:0",
			"MERCATOR_API_TOKEN":  "operator-token",
			"MERCATOR_SECRET_KEY": hex.EncodeToString(retiredMasterKey),
			"MERCATOR_SQLITE_DSN": dsn,
		}, io.Discard, io.Discard)
	}()
	t.Cleanup(func() {
		stopServing()
		if exitCode := <-served; exitCode != 0 {
			t.Errorf("serve exited %d", exitCode)
		}
	})
	startupLog.waitFor(t, "mercator listening on")
}

func seal(t *testing.T, dsn string, masterKey []byte, workspaceID, connectionID, secret string) {
	t.Helper()
	storage, err := sqlitestore.Open(t.Context(), dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = storage.Close() }()
	store := storage.CredentialStore()
	blob, ok := credential.NewResolver(nil, store, masterKey).Seal([]byte(secret))
	if !ok {
		t.Fatal("seal credential")
	}
	if err := store.Put(t.Context(), workspaceID, connectionID, blob); err != nil {
		t.Fatalf("put credential: %v", err)
	}
}

// resolve answers with the plaintext the master key reads, or "" when it reads
// nothing at all.
func resolve(t *testing.T, dsn string, masterKey []byte, workspaceID, connectionID string) string {
	t.Helper()
	storage, err := sqlitestore.Open(t.Context(), dsn)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer func() { _ = storage.Close() }()
	resolver := credential.NewResolver(nil, storage.CredentialStore(), masterKey)
	plaintext, err := resolver.Resolve(t.Context(), workspaceID,
		credential.Credential{Source: credential.SourceMercator, Ref: connectionID})
	if err != nil {
		return ""
	}
	return plaintext
}
