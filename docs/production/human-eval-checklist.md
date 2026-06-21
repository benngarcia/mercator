# Human Evaluation Checklist

Use this checklist before calling the V1 branch production-ready for a specific
environment.

## Build And Startup

- [ ] `go test ./...` passes.
- [ ] `go build ./...` passes.
- [ ] Server starts with explicit `MERCATOR_API_TOKEN`.
- [ ] Server starts with explicit `MERCATOR_SECRET_KEY_HEX`.
- [ ] `/health/live`, `/health/ready`, and `/openapi.json` return expected JSON.
- [ ] UI loads at `/` on the intended bind address.

## Auth And Workspace Isolation

- [ ] Valid bearer token can access an allowed workspace.
- [ ] Invalid bearer token returns `UNAUTHORIZED`.
- [ ] Disallowed workspace returns `FORBIDDEN`.
- [ ] All run reads use explicit `workspace_id`.

## Fake Adapter

- [ ] `MERCATOR_FAKE_OFFER=1` exposes `offer_local_fake`.
- [ ] A fake run reaches `closed: true`.
- [ ] Cleanup is `confirmed` before closure.
- [ ] Replaying the same idempotency key with identical payload is safe.
- [ ] Reusing the same idempotency key with a different payload returns
  `IDEMPOTENCY_CONFLICT`.

## Docker Adapter

- [ ] `docker version` passes on the host.
- [ ] `MERCATOR_DOCKER_INTEGRATION=1 go test ./internal/adapter/docker -run Integration -count=1` passes.
- [ ] Docker run uses a real digest-pinned image for the host architecture.
- [ ] Docker container has Mercator ownership labels while running.
- [ ] No owned Docker container remains after cleanup confirmation.

## Events And Audit

- [ ] Public run events show placement, launch intent, launch/observation,
  cleanup, and closure.
- [ ] Placement decision explains selected and rejected candidates.
- [ ] Public events do not expose secret plaintext.
- [ ] Sink `audit` status and replay work.
- [ ] Backup and restore drill recovers run list from a copied SQLite database.

## Production-Hardening Review

- [ ] Key management risk is accepted or replaced with environment-specific KMS.
- [ ] Registry digest resolution and credential handling are addressed.
- [ ] External sink wiring requirements are decided.
- [ ] TLS/reverse-proxy boundary is documented.
- [ ] Operational ownership for SQLite backup, restore, and migration is clear.
- [ ] Known limitations have explicit owners before GA.
