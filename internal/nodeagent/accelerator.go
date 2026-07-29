package nodeagent

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/benngarcia/mercator/internal/capability"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/gpunorm"
)

// acceleratorProbeDeadline bounds one call to the vendor tool, and
// acceleratorProbeReapDelay bounds the wait after that deadline kills it.
//
// Both are here because this probe runs inside Facts, and Facts runs on the
// heartbeat, which is the goroutine every long-running thing in this agent was
// deliberately moved off: a probe that never returns stops the heartbeats and
// has the control plane declare a healthy machine lost in the middle of the work
// it asked for. A card that has fallen off the bus puts nvidia-smi into a wait
// the kernel will not interrupt, so the deadline alone is not enough and the
// SIGKILL behind it lands on a process that cannot take it. The reap delay is
// what makes the bound real: Wait returns whether or not the process did, this
// heartbeat reports the silence, and the seven good cards keep their workload.
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
// What separates the two refusals here is whether the tool answered, and not
// whether this agent liked the answer. An nvidia-smi that ran and could not
// reach its driver has established the thing Placement needs, that there is no
// working NVIDIA driver on this machine, and a Run needing one is refused
// CAPABILITY_MISMATCH naming the machine. An nvidia-smi this process could not
// run at all has established nothing about the hardware: the tool is off this
// unit's PATH, or unreadable to this user, or wedged against a card that has
// fallen off the bus. Filing that as the established negative publishes an
// 8xH100 box as a machine that has proven it has no driver and tells its
// operator to buy a different one, when the fix is one PATH entry. It is the
// silence instead, the same answer diskFacts gives to a statfs it cannot
// perform, and the refusal it earns is the UNKNOWN_FACT that says go and look.
func (docker *DockerRuntime) acceleratorFacts(ctx context.Context) capability.AcceleratorFacts {
	version, answer := docker.nvidiaSMI(ctx, "--version")
	if answer == smiUnasked {
		return capability.AcceleratorFacts{}
	}
	looked := capability.AcceleratorFacts{Established: true}
	if answer == smiFailed {
		return looked
	}
	driver, capable := smiVersions(version)
	if driver == "" {
		return looked
	}
	// The driver is stated as soon as it is read, before the cards are counted,
	// because the two calls fail independently. A machine whose --version reports
	// a working 595.71.05 and whose --query-gpu cannot get a handle on GPU 0 has a
	// driver; discarding it published one report saying both that the machine
	// established it has no driver and that it never stated one, which is the
	// distinction domain.HostFacts exists to keep. The cards it could not count
	// are an empty inventory, which is the true answer for a card that is gone.
	looked.Vendor = "nvidia"
	looked.DriverVersion = driver
	looked.DriverCapability = capable
	cards, answer := docker.nvidiaSMI(ctx, "--query-gpu=name,memory.total", "--format=csv,noheader,nounits")
	if answer == smiUnasked {
		return capability.AcceleratorFacts{}
	}
	looked.Devices = smiDevices(cards)
	return looked
}

// smiAnswer is what came back from one call to the vendor tool, which is three
// states rather than the usual two. A tool that ran and failed is the machine
// answering, and a tool this process could not run or could not wait for is the
// machine never being asked, and the offer Mercator publishes says different
// things about the hardware in the two cases.
type smiAnswer int

const (
	// smiUnasked is this agent failing to put the question: no such binary, no
	// permission to execute it, or a call that outlived its deadline.
	smiUnasked smiAnswer = iota
	// smiFailed is the tool running and exiting non-zero, which is how nvidia-smi
	// reports that it cannot communicate with the NVIDIA driver.
	smiFailed
	// smiStated is the tool running and printing what it was asked for.
	smiStated
)

func (docker *DockerRuntime) nvidiaSMI(ctx context.Context, args ...string) (string, smiAnswer) {
	bounded, done := context.WithTimeout(ctx, acceleratorProbeDeadline)
	defer done()
	probe := exec.CommandContext(bounded, docker.acceleratorBinary, args...)
	probe.WaitDelay = acceleratorProbeReapDelay
	output, err := probe.Output()
	switch {
	case err == nil:
		return string(output), smiStated
	// Checked before the exit status, because a probe the deadline killed exits
	// on a signal and would otherwise read as the tool answering.
	case bounded.Err() != nil:
		return "", smiUnasked
	case errors.As(err, new(*exec.ExitError)):
		return "", smiFailed
	default:
		return "", smiUnasked
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
