/**
 * Development server for the Mercator console.
 *
 * Bun's HTML import gives us bundling + HMR for the React app. A fetch fallback
 * proxies API traffic (/v1/*, /health/*, /openapi.json) to a locally running
 * `go run ./cmd/mercator` on :8080, so the SPA talks same-origin in dev exactly
 * as it will once embedded. Everything else is served by the HTML route.
 *
 *   bun dev   # serves :3000, proxies API to 127.0.0.1:8080
 */
import index from "./index.html";

const API_TARGET = process.env.MERCATOR_API ?? "http://127.0.0.1:8080";
const PORT = Number(process.env.PORT ?? 3000);

function isApiPath(pathname: string): boolean {
  return (
    pathname.startsWith("/v1/") ||
    pathname.startsWith("/health") ||
    pathname === "/openapi.json"
  );
}

const server = Bun.serve({
  port: PORT,
  development: { hmr: true, console: true },
  routes: {
    "/*": index,
  },
  async fetch(req) {
    const url = new URL(req.url);
    if (isApiPath(url.pathname)) {
      const target = new URL(url.pathname + url.search, API_TARGET);
      try {
        return await fetch(target, {
          method: req.method,
          headers: req.headers,
          body: req.body,
          // @ts-expect-error duplex required by Bun/undici when streaming a body
          duplex: "half",
          redirect: "manual",
        });
      } catch (err) {
        return new Response(
          JSON.stringify({
            code: "API_PROXY_UNREACHABLE",
            message: `Could not reach API at ${API_TARGET}. Is the Go server running on :8080? (${String(err)})`,
          }),
          { status: 502, headers: { "content-type": "application/json" } },
        );
      }
    }
    // Non-API: let the HTML route handle it (returns null → 404 here is fine
    // because routes["/*"] catches all GET navigations first).
    return new Response("Not found", { status: 404 });
  },
});

console.log(`Mercator console dev server: ${server.url}`);
console.log(`Proxying /v1, /health, /openapi.json → ${API_TARGET}`);
