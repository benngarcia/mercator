package latency

import (
	"testing"
	"time"
)

func TestEstimatorRecordsObservationsAndEstimatesByOffer(t *testing.T) {
	estimator := New()
	now := time.Date(2026, 6, 20, 18, 31, 22, 0, time.UTC)
	estimator.Record(Observation{WorkspaceID: "ws_1", OfferSnapshotID: "offer_1", StartSeconds: 4, ObservedAt: now})
	estimator.Record(Observation{WorkspaceID: "ws_1", OfferSnapshotID: "offer_1", StartSeconds: 8, ObservedAt: now.Add(time.Second)})

	estimate := estimator.Estimate("ws_1", "offer_1")
	if estimate.Expected != 6 || estimate.P90 != 8 || estimate.SampleCount != 2 {
		t.Fatalf("unexpected estimate: %+v", estimate)
	}
}

func TestEstimatorReturnsZeroForUnknownOffer(t *testing.T) {
	estimate := New().Estimate("ws_1", "missing")
	if estimate.Expected != 0 || estimate.SampleCount != 0 {
		t.Fatalf("unexpected empty estimate: %+v", estimate)
	}
}
