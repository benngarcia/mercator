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
		Platform:   domain.Platform{OS: domain.DefaultPlatformOS, Architecture: hostArchitecture(cfg.GPUType)},
		Resources: domain.ResourceInventory{
			CPUMillis:          int64(cfg.VCPUs) * 1000,
			MemoryBytes:        int64(cfg.MemoryInGB) * gib,
			EphemeralDiskBytes: int64(cfg.StorageInGB) * gib,
			Accelerators:       accelerators,
		},
		Capabilities: domain.CapabilityProfile{
			// SupportsEntrypointOverride stays false: the docker launch
			// configuration has no entrypoint field, so the scheduler must
			// keep entrypoint-overriding workloads off these offers.
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
		// Shadeform pulls the image fresh on the provisioned host, but the
		// image (and its size) is unknown at offer time and the evidence
		// contract has no "uncached, size unknown" state: Known:true with
		// MissingBytes 0 scores as a free pull (estimatePullSeconds returns
		// 0), understating start latency. RunPod reports the same value; the
		// contract gap is tracked as a follow-up issue.
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
