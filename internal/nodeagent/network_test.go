package nodeagent

import (
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
)

// TestANodePublishesTheSlowestTransferItHasSeen is the quantile every reader in
// this tree asks for. A p10 is the pessimistic answer, so a node that has read
// once at a gigabit and once at a tenth of that has to publish the tenth: the
// number is read by a Run's hard floor on throughput and by the prediction of how
// long the next read takes, and a fleet publishing its best transfers would
// promise rates its machines routinely miss.
func TestANodePublishesTheSlowestTransferItHasSeen(t *testing.T) {
	at := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	measurements := newPathMeasurements()

	measurements.record(domain.NetworkScopeObjectStore, 1_000_000_000, 8*time.Second, at)
	measurements.record(domain.NetworkScopeObjectStore, 1_000_000_000, 80*time.Second, at.Add(time.Minute))
	measurements.record(domain.NetworkScopeObjectStore, 1_000_000_000, 4*time.Second, at.Add(2*time.Minute))

	facts := measurements.facts(at.Add(3 * time.Minute))
	if len(facts) != 1 {
		t.Fatalf("the node published %+v, want one reading of the one path it crossed", facts)
	}
	if facts[0].ValueMbps != 100 {
		t.Fatalf("the node published %v Mbps, and the slowest of its three reads was 100", facts[0].ValueMbps)
	}
	if facts[0].SampleCount != 3 {
		t.Fatalf("the node published %d samples, and it has timed three transfers", facts[0].SampleCount)
	}
	if !facts[0].ObservedAt.Equal(at.Add(2 * time.Minute)) {
		t.Fatalf("the node dated its reading %s, and it last measured this path at %s",
			facts[0].ObservedAt.Format(time.RFC3339), at.Add(2*time.Minute).Format(time.RFC3339))
	}
}

// TestATransferTooSmallToBeARateIsNotEvidence separates a measurement of
// bandwidth from a measurement of the round trip. A tiny object arrives in
// whatever the store took to start answering, so reporting its duration as a
// throughput publishes latency under bandwidth's name, and it is wrong in the
// direction that loses placements: too slow, on a machine that is fine.
func TestATransferTooSmallToBeARateIsNotEvidence(t *testing.T) {
	at := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	measurements := newPathMeasurements()

	measurements.record(domain.NetworkScopeObjectStore, minimumMeasuredBytes-1, 200*time.Millisecond, at)

	if facts := measurements.facts(at); len(facts) != 0 {
		t.Fatalf("the node published %+v from a read too small to say anything about the path", facts)
	}
}

// TestAReadingPastItsValidityIsNotPublished is what keeps a node's oldest
// measurement from becoming a permanent fact. A node keeps reporting its
// liveness, its containers, and its disk whether or not it has moved any content,
// so without this a machine that read a dataset once last month would still be
// publishing that morning's throughput as current.
func TestAReadingPastItsValidityIsNotPublished(t *testing.T) {
	at := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	measurements := newPathMeasurements()
	measurements.record(domain.NetworkScopeObjectStore, 1_000_000_000, 8*time.Second, at)

	if facts := measurements.facts(at.Add(MeasuredLinkValidity - time.Second)); len(facts) != 1 {
		t.Fatalf("the node published %+v inside the window it stands behind its own reading for", facts)
	}
	if facts := measurements.facts(at.Add(MeasuredLinkValidity)); len(facts) != 0 {
		t.Fatalf("the node published %+v about a path it has not crossed for an hour", facts)
	}
}
