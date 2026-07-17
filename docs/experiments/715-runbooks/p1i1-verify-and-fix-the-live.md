# Runbook: verify and fix the live 20-vs-16 GiB docker-offer disk mismatch

Item p1i1 of bucket-rails issue #715 (Track A). The code fix is in this PR:
the Docker adapter now probes real free disk on the connection host (one-shot
`busybox df` probe container, 60 s TTL cache) instead of advertising a
hardcoded 16 GiB `EphemeralDiskBytes`. This runbook covers the live actions an
operator must run on staging; the implementing agent is fenced to repo changes
only.

## Background

Since bucket-rails `e143ff1b3` (2026-07-13), every `model_training` dispatch
requests at least 20 GiB of ephemeral disk
(`Model::Training::Run::MINIMUM_REMOTE_DISK_GB = 20`, sent to Mercator as
`spec.resources.ephemeral_disk.min_bytes` via `Compute::Resources#as_mercator`).
Every Docker offer advertised exactly `16 GiB`
(`internal/adapter/docker/offer.go`, previously hardcoded), so the scheduler
rejects placement with `RESOURCE_INSUFFICIENT resources.ephemeral_disk` and the
orchestrator surfaces `orchestrator: no feasible offers`
(`internal/orchestrator/orchestrator.go` `decide()`).

## 1. Pre-fix canary (confirm the failure on staging)

1. From a bucket-rails checkout, launch a canary `model_training` dispatch on
   staging through the normal control-plane path (a small workflow run whose
   training stage requests default disk works; any dispatch since 2026-07-13
   with `runtime_params["disk_gb"] >= 20` reproduces).
2. Confirm the run never places:
   - Rails: the `Compute::Dispatch` stays without a running Mercator attempt.
   - Mercator accessory logs on the staging job host:

     ```bash
     ssh root@<staging-job-host> docker logs --since 1h $(ssh root@<staging-job-host> docker ps -qf name=mercator) 2>&1 | grep "no feasible offers"
     ```

## 2. Forensics (mercator.db sqlite)

The event-sourced log lives on the staging job host at
`/mnt/mercator-data/mercator.db` (mounted into the accessory at
`/data/mercator.db`).

```bash
ssh root@<staging-job-host>
sqlite3 /mnt/mercator-data/mercator.db "
  -- Runs that were requested but never received a placement decision:
  SELECT stream_id, occurred_at FROM events
  WHERE event_type = 'compute.run.requested.v1'
    AND stream_id NOT IN (
      SELECT stream_id FROM events
      WHERE event_type = 'compute.run.placement_decided.v1')
  ORDER BY occurred_at DESC LIMIT 20;"

# Inspect the requested resources of one stuck run (expect
# ephemeral_disk.min_bytes >= 21474836480, i.e. 20 GiB):
sqlite3 /mnt/mercator-data/mercator.db "
  SELECT data_json FROM events
  WHERE event_type = 'compute.run.requested.v1' AND stream_id = '<run_id>';" \
  | python3 -m json.tool | grep -A3 ephemeral_disk
```

Record the canary run id and the query output in the ExecPlan.

## 3. Deploy the fix to staging

1. Merge this PR, cut a mercator release image, and note its digest:

   ```bash
   # in the mercator repo
   docker buildx build --platform linux/amd64 -t ghcr.io/benngarcia/mercator:<version> --push .
   docker buildx imagetools inspect ghcr.io/benngarcia/mercator:<version>   # copy the index digest
   ```

2. Update the digest pin in bucket-rails
   `apps/web/config/deploy.{staging,production,preview}.yml` (`accessories.mercator.image`)
   and bump `BUCKET_MERCATOR_CONFIG_REVISION`.
3. Redeploy the accessory via the standard entrypoint:

   ```bash
   bin/bucketctl deploy apply --destination staging
   ```

   Do not `bin/kamal deploy` directly. Use `accessory start` semantics if the
   accessory must be re-registered (see project memory: reboot loses pre-proxy
   accessory registration).
4. Note: the first `ListOffers` after boot pulls `busybox:1.37` (~2 MiB) onto
   each Docker connection host; the daemon caches it afterwards.

## 4. Post-fix canary (acceptance)

1. Positive: launch a `model_training` dispatch requesting `disk_gb = 25`
   (any staging training stage does this by default when the dataset is small:
   `max(dataset_gb, 20)` and the cap allows 30). It must schedule on the
   staging Docker connection, whose host has more than 25 GiB free.
   Verify placement:

   ```bash
   sqlite3 /mnt/mercator-data/mercator.db "
     SELECT data_json FROM events
     WHERE event_type = 'compute.run.placement_decided.v1'
     ORDER BY occurred_at DESC LIMIT 1;" | python3 -m json.tool
   ```

   A `placement_decided` event (followed by `launch_accepted`) proves the fix.
   To see the advertised capacity directly, list offers through the API: the
   docker offer's `resources.ephemeral_disk_bytes` should equal the probed
   free disk (hundreds of GiB), not `17179869184`.
2. Negative: request more disk than the host has free (e.g. temporarily set
   `disk_gb` to a value above the host's free space via the runtime params of
   an ad-hoc dispatch). The dispatch must fail loudly: mercator logs
   `no feasible offers`, and the placement candidates (offers API or a
   `placement_decided`-less run in sqlite) show
   `RESOURCE_INSUFFICIENT resources.ephemeral_disk` with the probed capacity
   in `offered`.
3. Record both canary run ids and outcomes in the ExecPlan / issue #715.

## 5. Follow-up in bucket-rails

Re-vendor `vendor/mercator` to the merged commit and note the mercator
dependency in the bucket-rails PR, per the monorepo convention.
