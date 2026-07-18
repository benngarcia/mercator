package vast

import (
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
)

func pricePtr(v float64) *float64 { return &v }

func TestSecureOfferQueryHardCodesTheSecureTier(t *testing.T) {
	q := secureOfferQuery([]string{"RTX 4090"}, 2, 75, 20)
	if got := q["verification"].(map[string]any)["eq"]; got != "verified" {
		t.Errorf("verification filter = %v", got)
	}
	if got := q["datacenter"].(map[string]any)["eq"]; got != true {
		t.Errorf("datacenter filter = %v", got)
	}
	if got := q["external"].(map[string]any)["eq"]; got != false {
		t.Errorf("external filter = %v", got)
	}
	if got := q["type"]; got != "ondemand" {
		t.Errorf("type = %v", got)
	}
	if got := q["num_gpus"].(map[string]any)["eq"]; got != 2 {
		t.Errorf("num_gpus = %v", got)
	}
	if got := q["allocated_storage"]; got != float64(75) {
		t.Errorf("allocated_storage = %v", got)
	}
	if got := q["gpu_name"].(map[string]any)["in"].([]string); len(got) != 1 || got[0] != "RTX 4090" {
		t.Errorf("gpu_name = %v", got)
	}
}

func TestSecureAskQueryPinsIdAndTier(t *testing.T) {
	q := secureAskQuery(42)
	if got := q["id"].(map[string]any)["eq"]; got != int64(42) {
		t.Errorf("id filter = %v", got)
	}
	if got := q["verification"].(map[string]any)["eq"]; got != "verified" {
		t.Errorf("verification filter = %v", got)
	}
	if got := q["datacenter"].(map[string]any)["eq"]; got != true {
		t.Errorf("datacenter filter = %v", got)
	}
}

func TestBuildOffersMapsMarketplaceFacts(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	offers := []offer{{
		ID: 9001, GPUName: "RTX 4090", GPUArch: "nvidia", NumGPUs: 2, GPURAMMb: 24576,
		CPUCoresEffective: 16, CPURAMMb: 65536, DiskSpaceGB: 500,
		DPHTotal: pricePtr(0.72), Reliability: 0.98, Verification: "verified",
	}}
	got := buildOffers(offers, 2, 75, now)
	if len(got) != 1 {
		t.Fatalf("expected 1 offer, got %d: %+v", len(got), got)
	}
	o := got[0]
	if o.ID != "off_vast_9001" || o.NativeRef != "9001" {
		t.Errorf("id/native ref = %q/%q", o.ID, o.NativeRef)
	}
	if o.Kind != domain.OfferKindProvisionable {
		t.Errorf("kind = %q, want provisionable", o.Kind)
	}
	// $0.72/hour all-in => 2e-4/second.
	if o.Pricing.RatePerSecondUSD < 1.9e-4 || o.Pricing.RatePerSecondUSD > 2.1e-4 {
		t.Errorf("rate per second = %v", o.Pricing.RatePerSecondUSD)
	}
	acc := o.Resources.Accelerators[0]
	if acc.Vendor != "NVIDIA" || acc.Model != "RTX 4090" || acc.Count != 2 {
		t.Errorf("accelerator = %+v", acc)
	}
	if acc.CanonicalModel != "nvidia-rtx-4090" {
		t.Errorf("canonical model = %q", acc.CanonicalModel)
	}
	if acc.MemoryBytes != 24576*mib {
		t.Errorf("gpu memory = %d", acc.MemoryBytes)
	}
	if o.Resources.CPUMillis != 16000 || o.Resources.MemoryBytes != 65536*mib {
		t.Errorf("cpu/mem = %d/%d", o.Resources.CPUMillis, o.Resources.MemoryBytes)
	}
	if o.Resources.EphemeralDiskBytes != 75*gib {
		t.Errorf("disk = %d", o.Resources.EphemeralDiskBytes)
	}
	// reliability2=0.98 => interruption rate 0.02, so placement can rank on it.
	if o.Reliability.InterruptionRate < 0.019 || o.Reliability.InterruptionRate > 0.021 {
		t.Errorf("interruption rate = %v", o.Reliability.InterruptionRate)
	}
	if !o.Pricing.Known || !o.Capacity.Available || !o.ImageCache.Known {
		t.Errorf("facts must be known: %+v", o)
	}
}

func TestBuildOffersDropsNonSecureUnpricedAndWrongSizeOffers(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	offers := []offer{
		// Server-side filter regression must not leak unverified capacity.
		{ID: 1, GPUName: "RTX 4090", NumGPUs: 1, DPHTotal: pricePtr(0.2), Verification: "unverified"},
		{ID: 2, GPUName: "RTX 4090", NumGPUs: 1, DPHTotal: pricePtr(0.2), Verification: "deverified"},
		{ID: 3, GPUName: "RTX 4090", NumGPUs: 1, DPHTotal: nil, Verification: "verified"},           // no price
		{ID: 4, GPUName: "RTX 4090", NumGPUs: 4, DPHTotal: pricePtr(0.8), Verification: "verified"}, // wrong GPU count
	}
	if got := buildOffers(offers, 1, 20, now); len(got) != 0 {
		t.Fatalf("expected all offers dropped, got %+v", got)
	}
}
