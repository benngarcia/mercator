package orchestrator

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
)

type runState struct {
	requested        *runRequestedData
	bookingDecision  *domain.BookingDecision
	attempt          *attemptData
	launchIntent     *adapter.LaunchRequest
	launchAccepted   bool
	launchAcceptedAt time.Time
	// startedAt is when this attempt's workload actually began, as the machine
	// holding it reported the moment. It is nil until something observed one, and
	// it is never filled in from launchAcceptedAt: the gap between the two is the
	// start latency every prediction in phase 4 is calibrated against, and a
	// derived value would make that subtraction zero for every Run in the log.
	startedAt *time.Time
	// readyAt is when this attempt's application reported that it can do work, as
	// the application stated the moment. It is nil until a report arrives and is
	// never derived from startedAt: a running process is not a ready one, and only
	// the workload can tell the difference.
	readyAt       *time.Time
	launchFailure *launchFailureData
	// deferral is why admission last told this Run to wait, deferredAt is when it
	// said so, and queuedSince is when it first did. The three come apart on
	// purpose: the reason is replaced every time the answer changes, the moment
	// the wait started is what the class's bounds are measured from and must never
	// move, and the moment of the latest answer is where the interval that answer
	// covers begins.
	deferral    *domain.AdmissionDeferral
	deferredAt  time.Time
	queuedSince time.Time
	// selfImposedWait is how much of this Run's wait the caller's own declaration
	// held, over the intervals that are closed. The interval still open is added
	// where the wait is read, because how long it has run is a question about now.
	selfImposedWait time.Duration
	deferralCount   int
	attemptCount    int
	excluded        []domain.OfferExclusion
	// capacity is the machine this attempt promised to allocate, and the four
	// fields under it are how far it got: what the provider accepted, which
	// provisioning stages have an actual, when the last of them landed, and
	// whether an agent ever opened a session. A Run placed on capacity that
	// already exists carries none of it.
	capacity            *capacityRequestedData
	capacityAccepted    *capacityAcceptedData
	capacityStages      map[domain.LaunchStage]bool
	lastCapacityStageAt time.Time
	nodeEnrolled        bool
	// capacityReclaimed is Mercator having given this attempt's machine back
	// because its agent never came. It makes the Run replaceable, exactly as a
	// launch the provider refused does, and for the opposite reason: this one
	// leaves a machine that existed and was billed for.
	capacityReclaimed *capacityReclaimedData
	cancelRequested   bool
	firstTerminal     *terminalFact
	outcomeRecorded   bool
	outcome           domain.RunOutcome
	cleanupRequested  bool
	cleanupFailure    *domain.CleanupError
	cleanupConfirmed  bool
	closed            bool
	exitCode          *int
	lastObservedPhase adapter.ExternalPhase
	createdBy         string
	cancelledBy       string
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
	if state.capacityReclaimed != nil {
		return true
	}
	return state.launchFailure != nil && state.launchFailure.replacementEligible()
}

// exclude records what an attempt proved about the offer it was placed on, so
// the evaluation that stands in for it can refuse that offer with the reason
// rather than with a code covering both. An offer already struck out keeps the
// reason it was struck out with: the first proof is the one the record explains.
func (state *runState) exclude(offerSnapshotID string, reason domain.OfferExclusionReason) {
	if offerSnapshotID == "" {
		return
	}
	if _, struck := domain.ExcludedOffer(state.excluded, offerSnapshotID); struck {
		return
	}
	state.excluded = append(state.excluded, domain.OfferExclusion{OfferSnapshotID: offerSnapshotID, Reason: reason})
}

// wait is how long this Run has been waiting at one moment, split into the whole
// of it and the part the caller's own declaration held, which is what each of the
// two bounds its class states is asked of.
//
// A Run nothing has deferred yet has waited nothing, which is not the same as a
// wait of zero seconds that has begun: the class bounds are measured from the
// first deferral, and there is no such moment yet.
func (state runState) wait(at time.Time) domain.Wait {
	if state.queuedSince.IsZero() {
		return domain.Wait{}
	}
	return domain.Wait{
		Seconds:            at.Sub(state.queuedSince).Seconds(),
		SelfImposedSeconds: (state.selfImposedWait + state.selfImposedSince(at)).Seconds(),
	}
}

// selfImposedSince is how much of the interval between admission's latest answer
// and one later moment the caller's own declaration held. An answer stands until
// the next one replaces it, so the reason recorded at the start of the interval is
// what held the Run through it.
//
// A Run with no answer standing contributes nothing, which is the reading for the
// two ways that happens. A Run nobody has deferred has no wait to divide. A Run
// whose latest fact is a placement is a Run whose wait Mercator ended, and the
// interval it spent holding a machine is charged to nobody's queue: what the
// placement says about the family is that this Run is in the count rather than
// waiting on it.
func (state runState) selfImposedSince(at time.Time) time.Duration {
	if state.deferral == nil || state.deferredAt.IsZero() || !state.deferral.SelfImposed() {
		return 0
	}
	return at.Sub(state.deferredAt)
}

// supersession is the decision a fresh evaluation of this Run stands in for, and
// why. A Run nothing has decided about yet supersedes nothing, which is what
// leaves the first decision on every Run naming nothing.
//
// The reason is read off the Run's own record rather than passed down from the
// caller, because the record is what an operator will check it against: the
// machine the last decision chose refused to start the work, or the last decision
// placed the Run nowhere and the fleet is being asked again. Those are the only
// two ways Mercator decides twice about one Run, and both are facts already in
// the stream.
func (state runState) supersession() (string, string) {
	if state.bookingDecision == nil {
		return "", ""
	}
	if state.capacityReclaimed != nil {
		return state.bookingDecision.ID, domain.SupersededCapacityReclaimed
	}
	if state.replacementEligible() {
		return state.bookingDecision.ID, domain.SupersededLaunchFailed
	}
	return state.bookingDecision.ID, domain.SupersededSelectedNothing
}

// placed reports whether the last decision on this Run chose a machine. A
// decision that chose nothing is still a decision, and it is recorded, so
// "decided" and "placed" are separate questions now.
func (state runState) placed() bool {
	return state.bookingDecision != nil && state.bookingDecision.SelectedOfferSnapshotID != ""
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
		if data.Decision.SelectedOfferSnapshotID != "" {
			// Placed work is not waiting on a decision, whatever it is still
			// waiting for on the machine. How long it waited stays recorded, and so
			// does what held it: the interval the placement ends is closed here
			// rather than at the next deferral, because there is no answer standing
			// after this to attribute it to.
			state.selfImposedWait += state.selfImposedSince(stored.OccurredAt.UTC())
			state.deferral = nil
		}

	case EventAdmissionDeferred:
		var data admissionDeferredData
		if err := decodePublicRunPayload(stored, &data); err != nil {
			return err
		}
		if reason := invalidAdmissionDeferral(data); reason != "" {
			return invalidRunEvent(stored, reason)
		}
		state.selfImposedWait += state.selfImposedSince(stored.OccurredAt.UTC())
		state.deferral = &data.Deferral
		state.deferredAt = stored.OccurredAt.UTC()
		state.deferralCount++
		if state.queuedSince.IsZero() {
			state.queuedSince = stored.OccurredAt.UTC()
		}

	case EventAdmissionRefused:
		var data admissionDeferredData
		if err := decodePublicRunPayload(stored, &data); err != nil {
			return err
		}
		if reason := invalidAdmissionDeferral(data); reason != "" {
			return invalidRunEvent(stored, reason)
		}
		state.deferral = &data.Deferral
		state.deferralCount++

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
		state.launchAcceptedAt = time.Time{}
		// A new attempt is a new container, so the moment the previous one started
		// says nothing about this one and is cleared with the launch it belonged to.
		state.startedAt = nil
		state.readyAt = nil
		state.launchFailure = nil
		// A new attempt is a new machine as well as a new container, so nothing
		// the last one allocated, observed, or gave back belongs to this one.
		state.capacity = nil
		state.capacityAccepted = nil
		state.capacityStages = nil
		state.lastCapacityStageAt = time.Time{}
		state.nodeEnrolled = false
		state.capacityReclaimed = nil

	case EventCapacityRequested:
		var data capacityRequestedData
		if err := decodePublicRunPayload(stored, &data); err != nil {
			return err
		}
		if data.RentalID == "" || data.NodeID == "" || data.OfferSnapshotID == "" {
			return invalidRunEvent(stored, "requested capacity names its Rental, its node, and the offer it comes from")
		}
		state.capacity = &data

	case EventCapacityAccepted:
		var data capacityAcceptedData
		if err := decodePublicRunPayload(stored, &data); err != nil {
			return err
		}
		if state.capacity == nil {
			return invalidRunEvent(stored, "accepted capacity requires a recorded capacity request")
		}
		if !data.State.Valid() {
			return invalidRunEvent(stored, "accepted capacity states a lifecycle state Mercator knows")
		}
		state.capacityAccepted = &data
		// The machine the provider named, which is not always the one the listing
		// pointed at: a catalog selling a product mints the reference on accepting.
		state.capacity.NativeRef = data.NativeRef

	case EventCapacityStageObserved:
		var data capacityStageObservedData
		if err := decodePublicRunPayload(stored, &data); err != nil {
			return err
		}
		if !slices.Contains(domain.ProvisioningStages, data.Stage) {
			return invalidRunEvent(stored, "observed stage is one a machine being built goes through")
		}
		if state.capacityStages == nil {
			state.capacityStages = map[domain.LaunchStage]bool{}
		}
		state.capacityStages[data.Stage] = true
		state.lastCapacityStageAt = data.FinishedAt.UTC()
		if data.Stage == domain.StageAgentReady {
			state.nodeEnrolled = true
		}

	case EventCapacityReclaimed:
		var data capacityReclaimedData
		if err := decodePublicRunPayload(stored, &data); err != nil {
			return err
		}
		if data.RentalID == "" || data.Policy == "" {
			return invalidRunEvent(stored, "reclaimed capacity names its Rental and the policy that decided it")
		}
		state.capacityReclaimed = &data
		state.exclude(data.OfferSnapshotID, domain.OfferCapacityReclaimed)

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
		// When the provider took the launch, which is the only clock Mercator
		// has for how long this host has been getting ready to run it. Nothing
		// below the control plane reports "the image is still landing": a
		// provider says running from the moment it accepts, so what a host is
		// still doing for admitted work is Mercator's own prediction measured
		// from here.
		state.launchAcceptedAt = stored.OccurredAt

	case EventLaunchIndeterminate, EventLaunchFailed:
		var data launchFailureData
		if err := decodeRunPayload(stored, &data); err != nil {
			return err
		}
		if reason := invalidLaunchFailure(data); reason != "" {
			return invalidRunEvent(stored, reason)
		}
		state.launchFailure = &data
		if data.replacementEligible() && state.launchIntent != nil {
			state.exclude(state.launchIntent.SelectedOfferSnapshotID, domain.OfferRefusedTheLaunch)
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

	case EventExecutionStarted:
		var data executionStartedData
		if err := decodePublicRunPayload(stored, &data); err != nil {
			return err
		}
		if reason := invalidExecutionStarted(data); reason != "" {
			return invalidRunEvent(stored, reason)
		}
		startedAt := data.StartedAt.UTC()
		state.startedAt = &startedAt
		// A readiness already filed for a moment before this one is not this
		// container's readiness. The two moments come from different authorities,
		// the workload's arrives first as often as not, and neither of them can
		// check the order on its own, so it is checked here as well.
		if state.readyAt != nil && state.readyAt.Before(startedAt) {
			state.readyAt = nil
		}

	case EventRunReported:
		var data runReportedData
		if err := decodePublicRunPayload(stored, &data); err != nil {
			return err
		}
		if err := data.validate(); err != nil {
			return invalidRunEvent(stored, err.Error())
		}
		// The application's own moment rather than the moment Mercator appended
		// the report, and only where that moment is one Mercator can defend: no
		// later than the read that carried it, and no earlier than the container
		// it is about. Adopting whatever arrived is the defect the observed start
		// moment was fixed for one stage over, asked here in the same terms.
		//
		// The first defensible moment stands. A workload reports its readiness
		// once, so a second report is a repeat rather than a correction, and a
		// readiness that moved afterwards would rewrite a measurement already
		// recorded against a prediction.
		if ready, established := data.establishedReady(stored.OccurredAt, state.startedAt); established && state.readyAt == nil {
			state.readyAt = &ready
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
		ServiceClass:       state.requested.Workload.Spec.Placement.Class,
		Admission:          state.deferral,
		CreatedBy:          state.createdBy,
		CancelledBy:        state.cancelledBy,
	}
	if !state.queuedSince.IsZero() {
		queuedSince := state.queuedSince
		record.QueuedSince = &queuedSince
	}
	// A Run admission is still holding is queued, which is the phase the
	// lifecycle was missing: before this, a Run nothing could place read as
	// "requested" for ever and was indistinguishable from one Mercator had not
	// got to yet.
	//
	// It asks whether anything was chosen rather than whether anything was
	// decided. A Run nothing could place now records the decision that placed it
	// nowhere, and reading the presence of a decision as placement would report
	// every queued Run as requested again.
	if state.deferral != nil && !state.placed() {
		record.Phase = "queued"
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
	if state.startedAt != nil {
		startedAt := *state.startedAt
		record.StartedAt = &startedAt
	}
	if state.readyAt != nil {
		readyAt := *state.readyAt
		record.ReadyAt = &readyAt
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
