package orchestrator

import (
	"encoding/json"
	"fmt"

	"github.com/benngarcia/mercator/internal/adapter"
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
		state.placement = &data.Decision

	case EventAttemptCreated:
		var data attemptData
		if err := decodePublicRunPayload(stored, &data); err != nil {
			return err
		}
		if reason := invalidAttempt(data); reason != "" {
			return invalidRunEvent(stored, reason)
		}
		state.attempt = &data
		state.preStart.beginAttempt()

	case EventLaunchIntentRecorded:
		var data adapter.LaunchRequest
		if err := decodeRunPayload(stored, &data); err != nil {
			return err
		}
		if reason := invalidLaunchRequest(data); reason != "" {
			return invalidRunEvent(stored, reason)
		}
		if state.placement != nil && state.requested != nil {
			data.DiagnosticContext.AlternativesExhausted = finalPreStartAttempt(*state.placement, state.preStart.attempts, state.requested.Workload.Spec.Execution.MaxPreStartAttempts)
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
		state.preStart.accept()

	case EventLaunchIndeterminate, EventLaunchFailed:
		var data adapterErrorData
		if err := decodePublicRunPayload(stored, &data); err != nil {
			return err
		}
		if reason := invalidAdapterError(data); reason != "" {
			return invalidRunEvent(stored, reason)
		}
		if stored.Type == EventLaunchIndeterminate {
			state.preStart.markIndeterminate()
		} else {
			var diagnostic *adapter.ProviderFailureDiagnostic
			if len(stored.PrivateData) > 0 {
				var private adapter.ProviderFailureDiagnostic
				if err := json.Unmarshal(stored.PrivateData, &private); err != nil {
					return invalidRunEvent(stored, "private provider failure diagnostic is invalid")
				}
				if state.launchIntent == nil {
					return invalidRunEvent(stored, "private provider failure has no launch intent")
				}
				if reason := invalidProviderFailureDiagnostic(private, *state.launchIntent); reason != "" {
					return invalidRunEvent(stored, reason)
				}
				diagnostic = &private
			}
			if state.launchIntent != nil {
				state.preStart.reject(state.launchIntent.SelectedOfferSnapshotID, diagnostic)
			}
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

	case EventCancelAccepted:
		var data launchReferenceData
		if err := decodePublicRunPayload(stored, &data); err != nil {
			return err
		}
		if data.LaunchKey == "" {
			return invalidRunEvent(stored, "launch_key is required")
		}
		state.cancelAccepted = true

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
		if data.ExitCode != nil && data.Phase.Exited() {
			code := *data.ExitCode
			state.exitCode = &code
		}

	case EventRunReported:
		var data runReportedData
		if err := decodePublicRunPayload(stored, &data); err != nil {
			return err
		}
		if data.Type == "" {
			return invalidRunEvent(stored, "report type is required")
		}
		if data.ExitCode != nil {
			code := *data.ExitCode
			state.exitCode = &code
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
