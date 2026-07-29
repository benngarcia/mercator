package nodeagent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/benngarcia/mercator/internal/capability"
	"github.com/benngarcia/mercator/internal/domain"
)

// TestANodeReportsTheCardsAndTheDriverUnderThem is the accelerator half of a
// facts report, and it is the field that blocked every GPU placement onto an
// enrolled machine. The node registry publishes this inventory straight onto the
// offer, so a machine holding eight A100s that reported none was struck out of
// every accelerator placement with RESOURCE_INSUFFICIENT, which reads as a fleet
// that cannot run the work rather than an agent that never looked.
//
// The driver is reported beside the cards rather than derived from them. The
// host provides the driver, the image provides the accelerator stack that talks
// to it, and only one of those two numbers decides whether an image can start
// here at all.
func TestANodeReportsTheCardsAndTheDriverUnderThem(t *testing.T) {
	daemon := standInDaemon(t, cpuOnlyDaemon)
	cards := standInAcceleratorTool(t, `#!/bin/sh
case "$1" in
  "--version") printf 'NVIDIA-SMI version  : 550.54.15\nNVML version        : 550.54\nDRIVER version      : 550.54.15\nCUDA Version        : 12.4\n' ;;
  *) printf 'NVIDIA A100-SXM4-80GB, 81920\nNVIDIA A100-SXM4-80GB, 81920\n' ;;
esac
`)

	facts, err := NewDockerRuntime(daemon, WithAcceleratorTool(cards)).Facts(context.Background())

	if err != nil {
		t.Fatalf("read node facts: %v", err)
	}
	accelerator := facts.Host.Accelerator
	if !accelerator.Established {
		t.Fatal("the node asked its own machine about its cards and reported that it had not looked")
	}
	if accelerator.DriverVersion != "550.54.15" || accelerator.DriverCapability != "12.4" {
		t.Errorf("driver = %q supporting %q, want 550.54.15 supporting 12.4", accelerator.DriverVersion, accelerator.DriverCapability)
	}
	want := domain.AcceleratorInventory{
		Vendor:         "nvidia",
		Model:          "NVIDIA A100-SXM4-80GB",
		CanonicalModel: "nvidia-a100",
		Count:          2,
		MemoryBytes:    81920 * 1024 * 1024,
	}
	if len(accelerator.Devices) != 1 || accelerator.Devices[0] != want {
		t.Errorf("cards = %+v, want one entry %+v", accelerator.Devices, want)
	}
}

// TestAMachineWithNoDriverEstablishesThatItHasNone is the answer that must not
// be silence. A machine whose nvidia-smi is missing, whose nvidia-smi cannot
// reach its driver, and whose driver is broken have all established the thing
// Placement needs, and a Run that needs a driver is refused there with a code
// naming the machine rather than one naming an absence of evidence.
func TestAMachineWithNoDriverEstablishesThatItHasNone(t *testing.T) {
	daemon := standInDaemon(t, cpuOnlyDaemon)
	noDriver := standInAcceleratorTool(t, "#!/bin/sh\nexit 9\n")

	facts, err := NewDockerRuntime(daemon, WithAcceleratorTool(noDriver)).Facts(context.Background())

	if err != nil {
		t.Fatalf("a machine with no driver failed the whole report: %v", err)
	}
	if !facts.Host.Accelerator.Established {
		t.Fatal("the node looked, found no driver, and reported that it had not looked")
	}
	if facts.Host.Accelerator.DriverVersion != "" || len(facts.Host.Accelerator.Devices) != 0 {
		t.Fatalf("a machine with no driver reported %+v", facts.Host.Accelerator)
	}
	if stated := facts.Host.Accelerator.Attestations()[domain.HostFactNvidiaDriver]; stated {
		t.Fatal("a machine with no driver attested that it has one")
	}
}

// TestAReportThatNeverLookedCarriesNoDriverForward is the silence, on the other
// side of the contract. A runtime that never implemented this can send whatever
// it likes in the fields; the control plane keeps only the half the reporter
// stands behind, once, where the report crosses in, so no reader downstream has
// two answers to choose between.
func TestAReportThatNeverLookedCarriesNoDriverForward(t *testing.T) {
	claimed := capability.NodeFacts{Host: capability.HostFacts{Accelerator: capability.AcceleratorFacts{
		Established:   false,
		Vendor:        "nvidia",
		DriverVersion: "550.54.15",
		Devices:       []domain.AcceleratorInventory{{Vendor: "nvidia", Model: "A100", Count: 8}},
	}}}

	established := claimed.Established()

	if established.Host.Accelerator.DriverVersion != "" || len(established.Host.Accelerator.Devices) != 0 {
		t.Fatalf("an unestablished report kept %+v", established.Host.Accelerator)
	}
	if established.Host.Accelerator.Attestations() != nil {
		t.Fatal("a machine that never looked attested to a driver either way")
	}
}

// TestThisMachineReportsItsOwnCardsAndDriver is the live half: the agent against
// the hardware it is running on, checked against what the vendor tool says when
// this case asks it directly rather than against anything the agent wrote down.
// A machine with no NVIDIA driver has nothing to check, which is a different
// case and covered above.
func TestThisMachineReportsItsOwnCardsAndDriver(t *testing.T) {
	requireDocker(t)
	requireNvidiaDriver(t)

	facts, err := NewDockerRuntime("").Facts(context.Background())

	if err != nil {
		t.Fatalf("read node facts: %v", err)
	}
	accelerator := facts.Host.Accelerator
	if driver := smiField(t, "DRIVER version"); accelerator.DriverVersion != driver {
		t.Errorf("driver = %q, and nvidia-smi says %q", accelerator.DriverVersion, driver)
	}
	if capable := smiField(t, "CUDA Version"); accelerator.DriverCapability != capable {
		t.Errorf("driver capability = %q, and nvidia-smi says %q", accelerator.DriverCapability, capable)
	}
	if cards := acceleratorCount(accelerator); cards != countedCards(t) {
		t.Errorf("cards = %d, and nvidia-smi lists %d", cards, countedCards(t))
	}
	if !accelerator.Attestations()[domain.HostFactNvidiaDriver] {
		t.Errorf("a machine running driver %q did not attest one", accelerator.DriverVersion)
	}
	t.Logf("this machine reports %d card(s) on driver %s supporting CUDA %s: %+v",
		acceleratorCount(accelerator), accelerator.DriverVersion, accelerator.DriverCapability, accelerator.Devices)
}

func acceleratorCount(facts capability.AcceleratorFacts) int {
	cards := 0
	for _, device := range facts.Devices {
		cards += device.Count
	}
	return cards
}

// cpuOnlyDaemon is a stand-in `docker` that answers the rest of a facts report,
// so a case about accelerators states only the accelerators.
const cpuOnlyDaemon = `#!/bin/sh
case "$1 $2" in
  "info --format") echo '{"OperatingSystem":"linux","Architecture":"x86_64","ServerVersion":"29.4.0","NCPU":8,"MemTotal":1,"DockerRootDir":"/var/lib/docker-on-another-machine"}' ;;
  "images --digests") ;;
esac
`

// standInAcceleratorTool is a scripted `nvidia-smi`. Hardware is the one thing a
// case cannot arrange, so a machine with two A100s and a machine whose driver
// will not load are both stated here, and the live case beside them checks the
// same agent against the cards this host really has.
func standInAcceleratorTool(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nvidia-smi")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write the stand-in accelerator tool: %v", err)
	}
	return path
}

func requireNvidiaDriver(t *testing.T) {
	t.Helper()
	if err := exec.Command("nvidia-smi", "--version").Run(); err != nil {
		t.Skipf("this machine has no working NVIDIA driver to check the agent against: %v", err)
	}
}

func smiField(t *testing.T, label string) string {
	t.Helper()
	output, err := exec.Command("nvidia-smi", "--version").Output()
	if err != nil {
		t.Fatalf("nvidia-smi --version: %v", err)
	}
	for line := range strings.SplitSeq(string(output), "\n") {
		name, value, split := strings.Cut(line, ":")
		if split && strings.EqualFold(strings.TrimSpace(name), label) {
			return strings.TrimSpace(value)
		}
	}
	t.Fatalf("nvidia-smi --version states no %q:\n%s", label, output)
	return ""
}

func countedCards(t *testing.T) int {
	t.Helper()
	output, err := exec.Command("nvidia-smi", "--query-gpu=name", "--format=csv,noheader").Output()
	if err != nil {
		t.Fatalf("nvidia-smi --query-gpu: %v", err)
	}
	cards := 0
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if strings.TrimSpace(line) != "" {
			cards++
		}
	}
	if cards == 0 {
		t.Fatal("nvidia-smi answered a version and listed no cards")
	}
	return cards
}
