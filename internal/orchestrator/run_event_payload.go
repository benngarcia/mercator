package orchestrator

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/domain"
)

type runRequestedData struct {
	RunID    string                  `json:"run_id"`
	Workload domain.WorkloadRevision `json:"workload_revision"`
}

type bookingDecisionData struct {
	Decision domain.BookingDecision `json:"decision"`
}

// admissionDeferredData carries one moment admission told a Run to wait, or
// refused to let it wait. Both events carry the same shape because they are the
// same account of the same question, and the reason inside says which answer it
// was.
type admissionDeferredData struct {
	Deferral domain.AdmissionDeferral `json:"deferral"`
}

type bookingDispatchedData struct {
	Booking domain.Booking `json:"booking"`
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

// executionStartedData is the moment somebody observed this workload's process
// begin. It is its own event rather than a field on the observation because it is
// a different fact from a phase: a provider reports running from the moment it
// accepts a launch, so the phase says only that Mercator asked, while this says
// when the container actually started and is written once per attempt.
type executionStartedData struct {
	LaunchKey string    `json:"launch_key"`
	StartedAt time.Time `json:"started_at"`
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

// RunReportReady is the application saying it can do work, and when it could. It
// is the one report type Mercator reads the body of, because application
// readiness is the last stage of a launch and nothing else in the system can
// observe it: a provider, a node, and a container runtime can all see a process
// running, and none of them can see whether it is serving.
const RunReportReady = "ready"

// applicationReadyData is the body of a readiness report. The moment is the
// application's own, because the application is the authority: a moment stamped
// when Mercator appended the event would move with the control plane's polling
// cadence rather than with the workload.
type applicationReadyData struct {
	ReadyAt time.Time `json:"ready_at"`
}

func (report runReportedData) terminal() bool {
	return report.Type == "exit"
}

// readyAt is the moment this report says the application became ready, and
// whether it is one. A report of any other type says nothing about readiness.
func (report runReportedData) readyAt() (time.Time, bool) {
	if report.Type != RunReportReady {
		return time.Time{}, false
	}
	var data applicationReadyData
	if json.Unmarshal(report.Data, &data) != nil || data.ReadyAt.IsZero() {
		return time.Time{}, false
	}
	return data.ReadyAt.UTC(), true
}

// establishedReady is the moment this report establishes the application became
// ready, and whether it establishes one at all. It is the readiness half of
// adapter.ExternalObservation.EstablishedStart, and it exists for the same reason:
// the workload is the authority on the moment, and a moment Mercator cannot defend
// is not a measurement however authoritative its author.
//
// A moment later than the read that carried it is a clock Mercator does not share
// rather than anything the application saw. The application reads its own clock and
// the host's, so a container on a machine running an hour ahead reports a readiness
// an hour in Mercator's future, and filing it would put an hour of invented ready
// latency in the Run Bundle as the workload's own measurement. reportedAt is
// Mercator's own clock on the fact that carried the claim, which is what makes the
// comparison mean anything.
//
// A moment before the container started is an application serving before its
// process existed. That ordering is the one relation between two stages of a launch
// Mercator holds itself to, and the two moments come from different authorities, so
// nothing else would notice: a node states the start and the workload states the
// readiness. Where no start was established there is nothing to order against, and
// the moment is taken on the clock bound alone.
//
// The report itself is kept whatever this answers. What the workload said is a fact
// about the workload; this decides what Mercator adopts as the Run's readiness.
func (report runReportedData) establishedReady(reportedAt time.Time, startedAt *time.Time) (time.Time, bool) {
	moment, stated := report.readyAt()
	switch {
	case !stated || moment.After(reportedAt.UTC()):
		return time.Time{}, false
	case startedAt != nil && moment.Before(*startedAt):
		return time.Time{}, false
	default:
		return moment, true
	}
}

func (report runReportedData) validate() error {
	switch {
	case report.Type == "":
		return fmt.Errorf("%w: type is required", ErrInvalidReport)
	case report.terminal() && report.ExitCode == nil:
		return fmt.Errorf("%w: exit reports require exit_code", ErrInvalidReport)
	case !report.terminal() && report.ExitCode != nil:
		return fmt.Errorf("%w: %s reports cannot include exit_code", ErrInvalidReport, report.Type)
	case report.Type == RunReportReady:
		// A readiness report with no moment in it is the untyped callback this type
		// replaced: it says something happened and leaves the stage it completes
		// with no actual.
		if _, stated := report.readyAt(); !stated {
			return fmt.Errorf("%w: ready reports require data.ready_at", ErrInvalidReport)
		}
		return nil
	default:
		return nil
	}
}

// NewApplicationReadyReport is the workload's own readiness callback, built where
// its one required fact cannot be left out.
func NewApplicationReadyReport(readyAt time.Time) (RunReport, error) {
	data, err := json.Marshal(applicationReadyData{ReadyAt: readyAt.UTC()})
	if err != nil {
		return nil, err
	}
	return NewRunReport(RunReportReady, data, nil)
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

func invalidBookingDecision(data bookingDecisionData) string {
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
	case data.Decision.SelectedOfferSnapshotID != "" && data.Decision.Booking == nil:
		return "decision.booking is required"
	case data.Decision.SelectedOfferSnapshotID == "" && data.Decision.Booking != nil:
		return "decision.booking requires a selected offer"
	case data.Decision.Booking != nil && data.Decision.Booking.ID == "":
		return "decision.booking.id is required"
	case data.Decision.Booking != nil && data.Decision.Booking.RentalID == "":
		return "decision.booking.rental_id is required"
	case data.Decision.Booking != nil && data.Decision.Booking.State != domain.BookingStateRunning && data.Decision.Booking.State != domain.BookingStateQueued:
		return "decision.booking.state is invalid"
	case data.Decision.Booking != nil && data.Decision.Booking.ScheduleVersion == 0:
		return "decision.booking.schedule_version is required"
	default:
		return ""
	}
}

// invalidAdmissionDeferral refuses a deferral that does not say what it is for.
// A wait with no reason and a wait by a class Mercator cannot price are the two
// ways this record stops answering the question it exists to answer.
func invalidAdmissionDeferral(data admissionDeferredData) string {
	switch {
	case data.Deferral.Reason == "":
		return "deferral.reason is required"
	case !data.Deferral.Class.Known():
		return fmt.Sprintf("deferral names service class %q, which Mercator cannot price", data.Deferral.Class)
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

func invalidLaunchFailure(data launchFailureData) string {
	if err := data.publicData().Validate(); err != nil {
		return err.Error()
	}
	if !validProviderFailureKind(data.ProviderKind) {
		return fmt.Sprintf("unknown provider failure kind %q", data.ProviderKind)
	}
	return ""
}
func invalidExecutionStarted(data executionStartedData) string {
	switch {
	case data.LaunchKey == "":
		return "launch_key is required"
	case data.StartedAt.IsZero():
		// A start moment nobody observed is recorded as an absent stage rather than
		// as a zero one, so this event exists only when there is a moment in it.
		return "started_at is required"
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
