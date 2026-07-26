package lab

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
)

// This file is the pair of rules that make speculative preparation safe to have
// at all. Prewarming is the one thing Mercator does that no Run is waiting on,
// which is exactly why it needs laws: nothing fails when it goes wrong, a
// machine simply spends its disk and its link on content nobody asked to run,
// and the Run that was admitted there waits behind it.

// prefetchConvergenceBound is how long a requested preparation may take to
// reach an answer. It is stated generously on purpose: this is a liveness rule
// about never sitting half-done forever, not a performance target, and a fixture
// moving sixty gigabytes over an assumed link takes a quarter of an hour of
// virtual time before anything is wrong.
const prefetchConvergenceBound = 2 * time.Hour

// preparation is one stretch of time during which content was moving onto one
// machine: what it was for, what it was, and when it started and ended. Open is
// preparation the ledger never resolved, which is the state a liveness rule is
// about and a safety rule has to treat as still in the way.
type preparation struct {
	offerID string
	content string
	runID   string
	from    time.Time
	until   time.Time
	open    bool
}

func (window preparation) overlaps(other preparation) bool {
	return window.from.Before(other.until) && other.from.Before(window.until)
}

// prewarmYieldsToRealWork is the standing guard on speculation. Two things hold
// at once.
//
// Nothing prepared for a Run Mercator has not admitted may ever be moving onto a
// machine at the same time as content a Run admitted there is waiting for. A
// node performs its commands in order and a link carries what it carries, so a
// prefetch that overlaps a launch's own fetch is a Run whose start Mercator
// itself delayed. The rule is stated as an overlap rather than as an ordering
// because "precedes" is not the failure: a prefetch that started first and is
// still running is exactly the one in the way.
//
// And no more content is arriving speculatively at one moment than the bound
// this world stated. The bound is the control plane's own restraint, because
// nothing below it enforces one: a machine asked for six transfers performs six.
//
// It is read from the ledger rather than from world state, because a prefetch
// that has already finished leaves nothing in world state and the question is
// about the moment it was happening.
func prewarmYieldsToRealWork(observation InvariantObservation) error {
	prefetches, err := prefetchWindows(observation)
	if err != nil {
		return err
	}
	admitted, err := admittedPreparationWindows(observation)
	if err != nil {
		return err
	}
	for _, prefetch := range prefetches {
		for _, real := range admitted {
			if prefetch.offerID != real.offerID || !prefetch.overlaps(real) {
				continue
			}
			return fmt.Errorf(
				"machine %q was fetching %q speculatively for Run %q while Run %q was waiting on %q to start there",
				prefetch.offerID, prefetch.content, prefetch.runID, real.runID, real.content,
			)
		}
	}
	return concurrentPrefetchWithinBound(observation, prefetches)
}

// prewarmRateWithinBound is the standing guard on how often Mercator may start
// speculating. It is the second half of the restraint the control plane puts on
// itself, and it needs a rule of its own because the depth bound cannot express
// it: a machine that is allowed one transfer at a time is still asked for a
// fresh one the instant the last finishes, and a control plane that reconciled
// its desired set on every tick would keep a host permanently busy with content
// nobody has asked to run.
//
// The gap is measured between the moments preparation started rather than
// between transfers, because one desired set crosses the boundary at once and
// may open as many transfers as the depth bound allows. How many may be moving
// together is concurrentPrefetchWithinBound's question; how often Mercator may
// begin is this one.
func prewarmRateWithinBound(observation InvariantObservation) error {
	if observation.Prewarm == nil || observation.Prewarm.MinInterval.Duration() <= 0 {
		return nil
	}
	prefetches, err := prefetchWindows(observation)
	if err != nil {
		return err
	}
	interval := observation.Prewarm.MinInterval.Duration()
	starts := prefetchStartMoments(prefetches)
	for index := 1; index < len(starts); index++ {
		gap := starts[index].Sub(starts[index-1])
		if gap >= interval {
			continue
		}
		return fmt.Errorf(
			"speculative preparation started at %s and again %s later at %s, and this world allows one no sooner than %s",
			starts[index-1].Format(time.RFC3339Nano), gap, starts[index].Format(time.RFC3339Nano), interval,
		)
	}
	return nil
}

// prefetchStartMoments is the distinct instants at which speculation began, in
// order. A preparation with nothing to move is already absent from the windows,
// which is what the rate bound is about: the interval governs when Mercator may
// put content on a link, and answering that a host is ready puts nothing there.
func prefetchStartMoments(prefetches []preparation) []time.Time {
	starts := make([]time.Time, 0, len(prefetches))
	for _, prefetch := range prefetches {
		starts = append(starts, prefetch.from)
	}
	slices.SortFunc(starts, time.Time.Compare)
	return slices.CompactFunc(starts, time.Time.Equal)
}

// concurrentPrefetchWithinBound sweeps the prefetch windows and reports the
// moment more of them were open at once than the world allows. A Blueprint that
// states no bound states no opinion, and a control plane with no bound prepares
// nothing, so there is nothing to check.
func concurrentPrefetchWithinBound(observation InvariantObservation, prefetches []preparation) error {
	if observation.Prewarm == nil {
		return nil
	}
	type edge struct {
		at    time.Time
		delta int
	}
	edges := make([]edge, 0, 2*len(prefetches))
	for _, prefetch := range prefetches {
		edges = append(edges, edge{at: prefetch.from, delta: 1}, edge{at: prefetch.until, delta: -1})
	}
	sort.Slice(edges, func(left, right int) bool {
		if edges[left].at.Equal(edges[right].at) {
			// An ending closes before a beginning opens at the same instant: one
			// transfer finishing as the next starts is a handover rather than an
			// overlap.
			return edges[left].delta < edges[right].delta
		}
		return edges[left].at.Before(edges[right].at)
	})
	open := 0
	for _, point := range edges {
		open += point.delta
		if open > observation.Prewarm.MaxConcurrent {
			return fmt.Errorf(
				"%d speculative fetches were in flight at %s, and this world allows %d",
				open, point.at.Format(time.RFC3339Nano), observation.Prewarm.MaxConcurrent,
			)
		}
	}
	return nil
}

// prefetchConverges is the bounded-liveness half. A preparation Mercator asked
// for reaches an answer: the content is on the machine, or Mercator stopped
// wanting it and the machine stopped fetching. What may never happen is the
// third state, a transfer holding a machine's room and its link for hours with
// nobody able to say whether it is still coming.
//
// Assumptions: virtual time advances, and the registry and object store this
// world serves content from keep answering. Under a world that stops serving
// content this rule is stating something about the world rather than about
// Mercator, which is why the assumptions are published beside it.
func prefetchConverges(observation InvariantObservation) error {
	prefetches, err := prefetchWindows(observation)
	if err != nil {
		return err
	}
	for _, prefetch := range prefetches {
		if !prefetch.open || observation.Now.Sub(prefetch.from) <= prefetchConvergenceBound {
			continue
		}
		return fmt.Errorf(
			"machine %q has been preparing %q for %s and has neither finished nor been told to stop",
			prefetch.offerID, prefetch.content, observation.Now.Sub(prefetch.from),
		)
	}
	return nil
}

// prefetchWindows is every speculative transfer the ledger records, with the
// moment it stopped. A preparation with nothing to move is not a window: the
// machine already held the content and no time passed.
func prefetchWindows(observation InvariantObservation) ([]preparation, error) {
	settled, err := prefetchSettlements(observation.Effects)
	if err != nil {
		return nil, err
	}
	var windows []preparation
	for _, effect := range observation.Effects {
		if effect.Operation != OperationNodePrepareImage && effect.Operation != OperationNodePrepareArtifact {
			continue
		}
		if effect.Command != EffectCommandAccepted {
			continue
		}
		var request struct {
			OfferID string `json:"offer_id"`
			Content string `json:"content"`
			RunID   string `json:"run_id"`
		}
		var started struct {
			Ready bool `json:"ready"`
		}
		if err := json.Unmarshal(effect.Request, &request); err != nil {
			return nil, fmt.Errorf("decode preparation %s: %w", effect.ID, err)
		}
		if err := json.Unmarshal(effect.Consequence, &started); err != nil {
			return nil, fmt.Errorf("decode preparation consequence %s: %w", effect.ID, err)
		}
		if started.Ready {
			continue
		}
		window := preparation{
			offerID: request.OfferID,
			content: request.Content,
			runID:   request.RunID,
			from:    effect.At,
			// A transfer nothing resolved is still running now, which is what
			// makes it something a launch can be behind.
			until: later(observation.Now, effect.At),
			open:  true,
		}
		if settledAt, resolved := settled[request.OfferID+"/"+request.Content]; resolved {
			window.until = settledAt
			window.open = false
		}
		windows = append(windows, window)
	}
	return windows, nil
}

// prefetchSettlements is when each speculative transfer stopped, whichever way
// it stopped: the content landed and the host kept it, or Mercator withdrew the
// desire and the machine gave the room back. Both are answers; only their
// absence is not.
func prefetchSettlements(effects []EffectRecord) (map[string]time.Time, error) {
	settled := map[string]time.Time{}
	settle := func(key string, at time.Time) {
		if _, already := settled[key]; !already {
			settled[key] = at
		}
	}
	for _, effect := range effects {
		if effect.Command != EffectCommandAccepted {
			continue
		}
		var request struct {
			OfferID    string `json:"offer_id"`
			Source     string `json:"source"`
			Image      string `json:"image"`
			ArtifactID string `json:"artifact_id"`
			Content    string `json:"content"`
		}
		if err := json.Unmarshal(effect.Request, &request); err != nil {
			return nil, fmt.Errorf("decode preparation settlement %s: %w", effect.ID, err)
		}
		switch effect.Operation {
		case OperationImageRetained:
			if request.Source == "prewarm" {
				settle(request.OfferID+"/"+domain.ReferenceDigest(request.Image), effect.At)
			}
		case OperationArtifactReplicated:
			if request.Source == "prewarm" {
				settle(request.OfferID+"/"+request.ArtifactID, effect.At)
			}
		case OperationNodePrepareAbandoned:
			settle(request.OfferID+"/"+request.Content, effect.At)
		}
	}
	return settled, nil
}

// admittedPreparationWindows is every stretch during which a Run Mercator had
// already admitted was waiting on content to reach the machine it was launched
// on. It is the pull the launch dispatched and the reads that launch made out of
// the object store, which are the two things that stand between a container
// being accepted and a process starting.
func admittedPreparationWindows(observation InvariantObservation) ([]preparation, error) {
	var windows []preparation
	for _, effect := range observation.Effects {
		if effect.Command != EffectCommandAccepted {
			continue
		}
		if effect.Operation != OperationImagePull && effect.Operation != OperationArtifactRead {
			continue
		}
		var request struct {
			OfferID    string `json:"offer_id"`
			Image      string `json:"image"`
			ArtifactID string `json:"artifact_id"`
		}
		var moved struct {
			CompletesAt  time.Time `json:"completes_at"`
			Source       string    `json:"source"`
			FetchedBytes int64     `json:"fetched_bytes"`
		}
		if err := json.Unmarshal(effect.Request, &request); err != nil {
			return nil, fmt.Errorf("decode admitted preparation %s: %w", effect.ID, err)
		}
		if err := json.Unmarshal(effect.Consequence, &moved); err != nil {
			return nil, fmt.Errorf("decode admitted preparation consequence %s: %w", effect.ID, err)
		}
		// A launch onto a machine already holding what it needs moved nothing and
		// waited for nothing, so it is not a window anything could be in the way
		// of. That is true of a pull with no bytes to fetch and of a read served
		// by a copy that was already here.
		if effect.Operation == OperationImagePull && moved.FetchedBytes == 0 {
			continue
		}
		if effect.Operation == OperationArtifactRead && moved.Source != "object_store" {
			continue
		}
		content := request.Image
		if content == "" {
			content = request.ArtifactID
		}
		windows = append(windows, preparation{
			offerID: request.OfferID,
			content: content,
			runID:   effect.CorrelationID,
			from:    effect.At,
			until:   moved.CompletesAt,
		})
	}
	return slices.Clip(windows), nil
}
