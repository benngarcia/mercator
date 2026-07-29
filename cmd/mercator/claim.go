package main

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"syscall"
)

// databaseClaim is one process's exclusive hold on a SQLite database.
//
// Mercator is a single-process control plane: one process owns one database.
// That was written down and nothing enforced it, which made the rotation
// procedure unsafe rather than merely inadvisable. `mercator rekey` run against
// a live server re-seals the rows it can see, the server keeps sealing new
// credentials under the key it loaded at boot, and the next restart under the
// new key alone refuses to open one of those rows. The operator has by then
// been told to remove the retired key, so the row is gone for good.
//
// Both commands take this claim, so the second one to ask is told the database
// is already in use instead of racing for it. The lock is advisory and held for
// as long as the file is open, which is what makes it state "a process is using
// this right now" rather than "a process used this once".
type databaseClaim struct{ lock *os.File }

// claimDatabase takes the claim, or answers with the refusal that names the
// database somebody else is holding.
//
// A memory-backed DSN is private to the process that opened it, so there is
// nothing for a second process to collide with and nothing to claim.
func claimDatabase(dsn string) (databaseClaim, error) {
	path := lockPath(dsn)
	if path == "" {
		return databaseClaim{}, nil
	}
	lock, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return databaseClaim{}, fmt.Errorf("open %s: %w", path, err)
	}
	// LOCK_EX belongs to this open file description rather than to this
	// process, so a second claim is refused even from inside one process. A
	// POSIX fcntl lock, which is what SQLite itself uses, would silently
	// succeed there and prove nothing.
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		return databaseClaim{}, fmt.Errorf("another mercator process is using %s", dsn)
	}
	return databaseClaim{lock: lock}, nil
}

// release drops the claim. Process exit drops it too; this exists so a command
// that returns without exiting hands the database back.
func (c databaseClaim) release() {
	if c.lock == nil {
		return
	}
	_ = syscall.Flock(int(c.lock.Fd()), syscall.LOCK_UN)
	_ = c.lock.Close()
}

// lockPath names the file the claim is taken on: a sibling of the database,
// beside the -wal and -shm files SQLite keeps there. It holds no state. An
// empty answer means this DSN names no file on disk.
func lockPath(dsn string) string {
	path := strings.TrimPrefix(dsn, "file:")
	if mark := strings.IndexByte(path, '?'); mark >= 0 {
		if inMemory(path[mark+1:]) {
			return ""
		}
		path = path[:mark]
	}
	if path == "" || path == ":memory:" {
		return ""
	}
	return path + "-lock"
}

func inMemory(rawQuery string) bool {
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return false
	}
	return query.Get("mode") == "memory"
}
