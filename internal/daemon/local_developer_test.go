package daemon_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"path/filepath"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/daemon"
)

// `serve --dev` authenticates exactly one local operator and grants that session
// access to the deployment without requiring the machine bearer token.
func TestTheLocalDeveloperReachesTheirServer(t *testing.T) {
	// Arrange
	runtime, err := daemon.New(t.Context(), daemon.Config{
		SQLiteDSN:      "file:" + filepath.Join(t.TempDir(), "mercator.db"),
		OperatorToken:  "operator-token",
		MasterKey:      []byte("0123456789abcdef0123456789abcdef"),
		LocalAuthEmail: "developer@localhost",
	})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- runtime.Serve(listener) }()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := runtime.Shutdown(shutdownCtx); err != nil {
			t.Fatalf("shutdown runtime: %v", err)
		}
		if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("serve returned: %v", err)
		}
	}()

	// Act: the console's own flow. The browser asks who it is, --dev mints the
	// session in the answer, and every later request carries it. No bearer token
	// anywhere.
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("new cookie jar: %v", err)
	}
	browser := &http.Client{Jar: jar}
	signIn, err := browser.Get("http://" + listener.Addr().String() + "/auth/session")
	if err != nil {
		t.Fatalf("establish the local session: %v", err)
	}
	_ = signIn.Body.Close()
	response, err := browser.Get("http://" + listener.Addr().String() + "/v1/runs")
	if err != nil {
		t.Fatalf("read runs as the local developer: %v", err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatalf("read response: %v", err)
	}

	// Assert
	if response.StatusCode != http.StatusOK {
		t.Fatalf("the local developer reading the deployment = %d, want 200: %s", response.StatusCode, body)
	}
}
