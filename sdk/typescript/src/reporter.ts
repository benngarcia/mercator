/** Fetch function type for dependency injection in tests. */
type FetchFunction = (input: string | URL, init?: RequestInit) => Promise<Response>;

/** Options for {@link createReporter}. */
export type ReporterOptions = {
  /** Override the process environment (useful in tests). */
  env?: Record<string, string | undefined>;
  /** Override the fetch implementation (useful in tests). */
  fetch?: FetchFunction;
};

/**
 * Reporter posts structured events back to Mercator from inside a running workload.
 *
 * Obtain an instance via {@link createReporter}. If the required environment
 * variables are not present (i.e. the workload is running outside Mercator),
 * `createReporter` returns `null` and no network calls are made.
 */
export class Reporter {
  private readonly runId: string;
  private readonly workspaceId: string;
  private readonly reportUrl: string;
  private readonly token: string;
  private readonly fetchImpl: FetchFunction;

  /** @internal — use {@link createReporter} instead. */
  constructor(options: {
    runId: string;
    workspaceId: string;
    reportUrl: string;
    token: string;
    fetch: FetchFunction;
  }) {
    this.runId = options.runId;
    this.workspaceId = options.workspaceId;
    this.reportUrl = options.reportUrl.replace(/\/+$/, "");
    this.token = options.token;
    this.fetchImpl = options.fetch;
  }

  /**
   * Post a typed event to Mercator.
   *
   * @example
   * await reporter.report({ type: "progress", data: { pct: 50 } });
   */
  async report(event: { type: string; data?: unknown }): Promise<void> {
    const body: Record<string, unknown> = { type: event.type };
    if (event.data !== undefined) {
      body.data = event.data;
    }
    await this._post(body);
  }

  /**
   * Report that the workload is exiting with the given exit code.
   * Posts `{ type: "exit", exit_code: code }`.
   */
  async reportExit(code: number): Promise<void> {
    await this._post({ type: "exit", exit_code: code });
  }

  private async _post(body: Record<string, unknown>): Promise<void> {
    const url = `${this.reportUrl}/v1/runs/${encodeURIComponent(this.runId)}:report?workspace_id=${encodeURIComponent(this.workspaceId)}`;
    let response: Response;
    try {
      response = await this.fetchImpl(url, {
        method: "POST",
        headers: {
          "Authorization": `Bearer ${this.token}`,
          "Content-Type": "application/json",
          // Explicit User-Agent: some runtimes/proxies (e.g. Cloudflare's
          // managed rules) reject default agent strings with HTTP 403, which
          // would silently drop reports through a Cloudflare-fronted Mercator.
          "User-Agent": "mercator-reporter (typescript)",
        },
        body: JSON.stringify(body),
      });
    } catch (cause) {
      throw new Error(`Mercator reporter: request failed: ${String(cause)}`, { cause });
    }
    if (response.status !== 202) {
      let responseBody: string;
      try {
        responseBody = await response.text();
      } catch {
        responseBody = "(unreadable)";
      }
      throw new Error(
        `Mercator reporter: expected HTTP 202, got ${response.status}: ${responseBody}`,
      );
    }
  }
}

/**
 * Environment variables Mercator injects into workload containers. All four
 * are required for a working reporter — the server rejects reports without a
 * workspace_id (400 WORKSPACE_REQUIRED), so a partially populated environment
 * is a misconfiguration, not "running outside Mercator".
 */
const REQUIRED_ENV_VARS = [
  "MERCATOR_REPORT_URL",
  "MERCATOR_RUN_ID",
  "MERCATOR_WORKSPACE_ID",
  "MERCATOR_RUN_TOKEN",
] as const;

/**
 * Create a {@link Reporter} from environment variables injected by Mercator.
 *
 * Returns `null` (with a one-time `console.warn`) when none of the Mercator
 * variables (`MERCATOR_REPORT_URL`, `MERCATOR_RUN_ID`,
 * `MERCATOR_WORKSPACE_ID`, `MERCATOR_RUN_TOKEN`) are set, so workloads run
 * outside Mercator degrade gracefully. Throws when the environment is only
 * partially populated (some variables set, some missing/empty) — every report
 * from such a reporter would fail server-side, so fail fast at construction
 * instead.
 *
 * @example
 * const reporter = createReporter();
 * if (reporter) {
 *   await reporter.report({ type: "started" });
 * }
 */
export function createReporter(opts?: ReporterOptions): Reporter | null {
  const env = opts?.env ?? (typeof process !== "undefined" ? process.env : {});
  const missing = REQUIRED_ENV_VARS.filter((name) => !env[name]);

  if (missing.length === REQUIRED_ENV_VARS.length) {
    // Not running under Mercator.
    console.warn(
      "mercator-sdk: MERCATOR_REPORT_URL, MERCATOR_RUN_ID, MERCATOR_WORKSPACE_ID, and " +
      "MERCATOR_RUN_TOKEN are not set; reporter is disabled (no-op).",
    );
    return null;
  }
  if (missing.length > 0) {
    throw new Error(
      `mercator-sdk: reporter environment is incomplete; missing or empty: ${missing.join(", ")}`,
    );
  }

  const reportUrl = env["MERCATOR_REPORT_URL"]!;
  const runId = env["MERCATOR_RUN_ID"]!;
  const workspaceId = env["MERCATOR_WORKSPACE_ID"]!;
  const token = env["MERCATOR_RUN_TOKEN"]!;

  const fetchImpl =
    opts?.fetch ??
    (typeof globalThis !== "undefined" && typeof globalThis.fetch === "function"
      ? globalThis.fetch.bind(globalThis)
      : undefined);

  if (!fetchImpl) {
    throw new TypeError(
      "mercator-sdk Reporter requires a fetch implementation. " +
      "Pass one via createReporter({ fetch }) or upgrade to Node.js 18+.",
    );
  }

  return new Reporter({ runId, workspaceId, reportUrl, token, fetch: fetchImpl });
}
