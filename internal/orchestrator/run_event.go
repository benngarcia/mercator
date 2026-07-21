package orchestrator

import (
	"encoding/json"
	"fmt"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
)

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

	case EventPlacementDecided:
		var data placementData
		if err := decodePublicRunPayload(stored, &data); err != nil {
			return err
		}
		if reason := invalidPlacement(data); reason != "" {
			return invalidRunEvent(stored, reason)
		}

	case EventAttemptCreated:
		var data attemptData
		if err := decodePublicRunPayload(stored, &data); err != nil {
			return err
		}
		if reason := invalidAttempt(data); reason != "" {
			return invalidRunEvent(stored, reason)
		}
		state.attempt = &data

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
		var data domain.ProviderError
		if err := decodePublicRunPayload(stored, &data); err != nil {
			return err
		}
		if err := data.Validate(); err != nil {
			return invalidRunEvent(stored, err.Error())
		}
		if stored.Type == EventLaunchIndeterminate {
			state.launchIndeterminate = true
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
