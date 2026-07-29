# Backup And Recovery

Mercator's durable state lives in two places, and a backup of one without the
other does not restore:

| State | Where it lives | Why a restore needs it |
| --- | --- | --- |
| The SQLite event log and everything derived from it | The files named by `MERCATOR_SQLITE_DSN` | Events, command idempotency records, sink cursors, workspaces, connections, and the sealed bytes of every stored connection credential |
| `MERCATOR_SECRET_KEY` | Wherever you keep secrets, never in the database | Stored credentials are sealed under a subkey derived from it. A restored database opens only under the key its rows were sealed with, and a server that cannot open one row refuses to start |

Back the key up when you generate it, beside the API token and separately from
the database. A database restored without its key still holds every event and
every workspace, and every stored provider credential in it is unreadable
ciphertext.

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

A fourth file, `/var/lib/mercator/mercator.db-lock`, appears beside them. It is
the claim the running process holds on the database, which is what stops a
second server or a `mercator rekey` running beside it. It holds no state: do not
back it up and do not copy it into a restore.

## Online Backup

Preferred operator flow with `sqlite3` available:

```sh
sqlite3 /var/lib/mercator/mercator.db \
  ".backup '/var/backups/mercator/mercator-$(date -u +%Y%m%dT%H%M%SZ).db'"
```

If `sqlite3` is not available, stop Mercator cleanly and copy the db, WAL, and
shm files together.

## Restore Check

Restore into a fresh path, under the key that database's rows were sealed with:

```sh
cp /var/backups/mercator/mercator-YYYYMMDDTHHMMSSZ.db /tmp/mercator-restore.db

MERCATOR_SQLITE_DSN='file:/tmp/mercator-restore.db' \
MERCATOR_API_TOKEN='restore-eval-token' \
MERCATOR_SECRET_KEY="$RESTORED_MERCATOR_SECRET_KEY" \
mercator serve
```

Then read both halves back, the events and a sealed credential:

```sh
export MERCATOR_API_URL=http://127.0.0.1:8080
export MERCATOR_API_TOKEN='restore-eval-token'

mercator run list --workspace-id ws_eval | jq .
mercator connection list --workspace-id ws_eval | jq '.connections[].id'
```

A listening server has already opened every sealed row in the restored copy,
because it refuses to start if it cannot open one. Two ways this check fails,
both before the listener binds:

```text
load secret key: MERCATOR_SECRET_KEY is required (32+ decoded bytes, hex or base64)
```

```text
configure server: daemon: credential store: credential for ws_eval/runpod cannot be decrypted with the configured MERCATOR_SECRET_KEY
```

The first is a restore attempted with no key. The second is a restore attempted
under the wrong key, which a freshly generated one always is. Neither is
recoverable by generating another key: find the key that database was running
under, or accept that its stored credentials are lost and recreate each
connection against the restored copy.

## Recovery Expectations

- Events, command idempotency records, and sink cursors live in SQLite.
- Public broker state is recoverable from the event history.
- Derived read models are disposable and rebuild from the event log.
- Sealed credentials are not derived and do not rebuild. The event log and the
  master key together are the state that needs backup.

## Gaps Before GA

- No automated backup scheduler is included.
- Nothing verifies that a backup opens. A copy nobody has started under its key
  is a copy nobody knows is restorable.
- No documented point-in-time restore drill exists beyond SQLite backup/restore.
- No schema migration runbook exists yet.
- No multi-process failover or replicated event-log topology is implemented.
