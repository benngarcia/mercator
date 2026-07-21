package orchestrator

import (
	"encoding/json"
	"fmt"

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
	Closed bool `json:"closed"`
}

type runReportedData struct {
	Type     string          `json:"type"`
	Data     json.RawMessage `json:"data,omitempty"`
	ExitCode *int            `json:"exit_code,omitempty"`
}

func (report runReportedData) terminal() bool {
	return report.Type == "exit"
}

func (report runReportedData) validate() error {
	switch {
	case report.Type == "":
		return fmt.Errorf("%w: type is required", ErrInvalidReport)
	case report.terminal() && report.ExitCode == nil:
		return fmt.Errorf("%w: exit reports require exit_code", ErrInvalidReport)
	case !report.terminal() && report.ExitCode != nil:
		return fmt.Errorf("%w: %s reports cannot include exit_code", ErrInvalidReport, report.Type)
	default:
		return nil
	}
}

type RunReport interface {
	payload() runReportedData
}

type nonterminalRunReport struct {
	typeName string
	data     json.RawMessage
}

func (report nonterminalRunReport) payload() runReportedData {
	return runReportedData{Type: report.typeName, Data: report.data}
}

type terminalRunReport struct {
	data     json.RawMessage
	exitCode int
}

func (report terminalRunReport) payload() runReportedData {
	return runReportedData{Type: "exit", Data: report.data, ExitCode: &report.exitCode}
}

func NewRunReport(reportType string, data json.RawMessage, exitCode *int) (RunReport, error) {
	payload := runReportedData{Type: reportType, Data: data, ExitCode: exitCode}
	if err := payload.validate(); err != nil {
		return nil, err
	}
	if payload.terminal() {
		return terminalRunReport{data: data, exitCode: *exitCode}, nil
	}
	return nonterminalRunReport{typeName: reportType, data: data}, nil
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
	case data.Decision.SelectedOfferSnapshotID == "":
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
	case !data.Disposition.Valid():
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
