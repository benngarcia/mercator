package runpod

import (
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
)

func pricePtr(v float64) *float64 { return &v }

func TestBuildOffersFiltersByAllowlistStockAndPrice(t *testing.T) {
	now := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	gpus := []gpuType{
		{ID: "NVIDIA RTX A2000", DisplayName: "RTX A2000", MemoryInGb: 6, SecurePrice: pricePtr(0.12), SecureStockStatus: "High"},
		{ID: "NVIDIA RTX A4000", DisplayName: "RTX A4000", MemoryInGb: 16, SecurePrice: pricePtr(0.17), SecureStockStatus: "None"}, // out of stock
		{ID: "NVIDIA H100", DisplayName: "H100", MemoryInGb: 80, SecurePrice: pricePtr(3.5), SecureStockStatus: "High"},            // not in allow-list
		{ID: "NVIDIA RTX A5000", DisplayName: "RTX A5000", MemoryInGb: 24, SecurePrice: nil, SecureStockStatus: "High"},            // no price (and not allowed)
	}
	offers := buildOffers(gpus, []string{"NVIDIA RTX A2000", "NVIDIA RTX A4000"}, 2, 75, false, now)

	if len(offers) != 1 {
		t.Fatalf("expected exactly 1 offer (A2000), got %d: %+v", len(offers), offers)
	}
	o := offers[0]
	if o.NativeRef != "NVIDIA RTX A2000|SECURE" {
		t.Errorf("native ref = %q", o.NativeRef)
	}
	if o.Kind != domain.OfferKindProvisionable {
		t.Errorf("kind = %q, want provisionable", o.Kind)
	}
	if o.Platform.OS != "linux" || o.Platform.Architecture != "amd64" {
		t.Errorf("platform = %+v", o.Platform)
	}
	// Two GPUs at $0.12/GPU/hour => $0.24/hour ~= 6.667e-5/second.
	if o.Pricing.RatePerSecondUSD < 6.6e-5 || o.Pricing.RatePerSecondUSD > 6.7e-5 {
		t.Errorf("rate per second = %v", o.Pricing.RatePerSecondUSD)
	}
	if len(o.Resources.Accelerators) != 1 || o.Resources.Accelerators[0].Vendor != "NVIDIA" || o.Resources.Accelerators[0].Count != 2 {
		t.Errorf("accelerators = %+v", o.Resources.Accelerators)
	}
	if o.Resources.Accelerators[0].MemoryBytes != int64(6)*1024*1024*1024 {
		t.Errorf("accelerator memory = %d", o.Resources.Accelerators[0].MemoryBytes)
	}
	if o.Resources.EphemeralDiskBytes != int64(75)*1024*1024*1024 {
		t.Errorf("ephemeral disk = %d", o.Resources.EphemeralDiskBytes)
	}
	// The canonical id is what the scheduler matches on, derived from the
	// RunPod displayName ("RTX A2000") -> "nvidia-a2000".
	if o.Resources.Accelerators[0].CanonicalModel != "nvidia-a2000" {
		t.Errorf("canonical model = %q, want nvidia-a2000", o.Resources.Accelerators[0].CanonicalModel)
	}
	if !o.Capacity.Available {
		t.Errorf("capacity should be available")
	}
	// Image-cache fact must be KNOWN or the scheduler rejects the offer with UNKNOWN_FACT.
	if !o.ImageCache.Known {
		t.Errorf("image cache fact must be known (scheduler rejects unknown)")
	}
}

func TestBuildOffersSecureOnlyIgnoresCommunityCapacity(t *testing.T) {
	now := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	gpus := []gpuType{
		// No secure price (community-only GPU): even with secure stock reported,
		// community price/stock must not rescue it.
		{ID: "NVIDIA RTX A2000", DisplayName: "RTX A2000", MemoryInGb: 6, SecureStockStatus: "High", CommunityPrice: pricePtr(0.08), CommunityStockStatus: "High"},
		// Priced on both, but secure stock is gone: community stock must not rescue it.
		{ID: "NVIDIA RTX A4000", DisplayName: "RTX A4000", MemoryInGb: 16, SecurePrice: pricePtr(0.17), SecureStockStatus: "None", CommunityPrice: pricePtr(0.11), CommunityStockStatus: "High"},
	}
	if got := buildOffers(gpus, []string{"NVIDIA RTX A2000", "NVIDIA RTX A4000"}, 1, 20, false, now); len(got) != 0 {
		t.Fatalf("secure-only connection must not advertise community capacity, got %+v", got)
	}
}

func TestBuildOffersCommunityOptInAdvertisesBothClouds(t *testing.T) {
	now := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	gpus := []gpuType{{
		ID: "NVIDIA RTX A2000", DisplayName: "RTX A2000", MemoryInGb: 6,
		SecurePrice: pricePtr(0.12), SecureStockStatus: "High",
		CommunityPrice: pricePtr(0.08), CommunityStockStatus: "Low",
	}}
	offers := buildOffers(gpus, []string{"NVIDIA RTX A2000"}, 1, 20, true, now)
	if len(offers) != 2 {
		t.Fatalf("opt-in should advertise one offer per cloud, got %d: %+v", len(offers), offers)
	}
	secure, community := offers[0], offers[1]
	if secure.NativeRef != "NVIDIA RTX A2000|SECURE" || community.NativeRef != "NVIDIA RTX A2000|COMMUNITY" {
		t.Errorf("native refs = %q, %q", secure.NativeRef, community.NativeRef)
	}
	if secure.ID == community.ID {
		t.Errorf("offer ids must be distinct per cloud, both %q", secure.ID)
	}
	if secure.Pricing.RatePerSecondUSD <= community.Pricing.RatePerSecondUSD {
		t.Errorf("each cloud must carry its own price: secure=%v community=%v", secure.Pricing.RatePerSecondUSD, community.Pricing.RatePerSecondUSD)
	}
}

func TestSplitNativeRef(t *testing.T) {
	for _, c := range []struct {
		ref, gpu, cloud string
	}{
		{"NVIDIA RTX A2000|SECURE", "NVIDIA RTX A2000", "SECURE"},
		{"NVIDIA RTX A2000|COMMUNITY", "NVIDIA RTX A2000", "COMMUNITY"},
		// Legacy/cloudless refs default to the safe cloud.
		{"NVIDIA RTX A2000", "NVIDIA RTX A2000", "SECURE"},
		{"", "", "SECURE"},
	} {
		gpu, cloud := splitNativeRef(c.ref)
		if gpu != c.gpu || cloud != c.cloud {
			t.Errorf("splitNativeRef(%q) = (%q, %q), want (%q, %q)", c.ref, gpu, cloud, c.gpu, c.cloud)
		}
	}
}

func TestStockAvailable(t *testing.T) {
	for _, c := range []struct {
		in   string
		want bool
	}{{"High", true}, {"Medium", true}, {"Low", true}, {"", false}, {"None", false}, {"unavailable", false}} {
		if got := stockAvailable(c.in); got != c.want {
			t.Errorf("stockAvailable(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
