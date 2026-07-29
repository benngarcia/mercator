package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
)

// BackupDatabase writes a consistent copy of the database file at source to
// destination, and answers with the path it wrote.
//
// It exists because the documented recovery procedure shelled out to the
// `sqlite3` CLI, which Mercator does not ship: the daemon is built on
// modernc.org/sqlite, a pure-Go SQLite with no binary beside it, so the one
// documented way to take a backup was a command that is absent from the
// container image and from most hosts. A recovery procedure nobody can run is
// not a recovery procedure.
//
// VACUUM INTO is SQLite's own online backup. It reads the source inside a
// single read transaction, so the copy is the database as of one instant even
// while a server is writing to it, and it writes a defragmented file rather
// than a byte copy, so the result needs no companion -wal or -shm file to be
// complete.
//
// It deliberately does not open a Storage. Opening one runs every migration and
// purges the credentials of deleted connections, which are writes, and a
// process taking a copy of a live server's database has no business writing to
// it.
//
// It answers with the destination it wrote rather than the one it was handed,
// because it resolves that to an absolute path first, and the operator has to
// be told the file they can actually restore. Two things resolve this path:
// this process, which takes the file with O_EXCL, and SQLite, which writes the
// copy into it. They agree on an absolute path and disagree on others, because
// SQLite reads a destination beginning with "file:" as a URI. `mercator backup
// file:latest.db` used to claim a file literally named "file:latest.db" and
// write the copy over "latest.db", destroying whatever was there and creating
// the copy against the umask, with both of the claim's guarantees bypassed at
// once and nothing said about either.
func BackupDatabase(ctx context.Context, source, destination string) (string, error) {
	path, err := filepath.Abs(destination)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", destination, err)
	}
	if err := claimDestination(path); err != nil {
		return "", err
	}
	if err := copyDatabaseInto(ctx, source, path); err != nil {
		// The empty file the claim above took the destination with is not a
		// backup, and leaving it there refuses every later attempt at this path
		// with a message that reads as "an earlier backup is already there". It
		// is safe to remove only because the claim proved the path was free:
		// this can never be deleting a copy somebody else put there.
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

// copyDatabaseInto holds every step that can fail once the destination has been
// claimed, so that the one removal above covers all of them rather than the
// last of them.
func copyDatabaseInto(ctx context.Context, source, path string) error {
	db, err := sql.Open("sqlite", readOnlyDSN(source))
	if err != nil {
		return fmt.Errorf("open %s: %w", source, err)
	}
	defer func() { _ = db.Close() }()
	// Both statements below must reach the same connection: a PRAGMA is a
	// property of the connection it was set on, and a pool free to hand the
	// VACUUM to a second connection would run it with the default timeout.
	db.SetMaxOpenConns(1)
	// Wait for a writer that holds the lock rather than failing instantly with
	// SQLITE_BUSY. A backup taken while the control plane serves is the case
	// this command is for, and the control plane is always about to write.
	if _, err := db.ExecContext(ctx, `PRAGMA busy_timeout=5000`); err != nil {
		return fmt.Errorf("configure the backup connection to %s: %w", source, err)
	}
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, path); err != nil {
		return fmt.Errorf("back up %s into %s: %w", source, path, err)
	}
	return nil
}

// readOnlyDSN names the source in the only mode a copy has any business opening
// it in.
//
// mode=ro is what makes "this reads the source and nothing else" true rather
// than merely intended. An ordinary connection to a database whose last process
// was killed recovers the write-ahead log into the main file and deletes both
// companions: measured on this host, a 4KB main file beside a 342KB -wal became
// a 147KB main file with no -wal and no -shm, done by a command documented as
// read-only, run by an operator taking a copy of a crashed deployment before
// touching it. A read-only connection reads through that same -wal and leaves
// all three files as they were.
//
// The path is escaped into the URI rather than concatenated onto "file:",
// because a directory named with a question mark would otherwise be read as the
// start of a query and the copy taken of a database nobody named.
func readOnlyDSN(source string) string {
	return (&url.URL{Scheme: "file", Path: source, RawQuery: "mode=ro"}).String()
}

// claimDestination creates the file the copy is about to be written into, and
// fails if anything is already there.
//
// SQLite's own guard is not enough on its own. VACUUM INTO refuses a destination
// that is a database, which covers overwriting yesterday's backup, but a
// destination that exists and is shorter than a page header is silently
// overwritten: a copy that was truncated by a full disk is exactly the file an
// operator is most likely to be retrying over, and losing it without a word is
// the one outcome a backup command must not have. O_EXCL states the intent for
// every kind of file and closes the gap between asking whether the path is free
// and taking it.
//
// The mode is the other reason to create the file here. A backup holds every
// event and the sealed bytes of every stored provider credential, and SQLite
// would create it against the process umask.
func claimDestination(path string) error {
	claimed, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("take %s for the backup: %w", path, err)
	}
	return claimed.Close()
}
