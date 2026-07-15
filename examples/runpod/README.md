# RunPod Examples

These examples are for provider validation after the Docker adapter path passes.
They require a RunPod API key, a Mercator server reachable by the pod, a real
registry-pullable image digest, and billable provider capacity.

Start with [docs/production/runpod.md](../../docs/production/runpod.md) for
connection setup, GPU offer behavior, cleanup, and image-resolution caveats.

## Examples

| Example | What It Proves |
| --- | --- |
| [busybox-report](busybox-report/README.md) | A minimal container can self-report progress and exit using injected `MERCATOR_*` env and ordinary HTTP. |

## Before Running

- Use a public URL for `MERCATOR_PUBLIC_URL`; a loopback or private laptop URL
  is not reachable from RunPod.
- Set `MERCATOR_SECRET_KEY` so Mercator can sign per-run reporting tokens.
- Pin image references to real registry digests. The server rejects mutable
  tags at create time, and RunPod can only pull digests that actually exist in
  a registry.
- Keep the GPU requirement in the workload resources so Docker is infeasible
  and the RunPod offer can win placement.
- Rotate the RunPod API key after public launch testing.
