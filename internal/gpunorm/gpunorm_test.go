package gpunorm

import "testing"

func TestCanonicalConsolidatesProviderSpellings(t *testing.T) {
	cases := []struct {
		vendor string
		model  string
		want   string
	}{
		// Different provider spellings of the same GPU collapse to one id —
		// the Shadeform catalog identifier (bare, no RTX marketing prefix).
		{"NVIDIA", "RTX A2000", "nvidia-a2000"},
		{"NVIDIA", "NVIDIA RTX A2000", "nvidia-a2000"},
		{"nvidia", "A2000", "nvidia-a2000"},
		{"NVIDIA", "RTX A4000", "nvidia-a4000"},
		{"NVIDIA", "RTX A6000", "nvidia-a6000"},
		{"NVIDIA", "A6000", "nvidia-a6000"},
		// Memory variants of the same model share one id (memory is matched
		// separately via MemoryMinBytes).
		{"NVIDIA", "A100", "nvidia-a100"},
		{"NVIDIA", "A100 80GB PCIe", "nvidia-a100"},
		{"NVIDIA", "A100-SXM", "nvidia-a100"},
		{"NVIDIA", "A100_80G", "nvidia-a100"},
		{"NVIDIA", "V100 32GB", "nvidia-v100"},
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
		{"NVIDIA", "RTX 4000 Ada", "nvidia-rtx-4000-ada"},
		// The pre-Ada Quadro RTX 6000 stays distinct from the RTX 6000 Ada.
		{"NVIDIA", "Quadro RTX 6000", "nvidia-rtx-6000"},
		{"NVIDIA", "RTX 6000", "nvidia-rtx-6000"},
		{"NVIDIA", "B200", "nvidia-b200"},
		{"NVIDIA", "GH200", "nvidia-gh200"},
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

// TestCardMemoryBytesStatesTheCapacityACardIsSoldWith is the second half of a
// card's identity surviving the trip between publishers. A marketplace lists the
// capacity the card is sold with and a vendor tool measures the framebuffer left
// after the driver's reserved region, so a caller who copies memory_min_bytes
// out of a listing was writing a floor the very same card fails once Mercator
// enrolls it and measures for itself.
func TestCardMemoryBytesStatesTheCapacityACardIsSoldWith(t *testing.T) {
	for name, card := range map[string]struct {
		model          string
		framebufferMiB int64
		listedAs       int64
	}{
		// Every pair here is a real `nvidia-smi --query-gpu=memory.total` figure
		// beside what the marketplaces publish for the same part.
		"an RTX 5090 sold as 32GB":  {model: "NVIDIA GeForce RTX 5090", framebufferMiB: 32607, listedAs: 32 << 30},
		"an RTX 4090 sold as 24GB":  {model: "NVIDIA GeForce RTX 4090", framebufferMiB: 24564, listedAs: 24 << 30},
		"an H100 SXM sold as 80GB":  {model: "NVIDIA H100 80GB HBM3", framebufferMiB: 81559, listedAs: 80 << 30},
		"an H100 PCIe sold as 80GB": {model: "NVIDIA H100 PCIe", framebufferMiB: 81008, listedAs: 80 << 30},
		"an A100 sold as 80GB":      {model: "NVIDIA A100-SXM4-80GB", framebufferMiB: 81920, listedAs: 80 << 30},
		"an A100 sold as 40GB":      {model: "NVIDIA A100-SXM4-40GB", framebufferMiB: 40960, listedAs: 40 << 30},
		// The parts that hold ECC out of band give up a sixteenth of the board,
		// which is a whole gibibyte and more. Rounding to the next gibibyte covered
		// the reserve on the cards above and left every one of these a gibibyte
		// short of the floor its own listing taught a caller to write.
		"a T4 sold as 16GB":             {model: "Tesla T4", framebufferMiB: 15360, listedAs: 16 << 30},
		"an L4 sold as 24GB":            {model: "NVIDIA L4", framebufferMiB: 23034, listedAs: 24 << 30},
		"an A10G sold as 24GB":          {model: "NVIDIA A10G", framebufferMiB: 23028, listedAs: 24 << 30},
		"an L40S sold as 48GB":          {model: "NVIDIA L40S", framebufferMiB: 46068, listedAs: 48 << 30},
		"a machine that counted no MiB": {model: "NVIDIA A100-SXM4-80GB", framebufferMiB: 0, listedAs: 0},
		// A partition is not a card. A MIG instance is a slice of a board sold in
		// slices, and stating it at the size the slice is named after admits a Run
		// onto less memory than it asked for.
		"an A100 partitioned into 1g.10gb": {model: "NVIDIA A100-SXM4-40GB MIG 1g.10gb", framebufferMiB: 9856, listedAs: 9856 << 20},
		// A part this package has never heard of is stated a little low rather
		// than restated as the next size up, which would be a different card.
		"a card nobody here has heard of": {model: "NVIDIA Q900", framebufferMiB: 33500, listedAs: 33 << 30},
	} {
		t.Run(name, func(t *testing.T) {
			if got := CardMemoryBytes(card.model, card.framebufferMiB); got != card.listedAs {
				t.Errorf("CardMemoryBytes(%q, %d MiB) = %d, want %d", card.model, card.framebufferMiB, got, card.listedAs)
			}
		})
	}
}
