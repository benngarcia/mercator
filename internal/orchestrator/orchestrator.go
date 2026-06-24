package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
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

type publicRunRequestedData struct {
	RunID    string                 `json:"run_id"`
	Workload publicWorkloadRevision `json:"workload_revision"`
}

type publicWorkloadRevision struct {
	ID          string             `json:"id"`
	WorkspaceID string             `json:"workspace_id"`
	WorkloadID  string             `json:"workload_id"`
	Digest      string             `json:"digest"`
	Spec        publicWorkloadSpec `json:"spec"`
}

type publicWorkloadSpec struct {
	Containers []publicContainerSpec       `json:"containers"`
	Resources  domain.ResourceRequirements `json:"resources"`
	Network    domain.NetworkRequirements  `json:"network"`
	Placement  domain.PlacementPolicy      `json:"placement"`
	Execution  domain.ExecutionPolicy      `json:"execution"`
	Metadata   map[string]string           `json:"metadata,omitempty"`
	Raw        map[string]json.RawMessage  `json:"raw,omitempty"`
}

type publicContainerSpec struct {
	Name       string                      `json:"name"`
	Image      string                      `json:"image"`
	Platform   domain.Platform             `json:"platform"`
	Entrypoint *[]string                   `json:"entrypoint,omitempty"`
	Args       []string                    `json:"args,omitempty"`
	Env        map[string]publicEnvBinding `json:"env,omitempty"`
	Ports      []domain.PortSpec           `json:"ports,omitempty"`
}

type publicEnvBinding struct {
	Kind string `json:"kind"`
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

func (o *Orchestrator) AdvanceRun(ctx context.Context, workspaceID, runID string) error {
	events, err := o.GetRunEvents(ctx, workspaceID, runID)
	if err != nil {
		return err
	}
	state, err := reduceRun(events)
	if err != nil {
		return err
	}
	if state.closed {
		return nil
	}
	version := uint64(len(events))

	if state.cleanupRequested && !state.cleanupConfirmed {
		return o.releaseAndClose(ctx, workspaceID, runID, version, state.launchIntent)
	}

	if state.launchIntent == nil {
		decision, attempt, selectedOffer, err := o.decide(ctx, workspaceID, *state.requested, runID)
		if err != nil {
			return err
		}
		reportPublicURL, reportToken := "", ""
		if o.reportingPublicURL != "" && o.reportingSigner != nil && o.reportingSigner.Enabled() {
			reportPublicURL = o.reportingPublicURL
			reportToken = o.reportingSigner.Token(runID)
		}
		launchReq, err := buildLaunchRequest(workspaceID, runID, *state.requested, attempt, selectedOffer, reportPublicURL, reportToken)
		if err != nil {
			return err
		}
		if err := o.appendEvents(ctx, workspaceID, runID, version, "advance:placement", []eventlog.NewEvent{
			mustEvent(runID, "placement_decided", EventPlacementDecided, placementData{Decision: decision}, o.now()),
			mustEvent(runID, "attempt_created", EventAttemptCreated, attempt, o.now()),
			mustPrivateEvent(runID, "launch_intent_recorded", EventLaunchIntentRecorded, publicLaunchRequest(launchReq), launchReq, o.now()),
		}); err != nil {
			return err
		}
		version += 3
		state.attempt = &attempt
		state.launchIntent = &launchReq
	}

	if !state.launchAccepted && !state.launchIndeterminate {
		receipt, err := o.adapter.Launch(ctx, *state.launchIntent)
		if err != nil {
			if errors.Is(err, adapter.ErrLaunchIndeterminate) || errors.Is(err, adapter.ErrLaunchTimeout) {
				_ = o.appendEvents(ctx, workspaceID, runID, version, "advance:launch_indeterminate", []eventlog.NewEvent{
					mustEvent(runID, "launch_indeterminate", EventLaunchIndeterminate, publicAdapterError(err, state.launchIntent.LaunchKey), o.now()),
				})
				return err
			}
			_ = o.appendEvents(ctx, workspaceID, runID, version, "advance:launch_failed", []eventlog.NewEvent{
				mustEvent(runID, "launch_failed", EventLaunchFailed, publicAdapterError(err, state.launchIntent.LaunchKey), o.now()),
			})
			return err
		}
		if err := o.appendEvents(ctx, workspaceID, runID, version, "advance:launch_accepted", []eventlog.NewEvent{
			mustEvent(runID, "launch_accepted", EventLaunchAccepted, receipt, o.now()),
		}); err != nil {
			return err
		}
		version++
		state.launchAccepted = true
	}

	observation, err := o.observeLaunch(ctx, workspaceID, state)
	if err != nil {
		return err
	}
	return o.recordObservation(ctx, workspaceID, runID, version, state, observation)
}

func (o *Orchestrator) GetRunEvents(ctx context.Context, workspaceID, runID string) ([]eventlog.StoredEvent, error) {
	return o.log.ReadStream(ctx, runStream(workspaceID, runID), 0, 1000)
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
	events, err := o.log.ReadAll(ctx, 0, 1000, eventlog.EventFilter{
		WorkspaceID: workspaceID,
		StreamTypes: []string{"run"},
		EventTypes:  []string{EventRunRequested},
	})
	if err != nil {
		return nil, err
	}
	records := make([]domain.RunRecord, 0, len(events))
	for _, event := range events {
		record, err := o.GetRun(ctx, workspaceID, event.StreamID)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].ID < records[j].ID })
	return records, nil
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

func (o *Orchestrator) CancelRun(ctx context.Context, workspaceID, runID string) (domain.RunRecord, error) {
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
	version := uint64(len(events))
	if state.launchIntent == nil {
		if err := o.appendEvents(ctx, workspaceID, runID, version, "cancel:close_before_launch", []eventlog.NewEvent{
			mustEvent(runID, "cancel_requested", EventCancelRequested, map[string]any{"reason": "user"}, o.now()),
			mustEvent(runID, "outcome_recorded", EventRunOutcomeRecorded, map[string]any{"outcome": string(domain.RunOutcomeCancelled)}, o.now()),
			mustEvent(runID, "closed", EventRunClosed, map[string]any{"closed": true}, o.now()),
		}); err != nil {
			return domain.RunRecord{}, err
		}
		return o.GetRun(ctx, workspaceID, runID)
	}
	if !state.cancelRequested {
		if err := o.appendEvents(ctx, workspaceID, runID, version, "cancel:requested", []eventlog.NewEvent{
			mustEvent(runID, "cancel_requested", EventCancelRequested, map[string]any{"launch_key": state.launchIntent.LaunchKey}, o.now()),
		}); err != nil {
			return domain.RunRecord{}, err
		}
		version++
		state.cancelRequested = true
	}
	if !state.cancelAccepted {
		cancelReq := adapter.CancelRequest{WorkspaceID: workspaceID, ConnectionID: state.launchIntent.SelectedOfferConnectionID, OperationKey: "cancel_" + state.launchIntent.AttemptID, LaunchKey: state.launchIntent.LaunchKey}
		hash, err := domain.CanonicalHash(cancelReq)
		if err != nil {
			return domain.RunRecord{}, err
		}
		cancelReq.RequestHash = hash
		if _, err := o.adapter.Cancel(ctx, cancelReq); err != nil {
			return domain.RunRecord{}, err
		}
		if err := o.appendEvents(ctx, workspaceID, runID, version, "cancel:accepted", []eventlog.NewEvent{
			mustEvent(runID, "cancel_accepted", EventCancelAccepted, map[string]any{"launch_key": state.launchIntent.LaunchKey}, o.now()),
		}); err != nil {
			return domain.RunRecord{}, err
		}
		version++
		state.cancelAccepted = true
	}
	if err := o.recordObservation(ctx, workspaceID, runID, version, state, adapter.ExternalObservation{LaunchKey: state.launchIntent.LaunchKey, Phase: adapter.ExternalPhaseCancelled, ObservedAt: o.now().UTC()}); err != nil {
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
		events, err := o.log.ReadStream(ctx, runStream(workspaceID, runID), 0, 10000)
		if err != nil {
			return fmt.Errorf("orchestrator: read run stream: %w", err)
		}
		if len(events) == 0 {
			return ErrRunNotFound
		}
		version := uint64(len(events))
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
				// the AdvanceRun reconcile and Observe backstop still finalize.
				_ = o.finalizeReportedExit(ctx, workspaceID, runID)
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

// finalizeReportedExit makes a reported exit code authoritative: it records the
// run outcome (0 -> succeeded, else -> failed) and requests cleanup, then closes
// the run by releasing/terminating its external resource. It is a no-op when the
// run is already outcome-recorded or closed (the Observe backstop or a prior
// report won the race), or when there is no launch intent / no reported exit
// code to act on.
func (o *Orchestrator) finalizeReportedExit(ctx context.Context, workspaceID, runID string) error {
	events, err := o.GetRunEvents(ctx, workspaceID, runID)
	if err != nil {
		return err
	}
	state, err := reduceRun(events)
	if err != nil {
		return err
	}
	if state.outcomeRecorded || state.closed || state.launchIntent == nil || state.exitCode == nil {
		return nil
	}
	outcome := string(domain.RunOutcomeSucceeded)
	if *state.exitCode != 0 {
		outcome = string(domain.RunOutcomeFailed)
	}
	version := uint64(len(events))
	if err := o.appendEvents(ctx, workspaceID, runID, version, "advance:report-finalize", []eventlog.NewEvent{
		mustEvent(runID, "outcome_recorded", EventRunOutcomeRecorded, map[string]any{"outcome": outcome}, o.now()),
		mustEvent(runID, "cleanup_requested", EventCleanupRequested, map[string]any{"launch_key": state.launchIntent.LaunchKey}, o.now()),
	}); err != nil {
		return err
	}
	return o.releaseAndClose(ctx, workspaceID, runID, version+2, state.launchIntent)
}

func (o *Orchestrator) decide(ctx context.Context, workspaceID string, requested runRequestedData, runID string) (domain.PlacementDecision, attemptData, domain.OfferSnapshot, error) {
	offers, err := o.adapter.ListOffers(ctx, adapter.OfferRequest{WorkspaceID: requested.Workload.WorkspaceID})
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
	attemptID := "att_" + externalIDPart(workspaceID) + "_" + externalIDPart(strings.TrimPrefix(runID, "run_")) + "_" + shortExternalHash(workspaceID, runID)
	attempt := attemptData{
		AttemptID:      attemptID,
		LaunchKey:      "launch_" + attemptID,
		OwnershipToken: "own_" + attemptID,
		CleanupLocator: "cleanup_" + attemptID,
	}
	return decision, attempt, selectedOffer, nil
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
		Args:                      append([]string(nil), container.Args...),
		Environment:               env,
		Ports:                     append([]domain.PortSpec(nil), container.Ports...),
		Resources:                 requested.Workload.Spec.Resources,
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

func (o *Orchestrator) recordObservation(ctx context.Context, workspaceID, runID string, version uint64, state runState, observation adapter.ExternalObservation) error {
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
		return err
	}
	version += uint64(len(toAppend))
	if isTerminal(observation.Phase) {
		return o.releaseAndClose(ctx, workspaceID, runID, version, state.launchIntent)
	}
	return nil
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
}

func reduceRun(events []eventlog.StoredEvent) (runState, error) {
	var state runState
	for _, event := range events {
		switch event.Type {
		case EventRunRequested:
			var data runRequestedData
			payload := event.PrivateData
			if len(payload) == 0 {
				payload = event.Data
			}
			if err := json.Unmarshal(payload, &data); err != nil {
				return runState{}, err
			}
			state.requested = &data
		case EventAttemptCreated:
			var data attemptData
			if err := json.Unmarshal(event.Data, &data); err != nil {
				return runState{}, err
			}
			state.attempt = &data
		case EventLaunchIntentRecorded:
			var data adapter.LaunchRequest
			payload := event.PrivateData
			if len(payload) == 0 {
				payload = event.Data
			}
			if err := json.Unmarshal(payload, &data); err != nil {
				return runState{}, err
			}
			state.launchIntent = &data
		case EventExternalStateObserved:
			var data struct {
				ExitCode *int `json:"exit_code"`
			}
			if err := json.Unmarshal(event.Data, &data); err == nil && data.ExitCode != nil {
				code := *data.ExitCode
				state.exitCode = &code
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
	}
	if state.requested == nil {
		return runState{}, fmt.Errorf("orchestrator: run requested event not found")
	}
	if state.launchIntent == nil && state.attempt != nil {
		return runState{}, fmt.Errorf("orchestrator: attempt exists without launch intent")
	}
	return state, nil
}

func runRecordFromState(workspaceID, runID string, state runState) domain.RunRecord {
	record := domain.RunRecord{
		ID:                 runID,
		WorkspaceID:        workspaceID,
		WorkloadRevisionID: state.requested.Workload.ID,
		Phase:              "requested",
		Cleanup:            domain.CleanupNotRequired,
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

func decodeRunRequested(events []eventlog.StoredEvent) (runRequestedData, error) {
	for _, event := range events {
		if event.Type == EventRunRequested {
			var data runRequestedData
			payload := event.PrivateData
			if len(payload) == 0 {
				payload = event.Data
			}
			if err := json.Unmarshal(payload, &data); err != nil {
				return runRequestedData{}, err
			}
			return data, nil
		}
	}
	return runRequestedData{}, fmt.Errorf("orchestrator: run requested event not found")
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
	scoped := make([]eventlog.NewEvent, len(events))
	copy(scoped, events)
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

func hasEvent(events []eventlog.StoredEvent, eventType string) bool {
	for _, event := range events {
		if event.Type == eventType {
			return true
		}
	}
	return false
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

func publicAdapterError(err error, launchKey string) map[string]any {
	code := "ADAPTER_ERROR"
	retryable := true
	switch {
	case errors.Is(err, adapter.ErrIdempotencyConflict):
		code = "ADAPTER_IDEMPOTENCY_CONFLICT"
		retryable = false
	case errors.Is(err, adapter.ErrLaunchTimeout):
		code = "ADAPTER_LAUNCH_TIMEOUT"
	case errors.Is(err, adapter.ErrLaunchIndeterminate):
		code = "ADAPTER_LAUNCH_INDETERMINATE"
	case errors.Is(err, adapter.ErrRetryableFailure):
		code = "ADAPTER_RETRYABLE_FAILURE"
	}
	return map[string]any{"code": code, "message": "Adapter operation failed.", "retryable": retryable, "launch_key": launchKey}
}

func publicWorkload(rev domain.WorkloadRevision) publicWorkloadRevision {
	out := publicWorkloadRevision{
		ID:          rev.ID,
		WorkspaceID: rev.WorkspaceID,
		WorkloadID:  rev.WorkloadID,
		Digest:      rev.Digest,
		Spec: publicWorkloadSpec{
			Resources: rev.Spec.Resources,
			Network:   rev.Spec.Network,
			Placement: rev.Spec.Placement,
			Execution: rev.Spec.Execution,
			Metadata:  rev.Spec.Metadata,
			Raw:       rev.Spec.Raw,
		},
	}
	out.Spec.Containers = make([]publicContainerSpec, 0, len(rev.Spec.Containers))
	for _, container := range rev.Spec.Containers {
		publicContainer := publicContainerSpec{
			Name:       container.Name,
			Image:      container.Image,
			Platform:   container.Platform,
			Entrypoint: container.Entrypoint,
			Args:       container.Args,
			Ports:      container.Ports,
		}
		if len(container.Env) > 0 {
			publicContainer.Env = make(map[string]publicEnvBinding, len(container.Env))
			for key, binding := range container.Env {
				kind := "empty"
				if binding.Value != nil {
					kind = "literal"
				}
				publicContainer.Env[key] = publicEnvBinding{Kind: kind}
			}
		}
		out.Spec.Containers = append(out.Spec.Containers, publicContainer)
	}
	return out
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

func publicLaunchRequest(req adapter.LaunchRequest) adapter.LaunchRequest {
	public := req
	public.Environment = make([]adapter.EnvironmentBinding, 0, len(req.Environment))
	for _, binding := range req.Environment {
		kind := "empty"
		if binding.Value != nil {
			kind = "literal"
		}
		public.Environment = append(public.Environment, adapter.EnvironmentBinding{Name: binding.Name, Value: &kind})
	}
	return public
}

func cloneStringPtr(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func stringPtr(s string) *string { return &s }
