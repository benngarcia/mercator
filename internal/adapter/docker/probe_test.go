package docker

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
)

func TestNormalizeArchMapsDockerArchToOCI(t *testing.T) {
	cases := map[string]string{
		"aarch64": "arm64",
		"x86_64":  "amd64",
		"arm64":   "arm64",
		"amd64":   "amd64",
		"ppc64le": "ppc64le", // unknown: pass through unchanged
		"":        "",        // empty: caller decides the default
	}
	for input, want := range cases {
		if got := normalizeArch(input); got != want {
			t.Errorf("normalizeArch(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestParseDockerInfoExtractsHostFacts(t *testing.T) {
	// Shape emitted by `docker info --format '{{json .}}'`.
	raw := []byte(`{"ID":"7a9d0c1e-2b34-4f56-8a90-1b2c3d4e5f60","Architecture":"aarch64","OSType":"linux","NCPU":10,"MemTotal":8222068736,"ServerVersion":"29.4.0","Name":"orbstack"}`)
	info, err := parseDockerInfo(raw)
	if err != nil {
		t.Fatalf("parseDockerInfo: %v", err)
	}
	if info.ID != "7a9d0c1e-2b34-4f56-8a90-1b2c3d4e5f60" {
		t.Errorf("ID = %q, want the daemon's own identifier", info.ID)
	}
	if info.Architecture != "aarch64" {
		t.Errorf("Architecture = %q, want aarch64", info.Architecture)
	}
	if info.OSType != "linux" {
		t.Errorf("OSType = %q, want linux", info.OSType)
	}
	if info.NCPU != 10 {
		t.Errorf("NCPU = %d, want 10", info.NCPU)
	}
	if info.MemTotalBytes != 8222068736 {
		t.Errorf("MemTotalBytes = %d, want 8222068736", info.MemTotalBytes)
	}
	if info.ServerVersion != "29.4.0" {
		t.Errorf("ServerVersion = %q, want 29.4.0", info.ServerVersion)
	}
	if info.Name != "orbstack" {
		t.Errorf("Name = %q, want orbstack", info.Name)
	}
}

func TestParseDockerInfoRejectsGarbage(t *testing.T) {
	if _, err := parseDockerInfo([]byte("not json")); err == nil {
		t.Fatal("expected error parsing non-JSON docker info output")
	}
}

func TestParseDFAvailableBytesReadsRootMount(t *testing.T) {
	// Shape emitted by busybox `df -Pk /` inside the probe container.
	output := "Filesystem           1024-blocks    Used Available Capacity Mounted on\n" +
		"overlay              494384795 123456789 345678901  27% /\n"
	got, err := parseDFAvailableBytes(output)
	if err != nil {
		t.Fatalf("parseDFAvailableBytes: %v", err)
	}
	if want := int64(345678901) * 1024; got != want {
		t.Errorf("available = %d, want %d (Available KiB * 1024)", got, want)
	}
}

func TestParseDFAvailableBytesIgnoresNonRootMounts(t *testing.T) {
	output := "Filesystem           1024-blocks    Used Available Capacity Mounted on\n" +
		"tmpfs                    65536        0     65536   0% /dev\n" +
		"overlay              100000000 40000000  60000000  40% /\n"
	got, err := parseDFAvailableBytes(output)
	if err != nil {
		t.Fatalf("parseDFAvailableBytes: %v", err)
	}
	if want := int64(60000000) * 1024; got != want {
		t.Errorf("available = %d, want %d (root mount only)", got, want)
	}
}

func TestParseDFAvailableBytesRejectsGarbage(t *testing.T) {
	if _, err := parseDFAvailableBytes("Unable to find image 'busybox:1.37' locally"); err == nil {
		t.Fatal("expected error parsing df output with no root filesystem line")
	}
}

func TestGlobalArgsCarryEndpoint(t *testing.T) {
	if got := (&CLIClient{}).globalArgs(); len(got) != 0 {
		t.Errorf("no endpoint configured: globalArgs = %v, want empty", got)
	}
	if got := (&CLIClient{Host: "ssh://user@dockerhost"}).globalArgs(); len(got) != 2 || got[0] != "--host" || got[1] != "ssh://user@dockerhost" {
		t.Errorf("host endpoint: globalArgs = %v, want [--host ssh://user@dockerhost]", got)
	}
	if got := (&CLIClient{Context: "dockerhost"}).globalArgs(); len(got) != 2 || got[0] != "--context" || got[1] != "dockerhost" {
		t.Errorf("context endpoint: globalArgs = %v, want [--context dockerhost]", got)
	}
	// Context wins over Host (docker treats them as mutually exclusive).
	if got := (&CLIClient{Host: "tcp://x:2375", Context: "dockerhost"}).globalArgs(); got[0] != "--context" {
		t.Errorf("context should win over host, got %v", got)
	}
}

func TestParseDockerInfoExtractsRuntimes(t *testing.T) {
	// A GPU host provisioned with nvidia-container-toolkit registers the
	// "nvidia" runtime alongside the defaults.
	raw := []byte(`{"Architecture":"x86_64","OSType":"linux","NCPU":16,"MemTotal":68719476736,"Runtimes":{"io.containerd.runc.v2":{"path":"runc"},"nvidia":{"path":"nvidia-container-runtime"},"runc":{"path":"runc"}}}`)
	info, err := parseDockerInfo(raw)
	if err != nil {
		t.Fatalf("parseDockerInfo: %v", err)
	}
	if len(info.Runtimes) != 3 {
		t.Fatalf("Runtimes = %v, want 3 sorted names", info.Runtimes)
	}
	if !info.HasNvidiaRuntime() {
		t.Error("HasNvidiaRuntime() = false for a daemon with the nvidia runtime")
	}
}

func TestHasNvidiaRuntimeFalseForCPUOnlyDaemon(t *testing.T) {
	raw := []byte(`{"Architecture":"x86_64","Runtimes":{"io.containerd.runc.v2":{"path":"runc"},"runc":{"path":"runc"}}}`)
	info, err := parseDockerInfo(raw)
	if err != nil {
		t.Fatalf("parseDockerInfo: %v", err)
	}
	if info.HasNvidiaRuntime() {
		t.Error("HasNvidiaRuntime() = true for a CPU-only daemon")
	}
	if (HostInfo{}).HasNvidiaRuntime() {
		t.Error("HasNvidiaRuntime() = true for an empty (failed-probe) HostInfo")
	}
}

func TestParseNvidiaSMIFactsCanonicalizesSingleGPU(t *testing.T) {
	// Shape emitted by `nvidia-smi --query-gpu=name,memory.total,driver_version
	// --format=csv,noheader,nounits` inside the probe container (memory in MiB).
	facts, err := parseNvidiaSMIFacts("NVIDIA GeForce RTX 5090, 32607, 595.71.05\n")
	if err != nil {
		t.Fatalf("parseNvidiaSMIFacts: %v", err)
	}
	if !facts.Established || facts.DriverVersion != "595.71.05" {
		t.Errorf("a probe that listed a card reported %+v", facts)
	}
	inventory := facts.Devices
	if len(inventory) != 1 {
		t.Fatalf("inventory = %+v, want one entry", inventory)
	}
	gpu := inventory[0]
	if gpu.Vendor != "NVIDIA" || gpu.Model != "NVIDIA GeForce RTX 5090" || gpu.Count != 1 {
		t.Errorf("unexpected inventory entry: %+v", gpu)
	}
	if gpu.CanonicalModel != "nvidia-rtx-5090" {
		t.Errorf("CanonicalModel = %q, want nvidia-rtx-5090 (matches the runpod spelling)", gpu.CanonicalModel)
	}
	// The capacity the card is sold with, which is what a caller's memory floor
	// is copied out of a marketplace listing in. The framebuffer nvidia-smi
	// measured is 32607MiB, a few hundred mebibytes under it, and published raw
	// it strikes this card out of a floor written for this card.
	if want := int64(32) << 30; gpu.MemoryBytes != want {
		t.Errorf("MemoryBytes = %d, want %d (the 32GB this card is listed at)", gpu.MemoryBytes, want)
	}
}

func TestParseNvidiaSMIFactsGroupsIdenticalGPUs(t *testing.T) {
	output := "NVIDIA H100 80GB HBM3, 81559, 595.71.05\n" +
		"NVIDIA H100 80GB HBM3, 81559, 595.71.05\n" +
		"NVIDIA GeForce RTX 4090, 24564, 595.71.05\n"
	facts, err := parseNvidiaSMIFacts(output)
	if err != nil {
		t.Fatalf("parseNvidiaSMIFacts: %v", err)
	}
	inventory := facts.Devices
	if len(inventory) != 2 {
		t.Fatalf("inventory = %+v, want two grouped entries", inventory)
	}
	if inventory[0].CanonicalModel != "nvidia-h100" || inventory[0].Count != 2 {
		t.Errorf("H100 pair should group: %+v", inventory[0])
	}
	if inventory[1].CanonicalModel != "nvidia-rtx-4090" || inventory[1].Count != 1 {
		t.Errorf("4090 entry: %+v", inventory[1])
	}
}

func TestParseNvidiaSMIFactsRejectsGarbageAndEmpty(t *testing.T) {
	if _, err := parseNvidiaSMIFacts("Failed to initialize NVML: Driver/library version mismatch\n"); err == nil {
		t.Fatal("expected error parsing nvidia-smi failure output")
	}
	if _, err := parseNvidiaSMIFacts(""); err == nil {
		t.Fatal("expected error for empty nvidia-smi output (a probe that reports nothing is not a CPU-only fact)")
	}
}

// TestIntegrationThisEndpointCountsItsOwnCardsAndNamesTheirDriver is the live
// half of the GPU probe, against the daemon and the cards this suite is running
// on.
//
// It is the case that would have caught the probe image. The NVIDIA container
// runtime injects the host's own nvidia-smi and driver libraries into the
// container it starts, and those are linked against glibc, so the probe run
// inside busybox died with "error while loading shared libraries: libdl.so.2" on
// every endpoint there has ever been. Nothing in the unit cases could see it:
// they parse the output a working probe would have printed.
//
// The driver is asserted beside the cards because the offer now carries it. A
// container that enumerated a card proves a loaded driver on the daemon's host,
// which is what makes this endpoint answer a Run declaring facts:
// ["nvidia_driver"] instead of sending its operator to go and look.
func TestIntegrationThisEndpointCountsItsOwnCardsAndNamesTheirDriver(t *testing.T) {
	if os.Getenv("MERCATOR_DOCKER_INTEGRATION") != "1" {
		t.Skip("set MERCATOR_DOCKER_INTEGRATION=1 to run live Docker adapter integration")
	}
	client := NewCLIClient("")
	info, err := client.Info(context.Background())
	if err != nil {
		t.Fatalf("live docker info: %v", err)
	}
	if !info.HasNvidiaRuntime() {
		t.Skip("this daemon registers no NVIDIA runtime, so no container it starts can be handed a card")
	}

	facts, err := client.AcceleratorFacts(context.Background())

	if err != nil {
		t.Fatalf("live GPU probe: %v", err)
	}
	if !facts.Established || facts.DriverVersion == "" {
		t.Fatalf("a probe that ran against this host's cards reported %+v", facts)
	}
	cards := 0
	for _, device := range facts.Devices {
		if device.MemoryBytes%(1<<30) != 0 {
			t.Errorf("this endpoint offers %s with %d bytes, which is not the whole gibibytes a listing publishes the same card in", device.Model, device.MemoryBytes)
		}
		cards += device.Count
	}
	if cards == 0 {
		t.Fatal("a probe that answered listed no cards on a daemon with the NVIDIA runtime")
	}
	needsADriver := domain.HostRequirements{Facts: []domain.HostFact{domain.HostFactNvidiaDriver}}
	offer := StandingOffer(DeriveIdentity("", ""), "", info, 0, facts, time.Now().UTC())
	if refusals := offer.Host.Violations(needsADriver); len(refusals) != 0 {
		t.Fatalf("an endpoint running driver %s was refused %+v", facts.DriverVersion, refusals)
	}
	t.Logf("this endpoint offers %d card(s) on driver %s: %+v", cards, facts.DriverVersion, facts.Devices)
}
