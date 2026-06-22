# Environment Configuration

Mercator does not own a secret vault. Workloads receive literal environment
bindings, and the workload or runtime is responsible for using those values to
reach its own backing services such as S3, `op`, Infisical, AWS KMS, or another
secret-management system.

## Workload Spec Env

Store stable defaults in the workload JSON/YAML specification:

```json
{
  "spec": {
    "containers": [{
      "name": "main",
      "image": "ghcr.io/acme/worker:latest",
      "platform": {"os": "linux", "architecture": "amd64"},
      "env": {
        "LOG_LEVEL": {"value": "info"},
        "MODEL_BUCKET": {"value": "s3://acme-models/prod"}
      }
    }]
  }
}
```

Public run events redact env values and expose only binding metadata.

## Create-Run Overrides

`POST /v1/runs` accepts top-level `env`. For a stored or explicit workload, this
map overrides matching workload env keys and adds new keys for that run only:

```json
{
  "workspace_id": "ws_eval",
  "run_id": "run_eval_1",
  "workload_id": "wrk_eval",
  "workload_revision_id": "wrev_1",
  "env": {
    "LOG_LEVEL": {"value": "debug"},
    "INPUT_URI": {"value": "s3://acme-inputs/job-123.json"}
  }
}
```

For image shorthand, the same top-level `env` becomes the synthesized `main`
container env:

```json
{
  "image": "busybox",
  "args": ["env"],
  "env": {
    "LOG_LEVEL": {"value": "debug"}
  }
}
```

Mercator rejects `secret_ref` env bindings. If a workload needs secret material,
pass non-secret configuration that lets the runtime obtain it itself, or inject
the relevant environment outside Mercator before the container starts.
