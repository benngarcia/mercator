package sqlite

// Crash recovery, proven by killing a process rather than by closing one.
//
// Close-and-reopen proves nothing about durability: it is the path where every
// buffer is flushed on the way out. What an operator needs to know is what a
// machine that lost power or an OOM kill leaves behind, so these cases start a
// real second process, let it get exactly as far as they need, send it SIGKILL,
// and then read the file it left.
//
// They live in the package rather than beside it because one of them has to
// stand in the window between two migrations, which is a place only the package
// can put a process. What each case asserts afterwards is read back through the
// public door: Open, RequiresRebuild, and the events themselves.

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/eventlog"
)

// crashHelperVar names the database a re-executed copy of this test binary
// should open. Its presence is what tells that copy it is the subject of a
// crash case rather than an ordinary test run.
const crashHelperVar = "MERCATOR_CRASH_HELPER_DSN"

// helperReady is what a helper says on standard output once it has reached the
// state its case wants to kill it in. A token rather than a delay: sleeping for
// long enough is how a crash case becomes flaky on a loaded machine.
const helperReady = "mercator-crash-helper-ready"

// TestACommittedAppendSurvivesAKilledProcessAndAnInFlightOneDoesNot is what the
// event log promises: an append that answered is on disk, and an append that had
// not committed when the power went is not, with nothing half-written between
// them.
//
// The in-flight append is a real one. The event row is inserted inside the
// append's own transaction before the atomic mutation runs, so a mutation that
// never returns leaves that row written and uncommitted, and its payload is
// large enough that SQLite has spilled those pages into the write-ahead log
// rather than still holding them in a page cache the kill discards. Recovery has
// something to actually roll back.
func TestACommittedAppendSurvivesAKilledProcessAndAnInFlightOneDoesNot(t *testing.T) {
	// Arrange: a process holding one committed append and one that will never
	// commit.
	dsn := "file:" + filepath.Join(t.TempDir(), "mercator.db")
	helper := startCrashHelper(t, "TestCrashHelperHoldsAnInFlightAppend", dsn)

	// Act
	helper.kill(t)

	// Assert: the file as SQLite recovers it, read before anything this package
	// does can change how it is journalled.
	recovered := openRecovered(t, dsn)
	if mode := scalar(t, recovered, `PRAGMA journal_mode`); mode != "wal" {
		t.Errorf("the killed process left journal_mode %q, and only a write-ahead log recovers this way", mode)
	}
	if events := eventIDs(t, recovered); len(events) != 1 || events[0] != "evt_committed" {
		t.Fatalf("the recovered log holds %v, want only the append that answered", events)
	}

	// Assert: a control plane starts on the recovered file, which is the whole
	// point of surviving.
	storage, err := Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open the recovered database: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })
}

// TestCrashHelperHoldsAnInFlightAppend commits one event, opens a second append,
// and stops inside it with that append's transaction still open.
func TestCrashHelperHoldsAnInFlightAppend(t *testing.T) {
	dsn := crashHelperDSN(t)
	ctx := context.Background()
	storage, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	if _, err := storage.EventLog().Append(ctx, appendOf("evt_committed", 0, 16)); err != nil {
		t.Fatalf("commit the first append: %v", err)
	}
	_, _ = storage.EventLog().AppendAtomic(ctx, appendOf("evt_in_flight", 1, 8<<20),
		func(context.Context, *sql.Tx) error {
			announceReady()
			select {}
		})
	t.Fatal("the helper was expected to be killed inside its second append")
}

// TestBackingUpACrashedDatabaseLeavesTheSourceExactlyAsItWas is what "the
// backup reads the source and nothing else" has to mean on the one database
// where it is hardest: the file a killed process left behind.
//
// The recovery procedure tells an operator to take a copy of a crashed
// deployment before touching it, so the copy must not be the thing that touches
// it. An ordinary read-write connection is: it is the only connection left, so
// opening it recovers the write-ahead log into the main file and closing it
// deletes both companions. Measured before this was fixed, a 4KB main file
// beside a 342KB -wal came back as a 147KB main file with no -wal and no -shm,
// from a command whose own comment said it only read.
//
// Reading the copy back is the other half. A read-only connection that could
// not see through the -wal would take a copy of the main file alone, which on a
// crashed database is an empty one, and it would leave the source untouched
// while doing it.
func TestBackingUpACrashedDatabaseLeavesTheSourceExactlyAsItWas(t *testing.T) {
	// Arrange: a database whose last process was killed with a committed append
	// still in its write-ahead log.
	source := filepath.Join(t.TempDir(), "mercator.db")
	startCrashHelper(t, "TestCrashHelperHoldsAnInFlightAppend", "file:"+source).kill(t)
	database, log := databaseAndLog(t, source)
	if len(log) == 0 {
		t.Fatalf("the killed process left no write-ahead log, so this case is not about a crashed database")
	}

	// Act
	copied, err := BackupDatabase(context.Background(), source, filepath.Join(t.TempDir(), "backup.db"))
	if err != nil {
		t.Fatalf("back up the crashed database: %v", err)
	}

	// Assert: the database and the log the crash left are byte for byte what
	// they were. The -shm beside them is not asserted on: it is an index into
	// the log that SQLite rebuilds at will, and it holds nothing a restore
	// needs.
	databaseAfter, logAfter := databaseAndLog(t, source)
	if !bytes.Equal(databaseAfter, database) {
		t.Errorf("the backup rewrote the database it copied, from %d bytes to %d, and an operator takes this copy before touching a crashed deployment",
			len(database), len(databaseAfter))
	}
	if !bytes.Equal(logAfter, log) {
		t.Errorf("the backup changed the write-ahead log from %d bytes to %d, which is where a crashed deployment's last commits live",
			len(log), len(logAfter))
	}

	// Assert: the copy holds the append that was committed into that log, so the
	// source was left alone by reading through it rather than by ignoring it.
	if events := eventIDs(t, openRecovered(t, "file:"+copied)); len(events) != 1 || events[0] != "evt_committed" {
		t.Fatalf("the copy holds %v, want the append the crashed database had committed", events)
	}
}

// databaseAndLog is the two files a restore is made of, as they are on disk
// right now. A file that is not there reads as empty, which is what a copy that
// deleted the log leaves behind.
func databaseAndLog(t *testing.T, source string) (database, log []byte) {
	t.Helper()
	return fileOrNothing(t, source), fileOrNothing(t, source+"-wal")
}

func fileOrNothing(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read %s: %v", path, err)
	}
	return content
}

// TestAMigrationKilledAfterRewritingTheLogStillAsksForARebuild closes the window
// this slice found in the durability survey.
//
// The service class rename rewrites the events the Run projection was reduced
// from, and the projection is stored rather than recomputed, so the rename has to
// say the projection is stale. That used to be said four migrations later. A
// process killed in between committed the rewrite, and on the next boot the
// rewrite found nothing left to rename, so nothing ever marked the projection
// stale: RequiresRebuild answered "current" over a read model answering in a
// vocabulary the log no longer speaks, for the life of the database.
//
// The kill lands where it used to hurt: the helper performs the rename and
// nothing after it.
func TestAMigrationKilledAfterRewritingTheLogStillAsksForARebuild(t *testing.T) {
	// Arrange: a pre-rename database whose Run projection sits at the current
	// schema version, which is what every real one looks like.
	ctx := context.Background()
	dsn := "file:" + loadLegacyObjectiveFile(t)
	helper := startCrashHelper(t, "TestCrashHelperRenamesObjectivesThenStops", dsn)

	// Act
	helper.kill(t)

	// Assert: the rewrite is on disk, so this is the window and not the state
	// before it.
	recovered := openRecovered(t, dsn)
	if remaining := scalar(t, recovered, `
		SELECT COUNT(*) FROM events
		WHERE data_json LIKE '%"objective"%' OR private_data LIKE '%"objective"%'
	`); remaining != "0" {
		t.Fatalf("%s events still state a placement objective, so the killed process died before the rewrite", remaining)
	}

	// Assert: the boot that follows the crash knows the read model is no longer
	// derived from the log it is stored against.
	storage, err := New(ctx, recovered)
	if err != nil {
		t.Fatalf("boot on the recovered database: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	rebuild, err := storage.Runs().RequiresRebuild(ctx)
	if err != nil {
		t.Fatalf("inspect the Run projection: %v", err)
	}
	if !rebuild {
		t.Fatal("the crash committed a rewrite of the log and the projection reduced from it still calls itself current")
	}
}

// TestCrashHelperRenamesObjectivesThenStops gets exactly as far as the rename
// and no further, which is the state a process killed in that window leaves.
func TestCrashHelperRenamesObjectivesThenStops(t *testing.T) {
	dsn := crashHelperDSN(t)
	ctx := context.Background()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `PRAGMA journal_mode=WAL`); err != nil {
		t.Fatalf("journal the database the way the daemon does: %v", err)
	}
	if err := migrateLegacyPlacementObjectives(ctx, db); err != nil {
		t.Fatalf("rename placement objectives: %v", err)
	}
	announceReady()
	select {}
}

// appendOf is one event on its own stream, carrying payload bytes of it. Size is
// the caller's to say because an in-flight append only leaves something for
// recovery to undo once its pages no longer fit in the page cache.
func appendOf(eventID string, version uint64, payload int) eventlog.AppendRequest {
	data, _ := json.Marshal(map[string]string{"payload": strings.Repeat("a", payload)})
	return eventlog.AppendRequest{
		Stream:                eventlog.StreamKey{WorkspaceID: "ws_1", Type: "run", ID: "run_1"},
		ExpectedStreamVersion: version,
		CommandKey:            "crash:" + eventID,
		RequestHash:           "sha256:" + eventID,
		Events: []eventlog.NewEvent{{
			ID:            eventID,
			Type:          "compute.run.requested.v1",
			SchemaVersion: 1,
			Data:          data,
		}},
	}
}

// loadLegacyObjectiveFile writes the pre-rename fixture to a file a second
// process can open, and answers with its path. The fixture is the same one the
// rename's own cases use, including the Run projection row that was reduced from
// the events it is about to rewrite.
func loadLegacyObjectiveFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mercator.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer func() { _ = db.Close() }()
	fixture, err := os.ReadFile("testdata/legacy_objective_event.sql")
	if err != nil {
		t.Fatalf("read the legacy objective fixture: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), string(fixture)); err != nil {
		t.Fatalf("load the legacy objective fixture: %v", err)
	}
	return path
}

// openRecovered opens the file a killed process left, on a connection that
// changes nothing about how it is journalled. Reading PRAGMA journal_mode
// through a connection that has just set it would only report what this test
// asked for.
func openRecovered(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("reopen %s: %v", dsn, err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func scalar(t *testing.T, db *sql.DB, query string) string {
	t.Helper()
	var answered string
	if err := db.QueryRowContext(context.Background(), query).Scan(&answered); err != nil {
		t.Fatalf("read %q: %v", query, err)
	}
	return answered
}

func eventIDs(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `SELECT event_id FROM events ORDER BY global_position`)
	if err != nil {
		t.Fatalf("read the recovered log: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan the recovered log: %v", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the recovered log: %v", err)
	}
	return ids
}

// crashHelperDSN is how a helper tells whether it is the process a crash case
// started. Run any other way it is not a case at all, so it says so and stops.
func crashHelperDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv(crashHelperVar)
	if dsn == "" {
		t.Skip("started only by a crash recovery case, which kills it")
	}
	return dsn
}

// announceReady is written straight to the process's own output rather than
// through the testing package, whose output a killed process never flushes.
func announceReady() {
	_, _ = fmt.Fprintln(os.Stdout, helperReady)
}

// crashHelper is a second Mercator process, stopped where a case wants to kill
// it. It is this test binary re-executed, which is what lets a helper reach a
// state only this package can put a process in.
type crashHelper struct {
	command *exec.Cmd
}

func startCrashHelper(t *testing.T, helperName, dsn string) *crashHelper {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^"+helperName+"$", "-test.timeout=0")
	command.Env = append(os.Environ(), crashHelperVar+"="+dsn)
	command.Stderr = os.Stderr
	said, err := command.StdoutPipe()
	if err != nil {
		t.Fatalf("read the helper's output: %v", err)
	}
	if err := command.Start(); err != nil {
		t.Fatalf("start the helper: %v", err)
	}
	helper := &crashHelper{command: command}
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	})
	helper.awaitReady(t, said)
	return helper
}

func (h *crashHelper) awaitReady(t *testing.T, said io.Reader) {
	t.Helper()
	ready := make(chan struct{})
	go func() {
		lines := bufio.NewScanner(said)
		for lines.Scan() {
			if strings.Contains(lines.Text(), helperReady) {
				close(ready)
				return
			}
		}
	}()
	select {
	case <-ready:
	case <-time.After(60 * time.Second):
		t.Fatal("the helper never reached the state this case kills it in")
	}
}

// kill sends SIGKILL, which no process can catch, and then states that the
// process really died of it. A helper that had exited on its own would leave the
// same file behind for a reason this case is not about.
func (h *crashHelper) kill(t *testing.T) {
	t.Helper()
	if err := h.command.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("kill the helper: %v", err)
	}
	err := h.command.Wait()
	status, ok := h.command.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("the helper ended as %v (%v), want killed by SIGKILL", h.command.ProcessState, err)
	}
}
