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
// Both paths are resolved to absolute ones before anything opens them, because
// this process and SQLite both read them and they agree only on that form.
// SQLite takes a destination beginning with "file:" as a URI, so `mercator
// backup file:latest.db` used to write the copy over "latest.db" while claiming
// an empty file literally named "file:latest.db", destroying whatever the real
// path held and reporting the wrong file as the backup. The source is read by
// SQLite alone, through the URI below, which renders a relative path with an
// authority: a deployment served with MERCATOR_SQLITE_DSN=file:mercator.db,
// which starts and serves normally, was refused every backup with "invalid uri
// authority: mercator.db".
func BackupDatabase(ctx context.Context, source, destination string) (string, error) {
	from, err := filepath.Abs(source)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", source, err)
	}
	path, err := filepath.Abs(destination)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", destination, err)
	}
	if err := refuseWhatIsAlreadyThere(path); err != nil {
		return "", err
	}
	partial, err := takePartialFile(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(partial) }()
	if err := copyDatabaseInto(ctx, from, partial); err != nil {
		return "", err
	}
	// The link is the guarantee at the destination. It creates the name in one
	// step and refuses a name that is taken, so the path the operator gave holds
	// a finished copy or holds nothing at all, and the copy carries the mode the
	// partial file was created with.
	if err := os.Link(partial, path); err != nil {
		return "", fmt.Errorf("take %s for the backup: %w", path, err)
	}
	return path, nil
}

// takePartialFile creates the file the copy is written into: a sibling of the
// destination, empty, mode 0600, with a name no other run of this command uses.
//
// The copy is assembled here rather than at the destination because a backup is
// ended by things no process can catch. A `timeout` in a cron entry, a systemd
// TimeoutStopSec, an operator's Ctrl-C or an OOM kill stops this one where it
// stands, and the destination used to be a file this command had already
// claimed and was writing into. What that left at the backup path was a large
// partial file, unopenable because SQLite writes page 1 last, and every later
// backup at the same path was then refused for good with "file exists".
// Measured on this host: SIGTERM 0.35 seconds into copying a 2.3GB database
// left 923160576 bytes at the destination. A deployment writing to a fixed path
// stops backing up while looking, by file size, like it is backing up
// correctly.
//
// The name is unique per run rather than a fixed "<destination>.partial",
// because a fixed name has to be cleared before it can be taken and clearing it
// would delete the file a concurrent backup to the same destination is writing
// into. What a killed run leaves behind is one of these beside the destination,
// with SQLite's own journal beside it: they hold nothing a restore can use, and
// they block nothing.
//
// The mode is the other reason the file is created here. A backup holds every
// event and the sealed bytes of every stored provider credential, and SQLite
// would create it against the process umask.
func takePartialFile(path string) (string, error) {
	partial, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".partial-*")
	if err != nil {
		return "", fmt.Errorf("take a partial file beside %s: %w", path, err)
	}
	defer func() { _ = partial.Close() }()
	return partial.Name(), nil
}

// refuseWhatIsAlreadyThere answers the destination question before the database
// is read rather than after it. The link in BackupDatabase is what enforces the
// refusal, and it enforces it on a copy that has already been written in full:
// a nightly job pointed at yesterday's file would read and write the whole
// database, which is as large as the deployment's history, before being told
// the name was taken.
//
// SQLite's own guard is not something to lean on here. VACUUM INTO refuses a
// destination it can read as a database, which covers overwriting yesterday's
// backup, but one too short to be a database it overwrites without a word, and
// a copy a full disk truncated is exactly the file an operator is most likely
// to be retrying over.
func refuseWhatIsAlreadyThere(path string) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("take %s for the backup: file exists", path)
	}
	return nil
}

// copyDatabaseInto reads the source and writes the copy. Every step of it can
// fail, and the partial file is removed whichever one does.
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
// start of a query and the copy taken of a database nobody named. It takes an
// absolute path: this form renders a relative one as "file://mercator.db",
// whose first segment SQLite reads as a URI authority and refuses.
func readOnlyDSN(source string) string {
	return (&url.URL{Scheme: "file", Path: source, RawQuery: "mode=ro"}).String()
}
