package nodeagent

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// diskProbeImage is what the disk probe runs. busybox is about two megabytes,
// is built for every platform a node runs on, and ships df; after the first
// probe the daemon serves it out of its own store.
const diskProbeImage = "busybox:1.37"

// diskFacts is how much disk this daemon's machine has and how much of it is
// left, measured by running df inside a container of that daemon's own.
//
// It is measured through the daemon rather than by reading this process's own
// filesystem because every other host fact in this report is the daemon's
// answer about the machine it runs on: the CPU count and the memory come out of
// `docker info`, so the disk has to come from the same machine or the report
// describes two. An agent beside a daemon in a VM would otherwise report its
// laptop's SSD as the room a workload has.
//
// A container's root filesystem sits on the storage driver's filesystem, which
// is the one holding every image layer, every volume, and every writable layer,
// so its total and its available are exactly the disk content is accounted
// against.
//
// A machine that cannot answer this fails its whole report. There is no honest
// silence available: HostFacts states disk as a number, a node advertising zero
// is refused every workload that declares a disk minimum, and a node advertising
// a guess sends work to a machine with nowhere to put it. An operator whose
// daemon cannot run a two megabyte container learns it at enrollment with the
// daemon's own error rather than through a node that quietly never wins a
// placement.
func (docker *DockerRuntime) diskFacts(ctx context.Context) (total, free int64, err error) {
	output, err := docker.run(ctx,
		"run", "--rm", "--network=none",
		"--label", docker.labelPrefix+"probe=disk",
		diskProbeImage, "df", "-Pk", "/",
	)
	if err != nil {
		return 0, 0, fmt.Errorf("measure this daemon's disk: %w", err)
	}
	return parseDiskFacts(output)
}

// parseDiskFacts reads the root filesystem's size and available space out of
// POSIX `df -Pk` output, whose columns are 1024-byte blocks.
func parseDiskFacts(output string) (total, free int64, err error) {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 || fields[len(fields)-1] != "/" {
			continue
		}
		if total, err = kibibytes(fields[1]); err != nil {
			return 0, 0, err
		}
		if free, err = kibibytes(fields[3]); err != nil {
			return 0, 0, err
		}
		return total, free, nil
	}
	return 0, 0, fmt.Errorf("no root filesystem in df output: %q", strings.TrimSpace(output))
}

func kibibytes(field string) (int64, error) {
	blocks, err := strconv.ParseInt(field, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse df column %q: %w", field, err)
	}
	return blocks * 1024, nil
}
