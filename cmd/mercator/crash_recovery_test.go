package main

import (
	"bytes"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// TestAKilledContainerLeavesEverythingItAnsweredForOnDisk is the crash drill run
// against the shipped binary rather than against a library.
//
// The container is the point. `docker kill --signal=KILL` ends PID 1 from
// outside the process tree, with no shutdown hook, no deferred Close, and no
// chance for anything in the process to flush: it is the closest thing available
// to pulling the plug. The database lives on a bind mount, so what the host
// reads afterwards is what the killed process actually left on a filesystem.
//
// It asserts through the API on both sides. The write goes in over authenticated
// HTTP against a real `mercator serve`, and the survivor is read back by booting
// a second control plane on the same file, because "the row is in the table" is
// a weaker claim than "the control plane serves it".
func TestAKilledContainerLeavesEverythingItAnsweredForOnDisk(t *testing.T) {
	// Arrange: the shipped binary serving from a directory this host can read.
	requireDocker(t)
	binary := buildStaticMercator(t)
	directory := t.TempDir()
	container := runMercatorContainer(t, binary, directory)

	// Arrange: a connection whose credential the server sealed and answered for,
	// which is an append that has committed by the time the response arrives.
	container.post(t, "/v1/connections", map[string]any{
		"connection_id": "runpod",
		"adapter_type":  "runpod",
		"credential":    map[string]any{"source": "mercator"},
		"secret":        "rpa_survives_a_kill",
	})

	// Act
	container.kill(t)

	// Assert: the file is journalled the way recovery needs, read on a connection
	// that has not set the mode itself.
	dsn := "file:" + filepath.Join(directory, "mercator.db")
	if mode := journalMode(t, dsn); mode != "wal" {
		t.Errorf("the killed container left journal_mode %q, want wal", mode)
	}

	// Assert: a control plane boots on what it left and serves the write back.
	restarted := serveDatabase(t, dsn, hex.EncodeToString(retiredMasterKey))
	if connections := restarted.get(t, "/v1/connections"); !strings.Contains(connections, `"runpod"`) {
		t.Fatalf("after the kill the control plane serves %s, want the connection it had answered for", connections)
	}
}

// requireDocker states why a case cannot run rather than passing without having
// run. A crash drill that quietly skipped would be a green test proving nothing.
func requireDocker(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not on PATH, so this crash drill has no container to kill")
	}
	if err := exec.CommandContext(t.Context(), "docker", "info").Run(); err != nil {
		t.Skipf("docker is not answering (%v), so this crash drill has no container to kill", err)
	}
}

// buildStaticMercator builds the command under test for the container. CGO is
// off because Mercator's SQLite is pure Go, so the binary needs nothing from the
// image it runs in beyond a kernel.
func buildStaticMercator(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "mercator")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build mercator for the container: %v\n%s", err, output)
	}
	return binary
}

// mercatorContainer is one `mercator serve` running as PID 1 in a container,
// writing to a directory on this host.
type mercatorContainer struct {
	name    string
	baseURL string
}

// runMercatorContainer starts the server and returns once it answers.
//
// It shares this host's network namespace so the server can bind loopback,
// which is what lets the drill run without a certificate: Mercator refuses a
// non-loopback bind with no TLS material, and that refusal is not what this case
// is about. It runs as this user so the database it writes belongs to the host
// account that has to read it back.
func runMercatorContainer(t *testing.T, binary, directory string) *mercatorContainer {
	t.Helper()
	name := fmt.Sprintf("mercator-crash-%d", time.Now().UnixNano())
	address := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	started := exec.CommandContext(t.Context(), "docker", "run", "--detach",
		"--name", name,
		"--network", "host",
		"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		"--volume", binary+":/usr/local/bin/mercator:ro",
		"--volume", directory+":/data",
		"--env", "MERCATOR_ADDR="+address,
		"--env", "MERCATOR_API_TOKEN="+operatorToken,
		"--env", "MERCATOR_SECRET_KEY="+hex.EncodeToString(retiredMasterKey),
		"--env", "MERCATOR_SQLITE_DSN=file:/data/mercator.db",
		"alpine:3.22", "mercator", "serve")
	if output, err := started.CombinedOutput(); err != nil {
		t.Fatalf("start the mercator container: %v\n%s", err, output)
	}
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "--force", name).Run()
	})
	container := &mercatorContainer{name: name, baseURL: "http://" + address}
	container.awaitListening(t)
	return container
}

func (c *mercatorContainer) awaitListening(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get(c.baseURL + "/health/ready")
		if err == nil {
			_ = response.Body.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	logs, _ := exec.Command("docker", "logs", c.name).CombinedOutput()
	t.Fatalf("the mercator container never answered on %s\n%s", c.baseURL, logs)
}

func (c *mercatorContainer) post(t *testing.T, path string, body any) {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode %s body: %v", path, err)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, c.baseURL+path, bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("build POST %s: %v", path, err)
	}
	request.Header.Set("Authorization", "Bearer "+operatorToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "crash-drill-"+c.name)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode >= http.StatusBadRequest {
		t.Fatalf("POST %s = %d", path, response.StatusCode)
	}
}

// kill ends the container the way a machine ends a process it is not asking
// nicely, then states that this is what happened. Docker reports 137 for a
// process killed by signal 9, and an exit code of 0 would mean the server shut
// down cleanly and this case proved nothing.
func (c *mercatorContainer) kill(t *testing.T) {
	t.Helper()
	if output, err := exec.CommandContext(t.Context(), "docker", "kill", "--signal=KILL", c.name).CombinedOutput(); err != nil {
		t.Fatalf("kill the mercator container: %v\n%s", err, output)
	}
	waited, err := exec.CommandContext(t.Context(), "docker", "wait", c.name).Output()
	if err != nil {
		t.Fatalf("wait for the killed container: %v", err)
	}
	if code := strings.TrimSpace(string(waited)); code != "137" {
		t.Fatalf("the container exited %s, want 137, which is what a process killed by SIGKILL reports", code)
	}
}

// journalMode reads how the file on disk is journalled, on a connection that has
// set nothing. Asking through a connection that has just set WAL would report
// only what the question asked for.
func journalMode(t *testing.T, dsn string) string {
	t.Helper()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("reopen %s: %v", dsn, err)
	}
	defer func() { _ = db.Close() }()
	var mode string
	if err := db.QueryRowContext(t.Context(), `PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	return mode
}

// freePort asks the kernel for a port nothing is using, so a case does not pick
// one and hope.
func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find a free port: %v", err)
	}
	defer func() { _ = listener.Close() }()
	return listener.Addr().(*net.TCPAddr).Port
}
