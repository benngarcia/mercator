package nodeagent

import (
	"context"
	"os/exec"
	"strconv"
	"strings"

	"github.com/benngarcia/mercator/internal/capability"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/gpunorm"
)

// mebibyte is the unit nvidia-smi reports memory in under --format=nounits.
const mebibyte = int64(1) << 20

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
// Every way of failing is the same answer here, and that is deliberate. A
// machine with no nvidia-smi on it, a machine whose nvidia-smi cannot reach its
// driver, and a machine whose driver is broken have all established the one
// thing Placement needs: there is no working NVIDIA driver here. It is the
// established-false case rather than the silent one, so a Run needing a driver
// is refused with CAPABILITY_MISMATCH naming the machine rather than with the
// UNKNOWN_FACT that means nobody looked. Silence is what a runtime that never
// implemented this reports, which is what the flag is for.
func (docker *DockerRuntime) acceleratorFacts(ctx context.Context) capability.AcceleratorFacts {
	looked := capability.AcceleratorFacts{Established: true}
	version, err := docker.nvidiaSMI(ctx, "--version")
	if err != nil {
		return looked
	}
	driver, capable := smiVersions(version)
	if driver == "" {
		return looked
	}
	devices, err := docker.nvidiaSMI(ctx, "--query-gpu=name,memory.total", "--format=csv,noheader,nounits")
	if err != nil {
		return looked
	}
	looked.Vendor = "nvidia"
	looked.DriverVersion = driver
	looked.DriverCapability = capable
	looked.Devices = smiDevices(devices)
	return looked
}

func (docker *DockerRuntime) nvidiaSMI(ctx context.Context, args ...string) (string, error) {
	output, err := exec.CommandContext(ctx, docker.acceleratorBinary, args...).Output()
	if err != nil {
		return "", err
	}
	return string(output), nil
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
		devices = addCard(devices, strings.TrimSpace(name), mebibytes*mebibyte)
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
