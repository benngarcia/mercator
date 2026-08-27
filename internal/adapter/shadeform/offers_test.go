package shadeform

import (
	"context"
	"testing"

	"github.com/benngarcia/mercator/internal/capability"
	"github.com/benngarcia/mercator/internal/domain"
)

func TestListCapacityMapsCatalogTriplesToOffers(t *testing.T) {
	fake := newFakeShadeform()
	fake.types = []instanceType{vmType()}
	a := newTestAdapter(t, fake, nil)

	offers, err := a.ListCapacity(context.Background(), capability.CapacityQuery{})
	if err != nil {
		t.Fatalf("list capacity: %v", err)
	}
	// Both regions this type is listed in, and the one with no stock says so
	// rather than being left out. An answer filtered on availability makes a
	// sold-out region indistinguishable from inventory nobody sells.
	if len(offers) != 2 {
		t.Fatalf("want both listed regions, got %d: %+v", len(offers), offers)
	}
	o := offers[0]
	if !o.Capacity.Available {
		t.Errorf("the region with stock says its capacity is not available: %+v", o.Capacity)
	}
	if offers[1].Capacity.Available {
		t.Errorf("the region with no stock says its capacity is available: %+v", offers[1].Capacity)
	}
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
	if len(acc) != 1 || acc[0].Count != 1 || acc[0].CanonicalModel != "nvidia-a6000" || acc[0].MemoryBytes != 48*gib {
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
		t.Errorf("Mercator opens nothing inbound on a machine it rents, got %q", o.Capabilities.Network.Inbound)
	}
	// What a listing states is the machine, never what executing on it would be
	// like: a container runtime, an idempotent launch and a concurrency limit are
	// the enrolled agent's facts and arrive on that node's own offer. What this
	// provider promises about the lease is negotiated in CapacitySupport.
	if o.Capabilities.Lifecycle != (domain.LifecycleCapabilities{}) || o.Capabilities.Container != (domain.ContainerCapabilities{}) {
		t.Errorf("a capacity listing states execution semantics it cannot know: %+v", o.Capabilities)
	}
	if o.Images.Known {
		t.Errorf("a fresh instance cannot enumerate what it holds; its inventory must be silent, got %+v", o.Images)
	}
	if !o.ExpiresAt.After(o.ObservedAt) {
		t.Errorf("offer must expire after observation: %+v", o)
	}
	if o.Platform.OS != "linux" || o.Platform.Architecture != "amd64" {
		t.Errorf("platform = %+v", o.Platform)
	}
}

func TestListCapacityMarksGraceHostsAsARM64(t *testing.T) {
	gh200 := vmType()
	gh200.ShadeInstanceType = "GH200"
	gh200.Configuration.GPUType = "GH200"
	fake := newFakeShadeform()
	fake.types = []instanceType{gh200}
	a := newTestAdapter(t, fake, nil)

	offers, err := a.ListCapacity(context.Background(), capability.CapacityQuery{})
	if err != nil {
		t.Fatalf("list capacity: %v", err)
	}
	if len(offers) != 2 || offers[0].Platform.Architecture != "arm64" {
		t.Fatalf("GH200 (Grace superchip) hosts are ARM; advertising amd64 places images that die at exec: %+v", offers)
	}
}

func TestListCapacityExcludesNonVMDeploymentTypes(t *testing.T) {
	container := vmType()
	container.DeploymentType = "container"
	container.ShadeInstanceType = "A6000_container"
	baremetal := vmType()
	baremetal.DeploymentType = "baremetal"
	baremetal.ShadeInstanceType = "A6000_metal"
	fake := newFakeShadeform()
	fake.types = []instanceType{vmType(), container, baremetal}
	a := newTestAdapter(t, fake, nil)

	offers, err := a.ListCapacity(context.Background(), capability.CapacityQuery{})
	if err != nil {
		t.Fatalf("list capacity: %v", err)
	}
	if len(offers) != 2 || offers[0].NativeRef != "hyperstack/canada-1/A6000" {
		t.Fatalf("container/baremetal inventory must be excluded, got %+v", offers)
	}
}

func TestListCapacityFiltersToAllowedClouds(t *testing.T) {
	lambda := vmType()
	lambda.Cloud = "lambdalabs"
	fake := newFakeShadeform()
	fake.types = []instanceType{vmType(), lambda}
	a := newTestAdapter(t, fake, map[string]string{"allowed_clouds": "LambdaLabs"})

	offers, err := a.ListCapacity(context.Background(), capability.CapacityQuery{})
	if err != nil {
		t.Fatalf("list capacity: %v", err)
	}
	if len(offers) != 2 || offers[0].NativeRef != "lambdalabs/canada-1/A6000" {
		t.Fatalf("allowed_clouds must filter offers (case-insensitively), got %+v", offers)
	}
}

func TestListCapacityOmitsAcceleratorsForGPUlessTypes(t *testing.T) {
	cpu := vmType()
	cpu.Configuration.NumGPUs = 0
	fake := newFakeShadeform()
	fake.types = []instanceType{cpu}
	a := newTestAdapter(t, fake, nil)

	offers, err := a.ListCapacity(context.Background(), capability.CapacityQuery{})
	if err != nil {
		t.Fatalf("list capacity: %v", err)
	}
	if len(offers) != 2 || len(offers[0].Resources.Accelerators) != 0 {
		t.Fatalf("gpu-less type must advertise no accelerators, got %+v", offers)
	}
}

// TestOneRegionNameInTwoCloudsIsTwoPlaces is why this adapter states the cloud and
// the region together. Shadeform places by an explicit triple and a region name is
// only unique inside the cloud that named it, so "us-east-1" from two clouds is two
// datacentres on two networks: a history filed under the bare name would average a
// machine in Virginia with a machine somewhere else, and report the average as
// evidence about the region.
func TestOneRegionNameInTwoCloudsIsTwoPlaces(t *testing.T) {
	hyperstack, crusoe := vmType(), vmType()
	crusoe.Cloud = "crusoe"
	hyperstack.Availability = []availability{{Region: "us-east-1", Available: true}}
	crusoe.Availability = []availability{{Region: "us-east-1", Available: true}}
	fake := newFakeShadeform()
	fake.types = []instanceType{hyperstack, crusoe}
	a := newTestAdapter(t, fake, nil)

	offers, err := a.ListCapacity(context.Background(), capability.CapacityQuery{})
	if err != nil {
		t.Fatalf("list capacity: %v", err)
	}

	if len(offers) != 2 {
		t.Fatalf("expected the product in both clouds, got %d: %+v", len(offers), offers)
	}
	first := domain.CandidateIdentityOf(aggregated(offers[0]), "sha256:image")
	second := domain.CandidateIdentityOf(aggregated(offers[1]), "sha256:image")
	if first.ProviderAndRegion(false) == second.ProviderAndRegion(false) {
		t.Fatalf("two clouds naming one region share the place %q", first.ProviderAndRegion(false))
	}
	if first.ProviderAndRegion(false) != "lane=reusable;provider=shadeform;region=hyperstack/us-east-1" {
		t.Fatalf("the place this offer recurs in is %q", first.ProviderAndRegion(false))
	}
	if first.InstanceType != "A6000" {
		t.Fatalf("the product Shadeform sells this as is %q", first.InstanceType)
	}
}

// aggregated is the offer as a scheduler receives it: the Broker stamps the
// adapter type from the connection the offer arrived through, so an offer straight
// out of the adapter does not name its own provider yet.
func aggregated(offer domain.OfferSnapshot) domain.OfferSnapshot {
	offer.AdapterType = "shadeform"
	// And the lane from the Declaration this backend negotiated, which is reusable:
	// what Shadeform sells is a machine that outlives the workloads run on it.
	// Capacity nobody classified has no key at any level, so an unstamped offer
	// would make every assertion below one about the empty string.
	offer.Lane = domain.LaneReusable
	return offer
}
