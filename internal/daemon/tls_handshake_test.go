package daemon_test

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/daemon"
	"github.com/benngarcia/mercator/internal/tlsmaterial"
	"github.com/benngarcia/mercator/internal/tlsmaterial/tlsmaterialtest"
)

const handshakeMasterKey = "0123456789abcdef0123456789abcdef"

// TestTheDaemonTerminatesTLSItself boots the real production runtime with
// throwaway certificate material and completes a genuine handshake against it.
// The claim is that Mercator serves HTTPS from its own process rather than
// assuming a proxy in front, so nothing here is simulated: the client trusts
// only the certificate that was generated for this test, and it reads the
// negotiated protocol version off the connection it actually made.
func TestTheDaemonTerminatesTLSItself(t *testing.T) {
	// Arrange: a daemon holding a certificate issued moments ago.
	certFile, keyFile, pool := tlsmaterialtest.Issue(t, t.TempDir())
	address := serveRuntime(t, tlsmaterial.Material{CertFile: certFile, KeyFile: keyFile})
	client := &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}},
	}

	// Act
	response, err := client.Get("https://" + address + "/health/live")

	// Assert
	if err != nil {
		t.Fatalf("https GET /health/live: %v", err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	if response.TLS == nil {
		t.Fatal("response carried no TLS connection state")
	}
	if response.TLS.Version != tls.VersionTLS13 {
		t.Fatalf("negotiated version = %#x, want TLS 1.3 (%#x)", response.TLS.Version, tls.VersionTLS13)
	}
}

// TestAPlaintextRequestToTheTLSListenerIsRefused is the other half of the same
// claim: the port is not serving both. crypto/tls answers a plaintext request
// with a 400 in the clear and nothing else, so the request never reaches a
// route and the health handler never runs.
func TestAPlaintextRequestToTheTLSListenerIsRefused(t *testing.T) {
	// Arrange
	certFile, keyFile, _ := tlsmaterialtest.Issue(t, t.TempDir())
	address := serveRuntime(t, tlsmaterial.Material{CertFile: certFile, KeyFile: keyFile})

	// Act
	response, err := (&http.Client{Timeout: 10 * time.Second}).Get("http://" + address + "/health/live")

	// Assert
	if err != nil {
		return // A transport-level refusal is the same answer.
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("plaintext status = %d, want 400; the TLS listener must not serve the API in the clear", response.StatusCode)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read refusal body: %v", err)
	}
	if !strings.Contains(string(body), "HTTPS server") {
		t.Fatalf("refusal body = %q, want the HTTPS-server refusal", body)
	}
}

// TestAnUnreadableCertificateStopsStartup states the absence rule that replaces
// the old plaintext downgrade: material that is configured and unusable is a
// startup failure naming the file, never a server that came up without it.
func TestAnUnreadableCertificateStopsStartup(t *testing.T) {
	// Arrange: a certificate path pointing at a file that was never written.
	missing := filepath.Join(t.TempDir(), "mercator.crt")

	// Act
	runtime, err := daemon.New(t.Context(), daemon.Config{
		SQLiteDSN:     "file:" + filepath.Join(t.TempDir(), "mercator.db"),
		OperatorToken: "operator-token",
		MasterKey:     []byte(handshakeMasterKey),
		TLS:           tlsmaterial.Material{CertFile: missing, KeyFile: missing},
	})

	// Assert
	if runtime != nil {
		_ = runtime.Shutdown(t.Context())
		t.Fatal("a daemon must not start with a certificate it cannot read")
	}
	if err == nil || !strings.Contains(err.Error(), missing) {
		t.Fatalf("error = %v, want the certificate path %q", err, missing)
	}
}

// TestStartupRequiresTheMasterKey covers the third absence rule. Credential
// sealing, run-report tokens and node identity all derive from this key and all
// disable themselves without it, so a runtime that accepted its absence was a
// runtime that quietly turned three things off.
func TestStartupRequiresTheMasterKey(t *testing.T) {
	// Act
	runtime, err := daemon.New(t.Context(), daemon.Config{
		SQLiteDSN:     "file:" + filepath.Join(t.TempDir(), "mercator.db"),
		OperatorToken: "operator-token",
	})

	// Assert
	if runtime != nil {
		_ = runtime.Shutdown(t.Context())
		t.Fatal("a daemon must not start without a master key")
	}
	if err == nil || !strings.Contains(err.Error(), "MERCATOR_SECRET_KEY") {
		t.Fatalf("error = %v, want the missing variable named", err)
	}
}

// serveRuntime boots the real production graph on a loopback port of the
// kernel's choosing and answers with the address a client reaches it on.
func serveRuntime(t *testing.T, material tlsmaterial.Material) string {
	t.Helper()
	runtime, err := daemon.New(t.Context(), daemon.Config{
		SQLiteDSN:     "file:" + filepath.Join(t.TempDir(), "mercator.db"),
		OperatorToken: "operator-token",
		MasterKey:     []byte(handshakeMasterKey),
		TLS:           material,
		Getenv:        func(string) string { return "" },
	})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = runtime.Shutdown(t.Context())
		t.Fatalf("listen: %v", err)
	}
	served := make(chan error, 1)
	go func() { served <- runtime.Serve(listener) }()
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := runtime.Shutdown(shutdownCtx); err != nil {
			t.Errorf("shutdown runtime: %v", err)
		}
		if err := <-served; err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("serve: %v", err)
		}
	})
	return listener.Addr().String()
}
