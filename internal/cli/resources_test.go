package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// recorded captures one request the stub API server saw.
type recorded struct {
	Method         string
	Path           string
	IdempotencyKey string
	Body           map[string]any
}

// recordingServer returns 200 {} to everything and records each request.
func recordingServer(t *testing.T) (*httptest.Server, *[]recorded) {
	t.Helper()
	var seen []recorded
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entry := recorded{
			Method:         r.Method,
			Path:           r.URL.String(),
			IdempotencyKey: r.Header.Get("Idempotency-Key"),
		}
		if data, err := io.ReadAll(r.Body); err == nil && len(data) > 0 {
			_ = json.Unmarshal(data, &entry.Body)
		}
		seen = append(seen, entry)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(server.Close)
	return server, &seen
}

func TestFlagsWorkInAnyPosition(t *testing.T) {
	server, seen := recordingServer(t)
	cfg := Config{BaseURL: server.URL, ConfigPath: tempConfigPath(t)}

	// The historical trap: flags after the positional image were silently
	// passed to the container. They now parse as flags.
	code, _, errOut := runCLI(t, cfg,
		"run", "create", "busybox@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"--workspace-id", "ws_x", "--run-id", "run_flags")
	if code != 0 {
		t.Fatalf("trailing flags failed: %s", errOut)
	}
	last := (*seen)[len(*seen)-1]
	if last.Body["workspace_id"] != "ws_x" || last.Body["run_id"] != "run_flags" {
		t.Fatalf("flags after the image were not parsed: %+v", last.Body)
	}
	if _, hasArgs := last.Body["args"]; hasArgs {
		t.Fatalf("flags must not leak into container args: %+v", last.Body)
	}

	// Container args still pass verbatim after --.
	code, _, errOut = runCLI(t, cfg,
		"run", "create", "--workspace-id", "ws_x",
		"busybox@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"--", "echo", "--not-a-flag")
	if code != 0 {
		t.Fatalf("post-dash args failed: %s", errOut)
	}
	last = (*seen)[len(*seen)-1]
	args, _ := last.Body["args"].([]any)
	if len(args) != 2 || args[0] != "echo" || args[1] != "--not-a-flag" {
		t.Fatalf("container args after -- must pass verbatim: %+v", last.Body)
	}

	// The global --api-url works after the subcommand too.
	code, _, errOut = runCLI(t, Config{ConfigPath: tempConfigPath(t)},
		"run", "list", "--workspace-id", "ws_x", "--api-url", server.URL)
	if code != 0 {
		t.Fatalf("trailing --api-url failed: %s", errOut)
	}
}

func TestUnknownAndStrayArgumentsErrorLoudly(t *testing.T) {
	server, _ := recordingServer(t)
	cfg := Config{BaseURL: server.URL, ConfigPath: tempConfigPath(t)}

	// A misspelled flag is an error, not a silent container arg.
	code, _, errOut := runCLI(t, cfg, "run", "create", "busybox", "--workspace", "ws_x")
	if code == 0 || !strings.Contains(errOut, "not defined") {
		t.Fatalf("unknown flag must error loudly, got code=%d err=%q", code, errOut)
	}

	// A stray positional on a flags-only command is an error.
	code, _, errOut = runCLI(t, cfg, "run", "list", "extra", "--workspace-id", "ws_x")
	if code == 0 || !strings.Contains(errOut, "unexpected argument") {
		t.Fatalf("stray positional must error loudly, got code=%d err=%q", code, errOut)
	}
}

func TestConnectionCommandsBuildTheRightRequests(t *testing.T) {
	server, seen := recordingServer(t)
	cfg := Config{BaseURL: server.URL, WorkspaceID: "ws_1", ConfigPath: tempConfigPath(t)}

	code, _, errOut := runCLI(t, cfg, "connection", "create",
		"--connection-id", "conn_rp", "--adapter-type", "runpod",
		"--config", "region=us", "--config", "tier=secure",
		"--credential-source", "mercator", "--secret", "s3cret")
	if code != 0 {
		t.Fatalf("connection create failed: %s", errOut)
	}
	create := (*seen)[0]
	if create.Method != http.MethodPost || create.Path != "/v1/connections" {
		t.Fatalf("unexpected create request: %+v", create)
	}
	if create.IdempotencyKey != "connection:conn_rp:create" {
		t.Fatalf("expected derived idempotency key, got %q", create.IdempotencyKey)
	}
	config, _ := create.Body["config"].(map[string]any)
	if create.Body["adapter_type"] != "runpod" || config["region"] != "us" || config["tier"] != "secure" {
		t.Fatalf("unexpected create body: %+v", create.Body)
	}
	credential, _ := create.Body["credential"].(map[string]any)
	if credential["source"] != "mercator" || create.Body["secret"] != "s3cret" {
		t.Fatalf("unexpected credential in body: %+v", create.Body)
	}

	code, _, errOut = runCLI(t, cfg, "connection", "authorize", "--connection-id", "conn_rp")
	if code != 0 {
		t.Fatalf("connection authorize failed: %s", errOut)
	}
	authorize := (*seen)[1]
	if authorize.Method != http.MethodPost || authorize.Path != "/v1/connections/conn_rp:authorize?workspace_id=ws_1" {
		t.Fatalf("unexpected authorize request: %+v", authorize)
	}

	code, _, errOut = runCLI(t, cfg, "connection", "list")
	if code != 0 {
		t.Fatalf("connection list failed: %s", errOut)
	}
	list := (*seen)[2]
	if list.Method != http.MethodGet || list.Path != "/v1/connections?workspace_id=ws_1" {
		t.Fatalf("unexpected list request: %+v", list)
	}
}

func TestConnectionSecretFromStdin(t *testing.T) {
	server, seen := recordingServer(t)
	cfg := Config{
		BaseURL:     server.URL,
		WorkspaceID: "ws_1",
		ConfigPath:  tempConfigPath(t),
		Stdin:       bytes.NewBufferString("stdin-secret\n"),
	}
	code, _, errOut := runCLI(t, cfg, "connection", "create",
		"--connection-id", "conn_s", "--adapter-type", "runpod",
		"--credential-source", "mercator", "--secret-stdin")
	if code != 0 {
		t.Fatalf("secret-stdin create failed: %s", errOut)
	}
	if (*seen)[0].Body["secret"] != "stdin-secret" {
		t.Fatalf("stdin secret not forwarded (trailing newline should be trimmed): %+v", (*seen)[0].Body)
	}
}

func TestWorkloadCommandsBuildTheRightRequests(t *testing.T) {
	server, seen := recordingServer(t)
	cfg := Config{BaseURL: server.URL, WorkspaceID: "ws_1", ConfigPath: tempConfigPath(t)}

	code, _, errOut := runCLI(t, cfg, "workload", "create", "--workload-id", "wl_1", "--name", "trainer")
	if code != 0 {
		t.Fatalf("workload create failed: %s", errOut)
	}
	create := (*seen)[0]
	if create.Method != http.MethodPost || create.Path != "/v1/workloads" ||
		create.IdempotencyKey != "workload:wl_1:create" || create.Body["name"] != "trainer" {
		t.Fatalf("unexpected workload create: %+v", create)
	}

	code, _, errOut = runCLI(t, cfg, "workload", "revision", "create",
		"--workload-id", "wl_1", "--revision-json", `{"workspace_id":"ws_1"}`)
	if code != 0 {
		t.Fatalf("revision create failed: %s", errOut)
	}
	revision := (*seen)[1]
	if revision.Method != http.MethodPost || revision.Path != "/v1/workloads/wl_1/revisions?workspace_id=ws_1" {
		t.Fatalf("unexpected revision create: %+v", revision)
	}
	if revision.IdempotencyKey == "" {
		t.Fatalf("revision create must send an idempotency key")
	}

	code, _, errOut = runCLI(t, cfg, "workload", "revision", "list", "--workload-id", "wl_1")
	if code != 0 {
		t.Fatalf("revision list failed: %s", errOut)
	}
	if (*seen)[2].Path != "/v1/workloads/wl_1/revisions?workspace_id=ws_1" {
		t.Fatalf("unexpected revision list: %+v", (*seen)[2])
	}

	code, _, errOut = runCLI(t, cfg, "workload", "revision", "get", "--workload-id", "wl_1", "--revision-id", "wrev_9")
	if code != 0 {
		t.Fatalf("revision get failed: %s", errOut)
	}
	if (*seen)[3].Path != "/v1/workloads/wl_1/revisions/wrev_9?workspace_id=ws_1" {
		t.Fatalf("unexpected revision get: %+v", (*seen)[3])
	}
}

// The connection and workload commands drive the real handler end to end: a
// created connection is listable, a created workload accepts and lists a
// revision.
func TestConnectionAndWorkloadCommandsAgainstRealHandler(t *testing.T) {
	handler := newCLITestServer(t)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	cfg := Config{BaseURL: server.URL, WorkspaceID: "ws_1", ConfigPath: tempConfigPath(t)}

	code, _, errOut := runCLI(t, cfg, "connection", "create", "--connection-id", "conn_cli", "--adapter-type", "docker")
	if code != 0 {
		t.Fatalf("connection create failed: %s", errOut)
	}
	code, out, errOut := runCLI(t, cfg, "connection", "list")
	if code != 0 {
		t.Fatalf("connection list failed: %s", errOut)
	}
	var connections struct {
		Connections []struct {
			ID string `json:"id"`
		} `json:"connections"`
	}
	if err := json.Unmarshal([]byte(out), &connections); err != nil {
		t.Fatalf("connection list output not JSON: %q", out)
	}
	if len(connections.Connections) != 1 || connections.Connections[0].ID != "conn_cli" {
		t.Fatalf("created connection not listed: %+v", connections)
	}

	code, _, errOut = runCLI(t, cfg, "workload", "create", "--workload-id", "wl_cli", "--name", "cli test")
	if code != 0 {
		t.Fatalf("workload create failed: %s", errOut)
	}
	revision := mustJSON(t, cliRevision())
	code, _, errOut = runCLI(t, cfg, "workload", "revision", "create", "--workload-id", "wl_cli", "--revision-json", revision)
	if code != 0 {
		t.Fatalf("revision create failed: %s", errOut)
	}
	code, out, errOut = runCLI(t, cfg, "workload", "revision", "list", "--workload-id", "wl_cli")
	if code != 0 {
		t.Fatalf("revision list failed: %s", errOut)
	}
	var revisions struct {
		Revisions []struct {
			ID string `json:"id"`
		} `json:"revisions"`
	}
	if err := json.Unmarshal([]byte(out), &revisions); err != nil {
		t.Fatalf("revision list output not JSON: %q", out)
	}
	if len(revisions.Revisions) != 1 {
		t.Fatalf("created revision not listed: %+v", revisions)
	}
}
