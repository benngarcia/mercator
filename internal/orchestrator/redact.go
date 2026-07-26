// Redaction for the public event stream. Run events are stored twice: the
// private payload carries the full data, the public payload replaces every
// environment value with its kind ("literal" or "empty") so secrets never reach
// public readers. What a public reader may see of a workload revision is
// domain.WorkloadRevision.Public, which is shared with the door that stores a
// revision; the rest here is this package's own.
package orchestrator

import (
	"errors"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/domain"
)

type publicRunRequestedData struct {
	RunID    string                        `json:"run_id"`
	Workload domain.PublicWorkloadRevision `json:"workload_revision"`
}

// publicLaunchRequest redacts a launch request for the public payload of the
// launch_intent_recorded event. The wire shape stays adapter.LaunchRequest;
// each environment binding's value slot carries the binding's kind instead of
// its value, mirroring the {kind} redaction a public revision carries.
func publicLaunchRequest(req adapter.LaunchRequest) adapter.LaunchRequest {
	public := req
	public.Environment = make([]adapter.EnvironmentBinding, 0, len(req.Environment))
	for _, binding := range req.Environment {
		kind := domain.EnvKind(binding.Value)
		public.Environment = append(public.Environment, adapter.EnvironmentBinding{Name: binding.Name, Value: &kind})
	}
	return public
}

// publicAdapterError maps an adapter failure to a stable public error payload;
// the raw error text never reaches the public stream.
func publicAdapterError(err error, launchKey string) domain.ProviderError {
	var providerFailure *adapter.ProviderFailure
	if errors.As(err, &providerFailure) {
		code, message := publicProviderFailure(providerFailure.Kind)
		return domain.ProviderError{Code: code, Message: message, Retryable: providerFailure.Retryable, SideEffect: string(providerFailure.SideEffect), LaunchKey: launchKey}
	}
	code := "ADAPTER_ERROR"
	message := "Adapter operation failed."
	retryable := true
	sideEffect := ""
	switch {
	case errors.Is(err, adapter.ErrIdempotencyConflict):
		code = "ADAPTER_IDEMPOTENCY_CONFLICT"
		retryable = false
	case errors.Is(err, adapter.ErrLaunchTimeout):
		code = "ADAPTER_LAUNCH_TIMEOUT"
		sideEffect = string(adapter.SideEffectIndeterminate)
	case errors.Is(err, adapter.ErrLaunchIndeterminate):
		code = "ADAPTER_LAUNCH_INDETERMINATE"
		sideEffect = string(adapter.SideEffectIndeterminate)
	case errors.Is(err, adapter.ErrRetryableFailure):
		code = "ADAPTER_RETRYABLE_FAILURE"
	case errors.Is(err, adapter.ErrRegistryAuthentication):
		code = "ADAPTER_REGISTRY_AUTHENTICATION_FAILED"
		message = "Registry authentication failed."
		retryable = false
	}
	return domain.ProviderError{Code: code, Message: message, Retryable: retryable, SideEffect: sideEffect, LaunchKey: launchKey}
}
func publicProviderFailure(kind adapter.ProviderFailureKind) (string, string) {
	switch kind {
	case adapter.ProviderFailureCapacityUnavailable:
		return "PROVIDER_CAPACITY_UNAVAILABLE", "Provider capacity is unavailable."
	case adapter.ProviderFailureInvalidRequest:
		return "PROVIDER_INVALID_REQUEST", "Provider rejected the launch request."
	case adapter.ProviderFailureAuthentication:
		return "PROVIDER_AUTHENTICATION_FAILED", "Provider authentication failed."
	case adapter.ProviderFailureRateLimited:
		return "PROVIDER_RATE_LIMITED", "Provider rate limit was exhausted."
	case adapter.ProviderFailureTransport:
		return "PROVIDER_TRANSPORT_FAILURE", "Provider transport failed."
	default:
		return "PROVIDER_INTERNAL_ERROR", "Provider operation failed."
	}
}

func publicCleanupError(err error, launchKey string, disposition domain.Disposition) domain.CleanupError {
	adapterError := publicAdapterError(err, launchKey)
	return domain.CleanupError{
		ProviderError: adapterError,
		Disposition:   disposition,
	}
}
