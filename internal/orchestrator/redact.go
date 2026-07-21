// Redaction for the public event stream. Run events are stored twice: the
// private payload carries the full data, the public payload replaces every
// environment value with its kind ("literal" or "empty") so secrets never
// reach public readers. The types here mirror the domain types on the wire —
// changing a field changes the public event schema.
package orchestrator

import (
	"encoding/json"
	"errors"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/domain"
)

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
				publicContainer.Env[key] = publicEnvBinding{Kind: envKind(binding.Value)}
			}
		}
		out.Spec.Containers = append(out.Spec.Containers, publicContainer)
	}
	return out
}

// publicLaunchRequest redacts a launch request for the public payload of the
// launch_intent_recorded event. The wire shape stays adapter.LaunchRequest;
// each environment binding's value slot carries the binding's kind instead of
// its value, mirroring the {kind} redaction publicWorkload applies.
func publicLaunchRequest(req adapter.LaunchRequest) adapter.LaunchRequest {
	public := req
	public.Environment = make([]adapter.EnvironmentBinding, 0, len(req.Environment))
	for _, binding := range req.Environment {
		kind := envKind(binding.Value)
		public.Environment = append(public.Environment, adapter.EnvironmentBinding{Name: binding.Name, Value: &kind})
	}
	return public
}

func envKind(value *string) string {
	if value != nil {
		return "literal"
	}
	return "empty"
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
