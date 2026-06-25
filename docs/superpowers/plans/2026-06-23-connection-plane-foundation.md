# Connection Plane Foundation (Plan 1A) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move Mercator from a single hard-wired adapter to a multi-connection **Broker** that aggregates offers and routes lifecycle ops across registered connections, with a credential layer (env + encrypted store) — wired so the existing docker adapter runs through a bootstrap connection with no behavior change.

**Architecture:** A new `internal/credential` package resolves `{source, ref}` → secret (env var, or an AES-GCM encrypted SQLite side table; never the event log). A new `internal/broker` package holds an `AdapterFactory` (adapter_type → constructor) and a `Broker` that implements `adapter.Adapter` by listing a workspace's authorized connections, building/caching their adapters, aggregating `ListOffers`, and routing per-run lifecycle ops by connection id. `cmd/mercator` registers the env-configured docker adapter as a bootstrap connection and hands the orchestrator the Broker instead of a single adapter.

**Tech Stack:** Go 1.25 (no cgo), `modernc.org/sqlite`, `crypto/aes`+`crypto/cipher` (AES-256-GCM), existing `internal/{adapter,connection,orchestrator,eventlog}`.

## Global Constraints

- Module path: `github.com/benngarcia/mercator`. Pure Go, no cgo (`modernc.org/sqlite`).
- **Secrets never enter the event log.** The `connection.created` event holds only `{source, ref}`; ciphertext lives in a separate table.
- Master key from `MERCATOR_SECRET_KEY` (hex or base64, decodes to 32 bytes for AES-256). Absent → the `mercator` source is disabled (operations needing it return a clear error); env source still works.
- The `adapter.Adapter` interface is the contract; the Broker implements it. No orchestrator logic changes beyond threading the run's connection id into post-launch requests.
- TDD: write the failing test, watch it fail, minimal code, watch it pass, commit. `go test ./...` stays green after every task.
- This plan must NOT regress the docker path: after Task 8 a docker run still works end-to-end, now through the Broker.

---

### Task 1: ADR 0002 — scope ADR 0001 to workload secrets

**Files:**
- Create: `docs/adr/0002-connection-credentials-are-first-class.md`

- [ ] **Step 1: Write the ADR**

```markdown
# ADR 0002: Connection Credentials Are First-Class

Status: Accepted
Date: 2026-06-23

## Context

ADR 0001 removed a Mercator-owned secret vault. That decision is about
*workload* secrets — material the user's container needs (e.g. S3 credentials),
which the workload fetches from its own backend.

Connection/adapter credentials are different: they are the credentials Mercator
*itself* needs to call a provider's control plane (e.g. a RunPod API key).
Mercator cannot broker without them. The blanket "no secrets" reading of ADR
0001 does not fit this case.

## Decision

ADR 0001 governs *workload* secrets only. *Connection* credentials are
first-class and handled by a `credential_source` seam:

- `env` — the connection references an env var holding the secret.
- `mercator` — the secret is stored encrypted (AES-256-GCM under a process
  master key `MERCATOR_SECRET_KEY`) in a dedicated table, addressed by
  connection id. The append-only, sink-streamed event log stores only the
  reference `{source, ref}`, never the secret.
- `vault` / external sources may be added later behind the same seam.

Mercator does not store *workload* secrets, KMS, rotation policies, or secret
versions. The connection secret store is intentionally small: low cardinality,
infrastructure-only, never materialized into arbitrary containers.

## Consequences

Connections can be added and authorized from the API/UI without an operator env
change (the `mercator` source). The event log remains secret-free. One process
master key is required to enable the `mercator` source.
```

- [ ] **Step 2: Commit**

```bash
git add docs/adr/0002-connection-credentials-are-first-class.md
git commit -m "docs(adr): 0002 connection credentials are first-class (scopes ADR 0001)"
```

---

### Task 2: Credential type, AES-GCM seal/open, in-memory secret store, resolver

**Files:**
- Create: `internal/credential/credential.go`
- Create: `internal/credential/credential_test.go`

**Interfaces:**
- Produces:
  - `type Credential struct { Source string; Ref string }`
  - `type SecretStore interface { Put(ctx, workspaceID, connectionID string, blob []byte) error; Get(ctx, workspaceID, connectionID string) ([]byte, error) }`
  - `var ErrNotFound = errors.New("credential: secret not found")`
  - `func Seal(key, plaintext []byte) ([]byte, error)` / `func Open(key, blob []byte) ([]byte, error)`
  - `func NewMemoryStore() *MemoryStore` (implements SecretStore)
  - `type Resolver struct { ... }` with `func NewResolver(getenv func(string) string, store SecretStore, masterKey []byte) *Resolver` and `func (r *Resolver) Resolve(ctx, workspaceID string, c Credential) (string, error)`
  - source constants `SourceEnv = "env"`, `SourceMercator = "mercator"`

- [ ] **Step 1: Write the failing test**

```go
package credential

import (
	"context"
	"strings"
	"testing"
)

func key32() []byte { return []byte("0123456789abcdef0123456789abcdef") }

func TestSealOpenRoundTrip(t *testing.T) {
	blob, err := Seal(key32(), []byte("rp_secret"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if strings.Contains(string(blob), "rp_secret") {
		t.Fatal("ciphertext must not contain the plaintext")
	}
	got, err := Open(key32(), blob)
	if err != nil || string(got) != "rp_secret" {
		t.Fatalf("open: %q err=%v", got, err)
	}
}

func TestOpenWithWrongKeyFails(t *testing.T) {
	blob, _ := Seal(key32(), []byte("x"))
	wrong := []byte("ffffffffffffffffffffffffffffffff")
	if _, err := Open(wrong, blob); err == nil {
		t.Fatal("expected open with wrong key to fail closed")
	}
}

func TestResolveEnvSource(t *testing.T) {
	r := NewResolver(func(k string) string {
		if k == "MY_KEY" {
			return "from-env"
		}
		return ""
	}, NewMemoryStore(), nil)
	got, err := r.Resolve(context.Background(), "ws_1", Credential{Source: SourceEnv, Ref: "MY_KEY"})
	if err != nil || got != "from-env" {
		t.Fatalf("env resolve: %q err=%v", got, err)
	}
}

func TestResolveMercatorSource(t *testing.T) {
	store := NewMemoryStore()
	blob, _ := Seal(key32(), []byte("stored-secret"))
	if err := store.Put(context.Background(), "ws_1", "conn_x", blob); err != nil {
		t.Fatalf("put: %v", err)
	}
	r := NewResolver(nil, store, key32())
	got, err := r.Resolve(context.Background(), "ws_1", Credential{Source: SourceMercator, Ref: "conn_x"})
	if err != nil || got != "stored-secret" {
		t.Fatalf("mercator resolve: %q err=%v", got, err)
	}
}

func TestResolveMercatorWithoutKeyDisabled(t *testing.T) {
	r := NewResolver(nil, NewMemoryStore(), nil) // no master key
	_, err := r.Resolve(context.Background(), "ws_1", Credential{Source: SourceMercator, Ref: "conn_x"})
	if err == nil {
		t.Fatal("expected mercator source to be disabled without a master key")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/credential/`
Expected: FAIL — build error (`undefined: Seal`, etc.).

- [ ] **Step 3: Write minimal implementation**

```go
// Package credential resolves connection credentials from a {source, ref}
// reference. Secrets are never stored in the event log; the mercator source
// keeps ciphertext in a SecretStore, encrypted under a process master key.
package credential

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"sync"
)

const (
	SourceEnv      = "env"
	SourceMercator = "mercator"
)

var ErrNotFound = errors.New("credential: secret not found")

type Credential struct {
	Source string `json:"source"`
	Ref    string `json:"ref"`
}

// Seal encrypts plaintext with AES-256-GCM; the nonce is prepended to the blob.
func Seal(key, plaintext []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// Open reverses Seal.
func Open(key, blob []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(blob) < gcm.NonceSize() {
		return nil, errors.New("credential: ciphertext too short")
	}
	nonce, ct := blob[:gcm.NonceSize()], blob[gcm.NonceSize():]
	return gcm.Open(nil, nonce, ct, nil)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

type SecretStore interface {
	Put(ctx context.Context, workspaceID, connectionID string, blob []byte) error
	Get(ctx context.Context, workspaceID, connectionID string) ([]byte, error)
}

type MemoryStore struct {
	mu sync.Mutex
	m  map[string][]byte
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{m: map[string][]byte{}} }

func memKey(ws, id string) string { return ws + "/" + id }

func (s *MemoryStore) Put(_ context.Context, ws, id string, blob []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]byte, len(blob))
	copy(cp, blob)
	s.m[memKey(ws, id)] = cp
	return nil
}

func (s *MemoryStore) Get(_ context.Context, ws, id string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	blob, ok := s.m[memKey(ws, id)]
	if !ok {
		return nil, ErrNotFound
	}
	return blob, nil
}

type Resolver struct {
	getenv    func(string) string
	store     SecretStore
	masterKey []byte
}

func NewResolver(getenv func(string) string, store SecretStore, masterKey []byte) *Resolver {
	return &Resolver{getenv: getenv, store: store, masterKey: masterKey}
}

func (r *Resolver) Resolve(ctx context.Context, workspaceID string, c Credential) (string, error) {
	switch c.Source {
	case "", SourceEnv:
		if r.getenv == nil {
			return "", errors.New("credential: env source unavailable")
		}
		v := r.getenv(c.Ref)
		if v == "" {
			return "", fmt.Errorf("credential: env var %q is empty", c.Ref)
		}
		return v, nil
	case SourceMercator:
		if len(r.masterKey) == 0 {
			return "", errors.New("credential: mercator source disabled (set MERCATOR_SECRET_KEY)")
		}
		blob, err := r.store.Get(ctx, workspaceID, c.Ref)
		if err != nil {
			return "", err
		}
		plain, err := Open(r.masterKey, blob)
		if err != nil {
			return "", err
		}
		return string(plain), nil
	default:
		return "", fmt.Errorf("credential: unknown source %q", c.Source)
	}
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/credential/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/credential/credential.go internal/credential/credential_test.go
git commit -m "feat(credential): {source,ref} resolver with env + AES-GCM mercator source"
```

---

### Task 3: SQLite secret store

**Files:**
- Create: `internal/credential/sqlite.go`
- Create: `internal/credential/sqlite_test.go`

**Interfaces:**
- Produces: `func NewSQLiteStore(ctx context.Context, db *sql.DB) (*SQLiteStore, error)` (implements `SecretStore`; creates table `connection_secret(workspace_id, connection_id, blob, PRIMARY KEY(workspace_id, connection_id))`).

- [ ] **Step 1: Write the failing test**

```go
package credential

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestSQLiteStorePutGet(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewSQLiteStore(context.Background(), db)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if err := store.Put(context.Background(), "ws_1", "conn_x", []byte{1, 2, 3}); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := store.Get(context.Background(), "ws_1", "conn_x")
	if err != nil || string(got) != string([]byte{1, 2, 3}) {
		t.Fatalf("get: %v err=%v", got, err)
	}
	if _, err := store.Get(context.Background(), "ws_1", "missing"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/credential/ -run TestSQLiteStore`
Expected: FAIL — `undefined: NewSQLiteStore`.

- [ ] **Step 3: Write minimal implementation**

```go
package credential

import (
	"context"
	"database/sql"
)

type SQLiteStore struct{ db *sql.DB }

func NewSQLiteStore(ctx context.Context, db *sql.DB) (*SQLiteStore, error) {
	_, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS connection_secret (
		workspace_id  TEXT NOT NULL,
		connection_id TEXT NOT NULL,
		blob          BLOB NOT NULL,
		PRIMARY KEY (workspace_id, connection_id)
	)`)
	if err != nil {
		return nil, err
	}
	return &SQLiteStore{db: db}, nil
}

func (s *SQLiteStore) Put(ctx context.Context, ws, id string, blob []byte) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO connection_secret (workspace_id, connection_id, blob) VALUES (?, ?, ?)
		 ON CONFLICT(workspace_id, connection_id) DO UPDATE SET blob = excluded.blob`,
		ws, id, blob)
	return err
}

func (s *SQLiteStore) Get(ctx context.Context, ws, id string) ([]byte, error) {
	var blob []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT blob FROM connection_secret WHERE workspace_id = ? AND connection_id = ?`, ws, id).Scan(&blob)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	return blob, err
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/credential/`
Expected: PASS (all credential tests).

- [ ] **Step 5: Commit**

```bash
git add internal/credential/sqlite.go internal/credential/sqlite_test.go
git commit -m "feat(credential): sqlite secret store"
```

---

### Task 4: Add `Verify` to the adapter interface

**Files:**
- Modify: `internal/adapter/adapter.go` (the `Adapter` interface block, ~line 36)
- Modify: `internal/adapter/fake/fake.go`
- Modify: `internal/adapter/docker/docker.go`
- Modify: `internal/adapter/fake/fake_test.go` (or a new test)

**Interfaces:**
- Produces: `Verify(ctx context.Context) error` on `adapter.Adapter`. fake returns its configured error (nil by default); docker runs a cheap `Info` probe.

- [ ] **Step 1: Write the failing test**

```go
// internal/adapter/fake/verify_test.go
package fake

import (
	"context"
	"testing"
)

func TestFakeVerifyOK(t *testing.T) {
	if err := New().Verify(context.Background()); err != nil {
		t.Fatalf("fake verify should pass: %v", err)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/adapter/fake/ -run TestFakeVerifyOK`
Expected: FAIL — `New().Verify undefined` (and `go build ./...` fails: fake/docker no longer satisfy the interface once you add the method in step 3; add the method to the interface and both impls together).

- [ ] **Step 3: Write minimal implementation**

In `internal/adapter/adapter.go`, add to the interface:
```go
	// Verify performs a cheap credential/reachability check for the authorize
	// flow. It does not launch anything.
	Verify(ctx context.Context) error
```
In `internal/adapter/fake/fake.go` add:
```go
func (a *Adapter) Verify(context.Context) error { return a.verifyErr }
```
(add a `verifyErr error` field to the fake Adapter struct and a `WithVerifyError(err error)` option; default nil.)
In `internal/adapter/docker/docker.go` add:
```go
// Verify checks the Docker endpoint is reachable by probing it. The Adapter
// holds an *infoClient* — reuse the CLIClient if exposed, else add a probe.
func (a *Adapter) Verify(ctx context.Context) error {
	if v, ok := a.client.(interface {
		Info(context.Context) (CLIInfo, error)
	}); ok {
		_, err := v.Info(ctx)
		return err
	}
	return nil
}
```
NOTE: `CLIClient.Info` already exists (`internal/adapter/docker/probe.go`) returning `HostInfo`; adjust the type-assertion to `Info(context.Context) (HostInfo, error)` and reuse it. If the adapter's `client` field is the `Client` interface that lacks `Info`, add `Info` to a local optional interface as shown (type assertion), so the fake `Client` in tests is unaffected.

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/adapter/... && go build ./...`
Expected: PASS / builds. Existing httpapi tests still build (their `fake.New(...)` now also satisfies `Verify`).

- [ ] **Step 5: Commit**

```bash
git add internal/adapter
git commit -m "feat(adapter): add Verify(ctx) for the connection authorize flow"
```

---

### Task 5: AdapterFactory + registry

**Files:**
- Create: `internal/broker/factory.go`
- Create: `internal/broker/factory_test.go`

**Interfaces:**
- Produces:
  - `type Built struct { Adapter adapter.Adapter }` (room to grow)
  - `type FactoryFunc func(config map[string]string, secret string) (adapter.Adapter, error)`
  - `type Factory struct { ... }` with `func NewFactory() *Factory`, `func (f *Factory) Register(adapterType string, fn FactoryFunc)`, `func (f *Factory) Build(adapterType string, config map[string]string, secret string) (adapter.Adapter, error)` (errors on unknown type).

- [ ] **Step 1: Write the failing test**

```go
package broker

import (
	"context"
	"testing"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/domain"
)

type stubAdapter struct{ adapter.Adapter }

func (stubAdapter) ListOffers(context.Context, adapter.OfferRequest) ([]domain.OfferSnapshot, error) {
	return nil, nil
}
func (stubAdapter) Verify(context.Context) error { return nil }

func TestFactoryBuildsRegisteredType(t *testing.T) {
	f := NewFactory()
	f.Register("stub", func(map[string]string, string) (adapter.Adapter, error) {
		return stubAdapter{}, nil
	})
	if _, err := f.Build("stub", nil, ""); err != nil {
		t.Fatalf("build stub: %v", err)
	}
	if _, err := f.Build("nope", nil, ""); err == nil {
		t.Fatal("expected error for unknown adapter type")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/broker/`
Expected: FAIL — `undefined: NewFactory`.

- [ ] **Step 3: Write minimal implementation**

```go
// Package broker turns registered connections into live adapters and routes
// offer collection + run lifecycle across them.
package broker

import (
	"fmt"
	"sync"

	"github.com/benngarcia/mercator/internal/adapter"
)

type FactoryFunc func(config map[string]string, secret string) (adapter.Adapter, error)

type Factory struct {
	mu sync.RWMutex
	fns map[string]FactoryFunc
}

func NewFactory() *Factory { return &Factory{fns: map[string]FactoryFunc{}} }

func (f *Factory) Register(adapterType string, fn FactoryFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fns[adapterType] = fn
}

func (f *Factory) Build(adapterType string, config map[string]string, secret string) (adapter.Adapter, error) {
	f.mu.RLock()
	fn, ok := f.fns[adapterType]
	f.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("broker: no adapter registered for type %q", adapterType)
	}
	return fn(config, secret)
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/broker/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/broker/factory.go internal/broker/factory_test.go
git commit -m "feat(broker): adapter factory registry"
```

---

### Task 6: Broker — offer aggregation + Launch/Verify routing

**Files:**
- Create: `internal/broker/broker.go`
- Create: `internal/broker/broker_test.go`

**Interfaces:**
- Consumes: `connection.Service.List(ctx, workspaceID) ([]connection.Record, error)`; `credential.Resolver.Resolve`; `Factory.Build`; `connection.Record{ID, AdapterType, Authorized}` plus the **new** `Config map[string]string` and `Credential credential.Credential` fields (added in Plan 1B; for Task 6 tests use a fake `Connections` lister and a stub resolver — do NOT depend on the real connection.Service yet).
- Produces:
  - `type Connections interface { List(ctx, workspaceID string) ([]ConnRef, error) }` where `type ConnRef struct { ID, AdapterType string; Config map[string]string; Credential credential.Credential; Authorized bool }`
  - `type Resolver interface { Resolve(ctx, workspaceID string, c credential.Credential) (string, error) }`
  - `func NewBroker(conns Connections, factory *Factory, resolver Resolver) *Broker` implementing `adapter.Adapter`.
  - `Broker.ListOffers` aggregates authorized connections' offers, stamping `ConnectionID`/`AdapterType`. `Broker.Launch` routes by `req.SelectedOfferConnectionID`. `Broker.Verify` verifies all authorized connections (used rarely; per-connection authorize uses the factory directly in Plan 1B).

The `Connections`/`Resolver`/`ConnRef` indirection keeps the broker testable and decouples it from `internal/connection` (Plan 1B provides an adapter from `connection.Service` → `Connections`).

- [ ] **Step 1: Write the failing test**

```go
package broker

import (
	"context"
	"errors"
	"testing"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/credential"
	"github.com/benngarcia/mercator/internal/domain"
)

type fakeConns struct{ recs []ConnRef }

func (f fakeConns) List(context.Context, string) ([]ConnRef, error) { return f.recs, nil }

type nilResolver struct{}

func (nilResolver) Resolve(context.Context, string, credential.Credential) (string, error) {
	return "secret", nil
}

// recording adapter that reports which connection launched.
type recAdapter struct {
	adapter.Adapter
	id       string
	launched *string
}

func (a recAdapter) ListOffers(context.Context, adapter.OfferRequest) ([]domain.OfferSnapshot, error) {
	return []domain.OfferSnapshot{{ID: "offer_" + a.id, ConnectionID: a.id, AdapterType: "stub"}}, nil
}
func (a recAdapter) Launch(_ context.Context, req adapter.LaunchRequest) (adapter.LaunchReceipt, error) {
	*a.launched = a.id
	return adapter.LaunchReceipt{LaunchKey: req.LaunchKey}, nil
}
func (recAdapter) Verify(context.Context) error { return nil }

func TestBrokerAggregatesOffersAcrossConnections(t *testing.T) {
	conns := fakeConns{recs: []ConnRef{
		{ID: "conn_a", AdapterType: "stub", Authorized: true},
		{ID: "conn_b", AdapterType: "stub", Authorized: true},
		{ID: "conn_unauth", AdapterType: "stub", Authorized: false},
	}}
	f := NewFactory()
	f.Register("stub", func(map[string]string, string) (adapter.Adapter, error) {
		return recAdapter{id: "x"}, nil
	})
	b := NewBroker(conns, f, nilResolver{})
	offers, err := b.ListOffers(context.Background(), adapter.OfferRequest{WorkspaceID: "ws_1"})
	if err != nil {
		t.Fatalf("list offers: %v", err)
	}
	if len(offers) != 2 {
		t.Fatalf("expected 2 offers (authorized only), got %d", len(offers))
	}
}

func TestBrokerRoutesLaunchByConnection(t *testing.T) {
	var launchedBy string
	f := NewFactory()
	f.Register("stub", func(cfg map[string]string, _ string) (adapter.Adapter, error) {
		return recAdapter{id: cfg["id"], launched: &launchedBy}, nil
	})
	conns := fakeConns{recs: []ConnRef{
		{ID: "conn_a", AdapterType: "stub", Authorized: true, Config: map[string]string{"id": "conn_a"}},
		{ID: "conn_b", AdapterType: "stub", Authorized: true, Config: map[string]string{"id": "conn_b"}},
	}}
	b := NewBroker(conns, f, nilResolver{})
	_, err := b.Launch(context.Background(), adapter.LaunchRequest{
		LaunchKey:                 "lk1",
		SelectedOfferConnectionID: "conn_b",
		SelectedOfferAdapterType:  "stub",
	})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if launchedBy != "conn_b" {
		t.Fatalf("expected launch routed to conn_b, got %q", launchedBy)
	}
}

func TestBrokerLaunchUnknownConnectionErrors(t *testing.T) {
	b := NewBroker(fakeConns{}, NewFactory(), nilResolver{})
	_, err := b.Launch(context.Background(), adapter.LaunchRequest{SelectedOfferConnectionID: "nope"})
	if err == nil || !errors.Is(err, ErrConnectionNotFound) {
		t.Fatalf("expected ErrConnectionNotFound, got %v", err)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/broker/ -run TestBroker`
Expected: FAIL — `undefined: NewBroker`.

- [ ] **Step 3: Write minimal implementation**

```go
package broker

import (
	"context"
	"errors"
	"fmt"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/credential"
	"github.com/benngarcia/mercator/internal/domain"
)

var ErrConnectionNotFound = errors.New("broker: connection not found")

type ConnRef struct {
	ID          string
	AdapterType string
	Config      map[string]string
	Credential  credential.Credential
	Authorized  bool
}

type Connections interface {
	List(ctx context.Context, workspaceID string) ([]ConnRef, error)
}

type Resolver interface {
	Resolve(ctx context.Context, workspaceID string, c credential.Credential) (string, error)
}

type Broker struct {
	conns    Connections
	factory  *Factory
	resolver Resolver
}

func NewBroker(conns Connections, factory *Factory, resolver Resolver) *Broker {
	return &Broker{conns: conns, factory: factory, resolver: resolver}
}

// build constructs the adapter for one connection (no caching yet — YAGNI;
// providers' ListOffers are cached upstream by the offer service).
func (b *Broker) build(ctx context.Context, workspaceID string, c ConnRef) (adapter.Adapter, error) {
	secret := ""
	if c.Credential.Source != "" {
		s, err := b.resolver.Resolve(ctx, workspaceID, c.Credential)
		if err != nil {
			return nil, fmt.Errorf("broker: resolve credential for %s: %w", c.ID, err)
		}
		secret = s
	}
	return b.factory.Build(c.AdapterType, c.Config, secret)
}

func (b *Broker) connByID(ctx context.Context, workspaceID, connectionID string) (ConnRef, adapter.Adapter, error) {
	recs, err := b.conns.List(ctx, workspaceID)
	if err != nil {
		return ConnRef{}, nil, err
	}
	for _, c := range recs {
		if c.ID == connectionID {
			ad, err := b.build(ctx, workspaceID, c)
			return c, ad, err
		}
	}
	return ConnRef{}, nil, fmt.Errorf("%w: %s", ErrConnectionNotFound, connectionID)
}

func (b *Broker) ListOffers(ctx context.Context, req adapter.OfferRequest) ([]domain.OfferSnapshot, error) {
	recs, err := b.conns.List(ctx, req.WorkspaceID)
	if err != nil {
		return nil, err
	}
	var all []domain.OfferSnapshot
	for _, c := range recs {
		if !c.Authorized {
			continue
		}
		ad, err := b.build(ctx, req.WorkspaceID, c)
		if err != nil {
			continue // a broken connection should not sink the whole list
		}
		offers, err := ad.ListOffers(ctx, req)
		if err != nil {
			continue
		}
		for i := range offers {
			offers[i].ConnectionID = c.ID
			offers[i].AdapterType = c.AdapterType
			all = append(all, offers[i])
		}
	}
	return all, nil
}

func (b *Broker) Launch(ctx context.Context, req adapter.LaunchRequest) (adapter.LaunchReceipt, error) {
	_, ad, err := b.connByID(ctx, req.WorkspaceID, req.SelectedOfferConnectionID)
	if err != nil {
		return adapter.LaunchReceipt{}, err
	}
	return ad.Launch(ctx, req)
}

func (b *Broker) Verify(ctx context.Context) error { return nil } // per-connection verify is in Plan 1B
```

NOTE: `LaunchRequest` must carry `WorkspaceID`; confirm the field exists (it does — used by the orchestrator). If not, the routing uses `connByID` with the workspace from the request. Observe/Cancel/Release/Terminate/ListOwned are added in Task 7.

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/broker/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/broker/broker.go internal/broker/broker_test.go
git commit -m "feat(broker): aggregate offers + route launch by connection"
```

---

### Task 7: Route post-launch ops by connection (interface + orchestrator threading)

**Files:**
- Modify: `internal/adapter/adapter.go` — add `WorkspaceID string` and `ConnectionID string` to `ObserveRequest`, `CancelRequest`, `ReleaseRequest`, `TerminateRequest`.
- Modify: `internal/broker/broker.go` — implement `Observe/Cancel/Release/Terminate` (route by `req.ConnectionID`) and `ListOwned` (fan out across the workspace's authorized connections, aggregate).
- Modify: `internal/orchestrator/orchestrator.go` — populate `WorkspaceID`+`ConnectionID` on those requests from the run's recorded launch intent (`SelectedOfferConnectionID`).
- Modify: `internal/broker/broker_test.go` — add routing + fan-out tests.

**Interfaces:**
- Consumes: the run's recorded `SelectedOfferConnectionID` (already on the launch intent) and `WorkspaceID`.
- Produces: `Broker.Observe/Cancel/Release/Terminate` routing by `ConnectionID`; `Broker.ListOwned` aggregating across connections (stamping nothing new — `OwnedExternalObject` already has the workspace/run/attempt fields).

- [ ] **Step 1: Write the failing test**

```go
func TestBrokerListOwnedFansOut(t *testing.T) {
	f := NewFactory()
	f.Register("stub", func(cfg map[string]string, _ string) (adapter.Adapter, error) {
		return ownedAdapter{id: cfg["id"]}, nil
	})
	conns := fakeConns{recs: []ConnRef{
		{ID: "conn_a", AdapterType: "stub", Authorized: true, Config: map[string]string{"id": "a"}},
		{ID: "conn_b", AdapterType: "stub", Authorized: true, Config: map[string]string{"id": "b"}},
	}}
	b := NewBroker(conns, f, nilResolver{})
	owned, err := b.ListOwned(context.Background(), adapter.OwnershipQuery{WorkspaceID: "ws_1"})
	if err != nil {
		t.Fatalf("list owned: %v", err)
	}
	if len(owned) != 2 {
		t.Fatalf("expected owned objects from both connections, got %d", len(owned))
	}
}
```

Add an `ownedAdapter` test stub whose `ListOwned` returns one object tagged with its id, and whose other methods are no-ops (embed `adapter.Adapter`).

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/broker/ -run TestBrokerListOwned`
Expected: FAIL — `b.ListOwned undefined`.

- [ ] **Step 3: Write minimal implementation**

In `internal/adapter/adapter.go`, add `WorkspaceID string` and `ConnectionID string` fields to `ObserveRequest`, `CancelRequest`, `ReleaseRequest`, `TerminateRequest`.

In `internal/broker/broker.go`:
```go
func (b *Broker) Observe(ctx context.Context, req adapter.ObserveRequest) (adapter.ExternalObservation, error) {
	_, ad, err := b.connByID(ctx, req.WorkspaceID, req.ConnectionID)
	if err != nil {
		return adapter.ExternalObservation{}, err
	}
	return ad.Observe(ctx, req)
}

func (b *Broker) Cancel(ctx context.Context, req adapter.CancelRequest) (adapter.CancelReceipt, error) {
	_, ad, err := b.connByID(ctx, req.WorkspaceID, req.ConnectionID)
	if err != nil {
		return adapter.CancelReceipt{}, err
	}
	return ad.Cancel(ctx, req)
}

func (b *Broker) Release(ctx context.Context, req adapter.ReleaseRequest) (adapter.ReleaseReceipt, error) {
	_, ad, err := b.connByID(ctx, req.WorkspaceID, req.ConnectionID)
	if err != nil {
		return adapter.ReleaseReceipt{}, err
	}
	return ad.Release(ctx, req)
}

func (b *Broker) Terminate(ctx context.Context, req adapter.TerminateRequest) (adapter.TerminateReceipt, error) {
	_, ad, err := b.connByID(ctx, req.WorkspaceID, req.ConnectionID)
	if err != nil {
		return adapter.TerminateReceipt{}, err
	}
	return ad.Terminate(ctx, req)
}

func (b *Broker) ListOwned(ctx context.Context, req adapter.OwnershipQuery) ([]adapter.OwnedExternalObject, error) {
	recs, err := b.conns.List(ctx, req.WorkspaceID)
	if err != nil {
		return nil, err
	}
	var all []adapter.OwnedExternalObject
	for _, c := range recs {
		if !c.Authorized {
			continue
		}
		ad, err := b.build(ctx, req.WorkspaceID, c)
		if err != nil {
			continue
		}
		owned, err := ad.ListOwned(ctx, req)
		if err != nil {
			continue
		}
		all = append(all, owned...)
	}
	return all, nil
}
```

In `internal/orchestrator/orchestrator.go`, where it builds `ObserveRequest`/`CancelRequest`/`ReleaseRequest`/`TerminateRequest`, set `WorkspaceID` (already in scope) and `ConnectionID: state.launchIntent.SelectedOfferConnectionID` (the run's recorded connection). Find each construction (`adapter.ObserveRequest{...}`, etc.) and add the two fields. The launch intent already carries `SelectedOfferConnectionID`.

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/broker/ ./internal/orchestrator/ && go build ./...`
Expected: PASS. (Orchestrator tests use the fake adapter directly — routing fields are ignored by the fake, so they stay green.)

- [ ] **Step 5: Commit**

```bash
git add internal/adapter/adapter.go internal/broker internal/orchestrator/orchestrator.go
git commit -m "feat(broker): route post-launch ops by connection; fan out ListOwned"
```

---

### Task 8: Wire the Broker into the server + cmd; bootstrap the docker connection; drop the fake runtime

**Files:**
- Modify: `cmd/mercator/main.go` — build factory (register `docker`), resolver, secret store; register a bootstrap docker connection; hand the orchestrator the Broker. Remove `fakeOffers`/`MERCATOR_FAKE_OFFER` and the `offeringAdapter` shim's role as the sole adapter.
- Modify: `internal/httpapi/server.go` — remove the "derive connections from offers" block added in 218d43c (the registry now holds the bootstrap connection); keep `visibleOffers`.
- Modify: `internal/httpapi/connections_test.go` — update expectation (registry-backed, not offer-derived).
- Modify: `cmd/mercator/main_test.go` — drop `TestFakeOffersAreOptIn`; keep/adjust the docker selection test.

**Interfaces:**
- Consumes: `broker.NewBroker`, `broker.NewFactory`, `credential.NewResolver`, `credential.NewSQLiteStore`. A `connection.Service` → `broker.Connections` adapter is needed; for 1A the bootstrap connection can be supplied by a tiny in-code `Connections` that returns the docker connection (the full `connection.Service`-backed `Connections` lands in Plan 1B). Use a minimal `staticConnections` in `cmd/mercator` returning the docker `ConnRef` so 1A is self-contained and verifiable.

- [ ] **Step 1: Write the failing test (server still serves docker offers through the Broker)**

Add to `cmd/mercator/main_test.go`:
```go
func TestRuntimeBrokerServesDockerConnection(t *testing.T) {
	br := buildBroker(map[string]string{
		"MERCATOR_ADAPTER":    "docker",
		"MERCATOR_DOCKER_ARCH": "amd64",
	})
	if br == nil {
		t.Fatal("expected a broker for docker")
	}
	offers, err := br.ListOffers(context.Background(), adapter.OfferRequest{WorkspaceID: "ws_1"})
	if err != nil {
		t.Fatalf("list offers: %v", err)
	}
	if len(offers) != 1 || offers[0].AdapterType != "docker" || offers[0].ConnectionID == "" {
		t.Fatalf("unexpected offers via broker: %+v", offers)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./cmd/mercator/ -run TestRuntimeBrokerServesDockerConnection`
Expected: FAIL — `undefined: buildBroker`.

- [ ] **Step 3: Write minimal implementation**

In `cmd/mercator/main.go`, add `buildBroker(values map[string]string) *broker.Broker`:
- `factory := broker.NewFactory()`; register `"docker"`:
  ```go
  factory.Register("docker", func(config map[string]string, _ string) (adapter.Adapter, error) {
      client := dockeradapter.NewCLIClient(config["bin"])
      client.Host = config["host"]; client.Context = config["context"]
      return dockeradapter.New(client), nil
  })
  ```
- Build the resolver: `credential.NewResolver(func(k string) string { return values[k] }, secretStore, masterKey)` where `masterKey` decodes `values["MERCATOR_SECRET_KEY"]` (hex or base64; empty → nil) and `secretStore` is the SQLite store (or memory if the DSN is absent — for 1A wire memory; SQLite store wiring lands with the server's DB in 1B).
- Build a `staticConnections` returning the docker `ConnRef` derived from the existing `dockerIdentity(values)` + a probed offer config:
  ```go
  conn := broker.ConnRef{
      ID: id.ConnectionID, AdapterType: "docker", Authorized: true,
      Config: map[string]string{
          "bin": values["MERCATOR_DOCKER_BIN"],
          "host": values["MERCATOR_DOCKER_HOST"],
          "context": values["MERCATOR_DOCKER_CONTEXT"],
          // identity/arch passed through so ListOffers stamps correctly
      },
  }
  ```
  NOTE: the docker adapter's `ListOffers` currently returns an error (offers are synthesized in `main.go`). For 1A, give the docker factory a thin wrapper that returns the synthesized `dockerOfferFromInfo(...)` offer (reuse the existing `dockerOffer*` funcs), so the Broker's `ListOffers` yields the docker offer. Move `dockerOfferFromInfo`/`dockerIdentity` so the factory can call them (keep in `main` package; the factory closure can capture `values`).
- `return broker.NewBroker(staticConnections{conn}, factory, resolver)`.
Then in `run(...)`, replace the single `runtimeAdapter(env)` handed to `HandlerForSQLite...` with `buildBroker(env)` (the Broker is an `adapter.Adapter`). Delete `fakeOffers`, the `MERCATOR_FAKE_OFFER` path, and `offeringAdapter` if now unused.
In `internal/httpapi/server.go`, delete the offer-derivation loop in `listConnections` (keep registry `s.conns.List`; in 1A `s.conns` is still nil so the page is empty until Plan 1B wires `connection.Service` — acceptable, the offers page is unaffected).

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./... && go build ./...`
Expected: PASS. Then a real docker smoke test:
```bash
MERCATOR_ADAPTER=docker MERCATOR_DOCKER_ARCH=amd64 MERCATOR_API_TOKEN=t \
  MERCATOR_SQLITE_DSN='file:/tmp/cp.db' go run ./cmd/mercator &
curl -s 'http://127.0.0.1:8080/v1/offers?workspace_id=ws_1' -H 'Authorization: Bearer t' | grep offer_docker_loopback
```
Expected: the docker offer appears (now served through the Broker). Create a run with a digest-pinned amd64 image and confirm it reaches `closed/succeeded` (same as before the refactor).

- [ ] **Step 5: Commit**

```bash
git add cmd/mercator internal/httpapi
git commit -m "feat: serve docker through the multi-connection Broker; drop fake runtime adapter"
```

---

## Self-review notes (already applied)

- **Spec coverage (1A scope):** credential resolver+env+mercator (Tasks 2–3), Verify (Task 4), factory (Task 5), Broker aggregation+routing (Tasks 6–7), wiring+bootstrap+drop-fake (Task 8), ADR 0002 (Task 1). Connection-management API/UI and the `connection.Service`→`Connections` adapter are **Plan 1B**. Webhook sink, workload reporting, RunPod are pieces 2–4.
- **No secrets in the event log:** enforced — only `{source, ref}` is ever persisted to events; ciphertext is in `connection_secret`.
- **Type consistency:** `ConnRef`, `Credential{Source,Ref}`, `Resolve(ctx, workspaceID, Credential)`, `Factory.Build(adapterType, config, secret)`, and the new `WorkspaceID`/`ConnectionID` request fields are used consistently across Tasks 2–8.
- **Known follow-ups for 1B:** real `connection.Service`-backed `broker.Connections`; SQLite secret store wired to the server DB; `POST /v1/connections` (+ inline secret) and `:authorize` (Verify); UI add/authorize.
