package daemon_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/broker"
	"github.com/benngarcia/mercator/internal/capability"
	"github.com/benngarcia/mercator/internal/daemon"
	"github.com/benngarcia/mercator/internal/domain"
)

// TestACapacityConnectionSellsMachinesThroughTheProductionControlPlane holds the
// capacity contract through the real daemon: real SQLite, the real connection
// registry, the real HTTP API, and the node registry the production wiring builds.
//
// The provider here allocates machines and executes nothing, which is the only
// shape a provider adapter can have, and until this slice the control plane could
// not talk to one at all: aggregation and the ownership sweep demanded an
// EphemeralExecutor, so authorizing such a connection published no offer and
// failed every sweep of the workspace. The lane its listings carry is earned from
// the deployment's own enrolled node runtime, which every real daemon has whether
// or not a machine has enrolled yet.
func TestACapacityConnectionSellsMachinesThroughTheProductionControlPlane(t *testing.T) {
	// No Docker on PATH, so the daemon seeds no local connection and this
	// provider is the only capacity in the workspace.
	t.Setenv("PATH", t.TempDir())
	machines := &machineProvider{
		listed: []domain.OfferSnapshot{{
			ID:        "i-held",
			Kind:      domain.OfferKindStanding,
			NativeRef: "i-held",
			MachineID: "i-held",
			ExpiresAt: time.Now().Add(time.Hour).UTC(),
			Platform:  domain.Platform{OS: "linux", Architecture: "amd64"},
			Resources: domain.ResourceInventory{CPUMillis: 8000, MemoryBytes: 32 << 30},
			Pricing:   domain.PriceModel{Currency: "USD", RatePerSecondUSD: 0.000417, Known: true},
		}},
		held: []capability.OwnedCapacity{{
			NativeRef:      "i-held",
			WorkspaceID:    daemon.DefaultWorkspaceID,
			OwnershipToken: "own_held",
			State:          capability.CapacityStateActive,
		}},
	}
	harness, runtime := startDaemonServing(t, machines)

	harness.call(t, http.MethodPost, "/v1/connections", map[string]any{
		"workspace_id":  daemon.DefaultWorkspaceID,
		"connection_id": "conn_machines",
		"adapter_type":  "machines",
	}, nil, http.StatusCreated)
	harness.call(t, http.MethodPost,
		"/v1/connections/conn_machines/authorize?workspace_id="+daemon.DefaultWorkspaceID,
		nil, nil, http.StatusOK)

	listed := harness.offers(t)
	if len(listed) != 1 {
		t.Fatalf("offers = %+v, want the one machine the provider listed", listed)
	}
	if listed[0].Lane != string(domain.LaneReusable) {
		t.Fatalf("lane = %q, want %q from a machine the deployment's node runtime can execute on", listed[0].Lane, domain.LaneReusable)
	}
	owned, err := runtime.ListOwned(t.Context(), daemon.DefaultWorkspaceID)
	if err != nil {
		t.Fatalf("sweep a workspace whose only connection sells capacity: %v", err)
	}
	if len(owned) != 1 || owned[0].ExternalID != "i-held" {
		t.Fatalf("owned = %+v, want the machine the connection holds", owned)
	}
	if owned[0].ConnectionID != "conn_machines" {
		t.Fatalf("machine = %+v, want the connection it was listed through", owned[0])
	}
}

// startDaemonServing starts the production runtime with one capacity provider in
// its catalog, and answers with a client for its API and the runtime itself,
// because the ownership sweep is a runtime call rather than an endpoint.
func startDaemonServing(t *testing.T, provider capability.Backend) (*fleet, *daemon.Runtime) {
	t.Helper()
	factory := broker.NewFactory()
	factory.Register(adapter.Manifest{Type: "machines"}, func(map[string]string, string) (capability.Backend, error) {
		return provider, nil
	})
	runtime, err := daemon.New(t.Context(), daemon.Config{
		SQLiteDSN:       "file:" + filepath.Join(t.TempDir(), "mercator.db"),
		OperatorToken:   "operator-token",
		MasterKey:       []byte("0123456789abcdef0123456789abcdef"),
		ProviderFactory: factory,
		Getenv:          func(string) string { return "" },
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
	return &fleet{token: "operator-token", address: listener.Addr().String()}, runtime
}

// machineProvider allocates machines and executes nothing. It implements the
// capacity contract only, and embeds it so a call no case here makes panics rather
// than answering quietly.
type machineProvider struct {
	capability.CapacityProvider
	listed []domain.OfferSnapshot
	held   []capability.OwnedCapacity
}

func (*machineProvider) CapacitySupport() capability.CapacitySupport {
	return capability.CapacitySupport{
		Stop:                true,
		Resume:              true,
		PersistentDisk:      true,
		ExactPricing:        true,
		IdempotentProvision: capability.IdempotentProvisionOperationKey,
		ListOwned:           true,
	}
}

func (*machineProvider) Verify(context.Context) error { return nil }

func (provider *machineProvider) ListCapacity(context.Context, capability.CapacityQuery) ([]domain.OfferSnapshot, error) {
	return provider.listed, nil
}

func (provider *machineProvider) ListOwnedCapacity(context.Context, capability.OwnershipQuery) ([]capability.OwnedCapacity, error) {
	return provider.held, nil
}
