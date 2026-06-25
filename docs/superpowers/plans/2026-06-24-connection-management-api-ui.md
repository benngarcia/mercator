# Connection Management API + UI (Plan 1B) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Make connections manageable from the API and UI — create a connection (with an `env` or encrypted `mercator` credential), authorize it via a provider test-call, and have the Broker serve offers/runs from the real `connection.Service` registry (replacing 1A's static bootstrap).

**Architecture:** `connection.Record` gains `Config` + `Credential` (a `{source, ref}` reference — never a secret). A `connbroker` adapter maps `connection.Service` → `broker.Connections`, so the Broker reads the live registry. `cmd/mercator` registers the env-configured docker adapter as a real registry connection at startup, opens a second `*sql.DB` on the DSN for the SQLite secret store, and threads the connection service + secret store + credential resolver + a per-connection `Verifier` (the Broker) into the HTTP server. New handlers `POST /v1/connections` (encrypt+store the secret for `mercator`, then `Create`) and `POST /v1/connections/{id}:authorize` (build the adapter + `Verify`, then mark authorized). The console gets an Add-connection dialog + per-row Authorize.

**Tech Stack:** Go 1.25 (no cgo), `modernc.org/sqlite`, existing `internal/{connection,broker,credential,httpapi}`; React + shadcn + TanStack Query/Router (web/app), Bun build embedded in Go.

## Global Constraints

- Module `github.com/benngarcia/mercator`. Pure Go, no cgo.
- **Secrets never enter the event log.** The `connection.created` event persists only `{source, ref}` (+ non-secret config). For `mercator`, the inline secret is AES-256-GCM encrypted (`credential.Seal`) and written to the `connection_secret` table via the SQLite secret store BEFORE `Create`; `ref` = the connection id. No read API ever returns the secret.
- `mercator` source requires `MERCATOR_SECRET_KEY` (decodes to 32 bytes). Absent → creating a `mercator`-credential connection returns a clear 4xx error (the resolver already fails closed).
- Builds on 1A: `connection.Record{ID, WorkspaceID, AdapterType, AuthorizationSchema, Authorized}`, `broker.ConnRef{ID, AdapterType, Config, Credential, Authorized}`, `credential.{Credential, Resolver, Seal, NewSQLiteStore}`, the `Broker`, and `cmd/mercator buildBroker` (which this plan rewires from static to registry-backed).
- No regression: the docker bootstrap connection still appears (now as a real registry entry, authorized) and docker runs still work end-to-end.
- TDD; `go test ./...` green after every task. Frontend: `tsc` clean + `bun run build` + browser check.

---

### Task 1: Extend `connection.Record` with Config + Credential

**Files:**
- Modify: `internal/connection/connection.go` (`Record`, `CreateRequest`, `Create`)
- Modify: `internal/connection/connection_test.go`

**Interfaces:**
- Produces: `Record` and `CreateRequest` gain `Config map[string]string` and `Credential credential.Credential` (json: `config`, `credential`). `Create` persists them; `Get`/`reduceConnection` round-trip them (already marshals the whole Record — verify Credential/Config survive).

- [ ] **Step 1: Write the failing test**

```go
func TestCreateRoundTripsConfigAndCredential(t *testing.T) {
	svc := newTestService(t) // existing helper; if absent, build via eventlog.OpenSQLite in-memory like other tests
	_, err := svc.Create(context.Background(), CreateRequest{
		WorkspaceID:  "ws_1",
		ConnectionID: "conn_rp",
		AdapterType:  "runpod",
		Config:       map[string]string{"region": "us"},
		Credential:   credential.Credential{Source: "mercator", Ref: "conn_rp"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := svc.Get(context.Background(), "ws_1", "conn_rp")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Config["region"] != "us" {
		t.Errorf("config not round-tripped: %+v", got.Config)
	}
	if got.Credential.Source != "mercator" || got.Credential.Ref != "conn_rp" {
		t.Errorf("credential not round-tripped: %+v", got.Credential)
	}
}
```

(If there's no `newTestService` helper, mirror the existing test setup in `connection_test.go` to construct a `*Service` over an in-memory event log.)

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/connection/ -run TestCreateRoundTripsConfigAndCredential`
Expected: FAIL — `unknown field Config in struct literal` (Record/CreateRequest lack the fields).

- [ ] **Step 3: Write minimal implementation**

In `internal/connection/connection.go`:
- add import `"github.com/benngarcia/mercator/internal/credential"`.
- `Record`: add
  ```go
  Config     map[string]string     `json:"config,omitempty"`
  Credential credential.Credential `json:"credential,omitempty"`
  ```
- `CreateRequest`: add `Config map[string]string` and `Credential credential.Credential`.
- In `Create`, set them on the record before marshaling:
  ```go
  record := Record{
      ID: req.ConnectionID, WorkspaceID: req.WorkspaceID, AdapterType: req.AdapterType,
      AuthorizationSchema: cloneStringMap(req.AuthorizationSchema),
      Config: cloneStringMap(req.Config), Credential: req.Credential,
  }
  ```
`reduceConnection` already unmarshals the whole Record from the created event, so Config/Credential round-trip with no further change.

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/connection/`
Expected: PASS (new + existing).

- [ ] **Step 5: Commit**

```bash
git add internal/connection
git commit -m "feat(connection): persist config + credential reference on connections"
```

---

### Task 2: `connbroker` — map connection.Service to broker.Connections

**Files:**
- Create: `internal/connbroker/connbroker.go`
- Create: `internal/connbroker/connbroker_test.go`

**Interfaces:**
- Consumes: `connection.Service.List(ctx, workspaceID) ([]connection.Record, error)`; `broker.ConnRef`.
- Produces: `func New(svc *connection.Service) broker.Connections` — `List` maps each `connection.Record` → `broker.ConnRef{ID, AdapterType, Config, Credential, Authorized}`.

- [ ] **Step 1: Write the failing test**

```go
package connbroker

import (
	"context"
	"testing"

	"github.com/benngarcia/mercator/internal/connection"
	"github.com/benngarcia/mercator/internal/credential"
	"github.com/benngarcia/mercator/internal/eventlog"
)

func TestListMapsRecordsToConnRefs(t *testing.T) {
	log, err := eventlog.OpenSQLite(context.Background(), "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil { t.Fatalf("open: %v", err) }
	t.Cleanup(func() { _ = log.Close() })
	svc := connection.New(log)
	if _, err := svc.Create(context.Background(), connection.CreateRequest{
		WorkspaceID: "ws_1", ConnectionID: "conn_a", AdapterType: "docker",
		Config: map[string]string{"host": "loopback"},
		Credential: credential.Credential{Source: "env", Ref: "K"},
	}); err != nil { t.Fatalf("create: %v", err) }

	refs, err := New(svc).List(context.Background(), "ws_1")
	if err != nil { t.Fatalf("list: %v", err) }
	if len(refs) != 1 || refs[0].ID != "conn_a" || refs[0].AdapterType != "docker" ||
		refs[0].Config["host"] != "loopback" || refs[0].Credential.Ref != "K" {
		t.Fatalf("unexpected ref mapping: %+v", refs)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/connbroker/`
Expected: FAIL — `undefined: New`.

- [ ] **Step 3: Write minimal implementation**

```go
// Package connbroker adapts the connection.Service registry to the
// broker.Connections interface the Broker consumes.
package connbroker

import (
	"context"

	"github.com/benngarcia/mercator/internal/broker"
	"github.com/benngarcia/mercator/internal/connection"
)

type service struct{ svc *connection.Service }

func New(svc *connection.Service) broker.Connections { return service{svc: svc} }

func (s service) List(ctx context.Context, workspaceID string) ([]broker.ConnRef, error) {
	records, err := s.svc.List(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	refs := make([]broker.ConnRef, 0, len(records))
	for _, r := range records {
		refs = append(refs, broker.ConnRef{
			ID: r.ID, AdapterType: r.AdapterType, Config: r.Config,
			Credential: r.Credential, Authorized: r.Authorized,
		})
	}
	return refs, nil
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/connbroker/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/connbroker
git commit -m "feat(connbroker): map connection.Service to broker.Connections"
```

---

### Task 3: `Broker.VerifyConnection` (build one connection's adapter + Verify)

**Files:**
- Modify: `internal/broker/broker.go`
- Modify: `internal/broker/broker_test.go`

**Interfaces:**
- Produces: `func (b *Broker) VerifyConnection(ctx context.Context, workspaceID, connectionID string) error` — looks up the connection, builds its adapter (resolving its credential), and calls `adapter.Verify`. Returns `ErrConnectionNotFound` for unknown ids.

- [ ] **Step 1: Write the failing test**

```go
func TestBrokerVerifyConnectionBuildsAndVerifies(t *testing.T) {
	var verified string
	f := NewFactory()
	f.Register("stub", func(cfg map[string]string, _ string) (adapter.Adapter, error) {
		return verifyAdapter{id: cfg["id"], verified: &verified}, nil
	})
	conns := fakeConns{recs: []ConnRef{
		{ID: "conn_a", AdapterType: "stub", Authorized: false, Config: map[string]string{"id": "conn_a"}},
	}}
	b := NewBroker(conns, f, nilResolver{})
	if err := b.VerifyConnection(context.Background(), "ws_1", "conn_a"); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if verified != "conn_a" {
		t.Fatalf("expected Verify on conn_a, got %q", verified)
	}
	if err := b.VerifyConnection(context.Background(), "ws_1", "nope"); !errors.Is(err, ErrConnectionNotFound) {
		t.Fatalf("expected ErrConnectionNotFound, got %v", err)
	}
}
```

Add a `verifyAdapter` stub (embeds `adapter.Adapter`; `Verify` sets `*verified = id` and returns nil). Note: `VerifyConnection` must NOT require `Authorized` (you authorize *before* the connection is authorized).

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/broker/ -run TestBrokerVerifyConnection`
Expected: FAIL — `b.VerifyConnection undefined`.

- [ ] **Step 3: Write minimal implementation**

```go
// VerifyConnection builds the adapter for one connection (regardless of its
// current Authorized state — authorize runs before the flag is set) and calls
// its cheap Verify check. Used by the connection authorize flow.
func (b *Broker) VerifyConnection(ctx context.Context, workspaceID, connectionID string) error {
	_, ad, err := b.connByID(ctx, workspaceID, connectionID)
	if err != nil {
		return err
	}
	return ad.Verify(ctx)
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/broker/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/broker
git commit -m "feat(broker): VerifyConnection builds one connection's adapter and verifies"
```

---

### Task 4: Server wiring — thread connection service, secret store, resolver, verifier

**Files:**
- Modify: `internal/httpapi/server.go` (`Server` struct fields; `Option`s `WithSecretStore`, `WithCredentialResolver`, `WithVerifier`; `NewWithAllServices` unchanged signature — the new deps arrive via options).
- Modify: `cmd/mercator/main.go` (`buildBroker` → build connection.Service-backed registry; open the secret-store DB; register the bootstrap docker connection into the service; pass deps to the handler via options).
- Modify: `cmd/mercator/main_test.go` (the broker test now goes through the registry).

**Interfaces:**
- Consumes: `connbroker.New`, `credential.NewSQLiteStore`, `credential.NewResolver`, `Broker.VerifyConnection`.
- Produces: `Server` holds `secretStore credential.SecretStore`, `resolver *credential.Resolver`, `verifier interface{ VerifyConnection(ctx, ws, id string) error }`. Functional options set them. A `cmd/mercator` helper `buildServerDeps(values) (broker, *connection.Service, credential.SecretStore, *credential.Resolver, error)` (or inline) constructs everything sharing one event log + one secret-store DB.

This task is wiring; key requirements:
- `cmd/mercator` opens the event log once (or reuses the handler's), builds `connection.Service` over it, opens a SECOND `*sql.DB` via `sql.Open("sqlite", dsn)` for `credential.NewSQLiteStore`, builds the resolver (`env` getter from `values` + the SQLite store + master key), the factory (register `docker`), and the Broker via `connbroker.New(svc)`.
- Register the bootstrap docker connection into `svc` at startup **idempotently** (Create is idempotent by command key `connection:create:<id>`; calling it on every boot is safe) and authorize it (UpdateAuthorization true) so it serves offers. Its `Config` carries bin/host/context; `Credential` is empty (docker needs none).
- Because `HandlerForSQLiteWithAdapter` builds its OWN `connection.Service` internally, add a new constructor `HandlerForSQLiteWithBroker(ctx, dsn, ad adapter.Adapter, conns *connection.Service, opts...)` OR (simpler) extend the wiring so the server's `conns` is the SAME service the Broker reads. Cleanest: add `WithConnections(*connection.Service)` option used by `NewWithAllServices` when present (falls back to `connection.New(log)`), and have `cmd/mercator` pass the shared service. Pick the option approach to avoid a constructor explosion.

- [ ] **Step 1: Write the failing test**

In `cmd/mercator/main_test.go`, replace `TestRuntimeBrokerServesDockerConnection` with a registry-backed version:
```go
func TestBrokerServesRegisteredDockerConnection(t *testing.T) {
	deps := buildServerDeps(map[string]string{
		"MERCATOR_ADAPTER": "docker", "MERCATOR_DOCKER_ARCH": "amd64",
		"MERCATOR_SQLITE_DSN": "file:" + t.Name() + "?mode=memory&cache=shared",
	})
	defer deps.close()
	offers, err := deps.broker.ListOffers(context.Background(), adapter.OfferRequest{WorkspaceID: "ws_1"})
	if err != nil { t.Fatalf("list offers: %v", err) }
	if len(offers) != 1 || offers[0].AdapterType != "docker" || offers[0].ConnectionID == "" {
		t.Fatalf("expected one docker offer from the registered connection, got %+v", offers)
	}
	conns, err := deps.conns.List(context.Background(), "ws_1")
	if err != nil { t.Fatalf("list conns: %v", err) }
	if len(conns) != 1 || !conns[0].Authorized {
		t.Fatalf("expected one authorized registered connection, got %+v", conns)
	}
}
```
(Shape `buildServerDeps` to return a small struct `{broker *broker.Broker; conns *connection.Service; close func()}`.)

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./cmd/mercator/ -run TestBrokerServesRegisteredDockerConnection`
Expected: FAIL — `undefined: buildServerDeps`.

- [ ] **Step 3: Write minimal implementation**

Implement `buildServerDeps(values)` constructing the shared event log, connection.Service, secret-store DB + store, resolver, factory (docker), registering+authorizing the bootstrap docker connection, and the Broker over `connbroker.New(svc)`. Add the server options (`WithConnections`, `WithSecretStore`, `WithCredentialResolver`, `WithVerifier`) and have `run(...)` build the handler passing the broker as the adapter + the shared connection.Service + secret store + resolver + broker-as-verifier. Remove the 1A `staticConnections` path.

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./... && go build ./...`
Expected: PASS. Docker smoke: start the server, confirm `/v1/offers` and `/v1/connections?workspace_id=ws_1` both show the docker connection (now registry-backed, authorized).

- [ ] **Step 5: Commit**

```bash
git add internal/httpapi cmd/mercator
git commit -m "feat: registry-backed Broker connections + secret store wired into the server"
```

---

### Task 5: `POST /v1/connections` (create, with secret handling)

**Files:**
- Modify: `internal/httpapi/server.go` (route + `createConnection` handler + request type)
- Modify: `internal/httpapi/connections_test.go`

**Interfaces:**
- Consumes: `s.conns.Create`, `s.secretStore`, `credential.Seal`, the master key (via resolver or a stored key on the server).
- Produces: `POST /v1/connections` accepting `{workspace_id, connection_id, adapter_type, config, credential:{source,ref}, secret?}`. For `source=="mercator"` with a non-empty `secret`: `Seal` + `secretStore.Put(ws, connection_id, blob)`, set `credential.Ref = connection_id`, then `Create`. The response is the created `connection.Record` (no secret). Missing master key + `mercator` source → 400.

- [ ] **Step 1: Write the failing test**

```go
func TestCreateConnectionStoresSecretOutOfBand(t *testing.T) {
	store := credential.NewMemoryStore()
	handler := newHTTPTestServerWithOptions(t,
		WithConnectionsService(/* a connection.Service over the test log */),
		WithSecretStore(store),
		WithCredentialResolver(credential.NewResolver(nil, store, testKey32())),
	)
	body := mustMarshal(t, createConnectionBody{
		WorkspaceID: "ws_1", ConnectionID: "conn_rp", AdapterType: "runpod",
		Credential: credential.Credential{Source: "mercator"}, Secret: "rp_live_key",
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/connections", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "k1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted && rec.Code != http.StatusCreated {
		t.Fatalf("expected 201/202, got %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "rp_live_key") {
		t.Fatal("response must not echo the secret")
	}
	// secret is retrievable (encrypted) and decrypts to the original
	blob, err := store.Get(context.Background(), "ws_1", "conn_rp")
	if err != nil { t.Fatalf("secret not stored: %v", err) }
	plain, _ := credential.Open(testKey32(), blob)
	if string(plain) != "rp_live_key" { t.Fatalf("stored secret wrong: %q", plain) }
}
```

(Add test helpers/options as needed; `testKey32()` returns a 32-byte key. If wiring a `connection.Service` into the test server is heavy, add a `WithConnectionsService` option mirroring Task 4.)

- [ ] **Step 2: Run to verify it fails** → handler/route/body type undefined.
- [ ] **Step 3: Implement** the `createConnection` handler + `createConnectionBody` (with a write-only `Secret string json:"secret,omitempty"`), register `POST /v1/connections`, do the seal+store-then-create flow, and return the record without the secret. Guard: `mercator` source + no master key → `writeError(400, "SECRET_STORE_DISABLED", ...)`.
- [ ] **Step 4: Run** `go test ./internal/httpapi/` → PASS.
- [ ] **Step 5: Commit** `feat(httpapi): POST /v1/connections with out-of-band secret storage`.

---

### Task 6: `POST /v1/connections/{id}:authorize` + switch listConnections to the registry

**Files:**
- Modify: `internal/httpapi/server.go` (`connectionAction` for `:authorize`; `listConnections` → registry only)
- Modify: `internal/httpapi/connections_test.go`

**Interfaces:**
- Consumes: `s.verifier.VerifyConnection(ctx, ws, id)`, `s.conns.UpdateAuthorization`.
- Produces: `POST /v1/connections/{conn_action}` parsing `{id}:authorize` → `VerifyConnection`; on success `UpdateAuthorization(authorized=true)` and return the updated record; on Verify failure → 502 with the error, connection stays unauthorized. `listConnections` returns `s.conns.List` only (remove the offer-derivation block from 1A — the registry now holds the bootstrap connection).

- [ ] **Step 1: Write the failing test** (authorize a created connection whose adapter's `Verify` is stubbed to succeed → record becomes `authorized:true`; a stubbed-failing `Verify` → stays false + non-2xx). Use a fake `Verifier` injected via `WithVerifier`.
- [ ] **Step 2: Run** → `:authorize` route/handler undefined → FAIL.
- [ ] **Step 3: Implement** `connectionAction` (mirror `runAction`/`sinkAction` suffix-parsing), the authorize path, and the `listConnections` simplification.
- [ ] **Step 4: Run** `go test ./...` → PASS. Docker smoke: the bootstrap connection still lists as authorized.
- [ ] **Step 5: Commit** `feat(httpapi): POST /v1/connections/{id}:authorize; registry-backed connection list`.

---

### Task 7: Frontend — types, endpoints, and mutation hooks

**Files:**
- Modify: `web/app/src/lib/api/types.ts` (Connection type: `config`, `credential:{source,ref}`)
- Modify: `web/app/src/lib/api/endpoints.ts` (`createConnection`, `authorizeConnection`)
- Modify: `web/app/src/lib/api/queries.ts` (`useCreateConnection`, `useAuthorizeConnection` — invalidate the connections + offers queries on success)

**Interfaces:**
- Produces: `useCreateConnection()` (POST /v1/connections, auto Idempotency-Key) and `useAuthorizeConnection()` (POST /v1/connections/{id}:authorize); both invalidate `keys.connections(workspace)` and `keys.offers(workspace)`.

- [ ] **Step 1:** Add the `Connection` fields + endpoint fns + hooks following the existing patterns in those files (mirror `useCreateRun`/`useCancelRun`). No new deps.
- [ ] **Step 2:** `bunx tsc --noEmit` clean.
- [ ] **Step 3: Commit** `feat(web): connection create/authorize api hooks`.

(This task has no runtime test; it's typed glue verified by tsc + Task 8's UI exercise.)

---

### Task 8: Frontend — Add-connection dialog + Authorize action

**Files:**
- Create: `web/app/src/components/connections/AddConnectionDialog.tsx`
- Modify: `web/app/src/components/connections/ConnectionsTable.tsx` (Authorize action per row; an "Add connection" button opening the dialog)
- Modify: `web/app/src/routes/connections.tsx` (mount the Add-connection button in the page header)
- Modify: `web/static` (rebuilt assets)

**Interfaces:**
- Consumes: `useCreateConnection`, `useAuthorizeConnection`.

- [ ] **Step 1:** Build `AddConnectionDialog` (shadcn Dialog): fields — adapter_type Select (docker/runpod), connection_id, dynamic config (key/value rows), credential source Select (env/mercator) → either an env-var-name input (`env`) or a password Secret input (`mercator`); submit via `useCreateConnection`; surface `ApiError.details`/message inline. Add a per-row **Authorize** button (shown when `!authorized`) calling `useAuthorizeConnection`, with pending/authorized/error feedback. Follow the "Clear" design language (HIG, rounded, accent-soft) used across the console.
- [ ] **Step 2:** `bunx tsc --noEmit` clean; `bun run build`.
- [ ] **Step 3: Verify in browser** (docker adapter, seeded token): the bootstrap docker connection lists as authorized; "Add connection" creates a `mercator`-credential connection (paste a dummy key) → it appears unauthorized → Authorize (will fail for a fake provider, succeed for docker) updates status. Screenshot.
- [ ] **Step 4: Commit** `feat(web): add-connection dialog + authorize action`.

---

## Self-review notes (already applied)

- **Spec coverage:** connection model with config+credential (T1), registry-backed Broker (T2,T4), VerifyConnection (T3), `POST /v1/connections` with out-of-band secret (T5), `:authorize` + registry list (T6), UI hooks (T7) + dialog/authorize (T8). Matches the umbrella spec's "Connection management API" + "UI" + credential model.
- **No secrets in the event log / responses:** enforced in T5 (seal+store before Create; write-only `secret`; response omits it). The credential persisted is `{source, ref}` only.
- **Type consistency:** `connection.Record.{Config,Credential}`, `broker.ConnRef`, `credential.{Credential,Seal,Open,SecretStore,Resolver}`, `Broker.VerifyConnection`, and the server options (`WithConnections`/`WithSecretStore`/`WithCredentialResolver`/`WithVerifier`) are used consistently across T1–T8.
- **No regression:** the docker bootstrap connection is registered+authorized at startup (T4) so offers/runs and the Connections page keep working; the 1A static path + offer-derivation are removed only once the registry replaces them (T4/T6).
- **Carried follow-ups from 1A** (janitor ConnectionID; idempotency-hash drift) remain out of 1B scope.
