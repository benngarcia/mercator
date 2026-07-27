package orchestrator

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/domain"
)

// Prewarmer is capacity Mercator can ask to get ready for work it has not
// admitted. It takes the whole desired set at once because that is the only
// shape a control plane can reconcile: preparation Mercator has stopped wanting
// has to stop, and stopping is an absence rather than an order.
type Prewarmer interface {
	Prepare(ctx context.Context, request adapter.PrepareRequest) (adapter.PrepareReceipt, error)
}

// PrewarmPolicy is the restraint Mercator puts on itself. Nothing below the
// control plane enforces either bound: a machine asked for six transfers
// performs six, and what suffers is whatever was already fetching there.
type PrewarmPolicy struct {
	// MaxConcurrent is how many pieces of content may be arriving speculatively
	// at once. Zero turns preparation off, which is what a deployment that has
	// not configured it has.
	MaxConcurrent int
	// MinInterval is the shortest gap between two moments Mercator may begin
	// preparing. It bounds the rate rather than the depth: one desired set
	// crosses the boundary at once and may start as many transfers as
	// MaxConcurrent allows, and how often a new one may start is this. It is
	// deliberately not applied to withdrawals: stopping work that should not be
	// happening is never something to do less often.
	MinInterval time.Duration
}

// WithPrewarm gives Mercator somewhere to send preparation and the bounds it
// must stay inside. Without it nothing is ever prepared, which is what every
// deployment before this had: the prepare command path existed and no caller
// in the tree issued one.
func WithPrewarm(prewarmer Prewarmer, policy PrewarmPolicy) Option {
	return func(o *Orchestrator) {
		o.prewarmer = prewarmer
		o.prewarmPolicy = policy
	}
}

// PrewarmResult is what one reconciliation of the desired set did.
type PrewarmResult struct {
	// Wanted is the content Mercator asked for, after both bounds.
	Wanted int
	// Sent reports whether the desired set crossed the boundary at all. An
	// unchanged desire is not resent: a machine already holding this exact
	// instruction learns nothing from hearing it again.
	Sent    bool
	Receipt adapter.PrepareReceipt
}

// prewarmMemory is what this control plane last asked for, per workspace. It is
// in-process on purpose: the desired set is derived from the event log every
// time, so a restarted Mercator recomputes it and resends, which the far side
// answers Duplicate. Persisting it would make a durable record of a cache.
type prewarmMemory struct {
	mu   sync.Mutex
	sent map[string]map[string]bool
	key  map[string]string
	at   map[string]time.Time
}

func (memory *prewarmMemory) unchanged(workspaceID, key string) bool {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	return memory.key[workspaceID] == key
}

// tooSoon answers whether the rate bound holds this desire back. A desire that
// only drops content is never held: the machine is spending disk and bandwidth
// on work that will never happen, and waiting out an interval before saying so
// is the opposite of what the bound is for.
func (memory *prewarmMemory) tooSoon(workspaceID string, now time.Time, interval time.Duration, adds bool) bool {
	if !adds || interval <= 0 {
		return false
	}
	memory.mu.Lock()
	defer memory.mu.Unlock()
	last, sent := memory.at[workspaceID]
	return sent && now.Sub(last) < interval
}

func (memory *prewarmMemory) record(workspaceID, key string, wanted []adapter.PrepareItem, now time.Time) {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	if memory.sent == nil {
		memory.sent = map[string]map[string]bool{}
		memory.key = map[string]string{}
		memory.at = map[string]time.Time{}
	}
	asked := make(map[string]bool, len(wanted))
	for _, item := range wanted {
		asked[prewarmItemKey(item)] = true
	}
	memory.sent[workspaceID] = asked
	memory.key[workspaceID] = key
	memory.at[workspaceID] = now
}

// adds reports whether this desire names content the memory has not asked for.
// A desire that only drops content is not an addition, and the rate bound is
// deliberately blind to it.
func (memory *prewarmMemory) adds(workspaceID string, wanted []adapter.PrepareItem) bool {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	previous := memory.sent[workspaceID]
	for _, item := range wanted {
		if !previous[prewarmItemKey(item)] {
			return true
		}
	}
	return false
}

// Prewarm reconciles what Mercator wants prepared with what it last asked for.
// It is a controller rather than a step in a Run's lifecycle: no Run is waiting
// on it, nothing it does changes a Run's recorded state, and a machine that
// refuses every request costs the fleet start latency and never correctness.
func (o *Orchestrator) Prewarm(ctx context.Context, workspaceID string) (PrewarmResult, error) {
	if o.prewarmer == nil || o.prewarmPolicy.MaxConcurrent <= 0 {
		return PrewarmResult{}, nil
	}
	wanted, err := o.prewarmDesire(ctx, workspaceID)
	if err != nil {
		return PrewarmResult{}, err
	}
	key := prewarmOperationKey(workspaceID, wanted)
	if o.prewarmed.unchanged(workspaceID, key) {
		return PrewarmResult{Wanted: len(wanted)}, nil
	}
	if o.prewarmed.tooSoon(workspaceID, o.now(), o.prewarmPolicy.MinInterval, o.prewarmed.adds(workspaceID, wanted)) {
		return PrewarmResult{Wanted: len(wanted)}, nil
	}
	receipt, err := o.prewarmer.Prepare(ctx, adapter.PrepareRequest{
		WorkspaceID:  workspaceID,
		OperationKey: key,
		Wanted:       wanted,
	})
	if err != nil {
		return PrewarmResult{}, fmt.Errorf("orchestrator: prepare capacity for queued work: %w", err)
	}
	o.prewarmed.record(workspaceID, key, wanted, o.now())
	return PrewarmResult{Wanted: len(wanted), Sent: true, Receipt: receipt}, nil
}

// prewarmDesire is everything Mercator would like prepared right now, in the
// order it would like it: the content of the Runs whose Bookings are queued,
// earliest projected start first, minus what the host already holds, minus
// every host still getting ready for work Mercator has already admitted there,
// truncated to what may be in flight at once.
func (o *Orchestrator) prewarmDesire(ctx context.Context, workspaceID string) ([]adapter.PrepareItem, error) {
	queued, preparing, err := o.queuedPlacements(ctx, workspaceID)
	if err != nil || len(queued) == 0 {
		return nil, err
	}
	collected, err := o.adapter.CollectOffers(ctx, adapter.OfferRequest{WorkspaceID: workspaceID})
	if err != nil {
		return nil, fmt.Errorf("orchestrator: read capacity to prepare: %w", err)
	}
	catalog := offersByID(collected.Offers)
	var wanted []adapter.PrepareItem
	seen := map[string]bool{}
	for _, placement := range queued {
		if preparing[placement.offer.ID] {
			continue
		}
		items, err := o.prewarmItems(ctx, workspaceID, placement)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			key := prewarmItemKey(item)
			if seen[key] || alreadyHeld(catalog[item.OfferSnapshotID], item) {
				continue
			}
			seen[key] = true
			wanted = append(wanted, item)
		}
	}
	if len(wanted) > o.prewarmPolicy.MaxConcurrent {
		wanted = wanted[:o.prewarmPolicy.MaxConcurrent]
	}
	return wanted, nil
}

// queuedPlacement is one Run that has been given a machine and is waiting for
// it, which is the only Run whose future host Mercator knows.
type queuedPlacement struct {
	runID    string
	workload domain.WorkloadRevision
	offer    domain.OfferSnapshot
	startsAt time.Time
}

// queuedPlacements reads the two things a desired set is built from: the Runs
// waiting on a machine, and the machines still getting ready for a Run that is
// already admitted there.
//
// The second is what keeps speculation out of the way of real work. Nothing
// below the control plane reports that an image is still landing, because a
// provider says running from the moment it accepts a launch, so what a host is
// still doing for admitted work is Mercator's own prediction measured from when
// the launch was taken. Believing its own estimate is the honest answer here:
// it is the number the placement was made on, and a control plane that ignored
// it would be prepared to contradict itself.
func (o *Orchestrator) queuedPlacements(ctx context.Context, workspaceID string) ([]queuedPlacement, map[string]bool, error) {
	runIDs, err := o.listOpenRunIDs(ctx, workspaceID)
	if err != nil {
		return nil, nil, err
	}
	var queued []queuedPlacement
	preparing := map[string]bool{}
	for _, runID := range runIDs {
		events, err := o.GetRunEvents(ctx, workspaceID, runID)
		if err != nil {
			return nil, nil, err
		}
		state, err := reduceRun(events)
		if err != nil {
			return nil, nil, err
		}
		if host, until, admitted := admittedPreparation(state); admitted && o.now().Before(until) {
			preparing[host] = true
		}
		placement, ok := queuedPlacementOf(runID, state)
		if !ok {
			continue
		}
		queued = append(queued, placement)
	}
	sort.Slice(queued, func(left, right int) bool {
		if queued[left].startsAt.Equal(queued[right].startsAt) {
			return queued[left].runID < queued[right].runID
		}
		return queued[left].startsAt.Before(queued[right].startsAt)
	})
	return queued, preparing, nil
}

// queuedPlacementOf is one Run whose Booking is queued on a machine, with the
// content it will need and when Mercator projected it starting. A Run whose
// caller has already withdrawn it is not one: its Booking is about to be
// released, and preparing for it is exactly the waste this controller exists to
// avoid.
func queuedPlacementOf(runID string, state runState) (queuedPlacement, bool) {
	if !state.bookingQueued() || state.cancelRequested || state.closed || state.requested == nil {
		return queuedPlacement{}, false
	}
	selected := selectedDecisionCandidate(*state.bookingDecision)
	if selected == nil {
		return queuedPlacement{}, false
	}
	startsAt := time.Time{}
	if projected := state.bookingDecision.Booking.ProjectedStartAt; projected != nil {
		startsAt = *projected
	}
	return queuedPlacement{
		runID:    runID,
		workload: state.requested.Workload,
		offer: domain.OfferSnapshot{
			ID:           selected.OfferSnapshotID,
			ConnectionID: selected.ConnectionID,
			AdapterType:  selected.AdapterType,
			NativeRef:    selected.NativeRef,
		},
		startsAt: startsAt,
	}, true
}

// admittedPreparation is the host one admitted Run is still getting ready on,
// and the moment Mercator predicted that preparation ending.
func admittedPreparation(state runState) (string, time.Time, bool) {
	if !state.launchAccepted || state.launchIntent == nil || state.bookingDecision == nil {
		return "", time.Time{}, false
	}
	selected := selectedDecisionCandidate(*state.bookingDecision)
	if selected == nil {
		return "", time.Time{}, false
	}
	ready := state.launchAcceptedAt.Add(time.Duration(selected.Estimates.StartSeconds.Expected * float64(time.Second)))
	return state.launchIntent.SelectedOfferSnapshotID, ready, true
}

// prewarmItems is the content one queued Run will need where it is going: the
// image its container runs, and the immutable versions it declared reading.
// Each Artifact carries the durable location the catalog names, which is what a
// machine reads it from: the control plane mints the read, so no object-store
// credential of Mercator's is ever on a node.
func (o *Orchestrator) prewarmItems(ctx context.Context, workspaceID string, placement queuedPlacement) ([]adapter.PrepareItem, error) {
	item := adapter.PrepareItem{
		Kind:            adapter.PrepareImage,
		OfferSnapshotID: placement.offer.ID,
		ConnectionID:    placement.offer.ConnectionID,
		AdapterType:     placement.offer.AdapterType,
		NativeRef:       placement.offer.NativeRef,
		RunID:           placement.runID,
	}
	containers := placement.workload.Spec.Containers
	if len(containers) == 0 {
		return nil, nil
	}
	item.Image = containers[0].Image
	item.Platform = containers[0].Platform
	items := []adapter.PrepareItem{item}
	versions, err := o.consumedArtifacts(ctx, workspaceID, placement.workload)
	if err != nil {
		return nil, err
	}
	for _, version := range versions {
		if !version.Durable() {
			continue
		}
		items = append(items, adapter.PrepareItem{
			Kind:            adapter.PrepareArtifact,
			OfferSnapshotID: placement.offer.ID,
			ConnectionID:    placement.offer.ConnectionID,
			AdapterType:     placement.offer.AdapterType,
			NativeRef:       placement.offer.NativeRef,
			RunID:           placement.runID,
			ArtifactID:      version.ID,
			ContentDigest:   version.ContentDigest,
			Source:          version.Location,
			SizeBytes:       version.SizeBytes,
		})
	}
	return items, nil
}

// alreadyHeld drops content this machine has established it is holding. Silence
// is not absence and is not presence either: a host that cannot enumerate is
// asked to prepare, and the answer costs one command on a machine that may
// already be ready.
func alreadyHeld(offer domain.OfferSnapshot, item adapter.PrepareItem) bool {
	if item.Kind == adapter.PrepareImage {
		return offer.Images.Known && offer.Images.Holds(domain.ReferenceDigest(item.Image))
	}
	if !offer.Artifacts.Known {
		return false
	}
	replica, held := offer.Artifacts.Replica(item.ArtifactID)
	return held && replica.State.Usable() && replica.ContentDigest == item.ContentDigest
}

// selectedDecisionCandidate is the candidate a Booking Decision landed on, which
// is where the estimates and the connection identity for the machine live.
func selectedDecisionCandidate(decision domain.BookingDecision) *domain.CandidateDecision {
	for index := range decision.Candidates {
		if decision.Candidates[index].OfferSnapshotID == decision.SelectedOfferSnapshotID {
			return &decision.Candidates[index]
		}
	}
	return nil
}

func offersByID(offers []domain.OfferSnapshot) map[string]domain.OfferSnapshot {
	catalog := make(map[string]domain.OfferSnapshot, len(offers))
	for _, offer := range offers {
		catalog[offer.ID] = offer
	}
	return catalog
}

// prewarmItemKey names one piece of content on one machine, which is the
// identity two Runs wanting the same image on the same host share.
func prewarmItemKey(item adapter.PrepareItem) string {
	return string(item.Kind) + ":" + item.OfferSnapshotID + "/" + item.Content()
}

// prewarmOperationKey is the identity of one desired state. Two reconciliations
// reaching the same answer produce the same key, which is what lets the far
// side treat a redelivered desire as the same desire and lets this controller
// stay quiet when nothing has changed.
func prewarmOperationKey(workspaceID string, wanted []adapter.PrepareItem) string {
	// Wanting nothing is the empty key, which is what a control plane that has
	// never asked for anything is already holding. Without that, every Mercator
	// with no queued work would send one withdrawal of nothing at startup.
	if len(wanted) == 0 {
		return ""
	}
	keys := make([]string, 0, len(wanted))
	for _, item := range wanted {
		keys = append(keys, prewarmItemKey(item))
	}
	slices.Sort(keys)
	return "prewarm:" + workspaceID + ":" + strings.Join(keys, ",")
}
