package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/bengarcia/mercator/internal/adapter"
	"github.com/bengarcia/mercator/internal/domain"
	"github.com/bengarcia/mercator/internal/eventlog"
	"github.com/bengarcia/mercator/internal/scheduler"
)

const (
	EventRunRequested          = "compute.run.requested.v1"
	EventPlacementDecided      = "compute.run.placement_decided.v1"
	EventAttemptCreated        = "compute.run.attempt_created.v1"
	EventLaunchIntentRecorded  = "compute.run.launch_intent_recorded.v1"
	EventLaunchAccepted        = "compute.run.launch_accepted.v1"
	EventLaunchFailed          = "compute.run.launch_failed.v1"
	EventExternalStateObserved = "compute.run.external_state_observed.v1"
	EventRunOutcomeRecorded    = "compute.run.outcome_recorded.v1"
	EventCleanupRequested      = "compute.run.cleanup_requested.v1"
	EventCleanupConfirmed      = "compute.run.cleanup_confirmed.v1"
	EventRunClosed             = "compute.run.closed.v1"
)

type Orchestrator struct {
	log       eventlog.EventLog
	scheduler scheduler.Scheduler
	adapter   adapter.Adapter
	now       func() time.Time
}

func New(log eventlog.EventLog, scheduler scheduler.Scheduler, adapter adapter.Adapter) *Orchestrator {
	return &Orchestrator{log: log, scheduler: scheduler, adapter: adapter, now: time.Now}
}

type CreateRunRequest struct {
	WorkspaceID    string
	RunID          string
	CommandKey     string
	IdempotencyKey string
	Actor          json.RawMessage
	Workload       domain.WorkloadRevision
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
	if violations := domain.ValidateWorkloadRevision(req.Workload); len(violations) > 0 {
		return CreateRunResult{}, fmt.Errorf("%s: %s", violations[0].Code, violations[0].Message)
	}
	requestHash, err := domain.CanonicalHash(struct {
		RunID    string                  `json:"run_id"`
		Workload domain.WorkloadRevision `json:"workload"`
	}{req.RunID, req.Workload})
	if err != nil {
		return CreateRunResult{}, err
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
		return CreateRunResult{}, err
	}
	return CreateRunResult{RunID: req.RunID, Duplicate: result.Duplicate}, nil
}

func (o *Orchestrator) AdvanceRun(ctx context.Context, workspaceID, runID string) error {
	events, err := o.GetRunEvents(ctx, workspaceID, runID)
	if err != nil {
		return err
	}
	if hasEvent(events, EventRunClosed) {
		return nil
	}
	requested, err := decodeRunRequested(events)
	if err != nil {
		return err
	}
	version := uint64(len(events))
	decision, attempt, selectedOffer, err := o.decide(ctx, requested, runID)
	if err != nil {
		return err
	}
	container := requested.Workload.Spec.Containers[0]
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
		Environment:               launchEnvironment(container.Env),
		Ports:                     append([]domain.PortSpec(nil), container.Ports...),
		Resources:                 requested.Workload.Spec.Resources,
		SelectedOfferSnapshotID:   selectedOffer.ID,
		SelectedOfferConnectionID: selectedOffer.ConnectionID,
		SelectedOfferAdapterType:  selectedOffer.AdapterType,
		SelectedOfferNativeRef:    selectedOffer.NativeRef,
	}
	launchReq.RequestHash, err = domain.CanonicalHash(launchReq)
	if err != nil {
		return err
	}
	if !hasEvent(events, EventLaunchIntentRecorded) {
		if err := o.appendEvents(ctx, workspaceID, runID, version, "advance:placement", []eventlog.NewEvent{
			mustEvent(runID, "placement_decided", EventPlacementDecided, placementData{Decision: decision}, o.now()),
			mustEvent(runID, "attempt_created", EventAttemptCreated, attempt, o.now()),
			mustEvent(runID, "launch_intent_recorded", EventLaunchIntentRecorded, launchReq, o.now()),
		}); err != nil {
			return err
		}
		version += 3
	}
	receipt, err := o.adapter.Launch(ctx, launchReq)
	if err != nil {
		_ = o.appendEvents(ctx, workspaceID, runID, version, "advance:launch_failed", []eventlog.NewEvent{
			mustEvent(runID, "launch_failed", EventLaunchFailed, map[string]any{"error": err.Error(), "retryable": !errors.Is(err, adapter.ErrIdempotencyConflict)}, o.now()),
		})
		return err
	}
	if err := o.appendEvents(ctx, workspaceID, runID, version, "advance:launch_accepted", []eventlog.NewEvent{
		mustEvent(runID, "launch_accepted", EventLaunchAccepted, receipt, o.now()),
	}); err != nil {
		return err
	}
	version++

	observation, err := o.adapter.Observe(ctx, adapter.ObserveRequest{LaunchKey: attempt.LaunchKey})
	if err != nil {
		return err
	}
	toAppend := []eventlog.NewEvent{
		mustEvent(runID, "external_state_observed", EventExternalStateObserved, observation, o.now()),
	}
	if isTerminal(observation.Phase) {
		toAppend = append(toAppend,
			mustEvent(runID, "outcome_recorded", EventRunOutcomeRecorded, map[string]any{"outcome": outcomeForPhase(observation.Phase)}, o.now()),
			mustEvent(runID, "cleanup_requested", EventCleanupRequested, map[string]any{"launch_key": attempt.LaunchKey}, o.now()),
		)
	}
	if err := o.appendEvents(ctx, workspaceID, runID, version, "advance:observe", toAppend); err != nil {
		return err
	}
	version += uint64(len(toAppend))

	if isTerminal(observation.Phase) {
		releaseReq := adapter.ReleaseRequest{OperationKey: "release_" + attempt.AttemptID, LaunchKey: attempt.LaunchKey}
		releaseReq.RequestHash, err = domain.CanonicalHash(releaseReq)
		if err != nil {
			return err
		}
		if _, err := o.adapter.Release(ctx, releaseReq); err != nil {
			return err
		}
		if err := o.appendEvents(ctx, workspaceID, runID, version, "advance:cleanup", []eventlog.NewEvent{
			mustEvent(runID, "cleanup_confirmed", EventCleanupConfirmed, map[string]any{"launch_key": attempt.LaunchKey}, o.now()),
			mustEvent(runID, "closed", EventRunClosed, map[string]any{"closed": true}, o.now()),
		}); err != nil {
			return err
		}
	}
	return nil
}

func (o *Orchestrator) GetRunEvents(ctx context.Context, workspaceID, runID string) ([]eventlog.StoredEvent, error) {
	return o.log.ReadStream(ctx, runStream(workspaceID, runID), 0, 1000)
}

func (o *Orchestrator) decide(ctx context.Context, requested runRequestedData, runID string) (domain.PlacementDecision, attemptData, domain.OfferSnapshot, error) {
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
	attemptID := "att_" + strings.TrimPrefix(runID, "run_")
	attempt := attemptData{
		AttemptID:      attemptID,
		LaunchKey:      "launch_" + attemptID,
		OwnershipToken: "own_" + attemptID,
		CleanupLocator: "cleanup_" + attemptID,
	}
	return decision, attempt, selectedOffer, nil
}

func (o *Orchestrator) appendEvents(ctx context.Context, workspaceID, runID string, expectedVersion uint64, commandKey string, events []eventlog.NewEvent) error {
	requestHash, err := domain.CanonicalHash(events)
	if err != nil {
		return err
	}
	_, err = o.log.Append(ctx, eventlog.AppendRequest{
		Stream:                runStream(workspaceID, runID),
		ExpectedStreamVersion: expectedVersion,
		CommandKey:            commandKey,
		RequestHash:           requestHash,
		CorrelationID:         runID,
		CausationID:           commandKey,
		Events:                events,
	})
	return err
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
		ID:            eventID(runID, suffix),
		Type:          eventType,
		SchemaVersion: 1,
		OccurredAt:    now.UTC(),
		Visibility:    eventlog.VisibilityPublic,
		Data:          encoded,
	}
}

func eventID(runID, suffix string) string {
	return "evt_" + runID + "_" + suffix
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
	return phase == adapter.ExternalPhaseSucceeded || phase == adapter.ExternalPhaseFailed || phase == adapter.ExternalPhaseCancelled
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
				if binding.SecretRef != nil {
					kind = "secret"
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
			Name:      name,
			Value:     cloneStringPtr(binding.Value),
			SecretRef: cloneSecretRef(binding.SecretRef),
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

func cloneSecretRef(ref *domain.SecretReference) *domain.SecretReference {
	if ref == nil {
		return nil
	}
	cloned := *ref
	return &cloned
}
