package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sqlitestore "github.com/benngarcia/mercator/internal/storage/sqlite"
)

// TestABackupTakenWhileServingRestoresIntoAServerAnsweringTheSameRuns is the
// backup half of the recovery procedure, performed rather than described.
//
// The procedure used to shell out to the `sqlite3` CLI, which Mercator does not
// ship and which is not installed on most hosts, so the only documented way to
// take a backup was a command an operator could not run. A backup nobody has
// restored is not a backup, so this takes one with the shipped command, boots a
// second control plane on the copy, and reads the same Runs back through the
// authenticated API rather than by inspecting the file.
func TestABackupTakenWhileServingRestoresIntoAServerAnsweringTheSameRuns(t *testing.T) {
	// Arrange: a serving control plane holding two Runs and a sealed provider
	// credential.
	original := filepath.Join(t.TempDir(), "mercator.db")
	live := serveDatabase(t, "file:"+original, hex.EncodeToString(retiredMasterKey))
	firstRun := live.createRun(t, "backup-drill-1")
	secondRun := live.createRun(t, "backup-drill-2")
	live.post(t, "/v1/connections", "backup-drill-connection", map[string]any{
		"connection_id": "runpod",
		"adapter_type":  "runpod",
		"credential":    map[string]any{"source": "mercator"},
		"secret":        "rpa_backed_up",
	})

	// Act: back the database up with the shipped command, while the server is
	// still serving from it.
	backup := filepath.Join(t.TempDir(), "mercator-backup.db")
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	exitCode := run(context.Background(), []string{"mercator", "backup", backup}, map[string]string{
		"MERCATOR_SQLITE_DSN": "file:" + original,
	}, stdout, stderr)
	if exitCode != 0 {
		t.Fatalf("backup exited %d: %s", exitCode, stderr.String())
	}
	live.stop()

	// Assert: a control plane booted on the copy alone serves the same Runs, and
	// the copy needs no companion -wal or -shm file to do it.
	if _, err := os.Stat(backup + "-wal"); err == nil {
		t.Fatalf("the backup left a write-ahead log beside it, so the file alone is not the backup")
	}
	restored := serveDatabase(t, "file:"+backup, hex.EncodeToString(retiredMasterKey))
	restoredRuns := restored.runIDs(t)
	if len(restoredRuns) != 2 || restoredRuns[0] != firstRun || restoredRuns[1] != secondRun {
		t.Fatalf("the restored control plane lists Runs %v, want [%s %s]", restoredRuns, firstRun, secondRun)
	}
	// A server that could not open a sealed credential refuses to start, so this
	// also states that the credential travelled with the events.
	if connections := restored.get(t, "/v1/connections"); !strings.Contains(connections, `"runpod"`) {
		t.Fatalf("restored connections = %s, want the runpod connection", connections)
	}
}

// TestBackupRefusesToOverwriteAnythingAlreadyThere keeps one mistake from
// costing the only backup there is.
//
// It asks about a truncated file as well as a whole one, because those fail
// differently underneath. SQLite refuses a destination it can read as a
// database, which is yesterday's backup; a file too short to be one it
// overwrites without a word, and a copy truncated by a full disk is exactly the
// path an operator retries over. Mercator takes the destination itself so that
// both are the same refusal.
func TestBackupRefusesToOverwriteAnythingAlreadyThere(t *testing.T) {
	for _, existing := range []struct {
		name    string
		content []byte
	}{
		{"a whole backup", nil},
		{"a backup a full disk truncated", []byte("S")},
	} {
		t.Run(existing.name, func(t *testing.T) {
			// Arrange
			directory := t.TempDir()
			original := filepath.Join(directory, "mercator.db")
			live := serveDatabase(t, "file:"+original, hex.EncodeToString(retiredMasterKey))
			live.createRun(t, "overwrite-drill")
			live.stop()
			yesterday := filepath.Join(directory, "yesterday.db")
			writeEarlierBackup(t, original, yesterday, existing.content)
			before := contentOf(t, yesterday)

			// Act
			stderr := &bytes.Buffer{}
			exitCode := run(context.Background(), []string{"mercator", "backup", yesterday}, map[string]string{
				"MERCATOR_SQLITE_DSN": "file:" + original,
			}, io.Discard, stderr)

			// Assert
			if exitCode != 1 {
				t.Fatalf("backup exited %d, want 1", exitCode)
			}
			if after := contentOf(t, yesterday); !bytes.Equal(after, before) {
				t.Fatalf("the earlier backup is %d bytes, want the %d it was", len(after), len(before))
			}
			if !strings.Contains(stderr.String(), "file exists") {
				t.Fatalf("backup said %q, want the destination named as already there", stderr.String())
			}
		})
	}
}

// TestABackupIsWrittenToTheFileTheOperatorNamed is the destination read twice.
//
// Mercator takes the destination with O_EXCL and then hands the same string to
// SQLite, and the two do not resolve it the same way: SQLite reads a name
// beginning with "file:" as a URI. `mercator backup file:latest.db` therefore
// claimed an empty file literally called "file:latest.db" and wrote the copy
// over "latest.db", so both of the claim's guarantees went at once. The
// truncated file the claim exists to protect was destroyed without a word, the
// copy holding every event and every sealed credential was created against the
// umask instead of 0600, and the restore line the command printed named the
// empty file rather than the backup. It exited 0.
func TestABackupIsWrittenToTheFileTheOperatorNamed(t *testing.T) {
	// Arrange: a truncated earlier backup at the path SQLite would resolve the
	// destination to, and a working directory to name it relative to.
	directory := t.TempDir()
	original := filepath.Join(directory, "mercator.db")
	serveDatabase(t, "file:"+original, hex.EncodeToString(retiredMasterKey)).stop()
	truncated := filepath.Join(directory, "latest.db")
	if err := os.WriteFile(truncated, []byte("S"), 0o600); err != nil {
		t.Fatalf("write the truncated earlier backup: %v", err)
	}
	t.Chdir(directory)

	// Act
	stdout := &bytes.Buffer{}
	exitCode := run(context.Background(), []string{"mercator", "backup", "file:latest.db"}, map[string]string{
		"MERCATOR_SQLITE_DSN": "file:" + original,
	}, stdout, io.Discard)
	if exitCode != 0 {
		t.Fatalf("backup exited %d, want 0", exitCode)
	}

	// Assert: the file the operator named holds the copy, and the one SQLite
	// would have read the name as was not touched.
	named := filepath.Join(directory, "file:latest.db")
	info, err := os.Stat(named)
	if err != nil {
		t.Fatalf("stat the named destination: %v", err)
	}
	if info.Size() == 0 {
		t.Fatalf("%s is empty, so the copy went somewhere else", named)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("the backup is mode %#o, want 0600", mode)
	}
	if content := contentOf(t, truncated); !bytes.Equal(content, []byte("S")) {
		t.Fatalf("the earlier backup is %d bytes, want the 1 it was", len(content))
	}

	// Assert: what the operator is told to restore is the file that was written.
	if !strings.Contains(stdout.String(), "MERCATOR_SQLITE_DSN=file:"+named) {
		t.Fatalf("backup said %q, want the restore to name %s", stdout.String(), named)
	}
}

// TestABackupThatCannotReadTheDatabaseLeavesTheDestinationFree is what happens
// after a failure, which is the only thing that decides whether a deployment
// ever takes another backup.
//
// Mercator claims the destination before it opens anything, so every failure
// after that point owns an empty file. Only the copy's own failure used to
// remove it: a source the invoking account cannot open, which is a cron job not
// running as the server's user, failed one step earlier and left the claim on
// disk. Fixing the permissions and running the same command again was then
// refused with "file exists", which the recovery documentation teaches the
// operator to read as an earlier backup already being there.
func TestABackupThatCannotReadTheDatabaseLeavesTheDestinationFree(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a file whatever its mode says, so there is no unreadable database to arrange")
	}
	// Arrange: a database that is there and cannot be opened.
	directory := t.TempDir()
	original := filepath.Join(directory, "mercator.db")
	serveDatabase(t, "file:"+original, hex.EncodeToString(retiredMasterKey)).stop()
	if err := os.Chmod(original, 0o000); err != nil {
		t.Fatalf("make the database unreadable: %v", err)
	}
	destination := filepath.Join(directory, "latest.db")

	// Act
	stderr := &bytes.Buffer{}
	exitCode := run(context.Background(), []string{"mercator", "backup", destination}, map[string]string{
		"MERCATOR_SQLITE_DSN": "file:" + original,
	}, io.Discard, stderr)

	// Assert
	if exitCode != 1 {
		t.Fatalf("backup exited %d, want 1", exitCode)
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("the failed backup left a file at %s, which refuses every retry at that path", destination)
	}

	// Assert: the retry an operator makes after fixing the cause is taken.
	if err := os.Chmod(original, 0o600); err != nil {
		t.Fatalf("restore the database's permissions: %v", err)
	}
	if exitCode := run(context.Background(), []string{"mercator", "backup", destination}, map[string]string{
		"MERCATOR_SQLITE_DSN": "file:" + original,
	}, io.Discard, stderr); exitCode != 0 {
		t.Fatalf("the retry exited %d: %s", exitCode, stderr.String())
	}
}

// TestABackupIsReadableOnlyByTheAccountThatTookIt states what a backup is: every
// event and the sealed bytes of every stored provider credential. It is not a
// file to leave at whatever the process umask happens to be.
func TestABackupIsReadableOnlyByTheAccountThatTookIt(t *testing.T) {
	// Arrange
	directory := t.TempDir()
	original := filepath.Join(directory, "mercator.db")
	serveDatabase(t, "file:"+original, hex.EncodeToString(retiredMasterKey)).stop()

	// Act
	backup := filepath.Join(directory, "mercator-backup.db")
	if exitCode := run(context.Background(), []string{"mercator", "backup", backup}, map[string]string{
		"MERCATOR_SQLITE_DSN": "file:" + original,
	}, io.Discard, io.Discard); exitCode != 0 {
		t.Fatalf("backup exited %d, want 0", exitCode)
	}

	// Assert
	info, err := os.Stat(backup)
	if err != nil {
		t.Fatalf("stat the backup: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("the backup is mode %#o, want 0600", mode)
	}
}

// writeEarlierBackup puts yesterday's copy where today's is about to be asked
// for. A nil content means a whole one, taken the way the command takes them.
func writeEarlierBackup(t *testing.T, original, path string, content []byte) {
	t.Helper()
	if content != nil {
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatalf("write the earlier backup: %v", err)
		}
		return
	}
	if _, err := sqlitestore.BackupDatabase(t.Context(), original, path); err != nil {
		t.Fatalf("take the earlier backup: %v", err)
	}
}

func contentOf(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return content
}

// TestBackupRefusesToGuessWhichDatabaseToCopy is the cron entry that does not
// inherit the unit's environment, which is the ordinary way an operator gets
// here rather than an exotic one.
//
// `serve` resolves an unset MERCATOR_SQLITE_DSN to a per-user data directory
// and creates it, so a backup that reused that fallback copied whatever
// database a `mercator serve` on this host had once left in the invoking
// account's home directory. It exited 0, wrote a file the size of a real
// backup, and said which database it had read on standard output, which is the
// stream a cron job discards. Asking whether a file is there cannot tell that
// database from the server's, so the variable is required for this command.
func TestBackupRefusesToGuessWhichDatabaseToCopy(t *testing.T) {
	// Arrange: a database sitting at the path an unset DSN resolves to, which is
	// what one earlier `mercator serve` without the variable leaves behind.
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "mercator"), 0o700); err != nil {
		t.Fatalf("make the per-user data directory: %v", err)
	}
	stray := filepath.Join(home, "mercator", "mercator.db")
	serveDatabase(t, "file:"+stray, hex.EncodeToString(retiredMasterKey)).stop()
	destination := filepath.Join(t.TempDir(), "nightly.db")

	// Act: the backup a cron entry runs, carrying no MERCATOR_SQLITE_DSN.
	stderr := &bytes.Buffer{}
	exitCode := run(context.Background(), []string{"mercator", "backup", destination}, map[string]string{
		"XDG_DATA_HOME": home,
	}, io.Discard, stderr)

	// Assert
	if exitCode != 1 {
		t.Fatalf("backup exited %d, want 1", exitCode)
	}
	if !strings.Contains(stderr.String(), "MERCATOR_SQLITE_DSN is required") {
		t.Fatalf("backup said %q, want the variable it will not guess named", stderr.String())
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("backup wrote %s from a database nobody named", destination)
	}
}

// TestABackupIsTakenFromADatabaseNamedRelatively is the deployment whose DSN
// names its database from the directory the server runs in, which serves
// normally and used to be the one deployment that could not be backed up.
//
// The source is handed to SQLite as a URI, and a relative path rendered into
// one carries its first segment as an authority: `MERCATOR_SQLITE_DSN=file:
// mercator.db mercator backup latest.db` failed with "invalid uri authority:
// mercator.db?mode=ro", naming a string the operator never typed.
func TestABackupIsTakenFromADatabaseNamedRelatively(t *testing.T) {
	// Arrange: a serving deployment whose database is named relatively.
	directory := t.TempDir()
	t.Chdir(directory)
	live := serveDatabase(t, "file:mercator.db", hex.EncodeToString(retiredMasterKey))
	requested := live.createRun(t, "relative-dsn-drill")
	live.stop()

	// Act
	stderr := &bytes.Buffer{}
	exitCode := run(context.Background(), []string{"mercator", "backup", "latest.db"}, map[string]string{
		"MERCATOR_SQLITE_DSN": "file:mercator.db",
	}, io.Discard, stderr)
	if exitCode != 0 {
		t.Fatalf("backup exited %d: %s", exitCode, stderr.String())
	}

	// Assert: what it wrote is a database a control plane serves the Run from,
	// which is the only statement worth making about a copy.
	restored := serveDatabase(t, "file:"+filepath.Join(directory, "latest.db"), hex.EncodeToString(retiredMasterKey))
	if runs := restored.runIDs(t); len(runs) != 1 || runs[0] != requested {
		t.Fatalf("the restored control plane lists Runs %v, want [%s]", runs, requested)
	}
}

// TestBackupRefusesADatabaseThatIsNotThere is the shell that carries a DSN
// naming a database that is not there. Left alone, that backup would create an
// empty database, copy the nothing in it and exit 0, and the operator would be
// holding a file that restores into a control plane with no history.
func TestBackupRefusesADatabaseThatIsNotThere(t *testing.T) {
	// Arrange
	absent := filepath.Join(t.TempDir(), "not-the-servers.db")

	// Act
	stderr := &bytes.Buffer{}
	exitCode := run(context.Background(), []string{"mercator", "backup", filepath.Join(t.TempDir(), "backup.db")}, map[string]string{
		"MERCATOR_SQLITE_DSN": "file:" + absent,
	}, io.Discard, stderr)

	// Assert
	if exitCode != 1 {
		t.Fatalf("backup exited %d, want 1", exitCode)
	}
	if !strings.Contains(stderr.String(), "does not exist") {
		t.Fatalf("backup said %q, want the absent database named", stderr.String())
	}
	if _, err := os.Stat(absent); err == nil {
		t.Fatalf("backup created the database it was asked to copy")
	}
}

// createRun submits one digest-pinned workload and answers with the Run's ID.
// Nothing has to place it: this case is about what a copy of the database holds,
// and a requested Run is already a row in the log and in the Run projection.
func (s *mercatorServer) createRun(t *testing.T, name string) string {
	t.Helper()
	answered := s.post(t, "/v1/runs", "create-"+name, map[string]any{
		"workload": map[string]any{
			"id":          "wlr_" + name,
			"workload_id": "wl_" + name,
			"digest":      "sha256:1111111111111111111111111111111111111111111111111111111111111111",
			"spec": map[string]any{
				"containers": []map[string]any{{
					"name":     "main",
					"image":    "ghcr.io/acme/trainer@sha256:0000000000000000000000000000000000000000000000000000000000000000",
					"platform": map[string]any{"os": "linux", "architecture": "amd64"},
				}},
				"resources": map[string]any{
					"cpu":            map[string]any{"min_millis": 1000},
					"memory":         map[string]any{"min_bytes": 1 << 30},
					"ephemeral_disk": map[string]any{"min_bytes": 1 << 30},
				},
				"network":   map[string]any{"inbound": "none"},
				"placement": map[string]any{"service_class": "standard", "expected_runtime_seconds": 60},
				"execution": map[string]any{"max_runtime_seconds": 600, "max_pre_start_attempts": 1},
			},
		},
	})
	var created struct {
		Run struct {
			ID string `json:"id"`
		} `json:"run"`
	}
	if err := json.Unmarshal([]byte(answered), &created); err != nil {
		t.Fatalf("decode the created Run: %v", err)
	}
	if created.Run.ID == "" {
		t.Fatalf("create run answered %s, want a Run id", answered)
	}
	return created.Run.ID
}

// runIDs is what GET /v1/runs serves, which is the stored Run projection rather
// than the event log it was reduced from. A restore that brought the events and
// not the projection would answer with nothing here.
func (s *mercatorServer) runIDs(t *testing.T) []string {
	t.Helper()
	var listed struct {
		Runs []struct {
			ID string `json:"id"`
		} `json:"runs"`
	}
	answered := s.get(t, "/v1/runs")
	if err := json.Unmarshal([]byte(answered), &listed); err != nil {
		t.Fatalf("decode the listed Runs: %v", err)
	}
	ids := make([]string, 0, len(listed.Runs))
	for _, listedRun := range listed.Runs {
		ids = append(ids, listedRun.ID)
	}
	return ids
}
