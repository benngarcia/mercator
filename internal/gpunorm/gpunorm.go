// Package gpunorm maps each provider's native GPU vendor/model strings onto a
// stable canonical id, so a workload's accelerator requirement matches the same
// GPU regardless of which provider advertises it. The canonical model id is
// "<vendor>-<model>" in kebab-case, e.g. "nvidia-a6000".
//
// The canonical vocabulary is anchored to Shadeform's cross-cloud catalog:
// when Shadeform lists a card, the canonical model part is the slug of their
// identifier (they already did the work of standardizing names across
// providers, so our ids stay copyable from their catalog — e.g. their
// "A6000", not NVIDIA's "RTX A6000"). A card absent from their catalog
// (e.g. RTX 5090) canonicalizes to the slug of its NVIDIA marketing name.
//
// Granularity is model-level: memory/SKU variants of one marketing model share
// a single id (e.g. A100 40GB and 80GB both map to "nvidia-a100"); callers
// distinguish them via AcceleratorRequirement.MemoryMinBytes. That second half
// of the identity has to survive the same trip across publishers as the first,
// which is what CardMemoryBytes is for.
package gpunorm

import (
	"regexp"
	"strings"
)

const mebibyte = int64(1) << 20
const gibibyte = int64(1) << 30

// soldCapacitiesGiB is every capacity an NVIDIA part is sold with, smallest
// first, as the number a listing publishes: a card advertised as "16GB" is
// matched against a floor of 16 GiB, because that is the number a caller who
// copied the listing wrote down.
//
// It is a list rather than an arithmetic rule because the gap between what a
// card is sold with and what its framebuffer measures is not one width. A
// driver reserve costs a few hundred mebibytes; ECC on the datacenter parts
// that carry it out of band costs a sixteenth of the board, which is a whole
// gibibyte on a T4 and a gibibyte and a half on an L4. Rounding to the next
// gibibyte covered the first and left the second, so a T4 sold as 16GB was
// published as 15 GiB and struck out RESOURCE_INSUFFICIENT by the floor its own
// listing taught the caller to write.
var soldCapacitiesGiB = []int64{4, 6, 8, 10, 11, 12, 16, 20, 24, 32, 40, 48, 64, 80, 94, 96, 128, 141, 180, 192, 288}

// soldCapacityHeadroom bounds how far above a measurement this package will
// reach for the capacity a card is sold with, as one part in this many. An
// eighth is more than the sixteenth ECC holds back plus the driver's own
// reserve, and it is the bound rather than an unbounded reach because a part
// this list has never heard of must not be restated as the next part up: a
// measurement of 33 GiB is a card nobody here knows, and publishing it as the
// 40 GiB entry beside it would admit a Run onto a fifth less memory than it
// asked for.
const soldCapacityHeadroom = 8

// CardMemoryBytes states the memory of a card whose framebuffer a vendor tool
// measured, in the unit the marketplaces publish the same card in.
//
// The two numbers are not the same number and never were. A marketplace lists
// the capacity a card is sold with, and `nvidia-smi --query-gpu=memory.total`
// reports the framebuffer left after the driver and ECC have held back their
// own reserved regions: this workstation's RTX 5090 is sold as 32GB and
// measures 32607MiB, an H100 sold as 80GB measures 81559MiB, and a T4 sold as
// 16GB measures 15360MiB. Published raw, the measurement is under every floor a
// caller copied out of a listing, so the same physical card clears the floor
// while a provider is renting it and is struck out RESOURCE_INSUFFICIENT the
// moment Mercator enrolls it.
//
// The conversion is to the capacity the part is sold with, which is what the
// list above holds, and it is bounded: a measurement with no sold capacity
// within an eighth of it is published at the whole gibibyte, because a part
// this package has never heard of is better stated a little low than restated
// as a bigger card. It is deliberately not a tolerance in the comparison, which
// would loosen every floor including the ones a caller wrote against a real
// measurement; this restates one publisher's number in the other's unit, once,
// where the measurement is read.
//
// A partition is not a card and is left as measured. A MIG instance is sold in
// slices of a board, and its name says so; rounding one up published a
// 1g.10gb instance measuring 9856MiB as a 10 GiB device, which admits a Run
// that asked for 10 GiB onto less than it asked for and kills it on capacity
// Mercator already paid for.
func CardMemoryBytes(model string, framebufferMiB int64) int64 {
	if framebufferMiB <= 0 {
		return 0
	}
	measured := framebufferMiB * mebibyte
	if isPartition(model) {
		return measured
	}
	for _, capacity := range soldCapacitiesGiB {
		sold := capacity * gibibyte
		if sold >= measured && (sold-measured)*soldCapacityHeadroom <= sold {
			return sold
		}
	}
	return (measured + gibibyte - 1) / gibibyte * gibibyte
}

// isPartition reports whether this is a slice of a board rather than a board.
// nvidia-smi names a MIG instance after the profile it was cut to, so the name
// is where the distinction is stated, and it is the only place it is stated:
// the memory total alone cannot tell a partition from a small card.
func isPartition(model string) bool {
	return strings.Contains(strings.ToUpper(model), "MIG ")
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// slug lowercases s and collapses every run of non-alphanumeric characters into
// a single hyphen, trimming leading/trailing hyphens.
func slug(s string) string {
	s = nonAlnum.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "-")
	return strings.Trim(s, "-")
}

// vendorAliases maps known vendor spellings to a canonical vendor token.
var vendorAliases = map[string]string{
	"nvidia":                 "nvidia",
	"nvidia corporation":     "nvidia",
	"amd":                    "amd",
	"advanced micro devices": "amd",
	"radeon":                 "amd",
	"intel":                  "intel",
}

// NormalizeVendor returns a canonical vendor token (e.g. "nvidia"). Unknown
// vendors fall back to their slug so matching stays deterministic.
func NormalizeVendor(vendor string) string {
	if v, ok := vendorAliases[strings.ToLower(strings.TrimSpace(vendor))]; ok {
		return v
	}
	return slug(vendor)
}

// modelAliases maps a normalized model key (the model slug with any leading
// vendor prefix stripped) to the canonical model part. It consolidates provider
// spellings and memory/SKU variants onto one id.
var modelAliases = map[string]string{
	// Ampere workstation cards: Shadeform names these bare, without NVIDIA's
	// RTX marketing prefix.
	"a2000": "a2000", "rtx-a2000": "a2000", "rtx-a2000-6gb": "a2000",
	"a4000": "a4000", "rtx-a4000": "a4000",
	"a5000": "a5000", "rtx-a5000": "a5000",
	"a6000": "a6000", "rtx-a6000": "a6000",
	"a16":  "a16",
	"a30":  "a30",
	"a40":  "a40",
	"a100": "a100", "a100-pcie": "a100", "a100-sxm": "a100", "a100-sxm4": "a100",
	"a100-40gb": "a100", "a100-80gb": "a100", "a100-80gb-pcie": "a100",
	// Shadeform's API spelling of the 80GB variant (A100_80G).
	"a100-80g": "a100",
	// nvidia-smi spellings (as the docker adapter's GPU probe reports them).
	"a100-sxm4-40gb": "a100", "a100-sxm4-80gb": "a100",
	"a100-pcie-40gb": "a100", "a100-pcie-80gb": "a100",
	"h100": "h100", "h100-pcie": "h100", "h100-sxm": "h100", "h100-sxm5": "h100",
	"h100-nvl": "h100", "h100-80gb": "h100", "h100-80gb-hbm3": "h100",
	"h200":  "h200",
	"gh200": "gh200",
	"b200":  "b200",
	"b300":  "b300",
	"l4":    "l4",
	"l40":   "l40", "l40s": "l40s",
	"t4":   "t4",
	"v100": "v100", "v100-sxm2": "v100", "v100-32gb": "v100",
	"a10": "a10", "a10g": "a10g",
	"4090": "rtx-4090", "rtx-4090": "rtx-4090", "rtx4090": "rtx-4090",
	"3090": "rtx-3090", "rtx-3090": "rtx-3090", "rtx3090": "rtx-3090",
	"5090": "rtx-5090", "rtx-5090": "rtx-5090", "rtx5090": "rtx-5090",
	"5080": "rtx-5080", "rtx-5080": "rtx-5080", "rtx5080": "rtx-5080",
	// Workstation Ada/Blackwell cards. Providers spell these with and without
	// separators and with SKU-edition suffixes; every spelling of one
	// marketing model lands on one id.
	"rtx-4000-ada": "rtx-4000-ada", "4000-ada": "rtx-4000-ada",
	"rtx4000ada": "rtx-4000-ada", "rtx-4000-ada-generation": "rtx-4000-ada",
	"rtx-6000-ada": "rtx-6000-ada", "6000-ada": "rtx-6000-ada",
	"rtx6000ada": "rtx-6000-ada", "rtx-6000ada": "rtx-6000-ada",
	"rtx-6000-ada-generation": "rtx-6000-ada",
	// The pre-Ada Quadro RTX 6000 is a distinct card from the RTX 6000 Ada.
	"rtx-6000": "rtx-6000", "quadro-rtx-6000": "rtx-6000",
	"rtx-pro-6000":           "rtx-pro-6000",
	"pro-6000":               "rtx-pro-6000",
	"rtxpro6000":             "rtx-pro-6000",
	"pro6000":                "rtx-pro-6000",
	"rtx-pro-6000-blackwell": "rtx-pro-6000",
	"rtx-pro-6000-blackwell-workstation-edition":       "rtx-pro-6000",
	"rtx-pro-6000-blackwell-server-edition":            "rtx-pro-6000",
	"rtx-pro-6000-blackwell-max-q-workstation-edition": "rtx-pro-6000",
	"rtx-pro-6000-max-q":                               "rtx-pro-6000",
}

// canonicalModelPart resolves the canonical model token for (vendor, model).
// The "geforce" marketing prefix is stripped: nvidia-smi reports consumer
// cards as e.g. "NVIDIA GeForce RTX 5090" while cloud providers list the same
// GPU as "RTX 5090", and both must land on one canonical id.
func canonicalModelPart(vendor, model string) string {
	key := strings.TrimPrefix(slug(model), NormalizeVendor(vendor)+"-")
	key = strings.TrimPrefix(key, "geforce-")
	if c, ok := modelAliases[key]; ok {
		return c
	}
	return key
}

// Canonical returns the canonical GPU id "<vendor>-<model>" (e.g.
// "nvidia-a6000"). An unseeded GPU resolves to a stable slug rather than
// failing, so matching still works within a provider.
func Canonical(vendor, model string) string {
	cv := NormalizeVendor(vendor)
	part := canonicalModelPart(vendor, model)
	if part == "" {
		return cv
	}
	return cv + "-" + part
}
