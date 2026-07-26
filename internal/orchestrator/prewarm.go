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
//
// Both bounds are the fleet's rather than a tenant's. What they protect is
// shared by every tenant: one machine's link, and this process's own egress. A
// bound stated per workspace would let a deployment with ten of them begin ten
// transfers at once and hold ten times the depth in flight, which is the
// opposite of the restraint an operator configured.
type PrewarmPolicy struct {
	// MaxConcurrent is how many pieces of content may be arriving speculatively
	// at once, across every tenant. Zero turns preparation off, which is what a
	// deployment that has not configured it has.
	MaxConcurrent int
	// MinInterval is the shortest gap between two moments Mercator may begin
	// preparing. It bounds the rate rather than the depth: one desired set
	// crosses the boundary at once and may start as many transfers as
	// MaxConcurrent allows, and how often a new one may start is this. It is
	// deliberately not applied to withdrawals: stopping work that should not be
	// happening is never something to do less often.
	MinInterval time.Duration
}

// PreparationClock is where the moment Mercator last began preparing something
// is kept. It is durable state on purpose: a restart is not permission to start
// speculating again, and a control plane restarting in a loop with an in-process
// clock would begin a transfer on every boot, which is the rate bound not
// existing. It is one moment rather than one per tenant because the bound it
// serves is the fleet's.
type PreparationClock interface {
	LastBegan(ctx context.Context) (time.Time, bool, error)
	RecordBegan(ctx context.Context, at time.Time) error
}

// WithPrewarm gives Mercator somewhere to send preparation, the bounds it must
// stay inside, and the durable clock the rate bound is measured against. Without
// it nothing is ever prepared, which is what every deployment before this had:
// the prepare command path existed and no caller in the tree issued one.
func WithPrewarm(prewarmer Prewarmer, policy PrewarmPolicy, clock PreparationClock) Option {
	return func(o *Orchestrator) {
		o.prewarmer = prewarmer
		o.prewarmPolicy = policy
		o.preparationClock = clock
	}
}

// PrewarmResult is what one reconciliation of the fleet's desired set did.
type PrewarmResult struct {
	// Wanted is the content Mercator asked for, across every tenant, after both
	// bounds.
	Wanted int
	// Stated is how many tenants' desires crossed the boundary. An unchanged
	// desire is not restated: a machine already holding this exact instruction
	// learns nothing from hearing it again.
	Stated int
}

// prewarmMemory is what this control plane last asked each tenant for. It is
// in-process on purpose: each desired set is derived from the event log every
// time, so a restarted Mercator recomputes it and restates it, which the far
// side answers Duplicate, and persisting the sets would make a durable record of
// a cache.
//
// A restarted Mercator therefore cannot tell content it has already asked for
// from content it has not, so it states nothing at all until the rate bound
// allows a beginning. The price is that a withdrawal it discovers inside that
// window waits for the same interval, which is the operator's own number.
type prewarmMemory struct {
	mu   sync.Mutex
	sent map[string]map[string]bool
	key  map[string]string
}

func (memory *prewarmMemory) unchanged(workspaceID, key string) bool {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	return memory.key[workspaceID] == key
}

// withoutAdditions is this desire with every piece of content Mercator has not
// already asked for removed. It is what the rate bound leaves of a desire while
// it holds: restating what a machine is already fetching changes nothing, and
// dropping what is no longer wanted is not something to wait for.
func (memory *prewarmMemory) withoutAdditions(workspaceID string, wanted []adapter.PrepareItem) []adapter.PrepareItem {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	previous := memory.sent[workspaceID]
	kept := make([]adapter.PrepareItem, 0, len(wanted))
	for _, item := range wanted {
		if previous[prewarmItemKey(item)] {
			kept = append(kept, item)
		}
	}
	return kept
}

// remember records a desire Mercator has just stated, and answers whether
// stating it began any preparation. A desire naming only content this tenant was
// already asked for began no transfer, and neither did one that only drops
// content, so neither is a moment the rate bound measures from.
func (memory *prewarmMemory) remember(workspaceID, key string, wanted []adapter.PrepareItem) bool {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	if memory.sent == nil {
		memory.sent = map[string]map[string]bool{}
		memory.key = map[string]string{}
	}
	previous := memory.sent[workspaceID]
	asked := make(map[string]bool, len(wanted))
	began := false
	for _, item := range wanted {
		itemKey := prewarmItemKey(item)
		asked[itemKey] = true
		began = began || !previous[itemKey]
	}
	memory.sent[workspaceID] = asked
	memory.key[workspaceID] = key
	return began
}

// Prewarm reconciles what Mercator wants prepared with what it last asked for.
// It is a controller rather than a step in a Run's lifecycle: no Run is waiting
// on it, nothing it does changes a Run's recorded state, and a machine that
// refuses every request costs the fleet start latency and never correctness.
//
// It runs over every tenant in one pass because both bounds are fleet-wide, and
// a pass per workspace could not express either: what may be in flight at once
// has to be counted across the desires that are open together, and how often
// preparation may begin has to be measured over the moments it began at all.
func (o *Orchestrator) Prewarm(ctx context.Context) (PrewarmResult, error) {
	if o.prewarmer == nil || o.prewarmPolicy.MaxConcurrent <= 0 {
		return PrewarmResult{}, nil
	}
	if o.preparationClock == nil {
		return PrewarmResult{}, fmt.Errorf("orchestrator: preparation is configured with no durable clock to bound its rate")
	}
	workspaces, err := o.ListRunWorkspaces(ctx)
	if err != nil {
		return PrewarmResult{}, fmt.Errorf("orchestrator: read the tenants to prepare for: %w", err)
	}
	wanted, err := o.prewarmDesire(ctx, workspaces)
	if err != nil {
		return PrewarmResult{}, err
	}
	result := PrewarmResult{Wanted: len(wanted)}
	byTenant := itemsByWorkspace(wanted)
	for _, workspaceID := range workspaces {
		stated, err := o.stateDesire(ctx, workspaceID, byTenant[workspaceID])
		if err != nil {
			return PrewarmResult{}, err
		}
		if stated {
			result.Stated++
		}
	}
	return result, nil
}

// stateDesire hands one tenant's machines the content they should be holding,
// and answers whether that desire crossed the boundary at all.
func (o *Orchestrator) stateDesire(ctx context.Context, workspaceID string, wanted []adapter.PrepareItem) (bool, error) {
	holding, err := o.rateBoundHolds(ctx)
	if err != nil {
		return false, err
	}
	if holding {
		wanted = o.prewarmed.withoutAdditions(workspaceID, wanted)
	}
	key := prewarmOperationKey(workspaceID, wanted)
	if o.prewarmed.unchanged(workspaceID, key) {
		return false, nil
	}
	if _, err := o.prewarmer.Prepare(ctx, adapter.PrepareRequest{
		WorkspaceID:  workspaceID,
		OperationKey: key,
		Wanted:       wanted,
	}); err != nil {
		return false, fmt.Errorf("orchestrator: prepare capacity for queued work: %w", err)
	}
	if !o.prewarmed.remember(workspaceID, key, wanted) {
		return true, nil
	}
	if err := o.preparationClock.RecordBegan(ctx, o.now()); err != nil {
		return true, fmt.Errorf("orchestrator: record the moment preparation began: %w", err)
	}
	return true, nil
}

// rateBoundHolds answers whether the rate bound is holding new preparation back
// right now, whoever wants it. It is read from the durable clock every time
// rather than carried through the pass: a tenant that has just begun a transfer
// holds the next tenant for the same reason it holds itself, and a restarted
// control plane is held by what the last one did.
func (o *Orchestrator) rateBoundHolds(ctx context.Context) (bool, error) {
	if o.prewarmPolicy.MinInterval <= 0 {
		return false, nil
	}
	began, ever, err := o.preparationClock.LastBegan(ctx)
	if err != nil {
		return false, fmt.Errorf("orchestrator: read the moment preparation last began: %w", err)
	}
	return ever && o.now().Sub(began) < o.prewarmPolicy.MinInterval, nil
}

// prewarmWant is one piece of content one tenant wants on one machine, with the
// moment the Run waiting for it is projected to start. That moment is what
// orders the fleet's desire, so the depth bound spends its room on the work that
// starts soonest rather than on whichever tenant the log lists first.
type prewarmWant struct {
	workspaceID string
	runID       string
	startsAt    time.Time
	// rank is where this item sat among the content of its own Run, which keeps
	// a Run's image ahead of the Artifacts it reads: a machine without the image
	// cannot start the workload at all.
	rank int
	item adapter.PrepareItem
}

// prewarmDesire is everything Mercator would like prepared right now, fleet
// wide, in the order it would like it: the content of the Runs whose Bookings
// are queued, earliest projected start first whichever tenant they belong to,
// minus what the host already holds, minus every host still getting ready for
// work Mercator has already admitted there, truncated to what may be in flight
// at once.
func (o *Orchestrator) prewarmDesire(ctx context.Context, workspaces []string) ([]prewarmWant, error) {
	var wanted []prewarmWant
	for _, workspaceID := range workspaces {
		tenant, err := o.tenantDesire(ctx, workspaceID)
		if err != nil {
			return nil, err
		}
		wanted = append(wanted, tenant...)
	}
	slices.SortFunc(wanted, soonestFirst)
	if len(wanted) > o.prewarmPolicy.MaxConcurrent {
		wanted = wanted[:o.prewarmPolicy.MaxConcurrent]
	}
	return wanted, nil
}

// tenantDesire is one workspace's contribution to the fleet's desire, before the
// depth bound is spent on it.
func (o *Orchestrator) tenantDesire(ctx context.Context, workspaceID string) ([]prewarmWant, error) {
	queued, preparing, err := o.queuedPlacements(ctx, workspaceID)
	if err != nil || len(queued) == 0 {
		return nil, err
	}
	offers, err := o.adapter.ListOffers(ctx, adapter.OfferRequest{WorkspaceID: workspaceID})
	if err != nil {
		return nil, fmt.Errorf("orchestrator: read capacity to prepare: %w", err)
	}
	catalog := offersByID(offers)
	var wanted []prewarmWant
	seen := map[string]bool{}
	for _, placement := range queued {
		if preparing[placement.offer.ID] {
			continue
		}
		items, err := o.prewarmItems(ctx, workspaceID, placement)
		if err != nil {
			return nil, err
		}
		for rank, item := range items {
			key := prewarmItemKey(item)
			if seen[key] || alreadyHeld(catalog[item.OfferSnapshotID], item) {
				continue
			}
			seen[key] = true
			wanted = append(wanted, prewarmWant{
				workspaceID: workspaceID,
				runID:       placement.runID,
				startsAt:    placement.startsAt,
				rank:        rank,
				item:        item,
			})
		}
	}
	return wanted, nil
}

// soonestFirst orders the fleet's desire by when the work waiting for it starts.
// The tie-breaks are there so two reconciliations of one state produce one
// answer: a desired set that depended on map order would be restated forever.
func soonestFirst(left, right prewarmWant) int {
	if !left.startsAt.Equal(right.startsAt) {
		return left.startsAt.Compare(right.startsAt)
	}
	if left.workspaceID != right.workspaceID {
		return strings.Compare(left.workspaceID, right.workspaceID)
	}
	if left.runID != right.runID {
		return strings.Compare(left.runID, right.runID)
	}
	return left.rank - right.rank
}

// itemsByWorkspace splits the fleet's desire back into the desires each tenant
// is told, keeping the fleet's order inside each one.
func itemsByWorkspace(wanted []prewarmWant) map[string][]adapter.PrepareItem {
	byTenant := map[string][]adapter.PrepareItem{}
	for _, want := range wanted {
		byTenant[want.workspaceID] = append(byTenant[want.workspaceID], want.item)
	}
	return byTenant
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
