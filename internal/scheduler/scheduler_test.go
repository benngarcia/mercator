package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/bengarcia/mercator/internal/domain"
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
			Secrets:   domain.SecretDeliveryCapabilities{Delivery: "direct_env", CleanupSupported: true},
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
