package orchestrator

import (
	"encoding/json"
	"fmt"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
)

type decodedRunEvent interface {
	apply(*runState)
}

type decodedRunRequested struct {
	data      runRequestedData
	createdBy string
}

func (event decodedRunRequested) apply(state *runState) {
	state.requested = &event.data
	state.createdBy = event.createdBy
}

type decodedAttemptCreated struct{ data attemptData }

func (event decodedAttemptCreated) apply(state *runState) { state.attempt = &event.data }

type decodedLaunchIntent struct{ data adapter.LaunchRequest }

func (event decodedLaunchIntent) apply(state *runState) { state.launchIntent = &event.data }

type decodedExternalObservation struct{ data adapter.ExternalObservation }

func (event decodedExternalObservation) apply(state *runState) {
	state.lastObservedPhase = event.data.Phase
	// Only an exited container's code is authoritative. Docker observes exit
	// code zero on running containers, while workload-reported codes are
	// trusted independently by decodedRunReport.
	if event.data.ExitCode != nil && event.data.Phase.Exited() {
		code := *event.data.ExitCode
		state.exitCode = &code
	}
}

type decodedRunReport struct{ data runReportedData }

func (event decodedRunReport) apply(state *runState) {
	if event.data.ExitCode != nil {
		code := *event.data.ExitCode
		state.exitCode = &code
	}
}

type decodedRunOutcome struct{ outcome domain.RunOutcome }

func (event decodedRunOutcome) apply(state *runState) {
	state.outcomeRecorded = true
	state.outcome = event.outcome
}

type runSignal uint8

const (
	signalNoop runSignal = iota
	signalLaunchAccepted
	signalLaunchIndeterminate
	signalCancelRequested
	signalCancelAccepted
	signalCleanupRequested
	signalCleanupConfirmed
	signalRunClosed
)

type decodedRunSignal struct {
	signal runSignal
	actor  string
}

func (event decodedRunSignal) apply(state *runState) {
	switch event.signal {
	case signalNoop:
	case signalLaunchAccepted:
		state.launchAccepted = true
	case signalLaunchIndeterminate:
		state.launchIndeterminate = true
	case signalCancelRequested:
		state.cancelRequested = true
		state.cancelledBy = event.actor
	case signalCancelAccepted:
		state.cancelAccepted = true
	case signalCleanupRequested:
		state.cleanupRequested = true
	case signalCleanupConfirmed:
		state.cleanupConfirmed = true
	case signalRunClosed:
		state.closed = true
	}
}

func decodeRunEvent(stored eventlog.StoredEvent) (decodedRunEvent, error) {
	if stored.SchemaVersion != 1 {
		return nil, invalidRunEvent(stored, "unsupported schema version")
	}
	if !json.Valid(stored.Data) {
		return nil, invalidRunEvent(stored, "malformed public JSON")
	}

	switch stored.Type {
	case EventRunRequested:
		var data runRequestedData
		if err := decodeRunPayload(stored, &data); err != nil {
			return nil, err
		}
		return decodedRunRequested{data: data, createdBy: actorSubject(stored.Actor)}, nil
	case EventPlacementDecided, EventLaunchFailed:
		return decodedRunSignal{signal: signalNoop}, nil
	case EventAttemptCreated:
		var data attemptData
		if err := decodePublicRunPayload(stored, &data); err != nil {
			return nil, err
		}
		return decodedAttemptCreated{data: data}, nil
	case EventLaunchIntentRecorded:
		var data adapter.LaunchRequest
		if err := decodeRunPayload(stored, &data); err != nil {
			return nil, err
		}
		return decodedLaunchIntent{data: data}, nil
	case EventExternalStateObserved:
		var data adapter.ExternalObservation
		if err := decodePublicRunPayload(stored, &data); err != nil {
			return nil, err
		}
		if !knownExternalPhase(data.Phase) {
			return nil, invalidRunEvent(stored, fmt.Sprintf("unknown external phase %q", data.Phase))
		}
		return decodedExternalObservation{data: data}, nil
	case EventRunReported:
		var data runReportedData
		if err := decodePublicRunPayload(stored, &data); err != nil {
			return nil, err
		}
		if data.Type == "" {
			return nil, invalidRunEvent(stored, "report type is required")
		}
		return decodedRunReport{data: data}, nil
	case EventRunOutcomeRecorded:
		var data struct {
			Outcome domain.RunOutcome `json:"outcome"`
		}
		if err := decodePublicRunPayload(stored, &data); err != nil {
			return nil, err
		}
		if !knownRunOutcome(data.Outcome) {
			return nil, invalidRunEvent(stored, fmt.Sprintf("unknown run outcome %q", data.Outcome))
		}
		return decodedRunOutcome{outcome: data.Outcome}, nil
	case EventLaunchAccepted:
		return decodedRunSignal{signal: signalLaunchAccepted}, nil
	case EventLaunchIndeterminate:
		return decodedRunSignal{signal: signalLaunchIndeterminate}, nil
	case EventCancelRequested:
		return decodedRunSignal{signal: signalCancelRequested, actor: actorSubject(stored.Actor)}, nil
	case EventCancelAccepted:
		return decodedRunSignal{signal: signalCancelAccepted}, nil
	case EventCleanupRequested:
		return decodedRunSignal{signal: signalCleanupRequested}, nil
	case EventCleanupConfirmed:
		return decodedRunSignal{signal: signalCleanupConfirmed}, nil
	case EventRunClosed:
		return decodedRunSignal{signal: signalRunClosed}, nil
	default:
		return nil, invalidRunEvent(stored, "unknown event type")
	}
}

func decodeRunPayload(stored eventlog.StoredEvent, target any) error {
	payload := stored.PrivateData
	if len(payload) == 0 {
		payload = stored.Data
	}
	if err := json.Unmarshal(payload, target); err != nil {
		return invalidRunEvent(stored, err.Error())
	}
	return nil
}

func decodePublicRunPayload(stored eventlog.StoredEvent, target any) error {
	if err := json.Unmarshal(stored.Data, target); err != nil {
		return invalidRunEvent(stored, err.Error())
	}
	return nil
}

func invalidRunEvent(stored eventlog.StoredEvent, reason string) error {
	return fmt.Errorf("orchestrator: invalid run event id=%q type=%q schema=%d: %s", stored.ID, stored.Type, stored.SchemaVersion, reason)
}

func knownExternalPhase(phase adapter.ExternalPhase) bool {
	switch phase {
	case adapter.ExternalPhaseQueued,
		adapter.ExternalPhaseRunning,
		adapter.ExternalPhaseSucceeded,
		adapter.ExternalPhaseFailed,
		adapter.ExternalPhaseCancelled,
		adapter.ExternalPhaseReleased:
		return true
	default:
		return false
	}
}

func knownRunOutcome(outcome domain.RunOutcome) bool {
	switch outcome {
	case domain.RunOutcomeSucceeded, domain.RunOutcomeFailed, domain.RunOutcomeCancelled:
		return true
	default:
		return false
	}
}
