// Package daemon composes and owns one production Mercator server runtime.
package daemon

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	dockeradapter "github.com/benngarcia/mercator/internal/adapter/docker"
	"github.com/benngarcia/mercator/internal/broker"
	"github.com/benngarcia/mercator/internal/connection"
	"github.com/benngarcia/mercator/internal/credential"
	"github.com/benngarcia/mercator/internal/eventlog"
	"github.com/benngarcia/mercator/internal/httpapi"
	"github.com/benngarcia/mercator/internal/janitor"
	"github.com/benngarcia/mercator/internal/node"
	"github.com/benngarcia/mercator/internal/nodeapi"
	"github.com/benngarcia/mercator/internal/ociresolver"
	"github.com/benngarcia/mercator/internal/orchestrator"
	"github.com/benngarcia/mercator/internal/providers"
	"github.com/benngarcia/mercator/internal/reporting"
	"github.com/benngarcia/mercator/internal/scheduler"
	"github.com/benngarcia/mercator/internal/sinks"
	sqlitestore "github.com/benngarcia/mercator/internal/storage/sqlite"
	"github.com/benngarcia/mercator/internal/tlsmaterial"
	"github.com/benngarcia/mercator/internal/webauth"
	"github.com/benngarcia/mercator/internal/workload"
	"github.com/benngarcia/mercator/internal/workspace"
)

// Config contains the typed inputs needed to construct a production runtime.
// The caller owns listener allocation, secret generation, and environment
// parsing. Getenv is retained only for connections that explicitly reference an
// environment-backed provider credential.
type Config struct {
	SQLiteDSN     string
	OperatorToken string
	// MasterKey is required. The credential sealing key, the run-report signing
	// key and the node identity signing key are all derived from it, and each
	// derivation answers an absent master key by disabling itself, so a runtime
	// built without one is a runtime with three security features silently off.
	MasterKey []byte
	// TLS names the certificate and key this server terminates TLS with. An
	// unconfigured Material serves plaintext, which only a loopback deployment
	// may do; the process entrypoint enforces that, because only it knows the
	// listen address.
	TLS tlsmaterial.Material
	// AdminAddr is the bound address of the administrative listener, wildcards
	// resolved. Workspace creation and archiving, node invitation and sink
	// delivery answer there and nowhere else. Empty serves every route on every
	// listener, which is what a single-listener loopback deployment wants; the
	// process entrypoint is what refuses a non-loopback deployment that has not
	// named one.
	AdminAddr      string
	PublicURL      string
	Getenv         func(string) string
	WebAuth        webauth.Config
	LocalAuthEmail string
	// AgentVersion is the node agent build a bootstrapped machine is asked to
	// run. It is recorded on every invitation, so an operator can see which
	// build a node was told to be and which one it turned out to be, and a
	// capacity provider substitutes it into the download URL the machine fetches
	// from. Empty is a deployment that has stated no build, which provisions no
	// machine: the refusal is at the provider, before anything is paid for.
	AgentVersion string
	// NodeLease is how long the control plane believes a node absent a
	// heartbeat. Zero takes the registry's default. Tests shorten it so lease
	// expiry is stated rather than waited for.
	NodeLease time.Duration
	// NodeSession is how long one node session credential stays valid before its
	// agent has to renew. Zero takes the registry's default. Tests shorten it so a
	// machine outliving its first session is stated rather than waited out.
	NodeSession time.Duration
	// Prewarm bounds the preparation this Mercator may have in flight for work
	// it has not admitted. Nil takes DefaultPrewarmPolicy.
	Prewarm *orchestrator.PrewarmPolicy

	// ProviderFactory replaces the production catalog in lifecycle tests.
	// Production callers leave it nil.
	ProviderFactory *broker.Factory
}

// DefaultPrewarmPolicy is deliberately the most restrained bound that does
// anything at all: one piece of content arriving speculatively at a time, and no
// sooner than half a minute after the last. One is what an enrolled node can do
// anyway, because its command worker performs one command at a time; the
// interval bounds how often a reconcile sweep may start a fetch on a machine
// whose real work has to come first. Both err on the side of preparing too
// little, because too little costs a queued Run some of its start latency and
// too much costs an admitted Run its start.
var DefaultPrewarmPolicy = orchestrator.PrewarmPolicy{
	MaxConcurrent: 1,
	MinInterval:   30 * time.Second,
}

// Runtime owns the production HTTP server, broker graph, reconciliation loop,
// and SQLite storage for one Mercator process.
type Runtime struct {
	server  *http.Server
	broker  *broker.Broker
	storage *sqlitestore.Storage
	orch    *orchestrator.Orchestrator
	janitor *janitor.Janitor

	// servesTLS is whether this deployment was given a certificate. The
	// question cannot be asked of http.Server.TLSConfig, because net/http
	// installs one of its own the first time it configures HTTP/2 on a
	// listener: a runtime serving plaintext answers "yes" from its second
	// listener onwards and then tries to read a certificate from the empty
	// path.
	servesTLS bool

	stopReconcile context.CancelFunc
	reconcileDone chan struct{}
	prepareDone   chan struct{}
	nodes         *node.Registry

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
	if len(cfg.MasterKey) == 0 {
		return nil, errors.New("daemon: MasterKey is required; set MERCATOR_SECRET_KEY")
	}
	if cfg.WebAuth.Enabled() && cfg.LocalAuthEmail != "" {
		return nil, errors.New("daemon: OIDC and local authentication cannot both be enabled")
	}
	// Security material is loaded before storage is opened, so a deployment
	// configured with a certificate it cannot read fails without having touched
	// the database.
	serverTLS, err := serverTLSConfig(cfg.TLS)
	if err != nil {
		return nil, err
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

	if err := seedFirstWorkspace(ctx, storage.Workspaces()); err != nil {
		return nil, fmt.Errorf("daemon: seed first workspace: %w", err)
	}
	resolver := credential.NewResolver(cfg.Getenv, credentialStore, cfg.MasterKey)
	connections, err := storage.Connections(resolver)
	if err != nil {
		return nil, fmt.Errorf("daemon: init connection storage: %w", err)
	}
	connectionService := connection.NewWithCredentials(connections)
	if err := seedDockerConnection(ctx, connectionService, localDockerReachable); err != nil {
		return nil, fmt.Errorf("daemon: seed docker connection: %w", err)
	}
	factory := cfg.ProviderFactory
	if factory == nil {
		factory = providers.Factory()
	}
	nodeOptions := []node.Option{node.WithAgentVersion(cfg.AgentVersion)}
	if cfg.NodeLease > 0 {
		nodeOptions = append(nodeOptions, node.WithLease(cfg.NodeLease))
	}
	if cfg.NodeSession > 0 {
		nodeOptions = append(nodeOptions, node.WithSession(cfg.NodeSession))
	}
	nodes := node.NewRegistry(
		storage.Nodes(),
		node.NewSigner(node.DeriveKey(cfg.MasterKey)),
		cfg.PublicURL,
		nodeOptions...,
	)
	providerBroker := broker.NewBroker(
		connectionService,
		factory,
		resolver,
		broker.WithRentalSchedules(storage.RentalSchedules()),
		broker.WithNodes(nodes),
	)

	manifests, err := registryManifests(cfg)
	if err != nil {
		return nil, err
	}
	mint, err := contentMint(cfg)
	if err != nil {
		return nil, err
	}

	signer := reporting.NewSigner(reporting.DeriveKey(cfg.MasterKey))
	sched := scheduler.New()
	orchestratorOptions := []orchestrator.Option{
		orchestrator.WithRunProjection(storage.Runs()),
		// Without a manifest source no candidate can be told apart on image
		// locality, so every placement in the real product scores identically
		// on warmth however warm a host actually is.
		orchestrator.WithImageManifests(manifests),
	}
	orchestratorOptions = append(orchestratorOptions,
		orchestrator.WithRentalSchedules(providerBroker),
		// Preparation reaches enrolled nodes through the same Broker a launch
		// does, which is what makes the prepare half of capability.NodeRuntime
		// reachable from the control plane at all.
		orchestrator.WithPrewarm(providerBroker, prewarmPolicy(cfg.Prewarm), storage.Preparation()),
		// The accounts a rented machine must never hold. Without this every fetch
		// a node makes is anonymous, so a private image is a pull the registry
		// denies and a durable Artifact is a read the object store refuses, and
		// both of them fail on the machine rather than here.
		orchestrator.WithContentCredentials(mint),
		// The accounts a rented machine must never hold. Without this every fetch
		// a node makes is anonymous, so a private image is a pull the registry
		// denies and a durable Artifact is a read the object store refuses, and
		// both of them fail on the machine rather than here.
		// A placement that chose to provision allocates a machine through the
		// Broker's capacity lease and invites the node it will be through the
		// registry. They are two seams because they are two contracts: the Broker
		// owns what a provider allocates, and only the registry can say whether an
		// agent ever opened a session on it.
		orchestrator.WithCapacity(providerBroker),
		orchestrator.WithInviter(nodes),
	)
	if signer.Enabled() && cfg.PublicURL != "" {
		orchestratorOptions = append(orchestratorOptions, orchestrator.WithReporting(cfg.PublicURL, signer))
	}
	orch := orchestrator.New(logStore, sched, providerBroker, orchestratorOptions...)
	if rebuild, rebuildErr := storage.Runs().RequiresRebuild(ctx); rebuildErr != nil {
		return nil, fmt.Errorf("daemon: inspect Run projection: %w", rebuildErr)
	} else if rebuild {
		workspaceIDs, listErr := orch.ListRunWorkspaces(ctx)
		if listErr != nil {
			return nil, fmt.Errorf("daemon: list Run projection workspaces: %w", listErr)
		}
		for _, workspaceID := range workspaceIDs {
			if rebuildErr := orch.RebuildRunProjection(ctx, workspaceID); rebuildErr != nil {
				return nil, fmt.Errorf("daemon: rebuild Run projection for %s: %w", workspaceID, rebuildErr)
			}
		}
		if rebuildErr := storage.Runs().MarkRebuilt(ctx); rebuildErr != nil {
			return nil, fmt.Errorf("daemon: finish Run projection rebuild: %w", rebuildErr)
		}
	}

	serverOptions := []httpapi.Option{
		httpapi.WithBearerAuth(cfg.OperatorToken),
		httpapi.WithVerifier(providerBroker),
		httpapi.WithAdapterManifests(providerBroker.Manifests),
		httpapi.WithNodes(nodes),
	}
	if signer.Enabled() {
		serverOptions = append(serverOptions, httpapi.WithReportSigner(signer))
	}
	if cfg.AdminAddr != "" {
		serverOptions = append(serverOptions, httpapi.WithAdminAddr(cfg.AdminAddr))
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
		Resolver:     ociresolver.NewDaemonResolver(inspectLocalImage),
		Workspaces:   storage.Workspaces(),
		Events:       logStore,
	}, serverOptions...)
	// The node protocol is mounted beside the operator API rather than inside
	// it: different audience, different credentials, and an operator token
	// must never be able to act as a node.
	var rootHandler http.Handler = mountNodeProtocol(handler, nodeapi.New(nodes))
	if cfg.LocalAuthEmail != "" {
		// Local login mints a browser session for any request that lacks one,
		// so a DNS-rebound hostname resolving to 127.0.0.1 must never reach
		// it: only requests addressed to this machine by a loopback name are
		// served in --dev mode.
		rootHandler = loopbackHostOnly(rootHandler)
	}

	reconcileCtx, stopReconcile := context.WithCancel(ctx)
	workspaceJanitor := janitor.New(providerBroker, janitor.WithEventLog(logStore))
	runtime := &Runtime{
		server: &http.Server{
			Handler:           rootHandler,
			TLSConfig:         serverTLS,
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       60 * time.Second,
			WriteTimeout:      90 * time.Second,
			IdleTimeout:       120 * time.Second,
		},
		servesTLS:     serverTLS != nil,
		broker:        providerBroker,
		storage:       storage,
		orch:          orch,
		janitor:       workspaceJanitor,
		stopReconcile: stopReconcile,
		reconcileDone: make(chan struct{}),
		prepareDone:   make(chan struct{}),
		nodes:         nodes,
	}
	// Draining the node sessions is registered with the server rather than
	// sequenced by Shutdown, because it has to happen while the drain is waiting
	// and not before it or after it. A session is the one route here that is
	// active for as long as its machine is healthy, and http.Server.Shutdown waits
	// for active requests without cancelling any: every other route finishes on
	// its own and is left to.
	runtime.server.RegisterOnShutdown(nodes.Drain)
	go runtime.reconcile(reconcileCtx)
	go runtime.prepareWhenDesireChanges(reconcileCtx)
	return runtime, nil
}

// DefaultWorkspaceID names the workspace a fresh broker starts with. It is
// readable on purpose: an operator reads it in URLs and audit records far more
// often than they type it.
const DefaultWorkspaceID = "ws_default"

// seedFirstWorkspace gives an empty database one workspace. A broker with no
// workspace can accept no connection and no run, so starting with zero makes
// every first command fail on an id the operator has no way to know. Once any
// workspace exists this does nothing, so it never fights an operator who
// organizes their own.
func seedFirstWorkspace(ctx context.Context, catalog *workspace.SQLiteCatalog) error {
	existing, err := catalog.List(ctx, workspace.ListOptions{IncludeArchived: true})
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return nil
	}
	_, err = catalog.Create(ctx, workspace.Create{
		ID:          DefaultWorkspaceID,
		DisplayName: "Default",
		CreatedAt:   time.Now().UTC(),
		CreatedBy:   "system:bootstrap",
	})
	return err
}

// DefaultDockerConnectionID names the Docker connection a fresh broker seeds.
// It matches the CLI's own default connection id for `connection create
// --adapter-type docker`, so the seeded connection and a hand-made one are the
// same record.
const DefaultDockerConnectionID = "docker"

var bootstrapActor = json.RawMessage(`{"kind":"system","id":"bootstrap"}`)

// seedDockerConnection creates and authorizes the local Docker connection on a
// broker that has never had one, so the quickstart is `serve` then `run
// create` with no connection ceremony. It never resurrects a connection an
// operator deleted: a used id is left untouched. When the local Docker
// endpoint is unreachable it seeds nothing and returns, so a later start with
// Docker running still seeds cleanly.
func seedDockerConnection(ctx context.Context, conns *connection.Service, reachable func(context.Context) error) error {
	inUse, err := conns.IDInUse(ctx, DefaultWorkspaceID, DefaultDockerConnectionID)
	if err != nil {
		return err
	}
	if inUse {
		return nil
	}
	if err := reachable(ctx); err != nil {
		log.Printf("local Docker endpoint unreachable (%v); skipping the %q connection seed. Start Docker and restart, or run `mercator connection create --adapter-type docker`.", err, DefaultDockerConnectionID)
		return nil
	}
	if _, err := conns.Create(ctx, connection.CreateRequest{
		WorkspaceID:  DefaultWorkspaceID,
		ConnectionID: DefaultDockerConnectionID,
		AdapterType:  "docker",
		Actor:        bootstrapActor,
	}); err != nil {
		if errors.Is(err, workspace.ErrNotFound) {
			return nil
		}
		return err
	}
	if err := conns.UpdateAuthorization(ctx, connection.UpdateAuthorizationRequest{
		WorkspaceID:  DefaultWorkspaceID,
		ConnectionID: DefaultDockerConnectionID,
		Authorized:   true,
		Actor:        bootstrapActor,
	}); err != nil {
		return err
	}
	log.Printf("seeded and authorized the %q Docker connection in workspace %q", DefaultDockerConnectionID, DefaultWorkspaceID)
	return nil
}

// localDockerReachable probes the broker host's Docker endpoint the same way
// connection authorization does, so a seeded connection is only ever marked
// authorized when Docker actually answers.
func localDockerReachable(ctx context.Context) error {
	_, err := dockeradapter.NewCLIClient("").Info(ctx)
	return err
}

// inspectLocalImage reads an image's digest and platform from the broker host's
// Docker endpoint, which is the endpoint that launches local runs. This is what
// lets `mercator run create busybox` become a reproducible, digest-pinned run
// without the operator pinning it by hand.
func inspectLocalImage(ctx context.Context, ref string) (ociresolver.InspectedImage, error) {
	info, err := dockeradapter.NewCLIClient("").InspectImage(ctx, ref)
	if err != nil {
		return ociresolver.InspectedImage{}, err
	}
	return ociresolver.InspectedImage{
		RepoDigest:   info.RepoDigest,
		OS:           info.OS,
		Architecture: info.Architecture,
	}, nil
}

// registryManifests builds the manifest source Placement subtracts a host's
// inventory from. It reads the registry credentials the operator already has,
// because the alternative is a second place to configure the same thing; a
// machine that never ran `docker login` resolves anonymously, which is the
// right answer for every public image.
func registryManifests(cfg Config) (*ociresolver.RegistryResolver, error) {
	getenv := cfg.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	path := ociresolver.DefaultDockerConfigPath(getenv)
	if path == "" {
		return ociresolver.NewRegistryResolver(), nil
	}
	credentials, err := ociresolver.DockerConfigCredentials(path)
	if err != nil {
		return nil, fmt.Errorf("daemon: read registry credentials: %w", err)
	}
	return ociresolver.NewRegistryResolver(ociresolver.WithCredentials(credentials)), nil
}

// serverTLSConfig answers nil for a deployment that named no certificate, and
// an error naming the file for one that named a certificate it cannot load. A
// nil configuration serves plaintext, which is why the only other outcome here
// is a refusal: there is no path from a broken certificate to a served port.
func serverTLSConfig(material tlsmaterial.Material) (*tls.Config, error) {
	if !material.Configured() {
		return nil, nil
	}
	config, err := material.Config()
	if err != nil {
		return nil, fmt.Errorf("daemon: %w", err)
	}
	return config, nil
}

// contentMint is the control plane's authority to let one machine fetch one
// piece of content. It is built from the accounts this deployment already
// states, and stating them is the whole of what an operator has to do: a
// registry the host has run `docker login` against is one Mercator can mint a
// pull from, and an object store named in the environment is one it can sign a
// read of.
//
// Neither is required and neither is defaulted. A Mercator with no registry
// account mints nothing for a private image, which is correct for the many
// deployments that run only public ones, and the node's pull is refused by the
// registry rather than by a guess made here. A Mercator with no object store
// refuses to mint a read at all, which is the loud failure: a durable Artifact
// is a location nothing can be fetched from without one.
func contentMint(cfg Config) (*credential.Mint, error) {
	getenv := cfg.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	registries, err := registryAccounts(getenv)
	if err != nil {
		return nil, err
	}
	store, err := objectStoreAccount(getenv)
	if err != nil {
		return nil, err
	}
	mint, err := credential.NewMint(credential.MintConfig{Registries: registries, ObjectStore: store})
	if err != nil {
		return nil, fmt.Errorf("daemon: %w", err)
	}
	return mint, nil
}

// registryAccounts is every registry this host holds an account for, read from
// the file `docker login` writes. It is deliberately the same source the
// manifest resolver reads: an operator who has logged in has said which
// registries this Mercator can reach and as whom, and asking them to say it
// again in the environment would be two places to configure one fact and one of
// them silently wrong.
func registryAccounts(getenv func(string) string) ([]credential.RegistryAccount, error) {
	path := ociresolver.DefaultDockerConfigPath(getenv)
	if path == "" {
		return nil, nil
	}
	held, err := ociresolver.DockerConfigAccounts(path)
	if err != nil {
		return nil, fmt.Errorf("daemon: read registry accounts: %w", err)
	}
	accounts := make([]credential.RegistryAccount, 0, len(held))
	for host, account := range held {
		if account.Password == "" {
			continue
		}
		accounts = append(accounts, credential.RegistryAccount{
			Registry: host, Username: account.Username, Secret: account.Password,
		})
	}
	return accounts, nil
}

// objectStoreAccount is the durable Artifact authority as the environment states
// it. Nil is a Mercator that has none, which is a real deployment. Anything
// partially stated is an error rather than a nil: an operator who named a bucket
// and forgot the key meant to configure this, and the failure they would
// otherwise read is a node reporting that it was handed no read.
func objectStoreAccount(getenv func(string) string) (*credential.ObjectStoreAccount, error) {
	account := credential.ObjectStoreAccount{
		Endpoint:  getenv("MERCATOR_OBJECT_STORE_ENDPOINT"),
		Bucket:    getenv("MERCATOR_OBJECT_STORE_BUCKET"),
		Region:    getenv("MERCATOR_OBJECT_STORE_REGION"),
		AccessKey: getenv("MERCATOR_OBJECT_STORE_ACCESS_KEY"),
		Secret:    getenv("MERCATOR_OBJECT_STORE_SECRET"),
	}
	if account == (credential.ObjectStoreAccount{}) {
		return nil, nil
	}
	return &account, nil
}

// Serve runs the production HTTP server on a listener allocated by the caller.
// A runtime holding TLS material terminates TLS itself. The empty file names
// are what tell http.Server to serve the certificates already loaded into
// TLSConfig rather than read a pair of its own.
//
// It may be called more than once, on a listener each. One http.Server serves
// them all and one Shutdown drains them all, which is how the administrative
// surface gets an address of its own without a second server to shut down.
func (r *Runtime) Serve(listener net.Listener) error {
	if listener == nil {
		return errors.New("daemon: listener is required")
	}
	if r.servesTLS {
		return r.server.ServeTLS(listener, "", "")
	}
	return r.server.Serve(listener)
}

// Shutdown stops and joins background reconciliation, drains HTTP requests, then
// closes SQLite. Repeated calls return the first shutdown result.
func (r *Runtime) Shutdown(ctx context.Context) error {
	r.shutdownOnce.Do(func() {
		// Background work is told to stop before requests are drained, and joined
		// after. Reconciliation and preparation both send commands to machines, and
		// an enrolled node receives one by holding a request open on this same
		// server, so draining while they are still starting work is waiting for
		// requests this process is still creating. Joining them first would spend
		// the caller's drain budget on a sweep that answers to nobody.
		r.stopReconcile()
		httpErr := r.server.Shutdown(ctx)
		<-r.reconcileDone
		<-r.prepareDone
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
	_, prewarmErr := r.orch.Prewarm(ctx)
	swept, sweepErr := r.janitor.Sweep(ctx, workspaceID)
	owned, inventoryErr := r.ListOwned(ctx, workspaceID)
	return ReconcileResult{Advanced: advanced, Reclaimed: swept.Converged(), Owned: owned},
		errors.Join(advanceErr, prewarmErr, sweepErr, inventoryErr)
}

func prewarmPolicy(configured *orchestrator.PrewarmPolicy) orchestrator.PrewarmPolicy {
	if configured == nil {
		return DefaultPrewarmPolicy
	}
	return *configured
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
			// A node whose lease elapsed stops being offered and its workloads
			// become unobserved. Expiring here, on the same sweep that advances
			// Runs, is what keeps Placement from choosing a machine Mercator has
			// stopped hearing from.
			if lost, err := r.nodes.ExpireLeases(ctx); err != nil {
				log.Printf("expire node leases: %v", err)
			} else {
				for _, record := range lost {
					log.Printf("node %s stopped heartbeating; its workloads need reconciliation", record.ID)
				}
			}
			reconcileWorkspaces(ctx, r.orch, r.janitor)
		}
	}
}

// prepareWhenDesireChanges reconciles preparation as soon as something happened
// that could change what Mercator wants prepared, which is what makes preparation
// worth having: a Run submitted half a second after a sweep would otherwise wait
// out the rest of a minute before anything was fetched for the machine it is
// queued on, and the bound on how often preparation may begin would never be the
// thing that held it back.
//
// It subscribes from the log's head, so a restart wakes on what happens next
// rather than replaying every Booking this deployment ever made. Everything
// already delivered is answered by one pass, because the desired set is derived
// from all of it at once and reconciling per event would ask the same question
// several times over.
func (r *Runtime) prepareWhenDesireChanges(ctx context.Context) {
	defer close(r.prepareDone)
	events := r.storage.EventLog()
	filter := eventlog.EventFilter{EventTypes: orchestrator.PreparationTriggers()}
	head, err := events.LatestPosition(ctx, filter)
	if err != nil {
		log.Printf("read the log position to prepare from: %v", err)
		return
	}
	deliveries, err := events.Subscribe(ctx, eventlog.SubscriptionRequest{
		SubscriptionID: "daemon-preparation",
		After:          head,
		Filter:         filter,
	})
	if err != nil {
		log.Printf("subscribe to the events that change what is prepared: %v", err)
		return
	}
	for range deliveries {
		drain(deliveries)
		if ctx.Err() != nil {
			return
		}
		if prepared, err := r.orch.Prewarm(ctx); err != nil {
			log.Printf("prepare capacity for queued work: %v", err)
		} else if prepared.Stated > 0 {
			log.Printf("prepare capacity: asked %d workspaces for %d pieces of content", prepared.Stated, prepared.Wanted)
		}
	}
}

// drain takes everything already delivered off the channel. One reconciliation
// answers all of it.
func drain[T any](deliveries <-chan T) {
	for {
		select {
		case _, open := <-deliveries:
			if !open {
				return
			}
		default:
			return
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
		if result.Converged() > 0 {
			log.Printf(
				"janitor sweep %s: of %d owned objects the orphan policy adopted %d and terminated %d",
				workspaceID, result.Found, result.Adopted, result.Terminated,
			)
		}
	}
	// Preparation is reconciled after every tenant's Runs have moved, because
	// what Mercator wants prepared is derived from where they ended up, and in
	// one pass over the fleet, because the bounds it stays inside are the
	// fleet's. A machine that refuses it costs start latency and never
	// correctness, so a failure here is logged rather than ending the sweep.
	if prepared, err := orch.Prewarm(ctx); err != nil {
		log.Printf("prepare capacity sweep: %v", err)
	} else if prepared.Stated > 0 {
		log.Printf("prepare capacity sweep: asked %d workspaces for %d pieces of content", prepared.Stated, prepared.Wanted)
	}
}

func loopbackHostOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isLoopbackHost(r.Host) {
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, "local development mode serves loopback hosts only", http.StatusMisdirectedRequest)
	})
}

func isLoopbackHost(hostport string) bool {
	host := hostport
	if split, _, err := net.SplitHostPort(hostport); err == nil {
		host = split
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// mountNodeProtocol routes the node protocol's own prefix to the node handler
// and everything else to the operator API. The two never share authentication:
// a node credential reaches nothing here, and an operator token reaches no node
// route.
func mountNodeProtocol(operator, nodes http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/node-agent/") {
			nodes.ServeHTTP(w, r)
			return
		}
		operator.ServeHTTP(w, r)
	})
}
