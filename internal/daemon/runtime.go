// Package daemon composes and owns one production Mercator server runtime.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/broker"
	"github.com/benngarcia/mercator/internal/connection"
	"github.com/benngarcia/mercator/internal/credential"
	"github.com/benngarcia/mercator/internal/httpapi"
	"github.com/benngarcia/mercator/internal/janitor"
	"github.com/benngarcia/mercator/internal/ociresolver"
	"github.com/benngarcia/mercator/internal/orchestrator"
	"github.com/benngarcia/mercator/internal/providers"
	"github.com/benngarcia/mercator/internal/reporting"
	"github.com/benngarcia/mercator/internal/scheduler"
	"github.com/benngarcia/mercator/internal/sinks"
	sqlitestore "github.com/benngarcia/mercator/internal/storage/sqlite"
	"github.com/benngarcia/mercator/internal/webauth"
	"github.com/benngarcia/mercator/internal/workload"
)

// Config contains the typed inputs needed to construct a production runtime.
// The caller owns listener allocation, secret generation, and environment
// parsing. Getenv is retained only for connections that explicitly reference an
// environment-backed provider credential.
type Config struct {
	SQLiteDSN      string
	OperatorToken  string
	MasterKey      []byte
	PublicURL      string
	Getenv         func(string) string
	WebAuth        webauth.Config
	LocalAuthEmail string

	// ProviderFactory replaces the production catalog in lifecycle tests.
	// Production callers leave it nil.
	ProviderFactory *broker.Factory
}

// Runtime owns the production HTTP server, broker graph, reconciliation loop,
// and SQLite storage for one Mercator process.
type Runtime struct {
	server  *http.Server
	broker  *broker.Broker
	storage *sqlitestore.Storage
	orch    *orchestrator.Orchestrator
	janitor *janitor.Janitor

	stopReconcile context.CancelFunc
	reconcileDone chan struct{}

	shutdownOnce sync.Once
	shutdownErr  error
}

// New constructs the same production graph used by the daemon server. It does
// not bind a port; Serve accepts the listener selected by the caller.
func New(ctx context.Context, cfg Config) (_ *Runtime, err error) {
	if cfg.SQLiteDSN == "" {
		return nil, errors.New("daemon: SQLiteDSN is required")
	}
	if cfg.OperatorToken == "" {
		return nil, errors.New("daemon: OperatorToken is required")
	}
	if cfg.WebAuth.Enabled() && cfg.LocalAuthEmail != "" {
		return nil, errors.New("daemon: OIDC and local authentication cannot both be enabled")
	}

	storage, err := sqlitestore.Open(ctx, cfg.SQLiteDSN)
	if err != nil {
		return nil, fmt.Errorf("daemon: open sqlite storage: %w", err)
	}
	defer func() {
		if err != nil {
			_ = storage.Close()
		}
	}()

	logStore := storage.EventLog()
	credentialStore := storage.CredentialStore()
	migrated, err := credentialStore.MigrateSealKey(ctx, cfg.MasterKey)
	if err != nil {
		return nil, fmt.Errorf("daemon: credential store: %w", err)
	}
	if migrated > 0 {
		log.Printf("credential store: re-sealed %d credential(s) under the derived sealing key", migrated)
	}

	resolver := credential.NewResolver(cfg.Getenv, credentialStore, cfg.MasterKey)
	connections, err := storage.Connections(resolver)
	if err != nil {
		return nil, fmt.Errorf("daemon: init connection storage: %w", err)
	}
	connectionService := connection.NewWithCredentials(connections)
	factory := cfg.ProviderFactory
	if factory == nil {
		factory = providers.Factory()
	}
	providerBroker := broker.NewBroker(connectionService, factory, resolver)

	signer := reporting.NewSigner(reporting.DeriveKey(cfg.MasterKey))
	sched := scheduler.New()
	orchestratorOptions := []orchestrator.Option{}
	if signer.Enabled() && cfg.PublicURL != "" {
		orchestratorOptions = append(orchestratorOptions, orchestrator.WithReporting(cfg.PublicURL, signer))
	}
	orch := orchestrator.New(logStore, sched, providerBroker, orchestratorOptions...)

	serverOptions := []httpapi.Option{
		httpapi.WithBearerAuth(cfg.OperatorToken),
		httpapi.WithVerifier(providerBroker),
		httpapi.WithAdapterManifests(providerBroker.Manifests),
	}
	if signer.Enabled() {
		serverOptions = append(serverOptions, httpapi.WithReportSigner(signer))
	}
	if cfg.WebAuth.Enabled() {
		authenticator, authErr := webauth.New(ctx, cfg.WebAuth)
		if authErr != nil {
			return nil, fmt.Errorf("daemon: initialize OIDC login: %w", authErr)
		}
		serverOptions = append(serverOptions, httpapi.WithWebAuth(authenticator))
	} else if cfg.LocalAuthEmail != "" {
		authenticator, authErr := webauth.NewLocal(cfg.LocalAuthEmail)
		if authErr != nil {
			return nil, fmt.Errorf("daemon: initialize local login: %w", authErr)
		}
		serverOptions = append(serverOptions, httpapi.WithWebAuth(authenticator))
	}

	handler := httpapi.New(httpapi.Deps{
		Orchestrator: orch,
		Offers:       providerBroker,
		Workloads:    workload.New(logStore),
		Sinks:        sinks.NewManager(logStore, map[string]sinks.Sink{"audit": sinks.DiscardSink{}}),
		Connections:  connectionService,
		Resolver:     ociresolver.NewStaticResolver(nil),
		Workspaces:   storage.Workspaces(),
		Events:       logStore,
	}, serverOptions...)

	reconcileCtx, stopReconcile := context.WithCancel(ctx)
	workspaceJanitor := janitor.New(providerBroker, janitor.WithEventLog(logStore))
	runtime := &Runtime{
		server: &http.Server{
			Handler:           handler,
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       60 * time.Second,
			WriteTimeout:      90 * time.Second,
			IdleTimeout:       120 * time.Second,
		},
		broker:        providerBroker,
		storage:       storage,
		orch:          orch,
		janitor:       workspaceJanitor,
		stopReconcile: stopReconcile,
		reconcileDone: make(chan struct{}),
	}
	go runtime.reconcile(reconcileCtx)
	return runtime, nil
}

// Serve runs the production HTTP server on a listener allocated by the caller.
func (r *Runtime) Serve(listener net.Listener) error {
	if listener == nil {
		return errors.New("daemon: listener is required")
	}
	return r.server.Serve(listener)
}

// Shutdown drains HTTP requests, stops and joins background reconciliation,
// then closes SQLite. Repeated calls return the first shutdown result.
func (r *Runtime) Shutdown(ctx context.Context) error {
	r.shutdownOnce.Do(func() {
		httpErr := r.server.Shutdown(ctx)
		r.stopReconcile()
		<-r.reconcileDone
		storageErr := r.storage.Close()
		r.shutdownErr = errors.Join(httpErr, storageErr)
	})
	return r.shutdownErr
}

// ListOwned returns every external object owned by the workspace across its
// authorized connections.
func (r *Runtime) ListOwned(ctx context.Context, workspaceID string) ([]adapter.OwnedExternalObject, error) {
	return r.broker.ListOwned(ctx, adapter.OwnershipQuery{WorkspaceID: workspaceID})
}

type ReconcileResult struct {
	Advanced  orchestrator.AdvanceOpenRunsResult
	Reclaimed int
	Owned     []adapter.OwnedExternalObject
}

// ReconcileWorkspace drives run cleanup and orphan reclamation once, then
// returns the provider inventory observed after both paths run.
func (r *Runtime) ReconcileWorkspace(ctx context.Context, workspaceID string) (ReconcileResult, error) {
	advanced, advanceErr := r.orch.AdvanceOpenRuns(ctx, workspaceID)
	swept, sweepErr := r.janitor.Sweep(ctx, workspaceID)
	owned, inventoryErr := r.ListOwned(ctx, workspaceID)
	return ReconcileResult{Advanced: advanced, Reclaimed: swept.Released, Owned: owned}, errors.Join(advanceErr, sweepErr, inventoryErr)
}

func (r *Runtime) reconcile(ctx context.Context) {
	defer close(r.reconcileDone)
	const interval = time.Minute
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reconcileWorkspaces(ctx, r.orch, r.janitor)
		}
	}
}

func reconcileWorkspaces(ctx context.Context, orch *orchestrator.Orchestrator, jan *janitor.Janitor) {
	workspaces, err := orch.ListRunWorkspaces(ctx)
	if err != nil {
		log.Printf("discover run workspaces: %v", err)
		return
	}
	for _, workspaceID := range workspaces {
		advanced, err := orch.AdvanceOpenRuns(ctx, workspaceID)
		if err != nil {
			log.Printf("run advancement sweep %s: %v", workspaceID, err)
		}
		if advanced.Closed > 0 {
			log.Printf("run advancement sweep %s: closed %d of %d open runs", workspaceID, advanced.Closed, advanced.Open)
		}
		result, err := jan.Sweep(ctx, workspaceID)
		if err != nil {
			log.Printf("janitor sweep %s: %v", workspaceID, err)
			continue
		}
		if result.Released > 0 {
			log.Printf("janitor sweep %s: reclaimed %d of %d owned objects", workspaceID, result.Released, result.Found)
		}
	}
}
