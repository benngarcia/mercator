/**
 * Production build for the Mercator console.
 *
 * Bun.build over index.html → ../static (web/static). It emits a rewritten
 * index.html plus content-hashed, code-split, minified JS/CSS under
 * web/static/assets/. The Go binary embeds web/static via //go:embed and serves
 * /assets/* with an immutable cache header and index.html as the SPA fallback.
 *
 * Run order: this MUST run before `go build ./cmd/mercator` so the embed picks
 * up fresh assets. See README.md and the `ui` mise/make task.
 */
import { rm, mkdir, writeFile } from "node:fs/promises";
import { resolve } from "node:path";
import tailwind from "bun-plugin-tailwind";

const appDir = import.meta.dir;
const outdir = resolve(appDir, "../static");
const assetsDir = resolve(outdir, "assets");

// Clean prior build output (but keep the directory present for //go:embed).
await rm(outdir, { recursive: true, force: true });
await mkdir(assetsDir, { recursive: true });
// Keep the embed invariant: web.go uses `//go:embed all:static`, which requires
// at least one file even on a checkout that has not produced index.html yet.
await writeFile(resolve(outdir, ".gitkeep"), "");

const result = await Bun.build({
  entrypoints: [resolve(appDir, "index.html")],
  outdir,
  // Absolute asset URLs (/assets/...). Without this the emitted index.html
  // references assets relatively (./assets/...), which breaks on deep links like
  // /runs/{id}: the SPA-fallback index.html resolves ./assets against /runs/,
  // requests /runs/assets/... , gets index.html back, and fails the module MIME
  // check. publicPath "/" makes the references root-absolute so they resolve
  // correctly from any route depth.
  publicPath: "/",
  target: "browser",
  format: "esm",
  splitting: true,
  minify: true,
  // No sourcemap in the embedded production bundle: it would bake a multi-MB
  // .map into the Go binary and into git. The dev server (bun dev) keeps inline
  // maps for debugging.
  sourcemap: "none",
  naming: {
    entry: "[dir]/[name].[ext]",
    chunk: "assets/[name]-[hash].[ext]",
    asset: "assets/[name]-[hash].[ext]",
  },
  plugins: [tailwind],
  define: {
    "process.env.NODE_ENV": JSON.stringify("production"),
  },
});

if (!result.success) {
  for (const log of result.logs) {
    console.error(log);
  }
  throw new AggregateError(result.logs, "Bun build failed");
}

const sizes = result.outputs.map(
  (o) => `${o.path.replace(outdir, "static")} (${(o.size / 1024).toFixed(1)} KB)`,
);
console.log(`Built ${result.outputs.length} artifacts into ${outdir}:`);
for (const line of sizes) console.log(`  ${line}`);
