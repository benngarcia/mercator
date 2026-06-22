# Mercator Console — Frontend Rewrite Design

Date: 2026-06-22
Status: Approved (design phase)

## Problem

The current embedded UI (`web/static/{index.html,app.js,styles.css}`, vanilla JS)
is nonfunctional: the sidebar nav (Runs/Events/Decisions/Connections/Offers/Sinks)
is decorative — the buttons only toggle an `active` class and never switch views.
Only the Runs list + a single detail pane works, and it exposes a fraction of the
API. We are doing a complete rewrite as a real operator console.

## Goals

- A full operator console over the Mercator V1 REST API: read/inspect **and**
  mutate.
- Beautiful, dense, operator-grade UI built on **shadcn/ui**.
- Ships as part of the existing single Go binary — built by **Bun**, embedded via
  `//go:embed`, served at `/` with SPA fallback. Identical experience deployed and
  per-worktree-local. No CORS, no second process.
- No breaking changes to SDKs or CLI: the API stays at `/v1/*`.

## Non-goals

- Standalone `bun build --compile` executable (rejected: it would be a second
  server alongside the Go binary). Bun is the build *tool*, not a runtime.
- Moving the API behind `/api` (rejected: breaks TS SDK, Python SDK, CLI which
  target `/v1`).
- Mercator-owned secrets/secret_ref (out of scope per ADR 0001).

## Distribution & serving model

- **One server: the Go binary.**
  - API unchanged: `GET/POST /v1/*`, `/health/{live,ready}`, `/openapi.json`.
  - `GET /assets/*` → embedded hashed JS/CSS, `Cache-Control: max-age=31536000, immutable`.
  - `GET /` and any unmatched non-API GET → `index.html` (`no-cache`) for
    client-side routing. Go 1.22+ ServeMux precedence: the `/v1`, `/health`,
    `/openapi.json`, `/assets/` patterns are more specific and win; `serveUI`
    becomes the catch-all SPA fallback.
- **Bun = build tool.** `bun build` emits hashed, code-split, minified assets into
  `web/static/`; Go embeds them. `go build` always ships current assets via a
  `make ui` / mise task.

## Stack

| Concern | Choice |
|---|---|
| Framework | React 19 + TypeScript |
| Components | shadcn/ui (new-york style, Radix + Tailwind v4) |
| Styling | Tailwind CSS v4 + CSS theme tokens (light/dark) |
| Server state | TanStack Query |
| Routing | TanStack Router (typed route tree, Query-integrated loaders) |
| Build/dev | Bun (`bun build` + Bun HTML dev server w/ HMR) |
| Toasts | sonner |

TanStack Router is chosen for ecosystem consistency with TanStack Query: route
`loader`s call `queryClient.ensureQueryData(...)` for instant navigation off a warm
cache; params (`runId`, `workloadId`, `sinkId`) and search are type-safe;
`workspace_id` is a validated **search param** (default from `localStorage`) so a
workspace view is deep-linkable. The bearer token stays in `localStorage` only,
never in the URL.

## Directory organization

```
web/
  app/                         # NEW: frontend source (Bun-built)
    index.html                 # entry; #root + <script src="src/main.tsx">
    package.json bunfig.toml tsconfig.json
    build.ts                   # Bun.build → ../static
    src/
      main.tsx                 # QueryClient, RouterProvider, Theme, Toaster
      index.css                # Tailwind + shadcn theme tokens
      lib/
        api/
          types.ts             # TS mirror of domain ontology
          client.ts            # apiFetch<T>(): auth + workspace + error envelope
          endpoints.ts         # one typed fn per route
          queries.ts           # TanStack Query hooks
          keys.ts              # query-key factory
        session.ts             # workspace default + token (localStorage)
        format.ts              # bytes, USD, duration, relative-time, digest, phase
      components/
        ui/                    # shadcn primitives (generated)
        layout/                # AppShell, Sidebar, Topbar, WorkspaceSwitcher, TokenField, ThemeToggle
        common/                # PageHeader, DataTable, EmptyState, ErrorState, JsonViewer,
                               #   CopyButton, StatBlock, RelativeTime, ServiceDisabled
        runs/                  # RunStatusBadge, RunsTable, RunPhaseTimeline, EventTimeline,
                               #   DecisionPanel, CandidateTable, ViolationList, CreateRunForm,
                               #   EnvEditor, RunActions
        offers/                # OffersTable, OfferDetailSheet, PriceTag, ResourceSummary
        connections/           # ConnectionsTable
        workloads/             # WorkloadRevisionsTable, RevisionViewer, CreateWorkloadDialog, CreateRevisionDialog
        sinks/                 # SinkStatusCard, SinkActionsBar, ReplayDialog, SinkResultCard
        placements/            # WorkloadSpecEditor (shared w/ CreateRun), PreviewResult
      routes/                  # typed route tree (one module per page)
      hooks/                   # useSession, useIsTerminal, usePollInterval
  static/                      # build OUTPUT (embedded); hand-written files deleted
  web.go                       # extended: AssetsFS() for /assets/* (existing embed kept)
```

## Domain ontology reference (the types the UI mirrors)

From `internal/domain/types.go` and the API:

- **Workload → WorkloadRevision** (`id`, `workspace_id`, `workload_id`, `digest`,
  `spec`). `WorkloadSpec`: containers, resources (cpu/mem/disk/accelerators),
  network, placement policy (objective: cheapest/fastest_start/fastest_completion/
  balanced), execution policy.
- **Run** (`id`, `workspace_id`, `workload_revision_id`, `phase`, `outcome?`,
  `exit_code?`, `cleanup`, `disposition?`, `closed`).
  - Phases: `requested → launching → running → cleaning_up → closed`.
  - Outcomes: `succeeded | failed | cancelled`.
  - Cleanup: `not_required | pending | confirmed | blocked`.
  - Disposition: `release | terminate`.
- **Attempt** (`id`, `run_id`, `launch_key`, `ownership_token`).
- **OfferSnapshot** (kind `standing|provisionable`, platform, resources,
  capability profile, network facts, pricing, queue, provisioning estimate, image
  cache, capacity, reliability).
- **PlacementDecision** (`model_version`, `policy`, `collection_report`,
  `candidates[]`, `selected_offer_snapshot_id`, `selection_reason_codes[]`).
  - **CandidateDecision** (`offer_snapshot_id`, adapter, `feasible`,
    `rejections[]` of `Violation`, `estimates`, `score_usd`).
  - **CandidateEstimates**: queue/provision/pull/start seconds + cost USD, each a
    p50/p90/expected/confidence `Estimate`.
  - **Violation** (`code`, `path`, `required`, `offered`, `message`).
- **CloudEvent** (public run events): `type`, `time`, `globalposition`,
  `streamversion`, `subject`, `data`. Types include
  `compute.run.requested.v1`, `…placement_decided.v1`, `…launch_accepted.v1`, etc.

## API surface consumed

Error envelope: `{ "code", "message", "details"?: Violation[] }`. Mutations require
an `Idempotency-Key` header.

| Hook | Endpoint | Behavior |
|---|---|---|
| `useRuns(ws)` | `GET /v1/runs?workspace_id` | poll 3s; `Run[]` |
| `useRun(id)` | `GET /v1/runs/{id}` | poll 2s while `!closed` |
| `useRunEvents(id)` | `GET /v1/runs/{id}/events` | poll 2s while `!closed` |
| `useRunDecision(id)` | `GET /v1/runs/{id}/decision` | once/on-demand; null on 404 |
| `useOffers()` | `GET /v1/offers` | poll 10s |
| `useConnections()` | `GET /v1/connections` | poll 10s |
| `useWorkloadRevisions(id)` / `useRevision()` | `GET /v1/workloads/{id}/revisions[/{rev}]` | 501 → ServiceDisabled |
| `useSinkStatus(id)` | `GET /v1/sinks/{id}` | on demand |
| `useHealth()` | `GET /health/{live,ready}` | Topbar dot |
| `useCreateRun()` | `POST /v1/runs` | 202 → invalidate runs, nav to detail |
| `useCancelRun()` / `useRefreshRun()` | `POST /v1/runs/{id}:cancel` / `:refresh` | invalidate run |
| `useCreateWorkload()` | `POST /v1/workloads` | mutation |
| `useCreateRevision()` | `POST /v1/workloads/{id}/revisions` | mutation |
| `usePreviewPlacement()` | `POST /v1/placements:preview` | `PlacementDecision`, no run created |
| `useResolveImage()` | `POST /v1/images:resolve` | resolved digest |
| `useDeliverSink()` / `useReplaySink()` | `POST /v1/sinks/{id}:deliver` / `:replay` | `SinkResult` |

## Data layer — public API

`lib/api/client.ts`:
```ts
class ApiError extends Error { status: number; code: string; details?: Violation[] }
// Injects Authorization: Bearer <token> and ?workspace_id; parses {code,message,details};
// throws ApiError on non-2xx; auto Idempotency-Key (uuid) for mutations unless supplied.
function apiFetch<T>(path: string, opts?: { method?; body?; idempotencyKey?; signal? }): Promise<T>
```

`lib/api/queries.ts` exposes the hooks in the table above. `useIsTerminal(run)` →
`run.closed` gates polling. Mutations surface `ApiError.details` (Violations) to
forms; query errors render `<ErrorState>`.

## Components — responsibilities & public props

**Layout**
- `AppShell` — grid: Sidebar + Topbar + `<Outlet/>`.
- `Sidebar` — nav (Runs / Create Run / Preview / Workloads / Offers / Connections /
  Sinks) with active-route highlight (real routing).
- `Topbar` — `WorkspaceSwitcher`, `TokenField`, health dot (`useHealth`),
  `ThemeToggle`.
- `WorkspaceSwitcher` `{ value; onChange }` — editable workspace id bound to the
  route search param; remembers recents.
- `TokenField` — reads/writes `session.ts`; never logged or put in URL.

**Common**
- `DataTable<T>` `{ columns; data; onRowClick?; isLoading; emptyState; rowKey }` —
  sortable, keyboard-navigable; skeleton rows while loading.
- `PageHeader` `{ title; description?; actions? }`
- `EmptyState` `{ icon?; title; description?; action? }`
- `ErrorState` `{ error: ApiError; onRetry? }`
- `ServiceDisabled` `{ feature }` (renders for 501s)
- `JsonViewer` `{ value; collapsed? }` — pretty, copyable raw JSON.
- `CopyButton` `{ value }` · `RelativeTime` `{ iso }` · `StatBlock` `{ label; value; mono? }`

**Runs**
- `RunStatusBadge` `{ phase; outcome?; closed }` — colors: requested·slate,
  launching·amber, running·blue, cleaning_up·violet, succeeded·emerald, failed·red,
  cancelled·zinc.
- `RunsTable` `{ runs; selectedId?; onSelect }` — id (mono), revision, phase,
  cleanup/disposition, exit code.
- `RunPhaseTimeline` `{ run }` — stepper over the five phases.
- `EventTimeline` `{ events }` — vertical timeline keyed off CloudEvent `type`
  (humanized), `time`, `globalposition`; rows expand to `JsonViewer(event.data)`.
- `DecisionPanel` `{ decision }` — selected offer, `model_version`, policy
  objective, `selection_reason_codes`, `collection_report` (queried/cached/excluded).
- `CandidateTable` `{ candidates; selectedOfferId }` — per candidate: offer id,
  adapter, feasible badge, `score_usd`, estimate columns
  (queue/provision/pull/start/cost as p50·p90·expected). Infeasible rows expand to
  `ViolationList`.
- `ViolationList` `{ violations }` — `code`, `path`, `required` vs `offered`,
  message. The "why was this offer rejected" view.
- `CreateRunForm` `{ mode: "image" | "spec" }` — image shorthand (image, args,
  `EnvEditor`, objective `Select`) or full workload JSON; auto Idempotency-Key; on
  success navigates to run detail and watches.
- `EnvEditor` `{ value; onChange }` — key/value rows, **literal values only; no
  `secret_ref` (enforced per ADR 0001)**, with a note pointing at workload-owned
  secrets.
- `RunActions` `{ run }` — Cancel (confirm dialog) / Refresh; disabled when terminal.

**Offers / Connections / Workloads / Sinks / Placements**
- `OffersTable` + `OfferDetailSheet` (full capability profile, network facts,
  reliability, `PriceTag`, `ResourceSummary`).
- `ConnectionsTable` (id / adapter_type / authorized).
- `WorkloadRevisionsTable` + `RevisionViewer` + `CreateWorkloadDialog` /
  `CreateRevisionDialog`.
- `SinkStatusCard` / `SinkActionsBar` / `ReplayDialog` (from_exclusive, limit,
  replay_id) / `SinkResultCard` (delivered, last_position, failed_event_id).
- `WorkloadSpecEditor` (shared by Create Run "spec" mode and Preview) +
  `PreviewResult` (renders `DecisionPanel` + `CandidateTable`, no run created).

## Routes (TanStack Router; deep-linkable via SPA fallback)

```
/                 → redirect /runs
/runs             RunsPage           (RunsTable; row → detail)
/runs/new         CreateRunPage      (CreateRunForm)
/runs/:runId      RunDetailPage      (RunPhaseTimeline + tabs: Overview | Events | Decision)
/preview          PreviewPage        (WorkloadSpecEditor + PreviewResult)
/workloads        WorkloadsPage      (enter/select workload id → revisions)
/workloads/:id    WorkloadDetailPage
/offers           OffersPage
/connections      ConnectionsPage
/sinks            SinksPage          (sink id → status + actions)
```

`workspace_id` is a validated search param on the root route (default from
`session.ts`), inherited by children.

## Cross-cutting behavior

- **Session:** workspace default + bearer token in `localStorage`; token injected
  into every request; 401 → friendly "set a token" `ErrorState`.
- **Live updates:** TanStack Query polling, intervals tuned by terminal state
  (active runs fast, closed runs stop). The `:wait` long-poll is a noted future
  enhancement for create-then-watch.
- **Errors:** `{code,message,details}` → queries render `ErrorState`; mutations
  toast + map `details[]` Violations onto form fields / `ViolationList`.
- **Disabled services (501):** Workloads/Sinks/Resolver degrade to
  `ServiceDisabled`.

## Go server change (minimal, targeted)

In `internal/httpapi/server.go` + `web/web.go`:
- Add `GET /assets/` → file server over embedded `static/assets`, immutable cache.
- `serveUI` becomes the SPA fallback: serve `index.html` (no-cache) for any
  unmatched non-API GET. Retire `/ui/`.

No handler/SDK/CLI changes; API stays at `/v1/*`.

## Build & dev workflow

- **Dev:** `bun dev` → Bun HTML dev server with HMR (`:3000`), proxying `/v1` and
  `/health` to a running `go run ./cmd/mercator` (`:8080`).
- **Ship:** `bun run build` (`build.ts` → `Bun.build`, hashing/splitting/minify) →
  `web/static/`; `go build ./cmd/mercator` embeds it. `make ui` / mise task wires
  both.

## Visual direction

Operator-console aesthetic: dark-first with light toggle, dense data tables,
monospace for ids/digests/positions/scores, the phase color system above, one
restrained accent (a cartographer's teal — nodding to Mercator/projection), Inter
for UI + a mono for code. No generic gradients or stock-AI look; clarity and
information density first. Final polish via the frontend-design skill during
implementation.

## Decisions made (defaults)

- Polling over `:wait` long-poll (revisit later).
- Dark-first theme; teal accent.
- TanStack Router (ecosystem-consistent with TanStack Query).
- `/ui/` retired in favor of `/assets/`.
- `workspace_id` in URL search param; token in `localStorage` only.

## Testing

- Component/unit tests with Bun's test runner + Testing Library for `format.ts`,
  `client.ts` (error envelope parsing, auth/workspace injection, Idempotency-Key),
  `RunStatusBadge`, `ViolationList`, and the CreateRun/Preview forms.
- A Go test asserting the SPA fallback (`GET /runs/anything` → `index.html`,
  `GET /v1/...` unaffected, `GET /assets/...` cache header).
- Manual operator walkthrough against the fake adapter (`MERCATOR_FAKE_OFFER=1`).
