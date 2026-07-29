package daemon_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/benngarcia/mercator/internal/daemon"
)

// TestAdministrativeOperationsAnswerOnlyOnTheAdministrativeListener puts one
// running daemon behind two addresses and asks the same question at both. The
// public address does not serve workspace creation at all, so it answers what
// it answers for any path it does not have.
func TestAdministrativeOperationsAnswerOnlyOnTheAdministrativeListener(t *testing.T) {
	// Arrange
	public, admin := startRuntimeWithAdminListener(t)

	// Act
	onThePublicListener := createWorkspace(t, public)
	onTheAdministrativeListener := createWorkspace(t, admin)
	aPathThisDeploymentDoesNotHave := getWithToken(t, public, "/v1/no-such-operation")
	withNoCredentials := call(t, newRequest(t, http.MethodPost, public, "/v1/workspaces", `{"display_name":"Production"}`))

	// Assert
	if onTheAdministrativeListener.status != http.StatusCreated {
		t.Fatalf("create workspace on the administrative listener = %d: %s", onTheAdministrativeListener.status, onTheAdministrativeListener.body)
	}
	if onThePublicListener.status != http.StatusNotFound {
		t.Fatalf("create workspace on the public listener = %d, want 404: %s", onThePublicListener.status, onThePublicListener.body)
	}
	if onThePublicListener.body != aPathThisDeploymentDoesNotHave.body {
		t.Fatalf("the public listener distinguishes an administrative route from a path it does not have:\n administrative: %q\n absent:         %q",
			onThePublicListener.body, aPathThisDeploymentDoesNotHave.body)
	}
	// The refusal is decided before authentication, so an unauthenticated probe
	// learns "no such operation" rather than "there is one, and you need a
	// token for it".
	if withNoCredentials.status != http.StatusNotFound {
		t.Fatalf("an unauthenticated create on the public listener = %d, want 404: %s", withNoCredentials.status, withNoCredentials.body)
	}
}

// TestOrdinaryOperationsStillAnswerOnThePublicListener is the other half: only
// the administrative operations moved.
func TestOrdinaryOperationsStillAnswerOnThePublicListener(t *testing.T) {
	// Arrange
	public, _ := startRuntimeWithAdminListener(t)

	// Act
	listed := getWithToken(t, public, "/v1/workspaces")

	// Assert
	if listed.status != http.StatusOK {
		t.Fatalf("list workspaces on the public listener = %d: %s", listed.status, listed.body)
	}
}

type httpAnswer struct {
	status int
	body   string
}

func startRuntimeWithAdminListener(t *testing.T) (string, string) {
	t.Helper()
	adminListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen administratively: %v", err)
	}
	runtime, err := daemon.New(t.Context(), daemon.Config{
		SQLiteDSN:     "file:" + filepath.Join(t.TempDir(), "mercator.db"),
		OperatorToken: "operator-token",
		MasterKey:     []byte("0123456789abcdef0123456789abcdef"),
		AdminAddr:     adminListener.Addr().String(),
		Getenv:        func(string) string { return "" },
	})
	if err != nil {
		_ = adminListener.Close()
		t.Fatalf("new runtime: %v", err)
	}
	publicListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen publicly: %v", err)
	}
	served := make(chan error, 2)
	go func() { served <- runtime.Serve(publicListener) }()
	go func() { served <- runtime.Serve(adminListener) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), shutdownWindow)
		defer cancel()
		if err := runtime.Shutdown(ctx); err != nil {
			t.Fatalf("shutdown runtime: %v", err)
		}
		for range 2 {
			if err := <-served; err != nil && !errors.Is(err, http.ErrServerClosed) {
				t.Fatalf("serve returned: %v", err)
			}
		}
	})
	return publicListener.Addr().String(), adminListener.Addr().String()
}

func createWorkspace(t *testing.T, address string) httpAnswer {
	t.Helper()
	return postJSON(t, address, "/v1/workspaces", `{"display_name":"Production"}`)
}

func postJSON(t *testing.T, address, path, body string) httpAnswer {
	t.Helper()
	return callWithToken(t, newRequest(t, http.MethodPost, address, path, body))
}

func getWithToken(t *testing.T, address, path string) httpAnswer {
	t.Helper()
	return callWithToken(t, newRequest(t, http.MethodGet, address, path, ""))
}

func newRequest(t *testing.T, method, address, path, body string) *http.Request {
	t.Helper()
	request, err := http.NewRequest(method, "http://"+address+path, bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	return request
}

func callWithToken(t *testing.T, request *http.Request) httpAnswer {
	t.Helper()
	request.Header.Set("Authorization", "Bearer operator-token")
	return call(t, request)
}

func call(t *testing.T, request *http.Request) httpAnswer {
	t.Helper()
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("call %s: %v", request.URL, err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read %s: %v", request.URL, err)
	}
	return httpAnswer{status: response.StatusCode, body: string(body)}
}
