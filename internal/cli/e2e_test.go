package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/bengarcia/mercator/internal/domain"
	"github.com/bengarcia/mercator/internal/httpapi"
)

func TestE2EFakeAdapterHTTPAndCLI(t *testing.T) {
	if os.Getenv("MERCATOR_E2E_FAKE") != "1" {
		t.Skip("set MERCATOR_E2E_FAKE=1 to run fake-adapter E2E")
	}
	handler, closeFn, err := httpapi.HandlerForSQLite(context.Background(), "file:"+t.Name()+"?mode=memory&cache=shared", []domain.OfferSnapshot{cliOffer()})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	t.Cleanup(func() {
		if err := closeFn(); err != nil {
			t.Fatalf("close handler: %v", err)
		}
	})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	workload := mustJSON(t, cliRevision())
	commands := [][]string{
		{"run", "create", "--workspace-id", "ws_1", "--run-id", "run_e2e", "--idempotency-key", "idem_e2e", "--workload-json", workload},
		{"run", "get", "--workspace-id", "ws_1", "--run-id", "run_e2e"},
		{"run", "events", "--workspace-id", "ws_1", "--run-id", "run_e2e"},
		{"run", "decision", "--workspace-id", "ws_1", "--run-id", "run_e2e"},
		{"run", "refresh", "--workspace-id", "ws_1", "--run-id", "run_e2e"},
		{"run", "cancel", "--workspace-id", "ws_1", "--run-id", "run_e2e"},
	}
	var last bytes.Buffer
	for _, args := range commands {
		var stdout, stderr bytes.Buffer
		code := Run(context.Background(), Config{BaseURL: server.URL, Args: args, Stdout: &stdout, Stderr: &stderr})
		if code != 0 {
			t.Fatalf("%v failed code=%d stdout=%s stderr=%s", args, code, stdout.String(), stderr.String())
		}
		if !json.Valid(stdout.Bytes()) {
			t.Fatalf("%v returned non-json stdout: %s", args, stdout.String())
		}
		last = stdout
	}
	if !strings.Contains(last.String(), `"run_e2e"`) {
		t.Fatalf("cancel response missing run id: %s", last.String())
	}
}
