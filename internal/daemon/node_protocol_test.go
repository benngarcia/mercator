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
	address, _ := startRuntime(t)

	cases := map[string]struct {
		path       string
		token      string
		wantStatus int
	}{
		"enrollment without valid material is refused rather than missing": {
			path:       "/v1/node-agent/enroll",
			wantStatus: http.StatusUnauthorized,
		},
		"a session needs a node credential": {
			path:       "/v1/node-agent/nod_unknown/session",
			wantStatus: http.StatusUnauthorized,
		},
		"the operator token does not authenticate a node session": {
			path:       "/v1/node-agent/nod_unknown/session",
			token:      "operator-token",
			wantStatus: http.StatusUnauthorized,
		},
		"the operator token does not let a caller report node events": {
			path:       "/v1/node-agent/nod_unknown/events",
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

func startRuntime(t *testing.T) (string, *daemon.Runtime) {
	t.Helper()
	return startRuntimeWithLease(t, 0)
}

// startRuntimeWithLease answers with the address a client reaches this daemon on
// and the runtime itself. The runtime is what a case drives the reconcile sweep
// through: preparation is a controller rather than a request, so nothing an HTTP
// caller can do makes it happen, and waiting out the production minute would be
// a test of a ticker.
func startRuntimeWithLease(t *testing.T, lease time.Duration) (string, *daemon.Runtime) {
	t.Helper()
	runtime, err := daemon.New(t.Context(), daemon.Config{
		SQLiteDSN:     "file:" + filepath.Join(t.TempDir(), "mercator.db"),
		OperatorToken: "operator-token",
		MasterKey:     []byte("0123456789abcdef0123456789abcdef"),
		NodeLease:     lease,
		// An empty environment keeps the daemon off this machine's Docker
		// credentials: a test registry is anonymous, and reading the developer's
		// config.json would make the result depend on who ran the suite.
		Getenv: func(string) string { return "" },
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
		ctx, cancel := context.WithTimeout(context.Background(), shutdownWindow)
		defer cancel()
		if err := runtime.Shutdown(ctx); err != nil {
			t.Fatalf("shutdown runtime: %v", err)
		}
		if err := <-served; err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("serve returned: %v", err)
		}
	})
	return listener.Addr().String(), runtime
}

// shutdownWindow is what a case gives the daemon to drain, and it is the window
// the production binary gives it rather than a tighter one.
//
// A tighter one is not a claim about Mercator. net/http keeps a connection that
// was accepted and has sent nothing out of its quiescent set for five seconds, so
// a client that is slow with its first header is not cut off, and an
// http.Transport dials speculatively: a node agent reporting every twenty
// milliseconds routinely leaves one such connection behind. A two second bound
// here was therefore an assertion about the standard library's own grace period,
// and it is the second half of why this package was intermittently red, at the
// same site and under whichever case happened to be last through the sweep.
const shutdownWindow = 15 * time.Second

// TestADaemonDrainsWhileANodeHoldsItsSessionOpen holds the shutdown the
// production binary depends on. A node session is a long-lived read the node
// holds open and the control plane writes down, and http.Server.Shutdown waits
// for active requests rather than cancelling them, so nothing in the tree ever
// ended one: a control plane with a single enrolled machine burned its whole
// shutdown window and then exited on a deadline it could not have met. That was
// the first half of why this package was intermittently red, because an agent
// whose connection happened to drop first hid it.
func TestADaemonDrainsWhileANodeHoldsItsSessionOpen(t *testing.T) {
	fleet := startFleet(t)

	ctx, cancel := context.WithTimeout(context.Background(), shutdownWindow)
	defer cancel()
	err := fleet.control.Shutdown(ctx)

	if err != nil {
		t.Fatalf("a control plane holding one node's session did not drain: %v", err)
	}
}
