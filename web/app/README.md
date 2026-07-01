# Mercator Console (frontend)

The operator console for Mercator. React 19 + TypeScript (strict), shadcn/ui
(new-york), Tailwind CSS v4, TanStack Query + TanStack Router, sonner. Built by
**Bun**; the output is embedded in the Go binary and served same-origin — there
is no second server in production.

## Layout

```
web/app/            this dir — frontend source
  index.html        entry: #root + <script src="src/main.tsx">
  src/index.css     Tailwind v4 + the canonical shadcn theme tokens (light/dark, teal accent)
  src/lib/utils.ts  cn() helper
  build.ts          Bun.build → ../static (hashed, split, minified, no sourcemaps — they would be embedded in the Go binary)
  dev.ts            Bun HTML dev server (HMR) + /v1,/health proxy to :8080
web/static/         BUILD OUTPUT (git-tracked, embedded via //go:embed all:static)
```

The import alias `@/*` maps to `web/app/src/*` (see `tsconfig.json`).

## Develop

The Go API must be running first (it owns `/v1/*`, `/health/*`, `/openapi.json`):

```sh
go run ./cmd/mercator serve    # serves 127.0.0.1:8080 (set MERCATOR_SQLITE_DSN; requires a running Docker daemon)
cd web/app && bun install      # once
bun dev                        # serves :3000 with HMR, proxies API → 127.0.0.1:8080
```

Override the API target with `MERCATOR_API` and the port with `PORT`.

The token (bearer) lives in `localStorage`; the workspace id is a URL search
param defaulting from `localStorage`. Both are injected centrally by
`@/lib/api/client` — never call `fetch()` directly from a component.

## Build & embed (the order matters)

`go build` embeds whatever is in `web/static` at compile time, so the UI build
must run **first**:

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

> Makefile equivalent (if a Makefile is added): `ui: ; cd web/app && bun install && bun run build`.

## Typecheck

```sh
cd web/app && bun run typecheck   # tsc --noEmit, strict
```
