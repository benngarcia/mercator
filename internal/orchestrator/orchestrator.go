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
	EventRunRequested            = "compute.run.requested.v1"
	EventBookingDecided          = "compute.run.booking_decided.v1"
	EventBookingDispatched       = "compute.run.booking_dispatched.v1"
	EventAttemptCreated          = "compute.run.attempt_created.v1"
	EventLaunchIntentRecorded    = "compute.run.launch_intent_recorded.v1"
	EventLaunchAccepted          = "compute.run.launch_accepted.v1"
	EventLaunchIndeterminate     = "compute.run.launch_indeterminate.v1"
	EventLaunchFailed            = "compute.run.launch_failed.v1"
	EventCancelRequested         = "compute.run.cancel_requested.v1"
	EventCancelAccepted          = "compute.run.cancel_accepted.v1"
	EventExternalStateObserved   = "compute.run.external_state_observed.v1"
	EventRunOutcomeRecorded      = "compute.run.outcome_recorded.v1"
	EventCleanupRequested        = "compute.run.cleanup_requested.v1"
	EventCleanupFailed           = "compute.run.cleanup_failed.v1"
	EventCleanupConfirmed        = "compute.run.cleanup_confirmed.v1"
	EventRunClosed               = "compute.run.closed.v1"
	EventRunReported             = "compute.run.reported.v1"
	runCloseReasonRetryExhausted = "RETRY_EXHAUSTED"
)

type Orchestrator struct {
	log                eventlog.WorkspaceEventLog
	scheduler          scheduler.Scheduler
	adapter            Adapter
	schedules          rentalschedule.Store
	now                func() time.Time
	reportingPublicURL string
	reportingSigner    *reporting.Signer
	runLocks           keyedMutex
}

type Adapter interface {
	ListOffers(ctx context.Context, req adapter.OfferRequest) ([]domain.OfferSnapshot, error)
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

func New(log eventlog.WorkspaceEventLog, scheduler scheduler.Scheduler, adapter Adapter, opts ...Option) *Orchestrator {
	o := &Orchestrator{
		log:       log,
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
	WorkspaceID    string
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

type CreateRunResult struct {
	RunID     string
	Duplicate bool
}

func (o *Orchestrator) CreateRun(ctx context.Context, req CreateRunRequest) (CreateRunResult, error) {
	if req.WorkspaceID == "" || req.RunID == "" {
		return CreateRunResult{}, fmt.Errorf("orchestrator: workspace_id and run_id are required")
	}
	if req.CommandKey == "" {
		req.CommandKey = req.IdempotencyKey
	}
	if req.CommandKey == "" {
		return CreateRunResult{}, fmt.Errorf("orchestrator: idempotency key is required")
	}
	if req.Workload.WorkspaceID != "" && req.WorkspaceID != req.Workload.WorkspaceID {
		return CreateRunResult{}, fmt.Errorf("WORKSPACE_MISMATCH: request workspace_id must match workload workspace_id")
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
	privateData, err := json.Marshal(runRequestedData{RunID: req.RunID, Workload: req.Workload})
	if err != nil {
		return CreateRunResult{}, err
	}
	data, err := json.Marshal(publicRunRequestedData{RunID: req.RunID, Workload: publicWorkload(req.Workload)})
	if err != nil {
		return CreateRunResult{}, err
	}
	result, err := o.log.AppendIfWorkspaceActive(ctx, eventlog.AppendRequest{
		Stream:                runStream(req.WorkspaceID, req.RunID),
		ExpectedStreamVersion: 0,
		CommandKey:            req.CommandKey,
		RequestHash:           requestHash,
		Actor:                 req.Actor,
		CorrelationID:         req.RunID,
		CausationID:           req.CommandKey,
		Events: []eventlog.NewEvent{{
			ID:            eventID(req.WorkspaceID, req.RunID, "requested"),
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
		// A replay (same workspace + command key) returns the ORIGINAL stored
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
func (o *Orchestrator) AdvanceRun(ctx context.Context, workspaceID, runID string) error {
	unlock := o.runLocks.Lock(workspaceID + "/" + runID)
	defer unlock()

	for {
		events, err := o.GetRunEvents(ctx, workspaceID, runID)
		if err != nil {
			return err
		}
		state, err := reduceRun(events)
		if err != nil {
			return err
		}
		progressed, err := o.step(ctx, workspaceID, runID, streamVersion(events), state)
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
func (o *Orchestrator) step(ctx context.Context, workspaceID, runID string, version uint64, state runState) (bool, error) {
	switch {
	case state.closed:
		return false, nil
	case state.cleanupRequested && !state.cleanupConfirmed:
		return true, o.releaseAndCloseScheduled(ctx, workspaceID, runID, version, state)
	case state.firstTerminal != nil && !state.outcomeRecorded:
		return true, o.recordTerminalTransition(ctx, workspaceID, runID, version, state)
	case state.bookingQueued():
		return o.dispatchQueuedBooking(ctx, workspaceID, runID, version, state)
	case state.launchIntent == nil:
		return true, o.stepPlace(ctx, workspaceID, runID, version, state)
	case state.replacementEligible():
		return true, o.stepPlace(ctx, workspaceID, runID, version, state)
	case !state.launchAccepted && state.launchFailure == nil:
		return o.stepLaunch(ctx, workspaceID, runID, version, state)
	default:
		observation, err := o.observeLaunch(ctx, workspaceID, state)
		if err != nil {
			return false, err
		}
		return o.recordObservation(ctx, workspaceID, runID, version, state, observation)
	}
}

func (o *Orchestrator) dispatchQueuedBooking(ctx context.Context, workspaceID, runID string, version uint64, state runState) (bool, error) {
	schedules, err := o.schedules.List(ctx, workspaceID)
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
	attempt := newAttempt(workspaceID, runID, state.attemptCount+1)
	reportPublicURL, reportToken := "", ""
	if o.reportingPublicURL != "" && o.reportingSigner != nil && o.reportingSigner.Enabled() {
		reportPublicURL = o.reportingPublicURL
		reportToken = o.reportingSigner.Token(workspaceID, runID)
	}
	launchReq, err := buildLaunchRequest(workspaceID, runID, *state.requested, attempt, selectedOffer, reportPublicURL, reportToken)
	if err != nil {
		return false, err
	}
	err = o.appendEvents(ctx, workspaceID, runID, version, "advance:dispatch:"+booking.ID, []eventlog.NewEvent{
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
		}, nil
	}
	return domain.OfferSnapshot{}, fmt.Errorf("orchestrator: selected candidate %q is missing", decision.SelectedOfferSnapshotID)
}

// stepPlace decides placement and records the decision, attempt, and launch
// intent in one append, so the intent is durable before any adapter call.
func (o *Orchestrator) stepPlace(ctx context.Context, workspaceID, runID string, version uint64, state runState) error {
	attemptNumber := state.attemptCount + 1
	decision, attempt, selectedOffer, schedule, err := o.decide(ctx, workspaceID, *state.requested, runID, attemptNumber, state.excludedOfferSnapshotIDs)
	if err != nil {
		return err
	}
	if decision.SelectedOfferSnapshotID == "" {
		if state.replacementEligible() {
			return o.closeRetryExhausted(ctx, workspaceID, runID, version, decision)
		}
		return ErrNoFeasibleOffers
	}
	nextSchedule, err := reserveDecision(*state.requested, decision, schedule)
	if err != nil {
		return err
	}
	events := []eventlog.NewEvent{
		mustEvent(runID, "booking_decided_"+decision.Booking.ID, EventBookingDecided, bookingDecisionData{Decision: decision}, o.now()),
	}
	commandKey := "advance:placement:" + decision.Booking.ID
	if decision.Booking.State == domain.BookingStateQueued {
		request, requestErr := runAppendRequest(nil, workspaceID, runID, version, commandKey, events)
		if requestErr != nil {
			return requestErr
		}
		_, err = o.schedules.Commit(ctx, request, decision.Booking.ScheduleVersion-1, nextSchedule)
		return err
	}
	reportPublicURL, reportToken := "", ""
	if o.reportingPublicURL != "" && o.reportingSigner != nil && o.reportingSigner.Enabled() {
		reportPublicURL = o.reportingPublicURL
		reportToken = o.reportingSigner.Token(workspaceID, runID)
	}
	launchReq, err := buildLaunchRequest(workspaceID, runID, *state.requested, attempt, selectedOffer, reportPublicURL, reportToken)
	if err != nil {
		return err
	}
	events = append(events,
		mustEvent(runID, "attempt_created_"+attempt.AttemptID, EventAttemptCreated, attempt, o.now()),
		mustPrivateEvent(runID, "launch_intent_recorded_"+attempt.AttemptID, EventLaunchIntentRecorded, publicLaunchRequest(launchReq), launchReq, o.now()),
	)
	request, err := runAppendRequest(nil, workspaceID, runID, version, commandKey, events)
	if err != nil {
		return err
	}
	_, err = o.schedules.Commit(ctx, request, decision.Booking.ScheduleVersion-1, nextSchedule)
	return err
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
func (o *Orchestrator) recordTerminalTransition(ctx context.Context, workspaceID, runID string, version uint64, state runState) error {
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
			return o.completeBookingAndAppend(ctx, workspaceID, runID, version, state, "advance:terminal-before-launch", events)
		}
		return o.appendEvents(ctx, workspaceID, runID, version, "advance:terminal-before-launch", events)
	}
	return o.appendEvents(ctx, workspaceID, runID, version, "advance:terminal", []eventlog.NewEvent{
		mustEvent(runID, "outcome_recorded", EventRunOutcomeRecorded, runOutcomeRecordedData{Outcome: state.firstTerminal.Outcome}, o.now()),
		mustEvent(runID, "cleanup_requested", EventCleanupRequested, launchReferenceData{LaunchKey: state.launchIntent.LaunchKey}, o.now()),
	})
}

func (o *Orchestrator) GetRunEvents(ctx context.Context, workspaceID, runID string) ([]eventlog.StoredEvent, error) {
	history, err := eventlog.ReadFullStream(ctx, o.log, runStream(workspaceID, runID))
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

func (o *Orchestrator) GetRun(ctx context.Context, workspaceID, runID string) (domain.RunRecord, error) {
	events, err := o.GetRunEvents(ctx, workspaceID, runID)
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
	return runRecordFromState(workspaceID, runID, state), nil
}

func (o *Orchestrator) ListRuns(ctx context.Context, workspaceID string) ([]domain.RunRecord, error) {
	states := make(map[string]*runState)
	for event, err := range eventlog.ScanAll(ctx, o.log, eventlog.EventFilter{
		WorkspaceID: workspaceID,
		StreamTypes: []string{"run"},
	}) {
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
		records = append(records, runRecordFromState(workspaceID, runID, *state))
	}
	sort.Slice(records, func(i, j int) bool { return records[i].ID < records[j].ID })
	return records, nil
}

// ListRunWorkspaces returns the durable event-log partitions that have owned a
// run. Workspace IDs are data carried by run facts, not server configuration;
// the background reconciler reads the event log's distinct-partition index so
// every persisted run continues to converge after restart without repeatedly
// scanning historical run facts.
func (o *Orchestrator) ListRunWorkspaces(ctx context.Context) ([]string, error) {
	return o.log.ListWorkspaceIDs(ctx, eventlog.EventFilter{
		StreamTypes: []string{"run"},
		EventTypes:  []string{EventRunRequested},
	})
}

// AdvanceOpenRunsResult summarizes one background advancement sweep of a
// workspace: how many open runs were found and how many of them reached the
// closed state during the sweep.
type AdvanceOpenRunsResult struct {
	Open   int
	Closed int
}

// AdvanceOpenRuns drives every open (not yet closed) run in the workspace
// through AdvanceRun so runs converge to closed with zero client involvement:
// observing container exits, recording terminal outcomes, and confirming
// cleanup is the broker's job, not something every client must poll for via
// /refresh or /wait. An error on one run never stops advancement of the
// others; per-run errors are joined into the returned error alongside the
// sweep result, which stays valid either way.
func (o *Orchestrator) AdvanceOpenRuns(ctx context.Context, workspaceID string) (AdvanceOpenRunsResult, error) {
	openRuns, err := o.listOpenRunIDs(ctx, workspaceID)
	if err != nil {
		return AdvanceOpenRunsResult{}, err
	}
	result := AdvanceOpenRunsResult{Open: len(openRuns)}
	var errs []error
	for _, runID := range openRuns {
		if err := o.AdvanceRun(ctx, workspaceID, runID); err != nil {
			errs = append(errs, fmt.Errorf("advance %s: %w", runID, err))
			continue
		}
		record, err := o.GetRun(ctx, workspaceID, runID)
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
// a workspace whose history is all closed runs costs one filtered index scan
// and zero stream reads per tick.
func (o *Orchestrator) listOpenRunIDs(ctx context.Context, workspaceID string) ([]string, error) {
	var requested []string
	closed := map[string]bool{}
	for event, err := range eventlog.ScanAll(ctx, o.log, eventlog.EventFilter{
		WorkspaceID: workspaceID,
		StreamTypes: []string{"run"},
		EventTypes:  []string{EventRunRequested, EventRunClosed},
	}) {
		if err != nil {
			return nil, err
		}
		switch event.Type {
		case EventRunRequested:
			requested = append(requested, event.StreamID)
		case EventRunClosed:
			closed[event.StreamID] = true
		}
	}
	var open []string
	for _, runID := range requested {
		if !closed[runID] {
			open = append(open, runID)
		}
	}
	return open, nil
}

func (o *Orchestrator) GetBookingDecision(ctx context.Context, workspaceID, runID string) (domain.BookingDecision, error) {
	events, err := o.GetRunEvents(ctx, workspaceID, runID)
	if err != nil {
		return domain.BookingDecision{}, err
	}
	var latest domain.BookingDecision
	found := false
	for _, event := range events {
		if event.Type != EventBookingDecided {
			continue
		}
		var data bookingDecisionData
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return domain.BookingDecision{}, err
		}
		latest = data.Decision
		found = true
	}
	if found {
		return latest, nil
	}
	return domain.BookingDecision{}, fmt.Errorf("orchestrator: booking decision not found")
}

func (o *Orchestrator) RefreshRun(ctx context.Context, workspaceID, runID string) (domain.RunRecord, error) {
	if err := o.AdvanceRun(ctx, workspaceID, runID); err != nil {
		return domain.RunRecord{}, err
	}
	return o.GetRun(ctx, workspaceID, runID)
}

// CancelRun records the cancel request as a fact attributed to the acting
// principal, then advances it through the same terminal cleanup transition as
// workload exit and provider exit. Cancelling a closed run returns it unchanged.
func (o *Orchestrator) CancelRun(ctx context.Context, workspaceID, runID string, actor json.RawMessage) (domain.RunRecord, error) {
	events, err := o.GetRunEvents(ctx, workspaceID, runID)
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
		return runRecordFromState(workspaceID, runID, state), nil
	}
	if !state.cancelRequested {
		data := cancelRequestedData{Reason: "user"}
		if state.launchIntent != nil {
			data = cancelRequestedData{LaunchKey: state.launchIntent.LaunchKey}
		}
		if err := o.appendEventsAs(ctx, actor, workspaceID, runID, streamVersion(events), "cancel:requested", []eventlog.NewEvent{
			mustEvent(runID, "cancel_requested", EventCancelRequested, data, o.now()),
		}); err != nil {
			return domain.RunRecord{}, err
		}
	}
	if err := o.AdvanceRun(ctx, workspaceID, runID); err != nil {
		return domain.RunRecord{}, err
	}
	return o.GetRun(ctx, workspaceID, runID)
}

// RecordReport appends a compute.run.reported.v1 fact and returns before
// cleanup. Terminal reports use one semantic command per run, so an exact
// replay is idempotent and conflicting terminal data fails explicitly.
func (o *Orchestrator) RecordReport(ctx context.Context, workspaceID, runID string, report RunReport) error {
	if report == nil {
		return fmt.Errorf("%w: report is required", ErrInvalidReport)
	}
	payload := report.payload()
	unlock := o.runLocks.Lock(workspaceID + "/" + runID)
	defer unlock()

	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("orchestrator: marshal report data: %w", err)
	}

	for attempt := 0; attempt < 2; attempt++ {
		events, err := o.GetRunEvents(ctx, workspaceID, runID)
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
			ID:            eventID(workspaceID, runID, suffix),
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
		_, appendErr := o.log.Append(ctx, eventlog.AppendRequest{
			Stream:                runStream(workspaceID, runID),
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
func newAttempt(workspaceID, runID string, attemptNumber int) attemptData {
	ordinal := fmt.Sprintf("%d", attemptNumber)
	id := "att_" + externalIDPart(workspaceID) + "_" + externalIDPart(strings.TrimPrefix(runID, "run_")) + "_" + ordinal + "_" + shortExternalHash(workspaceID, runID, ordinal)
	return attemptData{
		AttemptID:      id,
		LaunchKey:      "launch_" + id,
		OwnershipToken: "own_" + id,
		CleanupLocator: "cleanup_" + id,
	}
}

func buildLaunchRequest(workspaceID, runID string, requested runRequestedData, attempt attemptData, selectedOffer domain.OfferSnapshot, reportPublicURL, reportToken string) (adapter.LaunchRequest, error) {
	container := requested.Workload.Spec.Containers[0]
	disposition, err := domain.DispositionForOfferKind(selectedOffer.Kind)
	if err != nil {
		return adapter.LaunchRequest{}, err
	}
	env := launchEnvironment(container.Env)
	if reportPublicURL != "" && reportToken != "" {
		env = append(env,
			adapter.EnvironmentBinding{Name: "MERCATOR_RUN_ID", Value: stringPtr(runID)},
			adapter.EnvironmentBinding{Name: "MERCATOR_REPORT_URL", Value: stringPtr(reportPublicURL)},
			adapter.EnvironmentBinding{Name: "MERCATOR_RUN_TOKEN", Value: stringPtr(reportToken)},
			adapter.EnvironmentBinding{Name: "MERCATOR_WORKSPACE_ID", Value: stringPtr(workspaceID)},
		)
	}
	launchReq := adapter.LaunchRequest{
		OperationKey:              attempt.LaunchKey,
		WorkspaceID:               workspaceID,
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
		MaxRuntimeSeconds:         requested.Workload.Spec.Execution.MaxRuntimeSeconds,
		SelectedOfferSnapshotID:   selectedOffer.ID,
		SelectedOfferConnectionID: selectedOffer.ConnectionID,
		SelectedOfferAdapterType:  selectedOffer.AdapterType,
		SelectedOfferNativeRef:    selectedOffer.NativeRef,
		// Derive the cleanup disposition from the selected offer's Kind and RECORD
		// it on the launch intent now. This recorded value — not the offer kind
		// looked up later — is the source of truth for cleanup.
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
func (o *Orchestrator) recordObservation(ctx context.Context, workspaceID, runID string, version uint64, state runState, observation adapter.ExternalObservation) (bool, error) {
	if !isTerminal(observation.Phase) && observation.Phase == state.lastObservedPhase {
		return false, nil
	}
	toAppend := []eventlog.NewEvent{
		mustEvent(runID, fmt.Sprintf("external_state_observed_%d", version+1), EventExternalStateObserved, observation, o.now()),
	}
	if err := o.appendEvents(ctx, workspaceID, runID, version, fmt.Sprintf("advance:observe:%d", version), toAppend); err != nil {
		return false, err
	}
	return isTerminal(observation.Phase), nil
}

func (o *Orchestrator) observeLaunch(ctx context.Context, workspaceID string, state runState) (adapter.ExternalObservation, error) {
	observation, err := o.adapter.Observe(ctx, adapter.ObserveRequest{
		WorkspaceID:    workspaceID,
		ConnectionID:   state.launchIntent.SelectedOfferConnectionID,
		LaunchKey:      state.launchIntent.LaunchKey,
		OwnershipToken: state.launchIntent.OwnershipToken,
		RequestHash:    state.launchIntent.RequestHash,
	})
	if err != nil {
		return adapter.ExternalObservation{}, err
	}
	if observation.Phase != adapter.ExternalPhaseReleased || !state.launchIndeterminate() {
		return observation, nil
	}
	owned, err := o.adapter.ListOwned(ctx, adapter.OwnershipQuery{WorkspaceID: workspaceID})
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

func (o *Orchestrator) releaseAndClose(ctx context.Context, workspaceID, runID string, version uint64, launchReq *adapter.LaunchRequest) error {
	return o.releaseAndCloseScheduled(ctx, workspaceID, runID, version, runState{launchIntent: launchReq})
}

func (o *Orchestrator) releaseAndCloseScheduled(ctx context.Context, workspaceID, runID string, version uint64, state runState) error {
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
	if err := o.cleanup(ctx, workspaceID, launchReq); err != nil {
		return o.recordCleanupFailure(ctx, workspaceID, runID, version, launchReq.LaunchKey, disposition, err)
	}
	events := []eventlog.NewEvent{
		mustEvent(runID, "cleanup_confirmed", EventCleanupConfirmed, cleanupConfirmedData{LaunchKey: launchReq.LaunchKey, Disposition: disposition}, o.now()),
		mustEvent(runID, "closed", EventRunClosed, runClosedData{Closed: true}, o.now()),
	}
	return o.completeBookingAndAppend(ctx, workspaceID, runID, version, state, "advance:cleanup", events)
}

func (o *Orchestrator) completeBookingAndAppend(ctx context.Context, workspaceID, runID string, version uint64, state runState, commandKey string, events []eventlog.NewEvent) error {
	if state.bookingDecision == nil || state.bookingDecision.Booking == nil {
		return fmt.Errorf("orchestrator: transition requires a recorded Booking")
	}
	schedules, err := o.schedules.List(ctx, workspaceID)
	if err != nil {
		return fmt.Errorf("orchestrator: list Rental Schedules: %w", err)
	}
	schedule := schedules[state.bookingDecision.Booking.RentalID]
	next, _, err := schedule.Complete(state.bookingDecision.Booking.ID, o.now().UTC())
	if err != nil {
		return err
	}
	request, err := runAppendRequest(nil, workspaceID, runID, version, commandKey, events)
	if err != nil {
		return err
	}
	_, err = o.schedules.Commit(ctx, request, schedule.Version, next)
	return err
}

func (o *Orchestrator) cleanup(ctx context.Context, workspaceID string, launchReq *adapter.LaunchRequest) error {
	switch launchReq.Disposition {
	case domain.DispositionTerminate:
		return o.terminate(ctx, workspaceID, launchReq)
	case domain.DispositionRelease:
		return o.release(ctx, workspaceID, launchReq)
	default:
		return fmt.Errorf("orchestrator: unknown cleanup disposition %q", launchReq.Disposition)
	}
}

func (o *Orchestrator) terminate(ctx context.Context, workspaceID string, launchReq *adapter.LaunchRequest) error {
	request := adapter.TerminateRequest{WorkspaceID: workspaceID, ConnectionID: launchReq.SelectedOfferConnectionID, OperationKey: "terminate_" + launchReq.AttemptID, LaunchKey: launchReq.LaunchKey, OwnershipToken: launchReq.OwnershipToken, LaunchRequestHash: launchReq.RequestHash}
	hash, err := domain.CanonicalHash(request)
	if err != nil {
		return err
	}
	request.RequestHash = hash
	_, err = o.adapter.Terminate(ctx, request)
	return err
}

func (o *Orchestrator) release(ctx context.Context, workspaceID string, launchReq *adapter.LaunchRequest) error {
	request := adapter.ReleaseRequest{WorkspaceID: workspaceID, ConnectionID: launchReq.SelectedOfferConnectionID, OperationKey: "release_" + launchReq.AttemptID, LaunchKey: launchReq.LaunchKey, OwnershipToken: launchReq.OwnershipToken, LaunchRequestHash: launchReq.RequestHash}
	hash, err := domain.CanonicalHash(request)
	if err != nil {
		return err
	}
	request.RequestHash = hash
	_, err = o.adapter.Release(ctx, request)
	return err
}

func (o *Orchestrator) recordCleanupFailure(ctx context.Context, workspaceID, runID string, version uint64, launchKey string, disposition domain.Disposition, cleanupErr error) error {
	appendErr := o.appendEvents(ctx, workspaceID, runID, version, fmt.Sprintf("advance:cleanup-failed:%d", version), []eventlog.NewEvent{
		mustEvent(runID, fmt.Sprintf("cleanup_failed_%d", version+1), EventCleanupFailed, publicCleanupError(cleanupErr, launchKey, disposition), o.now()),
	})
	return errors.Join(cleanupErr, appendErr)
}

func (o *Orchestrator) appendEvents(ctx context.Context, workspaceID, runID string, expectedVersion uint64, commandKey string, events []eventlog.NewEvent) error {
	return o.appendEventsAs(ctx, nil, workspaceID, runID, expectedVersion, commandKey, events)
}

// appendEventsAs is appendEvents with an explicit envelope actor, used by the
// human-command entry points (cancel). Advance-loop appends stay actorless:
// their events are system observations, and the issuing command is already
// captured on the command fact itself.
func (o *Orchestrator) appendEventsAs(ctx context.Context, actor json.RawMessage, workspaceID, runID string, expectedVersion uint64, commandKey string, events []eventlog.NewEvent) error {
	request, err := runAppendRequest(actor, workspaceID, runID, expectedVersion, commandKey, events)
	if err != nil {
		return err
	}
	_, err = o.log.Append(ctx, request)
	return err
}

func runAppendRequest(actor json.RawMessage, workspaceID, runID string, expectedVersion uint64, commandKey string, events []eventlog.NewEvent) (eventlog.AppendRequest, error) {
	events = scopeEventIDs(workspaceID, runID, events)
	requestHash, err := domain.CanonicalHash(events)
	if err != nil {
		return eventlog.AppendRequest{}, err
	}
	return eventlog.AppendRequest{
		Stream:                runStream(workspaceID, runID),
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

func scopeEventIDs(workspaceID, runID string, events []eventlog.NewEvent) []eventlog.NewEvent {
	scoped := slices.Clone(events)
	unscopedPrefix := "evt_" + runID + "_"
	scopedPrefix := "evt_" + workspaceID + "_" + runID + "_"
	for i := range scoped {
		if strings.HasPrefix(scoped[i].ID, scopedPrefix) {
			continue
		}
		if strings.HasPrefix(scoped[i].ID, unscopedPrefix) {
			scoped[i].ID = scopedPrefix + strings.TrimPrefix(scoped[i].ID, unscopedPrefix)
			continue
		}
		scoped[i].ID = workspaceID + "_" + scoped[i].ID
	}
	return scoped
}

func eventID(workspaceID, runID, suffix string) string {
	return "evt_" + workspaceID + "_" + runID + "_" + suffix
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

func runStream(workspaceID, runID string) eventlog.StreamKey {
	return eventlog.StreamKey{WorkspaceID: workspaceID, Type: "run", ID: runID}
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
