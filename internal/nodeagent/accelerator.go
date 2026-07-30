package nodeagent

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/benngarcia/mercator/internal/capability"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/gpunorm"
)

// acceleratorProbeDeadline bounds how long Facts waits for the vendor tool, and
// acceleratorProbeReapDelay bounds how long the call this agent walked away from
// keeps a goroutine and a pipe after that.
//
// Both are here because this probe runs inside Facts, and Facts runs on the
// heartbeat, which is the goroutine every long-running thing in this agent was
// deliberately moved off: a probe that never returns stops the heartbeats and
// has the control plane declare a healthy machine lost in the middle of the work
// it asked for. A card that has fallen off the bus puts nvidia-smi into a wait
// the kernel will not interrupt, and a SIGKILL delivered to a process in that
// state is a signal left pending on a process that never exits. Nothing in
// os/exec bounds that: Cmd.Wait blocks in Process.Wait before it ever consults
// the deadline or the reap delay. So the bound is this agent's own, the deadline
// is the caller walking away rather than the process being killed, and the reap
// delay is only what keeps an abandoned call from holding its goroutine and its
// stdout pipe for the six hundred seconds the tool asked for. This heartbeat
// reports the silence and the seven good cards keep their workload.
const (
	acceleratorProbeDeadline  = 3 * time.Second
	acceleratorProbeReapDelay = time.Second
)

// acceleratorFacts is this machine looking at its own cards and the driver
// behind them, so an offer can state what an image's accelerator stack has to
// run on instead of a launch finding out.
//
// It asks the vendor's own tool rather than reading the container runtime's
// answer, because the runtime answers a different question. `docker info` names
// the nvidia runtime when the container toolkit is installed, which says a
// container can be handed the cards and says nothing about how many there are,
// what they are, or which driver is under them: a toolkit installed on a machine
// whose driver was never loaded reports exactly the same thing as this
// workstation.
//
// What the tool establishes is what it printed, and nothing else. A machine
// whose nvidia-smi named a driver has one, and the cards it went on to list are
// this machine's inventory. Every other outcome is this agent failing to reach
// an answer rather than the machine answering: the tool is off this unit's PATH,
// or unreadable to this user, or wedged against a card that fell off the bus, or
// running in an execution context with no device nodes in it, which a hardened
// unit with PrivateDevices=yes and an agent in a container with only the docker
// socket both produce. All of those exit non-zero saying they could not
// communicate with the driver, exactly as a machine with no driver does, so the
// exit status cannot tell an unloaded driver from an agent that cannot see one.
// Filed as the established negative it publishes an 8xH100 box as a machine that
// has proven it has no driver and tells its operator to buy a different one.
//
// The kernel is asked instead, because the kernel is the thing that would know.
// A loaded NVIDIA driver publishes its own version under /proc, so a tool that
// failed beside a kernel that has the module says the agent could not see what
// is there, and a tool that failed beside a kernel with no module is a machine
// that has established it has no driver. A kernel that says nothing at all is
// the silence, and the refusal it earns is the UNKNOWN_FACT that says go and
// look, which is the same answer diskFacts gives to a statfs it cannot perform.
func (docker *DockerRuntime) acceleratorFacts(ctx context.Context) capability.AcceleratorFacts {
	version, answered := docker.nvidiaSMI(ctx, "--version")
	driver, capable := smiVersions(version)
	if !answered || driver == "" {
		return docker.acceleratorFactsFromTheKernel()
	}
	// The driver is stated as soon as it is read, before the cards are counted,
	// because the two calls fail independently. A machine whose --version reports
	// a working 595.71.05 and whose --query-gpu cannot get a handle on GPU 0 has a
	// driver; discarding it published one report saying both that the machine
	// established it has no driver and that it never stated one, which is the
	// distinction domain.HostFacts exists to keep. The cards it could not count
	// are an empty inventory, which is the true answer for a card that is gone.
	looked := capability.AcceleratorFacts{
		Established:      true,
		Vendor:           "nvidia",
		DriverVersion:    driver,
		DriverCapability: capable,
	}
	cards, answered := docker.nvidiaSMI(ctx, "--query-gpu=name,memory.total", "--format=csv,noheader,nounits")
	if !answered {
		// The cards were never counted, and one flag covers both halves of this
		// report, so the whole report is the silence. Publishing the driver with an
		// inventory nobody took would have a Run pinned to eight A100s struck out
		// RESOURCE_INSUFFICIENT on the machine holding them.
		return capability.AcceleratorFacts{}
	}
	looked.Devices = smiDevices(cards)
	return looked
}

// acceleratorFactsFromTheKernel is what this machine has established about its
// cards when the vendor tool did not answer for them. The kernel module is the
// driver, so the file it publishes is the fact: no module is a machine that has
// established it runs no NVIDIA driver, and a kernel this agent cannot read at
// all has established nothing.
func (docker *DockerRuntime) acceleratorFactsFromTheKernel() capability.AcceleratorFacts {
	if _, err := os.Stat(filepath.Join(docker.kernelReports, "driver", "nvidia", "version")); err == nil {
		return capability.AcceleratorFacts{}
	}
	if _, err := os.Stat(filepath.Join(docker.kernelReports, "version")); err != nil {
		return capability.AcceleratorFacts{}
	}
	return capability.AcceleratorFacts{Established: true}
}

// smiReport is one finished call to the vendor tool: what it printed, and
// whether this agent got to ask at all. A tool that ran and exited non-zero
// printed nothing and answered, which is not the same as a tool this process
// could not run, could not reach, or walked away from.
type smiReport struct {
	output   string
	answered bool
}

// nvidiaSMI asks the vendor tool one question under this agent's own bound. The
// call runs on its own goroutine and the heartbeat waits on the deadline, so a
// tool the kernel will not let go of holds nothing but itself.
func (docker *DockerRuntime) nvidiaSMI(ctx context.Context, args ...string) (string, bool) {
	bounded, done := context.WithTimeout(ctx, acceleratorProbeDeadline)
	defer done()
	probe := exec.CommandContext(bounded, docker.acceleratorBinary, args...)
	probe.WaitDelay = acceleratorProbeReapDelay
	answered := make(chan smiReport, 1)
	go func() { answered <- runProbe(probe) }()
	select {
	case report := <-answered:
		// The deadline is checked before the report, because a probe the deadline
		// killed exits on a signal and would otherwise read as the tool answering.
		if bounded.Err() != nil {
			return "", false
		}
		return report.output, report.answered
	case <-bounded.Done():
		return "", false
	}
}

func runProbe(probe *exec.Cmd) smiReport {
	output, err := probe.Output()
	switch {
	case err == nil:
		return smiReport{output: string(output), answered: true}
	case errors.As(err, new(*exec.ExitError)):
		return smiReport{answered: true}
	default:
		return smiReport{}
	}
}

// smiVersions reads the driver this host runs and the highest CUDA version that
// driver supports out of `nvidia-smi --version`, which states both as labelled
// lines. The second is the number an image's accelerator stack is weighed
// against: a container carrying a CUDA 13 runtime needs a driver that supports
// CUDA 13, whatever compute capability the cards under it happen to report.
func smiVersions(report string) (driver string, capable string) {
	for line := range strings.SplitSeq(report, "\n") {
		label, value, split := strings.Cut(line, ":")
		if !split {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(label)) {
		case "driver version":
			driver = strings.TrimSpace(value)
		case "cuda version":
			capable = strings.TrimSpace(value)
		}
	}
	return driver, capable
}

// smiDevices turns one CSV row per card into the inventory vocabulary a
// provider lists capacity in, so Placement counts a machine's own cards against
// a workload's requirement the same way it counts a marketplace listing's.
// Identical cards collapse into one entry with a count, which is what a
// provider publishes and what the requirement is written against.
//
// The memory goes through gpunorm for the same reason the model name does. Both
// halves of a card's identity have to survive the trip from a vendor tool to the
// unit a marketplace publishes, or a floor a caller copied out of a listing
// strikes out the very card it was copied from once Mercator owns it.
func smiDevices(report string) []domain.AcceleratorInventory {
	var devices []domain.AcceleratorInventory
	for line := range strings.SplitSeq(report, "\n") {
		name, memory, split := strings.Cut(line, ",")
		if !split {
			continue
		}
		mebibytes, err := strconv.ParseInt(strings.TrimSpace(memory), 10, 64)
		if err != nil {
			continue
		}
		card := strings.TrimSpace(name)
		devices = addCard(devices, card, gpunorm.CardMemoryBytes(card, mebibytes))
	}
	return devices
}

func addCard(devices []domain.AcceleratorInventory, model string, memoryBytes int64) []domain.AcceleratorInventory {
	for index, device := range devices {
		if device.Model == model && device.MemoryBytes == memoryBytes {
			devices[index].Count++
			return devices
		}
	}
	return append(devices, domain.AcceleratorInventory{
		Vendor:         "nvidia",
		Model:          model,
		CanonicalModel: gpunorm.Canonical("nvidia", model),
		Count:          1,
		MemoryBytes:    memoryBytes,
	})
}
