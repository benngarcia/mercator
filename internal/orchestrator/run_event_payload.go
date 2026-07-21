package orchestrator

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/domain"
)

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

type adapterErrorData struct {
	Code       string                      `json:"code"`
	Message    string                      `json:"message"`
	Retryable  bool                        `json:"retryable"`
	SideEffect adapter.SideEffectCertainty `json:"side_effect,omitempty"`
	LaunchKey  string                      `json:"launch_key"`
}

type cancelRequestedData struct {
	Reason    string `json:"reason,omitempty"`
	LaunchKey string `json:"launch_key,omitempty"`
}

type launchReferenceData struct {
	LaunchKey string `json:"launch_key"`
}

type runOutcomeRecordedData struct {
	Outcome domain.RunOutcome `json:"outcome"`
}

type cleanupConfirmedData struct {
	LaunchKey   string             `json:"launch_key"`
	Disposition domain.Disposition `json:"disposition"`
}

type runClosedData struct {
	Closed bool   `json:"closed"`
	Reason string `json:"reason,omitempty"`
}

type runReportedData struct {
	Type     string          `json:"type"`
	Data     json.RawMessage `json:"data,omitempty"`
	ExitCode *int            `json:"exit_code,omitempty"`
}

func invalidRunRequested(data runRequestedData) string {
	if data.RunID == "" {
		return "run_id is required"
	}
	return ""
}

func invalidPlacement(data placementData) string {
	switch {
	case data.Decision.ID == "":
		return "decision.id is required"
	case data.Decision.RunID == "":
		return "decision.run_id is required"
	case data.Decision.EvaluatedAt.IsZero():
		return "decision.evaluated_at is required"
	case data.Decision.ModelVersion == "":
		return "decision.model_version is required"
	case data.Decision.SelectedOfferSnapshotID == "" && !slices.Contains(data.Decision.SelectionReasonCodes, "NO_FEASIBLE_OFFERS"):
		return "decision.selected_offer_snapshot_id is required"
	default:
		return ""
	}
}

func invalidAttempt(data attemptData) string {
	switch {
	case data.AttemptID == "":
		return "attempt_id is required"
	case data.LaunchKey == "":
		return "launch_key is required"
	case data.OwnershipToken == "":
		return "ownership_token is required"
	case data.CleanupLocator == "":
		return "cleanup_locator is required"
	default:
		return ""
	}
}

func invalidLaunchRequest(data adapter.LaunchRequest) string {
	switch {
	case data.OperationKey == "":
		return "operation_key is required"
	case data.RequestHash == "":
		return "request_hash is required"
	case data.RunID == "":
		return "run_id is required"
	case data.AttemptID == "":
		return "attempt_id is required"
	case data.LaunchKey == "":
		return "launch_key is required"
	case data.OwnershipToken == "":
		return "ownership_token is required"
	case data.CleanupLocator == "":
		return "cleanup_locator is required"
	case data.Image == "":
		return "image is required"
	case data.SelectedOfferSnapshotID == "":
		return "selected_offer_snapshot_id is required"
	case data.SelectedOfferConnectionID == "":
		return "selected_offer_connection_id is required"
	case data.Disposition != "" && !data.Disposition.Valid():
		return fmt.Sprintf("unknown disposition %q", data.Disposition)
	default:
		return ""
	}
}

func invalidLaunchReceipt(data adapter.LaunchReceipt) string {
	switch {
	case data.ExternalID == "":
		return "external_id is required"
	case data.LaunchKey == "":
		return "launch_key is required"
	case data.OwnershipToken == "":
		return "ownership_token is required"
	case data.CleanupLocator == "":
		return "cleanup_locator is required"
	case !data.Phase.Valid():
		return fmt.Sprintf("unknown external phase %q", data.Phase)
	case data.AcceptedAt.IsZero():
		return "accepted_at is required"
	default:
		return ""
	}
}

func invalidAdapterError(data adapterErrorData) string {
	switch {
	case data.Code == "":
		return "code is required"
	case data.Message == "":
		return "message is required"
	case data.LaunchKey == "":
		return "launch_key is required"
	case data.SideEffect != "" && data.SideEffect != adapter.SideEffectNone && data.SideEffect != adapter.SideEffectIndeterminate:
		return fmt.Sprintf("unknown side effect certainty %q", data.SideEffect)
	default:
		return ""
	}
}

func invalidLaunchFailure(data launchFailureData) string {
	if reason := invalidAdapterError(data.publicData()); reason != "" {
		return reason
	}
	if !validProviderFailureKind(data.ProviderKind) {
		return fmt.Sprintf("unknown provider failure kind %q", data.ProviderKind)
	}
	return ""
}

func invalidExternalObservation(data adapter.ExternalObservation) string {
	switch {
	case data.LaunchKey == "":
		return "launch_key is required"
	case !data.Phase.Valid():
		return fmt.Sprintf("unknown external phase %q", data.Phase)
	case data.ObservedAt.IsZero():
		return "observed_at is required"
	default:
		return ""
	}
}
