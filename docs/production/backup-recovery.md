# Backup And Recovery

Mercator's durable state lives in two places, and a backup of one without the
other does not restore:

| State | Where it lives | Why a restore needs it |
| --- | --- | --- |
| The SQLite event log and everything derived from it | The files named by `MERCATOR_SQLITE_DSN` | Events, command idempotency records, sink cursors, connections, and the sealed bytes of every stored connection credential |
| `MERCATOR_SECRET_KEY` | Wherever you keep secrets, never in the database | Stored credentials are sealed under a subkey derived from it. A restored database opens only under the key its rows were sealed with, and a server that cannot open one row refuses to start |

Back the key up when you generate it, beside the API token and separately from
the database. A database restored without its key still holds every event and
the whole deployment, and every stored provider credential in it is unreadable
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

Mercator backs itself up. The server keeps serving while it runs:

```sh
MERCATOR_SQLITE_DSN='file:/var/lib/mercator/mercator.db' \
mercator backup "/var/backups/mercator/mercator-$(date -u +%Y%m%dT%H%M%SZ).db"
```

This is SQLite's own `VACUUM INTO`, taken inside one read transaction, so the
copy is the database as of a single instant even while the control plane is
writing to it. The result is one self-contained file: no `-wal` or `-shm` file
travels with it, and there is nothing to copy "together". `MERCATOR_SQLITE_DSN`
is required for this command, and it must name the database the server serves,
since the command copies that database and no other. The copy is created mode
`0600`, because it holds every event and the sealed bytes of every stored
provider credential.

Three ways it refuses rather than producing a copy you cannot use:

```text
backup: MERCATOR_SQLITE_DSN is required and must name the database the server
        serves; backup will not fall back to a default path, because a copy of
        the wrong database also exits 0

backup: /var/lib/mercator/mercator.db does not exist, and a backup of a database
        this command created would restore into a control plane with no history;
        export the MERCATOR_SQLITE_DSN the server runs with

backup: take /var/backups/mercator/yesterday.db for the backup: file exists
```

The first is the cron entry that does not inherit the unit's environment. `serve`
resolves an unset `MERCATOR_SQLITE_DSN` to a per-user data directory and creates
it, so a backup that did the same would copy whatever database a `mercator serve`
on this host once left in the invoking account's home directory, write a file the
size of a real backup, and exit 0. Put the variable in the cron entry itself.

The second is a DSN naming a database that is not there. Left to run, that backup
would create an empty database, copy the nothing in it, and exit 0.

The third is a destination that is already there. Mercator takes the destination
itself rather than leaving that to SQLite, which refuses a file it can read as a
database but silently overwrites one too short to be one: a copy a full disk
truncated is exactly the path an operator retries over, and losing it without a
word is the outcome a backup command must not have. Name a new path per backup,
and prune old ones yourself.

A backup that is interrupted leaves the destination free. The copy is assembled
in a file named `<destination>.partial-NNNNNNNNNN` beside the destination and
linked into place only once it is complete, so a `timeout` in the cron entry, a
systemd stop, Ctrl-C or an OOM kill leaves that partial file, and SQLite's
`-journal` beside it, with the destination path still free for the retry.
Delete files matching `*.partial-*` in the backup directory; nothing prunes
them, and none of them is a backup. `file exists` therefore always means a file
is really there.

The destination is a filesystem path, taken literally and resolved to an
absolute one. It is not a SQLite URI: `mercator backup file:latest.db` writes a
file whose name begins with `file:`, and the path the command reports is the
path it wrote.

The command opens the database read-only, so it is safe on a deployment that has
crashed and has not been restarted. Taking a copy before you restart a crashed
server leaves the `-wal` beside it exactly as the crash left it; the copy is
still whole, because a read-only connection reads through that log.

The command does not take the database claim that `serve` and `rekey` hold. A
backup changes nothing, and claiming the database would mean the only supported
backup was one taken with the control plane stopped.

It does not copy `MERCATOR_SECRET_KEY`, which is not in the database. Back the
key up separately, once, when you generate it.

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

mercator run list | jq .
mercator connection list | jq '.connections[].id'
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

## What A Crash Leaves Behind

Mercator's database is journalled write-ahead, so an ordinary crash needs no
operator intervention: the next start recovers the file.

- A write the API answered for is on disk. SQLite commits at `synchronous=FULL`
  under this build, which flushes the write-ahead log before the commit returns,
  so the answer is a durability promise rather than a hopeful one.
- A write that had not committed is gone, whole. A killed process leaves its
  half-written pages in the write-ahead log and recovery discards them; there is
  no partial event and no partly renamed record.
- Migrations are transactional. A start killed mid-migration either applied one
  in full or has not applied it, and the start after it applies what is left.
- A migration that rewrites run events marks the Run projection stale in the same
  transaction as the rewrite, so a crash between the two cannot leave a read
  model answering from a vocabulary the log no longer speaks. Two migrations
  rewrite run events, the service class rename and the booking decision rename,
  and both mark it. A migration that rewrites events on another kind of stream
  does not need to: the Run projection is reduced from run streams alone.

This is checked by killing real processes, not by closing them: see
`internal/storage/sqlite/crash_recovery_test.go` and
`cmd/mercator/crash_recovery_test.go`, the second of which kills a containerised
`mercator serve` with `SIGKILL` and reads the survivors back over HTTP.

## Gaps Before GA

- No automated backup scheduler is included. `mercator backup` is a command an
  operator or a cron job runs; nothing runs it for you, and nothing prunes old
  copies.
- Nothing verifies that a backup opens. `mercator backup` reports that it wrote
  a file, not that a control plane starts on it, so the restore check above is
  still a manual drill.
- Recovery is to the last backup. There is no continuous archiving and no
  point-in-time recovery, so the data you can lose is everything written since
  the last `mercator backup` ran.
- No schema migration runbook exists yet.
- No multi-process failover or replicated event-log topology is implemented.
