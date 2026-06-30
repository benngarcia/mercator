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
