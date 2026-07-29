package main

import (
	"context"
	"fmt"
	"io"
	"os"

	sqlitestore "github.com/benngarcia/mercator/internal/storage/sqlite"
)

// runBackupCommand copies the database this deployment runs on to a path the
// operator names, while the server keeps serving.
//
// It takes no database claim, which is the judgment call in this command. The
// claim `serve` and `rekey` hold says "one process is changing this database";
// a backup changes nothing, and taking the claim would mean the only supported
// backup was one taken with the control plane stopped. SQLite's own locking is
// what makes the copy consistent against a live writer.
func runBackupCommand(ctx context.Context, args []string, env map[string]string, stdout, stderr io.Writer) int {
	destination, err := backupDestination(args)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "backup: %v\n", err)
		return 2
	}
	dsn, err := sqliteDSN(env)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "backup: resolve database path: %v\n", err)
		return 1
	}
	source, err := requireDatabaseToCopy(dsn)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "backup: %v\n", err)
		return 1
	}
	// The path written is the one this reports. It is not always the one the
	// operator typed: a relative destination is resolved so that the restore
	// this prints names the copy from anywhere, and so that SQLite and this
	// process cannot resolve the same string to two different files.
	written, err := sqlitestore.BackupDatabase(ctx, source, destination)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "backup: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "backed up %s to %s; restore it with MERCATOR_SQLITE_DSN=file:%s and the MERCATOR_SECRET_KEY its rows were sealed under\n",
		source, written, written)
	return 0
}

func backupDestination(args []string) (string, error) {
	if len(args) != 3 {
		return "", fmt.Errorf("usage: mercator backup <path>")
	}
	return args[2], nil
}

// requireDatabaseToCopy answers with the database file to copy, and refuses one
// that is not there.
//
// sqliteDSN answers with a default path rather than failing when
// MERCATOR_SQLITE_DSN is unset, and sql.Open creates whatever file it is
// pointed at, so a backup run in a shell that does not carry the server's DSN
// would otherwise create an empty database, copy the nothing in it, and exit 0.
// The operator would hold a file that restores into a control plane with no
// history and no way to tell from the exit code.
//
// The file rather than the DSN is what goes on to the copy, because the copy
// opens it read-only and the server's DSN says nothing about how a reader of it
// should connect.
func requireDatabaseToCopy(dsn string) (string, error) {
	path := databasePath(dsn)
	if path == "" {
		return "", fmt.Errorf(
			"MERCATOR_SQLITE_DSN names a memory-backed database, which is private to the process that "+
				"opened it and holds nothing to copy (%s)", dsn)
	}
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf(
			"%s does not exist, and a backup of a database this command created would restore into a "+
				"control plane with no history; export the MERCATOR_SQLITE_DSN the server runs with", path)
	}
	return path, nil
}
