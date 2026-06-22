# ADR 0001: Use Env Configuration Instead Of A Secret Vault

Status: Accepted

Date: 2026-06-22

## Context

Mercator runs workloads in external runtimes such as Docker. Those workloads are
already responsible for their own file I/O and commonly need their own provider
credentials, for example S3 credentials or configuration for a secret-management
backend.

Adding a Mercator-owned secret vault, secret versions, and run-scoped grants
moves secret-management behavior into the broker. That creates extra API,
storage, key-management, rotation, adapter-materialization, and audit surfaces
that are not necessary for Mercator's core job: broker a run onto capacity and
record the lifecycle.

## Decision

Mercator uses environment bindings as the configuration API.

- A workload JSON/YAML specification may store base env on the `main`
  container.
- `create_run` may pass top-level `env` bindings.
- Run-level env overrides matching workload env keys and adds new keys for that
  run only.
- Env bindings are literal values only: `{"value": "..."}`.
- Mercator rejects `secret_ref` env bindings.
- Mercator does not expose secret vault, version, grant, KMS, or secret-backend
  adapter APIs.

Workloads or runtimes that need secret material should use env values as
ordinary configuration for their own secret-management system, or receive
runtime environment from the platform outside Mercator.

## Consequences

Mercator has a smaller security and product surface. It no longer stores
encrypted secret events, owns secret grants, or needs a process secret key.

Public run events still redact literal env values. The broker can safely record
that an env key exists without exposing the value.

SDKs expose env configuration and omit secret/grant helpers. Production docs
describe the no-secret-vault boundary and point operators to workload-owned
secret management.
