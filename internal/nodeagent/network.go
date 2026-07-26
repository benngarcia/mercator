package nodeagent

import (
	"sync"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
)

// This file is the node's half of the transfer model. Everything else about how
// long content takes to reach a machine is Mercator's own stated assumption,
// applied identically to every machine in the fleet, so a host beside the object
// store and a host across the country from it were predicted the same seconds for
// the same forty gigabytes. Nothing measured either of them, because nothing on a
// node was measuring anything.
//
// A node is the only thing that can. It is the process that moves the bytes, so
// it holds both halves of a throughput at the moment the transfer ends, and it
// needs no probe traffic of its own: every reading published here is work the
// control plane already asked this machine to do.

// minimumMeasuredBytes is how much content a transfer has to move before its
// duration says anything about throughput. A small read is dominated by the round
// trip to the object store and by whatever the store took to start answering, so
// reporting it as a rate would publish a measurement of latency under the name of
// a measurement of bandwidth, and the number would be wrong in the direction that
// loses placements: too slow, on a machine that is fine.
const minimumMeasuredBytes = 4_000_000

// MeasuredLinkConfidence is how much a node stands behind its own reading of a
// path. It is more than domain.AssumedLinkConfidence, which is what Mercator's
// fleet-wide guess about an unmeasured path is worth, and deliberately short of
// certainty: what is published is the slowest transfer this node has seen, which
// is a floor over a handful of readings rather than a distribution.
//
// The figure itself is stated rather than derived, and that is the honest
// description of it. Deriving it from the sample count would be an estimator this
// slice has not measured, and predicted-versus-actual for the fetch stage is what
// a calibration slice will replace it with.
const MeasuredLinkConfidence = 0.9

// MeasuredLinkValidity is how long a reading of a path stands. A measurement is
// evidence about the path as it was, and a node that has moved nothing for an
// hour is not evidence about the path now: it keeps reporting its liveness, its
// containers, and its disk, so without an expiry its oldest reading would be
// published as a current fact forever.
const MeasuredLinkValidity = time.Hour

// measurableScopes is every kind of path this node can time itself crossing,
// which is one: it streams Artifact content out of the object store, so it holds
// the bytes and the duration at the moment the read ends. Pulling an image is the
// registry path and is not here, because the daemon does that work and reports
// neither number. Publishing a registry rate derived from anything else would be
// an inference dressed as a measurement.
//
// It is a stated list rather than the map's own keys so the order a node publishes
// its facts in is fixed. An offer is canonically hashed, and facts that reordered
// between two reports of one unchanged machine would be two different offers.
var measurableScopes = []domain.NetworkScope{domain.NetworkScopeObjectStore}

// pathMeasurements is what this node has measured about each kind of path it
// crosses. It keeps the slowest reading rather than the latest or the mean,
// because the quantile every reader in this tree asks for is the pessimistic one:
// a fleet that published its best transfer would promise a rate its machines
// routinely miss, and a Run with a hard floor on throughput would be admitted
// onto them.
type pathMeasurements struct {
	mu       sync.Mutex
	observed map[domain.NetworkScope]pathReading
}

// pathReading is one path as this node has found it: the slowest throughput it
// has seen, how many transfers it has seen at all, and when the last of them
// finished.
type pathReading struct {
	slowestMbps float64
	samples     int
	at          time.Time
}

func newPathMeasurements() *pathMeasurements {
	return &pathMeasurements{observed: map[domain.NetworkScope]pathReading{}}
}

// record is one completed transfer offered as evidence about the path it crossed.
// A transfer too small to say anything about bandwidth is not evidence and is
// dropped rather than averaged in, and neither is one that took no measurable
// time: a duration rounded to zero divides into an infinite rate.
func (measurements *pathMeasurements) record(scope domain.NetworkScope, bytes int64, elapsed time.Duration, at time.Time) {
	if bytes < minimumMeasuredBytes || elapsed <= 0 {
		return
	}
	mbps := float64(bytes*8) / 1_000_000 / elapsed.Seconds()
	measurements.mu.Lock()
	defer measurements.mu.Unlock()
	previous, seen := measurements.observed[scope]
	reading := pathReading{slowestMbps: mbps, samples: previous.samples + 1, at: at.UTC()}
	if seen && previous.slowestMbps < mbps {
		reading.slowestMbps = previous.slowestMbps
	}
	measurements.observed[scope] = reading
}

// facts is what this node publishes about the paths it has measured. A path it
// has never crossed produces nothing at all, which is the silence Placement
// prices rather than an absence of speed, and a reading past its own validity
// produces nothing either: the node stops standing behind it rather than
// restating it as current.
func (measurements *pathMeasurements) facts(at time.Time) []domain.NetworkFact {
	measurements.mu.Lock()
	defer measurements.mu.Unlock()
	var published []domain.NetworkFact
	for _, scope := range measurableScopes {
		reading, measured := measurements.observed[scope]
		if !measured || !at.Before(reading.at.Add(MeasuredLinkValidity)) {
			continue
		}
		published = append(published, domain.NetworkFact{
			Scope:       scope,
			Statistic:   "p10",
			ValueMbps:   reading.slowestMbps,
			Source:      "node_transfer",
			SampleCount: reading.samples,
			ObservedAt:  reading.at,
			ValidUntil:  reading.at.Add(MeasuredLinkValidity),
			Confidence:  MeasuredLinkConfidence,
		})
	}
	return published
}
