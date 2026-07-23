package orchestrator

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
)

type runState struct {
	requested                *runRequestedData
	bookingDecision          *domain.BookingDecision
	attempt                  *attemptData
	launchIntent             *adapter.LaunchRequest
	launchAccepted           bool
	launchFailure            *launchFailureData
	attemptCount             int
	excludedOfferSnapshotIDs []string
	cancelRequested          bool
	firstTerminal            *terminalFact
	outcomeRecorded          bool
	outcome                  domain.RunOutcome
	cleanupRequested         bool
	cleanupFailure           *domain.CleanupError
	cleanupConfirmed         bool
	closed                   bool
	exitCode                 *int
	lastObservedPhase        adapter.ExternalPhase
	createdBy                string
	cancelledBy              string
}

type terminalFact struct {
	Outcome domain.RunOutcome
}

func (state runState) externalObjectPossible() bool {
	if state.launchIntent == nil {
		return false
	}
	return state.launchFailure == nil || state.launchFailure.SideEffect != adapter.SideEffectNone
}

func (state runState) replacementEligible() bool {
	return state.launchFailure != nil && state.launchFailure.replacementEligible()
}

func (state runState) bookingQueued() bool {
	return state.bookingDecision != nil && state.bookingDecision.Booking != nil && state.bookingDecision.Booking.State == domain.BookingStateQueued
}

func (state runState) launchIndeterminate() bool {
	return state.launchFailure != nil && state.launchFailure.indeterminate()
}

func applyStoredEvent(state *runState, stored eventlog.StoredEvent) error {
	if stored.SchemaVersion != 1 {
		return invalidRunEvent(stored, "unsupported schema version")
	}
	if err := requireRunEventObject(stored, stored.Data, "public"); err != nil {
		return err
	}

	switch stored.Type {
	case EventRunRequested:
		var data runRequestedData
		if err := decodeRunPayload(stored, &data); err != nil {
			return err
		}
		if reason := invalidRunRequested(data); reason != "" {
			return invalidRunEvent(stored, reason)
		}
		state.requested = &data
		state.createdBy = actorSubject(stored.Actor)

	case EventBookingDecided:
		var data bookingDecisionData
		if err := decodePublicRunPayload(stored, &data); err != nil {
			return err
		}
		if reason := invalidBookingDecision(data); reason != "" {
			return invalidRunEvent(stored, reason)
		}
		state.bookingDecision = &data.Decision

	case EventBookingDispatched:
		var data bookingDispatchedData
		if err := decodePublicRunPayload(stored, &data); err != nil {
			return err
		}
		if state.bookingDecision == nil || state.bookingDecision.Booking == nil {
			return invalidRunEvent(stored, "Booking dispatch requires a Booking Decision")
		}
		if data.Booking.ID != state.bookingDecision.Booking.ID || data.Booking.RunID != state.bookingDecision.RunID || data.Booking.State != domain.BookingStateRunning {
			return invalidRunEvent(stored, "dispatched Booking does not match its Booking Decision")
		}
		state.bookingDecision.Booking = &data.Booking

	case EventAttemptCreated:
		var data attemptData
		if err := decodePublicRunPayload(stored, &data); err != nil {
			return err
		}
		if reason := invalidAttempt(data); reason != "" {
			return invalidRunEvent(stored, reason)
		}
		state.attempt = &data
		state.attemptCount++
		state.launchIntent = nil
		state.launchAccepted = false
		state.launchFailure = nil

	case EventLaunchIntentRecorded:
		var data adapter.LaunchRequest
		if err := decodeRunPayload(stored, &data); err != nil {
			return err
		}
		if reason := invalidLaunchRequest(data); reason != "" {
			return invalidRunEvent(stored, reason)
		}
		state.launchIntent = &data

	case EventLaunchAccepted:
		var data adapter.LaunchReceipt
		if err := decodePublicRunPayload(stored, &data); err != nil {
			return err
		}
		if reason := invalidLaunchReceipt(data); reason != "" {
			return invalidRunEvent(stored, reason)
		}
		state.launchAccepted = true

	case EventLaunchIndeterminate, EventLaunchFailed:
		var data launchFailureData
		if err := decodeRunPayload(stored, &data); err != nil {
			return err
		}
		if reason := invalidLaunchFailure(data); reason != "" {
			return invalidRunEvent(stored, reason)
		}
		state.launchFailure = &data
		if data.replacementEligible() && state.launchIntent != nil && !slices.Contains(state.excludedOfferSnapshotIDs, state.launchIntent.SelectedOfferSnapshotID) {
			state.excludedOfferSnapshotIDs = append(state.excludedOfferSnapshotIDs, state.launchIntent.SelectedOfferSnapshotID)
		}

	case EventCancelRequested:
		var data cancelRequestedData
		if err := decodePublicRunPayload(stored, &data); err != nil {
			return err
		}
		if data.Reason == "" && data.LaunchKey == "" {
			return invalidRunEvent(stored, "reason or launch_key is required")
		}
		state.cancelRequested = true
		state.cancelledBy = actorSubject(stored.Actor)
		if state.firstTerminal == nil {
			state.firstTerminal = &terminalFact{Outcome: domain.RunOutcomeCancelled}
		}

	case EventCancelAccepted:
		var data launchReferenceData
		if err := decodePublicRunPayload(stored, &data); err != nil {
			return err
		}
		if data.LaunchKey == "" {
			return invalidRunEvent(stored, "launch_key is required")
		}

	case EventExternalStateObserved:
		var data adapter.ExternalObservation
		if err := decodePublicRunPayload(stored, &data); err != nil {
			return err
		}
		if reason := invalidExternalObservation(data); reason != "" {
			return invalidRunEvent(stored, reason)
		}
		state.lastObservedPhase = data.Phase
		// Only an exited container's code is authoritative. Docker observes exit
		// code zero on running containers, while workload-reported codes are
		// trusted independently by EventRunReported.
		if data.ExitCode != nil && data.Phase.Exited() && state.firstTerminal == nil {
			code := *data.ExitCode
			state.exitCode = &code
		}
		if isTerminal(data.Phase) && state.firstTerminal == nil {
			state.firstTerminal = &terminalFact{Outcome: outcomeForPhase(data.Phase)}
		}

	case EventRunReported:
		var data runReportedData
		if err := decodePublicRunPayload(stored, &data); err != nil {
			return err
		}
		if err := data.validate(); err != nil {
			return invalidRunEvent(stored, err.Error())
		}
		if data.terminal() && state.firstTerminal == nil {
			code := *data.ExitCode
			state.exitCode = &code
			outcome := domain.RunOutcomeSucceeded
			if code != 0 {
				outcome = domain.RunOutcomeFailed
			}
			state.firstTerminal = &terminalFact{Outcome: outcome}
		}

	case EventRunOutcomeRecorded:
		var data runOutcomeRecordedData
		if err := decodePublicRunPayload(stored, &data); err != nil {
			return err
		}
		if !data.Outcome.Valid() {
			return invalidRunEvent(stored, fmt.Sprintf("unknown run outcome %q", data.Outcome))
		}
		state.outcomeRecorded = true
		state.outcome = data.Outcome

	case EventCleanupRequested:
		var data launchReferenceData
		if err := decodePublicRunPayload(stored, &data); err != nil {
			return err
		}
		if data.LaunchKey == "" {
			return invalidRunEvent(stored, "launch_key is required")
		}
		state.cleanupRequested = true

	case EventCleanupFailed:
		var data domain.CleanupError
		if err := decodePublicRunPayload(stored, &data); err != nil {
			return err
		}
		if err := data.Validate(); err != nil {
			return invalidRunEvent(stored, err.Error())
		}
		state.cleanupFailure = &data

	case EventCleanupConfirmed:
		var data cleanupConfirmedData
		if err := decodePublicRunPayload(stored, &data); err != nil {
			return err
		}
		if data.LaunchKey == "" {
			return invalidRunEvent(stored, "launch_key is required")
		}
		if !data.Disposition.Valid() {
			return invalidRunEvent(stored, fmt.Sprintf("unknown disposition %q", data.Disposition))
		}
		state.cleanupConfirmed = true

	case EventRunClosed:
		var data runClosedData
		if err := decodePublicRunPayload(stored, &data); err != nil {
			return err
		}
		if !data.Closed {
			return invalidRunEvent(stored, "closed must be true")
		}
		state.closed = true

	default:
		return invalidRunEvent(stored, "unknown event type")
	}

	return nil
}

func decodeRunPayload(stored eventlog.StoredEvent, target any) error {
	payload := stored.PrivateData
	payloadName := "private"
	if len(payload) == 0 {
		payload = stored.Data
		payloadName = "public"
	}
	if err := requireRunEventObject(stored, payload, payloadName); err != nil {
		return err
	}
	if err := json.Unmarshal(payload, target); err != nil {
		return invalidRunEvent(stored, err.Error())
	}
	return nil
}

func decodePublicRunPayload(stored eventlog.StoredEvent, target any) error {
	if err := requireRunEventObject(stored, stored.Data, "public"); err != nil {
		return err
	}
	if err := json.Unmarshal(stored.Data, target); err != nil {
		return invalidRunEvent(stored, err.Error())
	}
	return nil
}

func invalidRunEvent(stored eventlog.StoredEvent, reason string) error {
	return fmt.Errorf("orchestrator: invalid run event id=%q type=%q schema=%d: %s", stored.ID, stored.Type, stored.SchemaVersion, reason)
}

func requireRunEventObject(stored eventlog.StoredEvent, payload json.RawMessage, name string) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(payload, &object); err != nil || object == nil {
		return invalidRunEvent(stored, name+" payload must be a JSON object")
	}
	return nil
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
	for _, stored := range events {
		if err := applyStoredEvent(&state, stored); err != nil {
			return runState{}, err
		}
	}
	if err := state.validate(); err != nil {
		return runState{}, err
	}
	return state, nil
}

func (state runState) validate() error {
	if state.requested == nil {
		return fmt.Errorf("orchestrator: run requested event not found")
	}
	if state.launchIntent == nil && state.attempt != nil {
		return fmt.Errorf("orchestrator: attempt exists without launch intent")
	}
	if state.closed && !state.outcomeRecorded {
		return fmt.Errorf("orchestrator: run closed without a recorded outcome")
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
	if state.launchAccepted || state.launchIndeterminate() {
		record.Phase = "running"
		record.Cleanup = domain.CleanupPending
	}
	if state.cleanupRequested {
		record.Phase = "cleaning_up"
		record.Cleanup = domain.CleanupPending
	}
	if state.cleanupFailure != nil && !state.cleanupConfirmed {
		record.Cleanup = domain.CleanupBlocked
		failure := *state.cleanupFailure
		record.CleanupError = &failure
	}
	if state.cleanupConfirmed {
		record.Cleanup = domain.CleanupConfirmed
	}
	if state.exitCode != nil {
		code := *state.exitCode
		record.ExitCode = &code
	}
	if state.outcomeRecorded {
		record.Outcome = state.outcome
	}
	if state.closed {
		record.Phase = "closed"
		record.Closed = true
	}
	return record
}
