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
		{ID: "NVIDIA RTX A2000", DisplayName: "RTX A2000", MemoryInGb: 6, CommunityPrice: pricePtr(0.12), StockStatus: "High"},
		{ID: "NVIDIA RTX A4000", DisplayName: "RTX A4000", MemoryInGb: 16, CommunityPrice: pricePtr(0.17), StockStatus: "None"}, // out of stock
		{ID: "NVIDIA H100", DisplayName: "H100", MemoryInGb: 80, CommunityPrice: pricePtr(3.5), StockStatus: "High"},            // not in allow-list
		{ID: "NVIDIA RTX A5000", DisplayName: "RTX A5000", MemoryInGb: 24, CommunityPrice: nil, StockStatus: "High"},            // no price (and not allowed)
	}
	offers := buildOffers(gpus, []string{"NVIDIA RTX A2000", "NVIDIA RTX A4000"}, now)

	if len(offers) != 1 {
		t.Fatalf("expected exactly 1 offer (A2000), got %d: %+v", len(offers), offers)
	}
	o := offers[0]
	if o.NativeRef != "NVIDIA RTX A2000" {
		t.Errorf("native ref = %q", o.NativeRef)
	}
	if o.Kind != domain.OfferKindProvisionable {
		t.Errorf("kind = %q, want provisionable", o.Kind)
	}
	if o.Platform.OS != "linux" || o.Platform.Architecture != "amd64" {
		t.Errorf("platform = %+v", o.Platform)
	}
	// 0.12 / 3600 ~= 3.333e-5
	if o.Pricing.RatePerSecondUSD < 3.3e-5 || o.Pricing.RatePerSecondUSD > 3.4e-5 {
		t.Errorf("rate per second = %v", o.Pricing.RatePerSecondUSD)
	}
	if len(o.Resources.Accelerators) != 1 || o.Resources.Accelerators[0].Vendor != "NVIDIA" || o.Resources.Accelerators[0].Count != 1 {
		t.Errorf("accelerators = %+v", o.Resources.Accelerators)
	}
	if o.Resources.Accelerators[0].MemoryBytes != int64(6)*1024*1024*1024 {
		t.Errorf("accelerator memory = %d", o.Resources.Accelerators[0].MemoryBytes)
	}
	// The canonical id is what the scheduler matches on, derived from the
	// RunPod displayName ("RTX A2000") -> "nvidia-rtx-a2000".
	if o.Resources.Accelerators[0].CanonicalModel != "nvidia-rtx-a2000" {
		t.Errorf("canonical model = %q, want nvidia-rtx-a2000", o.Resources.Accelerators[0].CanonicalModel)
	}
	if !o.Capacity.Available {
		t.Errorf("capacity should be available")
	}
	// Image-cache fact must be KNOWN or the scheduler rejects the offer with UNKNOWN_FACT.
	if !o.ImageCache.Known {
		t.Errorf("image cache fact must be known (scheduler rejects unknown)")
	}
}

func TestBuildOffersDropsAllowListedGPUWithNilPrice(t *testing.T) {
	now := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	// An allow-listed, in-stock GPU with no community price must still be dropped,
	// exercising the nil-price filter independently of the allow-list filter.
	unpriced := []gpuType{{ID: "NVIDIA RTX A2000", DisplayName: "A2000", MemoryInGb: 6, CommunityPrice: nil, StockStatus: "High"}}
	if got := buildOffers(unpriced, []string{"NVIDIA RTX A2000"}, now); len(got) != 0 {
		t.Fatalf("allow-listed GPU with nil price must be dropped, got %d", len(got))
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
