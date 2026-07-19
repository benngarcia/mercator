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
	"github.com/benngarcia/mercator/internal/reporting"
	"github.com/benngarcia/mercator/internal/scheduler"
)

var ErrRunNotFound = errors.New("orchestrator: run not found")

const (
	EventRunRequested          = "compute.run.requested.v1"
	EventPlacementDecided      = "compute.run.placement_decided.v1"
	EventAttemptCreated        = "compute.run.attempt_created.v1"
	EventLaunchIntentRecorded  = "compute.run.launch_intent_recorded.v1"
	EventLaunchAccepted        = "compute.run.launch_accepted.v1"
	EventLaunchIndeterminate   = "compute.run.launch_indeterminate.v1"
	EventLaunchFailed          = "compute.run.launch_failed.v1"
	EventCancelRequested       = "compute.run.cancel_requested.v1"
	EventCancelAccepted        = "compute.run.cancel_accepted.v1"
	EventExternalStateObserved = "compute.run.external_state_observed.v1"
	EventRunOutcomeRecorded    = "compute.run.outcome_recorded.v1"
	EventCleanupRequested      = "compute.run.cleanup_requested.v1"
	EventCleanupConfirmed      = "compute.run.cleanup_confirmed.v1"
	EventRunClosed             = "compute.run.closed.v1"
	EventRunReported           = "compute.run.reported.v1"
)

type Orchestrator struct {
	log                eventlog.EventLog
	scheduler          scheduler.Scheduler
	adapter            adapter.Adapter
	now                func() time.Time
	reportingPublicURL string
	reportingSigner    *reporting.Signer
	runLocks           keyedMutex
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

func New(log eventlog.EventLog, scheduler scheduler.Scheduler, adapter adapter.Adapter, opts ...Option) *Orchestrator {
	o := &Orchestrator{log: log, scheduler: scheduler, adapter: adapter, now: time.Now}
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
	ResolveImage func(ctx context.Context, image, platform string) (string, error)
}

type CreateRunResult struct {
	RunID     string
	Duplicate bool
}

type runRequestedData struct {
	RunID    string                  `json:"run_id"`
	Workload domain.WorkloadRevision `json:"workload_revision"`
}

type placementData struct {
	Decision domain.PlacementDecision `json:"decision"`
}

type attemptData struct {
	AttemptID      string `json:"attempt_id"`
	LaunchKey      string `json:"launch_key"`
	OwnershipToken string `json:"ownership_token"`
	CleanupLocator string `json:"cleanup_locator"`
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
	// Fill omitted, defaultable fields BEFORE validation so a minimal create body
	// (just an image) expands into a fully-specified, validatable revision.
	req.Workload = domain.NormalizeWorkloadRevision(req.Workload)
	if violations := domain.ValidateWorkloadRevision(req.Workload); len(violations) > 0 {
		return CreateRunResult{}, fmt.Errorf("%s: %s", violations[0].Code, violations[0].Message)
	}
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
	// Resolve tag-form images to digest-pinned references and pin them into the
	// stored/launched revision. This happens AFTER the hash above so replay
	// stays stable regardless of where a moving tag currently points.
	if req.ResolveImage != nil {
		for i := range req.Workload.Spec.Containers {
			c := req.Workload.Spec.Containers[i]
			pinned, rerr := req.ResolveImage(ctx, c.Image, c.Platform.String())
			if rerr != nil {
				return CreateRunResult{}, fmt.Errorf("IMAGE_RESOLUTION_FAILED: %s", rerr.Error())
			}
			req.Workload.Spec.Containers[i].Image = pinned
		}
	}
	privateData, err := json.Marshal(runRequestedData{RunID: req.RunID, Workload: req.Workload})
	if err != nil {
		return CreateRunResult{}, err
	}
	data, err := json.Marshal(publicRunRequestedData{RunID: req.RunID, Workload: publicWorkload(req.Workload)})
	if err != nil {
		return CreateRunResult{}, err
	}
	result, err := o.log.Append(ctx, eventlog.AppendRequest{
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
// stream and performing the single next transition: cleanup, cancel,
// placement, launch, reported-exit finalization, or observation. Every entry
// point (create, cancel, report, poll) appends its fact events and funnels
// through this loop, so there is exactly one place a run moves forward. Each
// iteration re-reads the stream, so state is always derived from the log
// rather than threaded through in memory.
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
		return true, o.releaseAndClose(ctx, workspaceID, runID, version, state.launchIntent)
	case state.cancelRequested:
		return o.stepCancel(ctx, workspaceID, runID, version, state)
	case state.launchIntent == nil:
		return true, o.stepPlace(ctx, workspaceID, runID, version, state)
	case !state.launchAccepted && !state.launchIndeterminate:
		return o.stepLaunch(ctx, workspaceID, runID, version, state)
	case state.exitCode != nil && !state.outcomeRecorded:
		// A reported exit code is authoritative: record the outcome (0 →
		// succeeded, else failed) and request cleanup without waiting for the
		// observation backstop to see the container exit.
		outcome := string(domain.RunOutcomeSucceeded)
		if *state.exitCode != 0 {
			outcome = string(domain.RunOutcomeFailed)
		}
		return true, o.appendEvents(ctx, workspaceID, runID, version, "advance:report-finalize", []eventlog.NewEvent{
			mustEvent(runID, "outcome_recorded", EventRunOutcomeRecorded, map[string]any{"outcome": outcome}, o.now()),
			mustEvent(runID, "cleanup_requested", EventCleanupRequested, map[string]any{"launch_key": state.launchIntent.LaunchKey}, o.now()),
		})
	default:
		observation, err := o.observeLaunch(ctx, workspaceID, state)
		if err != nil {
			return false, err
		}
		return o.recordObservation(ctx, workspaceID, runID, version, state, observation)
	}
}

// stepPlace decides placement and records the decision, attempt, and launch
// intent in one append, so the intent is durable before any adapter call.
func (o *Orchestrator) stepPlace(ctx context.Context, workspaceID, runID string, version uint64, state runState) error {
	decision, attempt, selectedOffer, err := o.decide(ctx, workspaceID, *state.requested, runID)
	if err != nil {
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
	return o.appendEvents(ctx, workspaceID, runID, version, "advance:placement", []eventlog.NewEvent{
		mustEvent(runID, "placement_decided", EventPlacementDecided, placementData{Decision: decision}, o.now()),
		mustEvent(runID, "attempt_created", EventAttemptCreated, attempt, o.now()),
		mustPrivateEvent(runID, "launch_intent_recorded", EventLaunchIntentRecorded, publicLaunchRequest(launchReq), launchReq, o.now()),
	})
}

func (o *Orchestrator) stepLaunch(ctx context.Context, workspaceID, runID string, version uint64, state runState) (bool, error) {
	receipt, err := o.adapter.Launch(ctx, *state.launchIntent)
	if err != nil {
		if errors.Is(err, adapter.ErrLaunchIndeterminate) || errors.Is(err, adapter.ErrLaunchTimeout) {
			_ = o.appendEvents(ctx, workspaceID, runID, version, "advance:launch_indeterminate", []eventlog.NewEvent{
				mustEvent(runID, "launch_indeterminate", EventLaunchIndeterminate, publicAdapterError(err, state.launchIntent.LaunchKey), o.now()),
			})
			return false, err
		}
		// A definitive launch rejection closes the run terminally: nothing
		// external was created (so no cleanup is needed), and retrying the
		// same launch on every poll can never succeed — it left runs wedged
		// in "launching" forever.
		if appendErr := o.appendEvents(ctx, workspaceID, runID, version, "advance:launch_failed", []eventlog.NewEvent{
			mustEvent(runID, "launch_failed", EventLaunchFailed, publicAdapterError(err, state.launchIntent.LaunchKey), o.now()),
			mustEvent(runID, "outcome_recorded", EventRunOutcomeRecorded, map[string]any{"outcome": string(domain.RunOutcomeFailed)}, o.now()),
			mustEvent(runID, "closed", EventRunClosed, map[string]any{"closed": true}, o.now()),
		}); appendErr != nil {
			return false, appendErr
		}
		return false, err
	}
	return true, o.appendEvents(ctx, workspaceID, runID, version, "advance:launch_accepted", []eventlog.NewEvent{
		mustEvent(runID, "launch_accepted", EventLaunchAccepted, receipt, o.now()),
	})
}

// stepCancel completes a requested cancel: close immediately when nothing was
// launched, otherwise cancel at the adapter, then record the terminal
// cancelled observation (the cleanup step closes the run on the next
// iteration).
func (o *Orchestrator) stepCancel(ctx context.Context, workspaceID, runID string, version uint64, state runState) (bool, error) {
	if state.launchIntent == nil {
		// Cancelled before placement: nothing external exists, so close
		// terminally with no cleanup.
		return true, o.appendEvents(ctx, workspaceID, runID, version, "cancel:close_before_launch", []eventlog.NewEvent{
			mustEvent(runID, "outcome_recorded", EventRunOutcomeRecorded, map[string]any{"outcome": string(domain.RunOutcomeCancelled)}, o.now()),
			mustEvent(runID, "closed", EventRunClosed, map[string]any{"closed": true}, o.now()),
		})
	}
	if !state.cancelAccepted {
		cancelReq := adapter.CancelRequest{WorkspaceID: workspaceID, ConnectionID: state.launchIntent.SelectedOfferConnectionID, OperationKey: "cancel_" + state.launchIntent.AttemptID, LaunchKey: state.launchIntent.LaunchKey}
		hash, err := domain.CanonicalHash(cancelReq)
		if err != nil {
			return false, err
		}
		cancelReq.RequestHash = hash
		if _, err := o.adapter.Cancel(ctx, cancelReq); err != nil {
			return false, err
		}
		return true, o.appendEvents(ctx, workspaceID, runID, version, "cancel:accepted", []eventlog.NewEvent{
			mustEvent(runID, "cancel_accepted", EventCancelAccepted, map[string]any{"launch_key": state.launchIntent.LaunchKey}, o.now()),
		})
	}
	if !state.outcomeRecorded {
		return o.recordObservation(ctx, workspaceID, runID, version, state, adapter.ExternalObservation{LaunchKey: state.launchIntent.LaunchKey, Phase: adapter.ExternalPhaseCancelled, ObservedAt: o.now().UTC()})
	}
	return false, nil
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
		if err := state.apply(event); err != nil {
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
// :refresh or :wait. An error on one run never stops advancement of the
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

func (o *Orchestrator) GetPlacementDecision(ctx context.Context, workspaceID, runID string) (domain.PlacementDecision, error) {
	events, err := o.GetRunEvents(ctx, workspaceID, runID)
	if err != nil {
		return domain.PlacementDecision{}, err
	}
	for _, event := range events {
		if event.Type != EventPlacementDecided {
			continue
		}
		var data placementData
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return domain.PlacementDecision{}, err
		}
		return data.Decision, nil
	}
	return domain.PlacementDecision{}, fmt.Errorf("orchestrator: placement decision not found")
}

func (o *Orchestrator) RefreshRun(ctx context.Context, workspaceID, runID string) (domain.RunRecord, error) {
	if err := o.AdvanceRun(ctx, workspaceID, runID); err != nil {
		return domain.RunRecord{}, err
	}
	return o.GetRun(ctx, workspaceID, runID)
}

// CancelRun records the cancel request as a fact (attributed to the acting
// principal), then advances the run: the advance loop cancels at the adapter,
// records the cancelled outcome, and cleans up. Cancelling an already-closed
// run returns the record unchanged.
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
		data := map[string]any{"reason": "user"}
		if state.launchIntent != nil {
			data = map[string]any{"launch_key": state.launchIntent.LaunchKey}
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

type runReportedData struct {
	Type     string          `json:"type"`
	Data     json.RawMessage `json:"data,omitempty"`
	ExitCode *int            `json:"exit_code,omitempty"`
}

// RecordReport appends a compute.run.reported.v1 event to the run's stream.
// It uses optimistic concurrency and retries once on a concurrency conflict.
func (o *Orchestrator) RecordReport(ctx context.Context, workspaceID, runID, reportType string, data json.RawMessage, exitCode *int) error {
	payload := runReportedData{Type: reportType, Data: data, ExitCode: exitCode}
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
		evt := eventlog.NewEvent{
			ID:            eventID(workspaceID, runID, suffix),
			Type:          EventRunReported,
			SchemaVersion: 1,
			OccurredAt:    o.now().UTC(),
			Visibility:    eventlog.VisibilityPublic,
			Data:          encoded,
		}
		requestHash, err := domain.CanonicalHash([]eventlog.NewEvent{evt})
		if err != nil {
			return err
		}
		_, appendErr := o.log.Append(ctx, eventlog.AppendRequest{
			Stream:                runStream(workspaceID, runID),
			ExpectedStreamVersion: version,
			CommandKey:            runID + ":report:" + suffix,
			RequestHash:           requestHash,
			CorrelationID:         runID,
			CausationID:           "report",
			Events:                []eventlog.NewEvent{evt},
		})
		if appendErr == nil {
			if exitCode != nil {
				// Drive the authoritative outcome + prompt cleanup from the
				// reported exit code. Best-effort: any error here is non-fatal —
				// the next poll's AdvanceRun still finalizes.
				_ = o.AdvanceRun(ctx, workspaceID, runID)
			}
			return nil
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

func (o *Orchestrator) decide(ctx context.Context, workspaceID string, requested runRequestedData, runID string) (domain.PlacementDecision, attemptData, domain.OfferSnapshot, error) {
	offers, err := o.adapter.ListOffers(ctx, adapter.OfferRequest{
		WorkspaceID: requested.Workload.WorkspaceID,
		Resources:   requested.Workload.Spec.Resources,
	})
	if err != nil {
		return domain.PlacementDecision{}, attemptData{}, domain.OfferSnapshot{}, err
	}
	decision, err := o.scheduler.Evaluate(ctx, scheduler.SchedulingInput{
		RunID:        runID,
		Workload:     requested.Workload,
		Offers:       offers,
		ModelVersion: "latency-v1",
		EvaluatedAt:  o.now().UTC(),
	})
	if err != nil {
		return domain.PlacementDecision{}, attemptData{}, domain.OfferSnapshot{}, err
	}
	if decision.SelectedOfferSnapshotID == "" {
		return domain.PlacementDecision{}, attemptData{}, domain.OfferSnapshot{}, fmt.Errorf("orchestrator: no feasible offers")
	}
	selectedOffer, ok := selectedOfferByID(offers, decision.SelectedOfferSnapshotID)
	if !ok {
		return domain.PlacementDecision{}, attemptData{}, domain.OfferSnapshot{}, fmt.Errorf("orchestrator: selected offer %s not found", decision.SelectedOfferSnapshotID)
	}
	return decision, newAttempt(workspaceID, runID), selectedOffer, nil
}

// newAttempt mints the run's attempt identity. There is ONE identity — the
// attempt id — and three fixed derivations of it that adapters use for
// different jobs: LaunchKey names the external object, OwnershipToken labels
// it as ours, CleanupLocator addresses its cleanup. The derivations are part
// of the adapter wire contract (container labels, pod env), so they are
// recorded on the launch intent and never re-derived after launch.
func newAttempt(workspaceID, runID string) attemptData {
	id := "att_" + externalIDPart(workspaceID) + "_" + externalIDPart(strings.TrimPrefix(runID, "run_")) + "_" + shortExternalHash(workspaceID, runID)
	return attemptData{
		AttemptID:      id,
		LaunchKey:      "launch_" + id,
		OwnershipToken: "own_" + id,
		CleanupLocator: "cleanup_" + id,
	}
}

func buildLaunchRequest(workspaceID, runID string, requested runRequestedData, attempt attemptData, selectedOffer domain.OfferSnapshot, reportPublicURL, reportToken string) (adapter.LaunchRequest, error) {
	container := requested.Workload.Spec.Containers[0]
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
		Disposition: domain.DispositionForOfferKind(selectedOffer.Kind),
	}
	hash, err := domain.CanonicalHash(launchReq)
	if err != nil {
		return adapter.LaunchRequest{}, err
	}
	launchReq.RequestHash = hash
	return launchReq, nil
}

// recordObservation appends the observation and, on a terminal phase, the
// outcome and cleanup request (the cleanup step then closes the run on the
// next advance iteration). It reports whether the run progressed. A
// non-terminal observation that repeats the last recorded phase carries no new
// information; appending it anyway would grow the stream on every poll
// (waitRun refreshes every 100ms) without bound.
func (o *Orchestrator) recordObservation(ctx context.Context, workspaceID, runID string, version uint64, state runState, observation adapter.ExternalObservation) (bool, error) {
	if !isTerminal(observation.Phase) && observation.Phase == state.lastObservedPhase {
		return false, nil
	}
	toAppend := []eventlog.NewEvent{
		mustEvent(runID, fmt.Sprintf("external_state_observed_%d", version+1), EventExternalStateObserved, observation, o.now()),
	}
	if isTerminal(observation.Phase) && !state.outcomeRecorded {
		toAppend = append(toAppend,
			mustEvent(runID, "outcome_recorded", EventRunOutcomeRecorded, map[string]any{"outcome": outcomeForPhase(observation.Phase)}, o.now()),
			mustEvent(runID, "cleanup_requested", EventCleanupRequested, map[string]any{"launch_key": state.launchIntent.LaunchKey}, o.now()),
		)
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
	if observation.Phase != adapter.ExternalPhaseReleased || !state.launchIndeterminate {
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
	if launchReq == nil {
		return fmt.Errorf("orchestrator: cleanup requested without launch intent")
	}
	// Dispatch on the RECORDED disposition from the launch intent. We never
	// consult live offers or re-derive the disposition here: that is what makes
	// cleanup crash-safe and orphan-free even if offers changed or disappeared.
	// A missing recorded disposition (e.g. a pre-change event log) defaults to
	// release, the safe option that never destroys a host.
	disposition := launchReq.Disposition
	if disposition == "" {
		disposition = domain.DispositionRelease
	}
	if disposition == domain.DispositionTerminate {
		terminateReq := adapter.TerminateRequest{WorkspaceID: workspaceID, ConnectionID: launchReq.SelectedOfferConnectionID, OperationKey: "terminate_" + launchReq.AttemptID, LaunchKey: launchReq.LaunchKey, OwnershipToken: launchReq.OwnershipToken, LaunchRequestHash: launchReq.RequestHash}
		hash, err := domain.CanonicalHash(terminateReq)
		if err != nil {
			return err
		}
		terminateReq.RequestHash = hash
		if _, err := o.adapter.Terminate(ctx, terminateReq); err != nil {
			return err
		}
	} else {
		releaseReq := adapter.ReleaseRequest{WorkspaceID: workspaceID, ConnectionID: launchReq.SelectedOfferConnectionID, OperationKey: "release_" + launchReq.AttemptID, LaunchKey: launchReq.LaunchKey, OwnershipToken: launchReq.OwnershipToken, LaunchRequestHash: launchReq.RequestHash}
		hash, err := domain.CanonicalHash(releaseReq)
		if err != nil {
			return err
		}
		releaseReq.RequestHash = hash
		if _, err := o.adapter.Release(ctx, releaseReq); err != nil {
			return err
		}
	}
	return o.appendEvents(ctx, workspaceID, runID, version, "advance:cleanup", []eventlog.NewEvent{
		mustEvent(runID, "cleanup_confirmed", EventCleanupConfirmed, map[string]any{"launch_key": launchReq.LaunchKey, "disposition": string(disposition)}, o.now()),
		mustEvent(runID, "closed", EventRunClosed, map[string]any{"closed": true}, o.now()),
	})
}

func (o *Orchestrator) appendEvents(ctx context.Context, workspaceID, runID string, expectedVersion uint64, commandKey string, events []eventlog.NewEvent) error {
	return o.appendEventsAs(ctx, nil, workspaceID, runID, expectedVersion, commandKey, events)
}

// appendEventsAs is appendEvents with an explicit envelope actor, used by the
// human-command entry points (cancel). Advance-loop appends stay actorless:
// their events are system observations, and the issuing command is already
// captured on the command fact itself.
func (o *Orchestrator) appendEventsAs(ctx context.Context, actor json.RawMessage, workspaceID, runID string, expectedVersion uint64, commandKey string, events []eventlog.NewEvent) error {
	events = scopeEventIDs(workspaceID, runID, events)
	requestHash, err := domain.CanonicalHash(events)
	if err != nil {
		return err
	}
	_, err = o.log.Append(ctx, eventlog.AppendRequest{
		Stream:                runStream(workspaceID, runID),
		ExpectedStreamVersion: expectedVersion,
		CommandKey:            runID + ":" + commandKey,
		RequestHash:           requestHash,
		Actor:                 actor,
		CorrelationID:         runID,
		CausationID:           commandKey,
		Events:                events,
	})
	return err
}

type runState struct {
	requested           *runRequestedData
	attempt             *attemptData
	launchIntent        *adapter.LaunchRequest
	launchAccepted      bool
	launchIndeterminate bool
	cancelRequested     bool
	cancelAccepted      bool
	outcomeRecorded     bool
	outcome             domain.RunOutcome
	cleanupRequested    bool
	cleanupConfirmed    bool
	closed              bool
	exitCode            *int
	lastObservedPhase   adapter.ExternalPhase
	createdBy           string
	cancelledBy         string
}

// actorSubject extracts the audited subject from an event envelope's actor
// ({"subject": ...}). Empty when the event was recorded without a principal.
func actorSubject(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var actor struct {
		Subject string `json:"subject"`
	}
	if err := json.Unmarshal(raw, &actor); err != nil {
		return ""
	}
	return actor.Subject
}

func reduceRun(events []eventlog.StoredEvent) (runState, error) {
	var state runState
	for _, event := range events {
		if err := state.apply(event); err != nil {
			return runState{}, err
		}
	}
	if err := state.validate(); err != nil {
		return runState{}, err
	}
	return state, nil
}

func (state *runState) apply(event eventlog.StoredEvent) error {
	switch event.Type {
	case EventRunRequested:
		var data runRequestedData
		payload := event.PrivateData
		if len(payload) == 0 {
			payload = event.Data
		}
		if err := json.Unmarshal(payload, &data); err != nil {
			return err
		}
		state.requested = &data
		state.createdBy = actorSubject(event.Actor)
	case EventAttemptCreated:
		var data attemptData
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return err
		}
		state.attempt = &data
	case EventLaunchIntentRecorded:
		var data adapter.LaunchRequest
		payload := event.PrivateData
		if len(payload) == 0 {
			payload = event.Data
		}
		if err := json.Unmarshal(payload, &data); err != nil {
			return err
		}
		state.launchIntent = &data
	case EventExternalStateObserved:
		var data struct {
			Phase    adapter.ExternalPhase `json:"phase"`
			ExitCode *int                  `json:"exit_code"`
		}
		if err := json.Unmarshal(event.Data, &data); err == nil {
			state.lastObservedPhase = data.Phase
			// Only an exited container's code is authoritative: docker
			// observes ExitCode 0 on running containers, and adopting it
			// here made the next advance finalize the run and reclaim a
			// live container. The guard also neutralizes such events
			// already recorded in existing logs. Workload-reported codes
			// (EventRunReported below) are trusted as-is.
			if data.ExitCode != nil && data.Phase.Exited() {
				code := *data.ExitCode
				state.exitCode = &code
			}
		}
	case EventRunReported:
		var data struct {
			ExitCode *int `json:"exit_code"`
		}
		if err := json.Unmarshal(event.Data, &data); err == nil && data.ExitCode != nil {
			code := *data.ExitCode
			state.exitCode = &code
		}
	case EventLaunchAccepted:
		state.launchAccepted = true
	case EventLaunchIndeterminate:
		state.launchIndeterminate = true
	case EventCancelRequested:
		state.cancelRequested = true
		state.cancelledBy = actorSubject(event.Actor)
	case EventCancelAccepted:
		state.cancelAccepted = true
	case EventRunOutcomeRecorded:
		state.outcomeRecorded = true
		var data struct {
			Outcome domain.RunOutcome `json:"outcome"`
		}
		if err := json.Unmarshal(event.Data, &data); err == nil {
			state.outcome = data.Outcome
		}
	case EventCleanupRequested:
		state.cleanupRequested = true
	case EventCleanupConfirmed:
		state.cleanupConfirmed = true
	case EventRunClosed:
		state.closed = true
	}
	return nil
}

func (state runState) validate() error {
	if state.requested == nil {
		return fmt.Errorf("orchestrator: run requested event not found")
	}
	if state.launchIntent == nil && state.attempt != nil {
		return fmt.Errorf("orchestrator: attempt exists without launch intent")
	}
	return nil
}

func runRecordFromState(workspaceID, runID string, state runState) domain.RunRecord {
	record := domain.RunRecord{
		ID:                 runID,
		WorkspaceID:        workspaceID,
		WorkloadRevisionID: state.requested.Workload.ID,
		Phase:              "requested",
		Cleanup:            domain.CleanupNotRequired,
		CreatedBy:          state.createdBy,
		CancelledBy:        state.cancelledBy,
	}
	if state.launchIntent != nil {
		record.Phase = "launching"
		// Surface the RECORDED disposition (defaulting a missing one to release)
		// so operators can see whether this run will terminate an owned host or
		// merely release a borrowed slot.
		record.Disposition = state.launchIntent.Disposition
		if record.Disposition == "" {
			record.Disposition = domain.DispositionRelease
		}
	}
	if state.launchAccepted || state.launchIndeterminate {
		record.Phase = "running"
		record.Cleanup = domain.CleanupPending
	}
	if state.cleanupRequested {
		record.Phase = "cleaning_up"
		record.Cleanup = domain.CleanupPending
	}
	if state.cleanupConfirmed {
		record.Cleanup = domain.CleanupConfirmed
	}
	if state.exitCode != nil {
		code := *state.exitCode
		record.ExitCode = &code
	}
	if state.closed {
		record.Phase = "closed"
		record.Closed = true
		if state.outcomeRecorded {
			record.Outcome = state.outcome
			if record.Outcome == "" {
				record.Outcome = domain.RunOutcomeSucceeded
			}
		}
	}
	return record
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

func outcomeForPhase(phase adapter.ExternalPhase) string {
	switch phase {
	case adapter.ExternalPhaseSucceeded:
		return string(domain.RunOutcomeSucceeded)
	case adapter.ExternalPhaseCancelled:
		return string(domain.RunOutcomeCancelled)
	default:
		return string(domain.RunOutcomeFailed)
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
