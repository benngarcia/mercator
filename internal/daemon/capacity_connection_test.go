package daemon_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/broker"
	"github.com/benngarcia/mercator/internal/capability"
	"github.com/benngarcia/mercator/internal/daemon"
	"github.com/benngarcia/mercator/internal/domain"
)

// TestACapacityConnectionIsHeldByTheProductionControlPlane holds the capacity
// contract through the real daemon: real SQLite, the real connection registry, the
// real HTTP API, and the production wiring.
//
// The provider here allocates machines and executes nothing, which is the only
// shape a provider adapter can have, and until this slice the control plane could
// not talk to one at all: aggregation and the ownership sweep both demanded an
// EphemeralExecutor, so authorizing such a connection published no offer and
// failed every sweep of the workspace.
//
// What it does not do is publish a candidate. A machine no agent has enrolled on
// cannot execute a container, so a Run is told nothing can hold it rather than
// booked against a Rental Mercator does not hold on a host nothing can launch on.
//
// Nothing asks it, either, and the record says so where it says who was asked.
// `ListCapacity` has no caller on this path until mercator#200, so a decision
// naming this connection among the queried would be recording a question that was
// never put to the provider, which is the one thing the census exists to keep
// apart from a provider that answered with nothing.
func TestACapacityConnectionIsHeldByTheProductionControlPlane(t *testing.T) {
	// No Docker on PATH, so the daemon seeds no local connection and this
	// provider is the only capacity in the workspace.
	t.Setenv("PATH", t.TempDir())
	machines := &machineProvider{listed: []domain.OfferSnapshot{standingMachine()}}
	harness, _ := startDaemonServing(t, map[string]capability.Backend{"machines": machines})

	harness.authorize(t, "conn_machines", "machines")

	if listed := harness.offers(t); len(listed) != 0 {
		t.Fatalf("offers = %+v, want no candidate for a machine no agent is enrolled on", listed)
	}
	runID := harness.submitWorkload(t, func(name string) map[string]any {
		return workloadRevision(name, unreachableImage)
	})
	decision := harness.awaitDecision(t, runID)
	if slices.Contains(decision.CollectionReport.ConnectionsQueried, "conn_machines") {
		t.Errorf("connections queried = %v, want no claim that a provider nothing contacted was asked",
			decision.CollectionReport.ConnectionsQueried)
	}
	if !slices.ContainsFunc(decision.CollectionReport.ExcludedConnections, func(entry string) bool {
		return strings.HasPrefix(entry, "conn_machines: ")
	}) {
		t.Errorf("excluded connections = %v, want the capacity connection named with why nobody asked it",
			decision.CollectionReport.ExcludedConnections)
	}
	if asked := machines.listings.Load(); asked != 0 {
		t.Errorf("the placement read asked the provider for its machines %d time(s)", asked)
	}
	for _, candidate := range decision.Candidates {
		if candidate.NativeRef == "i-held" {
			t.Fatalf("a machine nothing can execute on was weighed as a candidate: %+v", candidate)
		}
	}
	if decision.Booking != nil {
		t.Fatalf("the Run was booked against %s, want no Booking at all", *decision.Booking)
	}
	if !slices.Contains(decision.SelectionReasonCodes, "NO_FEASIBLE_OFFERS") {
		t.Errorf("reason codes = %v, want the Run told nothing can hold it", decision.SelectionReasonCodes)
	}
}

// TestReconcilingAWorkspaceHoldingCapacityConvergesTheExecutionsThatLeaked drives
// the reconcile the daemon runs every minute, over a workspace that holds both
// kinds of connection: one that rents machines and one that runs one-shot
// executions and has leaked one.
//
// The sweep converges workloads. A capacity connection is running none, and asking
// it for the machines it holds read each one as capacity nobody could account for,
// wrote down a decision to destroy it, then failed to carry that decision out.
// Because the sweep stops at the first reclamation it cannot perform, the leaked
// execution was never reached and billed forever, and the recorded decision was
// sticky: every later sweep read it back and failed the same way.
func TestReconcilingAWorkspaceHoldingCapacityConvergesTheExecutionsThatLeaked(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	machines := &machineProvider{
		listed: []domain.OfferSnapshot{standingMachine()},
		held: []capability.OwnedCapacity{{
			NativeRef:      "i-held",
			WorkspaceID:    daemon.DefaultWorkspaceID,
			OwnershipToken: "own_held",
			State:          capability.CapacityStateActive,
		}},
	}
	pods := &oneShotExecutor{leaked: "pod_leaked"}
	harness, runtime := startDaemonServing(t, map[string]capability.Backend{
		// The capacity connection sorts first, which is the order that used to
		// abort the sweep before it reached the leak.
		"machines": machines,
		"zpool":    pods,
	})
	harness.authorize(t, "conn_machines", "machines")
	harness.authorize(t, "conn_zpool", "zpool")

	result, err := runtime.ReconcileWorkspace(t.Context(), daemon.DefaultWorkspaceID)

	if err != nil {
		t.Fatalf("reconcile a workspace holding a capacity connection: %v", err)
	}
	if result.Reclaimed != 1 {
		t.Fatalf("reclaimed %d, want the one leaked execution converged", result.Reclaimed)
	}
	if terminated := pods.terminated(); len(terminated) != 1 || terminated[0] != "lk_pod_leaked" {
		t.Fatalf("terminated %v, want only the leaked one-shot execution", terminated)
	}
	for _, object := range result.Owned {
		if object.ExternalID == "i-held" {
			t.Fatalf("a machine was reported to the workload sweep as %+v", object)
		}
	}
}

// standingMachine is a machine the provider already holds, stated as completely as
// a provider can state one: it says what the host is and what it costs, and says
// nothing about a container runtime, because a provider does not own one.
func standingMachine() domain.OfferSnapshot {
	return domain.OfferSnapshot{
		ID:        "i-held",
		Kind:      domain.OfferKindStanding,
		NativeRef: "i-held",
		MachineID: "i-held",
		ExpiresAt: time.Now().Add(time.Hour).UTC(),
		Platform:  domain.Platform{OS: "linux", Architecture: "amd64"},
		Resources: domain.ResourceInventory{
			CPUMillis:          8000,
			MemoryBytes:        32 << 30,
			EphemeralDiskBytes: 100 << 30,
			EphemeralDiskKnown: true,
		},
		Pricing: domain.PriceModel{Currency: "USD", RatePerSecondUSD: 0.000417, Known: true},
	}
}

// unreachableImage is a digest-pinned reference no registry here serves. A Run
// that is never placed never resolves it, which is the point: these cases are
// about which capacity Mercator will weigh, not about image resolution.
const unreachableImage = "registry.invalid/acme/trainer@sha256:" +
	"1111111111111111111111111111111111111111111111111111111111111111"

// authorize creates and authorizes one connection over the API, which is the only
// way a connection enters a real deployment.
func (f *fleet) authorize(t *testing.T, connectionID, adapterType string) {
	t.Helper()
	f.call(t, http.MethodPost, "/v1/connections", map[string]any{
		"workspace_id":  daemon.DefaultWorkspaceID,
		"connection_id": connectionID,
		"adapter_type":  adapterType,
	}, nil, http.StatusCreated)
	f.call(t, http.MethodPost,
		"/v1/connections/"+connectionID+"/authorize?workspace_id="+daemon.DefaultWorkspaceID,
		nil, nil, http.StatusOK)
}

// awaitDecision is the answer that stands for a Run once Placement has weighed the
// fleet for it, read off the same route an operator reads.
func (f *fleet) awaitDecision(t *testing.T, runID string) recordedDecision {
	t.Helper()
	var latest recordedDecision
	waitFor(t, func() bool {
		var response struct {
			Decisions []recordedDecision `json:"decisions"`
		}
		status := f.get(t, "/v1/runs/"+runID+"/decision?workspace_id="+daemon.DefaultWorkspaceID, &response)
		if status != http.StatusOK || len(response.Decisions) == 0 {
			return false
		}
		latest = response.Decisions[len(response.Decisions)-1]
		return true
	}, "Placement never recorded a decision for the Run")
	return latest
}

// recordedDecision is the durable Booking Decision as the API answers with it: the
// candidates Placement weighed and the Booking it committed, if any.
type recordedDecision struct {
	Candidates []struct {
		NativeRef   string `json:"native_ref"`
		Disposition string `json:"disposition"`
		Feasible    bool   `json:"feasible"`
	} `json:"candidates"`
	Booking              *json.RawMessage `json:"booking"`
	SelectionReasonCodes []string         `json:"selection_reason_codes"`
	// CollectionReport is which connections Placement asked and which it did not.
	// A capacity connection is named in the second list with the reason nobody
	// asked it: it is still in the workspace's fleet and an operator reading this
	// decision has to be able to see it, and a census that counted it among the
	// asked would state that a provider had been consulted about this Run when
	// nothing had contacted it.
	CollectionReport struct {
		ConnectionsQueried  []string `json:"connections_queried"`
		ExcludedConnections []string `json:"excluded_connections"`
	} `json:"collection_report"`
}

// startDaemonServing starts the production runtime with a catalog of connections,
// and answers with a client for its API and the runtime itself, because the
// ownership sweep is a runtime call rather than an endpoint.
func startDaemonServing(t *testing.T, backends map[string]capability.Backend) (*fleet, *daemon.Runtime) {
	t.Helper()
	factory := broker.NewFactory()
	for adapterType, backend := range backends {
		built := backend
		factory.Register(adapter.Manifest{Type: adapterType}, func(map[string]string, string) (capability.Backend, error) {
			return built, nil
		})
	}
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
	// listings counts how many times something asked this provider what it has to
	// rent. A placement read asks it none: the census names the connection as one
	// nobody asked, and a record that said otherwise would be reporting a question
	// no request was ever made of.
	listings atomic.Int64
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
	provider.listings.Add(1)
	return provider.listed, nil
}

func (provider *machineProvider) ListOwnedCapacity(context.Context, capability.OwnershipQuery) ([]capability.OwnedCapacity, error) {
	return provider.held, nil
}

// oneShotExecutor runs provider-native executions and has leaked one: a resource
// Mercator created and lost the response for, which bills until a sweep reclaims
// it. It is what the ownership sweep exists for.
type oneShotExecutor struct {
	capability.EphemeralExecutor
	leaked string

	mu   sync.Mutex
	gone []string
}

func (*oneShotExecutor) EphemeralSupport() capability.EphemeralSupport {
	return capability.EphemeralSupport{IdempotentLaunch: "launch_key", ListOwned: true}
}

func (*oneShotExecutor) Verify(context.Context) error { return nil }

func (*oneShotExecutor) ListOffers(context.Context, adapter.OfferRequest) ([]domain.OfferSnapshot, error) {
	return nil, nil
}

func (executor *oneShotExecutor) ListOwned(context.Context, adapter.OwnershipQuery) ([]adapter.OwnedExternalObject, error) {
	return []adapter.OwnedExternalObject{{
		ExternalID:  executor.leaked,
		WorkspaceID: daemon.DefaultWorkspaceID,
		LaunchKey:   "lk_" + executor.leaked,
	}}, nil
}

func (executor *oneShotExecutor) Terminate(_ context.Context, request adapter.TerminateRequest) (adapter.TerminateReceipt, error) {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	executor.gone = append(executor.gone, request.LaunchKey)
	return adapter.TerminateReceipt{Terminated: true}, nil
}

func (executor *oneShotExecutor) terminated() []string {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	return append([]string(nil), executor.gone...)
}
