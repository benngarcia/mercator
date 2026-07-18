package shadeform

import (
	"context"
	"testing"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/domain"
)

func TestListOffersMapsCatalogTriplesToOffers(t *testing.T) {
	fake := newFakeShadeform()
	fake.types = []instanceType{vmType()}
	a := newTestAdapter(t, fake, nil)

	offers, err := a.ListOffers(context.Background(), adapter.OfferRequest{WorkspaceID: "ws_1"})
	if err != nil {
		t.Fatalf("list offers: %v", err)
	}
	if len(offers) != 1 {
		t.Fatalf("want 1 offer (only the available region), got %d: %+v", len(offers), offers)
	}
	o := offers[0]
	if o.NativeRef != "hyperstack/canada-1/A6000" {
		t.Errorf("native ref = %q, want the cloud/region/type triple", o.NativeRef)
	}
	if o.Kind != domain.OfferKindProvisionable {
		t.Errorf("kind = %q, want provisionable", o.Kind)
	}
	if o.Resources.CPUMillis != 12000 || o.Resources.MemoryBytes != 48*gib || o.Resources.EphemeralDiskBytes != 256*gib {
		t.Errorf("resources = %+v", o.Resources)
	}
	acc := o.Resources.Accelerators
	if len(acc) != 1 || acc[0].Count != 1 || acc[0].CanonicalModel != "nvidia-rtx-a6000" || acc[0].MemoryBytes != 48*gib {
		t.Errorf("accelerators = %+v", acc)
	}
	// 210 cents/hour → dollars per second
	wantRate := 210.0 / 100.0 / 3600.0
	if o.Pricing.RatePerSecondUSD != wantRate || !o.Pricing.Known {
		t.Errorf("pricing = %+v, want rate %v", o.Pricing, wantRate)
	}
	if o.Provisioning == nil || o.Provisioning.Expected != 240 || o.Provisioning.P90 != 300 {
		t.Errorf("provisioning from boot_time = %+v", o.Provisioning)
	}
	if o.Capabilities.Network.Inbound != domain.InboundNetworkNone {
		t.Errorf("no ports are mapped; inbound must be none, got %q", o.Capabilities.Network.Inbound)
	}
	if !o.Capabilities.Lifecycle.ProviderTTL {
		t.Error("auto_delete is set on every launch; ProviderTTL must be advertised")
	}
	if !o.ImageCache.Known || o.ImageCache.ManifestCached {
		t.Errorf("image cache must be a known miss, got %+v", o.ImageCache)
	}
	if !o.ExpiresAt.After(o.ObservedAt) {
		t.Errorf("offer must expire after observation: %+v", o)
	}
}

func TestListOffersExcludesNonVMDeploymentTypes(t *testing.T) {
	container := vmType()
	container.DeploymentType = "container"
	container.ShadeInstanceType = "A6000_container"
	baremetal := vmType()
	baremetal.DeploymentType = "baremetal"
	baremetal.ShadeInstanceType = "A6000_metal"
	fake := newFakeShadeform()
	fake.types = []instanceType{vmType(), container, baremetal}
	a := newTestAdapter(t, fake, nil)

	offers, err := a.ListOffers(context.Background(), adapter.OfferRequest{})
	if err != nil {
		t.Fatalf("list offers: %v", err)
	}
	if len(offers) != 1 || offers[0].NativeRef != "hyperstack/canada-1/A6000" {
		t.Fatalf("container/baremetal inventory must be excluded, got %+v", offers)
	}
}

func TestListOffersFiltersToAllowedClouds(t *testing.T) {
	lambda := vmType()
	lambda.Cloud = "lambdalabs"
	fake := newFakeShadeform()
	fake.types = []instanceType{vmType(), lambda}
	a := newTestAdapter(t, fake, map[string]string{"allowed_clouds": "LambdaLabs"})

	offers, err := a.ListOffers(context.Background(), adapter.OfferRequest{})
	if err != nil {
		t.Fatalf("list offers: %v", err)
	}
	if len(offers) != 1 || offers[0].NativeRef != "lambdalabs/canada-1/A6000" {
		t.Fatalf("allowed_clouds must filter offers (case-insensitively), got %+v", offers)
	}
}

func TestListOffersOmitsAcceleratorsForGPUlessTypes(t *testing.T) {
	cpu := vmType()
	cpu.Configuration.NumGPUs = 0
	fake := newFakeShadeform()
	fake.types = []instanceType{cpu}
	a := newTestAdapter(t, fake, nil)

	offers, err := a.ListOffers(context.Background(), adapter.OfferRequest{})
	if err != nil {
		t.Fatalf("list offers: %v", err)
	}
	if len(offers) != 1 || len(offers[0].Resources.Accelerators) != 0 {
		t.Fatalf("gpu-less type must advertise no accelerators, got %+v", offers)
	}
}
