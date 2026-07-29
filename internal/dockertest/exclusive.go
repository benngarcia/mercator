// Package dockertest hands this machine's one Docker daemon to one test at a
// time.
//
// `go test ./...` runs every package in its own process and all of them at once,
// and three packages in this tree work the single Docker daemon this host has.
// They create and remove images, run and reap containers, start registries and
// object stores, and read the whole daemon's inventory. Run together they measure
// each other rather than Mercator: a case that enumerates every image on the
// daemon sees one that another case is removing, a case waiting on a node's
// heartbeat waits behind a facts read that four suites are contending for, and an
// object store is asked to serve a bucket before a loaded host has finished
// starting it. All three were observed on a twenty four core workstation, in
// different places on different runs, and they are mercator#212.
//
// The daemon is one machine's worth of shared state, so the fix is the one shared
// state has always had: hold it while you use it.
package dockertest

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// lockName is a fixed path because the tests this serializes do not share a
// process and can agree on nothing else.
const lockName = "mercator-docker-daemon.lock"

// held is whether this process already has the daemon. A file lock belongs to
// the open file, not the process, so a second Exclusive inside a test that
// already holds one would wait on itself forever. Go runs a package's tests one
// at a time unless they ask otherwise, and none of the tests this serializes do,
// so what this state means is "the test running now already has it".
var held bool

// Exclusive gives this test the Docker daemon to itself until it ends, waiting
// for whichever test holds it now.
//
// It is a file lock rather than anything in this process, because the tests it
// keeps apart are in separate processes, and the kernel drops it when a process
// dies, so a test killed mid-run cannot wedge the tree.
func Exclusive(t *testing.T) {
	t.Helper()
	if held {
		return
	}
	lock, err := os.OpenFile(filepath.Join(os.TempDir(), lockName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open the lock this host's Docker daemon is held under: %v", err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		_ = lock.Close()
		t.Fatalf("take this host's Docker daemon: %v", err)
	}
	held = true
	t.Cleanup(func() {
		held = false
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
	})
}
