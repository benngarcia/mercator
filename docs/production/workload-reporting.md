# Workload Reporting

Workload reporting lets a running container push progress and result events
back to Mercator without the operator token. Each container receives a bearer
token scoped to its own run, derived from the master key.

The token is minted once, when the attempt is dispatched, and carries no
expiry. A run holds the token it was given for its whole life, so rotating
`MERCATOR_SECRET_KEY` under a running workload ends its reporting for good: see
[Let The Runs Finish First](security-model.md#let-the-runs-finish-first).
Expiry and re-issue is
[#215](https://github.com/benngarcia/mercator/issues/215).

---

## Environment Variables

| Variable | Required | Description |
|---|---|---|
| `MERCATOR_SECRET_KEY` | Yes, always: `serve` refuses to start without it | Master key of at least 32 decoded bytes, hex- or base64-encoded. Used to derive the report-token signing key. Also the input for the HKDF-derived subkey that encrypts stored credentials (the raw key itself never encrypts anything). A present malformed or short value stops startup. |
| `MERCATOR_PUBLIC_URL` | Yes (for reporting) | The publicly reachable base URL of this Mercator instance (e.g. `https://mercator.example.com`). Injected into containers as the report endpoint base. Both this and `MERCATOR_SECRET_KEY` must be set for reporting to be enabled. |

Reporting is **disabled** unless `MERCATOR_PUBLIC_URL` is set. `cmd/mercator`
will not start without `MERCATOR_SECRET_KEY` at all, so the key half of that
condition is now enforced at startup; a runtime embedded some other way can
still be built without one, and answers `501 REPORTING_DISABLED` when it is.

---

## Key Derivation

The report-token signer never uses the raw master key. Mercator derives a
domain-separated subkey:

```
reportKey = HMAC-SHA256(masterKey, "mercator-report-token-v1")
```

`masterKey` is the decoded `MERCATOR_SECRET_KEY` value (hex then base64 tried
in order). The derived `reportKey` is what backs `reporting.Signer`.

---

## Per-Run Token

Each run receives a unique token minted by the server:

```
runToken = base64url-raw(HMAC-SHA256(reportKey, workspace_id || 0x00 || run_id))
```

The workspace is in the token, so the `workspace_id` a reporter sends must be
the one the token was minted for.

The token is injected into the container at launch as `MERCATOR_RUN_TOKEN`.
Three additional vars are also injected:

| Container Var | Value |
|---|---|
| `MERCATOR_RUN_ID` | The run's UUID (e.g. `run_019ef...`) |
| `MERCATOR_WORKSPACE_ID` | The run's workspace ID; required as the `workspace_id` query parameter on `/report` |
| `MERCATOR_REPORT_URL` | The base URL (`<MERCATOR_PUBLIC_URL>`); clients append `/v1/runs/<run_id>/report` |
| `MERCATOR_RUN_TOKEN` | The per-run HMAC token |

Note that `MERCATOR_REPORT_URL` is the **base URL only**. The orchestrator does
not inject the full `/report` path. Workloads build the full endpoint by appending
`/v1/runs/<MERCATOR_RUN_ID>/report?workspace_id=<MERCATOR_WORKSPACE_ID>`.

---

## The `/report` Endpoint

**`POST /v1/runs/{run_id}/report?workspace_id=<ws>`**

- **Auth**: `Authorization: Bearer <MERCATOR_RUN_TOKEN>`, the run-scoped
  token, NOT the operator token. The operator token is explicitly rejected.
- **Body**: progress uses `{"type":"progress","data":{...}}`; terminal
  workload exit uses `{"type":"exit","exit_code":0}` with the real process
  exit code at the top level.
- **Success**: `202 Accepted`, body `{"recorded": true}`. This confirms the fact
  is durable. Cleanup runs after the response through normal reconciliation.
- **Errors**:
  - `400 WORKSPACE_REQUIRED`: `workspace_id` query param missing.
  - `400 INVALID_REPORT`: an `exit` report omitted `exit_code`, or a
    nonterminal report included one.
  - `401 INVALID_RUN_TOKEN`: token wrong, missing, or for a different run.
  - `409 TERMINAL_REPORT_CONFLICT`: a different terminal report is already
    recorded for the run.
  - `501 REPORTING_DISABLED`: `MERCATOR_SECRET_KEY` or `MERCATOR_PUBLIC_URL`
    not set on the server.

The event is recorded in the run's event stream as `compute.run.reported.v1`
and is visible via `GET /v1/runs/{run_id}/events` (operator token). Exact
terminal-report replay returns 202 without recording a second event. The first
terminal fact in stream order determines the outcome when report, cancellation,
or provider observation arrives concurrently.

The background sweep advances open runs every minute. It records the terminal
outcome, requests the launch intent's cleanup disposition, and closes only after
cleanup confirmation. A provider error leaves the run open with
`cleanup: "blocked"`, exposes `cleanup_error` on the run, and records the public
`compute.run.cleanup_failed.v1` event. The next sweep or an explicit refresh
retries the same idempotent cleanup operation.

---

## Local / Dev with cloudflared

For local development, use [cloudflared](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/)
to expose your local Mercator over a public HTTPS URL:

```sh
# Install
brew install cloudflared

# Start a quick tunnel to your local Mercator
cloudflared tunnel --url http://localhost:8080
# Output: https://<random>.trycloudflare.com

# Restart Mercator with that URL
MERCATOR_PUBLIC_URL=https://<random>.trycloudflare.com \
  MERCATOR_SECRET_KEY=<hex-key> \
  ...
  go run ./cmd/mercator
```

POST `/report` works correctly through both quick tunnels and named cloudflare
tunnels (verified end-to-end against a named tunnel with a custom domain). For a
stable dev/prod tunnel, prefer a named cloudflare tunnel with a custom domain.

## Production

In production, Mercator just needs to be reachable at a public URL. Set:

```sh
MERCATOR_SECRET_KEY=<64-char hex, 32 decoded bytes>
MERCATOR_PUBLIC_URL=https://mercator.example.com
```

No tunnel needed. The injected container vars point directly at the
`MERCATOR_PUBLIC_URL`.

Containers launched by a run-pod provider (e.g. RunPod) will use
`MERCATOR_REPORT_URL` as the base URL — appending
`/v1/runs/<run_id>/report` — to POST progress and result events back during
execution.

---

## Token Security Notes

- The run token binds the workspace as well as the `run_id`, so a leaked token
  cannot be replayed against another workspace's view of the same run.
- The token has no expiry and no re-issue path. It is as long-lived as the run
  it was minted for, which is what makes a master-key rotation under an
  in-flight run destructive rather than a brief interruption.
- The signer is disabled (and the server returns `501`) when `MERCATOR_SECRET_KEY`
  is absent — no silent no-op.
- The operator token is explicitly rejected on the `/report` endpoint; only a
  valid run token is accepted.
