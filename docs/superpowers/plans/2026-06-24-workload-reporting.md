# Workload Reporting (Piece 3) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Let a workload report its own lifecycle events (and exit code) back to Mercator over HTTP — provider-agnostic — so we get rich, in-band telemetry on the run's event stream and, later, authoritative exit codes for providers (RunPod) whose control plane doesn't expose them.

**Architecture:** At launch, Mercator injects `MERCATOR_RUN_ID`, `MERCATOR_REPORT_URL`, and a per-run HMAC token (`MERCATOR_RUN_TOKEN`) into the container env. A new `POST /v1/runs/{id}:report` endpoint — **exempt from operator-token auth, validated by the run token** — records the reported payload as a `compute.run.reported.v1` event on the run stream (visible in the Events timeline). Thin SDK reporters (TS + Python) wrap the HTTP contract. The cloudflared public URL is used to validate the path end-to-end.

**Tech Stack:** Go 1.25 (no cgo), `crypto/hmac`+`crypto/sha256`, existing `internal/{httpapi,orchestrator,eventlog}`; TS + Python SDKs in `sdk/`; cloudflared for the public-URL check.

## Global Constraints

- Module `github.com/benngarcia/mercator`. Pure Go, no cgo.
- **The run token authorizes ONLY `:report` for its own run** — not any operator API. The operator-token middleware must NOT accept run tokens, and the `:report` endpoint must NOT accept the operator token in place of a valid run token's run-scoping (a valid run token for run A cannot report for run B).
- Run token is **stateless HMAC** (`HMAC-SHA256(reportKey, runID)`, base64url) — no storage. `reportKey` derives from `MERCATOR_SECRET_KEY` (or a dedicated `MERCATOR_REPORT_KEY`); if no key is configured, reporting env is NOT injected and the endpoint returns 501 (reporting disabled) — fail closed, no insecure default.
- Injected env values are literal (ADR 0001-consistent). The report URL base comes from `MERCATOR_PUBLIC_URL`; if unset, reporting env is not injected (nothing to call back).
- **Scope decision (deferral):** this piece builds the reporting *channel* — env injection, ingest, `compute.run.reported.v1` events, SDKs. Using a reported exit as the run's *authoritative outcome* (reconciling with the adapter's `Observe`) lands with the **RunPod adapter** (piece 4), where a non-native-exit provider actually needs it. Here, `reportExit(code)` records a `compute.run.reported.v1` event carrying `exit_code` (visible, queryable) but does not yet rewrite the run outcome. Flagged for review.
- TDD; `go test ./...` green after each task.

---

### Task 1: Run token — mint + verify

**Files:**
- Create: `internal/reporting/token.go`, `internal/reporting/token_test.go`

**Interfaces:**
- Produces: `type Signer struct{ key []byte }`; `func NewSigner(key []byte) *Signer` (nil/empty key → `Enabled()==false`); `func (s *Signer) Token(runID string) string` (base64url of `HMAC-SHA256(key, runID)`); `func (s *Signer) Verify(runID, token string) bool` (constant-time compare; false when disabled); `func (s *Signer) Enabled() bool`.

- [ ] **Step 1: Write the failing test**
```go
package reporting

import "testing"

func TestTokenRoundTripAndScoping(t *testing.T) {
	s := NewSigner([]byte("0123456789abcdef0123456789abcdef"))
	if !s.Enabled() { t.Fatal("signer should be enabled with a key") }
	tok := s.Token("run_a")
	if tok == "" { t.Fatal("empty token") }
	if !s.Verify("run_a", tok) { t.Fatal("token should verify for its run") }
	if s.Verify("run_b", tok) { t.Fatal("token must NOT verify for a different run") }
	if s.Verify("run_a", "garbage") { t.Fatal("garbage token must not verify") }
}

func TestDisabledSignerVerifiesNothing(t *testing.T) {
	s := NewSigner(nil)
	if s.Enabled() { t.Fatal("nil key → disabled") }
	if s.Verify("run_a", s.Token("run_a")) { t.Fatal("disabled signer must verify nothing") }
}
```
- [ ] **Step 2: Run → FAIL** (`go test ./internal/reporting/` → undefined).
- [ ] **Step 3: Implement**
```go
// Package reporting mints and verifies per-run HMAC tokens that authorize a
// workload to report events for its own run, and nothing else.
package reporting

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
)

type Signer struct{ key []byte }

func NewSigner(key []byte) *Signer { return &Signer{key: key} }

func (s *Signer) Enabled() bool { return len(s.key) > 0 }

func (s *Signer) Token(runID string) string {
	if !s.Enabled() {
		return ""
	}
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(runID))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Signer) Verify(runID, token string) bool {
	if !s.Enabled() || token == "" {
		return false
	}
	want := s.Token(runID)
	return subtle.ConstantTimeCompare([]byte(want), []byte(token)) == 1
}
```
- [ ] **Step 4: Run → PASS**. **Step 5: Commit** `feat(reporting): per-run HMAC token mint/verify`.

---

### Task 2: Inject reporting env at launch

**Files:**
- Modify: `internal/orchestrator/orchestrator.go` (`Orchestrator` gains a reporting config; `buildLaunchRequest` injects env)
- Modify: `internal/orchestrator/orchestrator_test.go`

**Interfaces:**
- Consumes: `reporting.Signer`.
- Produces: `Orchestrator` gains a `reporting` field set via a new `func WithReporting(publicURL string, signer *reporting.Signer) Option` (or an extra param to `New` — match the existing constructor style; if `New` has no options, add a `SetReporting`/option). When `publicURL != "" && signer.Enabled()`, `buildLaunchRequest` appends to `Environment`: `MERCATOR_RUN_ID=<runID>`, `MERCATOR_REPORT_URL=<publicURL>` , `MERCATOR_RUN_TOKEN=<signer.Token(runID)>`. These are appended to the container's existing env bindings (do not overwrite workload env).

- [ ] **Step 1: Write the failing test** — construct an orchestrator with reporting configured (public URL + a signer), drive a run to the launch-intent, and assert the recorded `LaunchRequest.Environment` contains `MERCATOR_RUN_ID`/`MERCATOR_REPORT_URL`/`MERCATOR_RUN_TOKEN` with the right values (token == signer.Token(runID)). Mirror the existing orchestrator test setup. Also assert: with reporting NOT configured, those vars are absent (no regression).
- [ ] **Step 2: Run → FAIL.**
- [ ] **Step 3: Implement** — add the reporting config to the orchestrator; thread `runID`+config into `buildLaunchRequest` (it already takes `runID`); append the three bindings when enabled. Keep `buildLaunchRequest` a pure function — pass the public URL + token (or signer) as params rather than reaching globals.
- [ ] **Step 4: Run → PASS** (`go test ./internal/orchestrator/`). **Step 5: Commit** `feat(orchestrator): inject run-scoped reporting env at launch`.

---

### Task 3: Auth carve-out for the report endpoint

**Files:**
- Modify: `internal/httpapi/server.go` (`ServeHTTP`)
- Modify: `internal/httpapi/server_test.go` or a new `reporting_test.go`

**Interfaces:**
- Produces: `ServeHTTP` exempts `POST /v1/runs/<id>:report` from the operator-token check (the handler will validate the run token). Concretely: in the operator-token gate, skip the check when `r.Method == POST && strings.HasPrefix(path, "/v1/runs/") && strings.HasSuffix(path, ":report")`. All other `/v1/` paths are unchanged.

- [ ] **Step 1: Write the failing test** — with `WithBearerAuth` set, a `POST /v1/runs/run_x:report` WITHOUT an operator token is NOT rejected by the middleware with the operator 401 (it reaches the handler — which, until Task 4, 404s or 501s; assert it's NOT the operator "Bearer token is required" 401). And a GET `/v1/runs?...` without a token IS still 401 (no regression).
- [ ] **Step 2: Run → FAIL** (the report path currently hits the operator 401).
- [ ] **Step 3: Implement** the carve-out in `ServeHTTP`.
- [ ] **Step 4: Run → PASS.** **Step 5: Commit** `feat(httpapi): exempt :report from operator-token auth (run-token authed)`.

---

### Task 4: `POST /v1/runs/{id}:report` ingest

**Files:**
- Modify: `internal/httpapi/server.go` (route via `runAction` suffix parse; `reportRun` handler; `Server` gains `reportSigner *reporting.Signer` via `WithReportSigner` Option; record the event)
- Modify: `internal/orchestrator` or `internal/eventlog` usage to append `compute.run.reported.v1` (use the same append path the orchestrator uses for run events; expose a minimal `orch.RecordReport(ctx, workspaceID, runID, payload)` if needed, or append via the event log directly with the run's stream key — prefer a small orchestrator method so stream/versioning stays in one place)
- Modify: `internal/httpapi/reporting_test.go`

**Interfaces:**
- Consumes: `reporting.Signer.Verify`; the run's stream.
- Produces: `reportRun(w, r, runID)` — reads `Authorization: Bearer <run-token>`; `401` if `reportSigner == nil` → actually 501 REPORTING_DISABLED; `401` if token missing/invalid for `runID`; body `{type string, data json.RawMessage, exit_code *int}`; appends a `compute.run.reported.v1` event to the run's stream with `{type, data, exit_code}`; `202`. `runAction` routes `:report` → `reportRun`.

- [ ] **Step 1: Write the failing test** — build a server with `WithReportSigner(signer)` + a real event log/orchestrator over an in-memory DSN; create a run; POST `/v1/runs/<id>:report` with `Authorization: Bearer <signer.Token(id)>` and body `{"type":"progress","data":{"pct":50}}` → 202; then `GET /v1/runs/<id>/events` shows a `compute.run.reported.v1` event with the data. Wrong token → 401; a valid token for a DIFFERENT run id → 401; no signer configured → 501.
- [ ] **Step 2: Run → FAIL.**
- [ ] **Step 3: Implement** the handler + `runAction` routing + the event append (small orchestrator method `RecordReport` that appends to the run stream with correct stream versioning, mirroring how other run events are appended). `compute.run.reported.v1` Data = `{type, data, exit_code}`, Visibility public.
- [ ] **Step 4: Run → PASS** (`go test ./internal/httpapi/ ./internal/orchestrator/`). **Step 5: Commit** `feat(httpapi): POST /v1/runs/{id}:report ingest (run-token authed)`.

---

### Task 5: Surface reported events in the console

**Files:**
- Modify: `web/app/src/lib/format.ts` (humanize `compute.run.reported.v1` → e.g. "Workload report")
- Modify: `web/app/src/components/runs/EventTimeline.tsx` if needed (tone for the new type — it likely already falls through to a default; verify it renders the `data` payload)
- Modify: `web/static` (rebuilt)

- [ ] **Step 1:** Add the humanizer entry; confirm `EventTimeline` renders the reported event + its `data` via the existing `JsonViewer` expansion. `bunx tsc --noEmit` clean; `bun run build`.
- [ ] **Step 2: Verify in browser** — create a run (docker), `curl` a `:report` to it (using a token minted via the configured key), confirm "Workload report" appears in the Events tab with the payload. Screenshot.
- [ ] **Step 3: Commit** `feat(web): surface workload-reported events in the timeline`.

---

### Task 6: SDK reporters (TS + Python)

**Files:**
- Modify: `sdk/typescript/src/*` (add a `reporter` reading `MERCATOR_RUN_ID`/`MERCATOR_REPORT_URL`/`MERCATOR_RUN_TOKEN`; `report(event)` / `reportExit(code)`)
- Modify: `sdk/python/src/mercator/*` (same)
- Modify: `sdk/typescript/test/*`, `sdk/python/tests/*`

**Interfaces:**
- Produces: TS `createReporter()` (reads env; `report({type,data})` POSTs `:report` with the run-token bearer; `reportExit(code)` posts `{type:"exit", exit_code}`; no-op + warn when env absent). Python `from mercator import run_reporter` equivalent (+ a context manager that reports exit on `__exit__`).

- [ ] **Step 1 (TS):** add the reporter, mirroring the existing SDK client's request style; unit test against a mock server (asserts URL, bearer header, body). Run the TS test suite.
- [ ] **Step 2 (Python):** add the reporter; unit test against a mock/recording handler (mirror the existing python client tests). Run the python tests.
- [ ] **Step 3: Commit** `feat(sdk): workload reporter (typescript + python)`.

(If the two SDKs are large, split into 6a/6b — one commit each — during execution.)

---

### Task 7: cloudflared public-path verification

**Files:** none (verification task) — produces a runbook note appended to `docs/production/` if useful.

- [ ] **Step 1:** Install cloudflared (`brew install cloudflared`). Start Mercator (docker adapter) with `MERCATOR_PUBLIC_URL` set to the cloudflared URL + `MERCATOR_SECRET_KEY` set (enables the signer). Run `cloudflared tunnel --url http://localhost:8080` → capture the public `https://*.trycloudflare.com` URL.
- [ ] **Step 2:** Create a run; mint a token for it (the server injected it, or compute via the configured key); from OUTSIDE (a plain `curl` to the public URL, simulating the workload) POST `:report` → confirm 202 and the event appears in `/v1/runs/<id>/events`. This validates the exact path a RunPod pod will use.
- [ ] **Step 3:** Append a short "Workload reporting + tunnel" runbook to `docs/production/` (env vars, the cloudflared command, the report contract). Commit `docs: workload reporting + cloudflared runbook`.

---

## Self-review notes (already applied)

- **Spec coverage:** run-scoped token (T1), env injection (T2), auth carve-out (T3), `:report` ingest + `compute.run.reported.v1` (T4), console surfacing (T5), SDK reporters (T6), public-path validation (T7). Matches umbrella spec section 3.
- **Auth safety:** the run token authorizes ONLY its run's `:report` (HMAC over runID; T1 tests cross-run rejection); the carve-out (T3) is narrowly scoped to `POST /v1/runs/*:report`; the operator-token path is otherwise unchanged.
- **Fail-closed:** no signer / no public URL → no env injected + 501 on the endpoint; no insecure default token.
- **Deferral flagged:** authoritative-outcome integration (reported exit → run outcome) lands with the RunPod adapter (piece 4), where it's needed; here `reportExit` records a visible event only.
- **Type consistency:** `reporting.Signer.{Token,Verify,Enabled}`, the orchestrator reporting option, `WithReportSigner`, and the `compute.run.reported.v1` payload shape `{type, data, exit_code}` are used consistently across T1–T6.
