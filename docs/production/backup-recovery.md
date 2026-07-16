# Backup And Recovery

Mercator's internal source of truth is the SQLite event log configured by
`MERCATOR_SQLITE_DSN`. Back up the database and WAL files as a unit.

## Identify The Database

For a file DSN such as:

```sh
export MERCATOR_SQLITE_DSN='file:/var/lib/mercator/mercator.db'
```

the durable files are typically:

```text
/var/lib/mercator/mercator.db
/var/lib/mercator/mercator.db-wal
/var/lib/mercator/mercator.db-shm
```

## Online Backup

Preferred operator flow with `sqlite3` available:

```sh
sqlite3 /var/lib/mercator/mercator.db \
  ".backup '/var/backups/mercator/mercator-$(date -u +%Y%m%dT%H%M%SZ).db'"
```

If `sqlite3` is not available, stop Mercator cleanly and copy the db, WAL, and
shm files together.

## Restore Check

```sh
cp /var/backups/mercator/mercator-YYYYMMDDTHHMMSSZ.db /tmp/mercator-restore.db

MERCATOR_SQLITE_DSN='file:/tmp/mercator-restore.db' \
MERCATOR_API_TOKEN='restore-eval-token' \
go run ./cmd/mercator serve
```

Then verify:

```sh
MERCATOR_API_URL=http://127.0.0.1:8080 \
MERCATOR_API_TOKEN='restore-eval-token' \
go run ./cmd/mercator run list --workspace-id ws_eval | jq .
```

## Recovery Expectations

- Events, command idempotency records, and sink cursors live in
  SQLite.
- Public broker state is recoverable from the event history.
- Derived read models are disposable; the event log is the only state that needs backup.

## Gaps Before GA

- No automated backup scheduler is included.
- No documented point-in-time restore drill exists beyond SQLite backup/restore.
- No schema migration runbook exists yet.
- No multi-process failover or replicated event-log topology is implemented.
