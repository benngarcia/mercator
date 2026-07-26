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
	// Dated by the transfer that measured it, and not by the last one to finish.
	// A fact carrying one machine's slowest number under a later transfer's clock
	// asserts a measurement nothing took.
	if !facts[0].ObservedAt.Equal(at.Add(time.Minute)) {
		t.Fatalf("the node dated its 100 Mbps reading %s, and it measured 100 Mbps at %s",
			facts[0].ObservedAt.Format(time.RFC3339), at.Add(time.Minute).Format(time.RFC3339))
	}
}

// TestASlowReadingRetiresWhileTheNodeKeepsWorking is the expiry read against a
// machine that never stops. A node that shared its link with a container once and
// read at a tenth of its usual rate has to publish that floor while it stands, and
// has to stop publishing it an hour later, whatever it has done since: a floor
// that every later transfer re-dated would be this machine's worst hour published
// as its current speed for the rest of its life, and the only ways out would be an
// hour of doing nothing or restarting the agent.
func TestASlowReadingRetiresWhileTheNodeKeepsWorking(t *testing.T) {
	at := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	measurements := newPathMeasurements()

	measurements.record(domain.NetworkScopeObjectStore, 1_000_000_000, 80*time.Second, at)
	for reading := range 24 {
		measurements.record(domain.NetworkScopeObjectStore, 1_000_000_000, 8*time.Second, at.Add(time.Duration(reading+1)*30*time.Minute))
	}

	facts := measurements.facts(at.Add(12 * time.Hour))
	if len(facts) != 1 || facts[0].ValueMbps != 1000 {
		t.Fatalf("twelve hours of gigabit reads published %+v, and nothing measured 100 Mbps since noon", facts)
	}
	if facts[0].SampleCount != 2 {
		t.Fatalf("the node published %d samples, and two of its reads landed inside the hour it stands behind",
			facts[0].SampleCount)
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
