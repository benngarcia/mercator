# Threat Model

This document is a maintainer-facing threat model for the current V1 evaluation
slice. It complements `docs/production/security-model.md`, which describes the
implemented boundary. This file focuses on assets, attackers, abuse cases,
mitigations, and launch gates.

Mercator is not production GA. Treat this as a living review artifact for
deciding what can be launched openly and what must remain a known limitation.

## Scope

In scope:

- Single-process `cmd/mercator` server.
- SQLite event log and read models.
- `/v1/*` HTTP API, CLI, SDKs, and embedded console.
- The Docker host adapter path, plus RunPod-oriented adapters added via
  connections.
- Workload metadata, run lifecycle events, placement decisions, and cleanup
  intents.
- Adapter credentials, per-run reporting tokens, and public event visibility.

Out of scope for the current implementation:

- Multi-user SaaS tenancy.
- Internet-facing TLS termination.
- Provider account security outside Mercator configuration.
- Workload-owned secret stores and runtime secret delivery.
- Host/container breakout prevention. Docker and provider runtimes are trusted
  execution environments from Mercator's point of view.

## Assets

| Asset | Why It Matters | Current Owner |
| --- | --- | --- |
| Operator bearer token | Authorizes API calls and console actions. | Operator configuration |
| Workspace IDs | Scope run, offer, connection, and event reads. | Mercator API/authz |
| SQLite event log | Durable source of truth for commands, intents, events, and cursor state. | Mercator process |
| Workload env values | May contain sensitive literals if callers misuse env. | Caller/workload owner |
| Adapter credentials | Can create, observe, and delete provider resources. | Operator configuration / credential store |
| Per-run reporting token | Lets a workload report progress and exit state for one run. | Mercator reporting signer |
| Cleanup disposition | Controls release versus terminate side effects. | Mercator orchestrator |
| Public event stream | User-facing audit trail; must not leak private command data. | Mercator event projection |

## Trust Boundaries

| Boundary | Trusted Side | Less-Trusted Side | Notes |
| --- | --- | --- | --- |
| HTTP API | Mercator process | Any client with network access | Bearer token required for `/v1/*`; health/OpenAPI/UI shell are public on the bind address. |
| Workspace authorization | Allowed workspace list | Caller-supplied workspace IDs | Reads and writes must remain workspace-scoped. |
| Public events | Redacted/public event data | Private command payloads and workload env values | Public API and sinks must not expose private events. |
| Adapter calls | Mercator adapter contract | Provider APIs and local Docker daemon | Adapters are trusted code but provider responses are external facts. |
| Reporting endpoint | One run's reporter token | Workload process | Report token must not authorize operator API access or other runs. |
| SQLite file | Local operator host | Other local users/processes | File permissions and backup handling are operator responsibilities. |

## Threat Actors

- External network caller without a valid bearer token.
- Authenticated caller attempting cross-workspace access.
- Buggy or malicious workload process with access to injected reporting env.
- Operator or integration accidentally passing secrets as literal env values.
- Provider/API returning stale or dishonest offer/capacity facts.
- Local process with filesystem access to SQLite or environment variables.
- Contributor changing lifecycle, auth, event visibility, or cleanup behavior.

## Abuse Cases And Current Mitigations

### API Access Without Authorization

Risk: a caller creates runs, reads run data, or triggers cleanup without the
operator token.

Mitigations:

- `/v1/*` routes use bearer-token auth.
- Workspace allow-list checks gate workspace-scoped API behavior.
- Security tests cover invalid token and disallowed workspace responses.

Remaining work:

- No per-user auth or token rotation workflow.
- No built-in TLS; remote deployments require a trusted reverse proxy.

### Cross-Workspace Data Access

Risk: an authenticated caller reads or acts on another workspace's runs,
connections, offers, or events.

Mitigations:

- API reads require or derive explicit `workspace_id`.
- SDK and CLI clients carry workspace defaults centrally.
- Query keys in the console are workspace-scoped.

Remaining work:

- Workspace membership is attached to the bearer principal, not users/groups.
- Connection bootstrap behavior for wildcard workspaces remains narrow.

### Secret Or Env Leakage In Public Events

Risk: workload env values, credential identifiers, or report tokens leak through
events, sinks, logs, or UI surfaces.

Mitigations:

- Public event APIs skip private events.
- Public run-request data exposes env binding kinds rather than literal values.
- Sink delivery skips private events.
- `secret_ref` workload env bindings are rejected in the current boundary.

Remaining work:

- Operators can still misuse literal env values when launching workloads.
- Workload-owned secret management is outside Mercator and needs deployment
  docs once a supported integration exists.

### Duplicate Launch Or Unsafe Retry

Risk: retries produce duplicate provider workloads or conflicting run state.

Mitigations:

- Command idempotency uses request hashes and returns
  `IDEMPOTENCY_CONFLICT` on conflicting reuse.
- SDK happy paths generate run IDs and derive stable idempotency keys.
- Launch and cleanup side effects are preceded by durable intent events.
- Fake adapter tests cover idempotency conflict behavior.

Remaining work:

- Provider-specific idempotency guarantees must be reviewed per new adapter.
- External providers may still accept duplicate side effects if an adapter maps
  ownership tokens incorrectly.

### Wrong Cleanup Action

Risk: Mercator terminates a resource it only borrowed, or leaves owned compute
running unexpectedly.

Mitigations:

- Launch intent records cleanup disposition at launch time.
- Cleanup dispatch uses the recorded disposition, not a later offer
  re-inference.
- Docker adapter applies deterministic ownership labels.

Remaining work:

- No automated no-orphan drill runs in public CI.
- Production RunPod cleanup verification should be exercised with real
  credentials before GA claims.

### Reporting Token Misuse

Risk: a workload reports events for another run, changes operator state, or
keeps reporting after it should not.

Mitigations:

- Reporting tokens are per-run and signed separately from the operator bearer
  token.
- Reporting endpoint accepts workload reports, not arbitrary operator actions.
- SDK reporter returns `nil` outside a Mercator-injected environment.

Remaining work:

- Token rotation and revocation are not documented.
- Public threat review should inspect report-token expiry and replay behavior
  before production GA.

### Malicious Or Dishonest Provider Facts

Risk: stale or dishonest offer facts cause bad placement, cost surprises, or
failed launches.

Mitigations:

- Scheduler records selected and rejected candidates for audit.
- Capability, platform, resource, accelerator, price, and policy checks are
  deterministic.
- Offer facts carry timestamps and evidence fields.

Remaining work:

- Provider trust and refresh cadence need production docs per adapter.
- External billing/cost verification is not automated.

## Launch Gates

Before calling the repository A+ for public launch:

- PR CI must be green on the launch-prep branch.
- The launch-prep PR must be merged or otherwise promoted to the default branch.
- The repository visibility decision must be explicit.
- The first public CI run must be green after visibility changes.
- A tagged release must produce downloadable archives and checksums.
- The release notes must include known limitations and pre-GA scope.
- At least one external review, integration note, or user story must exist
  outside maintainer-only docs.
- Any public proof point must follow `docs/launch/proof-point-template.md` and
  exclude secrets, private hostnames, customer data, and unpublished downstream
  details.

Before calling Mercator production GA:

- TLS/reverse-proxy deployment pattern is documented and tested.
- Token rotation, key rotation, and backup/restore drills are documented.
- Registry credential and digest-resolution flows are implemented or explicitly
  deferred.
- External sink configuration and failure handling are production-configurable.
- Adapter cleanup/no-orphan drills run against at least one real provider.
