package nodeagent

import (
	"syscall"

	"github.com/benngarcia/mercator/internal/capability"
)

// diskFacts is how much room this daemon's machine has and how much of it is
// left, measured by asking the kernel about the filesystem the daemon keeps its
// content on.
//
// The daemon names that filesystem itself. Every image layer, every volume, and
// every container's writable layer lands under DockerRootDir, so its total and
// its available are exactly the disk content is accounted against, and reading
// it from `docker info` is what keeps this measurement about the daemon's
// machine rather than about whichever filesystem this process happens to be
// started on.
//
// It is a kernel call rather than a probe container on purpose. Running
// `busybox df` to learn how much disk is left needs the daemon to have that
// image, an egress path to fetch it if it does not, and enough room to create a
// container: the three things least likely to hold on a machine that is short
// of disk, out of network, or freshly pruned, which are precisely the machines
// this fact exists to measure. statfs answers on a filesystem that is one
// hundred percent full.
//
// A machine this agent cannot measure reports no disk fact rather than no facts
// at all. The daemon may be somewhere this process cannot see, which is a
// deployment this agent has no way to fix and no reason to leave the fleet over:
// its containers, their exits, and its own liveness are still the node's to
// report.
// A node that also keeps Artifact copies has content on two filesystems, and
// the room it can promise is the smaller of the two. On an ordinary install
// they are one filesystem and this changes nothing; where they are not, the
// alternative is advertising a terabyte of daemon storage to a Run whose dataset
// has to land in an agent directory with ten gigabytes left.
func (docker *DockerRuntime) diskFacts(daemonRoot string) capability.DiskFacts {
	daemon := statfsFacts(daemonRoot)
	if docker.artifactRoot == "" {
		return daemon
	}
	replicas := statfsFacts(docker.artifactRoot)
	if !daemon.Known || !replicas.Known {
		return capability.DiskFacts{}
	}
	if replicas.FreeBytes < daemon.FreeBytes {
		return replicas
	}
	return daemon
}

func statfsFacts(root string) capability.DiskFacts {
	if root == "" {
		return capability.DiskFacts{}
	}
	var filesystem syscall.Statfs_t
	if err := syscall.Statfs(root, &filesystem); err != nil {
		return capability.DiskFacts{}
	}
	block := int64(filesystem.Bsize)
	return capability.DiskFacts{
		Known:      true,
		TotalBytes: int64(filesystem.Blocks) * block,
		// Available rather than free, because the blocks a filesystem reserves
		// for root are not room a workload of this node's can use.
		FreeBytes: int64(filesystem.Bavail) * block,
	}
}
