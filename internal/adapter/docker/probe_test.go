package docker

import "testing"

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
	raw := []byte(`{"Architecture":"aarch64","OSType":"linux","NCPU":10,"MemTotal":8222068736,"ServerVersion":"29.4.0","Name":"orbstack"}`)
	info, err := parseDockerInfo(raw)
	if err != nil {
		t.Fatalf("parseDockerInfo: %v", err)
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

func TestParseNvidiaSMIInventoryCanonicalizesSingleGPU(t *testing.T) {
	// Shape emitted by `nvidia-smi --query-gpu=name,memory.total
	// --format=csv,noheader,nounits` inside the probe container (memory in MiB).
	inventory, err := parseNvidiaSMIInventory("NVIDIA GeForce RTX 5090, 32607\n")
	if err != nil {
		t.Fatalf("parseNvidiaSMIInventory: %v", err)
	}
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
	if want := int64(32607) * 1024 * 1024; gpu.MemoryBytes != want {
		t.Errorf("MemoryBytes = %d, want %d (MiB * 1024 * 1024)", gpu.MemoryBytes, want)
	}
}

func TestParseNvidiaSMIInventoryGroupsIdenticalGPUs(t *testing.T) {
	output := "NVIDIA H100 80GB HBM3, 81559\n" +
		"NVIDIA H100 80GB HBM3, 81559\n" +
		"NVIDIA GeForce RTX 4090, 24564\n"
	inventory, err := parseNvidiaSMIInventory(output)
	if err != nil {
		t.Fatalf("parseNvidiaSMIInventory: %v", err)
	}
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

func TestParseNvidiaSMIInventoryRejectsGarbageAndEmpty(t *testing.T) {
	if _, err := parseNvidiaSMIInventory("Failed to initialize NVML: Driver/library version mismatch\n"); err == nil {
		t.Fatal("expected error parsing nvidia-smi failure output")
	}
	if _, err := parseNvidiaSMIInventory(""); err == nil {
		t.Fatal("expected error for empty nvidia-smi output (a probe that reports nothing is not a CPU-only fact)")
	}
}
