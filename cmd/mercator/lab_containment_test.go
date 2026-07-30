package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/lab"
	"github.com/benngarcia/mercator/internal/scenario"
)

// The Lab restarts a simulated world and exports its whole event log through
// routes that exist nowhere else. Containment is a property of the server: no
// flag, no environment variable and no mistaken deployment puts it on an
// address another machine can reach. This is the caller's proof of that,
// written here because the Lab's own package is not this track's to add files
// to.
func TestTheLabRefusesAListenerAnotherMachineCouldReach(t *testing.T) {
	// Arrange
	server := openLabServer(t)
	exposed, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatalf("listen on every interface: %v", err)
	}
	defer func() { _ = exposed.Close() }()

	// Act
	served := make(chan error, 1)
	go func() { served <- server.Serve(exposed) }()

	// Assert
	select {
	case err := <-served:
		if err == nil || errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("the Lab served an address every interface can reach and then stopped: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the Lab is serving an address every interface can reach")
	}
}

func TestTheLabServesLoopback(t *testing.T) {
	// Arrange
	server := openLabServer(t)
	contained, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on loopback: %v", err)
	}

	// Act
	served := make(chan error, 1)
	go func() { served <- server.Serve(contained) }()

	// Assert
	response, err := http.Get("http://" + contained.Addr().String() + "/v1/lab/status")
	if err != nil {
		t.Fatalf("call the Lab: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("an unauthenticated Lab status = %s, want 401 from a Lab that is listening", response.Status)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown Lab: %v", err)
	}
	if err := <-served; err != nil && !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("serve returned: %v", err)
	}
}

func openLabServer(t *testing.T) *lab.Server {
	t.Helper()
	blueprint, err := scenario.LoadBlueprint(filepath.Join(
		"..", "..", "internal", "scenario", "scenarios", "demos", "artifact-warmth-restart.json",
	))
	if err != nil {
		t.Fatalf("load Blueprint: %v", err)
	}
	tape, samples, err := lab.Compile(blueprint, lab.CompileOptions{})
	if err != nil {
		t.Fatalf("compile Blueprint: %v", err)
	}
	server, err := lab.NewServer(t.Context(), lab.ServerConfig{
		Execution: lab.Config{
			Blueprint:        blueprint,
			Tape:             tape,
			Samples:          samples,
			Limits:           lab.DefaultLimits(),
			Policy:           "default",
			MercatorRevision: "lab-containment-test",
		},
		OperatorToken: "lab-containment-token",
	})
	if err != nil {
		t.Fatalf("open Lab server: %v", err)
	}
	return server
}

func TestTheLabRefusesAnEmptyOperatorToken(t *testing.T) {
	// Arrange, Act
	_, err := lab.NewServer(t.Context(), lab.ServerConfig{OperatorToken: ""})

	// Assert
	if err == nil {
		t.Fatal("a Lab with no operator token is a Lab anyone on this machine can drive")
	}
}
