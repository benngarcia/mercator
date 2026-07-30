package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/benngarcia/mercator/internal/keymaterial"
	sqlitestore "github.com/benngarcia/mercator/internal/storage/sqlite"
)

// previousMasterKeyVar names the master key being retired. It exists only for
// the duration of a rotation and must be removed from the deployment's
// environment once `mercator rekey` has succeeded.
const previousMasterKeyVar = "MERCATOR_SECRET_KEY_PREVIOUS"

// runRekeyCommand re-seals every stored provider credential from the retired
// master key to the configured one, then reports how many rows moved.
//
// It is a separate command rather than a step in `serve` on purpose: a boot
// that rotated would need the retired key present at every boot, so the key
// would never actually be retired.
//
// Two ways of rotating the wrong thing are refusals rather than advice, because
// both of them end with the operator deleting the only key that opens a
// credential. A rotation beside a live server would re-seal the rows it can see
// while the server keeps sealing new ones under the key it loaded at boot; a
// rotation against a database this command created would re-seal nothing at all
// while the real one sits untouched. What the refusals cannot see is a rotation
// against some other database that does exist, so the DSN still has to be the
// server's.
func runRekeyCommand(ctx context.Context, env map[string]string, stdout, stderr io.Writer) int {
	newKey, err := masterKeyFromEnv(env)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "rekey: %v\n", err)
		return 1
	}
	retiredKey, err := retiredMasterKeyFromEnv(env)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "rekey: %v\n", err)
		return 1
	}
	dsn, err := sqliteDSN(env)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "rekey: resolve database path: %v\n", err)
		return 1
	}
	if err := requireStoredDatabase(dsn); err != nil {
		_, _ = fmt.Fprintf(stderr, "rekey: %v\n", err)
		return 1
	}
	claim, err := claimDatabase(dsn)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "rekey: %v; stop the server before rotating its master key\n", err)
		return 1
	}
	defer claim.release()
	storage, err := sqlitestore.Open(ctx, dsn)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "rekey: open %s: %v\n", dsn, err)
		return 1
	}
	defer func() { _ = storage.Close() }()
	resealed, err := storage.CredentialStore().Rekey(ctx, retiredKey, newKey)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "rekey: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintln(stdout, rekeyReport(dsn, resealed))
	return 0
}

// requireStoredDatabase refuses a rotation the command would have to invent a
// database for.
//
// sqlitestore.Open creates the file and its schema, and an unset
// MERCATOR_SQLITE_DSN resolves to a default path rather than failing, so a
// rotation run in a shell that does not carry the server's DSN would otherwise
// create an empty database, re-seal the nothing in it, exit 0, and print the
// instruction to delete the retired key. Every real credential would still be
// sealed under the key the operator has just been told to remove, and its
// plaintext would be gone. The command knows it is about to create the file, so
// it says so instead.
func requireStoredDatabase(dsn string) error {
	path := databasePath(dsn)
	if path == "" {
		return fmt.Errorf(
			"MERCATOR_SQLITE_DSN names a memory-backed database, which stores nothing to rotate; "+
				"name the database file the server uses (%s)", dsn)
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf(
			"%s does not exist, and rotating a database this command created would leave every real "+
				"credential sealed under the key you are about to delete; export the MERCATOR_SQLITE_DSN "+
				"the server runs with", path)
	}
	return nil
}

// rekeyReport states what moved. Zero rows in a database that exists is
// ordinary in a deployment that stores no connection credentials, and is also
// what a rotation against the wrong database looks like, so it names the file
// rather than reporting a bare success.
func rekeyReport(dsn string, resealed int) string {
	if resealed == 0 {
		return fmt.Sprintf(
			"re-sealed 0 credential(s): %s holds no sealed credential. "+
				"Confirm that is the database the server runs against before removing %s",
			databasePath(dsn), previousMasterKeyVar)
	}
	return fmt.Sprintf(
		"re-sealed %d credential(s) under the new MERCATOR_SECRET_KEY; remove %s from the environment",
		resealed, previousMasterKeyVar)
}

func retiredMasterKeyFromEnv(values map[string]string) ([]byte, error) {
	raw := values[previousMasterKeyVar]
	if raw == "" {
		return nil, fmt.Errorf("%s is required and must hold the key the credentials were sealed with", previousMasterKeyVar)
	}
	return keymaterial.Decode(previousMasterKeyVar, raw, 32)
}
