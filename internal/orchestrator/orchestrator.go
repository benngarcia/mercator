package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
	"github.com/benngarcia/mercator/internal/rentalschedule"
	"github.com/benngarcia/mercator/internal/reporting"
	"github.com/benngarcia/mercator/internal/runprojection"
	"github.com/benngarcia/mercator/internal/scheduler"
)

var (
	ErrRunNotFound            = errors.New("orchestrator: run not found")
	ErrInvalidReport          = errors.New("orchestrator: invalid run report")
	ErrTerminalReportConflict = errors.New("orchestrator: terminal report conflict")
	// ErrRunRequestPersistence marks failure to durably record the acceptance
	// event, before Mercator owns the Run lifecycle.
	ErrRunRequestPersistence = errors.New("orchestrator: persist run request")
	// ErrAcceptedRunUnavailable marks failure to read the current record after
	// the acceptance event was durably recorded.
	ErrAcceptedRunUnavailable = errors.New("orchestrator: read accepted run")
)

const (
	EventRunRequested   = "compute.run.requested.v1"
	EventBookingDecided = "compute.run.booking_decided.v1"
	// EventAdmissionDeferred is a Run being told to wait, and why. It is a
	// public fact rather than a log line because the queue is something an
	// operator has to be able to read: what this Run is behind, and what its
	// class was worth at the moment it was asked.
	EventAdmissionDeferred = "compute.run.admission_deferred.v1"
	// EventAdmissionRefused is a Run admission would not queue, because its
	// class states a moment it must have started by that the queue in front of
	// it is already past.
	EventAdmissionRefused        = "compute.run.admission_refused.v1"
	EventBookingDispatched       = "compute.run.booking_dispatched.v1"
	EventAttemptCreated          = "compute.run.attempt_created.v1"
	EventLaunchIntentRecorded    = "compute.run.launch_intent_recorded.v1"
	EventLaunchAccepted          = "compute.run.launch_accepted.v1"
	EventLaunchIndeterminate     = "compute.run.launch_indeterminate.v1"
	EventLaunchFailed            = "compute.run.launch_failed.v1"
	EventCancelRequested         = "compute.run.cancel_requested.v1"
	EventCancelAccepted          = "compute.run.cancel_accepted.v1"
	EventExternalStateObserved   = "compute.run.external_state_observed.v1"
	EventExecutionStarted        = "compute.run.execution_started.v1"
	EventRunOutcomeRecorded      = "compute.run.outcome_recorded.v1"
	EventCleanupRequested        = "compute.run.cleanup_requested.v1"
	EventCleanupFailed           = "compute.run.cleanup_failed.v1"
	EventCleanupConfirmed        = "compute.run.cleanup_confirmed.v1"
	EventRunClosed               = "compute.run.closed.v1"
	EventRunReported             = "compute.run.reported.v1"
	runCloseReasonRetryExhausted = "RETRY_EXHAUSTED"
)

// runEventTypes is every Run lifecycle event type the orchestrator records.
// Authored contracts that name an event, such as Blueprint fault triggers,
// resolve it here so a name Mercator never emits is rejected where it is
// written rather than silently never matching at execution time.
var runEventTypes = map[string]bool{
	EventRunRequested:          true,
	EventBookingDecided:        true,
	EventAdmissionDeferred:     true,
	EventAdmissionRefused:      true,
	EventBookingDispatched:     true,
	EventAttemptCreated:        true,
	EventLaunchIntentRecorded:  true,
	EventLaunchAccepted:        true,
	EventLaunchIndeterminate:   true,
	EventLaunchFailed:          true,
	EventCancelRequested:       true,
	EventCancelAccepted:        true,
	EventExternalStateObserved: true,
	EventExecutionStarted:      true,
	EventRunOutcomeRecorded:    true,
	EventCleanupRequested:      true,
	EventCleanupFailed:         true,
	EventCleanupConfirmed:      true,
	EventRunClosed:             true,
	EventRunReported:           true,
	EventCapacityRequested:     true,
	EventCapacityAccepted:      true,
	EventCapacityStageObserved: true,
	EventCapacityReclaimed:     true,
}

// IsRunEventType reports whether candidate names a Run lifecycle event the
// orchestrator actually records.
func IsRunEventType(candidate string) bool {
	return runEventTypes[candidate]
}

type Orchestrator struct {
	log                eventlog.EventLog
	runs               runprojection.Store
	scheduler          scheduler.Scheduler
	adapter            Adapter
	schedules          rentalschedule.Store
	now                func() time.Time
	manifests          ImageManifests
	artifacts          ArtifactCatalog
	reportingPublicURL string
	reportingSigner    *reporting.Signer
	runLocks           keyedMutex
	// admissionLock serialises deployment-wide admission. Every other
	// transition a Run makes is guarded by that Run's own stream version, and the
	// log refuses an append written against a version somebody else has already
	// spent. Admission is the one decision read over the whole deployment and
	// written to a single Run, so nothing in the log can refuse it: two members of
	// one family asked at the same instant each replay a queue the other is not in
	// yet, and each appends a decision the log has no reason to reject.
	//
	// It is a lock in this process because the log is this process's own SQLite
	// file, so a deployment's admissions are all decided here or not at all. A
	// second control plane over one log would need the log to arbitrate, which is a
	// different design rather than a wider mutex.
	admissionLock    sync.Mutex
	prewarmer        Prewarmer
	prewarmPolicy    PrewarmPolicy
	prewarmed        prewarmMemory
	preparationClock PreparationClock
	// contentCredentials is what Mercator hands a machine so it can fetch one
	// piece of content. Nil is a Mercator that mints nothing, and every fetch its
	// nodes make is anonymous.
	contentCredentials ContentCredentials
	// capacity is the machine lease, and inviter the node registry a fresh
	// machine is invited through. They are separate from adapter because a lease
	// is not an execution: see Capacity.
	capacity Capacity
	inviter  Inviter
}

type Adapter interface {
	// CollectOffers is the placement read rather than a plain offer list, because
	// a decision has to record who was asked. A connection that answered with
	// nothing and a connection nobody contacted both publish no offer, so a census
	// derived from the offers states the two identically, and admission reads an
	// empty answer as the strongest thing a fleet can say about an ask.
	CollectOffers(ctx context.Context, req adapter.OfferRequest) (adapter.OfferCollection, error)
	Launch(ctx context.Context, req adapter.LaunchRequest) (adapter.LaunchReceipt, error)
	Observe(ctx context.Context, req adapter.ObserveRequest) (adapter.ExternalObservation, error)
	Release(ctx context.Context, req adapter.ReleaseRequest) (adapter.ReleaseReceipt, error)
	Terminate(ctx context.Context, req adapter.TerminateRequest) (adapter.TerminateReceipt, error)
	ListOwned(ctx context.Context, req adapter.OwnershipQuery) ([]adapter.OwnedExternalObject, error)
}

// Option configures an Orchestrator.
type Option func(*Orchestrator)

// WithReporting enables injection of run-scoped reporting env vars into the
// container at launch. When publicURL is non-empty and signer.Enabled(), three
// vars are appended to the launch environment: MERCATOR_RUN_ID,
// MERCATOR_REPORT_URL, and MERCATOR_RUN_TOKEN.
func WithReporting(publicURL string, signer *reporting.Signer) Option {
	return func(o *Orchestrator) {
		o.reportingPublicURL = publicURL
		o.reportingSigner = signer
	}
}

// ImageManifests resolves what an image contains, so Placement can subtract
// what a candidate already holds from what the Run needs. A resolver that
// cannot answer returns a manifest whose Known is false, which leaves every
// candidate indistinguishable on locality rather than silently free.
type ImageManifests interface {
	ResolveManifest(ctx context.Context, imageDigest string, platform domain.Platform) (domain.ImageManifest, error)
}

// WithImageManifests supplies the manifest source. Without one, Mercator cannot
// tell a warm candidate from a cold one and says so in every decision.
func WithImageManifests(manifests ImageManifests) Option {
	return func(o *Orchestrator) {
		o.manifests = manifests
	}
}

// WithClock replaces the wall clock used to stamp event occurrence times and
// placement evaluation times. Scenario harnesses inject a scripted clock so
// decisions and deadlines are deterministic; production uses time.Now.
func WithClock(now func() time.Time) Option {
	return func(o *Orchestrator) {
		o.now = now
	}
}

// WithRentalSchedules supplies the Broker-owned Rental Schedule store used by
// placement and dispatch. Production injects the durable Broker boundary;
// focused tests and local compositions use the in-memory implementation.
func WithRentalSchedules(schedules rentalschedule.Store) Option {
	return func(o *Orchestrator) {
		o.schedules = schedules
	}
}

// WithRunProjection installs the durable Run read model. Production uses the
// SQLite projection; focused compositions default to an event-derived adapter.
func WithRunProjection(runs runprojection.Store) Option {
	return func(o *Orchestrator) {
		o.runs = runs
	}
}

func New(log eventlog.EventLog, scheduler scheduler.Scheduler, adapter Adapter, opts ...Option) *Orchestrator {
	o := &Orchestrator{
		log:       log,
		runs:      historyRunProjection{log: log},
		scheduler: scheduler,
		adapter:   adapter,
		schedules: rentalschedule.NewMemory(log),
		now:       time.Now,
	}
	for _, opt := range opts {
		opt(o)
	}
	return o
}

type CreateRunRequest struct {
	RunID          string
	CommandKey     string
	IdempotencyKey string
	Actor          json.RawMessage
	Workload       domain.WorkloadRevision
	// GeneratedRunID is true when the server minted RunID (no client-supplied
	// run_id). A generated run_id is cosmetic for idempotency: it is excluded
	// from the request hash so a replay keyed by the same Idempotency-Key still
	// matches and returns the original run rather than a freshly generated one.
	GeneratedRunID bool
	// ResolveImage, when set, pins each container's tag-form image to a
	// digest-pinned reference AFTER the idempotency request hash is computed over
	// the submitted (tag-form) spec. This keeps logical retries and moving tags
	// (e.g. :latest) replay-stable while storing/launching a pinned revision.
	ResolveImage ResolveImageFunc
}

// ResolveImageFunc pins one container image and reports the platform that image
// was built for. An empty platform argument means the workload did not state
// one, so the image decides.
type ResolveImageFunc func(ctx context.Context, image, platform string) (pinnedImage, resolvedPlatform string, err error)

// resolveWorkloadImages pins every container image in a revision and records
// the platform each image reports. A workload that stated its platform keeps
// it; one that did not gets the truth from the image instead of a guess.
func resolveWorkloadImages(ctx context.Context, rev domain.WorkloadRevision, resolve ResolveImageFunc) (domain.WorkloadRevision, error) {
	if resolve == nil {
		return rev, nil
	}
	for i := range rev.Spec.Containers {
		container := rev.Spec.Containers[i]
		image, platform, err := resolve(ctx, container.Image, container.Platform.String())
		if err != nil {
			return domain.WorkloadRevision{}, fmt.Errorf("IMAGE_RESOLUTION_FAILED: %s", err.Error())
		}
		rev.Spec.Containers[i].Image = image
		if parsed, ok := domain.ParsePlatform(platform); ok {
			rev.Spec.Containers[i].Platform = parsed
		}
	}
	return rev, nil
}

// refuseUnpinnedImages rejects a Run whose image names a label instead of
// content. A stored workload revision may carry a tag, because it is a template
// and resolution is deferred to here, but a Run is a commitment to bytes: every
// answer Mercator gives about an image afterwards is a digest comparison, so a
// Run admitted with a tag is one whose content Mercator cannot name, cannot ask
// a machine whether it holds, and cannot recognise as the same content another
// Run wants. It is refused at intake rather than guarded downstream, because the
// answer never changes and each downstream guard would have to invent one.
func refuseUnpinnedImages(workload domain.WorkloadRevision) error {
	for _, container := range workload.Spec.Containers {
		if domain.PinnedImage(container.Image) {
			continue
		}
		return fmt.Errorf(
			"IMAGE_NOT_PINNED: Run image %q names no content; reference it as repository@sha256:<64 hex> or configure an image resolver to pin it",
			container.Image,
		)
	}
	return nil
}

type CreateRunResult struct {
	RunID     string
	Duplicate bool
}

func (o *Orchestrator) CreateRun(ctx context.Context, req CreateRunRequest) (CreateRunResult, error) {
	if req.RunID == "" {
		return CreateRunResult{}, fmt.Errorf("orchestrator: run_id is required")
	}
	if req.CommandKey == "" {
		req.CommandKey = req.IdempotencyKey
	}
	if req.CommandKey == "" {
		return CreateRunResult{}, fmt.Errorf("orchestrator: idempotency key is required")
	}
	// Fill omitted, defaultable fields so a minimal create body (just an image)
	// expands toward a fully-specified revision. Architecture is not one of
	// them: image resolution below fills it from the image itself.
	req.Workload = domain.NormalizeWorkloadRevision(req.Workload)
	// The request hash must be stable across logical retries that regenerate
	// cosmetic, client-minted identifiers. The workload revision ID is one such
	// id: a retry that re-mints it is the same logical create and must replay,
	// not 409. Exclude it (and any other cosmetic churn) from the hash. A
	// server-generated run_id is likewise cosmetic for idempotency: excluding it
	// lets a replay keyed by the same Idempotency-Key return the original run.
	// The hash is computed over the SUBMITTED (tag-form) spec, BEFORE digest
	// resolution, so a moving tag like :latest stays replay-stable.
	hashableWorkload := req.Workload
	hashableWorkload.ID = ""
	hashRunID := req.RunID
	if req.GeneratedRunID {
		hashRunID = ""
	}
	requestHash, err := domain.CanonicalHash(struct {
		RunID    string                  `json:"run_id"`
		Workload domain.WorkloadRevision `json:"workload"`
	}{hashRunID, hashableWorkload})
	if err != nil {
		return CreateRunResult{}, err
	}
	// Resolve tag-form images to digest-pinned references and let each image
	// declare the platform it was built for. This happens AFTER the hash above
	// so replay stays stable regardless of where a moving tag currently points.
	req.Workload, err = resolveWorkloadImages(ctx, req.Workload, req.ResolveImage)
	if err != nil {
		return CreateRunResult{}, err
	}
	// Validate what we are about to store and launch, not what was submitted.
	if violations := domain.ValidateWorkloadRevision(req.Workload); len(violations) > 0 {
		return CreateRunResult{}, fmt.Errorf("%s: %s", violations[0].Code, violations[0].Message)
	}
	if err := refuseUnpinnedImages(req.Workload); err != nil {
		return CreateRunResult{}, err
	}
	if err := o.refuseUnknowableInputs(req.Workload); err != nil {
		return CreateRunResult{}, err
	}
	privateData, err := json.Marshal(runRequestedData{RunID: req.RunID, Workload: req.Workload})
	if err != nil {
		return CreateRunResult{}, err
	}
	data, err := json.Marshal(publicRunRequestedData{RunID: req.RunID, Workload: req.Workload.Public()})
	if err != nil {
		return CreateRunResult{}, err
	}
	result, err := o.appendRun(ctx, eventlog.AppendRequest{
		Stream:                runStream(req.RunID),
		ExpectedStreamVersion: 0,
		CommandKey:            req.CommandKey,
		RequestHash:           requestHash,
		Actor:                 req.Actor,
		CorrelationID:         req.RunID,
		CausationID:           req.CommandKey,
		Events: []eventlog.NewEvent{{
			ID:            eventID(req.RunID, "requested"),
			Type:          EventRunRequested,
			SchemaVersion: 1,
			OccurredAt:    o.now().UTC(),
			Visibility:    eventlog.VisibilityPublic,
			Data:          data,
			PrivateData:   privateData,
		}},
	})
	if err != nil {
		if !errors.Is(err, eventlog.ErrIdempotencyConflict) && !errors.Is(err, eventlog.ErrConcurrencyConflict) {
			return CreateRunResult{}, fmt.Errorf("%w: %w", ErrRunRequestPersistence, err)
		}
		return CreateRunResult{}, err
	}
	runID := req.RunID
	if result.Duplicate {
		// A replay of the same command key returns the ORIGINAL stored
		// events. The run identifier is the stream id of the original
		// run_requested event, NOT the (possibly freshly generated) req.RunID.
		// This preserves the idempotency invariant: same Idempotency-Key replay
		// returns the original run_id.
		for _, event := range result.Events {
			if event.Type == EventRunRequested {
				runID = event.StreamID
				break
			}
		}
	}
	return CreateRunResult{RunID: runID, Duplicate: result.Duplicate}, nil
}

// AdvanceRun drives a run toward closure by repeatedly reducing its event
// stream and performing the single next transition: terminal convergence,
// cleanup, placement, launch, or observation. Commands append facts; create,
// cancel, refresh, wait, and the background sweep drive those facts through
// this loop. Each iteration re-reads the stream, so state is always derived
// from the log rather than threaded through in memory.
func (o *Orchestrator) AdvanceRun(ctx context.Context, runID string) error {
	unlock := o.runLocks.Lock(runID)
	defer unlock()

	for {
		events, err := o.GetRunEvents(ctx, runID)
		if err != nil {
			return err
		}
		state, err := reduceRun(events)
		if err != nil {
			return err
		}
		progressed, err := o.step(ctx, runID, streamVersion(events), state)
		if err != nil || !progressed {
			return err
		}
	}
}

type keyedMutex struct {
	mu      sync.Mutex
	entries map[string]*keyedMutexEntry
}

type keyedMutexEntry struct {
	mu   sync.Mutex
	refs int
}

func (m *keyedMutex) Lock(key string) func() {
	m.mu.Lock()
	if m.entries == nil {
		m.entries = map[string]*keyedMutexEntry{}
	}
	entry := m.entries[key]
	if entry == nil {
		entry = &keyedMutexEntry{}
		m.entries[key] = entry
	}
	entry.refs++
	m.mu.Unlock()

	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()

		m.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(m.entries, key)
		}
		m.mu.Unlock()
	}
}

// step performs the run's next transition and reports whether the run may have
// further work (true → reduce and step again). Every transition is one side
// effect plus one event append at the given optimistic-concurrency version.
func (o *Orchestrator) step(ctx context.Context, runID string, version uint64, state runState) (bool, error) {
	switch {
	case state.closed:
		return false, nil
	case state.cleanupRequested && !state.cleanupConfirmed:
		return true, o.releaseAndCloseScheduled(ctx, runID, version, state)
	case state.firstTerminal != nil && !state.outcomeRecorded:
		return true, o.recordTerminalTransition(ctx, runID, version, state)
	case state.bookingQueued():
		return o.dispatchQueuedBooking(ctx, runID, version, state)
	case state.launchIntent == nil, state.replacementEligible():
		// A Run whose declared Artifacts are not all durable is not admitted at
		// all, and stays exactly where it is until one of them is published. It
		// is not queued either: waiting for content nobody has written yet is
		// not waiting for capacity, and the queue is about capacity.
		durable, err := o.inputsAreDurable(ctx, state.requested.Workload)
		if err != nil || !durable {
			return false, err
		}
		return o.stepAdmit(ctx, runID, version, state)
	case state.capacity != nil && !state.nodeEnrolled:
		// A placement that chose to provision has to build the machine before
		// anything can be launched on it. Until an agent enrols there is no
		// session to create a container through, whatever the provider says about
		// the allocation.
		return o.stepBuildCapacity(ctx, runID, version, state)
	case !state.launchAccepted && state.launchFailure == nil:
		return o.stepLaunch(ctx, runID, version, state)
	default:
		observation, err := o.observeLaunch(ctx, state)
		if err != nil {
			return false, err
		}
		return o.recordObservation(ctx, runID, version, state, observation)
	}
}

func (o *Orchestrator) dispatchQueuedBooking(ctx context.Context, runID string, version uint64, state runState) (bool, error) {
	schedules, err := o.schedules.List(ctx)
	if err != nil {
		return false, fmt.Errorf("orchestrator: list Rental Schedules: %w", err)
	}
	booking, found := scheduledBooking(schedules[state.bookingDecision.Booking.RentalID], state.bookingDecision.Booking.ID)
	if !found {
		return false, fmt.Errorf("orchestrator: queued Booking %q is missing from its Rental Schedule", state.bookingDecision.Booking.ID)
	}
	if booking.State == domain.BookingStateQueued {
		return false, nil
	}
	if booking.State != domain.BookingStateRunning {
		return false, fmt.Errorf("orchestrator: queued Booking %q has invalid dispatched state %q", booking.ID, booking.State)
	}
	selectedOffer, err := offerFromDecision(*state.bookingDecision)
	if err != nil {
		return false, err
	}
	attempt := newAttempt(runID, state.attemptCount+1)
	reportPublicURL, reportToken := "", ""
	if o.reportingPublicURL != "" && o.reportingSigner != nil && o.reportingSigner.Enabled() {
		reportPublicURL = o.reportingPublicURL
		reportToken = o.reportingSigner.Token(runID)
	}
	launchReq, err := buildLaunchRequest(runID, *state.requested, attempt, selectedOffer, reportPublicURL, reportToken)
	if err != nil {
		return false, err
	}
	err = o.appendEvents(ctx, runID, version, "advance:dispatch:"+booking.ID, []eventlog.NewEvent{
		mustEvent(runID, "booking_dispatched_"+booking.ID, EventBookingDispatched, bookingDispatchedData{Booking: booking}, o.now()),
		mustEvent(runID, "attempt_created_"+attempt.AttemptID, EventAttemptCreated, attempt, o.now()),
		mustPrivateEvent(runID, "launch_intent_recorded_"+attempt.AttemptID, EventLaunchIntentRecorded, publicLaunchRequest(launchReq), launchReq, o.now()),
	})
	return err == nil, err
}

func scheduledBooking(schedule domain.RentalSchedule, bookingID string) (domain.Booking, bool) {
	for _, scheduled := range schedule.Bookings {
		if scheduled.Booking.ID == bookingID {
			return scheduled.Booking, true
		}
	}
	return domain.Booking{}, false
}

func offerFromDecision(decision domain.BookingDecision) (domain.OfferSnapshot, error) {
	for _, candidate := range decision.Candidates {
		if candidate.OfferSnapshotID != decision.SelectedOfferSnapshotID {
			continue
		}
		if candidate.Disposition != domain.CandidateDispositionQueue && candidate.Disposition != domain.CandidateDispositionRunNow {
			return domain.OfferSnapshot{}, fmt.Errorf("orchestrator: dispatched Booking requires an existing Rental candidate")
		}
		return domain.OfferSnapshot{
			ID:           candidate.OfferSnapshotID,
			ConnectionID: candidate.ConnectionID,
			AdapterType:  candidate.AdapterType,
			NativeRef:    candidate.NativeRef,
			Kind:         domain.OfferKindStanding,
			// A Booking waits on a Rental, and only reusable capacity may become
			// one, which is what the candidate disposition checked just above
			// already established. It is stated rather than left empty because the
			// launch path dispatches on it: an empty lane sends a Run that queued on
			// an enrolled node down the ephemeral seam, to look for a provider
			// connection under the node registry's own connection id.
			Lane: domain.LaneReusable,
		}, nil
	}
	return domain.OfferSnapshot{}, fmt.Errorf("orchestrator: selected candidate %q is missing", decision.SelectedOfferSnapshotID)
}

// stepPlace decides placement and records the decision, attempt, and launch
// intent in one append, so the intent is durable before any adapter call. It
// reports whether the Run moved, because a placement that selected nothing has
// not moved it: admission queues that Run and the next tick asks again.
func (o *Orchestrator) stepPlace(ctx context.Context, runID string, version uint64, state runState, run queuePosition) (bool, error) {
	attemptNumber := state.attemptCount + 1
	supersedes, supersedesReason := state.supersession()
	decision, attempt, selectedOffer, schedule, err := o.decide(ctx, *state.requested, runID, attemptNumber, placementRequest{
		excluded:         state.excluded,
		supersedes:       supersedes,
		supersedesReason: supersedesReason,
	})
	if err != nil {
		return false, err
	}
	if decision.SelectedOfferSnapshotID == "" {
		if state.replacementEligible() {
			return true, o.closeRetryExhausted(ctx, runID, version, decision)
		}
		deferral, projected := placementDeferral(run, decision)
		return false, o.deferOrRefuse(ctx, runID, version, state, run, admissionAnswer{
			deferral:  deferral,
			projected: projected,
			decision:  &decision,
		})
	}
	nextSchedule, err := reserveDecision(*state.requested, decision, schedule)
	if err != nil {
		return false, err
	}
	events := []eventlog.NewEvent{decisionEvent(runID, decision, o.now())}
	commandKey := "advance:placement:" + decision.Booking.ID
	if decision.Booking.State == domain.BookingStateQueued {
		request, requestErr := runAppendRequest(nil, runID, version, commandKey, events)
		if requestErr != nil {
			return false, requestErr
		}
		_, err = o.commitSchedule(ctx, request, decision.Booking.ScheduleVersion-1, nextSchedule)
		return true, err
	}
	reportPublicURL, reportToken := "", ""
	if o.reportingPublicURL != "" && o.reportingSigner != nil && o.reportingSigner.Enabled() {
		reportPublicURL = o.reportingPublicURL
		reportToken = o.reportingSigner.Token(runID)
	}
	launchReq, err := buildLaunchRequest(runID, *state.requested, attempt, selectedOffer, reportPublicURL, reportToken)
	if err != nil {
		return false, err
	}
	events = append(events,
		mustEvent(runID, "attempt_created_"+attempt.AttemptID, EventAttemptCreated, attempt, o.now()),
		mustPrivateEvent(runID, "launch_intent_recorded_"+attempt.AttemptID, EventLaunchIntentRecorded, publicLaunchRequest(launchReq), launchReq, o.now()),
	)
	// What this answer commits Mercator to allocating, written down with the
	// answer itself and before any provider is asked for anything. A machine
	// allocated by a command whose response never came back is reconcilable
	// because this is already durable.
	if plan := capacityPlan(decision, selectedOffer, o.now().UTC()); plan != nil {
		events = append(events, mustEvent(runID, "capacity_requested_"+plan.RentalID, EventCapacityRequested, *plan, o.now()))
	}
	request, err := runAppendRequest(nil, runID, version, commandKey, events)
	if err != nil {
		return false, err
	}
	_, err = o.commitSchedule(ctx, request, decision.Booking.ScheduleVersion-1, nextSchedule)
	return true, err
}

// decisionEvent is one Booking Decision written down. It is identified by the
// decision rather than by the Booking it created, because a decision that placed
// the Run nowhere created no Booking and is still a decision the Run has to be
// explainable from.
func decisionEvent(runID string, decision domain.BookingDecision, now time.Time) eventlog.NewEvent {
	return mustEvent(runID, "booking_decided_"+decision.ID, EventBookingDecided, bookingDecisionData{Decision: decision}, now)
}

func reserveDecision(requested runRequestedData, decision domain.BookingDecision, schedule domain.RentalSchedule) (domain.RentalSchedule, error) {
	if decision.Booking == nil {
		return domain.RentalSchedule{}, fmt.Errorf("orchestrator: selected placement requires Booking")
	}
	expectedRuntime := requested.Workload.Spec.Placement.ExpectedRuntimeSeconds
	maxRuntime := float64(requested.Workload.Spec.Execution.MaxRuntimeSeconds)
	if expectedRuntime <= 0 {
		expectedRuntime = maxRuntime
	}
	if schedule.RentalID != decision.Booking.RentalID || schedule.Version+1 != decision.Booking.ScheduleVersion {
		return domain.RentalSchedule{}, fmt.Errorf("orchestrator: scheduler Booking references a stale Rental Schedule")
	}
	next, booking, err := schedule.Reserve(domain.BookingRequest{
		BookingID:              decision.Booking.ID,
		RunID:                  decision.RunID,
		ExpectedRuntimeSeconds: expectedRuntime,
		MaxRuntimeSeconds:      maxRuntime,
		ReservedAt:             decision.EvaluatedAt,
	})
	if err != nil {
		return domain.RentalSchedule{}, err
	}
	bookingHash, hashErr := domain.CanonicalHash(booking)
	if hashErr != nil {
		return domain.RentalSchedule{}, hashErr
	}
	decisionHash, hashErr := domain.CanonicalHash(decision.Booking)
	if hashErr != nil {
		return domain.RentalSchedule{}, hashErr
	}
	if bookingHash != decisionHash {
		return domain.RentalSchedule{}, fmt.Errorf("orchestrator: scheduler Booking does not match Rental Schedule transition")
	}
	return next, nil
}

// recordTerminalTransition converts the first terminal fact in stream order
// into the run's single outcome and cleanup intent.
func (o *Orchestrator) recordTerminalTransition(ctx context.Context, runID string, version uint64, state runState) error {
	if !state.externalObjectPossible() {
		events := []eventlog.NewEvent{
			mustEvent(runID, "outcome_recorded", EventRunOutcomeRecorded, runOutcomeRecordedData{Outcome: state.firstTerminal.Outcome}, o.now()),
			mustEvent(runID, "closed", EventRunClosed, runClosedData{Closed: true}, o.now()),
		}
		// A run cancelled while its Booking is still queued must release that
		// Booking from its Rental Schedule in the same commit, or the schedule
		// keeps a phantom entry that later promotes to running and wedges the
		// Rental. Guarded on bookingQueued, not bookingDecision: a booking
		// already released by a replaceable launch failure must not complete
		// twice.
		if state.bookingQueued() {
			return o.completeBookingAndAppend(ctx, runID, version, state, "advance:terminal-before-launch", events)
		}
		return o.appendEvents(ctx, runID, version, "advance:terminal-before-launch", events)
	}
	return o.appendEvents(ctx, runID, version, "advance:terminal", []eventlog.NewEvent{
		mustEvent(runID, "outcome_recorded", EventRunOutcomeRecorded, runOutcomeRecordedData{Outcome: state.firstTerminal.Outcome}, o.now()),
		mustEvent(runID, "cleanup_requested", EventCleanupRequested, launchReferenceData{LaunchKey: state.launchIntent.LaunchKey}, o.now()),
	})
}

func (o *Orchestrator) GetRunEvents(ctx context.Context, runID string) ([]eventlog.StoredEvent, error) {
	history, err := eventlog.ReadFullStream(ctx, o.log, runStream(runID))
	return history.Events, err
}

// streamVersion is the optimistic-concurrency expectation for the next append:
// the stream version of the last stored event, not len(events), so a partial or
// filtered read can never silently expect a stale version.
func streamVersion(events []eventlog.StoredEvent) uint64 {
	if len(events) == 0 {
		return 0
	}
	return events[len(events)-1].StreamVersion
}

func (o *Orchestrator) GetRun(ctx context.Context, runID string) (domain.RunRecord, error) {
	events, err := o.GetRunEvents(ctx, runID)
	if err != nil {
		return domain.RunRecord{}, err
	}
	if len(events) == 0 {
		return domain.RunRecord{}, fmt.Errorf("orchestrator: run not found")
	}
	state, err := reduceRun(events)
	if err != nil {
		return domain.RunRecord{}, err
	}
	return runRecordFromState(runID, state), nil
}

func (o *Orchestrator) ListRuns(ctx context.Context, request runprojection.PageRequest) (runprojection.Page, error) {
	return o.runs.List(ctx, request)
}

// RebuildRunProjection replaces the Run projection from the event log,
// which remains the source of truth.
func (o *Orchestrator) RebuildRunProjection(ctx context.Context) error {
	records, err := o.runRecordsFromHistory(ctx)
	if err != nil {
		return err
	}
	return o.runs.Replace(ctx, records)
}

func (o *Orchestrator) runRecordsFromHistory(ctx context.Context) ([]domain.RunRecord, error) {
	states := make(map[string]*runState)
	filter := eventlog.EventFilter{

		StreamTypes: []string{"run"},
	}
	head, err := o.log.LatestPosition(ctx, filter)
	if err != nil {
		return nil, err
	}
	for event, err := range eventlog.ScanAll(ctx, o.log, head, filter) {
		if err != nil {
			return nil, err
		}
		state := states[event.StreamID]
		if state == nil {
			state = &runState{}
			states[event.StreamID] = state
		}
		if err := applyStoredEvent(state, event); err != nil {
			return nil, err
		}
	}
	records := make([]domain.RunRecord, 0, len(states))
	for runID, state := range states {
		if err := state.validate(); err != nil {
			return nil, err
		}
		records = append(records, runRecordFromState(runID, *state))
	}
	sort.Slice(records, func(i, j int) bool { return records[i].ID < records[j].ID })
	return records, nil
}

// AdvanceOpenRunsResult summarizes one background advancement sweep of a
// deployment: how many open runs were found and how many of them reached the
// closed state during the sweep.
type AdvanceOpenRunsResult struct {
	Open   int
	Closed int
}

// AdvanceOpenRuns drives every open (not yet closed) run
// through AdvanceRun so runs converge to closed with zero client involvement:
// observing container exits, recording terminal outcomes, and confirming
// cleanup is the broker's job, not something every client must poll for via
// /refresh or /wait. An error on one run never stops advancement of the
// others; per-run errors are joined into the returned error alongside the
// sweep result, which stays valid either way.
func (o *Orchestrator) AdvanceOpenRuns(ctx context.Context) (AdvanceOpenRunsResult, error) {
	openRuns, err := o.listOpenRunIDs(ctx)
	if err != nil {
		return AdvanceOpenRunsResult{}, err
	}
	result := AdvanceOpenRunsResult{Open: len(openRuns)}
	var errs []error
	for _, runID := range openRuns {
		if err := o.AdvanceRun(ctx, runID); err != nil {
			errs = append(errs, fmt.Errorf("advance %s: %w", runID, err))
			continue
		}
		record, err := o.GetRun(ctx, runID)
		if err != nil {
			errs = append(errs, fmt.Errorf("advance %s: %w", runID, err))
			continue
		}
		if record.Closed {
			result.Closed++
		}
	}
	return result, errors.Join(errs...)
}

// listOpenRunIDs enumerates run streams that recorded RunRequested but no
// RunClosed, using the same paginated event-index scan as ListRuns but without
// hydrating per-run streams. That keeps the background sweep cheap when idle:
// a deployment whose history is all closed runs costs one filtered index scan
// and zero stream reads per tick.
func (o *Orchestrator) listOpenRunIDs(ctx context.Context) ([]string, error) {
	return o.runs.ListOpenIDs(ctx)
}

// GetBookingDecisions is every decision Mercator recorded about this Run, in the
// order it recorded them, and it is the whole chain rather than its last element.
// A decision is added and never edited, so the newest one is an answer that
// stands in for the ones before it and names them: collapsing the chain to its
// last entry was showing a reader a Run that had only ever been answered once,
// with the refusal that came first and the machine that turned it away nowhere on
// the page.
func (o *Orchestrator) GetBookingDecisions(ctx context.Context, runID string) ([]domain.BookingDecision, error) {
	events, err := o.GetRunEvents(ctx, runID)
	if err != nil {
		return nil, err
	}
	var chain []domain.BookingDecision
	for _, event := range events {
		if event.Type != EventBookingDecided {
			continue
		}
		var data bookingDecisionData
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return nil, err
		}
		chain = append(chain, data.Decision)
	}
	if len(chain) == 0 {
		return nil, fmt.Errorf("orchestrator: booking decision not found")
	}
	return chain, nil
}

func (o *Orchestrator) RefreshRun(ctx context.Context, runID string) (domain.RunRecord, error) {
	if err := o.AdvanceRun(ctx, runID); err != nil {
		return domain.RunRecord{}, err
	}
	return o.GetRun(ctx, runID)
}

// CancelRun records the cancel request as a fact attributed to the acting
// principal, then advances it through the same terminal cleanup transition as
// workload exit and provider exit. Cancelling a closed run returns it unchanged.
func (o *Orchestrator) CancelRun(ctx context.Context, runID string, actor json.RawMessage) (domain.RunRecord, error) {
	events, err := o.GetRunEvents(ctx, runID)
	if err != nil {
		return domain.RunRecord{}, err
	}
	if len(events) == 0 {
		return domain.RunRecord{}, fmt.Errorf("orchestrator: run not found")
	}
	state, err := reduceRun(events)
	if err != nil {
		return domain.RunRecord{}, err
	}
	if state.closed {
		return runRecordFromState(runID, state), nil
	}
	if !state.cancelRequested {
		data := cancelRequestedData{Reason: "user"}
		if state.launchIntent != nil {
			data = cancelRequestedData{LaunchKey: state.launchIntent.LaunchKey}
		}
		if err := o.appendEventsAs(ctx, actor, runID, streamVersion(events), "cancel:requested", []eventlog.NewEvent{
			mustEvent(runID, "cancel_requested", EventCancelRequested, data, o.now()),
		}); err != nil {
			return domain.RunRecord{}, err
		}
	}
	if err := o.AdvanceRun(ctx, runID); err != nil {
		return domain.RunRecord{}, err
	}
	return o.GetRun(ctx, runID)
}

// RecordReport appends a compute.run.reported.v1 fact and returns before
// cleanup. Terminal reports use one semantic command per run, so an exact
// replay is idempotent and conflicting terminal data fails explicitly.
func (o *Orchestrator) RecordReport(ctx context.Context, runID string, report RunReport) error {
	if report == nil {
		return fmt.Errorf("%w: report is required", ErrInvalidReport)
	}
	payload := report.payload()
	unlock := o.runLocks.Lock(runID)
	defer unlock()

	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("orchestrator: marshal report data: %w", err)
	}

	for attempt := 0; attempt < 2; attempt++ {
		events, err := o.GetRunEvents(ctx, runID)
		if err != nil {
			return fmt.Errorf("orchestrator: read run stream: %w", err)
		}
		if len(events) == 0 {
			return ErrRunNotFound
		}
		version := streamVersion(events)
		suffix := fmt.Sprintf("reported_%d", version+1)
		commandKey := runID + ":report:" + suffix
		requestHash := ""
		if payload.terminal() {
			suffix = "reported_terminal"
			commandKey = runID + ":report:terminal"
			requestHash, err = domain.CanonicalHash(payload)
			if err != nil {
				return err
			}
		}
		evt := eventlog.NewEvent{
			ID:            eventID(runID, suffix),
			Type:          EventRunReported,
			SchemaVersion: 1,
			OccurredAt:    o.now().UTC(),
			Visibility:    eventlog.VisibilityPublic,
			Data:          encoded,
		}
		if requestHash == "" {
			requestHash, err = domain.CanonicalHash([]eventlog.NewEvent{evt})
			if err != nil {
				return err
			}
		}
		_, appendErr := o.appendRun(ctx, eventlog.AppendRequest{
			Stream:                runStream(runID),
			ExpectedStreamVersion: version,
			CommandKey:            commandKey,
			RequestHash:           requestHash,
			CorrelationID:         runID,
			CausationID:           "report",
			Events:                []eventlog.NewEvent{evt},
		})
		if appendErr == nil {
			return nil
		}
		if payload.terminal() && errors.Is(appendErr, eventlog.ErrIdempotencyConflict) {
			return fmt.Errorf("%w: %v", ErrTerminalReportConflict, appendErr)
		}
		if errors.Is(appendErr, eventlog.ErrConcurrencyConflict) && attempt == 0 {
			// Retry once on optimistic-concurrency conflict.
			continue
		}
		return fmt.Errorf("orchestrator: append report event: %w", appendErr)
	}
	// All retries exhausted; last error was a concurrency conflict.
	return fmt.Errorf("orchestrator: append report event: concurrency conflict after retry")
}

// newAttempt mints the run's attempt identity. There is ONE identity — the
// attempt id — and three fixed derivations of it that adapters use for
// different jobs: LaunchKey names the external object, OwnershipToken labels
// it as ours, CleanupLocator addresses its cleanup. The derivations are part
// of the adapter wire contract (container labels, pod env), so they are
// recorded on the launch intent and never re-derived after launch.
func newAttempt(runID string, attemptNumber int) attemptData {
	ordinal := fmt.Sprintf("%d", attemptNumber)
	id := "att_" + externalIDPart(strings.TrimPrefix(runID, "run_")) + "_" + ordinal + "_" + shortExternalHash(runID, ordinal)
	return attemptData{
		AttemptID:      id,
		LaunchKey:      "launch_" + id,
		OwnershipToken: "own_" + id,
		CleanupLocator: "cleanup_" + id,
	}
}

func buildLaunchRequest(runID string, requested runRequestedData, attempt attemptData, selectedOffer domain.OfferSnapshot, reportPublicURL, reportToken string) (adapter.LaunchRequest, error) {
	container := requested.Workload.Spec.Containers[0]
	disposition, err := selectedOffer.CleanupDisposition()
	if err != nil {
		return adapter.LaunchRequest{}, err
	}
	env := launchEnvironment(container.Env)
	if reportPublicURL != "" && reportToken != "" {
		env = append(env,
			adapter.EnvironmentBinding{Name: "MERCATOR_RUN_ID", Value: stringPtr(runID)},
			adapter.EnvironmentBinding{Name: "MERCATOR_REPORT_URL", Value: stringPtr(reportPublicURL)},
			adapter.EnvironmentBinding{Name: "MERCATOR_RUN_TOKEN", Value: stringPtr(reportToken)},
		)
	}
	launchReq := adapter.LaunchRequest{
		OperationKey: attempt.LaunchKey,

		RunID:                     runID,
		AttemptID:                 attempt.AttemptID,
		WorkloadID:                requested.Workload.WorkloadID,
		WorkloadRevisionID:        requested.Workload.ID,
		OwnershipToken:            attempt.OwnershipToken,
		LaunchKey:                 attempt.LaunchKey,
		CleanupLocator:            attempt.CleanupLocator,
		Image:                     container.Image,
		Platform:                  container.Platform,
		Entrypoint:                container.Entrypoint,
		Args:                      slices.Clone(container.Args),
		Environment:               env,
		Ports:                     slices.Clone(container.Ports),
		Resources:                 requested.Workload.Spec.Resources,
		CacheMounts:               slices.Clone(requested.Workload.Spec.Caches),
		MaxRuntimeSeconds:         requested.Workload.Spec.Execution.MaxRuntimeSeconds,
		SelectedOfferSnapshotID:   selectedOffer.ID,
		SelectedOfferConnectionID: selectedOffer.ConnectionID,
		SelectedOfferAdapterType:  selectedOffer.AdapterType,
		SelectedOfferNativeRef:    selectedOffer.NativeRef,
		SelectedOfferLane:         selectedOffer.Lane,
		// Derive the cleanup disposition from what the selected offer says it is,
		// and RECORD it on the launch intent now. This recorded value is the source
		// of truth for cleanup, never the offer looked up again later.
		Disposition: disposition,
	}
	hash, err := domain.CanonicalHash(launchReq)
	if err != nil {
		return adapter.LaunchRequest{}, err
	}
	launchReq.RequestHash = hash
	return launchReq, nil
}

// recordObservation appends the provider fact. A terminal fact makes the next
// advance iteration record the outcome and cleanup intent. A repeated
// non-terminal phase carries no new information, so it is not appended on
// every poll.
func (o *Orchestrator) recordObservation(ctx context.Context, runID string, version uint64, state runState, observation adapter.ExternalObservation) (bool, error) {
	started := startMoment(state, observation)
	if !isTerminal(observation.Phase) && observation.Phase == state.lastObservedPhase && started == nil {
		return false, nil
	}
	events := []eventlog.NewEvent{
		mustEvent(runID, fmt.Sprintf("external_state_observed_%d", version+1), EventExternalStateObserved, observation, o.now()),
	}
	if started != nil {
		events = append(events, mustEvent(runID, fmt.Sprintf("execution_started_%d", version+1), EventExecutionStarted, executionStartedData{
			LaunchKey: observation.LaunchKey,
			StartedAt: *started,
		}, o.now()))
	}
	request, err := runAppendRequest(nil, runID, version, fmt.Sprintf("advance:observe:%d", version), events)
	if err != nil {
		return false, err
	}
	if err := o.commitObservation(ctx, request, state, observation); err != nil {
		return false, err
	}
	return isTerminal(observation.Phase), nil
}

// startMoment is the moment this Run's workload began, taken from an observation
// that carries one and only the first time. It is nil for every other
// observation, which is what makes the run stream hold one start moment per
// attempt rather than restating the same fact on every poll.
//
// Mercator never fills this in for itself. A holder that publishes no start
// moment leaves the stage without an actual, and the record says so: acquisition
// and boot have no production observation at all until an agent bootstraps on
// provisioned capacity, and deriving them from when the launch was accepted would
// put Mercator's own arithmetic into the log as though somebody had measured it.
//
// It is also not every moment a holder publishes. A start Mercator can defend is
// one an observation established, which adapter.ExternalObservation decides for
// every lane at once, so a foreign clock running ahead and a provider calling a
// pod started before it has run anything are refused here rather than three times
// in three adapters.
func startMoment(state runState, observation adapter.ExternalObservation) *time.Time {
	moment, established := observation.EstablishedStart()
	if state.startedAt != nil || !established {
		return nil
	}
	return &moment
}

// commitObservation writes the fact, and with it the moment this Run's workload
// began running when the fact is what establishes it. The two are one append
// because they are one observation: a schedule that learned the start separately
// could disagree with the run's own history about when the machine said it.
func (o *Orchestrator) commitObservation(
	ctx context.Context,

	request eventlog.AppendRequest,
	state runState,
	observation adapter.ExternalObservation,
) error {
	started, establishes, err := o.workloadStarted(ctx, state, observation)
	if err != nil {
		return err
	}
	if !establishes {
		_, err = o.appendRun(ctx, request)
		return err
	}
	_, err = o.commitSchedule(ctx, request, started.Version-1, started)
	return err
}

// workloadStarted is this Run's Rental Schedule with the moment its workload
// began running recorded on it. Both runtimes a Booking declares are bounds on a
// container, so that moment is the only clock they can be measured from: charging
// them from the placement decision spent provisioning and image pull against a
// runtime nothing was enforcing yet, which read a machine still fetching as past
// its own bound.
//
// Three observations establish nothing. One that does not say the container is
// running has no moment in it. A Rental holding nothing has already finished this
// Booking, so there is nothing left to start. A Rental holding a Booking for
// another Run is a machine two Runs are on, which is a reconciliation problem
// rather than a moment to record, and starting this Booking's clock from another
// Run's container would bury it.
func (o *Orchestrator) workloadStarted(
	ctx context.Context,

	state runState,
	observation adapter.ExternalObservation,
) (domain.RentalSchedule, bool, error) {
	if observation.Phase != adapter.ExternalPhaseRunning || state.bookingDecision == nil || state.bookingDecision.Booking == nil {
		return domain.RentalSchedule{}, false, nil
	}
	schedules, err := o.schedules.List(ctx)
	if err != nil {
		return domain.RentalSchedule{}, false, fmt.Errorf("orchestrator: list Rental Schedules: %w", err)
	}
	booking := state.bookingDecision.Booking
	schedule := schedules[booking.RentalID]
	holding, held := schedule.Holding()
	if !held || holding.Booking.ID != booking.ID || !holding.StartedAt.IsZero() {
		return domain.RentalSchedule{}, false, nil
	}
	next, err := schedule.Started(booking.ID, bookingStartedAt(observation))
	if err != nil {
		return domain.RentalSchedule{}, false, err
	}
	return next, true, nil
}

// bookingStartedAt is the moment this Booking's runtime bounds are measured from.
// It is the container's own start where an observation established one, because
// that is the process the bounds are about. Where none did it is the moment
// Mercator read it running, which is the latest instant the container is known to
// have been up: a schedule needs a clock to project a remaining runtime from, and
// the honest fallback is the last thing Mercator can prove rather than the
// earliest thing it could guess. Nothing derives the RUN's start moment this way,
// because a projection may be conservative and a record may not be invented.
//
// It asks the same law the run stream does, and asking anything else was the
// defect: a bound is enforced against Mercator's clock, so a moment from a host an
// hour ahead put this Booking's expiry an hour into Mercator's future while the
// container burned paid capacity and the schedule reported the machine busy. The
// fallback is honest only because ObservedAt is Mercator's own clock on every seam
// that fills it in.
func bookingStartedAt(observation adapter.ExternalObservation) time.Time {
	if moment, established := observation.EstablishedStart(); established {
		return moment
	}
	return observation.ObservedAt
}

func (o *Orchestrator) observeLaunch(ctx context.Context, state runState) (adapter.ExternalObservation, error) {
	observation, err := o.adapter.Observe(ctx, adapter.ObserveRequest{

		ConnectionID:   state.launchIntent.SelectedOfferConnectionID,
		LaunchKey:      state.launchIntent.LaunchKey,
		OwnershipToken: state.launchIntent.OwnershipToken,
		RequestHash:    state.launchIntent.RequestHash,
		Lane:           state.launchIntent.SelectedOfferLane,
		NativeRef:      state.launchIntent.SelectedOfferNativeRef,
		RunID:          state.launchIntent.RunID,
		AttemptID:      state.launchIntent.AttemptID,
	})
	if err != nil {
		return adapter.ExternalObservation{}, err
	}
	if observation.Phase != adapter.ExternalPhaseReleased || !state.launchIndeterminate() {
		return observation, nil
	}
	owned, err := o.adapter.ListOwned(ctx, adapter.OwnershipQuery{})
	if err != nil {
		return adapter.ExternalObservation{}, err
	}
	for _, object := range owned {
		if object.RunID == state.launchIntent.RunID &&
			object.AttemptID == state.launchIntent.AttemptID &&
			object.OwnershipToken == state.launchIntent.OwnershipToken &&
			object.RequestHash == state.launchIntent.RequestHash {
			return adapter.ExternalObservation{
				ExternalID: object.ExternalID,
				LaunchKey:  object.LaunchKey,
				Phase:      object.Phase,
				ObservedAt: o.now().UTC(),
				NativeJSON: `{"source":"list_owned"}`,
			}, nil
		}
	}
	return observation, nil
}

func (o *Orchestrator) releaseAndClose(ctx context.Context, runID string, version uint64, launchReq *adapter.LaunchRequest) error {
	return o.releaseAndCloseScheduled(ctx, runID, version, runState{launchIntent: launchReq})
}

func (o *Orchestrator) releaseAndCloseScheduled(ctx context.Context, runID string, version uint64, state runState) error {
	launchReq := state.launchIntent
	if launchReq == nil {
		return fmt.Errorf("orchestrator: cleanup requested without launch intent")
	}
	// Dispatch on the RECORDED disposition from the launch intent. We never
	// consult live offers or re-derive the disposition here: that is what makes
	// cleanup crash-safe and orphan-free even if offers changed or disappeared.
	disposition := launchReq.Disposition
	if !disposition.Valid() {
		return fmt.Errorf("orchestrator: cleanup requires a valid recorded disposition, got %q", disposition)
	}
	if err := o.cleanup(ctx, launchReq); err != nil {
		return o.recordCleanupFailure(ctx, runID, version, launchReq.LaunchKey, disposition, err)
	}
	events := []eventlog.NewEvent{
		mustEvent(runID, "cleanup_confirmed", EventCleanupConfirmed, cleanupConfirmedData{LaunchKey: launchReq.LaunchKey, Disposition: disposition}, o.now()),
		mustEvent(runID, "closed", EventRunClosed, runClosedData{Closed: true}, o.now()),
	}
	return o.completeBookingAndAppend(ctx, runID, version, state, "advance:cleanup", events)
}

func (o *Orchestrator) completeBookingAndAppend(ctx context.Context, runID string, version uint64, state runState, commandKey string, events []eventlog.NewEvent) error {
	if state.bookingDecision == nil || state.bookingDecision.Booking == nil {
		return fmt.Errorf("orchestrator: transition requires a recorded Booking")
	}
	schedules, err := o.schedules.List(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: list Rental Schedules: %w", err)
	}
	schedule := schedules[state.bookingDecision.Booking.RentalID]
	next, _, err := schedule.Complete(state.bookingDecision.Booking.ID, o.now().UTC())
	if err != nil {
		return err
	}
	request, err := runAppendRequest(nil, runID, version, commandKey, events)
	if err != nil {
		return err
	}
	_, err = o.commitSchedule(ctx, request, schedule.Version, next)
	return err
}

func (o *Orchestrator) commitSchedule(
	ctx context.Context,
	request eventlog.AppendRequest,
	expectedVersion uint64,
	next domain.RentalSchedule,
) (eventlog.AppendResult, error) {
	run, err := o.projectRunAppend(ctx, request)
	if err != nil {
		return eventlog.AppendResult{}, err
	}
	return o.schedules.Commit(ctx, request, expectedVersion, next, run)
}

func (o *Orchestrator) cleanup(ctx context.Context, launchReq *adapter.LaunchRequest) error {
	switch launchReq.Disposition {
	case domain.DispositionTerminate:
		return o.terminate(ctx, launchReq)
	case domain.DispositionRelease:
		return o.release(ctx, launchReq)
	default:
		return fmt.Errorf("orchestrator: unknown cleanup disposition %q", launchReq.Disposition)
	}
}

func (o *Orchestrator) terminate(ctx context.Context, launchReq *adapter.LaunchRequest) error {
	request := adapter.TerminateRequest{ConnectionID: launchReq.SelectedOfferConnectionID, OperationKey: "terminate_" + launchReq.AttemptID, LaunchKey: launchReq.LaunchKey, OwnershipToken: launchReq.OwnershipToken, LaunchRequestHash: launchReq.RequestHash}
	hash, err := domain.CanonicalHash(request)
	if err != nil {
		return err
	}
	request.RequestHash = hash
	_, err = o.adapter.Terminate(ctx, request)
	return err
}

func (o *Orchestrator) release(ctx context.Context, launchReq *adapter.LaunchRequest) error {
	request := adapter.ReleaseRequest{

		ConnectionID:      launchReq.SelectedOfferConnectionID,
		OperationKey:      "release_" + launchReq.AttemptID,
		LaunchKey:         launchReq.LaunchKey,
		OwnershipToken:    launchReq.OwnershipToken,
		LaunchRequestHash: launchReq.RequestHash,
		Lane:              launchReq.SelectedOfferLane,
		NativeRef:         launchReq.SelectedOfferNativeRef,
		RunID:             launchReq.RunID,
	}
	hash, err := domain.CanonicalHash(request)
	if err != nil {
		return err
	}
	request.RequestHash = hash
	_, err = o.adapter.Release(ctx, request)
	return err
}

func (o *Orchestrator) recordCleanupFailure(ctx context.Context, runID string, version uint64, launchKey string, disposition domain.Disposition, cleanupErr error) error {
	appendErr := o.appendEvents(ctx, runID, version, fmt.Sprintf("advance:cleanup-failed:%d", version), []eventlog.NewEvent{
		mustEvent(runID, fmt.Sprintf("cleanup_failed_%d", version+1), EventCleanupFailed, publicCleanupError(cleanupErr, launchKey, disposition), o.now()),
	})
	return errors.Join(cleanupErr, appendErr)
}

func (o *Orchestrator) appendEvents(ctx context.Context, runID string, expectedVersion uint64, commandKey string, events []eventlog.NewEvent) error {
	return o.appendEventsAs(ctx, nil, runID, expectedVersion, commandKey, events)
}

// appendEventsAs is appendEvents with an explicit envelope actor, used by the
// human-command entry points (cancel). Advance-loop appends stay actorless:
// their events are system observations, and the issuing command is already
// captured on the command fact itself.
func (o *Orchestrator) appendEventsAs(ctx context.Context, actor json.RawMessage, runID string, expectedVersion uint64, commandKey string, events []eventlog.NewEvent) error {
	request, err := runAppendRequest(actor, runID, expectedVersion, commandKey, events)
	if err != nil {
		return err
	}
	_, err = o.appendRun(ctx, request)
	return err
}

func (o *Orchestrator) appendRun(
	ctx context.Context,
	request eventlog.AppendRequest,
) (eventlog.AppendResult, error) {
	next, err := o.projectRunAppend(ctx, request)
	if err != nil {
		return eventlog.AppendResult{}, err
	}
	return o.runs.Append(ctx, request, next)
}

func (o *Orchestrator) projectRunAppend(
	ctx context.Context,
	request eventlog.AppendRequest,
) (domain.RunRecord, error) {
	history, err := eventlog.ReadFullStream(ctx, o.log, request.Stream)
	if err != nil {
		return domain.RunRecord{}, err
	}
	var state runState
	for _, stored := range history.Events {
		if err := applyStoredEvent(&state, stored); err != nil {
			return domain.RunRecord{}, err
		}
	}
	if history.LastVersion == request.ExpectedStreamVersion {
		for index, event := range request.Events {
			stored := eventlog.StoredEvent{
				ID: event.ID,

				StreamType:    request.Stream.Type,
				StreamID:      request.Stream.ID,
				StreamVersion: request.ExpectedStreamVersion + uint64(index) + 1,
				Type:          event.Type,
				SchemaVersion: event.SchemaVersion,
				OccurredAt:    event.OccurredAt,
				Actor:         request.Actor,
				Visibility:    event.Visibility,
				Data:          event.Data,
				PrivateData:   event.PrivateData,
			}
			if err := applyStoredEvent(&state, stored); err != nil {
				return domain.RunRecord{}, err
			}
		}
	}
	if err := state.validate(); err != nil {
		return domain.RunRecord{}, err
	}
	return runRecordFromState(request.Stream.ID, state), nil
}

func runAppendRequest(actor json.RawMessage, runID string, expectedVersion uint64, commandKey string, events []eventlog.NewEvent) (eventlog.AppendRequest, error) {
	events = scopeEventIDs(runID, events)
	requestHash, err := domain.CanonicalHash(events)
	if err != nil {
		return eventlog.AppendRequest{}, err
	}
	return eventlog.AppendRequest{
		Stream:                runStream(runID),
		ExpectedStreamVersion: expectedVersion,
		CommandKey:            runID + ":" + commandKey,
		RequestHash:           requestHash,
		Actor:                 actor,
		CorrelationID:         runID,
		CausationID:           commandKey,
		Events:                events,
	}, nil
}

func mustEvent(runID, suffix, eventType string, data any, now time.Time) eventlog.NewEvent {
	encoded, err := json.Marshal(data)
	if err != nil {
		panic(err)
	}
	return eventlog.NewEvent{
		ID:            "evt_" + runID + "_" + suffix,
		Type:          eventType,
		SchemaVersion: 1,
		OccurredAt:    now.UTC(),
		Visibility:    eventlog.VisibilityPublic,
		Data:          encoded,
	}
}

func mustPrivateEvent(runID, suffix, eventType string, publicData any, privateData any, now time.Time) eventlog.NewEvent {
	event := mustEvent(runID, suffix, eventType, publicData, now)
	encoded, err := json.Marshal(privateData)
	if err != nil {
		panic(err)
	}
	event.PrivateData = encoded
	return event
}

func scopeEventIDs(runID string, events []eventlog.NewEvent) []eventlog.NewEvent {
	scoped := slices.Clone(events)
	return scoped
}

func eventID(runID, suffix string) string {
	return "evt_" + runID + "_" + suffix
}

func externalIDPart(value string) string {
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('_')
	}
	if b.Len() == 0 {
		return "id"
	}
	return b.String()
}

func shortExternalHash(parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = hash.Write([]byte(part))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))[:12]
}

func runStream(runID string) eventlog.StreamKey {
	return eventlog.StreamKey{Type: "run", ID: runID}
}

func isTerminal(phase adapter.ExternalPhase) bool {
	return phase == adapter.ExternalPhaseSucceeded || phase == adapter.ExternalPhaseFailed || phase == adapter.ExternalPhaseCancelled || phase == adapter.ExternalPhaseReleased
}

func outcomeForPhase(phase adapter.ExternalPhase) domain.RunOutcome {
	switch phase {
	case adapter.ExternalPhaseSucceeded:
		return domain.RunOutcomeSucceeded
	case adapter.ExternalPhaseCancelled:
		return domain.RunOutcomeCancelled
	default:
		return domain.RunOutcomeFailed
	}
}

func selectedOfferByID(offers []domain.OfferSnapshot, id string) (domain.OfferSnapshot, bool) {
	for _, offer := range offers {
		if offer.ID == id {
			return offer, true
		}
	}
	return domain.OfferSnapshot{}, false
}

func launchEnvironment(env map[string]domain.EnvBinding) []adapter.EnvironmentBinding {
	if len(env) == 0 {
		return nil
	}
	names := make([]string, 0, len(env))
	for name := range env {
		names = append(names, name)
	}
	sort.Strings(names)
	bindings := make([]adapter.EnvironmentBinding, 0, len(names))
	for _, name := range names {
		binding := env[name]
		bindings = append(bindings, adapter.EnvironmentBinding{
			Name:  name,
			Value: cloneStringPtr(binding.Value),
		})
	}
	return bindings
}

func cloneStringPtr(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func stringPtr(s string) *string { return &s }
