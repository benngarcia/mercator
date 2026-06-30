# RunPod Examples

These examples are for provider validation after the fake-adapter path passes.
They require a RunPod API key, a Mercator server reachable by the pod, a real
registry-pullable image digest, and billable provider capacity.

Start with [docs/production/runpod.md](../../docs/production/runpod.md) for
connection setup, GPU offer behavior, cleanup, and image-resolution caveats.

SDK registry packages are not published for the first launch. The Python SDK
example therefore installs from the public Mercator source repository using a
Git ref. After `v0.1.0` exists, use that tag. Before the tag exists, replace
`v0.1.0` with the public commit or branch you are evaluating.

## Examples

| Example | What It Proves | Package Assumption |
| --- | --- | --- |
| [busybox-report](busybox-report/README.md) | A minimal container can self-report progress and exit using injected `MERCATOR_*` env and raw HTTP. | no SDK package |
| [python-sdk](python-sdk/README.md) | A Python workload can emit custom events and report exit through the SDK reporter helper. | SDK source install from Git |

## Before Running

- Use a public URL for `MERCATOR_PUBLIC_URL`; a loopback or private laptop URL
  is not reachable from RunPod.
- Set `MERCATOR_SECRET_KEY` so Mercator can sign per-run reporting tokens.
- Pin image references to real registry digests when using the current dev
  resolver. Synthetic fake-mode digests are not pullable by RunPod.
- Keep the GPU requirement in the workload resources so Docker is infeasible
  and the RunPod offer can win placement.
- Rotate the RunPod API key after public launch testing.
