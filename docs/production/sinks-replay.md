# Sinks And Replay

Mercator V1 has sink cursor and replay mechanics. The executable server wires a
single in-process discard sink named `audit`; webhook, Kafka, and Postgres sink
implementations exist as code boundaries but are not externally configurable in
`cmd/mercator` yet.

## Check Sink Status

```sh
go run ./cmd/mercator sink status --sink-id audit | jq .
```

Equivalent REST call:

```sh
curl -fsS \
  -H "Authorization: Bearer $MERCATOR_API_TOKEN" \
  "$MERCATOR_API_URL/v1/sinks/audit" | jq .
```

The status response includes `cursor` and `has_cursor`.

## Deliver New Events

```sh
go run ./cmd/mercator sink deliver --sink-id audit | jq .
```

Delivery reads public events after the durable cursor. Private events are
skipped while advancing the cursor so sink delivery does not expose private
event data.

## Replay Events Without Moving The Cursor

```sh
go run ./cmd/mercator sink replay \
  --sink-id audit \
  --from 0 \
  --limit 100 \
  --replay-id replay-eval-1 | jq .
```

Replay returns delivered count and last position, but it does not move the
durable cursor. Use replay for audits, backfills, and evaluation of downstream
consumers.

## Failure Model

- Sink delivery failures are isolated from placement and run lifecycle.
- Durable cursors are stored in the SQLite event log's `subscription_offsets`
  table with IDs shaped like `sink:<sink_id>`.
- Replay is bounded by `limit`; default delivery batch size is 100.

## Current Limitations

- `cmd/mercator` configures only the `audit` discard sink.
- There are no production env vars for webhook endpoints, Kafka producers, or
  Postgres writers yet.
- External sink retry policy, authentication, dead-letter handling, and
  deployment-specific ordering guarantees still need GA documentation after
  client wiring exists.
