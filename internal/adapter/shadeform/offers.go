package shadeform

import (
	"strings"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/gpunorm"
)

const gib = int64(1024) * 1024 * 1024

// buildOffers maps the Shadeform instance-type catalog onto offer snapshots.
// Placement on Shadeform is an explicit (cloud, region, shade_instance_type)
// triple, so each region of each type becomes its own offer and the triple is
// the offer's NativeRef. Only deployment_type "vm" is offered: the docs never
// define what launch_configuration means on container- or baremetal-typed
// inventory (open question with Shadeform support); callers log the excluded
// count so the coverage loss stays visible.
//
// A region with no stock right now is published as capacity that is not
// available rather than dropped. Dropping it made a region that is sold out
// indistinguishable from a machine type this catalog does not sell, and the
// control plane reads the difference: a Run the fleet published nothing for is
// waiting for capacity to be added and is exempted from holding the queue, while
// a Run refused by a machine somebody else is on keeps its place and waits for
// that machine. Sold out is the second of those.
func buildOffers(types []instanceType, allowedClouds map[string]bool, now time.Time) (offers []domain.OfferSnapshot, excludedNonVM int) {
	for _, t := range types {
		if t.DeploymentType != "vm" {
			excludedNonVM++
			continue
		}
		if allowedClouds != nil && !allowedClouds[strings.ToLower(t.Cloud)] {
			continue
		}
		for _, region := range t.Availability {
			offers = append(offers, buildOffer(t, region.Region, region.Available, now))
		}
	}
	return offers, excludedNonVM
}

func buildOffer(t instanceType, region string, available bool, now time.Time) domain.OfferSnapshot {
	cfg := t.Configuration
	var accelerators []domain.AcceleratorInventory
	if cfg.NumGPUs > 0 {
		accelerators = []domain.AcceleratorInventory{{
			Vendor:         cfg.GPUManufacturer,
			Model:          cfg.GPUType,
			CanonicalModel: gpunorm.Canonical(cfg.GPUManufacturer, cfg.GPUType),
			Count:          cfg.NumGPUs,
			MemoryBytes:    int64(cfg.VRAMPerGPUInGB) * gib,
		}}
	}
	offer := domain.OfferSnapshot{
		ID:   "off_shadeform_" + offerSlug(t.Cloud+"_"+region+"_"+t.ShadeInstanceType),
		Kind: domain.OfferKindProvisionable,
		// Placement here is an explicit triple, and the region half of it is the
		// cloud and the region together: a region name is only unique inside the
		// cloud that named it, so "us-east-1" from two clouds is two places and a
		// history filed under the bare name would average them.
		Region:       t.Cloud + "/" + region,
		InstanceType: t.ShadeInstanceType,
		NativeRef:    nativeRef(t.Cloud, region, t.ShadeInstanceType),
		ObservedAt:   now,
		ExpiresAt:    now.Add(5 * time.Minute),
		Platform:     domain.Platform{OS: domain.DefaultPlatformOS, Architecture: hostArchitecture(cfg.GPUType)},
		Resources: domain.ResourceInventory{
			CPUMillis:          int64(cfg.VCPUs) * 1000,
			MemoryBytes:        int64(cfg.MemoryInGB) * gib,
			EphemeralDiskBytes: int64(cfg.StorageInGB) * gib,
			EphemeralDiskKnown: true,
			// A catalog states the cards it sells with the machine, so this
			// inventory is one somebody took: a listing with no accelerator
			// entries is a CPU instance type rather than a machine nobody counted.
			Accelerators:      accelerators,
			AcceleratorsKnown: true,
		},
		// What this listing states is what the machine is, and nothing about what
		// executing on it would be like. A capacity listing that declared a
		// container runtime, an idempotent launch or a concurrency limit would be
		// a provider answering for a runtime it does not run: those are the
		// enrolled agent's facts, established from the machine itself once an
		// agent is on it, and they arrive on that node's own offer.
		//
		// What Shadeform can promise about the lease it is selling is negotiated
		// in CapacitySupport instead, where a caller reads it before renting
		// rather than after placing work.
		Capabilities: domain.CapabilityProfile{
			Resources: domain.ResourceCapabilities{GPUVendors: []string{cfg.GPUManufacturer}},
			// A rented machine gets a public address and Mercator opens nothing
			// inbound on it: every exchange is one the agent opens outbound.
			Network: domain.NetworkCapabilities{Inbound: domain.InboundNetworkNone, PublicIPv4: true},
			Pricing: domain.PricingCapabilities{Known: true},
		},
		Pricing: domain.PriceModel{
			Currency:           "USD",
			RatePerSecondUSD:   float64(t.HourlyPrice) / 100.0 / 3600.0,
			GranularitySeconds: 1,
			Known:              true,
		},
		// A catalog listing says this machine type can be had in this region, and
		// Shadeform says per region whether it can be had right now. That answer
		// is carried through rather than used to drop the listing: a region with
		// no stock is a wait, a machine type this catalog never listed is a shape
		// nobody sells, and the queue is ordered on the difference.
		//
		// Its publisher states no confidence in either, and neither does Mercator
		// on their behalf: capacity that may be gone by launch, asserted certain,
		// is a claim nobody made. What would state it here is a measurement of how
		// often provisioning this listing actually succeeds, which nothing
		// collects yet.
		Capacity: domain.CapacityEvidence{Available: available},
		// Shadeform pulls the image fresh on the provisioned host, but the
		// image (and its size) is unknown at offer time and the evidence
		// contract has no "uncached, size unknown" state: Known:true with
		// MissingBytes 0 scores as a free pull (estimatePullSeconds returns
		// 0), understating start latency. RunPod reports the same value; the
		// contract gap is tracked as a follow-up issue.
		// A fresh instance reports nothing about what it holds, so its inventory
		// is silent rather than empty.
		Images: domain.ImageInventory{Known: false},
	}
	if t.BootTime != nil && t.BootTime.MaxBootInSec > 0 {
		offer.Provisioning = &domain.Estimate{
			Expected: float64(t.BootTime.MinBootInSec+t.BootTime.MaxBootInSec) / 2,
			P90:      float64(t.BootTime.MaxBootInSec),
			Source:   "shadeform:boot_time",
		}
	}
	return offer
}

func nativeRef(cloud, region, shadeInstanceType string) string {
	return cloud + "/" + region + "/" + shadeInstanceType
}

// hostArchitecture infers the host CPU architecture from the GPU type. The
// Shadeform catalog has no architecture field, and Grace-based superchips
// (GH200/GB200) are ARM hosts: advertising them as amd64 would let the
// scheduler place an amd64 image that dies at exec, invisibly to Observe.
func hostArchitecture(gpuType string) string {
	t := strings.ToUpper(gpuType)
	if strings.Contains(t, "GH200") || strings.Contains(t, "GB200") {
		return "arm64"
	}
	return catalogFallbackArch
}

// catalogFallbackArch is what we advertise for a Shadeform catalog entry whose
// GPU type does not identify an ARM host. Every non-Grace instance Shadeform
// lists today is x86.
const catalogFallbackArch = "amd64"

func offerSlug(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, " ", "_")
	s = strings.ReplaceAll(s, "/", "_")
	return s
}
