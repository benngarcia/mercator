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
// triple, so each available region of each type becomes its own offer and the
// triple is the offer's NativeRef. Only deployment_type "vm" is offered: the
// docs never define what launch_configuration means on container- or
// baremetal-typed inventory (open question with Shadeform support); callers log
// the excluded count so the coverage loss stays visible.
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
			if !region.Available {
				continue
			}
			offers = append(offers, buildOffer(t, region.Region, now))
		}
	}
	return offers, excludedNonVM
}

func buildOffer(t instanceType, region string, now time.Time) domain.OfferSnapshot {
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
		ID:         "off_shadeform_" + offerSlug(t.Cloud+"_"+region+"_"+t.ShadeInstanceType),
		Kind:       domain.OfferKindProvisionable,
		NativeRef:  nativeRef(t.Cloud, region, t.ShadeInstanceType),
		ObservedAt: now,
		ExpiresAt:  now.Add(5 * time.Minute),
		Platform:   domain.Platform{OS: "linux", Architecture: "amd64"},
		Resources: domain.ResourceInventory{
			CPUMillis:          int64(cfg.VCPUs) * 1000,
			MemoryBytes:        int64(cfg.MemoryInGB) * gib,
			EphemeralDiskBytes: int64(cfg.StorageInGB) * gib,
			Accelerators:       accelerators,
		},
		Capabilities: domain.CapabilityProfile{
			Container: domain.ContainerCapabilities{MaxContainers: 1, SupportsDigestRefs: true, MaxEnvironmentBytes: 32768},
			// ProviderTTL: every launch sets auto_delete, so the provider
			// reclaims the instance even if the whole broker is down.
			Lifecycle: domain.LifecycleCapabilities{IdempotentLaunch: "launch_key", ListOwned: true, ProviderTTL: true, CancelQueued: true},
			Resources: domain.ResourceCapabilities{GPUVendors: []string{cfg.GPUManufacturer}},
			// The docker launch configuration runs with --network=host and the
			// adapter maps no ports, so no inbound port contract is offered.
			Network: domain.NetworkCapabilities{Inbound: domain.InboundNetworkNone, PublicIPv4: true},
			Pricing: domain.PricingCapabilities{Known: true},
		},
		Pricing: domain.PriceModel{
			Currency:           "USD",
			RatePerSecondUSD:   float64(t.HourlyPrice) / 100.0 / 3600.0,
			GranularitySeconds: 1,
			Known:              true,
		},
		Capacity: domain.CapacityEvidence{Available: true, Confidence: 1},
		// Shadeform pulls the image fresh on the provisioned host; report a
		// known "not cached" fact so the scheduler prices in a pull instead of
		// rejecting the offer with UNKNOWN_FACT.
		ImageCache: domain.ImageCacheEvidence{Known: true},
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

func offerSlug(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, " ", "_")
	s = strings.ReplaceAll(s, "/", "_")
	return s
}
