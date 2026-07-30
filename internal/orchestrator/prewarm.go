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

// ContentCredentials is the control plane's authority to let one machine fetch
// one piece of content. It is asked here rather than at the Broker because the
// mint is a control-plane act: Mercator holds the registry account and the
// object-store key, decides that this machine may have this content now, and
// hands out material that says so and expires. A seam below this one would be a
// machine's own runtime deciding what it is allowed to read.
type ContentCredentials interface {
	// RegistryPull is the material for one pull of one digest-pinned reference.
	// Content any anonymous reader can have is minted nothing, which is the
	// answer rather than an error.
	RegistryPull(ctx context.Context, operation, workspaceID, reference string) (domain.RegistryPull, error)
	// ArtifactRead is one read of one durable location, minted as a URL that
	// expires.
	ArtifactRead(ctx context.Context, operation, workspaceID, artifactID, location string) (domain.ArtifactRead, error)
}

// WithContentCredentials gives Mercator the accounts a machine must never hold.
// Without it nothing is minted and every fetch a node makes is anonymous, which
// is what every deployment before this had: both fields of the node contract had
// been declared since phase 2 and populated by nobody.
func WithContentCredentials(credentials ContentCredentials) Option {
	return func(o *Orchestrator) { o.contentCredentials = credentials }
}

// PreparationTriggers is every recorded event after which what Mercator wants
// prepared may be different: a Booking that named a machine, one that was
// dispatched and is no longer speculative, a launch a host is now getting ready
// for, a caller withdrawing work, and a Run whose machine is free again.
//
// Preparation has to be driven by them rather than by a timer alone. A control
// plane that reconciled it on a sweep prepares nothing for a Run that arrived a
// moment after the last one, and its own rate bound never binds either, because
// no sweep cadence an operator would run is faster than the interval they would
// state. Waking here is what makes the bound the thing that paces preparation
// instead of the sweep.
func PreparationTriggers() []string {
	return []string{
		EventBookingDecided,
		EventBookingDispatched,
		EventLaunchAccepted,
		EventCancelRequested,
		EventRunClosed,
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
//
// Nothing asked for and nothing wanted are held apart, which is what makes a
// restart survivable rather than merely slow. A restarted Mercator whose Runs
// were all withdrawn while it was down computes a desire that names nothing, and
// a memory that read its own absence as having already asked for nothing would
// answer that the fleet is in the state it wants and stay quiet, leaving
// whatever was in flight to run to completion for Runs that no longer exist.
// That is why wanting nothing has a key of its own rather than the empty one.
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

// remember records what the far side actually took on, which is the desire minus
// whatever it turned away, and answers whether stating it began preparing
// anything. A desire naming only content this tenant was already asked for began
// no transfer, and neither did one that only drops content, so neither is a
// moment the rate bound measures from.
//
// The two questions are asked of different sets, which is the whole subtlety
// here. What Mercator remembers asking for is what the holder kept, because
// content it refused is not on its way anywhere and nothing stopped it being
// asked for again: remembering a refusal as asked for is what made it permanent,
// since the desire is recomputed identically on the next pass and an unchanged
// desire is not restated. What the rate bound measures is the attempt, refused or
// not, because the bound is on how often Mercator may begin asking a fleet to
// move bytes. Pacing it on what was accepted instead lets a machine that refuses
// everything be asked again in the same instant, forever.
//
// A refusal is matched by the identity the desire stated the item under, which
// names the machine as well as the content. Matching on content alone let one
// host's refusal forget the same content another host had taken on, and what a
// host is really fetching is what the withdrawal for it is computed against: the
// memory collapsed to nothing, the next empty desire read as unchanged, and the
// transfer nobody was waiting for any more ran to completion.
func (memory *prewarmMemory) remember(workspaceID string, wanted []adapter.PrepareItem, receipt adapter.PrepareReceipt) bool {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	if memory.sent == nil {
		memory.sent = map[string]map[string]bool{}
		memory.key = map[string]string{}
	}
	previous := memory.sent[workspaceID]
	began := false
	for _, item := range wanted {
		began = began || !previous[prewarmItemKey(item)]
	}
	kept := slices.DeleteFunc(slices.Clone(wanted), func(item adapter.PrepareItem) bool {
		return slices.Contains(receipt.Refused, item.Identity())
	})
	asked := make(map[string]bool, len(kept))
	for _, item := range kept {
		asked[prewarmItemKey(item)] = true
	}
	memory.sent[workspaceID] = asked
	memory.key[workspaceID] = prewarmOperationKey(workspaceID, kept)
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
	receipt, err := o.prewarmer.Prepare(ctx, adapter.PrepareRequest{
		WorkspaceID:  workspaceID,
		OperationKey: key,
		Wanted:       wanted,
	})
	if err != nil {
		return false, fmt.Errorf("orchestrator: prepare capacity for queued work: %w", err)
	}
	if !o.prewarmed.remember(workspaceID, wanted, receipt) {
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
	collected, err := o.adapter.CollectOffers(ctx, adapter.OfferRequest{WorkspaceID: workspaceID})
	if err != nil {
		return nil, fmt.Errorf("orchestrator: read capacity to prepare: %w", err)
	}
	catalog := offersByID(collected.Offers)
	var wanted []prewarmWant
	seen := map[string]bool{}
	for _, placement := range queued {
		// A machine absent from the current listing is one Mercator can say
		// nothing about: not what it holds, not whether it is still there, and
		// not whether a command would reach it. An offer is the only thing
		// capacity is learned from, so this is a different state from a machine
		// on offer that cannot enumerate its content, and it wants nothing. The
		// reusable lane makes that concrete: a node leaves the catalog through
		// the same predicate that makes the registry refuse to hand out its
		// address, so a desire naming one is a command the Broker refuses and a
		// fleet pass that ends before any other tenant is told anything.
		offer, onOffer := catalog[placement.offer.ID]
		if !onOffer || preparing[placement.offer.ID] {
			continue
		}
		items, err := o.prewarmItems(ctx, workspaceID, placement)
		if err != nil {
			return nil, err
		}
		for rank, item := range items {
			key := prewarmItemKey(item)
			if seen[key] || alreadyHeld(offer, item) {
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
// Each item carries what that one machine may present to fetch that one piece of
// content, minted here and expiring: the registry account and the object-store
// key stay in the control plane, and a host an operator rents by the hour holds
// neither.
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
	pull, err := o.mintPull(ctx, workspaceID, item)
	if err != nil {
		return nil, err
	}
	item.RegistryCredential = pull
	items := []adapter.PrepareItem{item}
	versions, err := o.consumedArtifacts(ctx, workspaceID, placement.workload)
	if err != nil {
		return nil, err
	}
	for _, version := range versions {
		if !version.Durable() {
			continue
		}
		artifact := adapter.PrepareItem{
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
		}
		if artifact.SourceCredential, err = o.mintRead(ctx, workspaceID, artifact); err != nil {
			return nil, err
		}
		items = append(items, artifact)
	}
	return items, nil
}

// mintPull and mintRead name the operation the material is good for by the same
// identity the desired set names the item by: this machine and this content.
// That is the identity a node command carries and the one two Runs wanting one
// image on one host share, so a credential minted for a fetch is spent by that
// fetch rather than by whichever Run happened to trigger the sweep.
func (o *Orchestrator) mintPull(ctx context.Context, workspaceID string, item adapter.PrepareItem) (domain.RegistryPull, error) {
	if o.contentCredentials == nil {
		return domain.RegistryPull{}, nil
	}
	return o.contentCredentials.RegistryPull(ctx, item.Operation(), workspaceID, item.Image)
}

func (o *Orchestrator) mintRead(ctx context.Context, workspaceID string, item adapter.PrepareItem) (domain.ArtifactRead, error) {
	if o.contentCredentials == nil {
		return domain.ArtifactRead{}, nil
	}
	return o.contentCredentials.ArtifactRead(ctx, item.Operation(), workspaceID, item.ArtifactID, item.Source)
}

// alreadyHeld drops content this machine has established it is holding. It is
// asked of an offer, so the machine is one Mercator can currently see, and the
// only silence left is a machine on offer whose runtime cannot enumerate what it
// holds. That silence is not absence and is not presence either: such a host is
// asked to prepare, and the answer costs one command on a machine that may already
// be ready. A node reports that silence whenever it keeps no replica store, and it
// is why the branch exists.
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
// identity two Runs wanting the same image on the same host share. It is the
// item's own identity, so what the far side answers about one item and what this
// controller remembers about it are the same string.
func prewarmItemKey(item adapter.PrepareItem) string {
	return string(item.Kind) + ":" + item.Identity()
}

// prewarmOperationKey is the identity of one desired state. Two reconciliations
// reaching the same answer produce the same key, which is what lets the far
// side treat a redelivered desire as the same desire and lets this controller
// stay quiet when nothing has changed.
//
// Wanting nothing has a key of its own, like every other desire. It is the one
// desire a control plane that has never asked for anything might be mistaken for
// holding, and the two are opposite instructions: one leaves a fleet alone and
// the other stops everything speculative on it. So a Mercator that comes up with
// no queued work sends one withdrawal of nothing, which costs a machine nothing
// and is the only thing that stops a transfer whose Runs went away while this
// control plane was down.
func prewarmOperationKey(workspaceID string, wanted []adapter.PrepareItem) string {
	keys := make([]string, 0, len(wanted))
	for _, item := range wanted {
		keys = append(keys, prewarmItemKey(item))
	}
	slices.Sort(keys)
	return "prewarm:" + workspaceID + ":" + strings.Join(keys, ",")
}
