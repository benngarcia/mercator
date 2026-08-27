package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"os"
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
	seal(t, dsn, retiredMasterKey, "conn_vast", "vast-token")

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
	if got := resolve(t, dsn, newMasterKey, "conn_vast"); got != "vast-token" {
		t.Fatalf("resolve under the new key = %q, want %q", got, "vast-token")
	}
	if got := resolve(t, dsn, retiredMasterKey, "conn_vast"); got != "" {
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
	serveDatabase(t, dsn, hex.EncodeToString(retiredMasterKey))

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

// TestRekeyRefusesADatabaseItWouldHaveToCreate drives the real command against
// a path that holds no database. Opening a SQLite store creates the file and
// its schema, so without the refusal the rotation succeeds against an empty
// database, reports zero rows, and tells the operator to delete the key every
// real credential is still sealed under.
func TestRekeyRefusesADatabaseItWouldHaveToCreate(t *testing.T) {
	// Arrange: the DSN an operator mistypes, or one a root shell never exported.
	absent := filepath.Join(t.TempDir(), "mercator.db")

	// Act
	var stdout, stderr bytes.Buffer
	exitCode := run(context.Background(), []string{"mercator", "rekey"}, map[string]string{
		"MERCATOR_SQLITE_DSN": "file:" + absent,
		"MERCATOR_SECRET_KEY": hex.EncodeToString(newMasterKey),
		previousMasterKeyVar:  hex.EncodeToString(retiredMasterKey),
	}, &stdout, &stderr)

	// Assert
	if exitCode != 1 {
		t.Fatalf("run() = %d, want 1; stdout = %q", exitCode, stdout.String())
	}
	if !strings.Contains(stderr.String(), absent) {
		t.Fatalf("stderr = %q, want the database path named", stderr.String())
	}
	if _, err := os.Stat(absent); !os.IsNotExist(err) {
		t.Fatalf("os.Stat(%s) = %v, want the refusal to have created nothing", absent, err)
	}
}

// TestRekeyRefusesAMemoryBackedDatabase: a memory DSN stores nothing past the
// process that opened it, so rotating one moves no credential anywhere.
func TestRekeyRefusesAMemoryBackedDatabase(t *testing.T) {
	// Arrange, Act
	var stdout, stderr bytes.Buffer
	exitCode := run(context.Background(), []string{"mercator", "rekey"}, map[string]string{
		"MERCATOR_SQLITE_DSN": "file::memory:?mode=memory&cache=shared",
		"MERCATOR_SECRET_KEY": hex.EncodeToString(newMasterKey),
		previousMasterKeyVar:  hex.EncodeToString(retiredMasterKey),
	}, &stdout, &stderr)

	// Assert
	if exitCode != 1 {
		t.Fatalf("run() = %d, want 1; stdout = %q", exitCode, stdout.String())
	}
	if !strings.Contains(stderr.String(), "memory-backed") {
		t.Fatalf("stderr = %q, want the memory-backed database named", stderr.String())
	}
}

// TestRekeyNamesTheDatabaseWhenItMovesNothing: zero rows in a database that
// does exist is ordinary, and is also what rotating the wrong file looks like,
// so the report names the file instead of reading as a plain success.
func TestRekeyNamesTheDatabaseWhenItMovesNothing(t *testing.T) {
	// Arrange: a real database with no sealed credential in it.
	path := filepath.Join(t.TempDir(), "mercator.db")
	dsn := "file:" + path
	storage, err := sqlitestore.Open(t.Context(), dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	_ = storage.Close()

	// Act
	var stdout, stderr bytes.Buffer
	exitCode := run(context.Background(), []string{"mercator", "rekey"}, map[string]string{
		"MERCATOR_SQLITE_DSN": dsn,
		"MERCATOR_SECRET_KEY": hex.EncodeToString(newMasterKey),
		previousMasterKeyVar:  hex.EncodeToString(retiredMasterKey),
	}, &stdout, &stderr)

	// Assert
	if exitCode != 0 {
		t.Fatalf("run() = %d, stderr = %s", exitCode, stderr.String())
	}
	if !strings.Contains(stdout.String(), "re-sealed 0 credential(s)") || !strings.Contains(stdout.String(), path) {
		t.Fatalf("stdout = %q, want zero rows and the database named", stdout.String())
	}
}

func seal(t *testing.T, dsn string, masterKey []byte, connectionID, secret string) {
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
	if err := store.Put(t.Context(), connectionID, blob); err != nil {
		t.Fatalf("put credential: %v", err)
	}
}

// resolve answers with the plaintext the master key reads, or "" when it reads
// nothing at all.
func resolve(t *testing.T, dsn string, masterKey []byte, connectionID string) string {
	t.Helper()
	storage, err := sqlitestore.Open(t.Context(), dsn)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer func() { _ = storage.Close() }()
	resolver := credential.NewResolver(nil, storage.CredentialStore(), masterKey)
	plaintext, err := resolver.Resolve(t.Context(),
		credential.Credential{Source: credential.SourceMercator, Ref: connectionID})
	if err != nil {
		return ""
	}
	return plaintext
}
