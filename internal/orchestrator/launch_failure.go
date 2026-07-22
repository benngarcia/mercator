package orchestrator

import (
	"context"
	"errors"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
)

type launchFailureData struct {
	ProviderKind adapter.ProviderFailureKind `json:"provider_kind,omitempty"`
	Code         string                      `json:"code,omitempty"`
	Message      string                      `json:"message,omitempty"`
	Retryable    bool                        `json:"retryable"`
	SideEffect   adapter.SideEffectCertainty `json:"side_effect,omitempty"`
	LaunchKey    string                      `json:"launch_key"`
}

func launchFailureFrom(err error, launchKey string) launchFailureData {
	var providerFailure *adapter.ProviderFailure
	if errors.As(err, &providerFailure) {
		return launchFailureData{
			ProviderKind: providerFailure.Kind,
			Retryable:    providerFailure.Retryable,
			SideEffect:   providerFailure.SideEffect,
			LaunchKey:    launchKey,
		}
	}

	failure := launchFailureData{
		Code:      "ADAPTER_ERROR",
		Message:   "Adapter operation failed.",
		Retryable: true,
		LaunchKey: launchKey,
	}
	switch {
	case errors.Is(err, adapter.ErrIdempotencyConflict):
		failure.Code = "ADAPTER_IDEMPOTENCY_CONFLICT"
		failure.Retryable = false
	case errors.Is(err, adapter.ErrLaunchTimeout):
		failure.Code = "ADAPTER_LAUNCH_TIMEOUT"
		failure.SideEffect = adapter.SideEffectIndeterminate
	case errors.Is(err, adapter.ErrLaunchIndeterminate):
		failure.Code = "ADAPTER_LAUNCH_INDETERMINATE"
		failure.SideEffect = adapter.SideEffectIndeterminate
	case errors.Is(err, adapter.ErrRetryableFailure):
		failure.Code = "ADAPTER_RETRYABLE_FAILURE"
	case errors.Is(err, adapter.ErrRegistryAuthentication):
		failure.Code = "ADAPTER_REGISTRY_AUTHENTICATION_FAILED"
		failure.Message = "Registry authentication failed."
		failure.Retryable = false
	}
	return failure
}

func (failure launchFailureData) publicData() domain.ProviderError {
	code, message := failure.Code, failure.Message
	if failure.ProviderKind != "" {
		code, message = publicProviderFailure(failure.ProviderKind)
	}
	return domain.ProviderError{
		Code:       code,
		Message:    message,
		Retryable:  failure.Retryable,
		SideEffect: string(failure.SideEffect),
		LaunchKey:  failure.LaunchKey,
	}
}

func (failure launchFailureData) replacementEligible() bool {
	return failure.ProviderKind == adapter.ProviderFailureCapacityUnavailable &&
		failure.Retryable &&
		failure.SideEffect == adapter.SideEffectNone
}

func (failure launchFailureData) indeterminate() bool {
	return failure.SideEffect == adapter.SideEffectIndeterminate
}

func validProviderFailureKind(kind adapter.ProviderFailureKind) bool {
	switch kind {
	case "",
		adapter.ProviderFailureCapacityUnavailable,
		adapter.ProviderFailureInvalidRequest,
		adapter.ProviderFailureAuthentication,
		adapter.ProviderFailureRateLimited,
		adapter.ProviderFailureTransport,
		adapter.ProviderFailureInternal:
		return true
	default:
		return false
	}
}

func (o *Orchestrator) stepLaunch(ctx context.Context, workspaceID, runID string, version uint64, state runState) (bool, error) {
	receipt, err := o.adapter.Launch(ctx, *state.launchIntent)
	if err != nil {
		return o.recordLaunchFailure(ctx, workspaceID, runID, version, state, err)
	}
	return true, o.appendEvents(ctx, workspaceID, runID, version, "advance:launch_accepted:"+state.launchIntent.AttemptID, []eventlog.NewEvent{
		mustEvent(runID, "launch_accepted_"+state.launchIntent.AttemptID, EventLaunchAccepted, receipt, o.now()),
	})
}

func (o *Orchestrator) recordLaunchFailure(ctx context.Context, workspaceID, runID string, version uint64, state runState, launchErr error) (bool, error) {
	failure := launchFailureFrom(launchErr, state.launchIntent.LaunchKey)
	if failure.indeterminate() {
		return false, o.recordIndeterminateLaunch(ctx, workspaceID, runID, version, state, failure, launchErr)
	}
	if failure.replacementEligible() {
		return o.recordReplaceableLaunchFailure(ctx, workspaceID, runID, version, state, failure)
	}
	return false, o.recordTerminalLaunchFailure(ctx, workspaceID, runID, version, state, failure, launchErr)
}

func (o *Orchestrator) recordIndeterminateLaunch(ctx context.Context, workspaceID, runID string, version uint64, state runState, failure launchFailureData, launchErr error) error {
	appendErr := o.appendEvents(ctx, workspaceID, runID, version, "advance:launch_indeterminate:"+state.launchIntent.AttemptID, []eventlog.NewEvent{
		mustPrivateEvent(runID, "launch_indeterminate_"+state.launchIntent.AttemptID, EventLaunchIndeterminate, failure.publicData(), failure, o.now()),
	})
	if appendErr != nil {
		return appendErr
	}
	return launchErr
}

func (o *Orchestrator) recordReplaceableLaunchFailure(ctx context.Context, workspaceID, runID string, version uint64, state runState, failure launchFailureData) (bool, error) {
	exhausted := state.attemptCount >= state.requested.Workload.Spec.Execution.MaxPreStartAttempts
	toAppend := []eventlog.NewEvent{
		mustPrivateEvent(runID, "launch_failed_"+state.launchIntent.AttemptID, EventLaunchFailed, failure.publicData(), failure, o.now()),
	}
	if exhausted {
		toAppend = append(toAppend, retryExhaustedEvents(runID, o.now())...)
	}
	if err := o.appendEvents(ctx, workspaceID, runID, version, "advance:launch_failed:"+state.launchIntent.AttemptID, toAppend); err != nil {
		return false, err
	}
	return !exhausted, nil
}

// A definitive launch rejection closes the run terminally. The provider did
// not create an object, so cleanup is not required and a later poll must never
// repeat the rejected launch.
func (o *Orchestrator) recordTerminalLaunchFailure(ctx context.Context, workspaceID, runID string, version uint64, state runState, failure launchFailureData, launchErr error) error {
	if err := o.appendEvents(ctx, workspaceID, runID, version, "advance:launch_failed:"+state.launchIntent.AttemptID, []eventlog.NewEvent{
		mustPrivateEvent(runID, "launch_failed_"+state.launchIntent.AttemptID, EventLaunchFailed, failure.publicData(), failure, o.now()),
		mustEvent(runID, "outcome_recorded", EventRunOutcomeRecorded, runOutcomeRecordedData{Outcome: domain.RunOutcomeFailed}, o.now()),
		mustEvent(runID, "closed", EventRunClosed, runClosedData{Closed: true}, o.now()),
	}); err != nil {
		return err
	}
	return launchErr
}

func (o *Orchestrator) closeRetryExhausted(ctx context.Context, workspaceID, runID string, version uint64, decision domain.BookingDecision) error {
	events := []eventlog.NewEvent{
		mustEvent(runID, "booking_decided_retry_exhausted", EventBookingDecided, bookingDecisionData{Decision: decision}, o.now()),
	}
	events = append(events, retryExhaustedEvents(runID, o.now())...)
	return o.appendEvents(ctx, workspaceID, runID, version, "advance:retry_exhausted", events)
}

func retryExhaustedEvents(runID string, now time.Time) []eventlog.NewEvent {
	return []eventlog.NewEvent{
		mustEvent(runID, "outcome_recorded", EventRunOutcomeRecorded, runOutcomeRecordedData{Outcome: domain.RunOutcomeFailed}, now),
		mustEvent(runID, "closed", EventRunClosed, runClosedData{Closed: true, Reason: runCloseReasonRetryExhausted}, now),
	}
}
