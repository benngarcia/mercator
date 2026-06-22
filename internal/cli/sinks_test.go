package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSinkReplayCommandEmitsJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/sinks/audit:replay" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sink_id":"audit","delivered":1,"last_position":4,"replay_id":"replay_cli"}`))
	}))
	t.Cleanup(server.Close)

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), Config{
		BaseURL: server.URL,
		Args:    []string{"sink", "replay", "--sink-id", "audit", "--from", "0", "--limit", "10", "--replay-id", "replay_cli"},
		Stdout:  &stdout,
		Stderr:  &stderr,
	})
	if code != 0 {
		t.Fatalf("sink replay failed: code=%d stderr=%s", code, stderr.String())
	}
	var decoded map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("stdout was not json: %q: %v", stdout.String(), err)
	}
}
