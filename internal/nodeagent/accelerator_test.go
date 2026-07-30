package nodeagent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
// be silence. A machine whose kernel has no NVIDIA module has established the
// thing Placement needs, and a Run that needs a driver is refused there with a
// code naming the machine rather than one naming an absence of evidence.
func TestAMachineWithNoDriverEstablishesThatItHasNone(t *testing.T) {
	daemon := standInDaemon(t, cpuOnlyDaemon)
	noDriver := standInAcceleratorTool(t, "#!/bin/sh\nexit 9\n")

	facts, err := NewDockerRuntime(daemon,
		WithAcceleratorTool(noDriver),
		WithKernelReports(standInKernel(t, false)),
	).Facts(context.Background())

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

// TestAMachineWhoseVendorToolCannotBeRunEstablishesNothing is the other side of
// the case above, and the two must not answer the same way. nvidia-smi missing
// from this process's PATH is not a machine that has proven it has no cards: it
// is a unit file with a trimmed Environment=PATH, or a distribution that
// installs the tool where the service PATH does not reach, on a box that may
// well hold eight H100s. Published as the established negative, every GPU Run is
// refused there with the code that tells an operator to buy a different machine
// when the fix is one PATH entry, so the report is the silence and the refusal
// is the one that says go and look.
func TestAMachineWhoseVendorToolCannotBeRunEstablishesNothing(t *testing.T) {
	daemon := standInDaemon(t, cpuOnlyDaemon)
	unreachable := filepath.Join(t.TempDir(), "nvidia-smi-that-is-not-installed")

	facts, err := NewDockerRuntime(daemon,
		WithAcceleratorTool(unreachable),
		WithKernelReports(standInKernel(t, true)),
	).Facts(context.Background())

	if err != nil {
		t.Fatalf("a machine whose vendor tool is missing failed the whole report: %v", err)
	}
	if facts.Host.Accelerator.Established {
		t.Fatalf("a machine nobody could ask established %+v", facts.Host.Accelerator)
	}
	if facts.Host.Accelerator.Attestations() != nil {
		t.Fatal("a machine nobody could ask attested a driver either way")
	}
	if refusals := refusalsForADriver(facts); len(refusals) != 1 || refusals[0].Code != "UNKNOWN_FACT" {
		t.Fatalf("a Run needing a driver was refused %+v on a machine nobody could ask", refusals)
	}
}

// TestADriverThatAnsweredSurvivesCardsThatCouldNotBeCounted holds one report to
// one story. The two calls to the vendor tool fail independently: a card that
// has fallen off the bus, an Xid, or an ECC remap pending answers --version with
// a working driver and fails --query-gpu on the handle. Discarding the driver
// there published a machine that had both established it has no driver and never
// stated one, in the same Booking Decision, which is exactly the distinction
// domain.HostFacts exists to keep.
func TestADriverThatAnsweredSurvivesCardsThatCouldNotBeCounted(t *testing.T) {
	daemon := standInDaemon(t, cpuOnlyDaemon)
	lostCard := standInAcceleratorTool(t, `#!/bin/sh
case "$1" in
  "--version") printf 'DRIVER version      : 595.71.05\nCUDA Version        : 13.2\n' ;;
  *) echo "Unable to determine the device handle for GPU 0000:01:00.0" >&2; exit 15 ;;
esac
`)

	facts, err := NewDockerRuntime(daemon, WithAcceleratorTool(lostCard)).Facts(context.Background())

	if err != nil {
		t.Fatalf("a machine whose card fell off the bus failed the whole report: %v", err)
	}
	if facts.Host.Accelerator.DriverVersion != "595.71.05" {
		t.Fatalf("a machine running 595.71.05 reported %+v", facts.Host.Accelerator)
	}
	if !facts.Host.Accelerator.Attestations()[domain.HostFactNvidiaDriver] {
		t.Fatal("a machine whose vendor tool named a working driver attested it has none")
	}
	if len(facts.Host.Accelerator.Devices) != 0 {
		t.Fatalf("a machine that could not get a handle on its card counted %+v", facts.Host.Accelerator.Devices)
	}
	if refusals := refusalsForADriver(facts); len(refusals) != 0 {
		t.Fatalf("a machine running 595.71.05 was refused %+v", refusals)
	}
}

// TestAWedgedVendorToolDoesNotHoldTheReport is the heartbeat's bound. Facts runs
// on the agent's own select loop, which is the goroutine every long-running
// thing in this agent was deliberately moved off, so a vendor tool that never
// returns stops the heartbeats and has the control plane declare a healthy
// machine lost in the middle of the work it asked for. A card that has fallen
// off the bus puts nvidia-smi into a wait the kernel will not interrupt, so the
// stand-in here ignores the signal the deadline sends it, and the report still
// comes back.
func TestAWedgedVendorToolDoesNotHoldTheReport(t *testing.T) {
	daemon := standInDaemon(t, cpuOnlyDaemon)
	wedged := standInAcceleratorTool(t, wedgedVendorTool)

	started := time.Now()
	facts, err := NewDockerRuntime(daemon,
		WithAcceleratorTool(wedged),
		WithKernelReports(standInKernel(t, true)),
	).Facts(context.Background())

	if err != nil {
		t.Fatalf("a wedged vendor tool failed the whole report: %v", err)
	}
	// The bound is the deadline and not the deadline plus the reap delay. Waiting
	// out the reap is waiting on os/exec to give up on a process, which is the
	// wait this agent must not be doing: Cmd.Wait blocks in Process.Wait before it
	// ever consults WaitDelay, so a tool the kernel will not let go of holds the
	// heartbeat for as long as it likes. What comes back at the deadline is this
	// agent walking away from the call.
	if held := time.Since(started); held < acceleratorProbeDeadline || held > acceleratorProbeDeadline+acceleratorProbeReapDelay {
		t.Fatalf("a wedged vendor tool held the heartbeat for %s, and the deadline is %s", held, acceleratorProbeDeadline)
	}
	if facts.Host.Accelerator.Established {
		t.Fatalf("a machine whose vendor tool never answered established %+v", facts.Host.Accelerator)
	}
}

// TestACardStatesTheCapacityItIsSoldWith is the unit a floor is written in. A
// caller copies memory_min_bytes out of a marketplace listing, which publishes
// the capacity the card is sold with; nvidia-smi reports the framebuffer left
// after the driver's own reserved region. Published raw, the same physical card
// cleared the floor while a provider rented it and was struck out
// RESOURCE_INSUFFICIENT the moment Mercator enrolled it.
func TestACardStatesTheCapacityItIsSoldWith(t *testing.T) {
	daemon := standInDaemon(t, cpuOnlyDaemon)
	workstation := standInAcceleratorTool(t, `#!/bin/sh
case "$1" in
  "--version") printf 'DRIVER version      : 595.71.05\nCUDA Version        : 13.2\n' ;;
  *) printf 'NVIDIA GeForce RTX 5090, 32607\n' ;;
esac
`)

	facts, err := NewDockerRuntime(daemon, WithAcceleratorTool(workstation)).Facts(context.Background())

	if err != nil {
		t.Fatalf("read node facts: %v", err)
	}
	// What a marketplace publishes for the same card, which is what a Run's
	// memory floor is written against.
	const listedAs = int64(32) << 30
	if len(facts.Host.Accelerator.Devices) != 1 || facts.Host.Accelerator.Devices[0].MemoryBytes != listedAs {
		t.Fatalf("a card listed at %d bytes is offered as %+v", listedAs, facts.Host.Accelerator.Devices)
	}
}

// TestAnAgentThatCannotSeeTheDriverEstablishesNothing is the failure the exit
// status cannot tell apart from a machine with no driver, on the machine where
// getting it wrong costs the most. An agent whose execution context has no
// device nodes in it, which a unit hardened with PrivateDevices=yes and an agent
// in a container with only the docker socket both produce, runs nvidia-smi and
// gets "NVIDIA-SMI has failed because it couldn't communicate with the NVIDIA
// driver" and exit 9: the same words and the same status a machine with no
// driver gives. The kernel underneath still holds the module and still runs
// every `--gpus` container the daemon starts. Filed on the exit status alone,
// an 8xH100 box refused every GPU Run with CAPABILITY_MISMATCH and told its
// operator to buy a different machine.
func TestAnAgentThatCannotSeeTheDriverEstablishesNothing(t *testing.T) {
	daemon := standInDaemon(t, cpuOnlyDaemon)
	blinded := standInAcceleratorTool(t, `#!/bin/sh
echo "NVIDIA-SMI has failed because it couldn't communicate with the NVIDIA driver." >&2
exit 9
`)

	facts, err := NewDockerRuntime(daemon,
		WithAcceleratorTool(blinded),
		WithKernelReports(standInKernel(t, true)),
	).Facts(context.Background())

	if err != nil {
		t.Fatalf("an agent that cannot see the driver failed the whole report: %v", err)
	}
	if facts.Host.Accelerator.Established {
		t.Fatalf("a machine whose kernel holds the NVIDIA module established %+v", facts.Host.Accelerator)
	}
	if refusals := refusalsForADriver(facts); len(refusals) != 1 || refusals[0].Code != "UNKNOWN_FACT" {
		t.Fatalf("a Run needing a driver was refused %+v on a machine whose agent could not look", refusals)
	}
}

// standInKernel is a machine's own /proc, holding the NVIDIA module's report or
// not holding it. Hardware is the one thing a case cannot arrange, and the
// kernel under it is the second: a case that read this host's own /proc would
// state one machine on the workstation it was written on and another on the
// build box.
func standInKernel(t *testing.T, driverLoaded bool) string {
	t.Helper()
	root := t.TempDir()
	// Every Linux kernel publishes this, and a root without it is a machine whose
	// kernel this agent cannot read at all.
	if err := os.WriteFile(filepath.Join(root, "version"), []byte("Linux version 7.0.0\n"), 0o644); err != nil {
		t.Fatalf("write the stand-in kernel report: %v", err)
	}
	if !driverLoaded {
		return root
	}
	reports := filepath.Join(root, "driver", "nvidia")
	if err := os.MkdirAll(reports, 0o755); err != nil {
		t.Fatalf("make the stand-in driver reports: %v", err)
	}
	if err := os.WriteFile(filepath.Join(reports, "version"), []byte("NVRM version: 595.71.05\n"), 0o644); err != nil {
		t.Fatalf("write the stand-in driver report: %v", err)
	}
	return root
}

// wedgedVendorTool is an nvidia-smi that never answers and leaves something
// behind that outlives it. The background sleep inherits this script's stdout
// pipe, so killing the script does not close the pipe and os/exec's own read
// keeps waiting: this is the shape of a vendor tool that has to be walked away
// from rather than waited out, and it is arrangeable, where a process in an
// uninterruptible ioctl on /dev/nvidiactl is not.
const wedgedVendorTool = `#!/bin/sh
sleep 600 &
exec sleep 600
`

// refusalsForADriver is what Placement would say about this report to a Run that
// will not run without an NVIDIA driver, asked of the domain rule Placement and
// the Lab both ask, so a case states the refusal an operator reads rather than
// the fields behind it.
func refusalsForADriver(facts capability.NodeFacts) []domain.Violation {
	published := domain.HostFacts{
		Attested: facts.Host.Accelerator.Attestations(),
		Driver:   facts.Host.Accelerator.Driver(),
	}
	return published.Violations(domain.HostRequirements{Facts: []domain.HostFact{domain.HostFactNvidiaDriver}})
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
