package gpunorm

import "testing"

func TestCanonicalConsolidatesProviderSpellings(t *testing.T) {
	cases := []struct {
		vendor string
		model  string
		want   string
	}{
		// Different provider spellings of the same GPU collapse to one id.
		{"NVIDIA", "RTX A2000", "nvidia-rtx-a2000"},
		{"NVIDIA", "NVIDIA RTX A2000", "nvidia-rtx-a2000"},
		{"nvidia", "A2000", "nvidia-rtx-a2000"},
		{"NVIDIA", "RTX A4000", "nvidia-rtx-a4000"},
		// Memory variants of the same model share one id (memory is matched
		// separately via MemoryMinBytes).
		{"NVIDIA", "A100", "nvidia-a100"},
		{"NVIDIA", "A100 80GB PCIe", "nvidia-a100"},
		{"NVIDIA", "A100-SXM", "nvidia-a100"},
		{"NVIDIA", "H100", "nvidia-h100"},
		{"NVIDIA", "H100 NVL", "nvidia-h100"},
		// nvidia-smi marketing names (docker GPU probe) collapse onto the same
		// id as the cloud-provider spellings.
		{"NVIDIA", "NVIDIA GeForce RTX 5090", "nvidia-rtx-5090"},
		{"NVIDIA", "RTX 5090", "nvidia-rtx-5090"},
		{"NVIDIA", "NVIDIA GeForce RTX 4090", "nvidia-rtx-4090"},
		{"NVIDIA", "NVIDIA H100 80GB HBM3", "nvidia-h100"},
		{"NVIDIA", "NVIDIA A100-SXM4-80GB", "nvidia-a100"},
	}
	for _, c := range cases {
		if got := Canonical(c.vendor, c.model); got != c.want {
			t.Errorf("Canonical(%q,%q) = %q, want %q", c.vendor, c.model, got, c.want)
		}
	}
}

func TestCanonicalUnknownGPUFallsBackToDeterministicSlug(t *testing.T) {
	// An unseeded GPU still resolves to a stable, matchable id, never an error.
	got := Canonical("NVIDIA", "RTX 6000 Ada Generation")
	if got != "nvidia-rtx-6000-ada-generation" {
		t.Fatalf("unknown GPU canonical = %q", got)
	}
	if Canonical("NVIDIA", "RTX 6000 Ada Generation") != got {
		t.Fatal("Canonical must be deterministic")
	}
}

func TestNormalizeVendor(t *testing.T) {
	cases := map[string]string{
		"NVIDIA":                 "nvidia",
		"Nvidia":                 "nvidia",
		"nvidia":                 "nvidia",
		"NVIDIA Corporation":     "nvidia",
		"AMD":                    "amd",
		"Advanced Micro Devices": "amd",
		"Intel":                  "intel",
		"SomeNewVendor":          "somenewvendor",
	}
	for in, want := range cases {
		if got := NormalizeVendor(in); got != want {
			t.Errorf("NormalizeVendor(%q) = %q, want %q", in, got, want)
		}
	}
}
