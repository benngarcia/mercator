# Mercator Console (frontend)

The operator console for Mercator. React 19 + TypeScript (strict), Effect v4,
shadcn/ui (new-york), Tailwind CSS v4, TanStack Router, sonner. Built by
**Bun**; the output is embedded in the Go binary and served same-origin — there
is no second server in production.

## Layout

```
web/app/            this dir — frontend source
  index.html        entry: #root + <script src="src/main.tsx">
  src/index.css     Tailwind v4 + the canonical shadcn theme tokens (light/dark, teal accent)
  src/lib/utils.ts  cn() helper
  src/lib/workspace/ Workspace event feed, pure reducer, and scenario playback (the /canvas model)
  src/components/canvas/ WorkspaceCanvas time-lane board rendered at /canvas
  src/routes/canvas.tsx  the /canvas route (console home)
  build.ts          Bun.build → ../static (hashed, minified, no sourcemaps)
  dev.ts            Bun HTML dev server (HMR) + /v1,/auth,/health proxy to :8080
web/static/         GENERATED BUILD OUTPUT (git-ignored, embedded via //go:embed all:static)
```

The import alias `@/*` maps to `web/app/src/*` (see `tsconfig.json`).

## Develop

The Go API must be running first (it owns `/v1/*`, `/auth/*`, `/health/*`, `/openapi.json`):

```sh
go run ./cmd/mercator serve --dev # loopback-only local session on 127.0.0.1:8080
cd web/app && bun install      # once
bun dev                        # serves :3000 with HMR, proxies API → 127.0.0.1:8080
```

Override the API target with `MERCATOR_API` and the port with `PORT`.

Auth has three modes, discovered at runtime from `GET /auth/session`:

- **Local development (`serve --dev`)**: the server establishes a signed,
  HTTP-only session for `developer@localhost`; the console never asks for a
  token, while CLI and automation can still use the generated operator token.

- **OIDC configured on the server**: humans sign in through `/auth/login`
  (the server redirects unauthenticated page loads there); the topbar shows
  the signed-in email and a sign-out action, and requests authenticate with
  the HTTP-only session cookie. No token pasting.
- **Token-only server**: the topbar shows the TokenField
  fallback; the bearer token lives in `localStorage`.

The workspace id is a URL search param defaulting from `localStorage`. Token
and workspace are injected centrally by `@/lib/api/client`; components do not
call `fetch()` directly. The Effect API service also owns cookie-based `/auth`
calls, which use no envelope, token, or workspace.

## Build & embed (the order matters)

`go build` embeds whatever is in `web/static` at compile time, so shipping paths
must build the UI **first**. `mise run build`, the Dockerfile, and the release
archive builder all enforce that order:

```sh
mise run ui                    # = (cd web/app && bun install && bun run build) → web/static
go build ./cmd/mercator        # embeds the fresh assets

# or, in one step:
mise run build                 # ui, then go build ./cmd/mercator
```

`bun run build` cleans `web/static`, then emits a rewritten `index.html` plus
content-hashed `assets/*.js` / `assets/*.css`. The Go server serves:

- `GET /assets/*` → embedded hashed assets, `Cache-Control: public, max-age=31536000, immutable`.
- `GET /` and any unmatched **non-API** GET → `index.html` (`Cache-Control: no-cache`) — the SPA fallback for client-side routing.
- `/v1/*`, `/health/*`, `/openapi.json` are registered more specifically and win under the Go 1.22+ ServeMux precedence rules.

A `.gitkeep` placeholder keeps `web/static` non-empty so `//go:embed all:static`
always has a file to embed even on a fresh checkout that has not run the build
(in that state the server returns a `UI_NOT_AVAILABLE` hint until `mise run ui`).
Generated `index.html` and `assets/` remain ignored; release artifacts never
depend on a committed bundle.

> Makefile equivalent (if a Makefile is added): `ui: ; cd web/app && bun install && bun run build`.

## Typecheck

```sh
cd web/app && bun run typecheck   # tsc --noEmit, strict
```

## Canvas (console home)

`/` redirects to `/canvas`, the live Workspace dashboard. A single authenticated
composite SSE feed (`GET /v1/console/events`) drives one pure reducer whose root
is the `Workspace`; the board renders intake, Rental schedules, marketplace
Offers, and provisioning as Run cards move between time lanes.

Outside production, the same reducer replays a deterministic scenario fixture
instead of live traffic: `/canvas?scenario=<name>` selects a fixture and `&play=1`
autoplays it. These params are ignored when `NODE_ENV=production`.

## Runs

`/runs` owns run intake as well as run history. Its **Create run** action adds
`action=create` to the current URL and opens an accessible sheet without
leaving the Runs context. Successful creation replaces that transient history
entry with `/runs/{run_id}`; cancelling returns to the exact originating URL
and restores focus to the button that opened the sheet.

The sheet accepts either the image shorthand or a complete immutable workload
revision. The former Workloads, Preview, and `/runs/new` authoring pages are
retired. Their public API endpoints remain available for automation.

## Connections page

`/connections` is the guided provider onboarding surface:

- A card per registered adapter, rendered from `GET /v1/adapters` (the
  manifest API). Each card shows a bundled logomark (or typographic monogram —
  see `src/components/connections/LOGO-LICENSES.md`; the CSP forbids external
  images), a one-liner, and a quiet status: none / configured / verified /
  verify failed. "Verify failed" is session state — the API stores only the
  authorized bit.
- Selecting a card with no connection opens the setup modal: the manifest's
  numbered setup steps (with links) beside the form. "Save & verify" creates
  the connection then calls `/authorize`; failure keeps the modal open with
  the adapter's real error text and offers verify-again or discard-and-edit
  (connection configs are immutable, so editing recreates under a fresh id).
- Secret fields are masked and write-only: after save the UI shows presence
  ("configured"), never the value; editing means re-entering.
- Selecting a card with existing connections opens the management sheet:
  config with manifest-declared secret fields redacted, re-verify, and delete
  with inline confirmation (`DELETE /v1/connections/{id}`; deleted ids cannot
  be reused).
- The dashed "Custom connection" card keeps the raw adapter-type + key/value
  dialog for adapters without manifests, and the dense operator table below
  retains the full-record view.
