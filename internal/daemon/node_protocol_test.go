package daemon_test

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/daemon"
)

// TestNodeProtocolIsMountedAndSeparateFromTheOperatorAPI holds the boundary
// that keeps node credentials and operator credentials from standing in for
// each other. The node routes exist, and the operator token opens none of them.
func TestNodeProtocolIsMountedAndSeparateFromTheOperatorAPI(t *testing.T) {
	address := startRuntime(t)

	cases := map[string]struct {
		path       string
		token      string
		wantStatus int
	}{
		"enrollment without valid material is refused rather than missing": {
			path:       "/v1/nodes/enroll",
			wantStatus: http.StatusUnauthorized,
		},
		"a session needs a node credential": {
			path:       "/v1/nodes/nod_unknown/session",
			wantStatus: http.StatusUnauthorized,
		},
		"the operator token does not authenticate a node session": {
			path:       "/v1/nodes/nod_unknown/session",
			token:      "operator-token",
			wantStatus: http.StatusUnauthorized,
		},
		"the operator token does not let a caller report node events": {
			path:       "/v1/nodes/nod_unknown/events",
			token:      "operator-token",
			wantStatus: http.StatusUnauthorized,
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			request, err := http.NewRequest(http.MethodPost, "http://"+address+testCase.path, bytes.NewReader([]byte(`{}`)))
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			request.Header.Set("Content-Type", "application/json")
			if testCase.token != "" {
				request.Header.Set("Authorization", "Bearer "+testCase.token)
			}

			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatalf("call %s: %v", testCase.path, err)
			}
			defer func() { _ = response.Body.Close() }()

			if response.StatusCode != testCase.wantStatus {
				t.Fatalf("%s = %d, want %d", testCase.path, response.StatusCode, testCase.wantStatus)
			}
		})
	}
}

func startRuntime(t *testing.T) string {
	t.Helper()
	runtime, err := daemon.New(t.Context(), daemon.Config{
		SQLiteDSN:     "file:" + filepath.Join(t.TempDir(), "mercator.db"),
		OperatorToken: "operator-token",
		MasterKey:     []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	served := make(chan error, 1)
	go func() { served <- runtime.Serve(listener) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := runtime.Shutdown(ctx); err != nil {
			t.Fatalf("shutdown runtime: %v", err)
		}
		if err := <-served; err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("serve returned: %v", err)
		}
	})
	return listener.Addr().String()
}
