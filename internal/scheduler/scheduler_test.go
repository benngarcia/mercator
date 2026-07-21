package scheduler

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
)

func TestSchedulerSelectsLowestDeterministicScore(t *testing.T) {
	now := time.Date(2026, 6, 20, 18, 31, 22, 0, time.UTC)
	input := SchedulingInput{
		RunID:        "run_1",
		Workload:     schedulerRevision(),
		Offers:       []domain.OfferSnapshot{schedulerOffer("off_slow", now, 0.00010, 40), schedulerOffer("off_fast", now, 0.00012, 5)},
		ModelVersion: "latency-v1",
		EvaluatedAt:  now,
		Weights: ScoreWeights{
			StartLatencyUSDPerSecond: 0.001,
		},
	}

	decision, err := New().Evaluate(context.Background(), input)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if decision.SelectedOfferSnapshotID != "off_fast" {
		t.Fatalf("expected off_fast to win, got %+v", decision)
	}
	if len(decision.Candidates) != 2 {
		t.Fatalf("expected two audited candidates, got %+v", decision.Candidates)
	}

	again, err := New().Evaluate(context.Background(), input)
	if err != nil {
		t.Fatalf("evaluate again: %v", err)
	}
	if decision.ID != again.ID || decision.SelectedOfferSnapshotID != again.SelectedOfferSnapshotID {
		t.Fatalf("scheduler is not deterministic:\nfirst=%+v\nsecond=%+v", decision, again)
	}
}

func TestSchedulerReportsHardRejections(t *testing.T) {
	now := time.Date(2026, 6, 20, 18, 31, 22, 0, time.UTC)
	stale := schedulerOffer("off_stale", now, 0.01, 1)
	stale.ExpiresAt = now.Add(-time.Second)
	wrongPlatform := schedulerOffer("off_platform", now, 0.01, 1)
	wrongPlatform.Platform = domain.Platform{OS: "linux", Architecture: "arm64"}
	noInbound := schedulerOffer("off_no_inbound", now, 0.01, 1)
	noInbound.Capabilities.Network.Inbound = domain.InboundNetworkNone
	unknownNetwork := schedulerOffer("off_unknown_network", now, 0.01, 1)
	unknownNetwork.Network.Download = nil

	decision, err := New().Evaluate(context.Background(), SchedulingInput{
		RunID:        "run_1",
		Workload:     schedulerRevision(),
		Offers:       []domain.OfferSnapshot{stale, wrongPlatform, noInbound, unknownNetwork},
		ModelVersion: "latency-v1",
		EvaluatedAt:  now,
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if decision.SelectedOfferSnapshotID != "" {
		t.Fatalf("expected no selected offer, got %+v", decision)
	}
	assertCandidateRejected(t, decision, "off_stale", "OFFER_EXPIRED", "expires_at")
	assertCandidateRejected(t, decision, "off_platform", "CAPABILITY_MISMATCH", "platform")
	assertCandidateRejected(t, decision, "off_no_inbound", "CAPABILITY_MISMATCH", "network.inbound")
	assertCandidateRejected(t, decision, "off_unknown_network", "UNKNOWN_FACT", "network.download")
}

func TestSchedulerRejectsEntrypointOverrideOnIncapableOffer(t *testing.T) {
	now := time.Date(2026, 6, 20, 18, 31, 22, 0, time.UTC)
	offer := schedulerOffer("off_no_entrypoint", now, 0.01, 1)
	offer.Capabilities.Container.SupportsEntrypointOverride = false
	workload := schedulerRevision()
	entrypoint := []string{"/bin/worker"}
	workload.Spec.Containers[0].Entrypoint = &entrypoint

	decision, err := New().Evaluate(context.Background(), SchedulingInput{
		RunID:        "run_1",
		Workload:     workload,
		Offers:       []domain.OfferSnapshot{offer},
		ModelVersion: "latency-v1",
		EvaluatedAt:  now,
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if decision.SelectedOfferSnapshotID != "" {
		t.Fatalf("an entrypoint-overriding workload must not land on an offer that cannot override entrypoints: %+v", decision)
	}
	assertCandidateRejected(t, decision, "off_no_entrypoint", "CAPABILITY_MISMATCH", "container.supports_entrypoint_override")
}

func TestSchedulerRejectsConservativeFactAndResourceGaps(t *testing.T) {
	now := time.Date(2026, 6, 20, 18, 31, 22, 0, time.UTC)
	rev := schedulerRevision()
	rev.Spec.Resources.Accelerators = []domain.AcceleratorRequirement{{
		Vendor: "nvidia", ModelAnyOf: []string{"nvidia-a10"}, Count: 1, MemoryMinBytes: 16 << 30,
	}}
	maxCost := 0.05
	rev.Spec.Placement.MaxExpectedCostUSD = &maxCost

	noGPU := schedulerOffer("off_no_gpu", now, 0.00001, 1)
	zeroMaxContainers := schedulerOffer("off_zero_containers", now, 0.00001, 1)
	zeroMaxContainers.Capabilities.Container.MaxContainers = 0
	unavailable := schedulerOffer("off_unavailable", now, 0.00001, 1)
	unavailable.Capacity.Available = false
	unknownImageCache := schedulerOffer("off_unknown_image_cache", now, 0.00001, 1)
	unknownImageCache.ImageCache.Known = false
	tooExpensive := schedulerOffer("off_too_expensive", now, 0.001, 1)
	tooExpensive.Resources.Accelerators = []domain.AcceleratorInventory{{Vendor: "nvidia", Model: "a10", CanonicalModel: "nvidia-a10", Count: 1, MemoryBytes: 24 << 30}}
	tooExpensive.Capabilities.Resources.GPUVendors = []string{"nvidia"}
	tooExpensive.ImageCache.Known = true
	tooExpensive.ImageCache.MissingBytes = 0

	decision, err := New().Evaluate(context.Background(), SchedulingInput{
		RunID:        "run_1",
		Workload:     rev,
		Offers:       []domain.OfferSnapshot{noGPU, zeroMaxContainers, unavailable, unknownImageCache, tooExpensive},
		ModelVersion: "latency-v1",
		EvaluatedAt:  now,
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if decision.SelectedOfferSnapshotID != "" {
		t.Fatalf("expected no selected offer, got %+v", decision)
	}
	assertCandidateRejected(t, decision, "off_no_gpu", "RESOURCE_INSUFFICIENT", "resources.accelerators")
	assertCandidateRejected(t, decision, "off_zero_containers", "UNKNOWN_FACT", "container.max_containers")
	assertCandidateRejected(t, decision, "off_unavailable", "CAPACITY_UNAVAILABLE", "capacity.available")
	assertCandidateRejected(t, decision, "off_unknown_image_cache", "UNKNOWN_FACT", "image_cache")
	assertCandidateRejected(t, decision, "off_too_expensive", "COST_LIMIT_EXCEEDED", "placement.max_expected_cost_usd")
}

func TestSchedulerMatchesAcceleratorByCanonicalModelAndNormalizedVendor(t *testing.T) {
	now := time.Date(2026, 6, 20, 18, 31, 22, 0, time.UTC)

	// Offer advertises the GPU with the provider's RAW vendor casing and the
	// canonical model id. Matching must normalize the vendor and compare the
	// canonical model — not the raw Model string.
	gpu := schedulerOffer("off_gpu", now, 0.00001, 1)
	gpu.Resources.Accelerators = []domain.AcceleratorInventory{{
		Vendor: "NVIDIA", Model: "RTX A2000", CanonicalModel: "nvidia-rtx-a2000", Count: 1, MemoryBytes: 6 << 30,
	}}
	gpu.Capabilities.Resources.GPUVendors = []string{"nvidia"}

	rev := schedulerRevision()
	rev.Spec.Resources.Accelerators = []domain.AcceleratorRequirement{{
		Vendor: "nvidia", ModelAnyOf: []string{"nvidia-rtx-a2000"}, Count: 1,
	}}
	decision, err := New().Evaluate(context.Background(), SchedulingInput{
		RunID: "run_1", Workload: rev, Offers: []domain.OfferSnapshot{gpu}, ModelVersion: "latency-v1", EvaluatedAt: now,
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if decision.SelectedOfferSnapshotID != "off_gpu" {
		t.Fatalf("canonical GPU match should select off_gpu, got %q", decision.SelectedOfferSnapshotID)
	}

	// A requirement for a DIFFERENT canonical model must not match this offer.
	revH := schedulerRevision()
	revH.Spec.Resources.Accelerators = []domain.AcceleratorRequirement{{
		Vendor: "nvidia", ModelAnyOf: []string{"nvidia-h100"}, Count: 1,
	}}
	dec2, err := New().Evaluate(context.Background(), SchedulingInput{
		RunID: "run_2", Workload: revH, Offers: []domain.OfferSnapshot{gpu}, ModelVersion: "latency-v1", EvaluatedAt: now,
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if dec2.SelectedOfferSnapshotID != "" {
		t.Fatalf("a different canonical model must not match, got %q", dec2.SelectedOfferSnapshotID)
	}
}

func TestSchedulerNormalizesRequirementModelSpellings(t *testing.T) {
	now := time.Date(2026, 6, 20, 18, 31, 22, 0, time.UTC)

	gpu := schedulerOffer("off_gpu", now, 0.00001, 1)
	gpu.Resources.Accelerators = []domain.AcceleratorInventory{{
		Vendor: "NVIDIA", Model: "NVIDIA GeForce RTX 5090", CanonicalModel: "nvidia-rtx-5090", Count: 1, MemoryBytes: 32 << 30,
	}}
	gpu.Capabilities.Resources.GPUVendors = []string{"nvidia"}

	// A requirement spelled any way gpunorm can resolve — marketing name,
	// separator-free id, or canonical id — must match the same inventory.
	for _, spelling := range []string{"nvidia-rtx-5090", "nvidia-rtx5090", "RTX 5090"} {
		rev := schedulerRevision()
		rev.Spec.Resources.Accelerators = []domain.AcceleratorRequirement{{
			Vendor: "nvidia", ModelAnyOf: []string{spelling}, Count: 1,
		}}
		decision, err := New().Evaluate(context.Background(), SchedulingInput{
			RunID: "run_1", Workload: rev, Offers: []domain.OfferSnapshot{gpu}, ModelVersion: "latency-v1", EvaluatedAt: now,
		})
		if err != nil {
			t.Fatalf("evaluate(%q): %v", spelling, err)
		}
		if decision.SelectedOfferSnapshotID != "off_gpu" {
			t.Fatalf("requirement spelling %q must match canonical inventory, got %q", spelling, decision.SelectedOfferSnapshotID)
		}
	}

	// Normalization must not loosen matching: a nearby but different model
	// still rejects.
	rev := schedulerRevision()
	rev.Spec.Resources.Accelerators = []domain.AcceleratorRequirement{{
		Vendor: "nvidia", ModelAnyOf: []string{"RTX 5080"}, Count: 1,
	}}
	decision, err := New().Evaluate(context.Background(), SchedulingInput{
		RunID: "run_2", Workload: rev, Offers: []domain.OfferSnapshot{gpu}, ModelVersion: "latency-v1", EvaluatedAt: now,
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if decision.SelectedOfferSnapshotID != "" {
		t.Fatalf("a different model spelling must not match, got %q", decision.SelectedOfferSnapshotID)
	}
}

func TestSchedulerAllowsUnknownNetworkWhenPolicyAllowsIt(t *testing.T) {
	now := time.Date(2026, 6, 20, 18, 31, 22, 0, time.UTC)
	rev := schedulerRevision()
	rev.Spec.Network.Download.AllowUnknown = true
	offer := schedulerOffer("off_unknown_network", now, 0.01, 1)
	offer.Network.Download = nil

	decision, err := New().Evaluate(context.Background(), SchedulingInput{
		RunID:        "run_1",
		Workload:     rev,
		Offers:       []domain.OfferSnapshot{offer},
		ModelVersion: "latency-v1",
		EvaluatedAt:  now,
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if decision.SelectedOfferSnapshotID != "off_unknown_network" {
		t.Fatalf("expected unknown network offer to be selected, got %+v", decision)
	}
}

func TestSchedulerAppliesRiskAndUncertaintyPenalties(t *testing.T) {
	now := time.Date(2026, 6, 20, 18, 31, 22, 0, time.UTC)
	stable := schedulerOffer("off_stable", now, 0.00012, 5)
	stable.Capacity.Confidence = 0.99
	stable.Reliability = domain.ReliabilityEvidence{
		StartFailureRate: 0.01,
		InterruptionRate: 0.01,
		Confidence:       0.99,
	}
	risky := schedulerOffer("off_risky", now, 0.00010, 5)
	risky.Capacity.Confidence = 0.4
	risky.Reliability = domain.ReliabilityEvidence{
		StartFailureRate: 0.35,
		InterruptionRate: 0.25,
		Confidence:       0.5,
	}

	decision, err := New().Evaluate(context.Background(), SchedulingInput{
		RunID:        "run_1",
		Workload:     schedulerRevision(),
		Offers:       []domain.OfferSnapshot{risky, stable},
		ModelVersion: "latency-v1",
		EvaluatedAt:  now,
		Weights: ScoreWeights{
			StartFailurePenaltyUSD: 1,
			InterruptionPenaltyUSD: 1,
			UncertaintyPenaltyUSD:  1,
		},
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if decision.SelectedOfferSnapshotID != "off_stable" {
		t.Fatalf("expected lower-risk offer to win, got %+v", decision)
	}
	if findCandidate(t, decision, "off_risky").ScoreUSD <= findCandidate(t, decision, "off_stable").ScoreUSD {
		t.Fatalf("expected risk penalties to increase risky score, got %+v", decision.Candidates)
	}
}

func TestSchedulerUsesLatencyEstimateOverrides(t *testing.T) {
	now := time.Date(2026, 6, 20, 18, 31, 22, 0, time.UTC)
	offer := schedulerOffer("off_latency", now, 0.00010, 40)
	decision, err := New().Evaluate(context.Background(), SchedulingInput{
		RunID:        "run_1",
		Workload:     schedulerRevision(),
		Offers:       []domain.OfferSnapshot{offer},
		ModelVersion: "latency-v1",
		EvaluatedAt:  now,
		LatencyEstimates: map[string]domain.Estimate{
			"off_latency": {Expected: 3, P50: 2, P90: 5, Source: "latency_estimator", SampleCount: 2, ModelVersion: "latency-v1"},
		},
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	candidate := findCandidate(t, decision, "off_latency")
	if candidate.Estimates.StartSeconds.Expected != 3 || candidate.Estimates.StartSeconds.Source != "latency_estimator" {
		t.Fatalf("expected latency override to feed scheduler, got %+v", candidate.Estimates.StartSeconds)
	}
}

func TestSchedulerDecisionStableAcrossOfferOrder(t *testing.T) {
	now := time.Date(2026, 6, 20, 18, 31, 22, 0, time.UTC)
	offers := []domain.OfferSnapshot{
		schedulerOffer("off_slow", now, 0.00010, 40),
		schedulerOffer("off_fast", now, 0.00012, 5),
	}
	forward, err := New().Evaluate(context.Background(), SchedulingInput{
		RunID:        "run_1",
		Workload:     schedulerRevision(),
		Offers:       offers,
		ModelVersion: "latency-v1",
		EvaluatedAt:  now,
		Weights: ScoreWeights{
			StartLatencyUSDPerSecond: 0.001,
		},
	})
	if err != nil {
		t.Fatalf("evaluate forward: %v", err)
	}
	reversed, err := New().Evaluate(context.Background(), SchedulingInput{
		RunID:        "run_1",
		Workload:     schedulerRevision(),
		Offers:       []domain.OfferSnapshot{offers[1], offers[0]},
		ModelVersion: "latency-v1",
		EvaluatedAt:  now,
		Weights: ScoreWeights{
			StartLatencyUSDPerSecond: 0.001,
		},
	})
	if err != nil {
		t.Fatalf("evaluate reversed: %v", err)
	}
	if forward.ID != reversed.ID || forward.SelectedOfferSnapshotID != reversed.SelectedOfferSnapshotID {
		t.Fatalf("same offer set should produce same decision identity and selection:\nforward=%+v\nreversed=%+v", forward, reversed)
	}
	if got, want := candidateIDs(reversed), candidateIDs(forward); got != want {
		t.Fatalf("same offer set should produce stable candidate order: forward=%s reversed=%s", want, got)
	}
}

func TestSchedulerPopulatesDeterministicCollectionAndCandidateAuditData(t *testing.T) {
	now := time.Date(2026, 6, 20, 18, 31, 22, 0, time.UTC)
	offB := schedulerOffer("off_b", now, 0.00010, 10)
	offB.ConnectionID = "conn_b"
	offB.NativeRef = "native_b"
	offA := schedulerOffer("off_a", now, 0.00010, 10)
	offA.ConnectionID = "conn_a"
	offA.NativeRef = "native_a"

	decision, err := New().Evaluate(context.Background(), SchedulingInput{
		RunID:        "run_1",
		Workload:     schedulerRevision(),
		Offers:       []domain.OfferSnapshot{offB, offA},
		ModelVersion: "latency-v1",
		EvaluatedAt:  now,
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := strings.Join(decision.CollectionReport.ConnectionsQueried, ","); got != "conn_a,conn_b" {
		t.Fatalf("expected deterministic collection report, got %+v", decision.CollectionReport)
	}
	first := decision.Candidates[0]
	if first.OfferSnapshotID != "off_a" || first.ConnectionID != "conn_a" || first.AdapterType != "fake" || first.NativeRef != "native_a" {
		t.Fatalf("candidate audit data missing or unstable: %+v", decision.Candidates)
	}
}

func schedulerRevision() domain.WorkloadRevision {
	return domain.WorkloadRevision{
		ID:          "wrev_1",
		WorkspaceID: "ws_1",
		WorkloadID:  "wrk_1",
		Digest:      "sha256:revision",
		Spec: domain.WorkloadSpec{
			Containers: []domain.ContainerSpec{{
				Name:     "main",
				Image:    "ghcr.io/acme/inference@sha256:0000000000000000000000000000000000000000000000000000000000000000",
				Platform: domain.Platform{OS: "linux", Architecture: "amd64"},
				Ports: []domain.PortSpec{{
					Name: "http", ContainerPort: 8080, Protocol: "tcp", Exposure: domain.PortExposurePublic,
				}},
			}},
			Resources: domain.ResourceRequirements{
				CPU:           domain.CPURequirement{MinMillis: 4000},
				Memory:        domain.MemoryRequirement{MinBytes: 8 << 30},
				EphemeralDisk: domain.DiskRequirement{MinBytes: 40 << 30},
			},
			Network: domain.NetworkRequirements{
				Inbound: domain.InboundNetworkPublicPort,
				Download: &domain.NetworkDownloadRequirement{
					Scope:                    domain.NetworkScopeRegistry,
					MinP10Mbps:               500,
					MaxMeasurementAgeSeconds: 86400,
					AllowUnknown:             false,
				},
			},
			Placement: domain.PlacementPolicy{Objective: domain.ObjectiveBalanced, MaxP90StartSeconds: 180, ExpectedRuntimeSeconds: 900},
		},
	}
}

func schedulerOffer(id string, now time.Time, ratePerSecondUSD float64, startSeconds float64) domain.OfferSnapshot {
	return domain.OfferSnapshot{
		ID:           id,
		ConnectionID: "conn_1",
		AdapterType:  "fake",
		Kind:         domain.OfferKindStanding,
		ObservedAt:   now.Add(-time.Minute),
		ExpiresAt:    now.Add(time.Minute),
		Platform:     domain.Platform{OS: "linux", Architecture: "amd64"},
		Resources: domain.ResourceInventory{
			CPUMillis:          8000,
			MemoryBytes:        16 << 30,
			EphemeralDiskBytes: 80 << 30,
		},
		Capabilities: domain.CapabilityProfile{
			Container: domain.ContainerCapabilities{MaxContainers: 1, SupportsDigestRefs: true, MaxEnvironmentBytes: 32768},
			Network:   domain.NetworkCapabilities{Inbound: domain.InboundNetworkPublicPort, Protocols: []string{"tcp"}, PublicIPv4: true},
			Pricing:   domain.PricingCapabilities{Known: true},
			Lifecycle: domain.LifecycleCapabilities{IdempotentLaunch: "deterministic_name", ListOwned: true, CancelQueued: true},
		},
		Network: domain.NetworkFacts{Download: []domain.NetworkFact{{
			Scope:      domain.NetworkScopeRegistry,
			Statistic:  "p10",
			ValueMbps:  750,
			ObservedAt: now.Add(-time.Hour),
			ValidUntil: now.Add(time.Hour),
			Confidence: 0.9,
		}}},
		Pricing:  domain.PriceModel{Currency: "USD", RatePerSecondUSD: ratePerSecondUSD, Known: true, GranularitySeconds: 1},
		Queue:    &domain.QueueSnapshot{QueuedWorkSeconds: startSeconds},
		Capacity: domain.CapacityEvidence{Available: true, Confidence: 1},
		ImageCache: domain.ImageCacheEvidence{
			ManifestCached: true,
			MissingBytes:   0,
			Known:          true,
		},
		Reliability: domain.ReliabilityEvidence{
			StartFailureRate: 0.01,
			InterruptionRate: 0.01,
			Confidence:       1,
		},
	}
}

func candidateIDs(decision domain.PlacementDecision) string {
	ids := ""
	for _, candidate := range decision.Candidates {
		if ids != "" {
			ids += ","
		}
		ids += candidate.OfferSnapshotID
	}
	return ids
}

func assertCandidateRejected(t *testing.T, decision domain.PlacementDecision, offerID, code, path string) {
	t.Helper()
	for _, candidate := range decision.Candidates {
		if candidate.OfferSnapshotID != offerID {
			continue
		}
		for _, rejection := range candidate.Rejections {
			if rejection.Code == code && rejection.Path == path {
				return
			}
		}
		t.Fatalf("candidate %s missing rejection code=%s path=%s: %+v", offerID, code, path, candidate)
	}
	t.Fatalf("candidate %s not found in %+v", offerID, decision.Candidates)
}

func findCandidate(t *testing.T, decision domain.PlacementDecision, offerID string) domain.CandidateDecision {
	t.Helper()
	for _, candidate := range decision.Candidates {
		if candidate.OfferSnapshotID == offerID {
			return candidate
		}
	}
	t.Fatalf("candidate %s not found in %+v", offerID, decision.Candidates)
	return domain.CandidateDecision{}
}
