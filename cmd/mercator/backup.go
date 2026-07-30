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
	source, err := databaseToBackUp(env)
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

// databaseToBackUp answers with the database file to copy. It requires
// MERCATOR_SQLITE_DSN and refuses a database that is not there.
//
// The variable is required here and optional for `serve` on purpose. `serve`
// falls back to a per-user data directory because a server nobody can start
// without first inventing a path is a server nobody tries, and it creates that
// directory on the way past. A backup inherits none of that reasoning: the same
// fallback, in the cron entry that does not carry the unit's environment,
// resolves to whatever database a `mercator serve` on this host once left in
// the invoking account's home directory. Measured on this host, that run copied
// an empty 147456-byte database over a production one and exited 0, and the
// only signal the operator got was a source path on standard output that cron
// discards. Refusing to guess is the whole fix: a fallback path exists, so
// asking whether a file is there cannot tell the server's database from a
// stray one.
//
// The file rather than the DSN is what goes on to the copy, because the copy
// opens it read-only and the server's DSN says nothing about how a reader of it
// should connect.
func databaseToBackUp(env map[string]string) (string, error) {
	dsn := env["MERCATOR_SQLITE_DSN"]
	if dsn == "" {
		return "", fmt.Errorf(
			"MERCATOR_SQLITE_DSN is required and must name the database the server serves; backup will " +
				"not fall back to a default path, because a copy of the wrong database also exits 0")
	}
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
