package main

import (
	"context"
	"fmt"
	"io"

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
// It runs with the server stopped, and it enforces that rather than asking for
// it. A rotation that ran beside a live server would re-seal the rows it could
// see while the server kept sealing new credentials under the key it loaded at
// boot, and the restart this command tells the operator to perform would then
// refuse to open one of those rows, under a retired key the operator has just
// been told to delete.
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
	_, _ = fmt.Fprintf(stdout,
		"re-sealed %d credential(s) under the new MERCATOR_SECRET_KEY; remove %s from the environment\n",
		resealed, previousMasterKeyVar)
	return 0
}

func retiredMasterKeyFromEnv(values map[string]string) ([]byte, error) {
	raw := values[previousMasterKeyVar]
	if raw == "" {
		return nil, fmt.Errorf("%s is required and must hold the key the credentials were sealed with", previousMasterKeyVar)
	}
	return keymaterial.Decode(previousMasterKeyVar, raw, 32)
}
