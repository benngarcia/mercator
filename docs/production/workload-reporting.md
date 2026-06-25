# Workload Reporting

Workload reporting lets a running container push progress and result events
back to Mercator without the operator token. Each container receives a
short-lived, run-scoped bearer token derived from the master key.

---

## Environment Variables

| Variable | Required | Description |
|---|---|---|
| `MERCATOR_SECRET_KEY` | Yes (for reporting) | Master key, hex- or base64-encoded. Used to derive the report-token signing key. Also used for encrypting stored credentials. |
| `MERCATOR_PUBLIC_URL` | Yes (for reporting) | The publicly reachable base URL of this Mercator instance (e.g. `https://mercator.example.com`). Injected into containers as the report endpoint base. Both this and `MERCATOR_SECRET_KEY` must be set for reporting to be enabled. |

Reporting is **disabled** unless both `MERCATOR_SECRET_KEY` and
`MERCATOR_PUBLIC_URL` are set.

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
runToken = base64url-raw(HMAC-SHA256(reportKey, run_id))
```

The token is injected into the container at launch as `MERCATOR_RUN_TOKEN`.
Two additional vars are also injected:

| Container Var | Value |
|---|---|
| `MERCATOR_RUN_ID` | The run's UUID (e.g. `run_019ef...`) |
| `MERCATOR_REPORT_URL` | `<MERCATOR_PUBLIC_URL>/v1/runs/<run_id>:report` |
| `MERCATOR_RUN_TOKEN` | The per-run HMAC token |

---

## The `:report` Endpoint

**`POST /v1/runs/{run_id}:report?workspace_id=<ws>`**

- **Auth**: `Authorization: Bearer <MERCATOR_RUN_TOKEN>` — the run-scoped
  token, NOT the operator token. The operator token is explicitly rejected.
- **Body**: arbitrary JSON object (`{"type": "...", "data": {...}}`).
- **Success**: `202 Accepted`, body `{"recorded": true}`.
- **Errors**:
  - `400 WORKSPACE_REQUIRED` — `workspace_id` query param missing.
  - `401 INVALID_RUN_TOKEN` — token wrong, missing, or for a different run.
  - `501 REPORTING_DISABLED` — `MERCATOR_SECRET_KEY` or `MERCATOR_PUBLIC_URL`
    not set on the server.

The event is recorded in the run's event stream as `compute.run.reported.v1`
and is visible via `GET /v1/runs/{run_id}/events` (operator token).

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

POST `:report` works correctly through both quick tunnels and named cloudflare
tunnels (verified end-to-end against a named tunnel with a custom domain). For a
stable dev/prod tunnel, prefer a named cloudflare tunnel with a custom domain.

**Shell gotcha (zsh):** when testing `:report` by hand, always brace the run-id
variable — `"${RUN_ID}:report"`, not `"$RUN_ID:report"`. In zsh the unbraced
form applies the `:r` history modifier to `$RUN_ID` and silently eats the `:r`
of `:report`, producing `…<id>eport` and a spurious operator-`401`. The SDK
reporters build the URL in TypeScript/Python and are unaffected.

---

## Production

In production, Mercator just needs to be reachable at a public URL. Set:

```sh
MERCATOR_SECRET_KEY=<64-char hex, 32 decoded bytes>
MERCATOR_PUBLIC_URL=https://mercator.example.com
```

No tunnel needed. The injected container vars point directly at the
`MERCATOR_PUBLIC_URL`.

Containers launched by a run-pod provider (e.g. RunPod) will use
`MERCATOR_REPORT_URL` to POST progress and result events back during execution.

---

## Token Security Notes

- The run token is bound to the `run_id` only (not workspace). This is safe
  under the current single-operator model where run IDs are globally unique
  (UUIDv7). A future multi-tenant deployment should bind workspace into the
  token as well.
- The signer is disabled (and the server returns `501`) when `MERCATOR_SECRET_KEY`
  is absent — no silent no-op.
- The operator token is explicitly rejected on the `:report` endpoint; only a
  valid run token is accepted.
