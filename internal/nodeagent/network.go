package nodeagent

import (
	"cmp"
	"slices"
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

// ArtifactCopySource names what a node measured when it timed itself reading
// Artifact content, in the words of the process that did it. It is the whole
// copy: the bytes crossing the path, landing on this disk, and being hashed on
// the way past, because one read does all three jobs here and the node holds one
// duration over all of them.
//
// The name says so rather than claiming the link alone, and the difference is not
// pedantry. A machine on a ten gigabit path whose Artifact root is a slow disk
// delivers content at the disk's rate, and that rate is the honest answer to both
// questions anything asks this fact: how long the next read of forty gigabytes
// takes here, and whether a Run that states a floor on reading its dataset can be
// served by this machine. Timing the socket alone would answer neither, and would
// be wrong in the direction that wins placements the machine cannot honour.
const ArtifactCopySource = "node_artifact_copy"

// MeasuredLinkValidity is how long one reading of a path stands. A measurement is
// evidence about the path as it was, and a node that has moved nothing for an
// hour is not evidence about the path now: it keeps reporting its liveness, its
// containers, and its disk, so without an expiry its oldest reading would be
// published as a current fact forever.
//
// It is the life of a reading and never of the summary over them. A window that
// expired only when the node stopped transferring would keep the slowest reading
// this machine ever took alive for as long as it keeps working, which is the
// opposite of what an expiry is for: the machine that read once at a hundred
// megabits while a container shared its link would publish that for the rest of
// its life, and every transfer after it would refresh the date rather than retire
// the number.
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

// pathMeasurements is every transfer this node has timed and still stands behind,
// kept per kind of path. What it publishes is the slowest of them rather than the
// latest or the mean, because the quantile every reader in this tree asks for is
// the pessimistic one: a fleet that published its best transfer would promise a
// rate its machines routinely miss, and a Run with a hard floor on throughput
// would be admitted onto them.
//
// The readings are kept one by one rather than reduced to a running floor. A floor
// cannot be retired: the transfer that set it is the only thing that dates it, and
// a summary carrying one date for a value some earlier transfer measured is either
// a number nothing stands behind or a date nothing measured. Keeping the readings
// is what lets the slow one expire and the fleet see this machine as it is now,
// and the window bounds the collection for the same reason it bounds the fact.
type pathMeasurements struct {
	mu       sync.Mutex
	observed map[domain.NetworkScope][]pathReading
}

// pathReading is one completed transfer: the throughput it moved at, and the
// moment it finished. The moment travels with the number because it is what the
// published fact is dated and expired by, and a reading dated by some later
// transfer that measured something else is a measurement nothing took.
type pathReading struct {
	mbps float64
	at   time.Time
}

func newPathMeasurements() *pathMeasurements {
	return &pathMeasurements{observed: map[domain.NetworkScope][]pathReading{}}
}

// record is one completed transfer offered as evidence about the path it crossed.
// A transfer too small to say anything about bandwidth is not evidence and is
// dropped rather than averaged in, and neither is one that took no measurable
// time: a duration rounded to zero divides into an infinite rate.
func (measurements *pathMeasurements) record(scope domain.NetworkScope, bytes int64, elapsed time.Duration, at time.Time) {
	if bytes < minimumMeasuredBytes || elapsed <= 0 {
		return
	}
	reading := pathReading{mbps: float64(bytes*8) / 1_000_000 / elapsed.Seconds(), at: at.UTC()}
	measurements.mu.Lock()
	defer measurements.mu.Unlock()
	measurements.observed[scope] = append(measurements.standing(scope, at), reading)
}

// facts is what this node publishes about the paths it has measured. A path it
// has never crossed produces nothing at all, which is the silence Placement
// prices rather than an absence of speed, and a path whose every reading is past
// its validity produces nothing either: the node stops standing behind them
// rather than restating one as current.
//
// The fact is the slowest reading still standing, published as the transfer that
// took it, so what an operator reads is a throughput this machine really moved at
// and the moment it really moved at it.
func (measurements *pathMeasurements) facts(at time.Time) []domain.NetworkFact {
	measurements.mu.Lock()
	defer measurements.mu.Unlock()
	var published []domain.NetworkFact
	for _, scope := range measurableScopes {
		standing := measurements.standing(scope, at)
		measurements.observed[scope] = standing
		if len(standing) == 0 {
			continue
		}
		slowest := slices.MinFunc(standing, func(left, right pathReading) int {
			return cmp.Compare(left.mbps, right.mbps)
		})
		published = append(published, domain.NetworkFact{
			Scope:       scope,
			Statistic:   "p10",
			ValueMbps:   slowest.mbps,
			Source:      ArtifactCopySource,
			SampleCount: len(standing),
			ObservedAt:  slowest.at,
			ValidUntil:  slowest.at.Add(MeasuredLinkValidity),
			Confidence:  MeasuredLinkConfidence,
		})
	}
	return published
}

// standing is the readings of one path this node still stands behind as of a
// moment. Everything older than a reading's own validity is dropped rather than
// held: it describes the path as it used to be, and this node has since crossed
// the same path and knows better.
func (measurements *pathMeasurements) standing(scope domain.NetworkScope, at time.Time) []pathReading {
	return slices.DeleteFunc(measurements.observed[scope], func(reading pathReading) bool {
		return !at.Before(reading.at.Add(MeasuredLinkValidity))
	})
}
