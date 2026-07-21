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
		// Separator-free provider spellings collapse onto the dashed id.
		{"NVIDIA", "RTX5090", "nvidia-rtx-5090"},
		{"NVIDIA", "RTX4090", "nvidia-rtx-4090"},
		// Workstation 6000-class cards: Ada and Blackwell generations keep
		// distinct ids, while SKU-edition suffixes collapse within each.
		{"NVIDIA", "RTX 6000 Ada", "nvidia-rtx-6000-ada"},
		{"NVIDIA", "RTX6000Ada", "nvidia-rtx-6000-ada"},
		{"NVIDIA", "RTX 6000 Ada Generation", "nvidia-rtx-6000-ada"},
		{"NVIDIA", "RTX PRO 6000", "nvidia-rtx-pro-6000"},
		{"NVIDIA", "RTXPRO6000", "nvidia-rtx-pro-6000"},
		{"NVIDIA", "RTX PRO 6000 Blackwell", "nvidia-rtx-pro-6000"},
		{"NVIDIA", "NVIDIA RTX PRO 6000 Blackwell Workstation Edition", "nvidia-rtx-pro-6000"},
		{"NVIDIA", "RTX PRO 6000 Blackwell Server Edition", "nvidia-rtx-pro-6000"},
	}
	for _, c := range cases {
		if got := Canonical(c.vendor, c.model); got != c.want {
			t.Errorf("Canonical(%q,%q) = %q, want %q", c.vendor, c.model, got, c.want)
		}
	}
}

func TestCanonicalUnknownGPUFallsBackToDeterministicSlug(t *testing.T) {
	// An unseeded GPU still resolves to a stable, matchable id, never an error.
	got := Canonical("NVIDIA", "RTX 9000 Rubin")
	if got != "nvidia-rtx-9000-rubin" {
		t.Fatalf("unknown GPU canonical = %q", got)
	}
	if Canonical("NVIDIA", "RTX 9000 Rubin") != got {
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
