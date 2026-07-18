// Package gpunorm maps each provider's native GPU vendor/model strings onto a
// stable canonical id, so a workload's accelerator requirement matches the same
// GPU regardless of which provider advertises it. The canonical model id is
// "<vendor>-<model>" in kebab-case, e.g. "nvidia-rtx-a2000".
//
// Granularity is model-level: memory/SKU variants of one marketing model share
// a single id (e.g. A100 40GB and 80GB both map to "nvidia-a100"); callers
// distinguish them via AcceleratorRequirement.MemoryMinBytes.
package gpunorm

import (
	"regexp"
	"strings"
)

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
	"a2000": "rtx-a2000", "rtx-a2000": "rtx-a2000", "rtx-a2000-6gb": "rtx-a2000",
	"a4000": "rtx-a4000", "rtx-a4000": "rtx-a4000",
	"a5000": "rtx-a5000", "rtx-a5000": "rtx-a5000",
	"a6000": "rtx-a6000", "rtx-a6000": "rtx-a6000",
	"a40":  "a40",
	"a100": "a100", "a100-pcie": "a100", "a100-sxm": "a100", "a100-sxm4": "a100",
	"a100-40gb": "a100", "a100-80gb": "a100", "a100-80gb-pcie": "a100",
	// nvidia-smi spellings (as the docker adapter's GPU probe reports them).
	"a100-sxm4-40gb": "a100", "a100-sxm4-80gb": "a100",
	"a100-pcie-40gb": "a100", "a100-pcie-80gb": "a100",
	"h100": "h100", "h100-pcie": "h100", "h100-sxm": "h100", "h100-sxm5": "h100",
	"h100-nvl": "h100", "h100-80gb": "h100", "h100-80gb-hbm3": "h100",
	"h200": "h200",
	"l4":   "l4",
	"l40":  "l40", "l40s": "l40s",
	"t4":   "t4",
	"v100": "v100", "v100-sxm2": "v100",
	"a10": "a10", "a10g": "a10g",
	"4090": "rtx-4090", "rtx-4090": "rtx-4090",
	"3090": "rtx-3090", "rtx-3090": "rtx-3090",
	"5090": "rtx-5090", "rtx-5090": "rtx-5090",
	"5080": "rtx-5080", "rtx-5080": "rtx-5080",
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
// "nvidia-rtx-a2000"). An unseeded GPU resolves to a stable slug rather than
// failing, so matching still works within a provider.
func Canonical(vendor, model string) string {
	cv := NormalizeVendor(vendor)
	part := canonicalModelPart(vendor, model)
	if part == "" {
		return cv
	}
	return cv + "-" + part
}
