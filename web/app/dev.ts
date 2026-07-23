/**
 * Development server for the Mercator console.
 *
 * Bun's HTML import gives us bundling + HMR for the React app. A fetch fallback
 * proxies API and auth traffic (/v1/*, /auth/*, /health/*, /openapi.json) to a locally running
 * `go run ./cmd/mercator` on :8080, so the SPA talks same-origin in dev exactly
 * as it will once embedded. Everything else is served by the HTML route.
 *
 *   bun dev   # serves :3000, proxies API to 127.0.0.1:8080
 */
import index from "./index.html";

const API_TARGET = process.env.MERCATOR_API ?? "http://127.0.0.1:8080";
const PORT = Number(process.env.PORT ?? 3000);

async function proxyAPI(
  req: Request,
  server: Bun.Server<unknown>,
): Promise<Response> {
  const url = new URL(req.url);
  const target = new URL(url.pathname + url.search, API_TARGET);
  if (url.pathname === "/v1/console/events") {
    server.timeout(req, 0);
  }
  try {
    const body = await proxyBody(req);
    const response = await fetch(target, {
      method: req.method,
      headers: proxyHeaders(req),
      body,
      signal: req.signal,
      // @ts-expect-error duplex required by Bun/undici when streaming a body
      duplex: "half",
      redirect: "manual",
    });
    return proxyResponse(response);
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

function proxyResponse(response: Response): Response {
  const headers = proxySafeHeaders(response.headers);
  return new Response(response.body, {
    status: response.status,
    statusText: response.statusText,
    headers,
  });
}

function proxySafeHeaders(source: Headers): Headers {
  const headers = new Headers(source);
  headers.delete("connection");
  headers.delete("keep-alive");
  headers.delete("proxy-authenticate");
  headers.delete("proxy-authorization");
  headers.delete("te");
  headers.delete("trailer");
  headers.delete("transfer-encoding");
  headers.delete("upgrade");
  return headers;
}

function proxyHeaders(req: Request): Headers {
  const headers = proxySafeHeaders(req.headers);
  headers.delete("host");
  return headers;
}

async function proxyBody(req: Request): Promise<BodyInit | undefined> {
  if (req.method === "GET" || req.method === "HEAD") return undefined;
  if (req.headers.get("content-type")?.includes("application/json")) {
    return await req.arrayBuffer();
  }
  return req.body ?? undefined;
}

const server = Bun.serve({
  port: PORT,
  development: { hmr: true, console: true },
  routes: {
    "/v1/*": proxyAPI,
    "/auth/*": proxyAPI,
    "/health/*": proxyAPI,
    "/openapi.json": proxyAPI,
    "/*": index,
  },
});

console.log(`Mercator console dev server: ${server.url}`);
console.log(`Proxying /v1, /auth, /health, /openapi.json → ${API_TARGET}`);
