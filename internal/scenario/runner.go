package scenario

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
	"github.com/benngarcia/mercator/internal/orchestrator"
)

// Backend executes scenarios against some capacity substrate: the fake
// adapter's simulated world today, real daemons and providers later.
type Backend interface {
	StartWorld(world WorldSpec) (Session, error)
}

// Session is one scenario's live world. The runner drives it and asserts
// only on the events it records; scheduler internals stay invisible.
type Session interface {
	// Submit creates the named run and drives it to its first decision.
	Submit(name string, req RequestSpec) error
	// Reconcile drives Broker advancement for a named Run after relevant world
	// state or time changed.
	Reconcile(name string) error
	// AdvanceClock moves the scripted clock forward.
	AdvanceClock(d time.Duration)
	// RunEvents returns the named run's recorded event stream.
	RunEvents(name string) ([]eventlog.StoredEvent, error)
	// Notes reports world or request ontology the backend could not express,
	// so pending-red results say what was dropped.
	Notes() []string
	Close()
}

// Result is one scenario's execution: an empty Failures means every
// expectation held.
type Result struct {
	Failures []string
	Notes    []string
}

// Run executes a scenario against a backend and checks every expectation,
// reading only the event log.
func Run(backend Backend, sc Scenario) (Result, error) {
	session, err := backend.StartWorld(sc.World)
	if err != nil {
		return Result{}, fmt.Errorf("start world: %w", err)
	}
	defer session.Close()
	start := sc.World.Start()
	var failures []string
	for i, step := range sc.Steps() {
		switch {
		case step.Submit != "":
			if err := session.Submit(step.Submit, *step.Request); err != nil {
				failures = append(failures, fmt.Sprintf("step %d: submit %q: %v", i+1, step.Submit, err))
			}
			failures = append(failures, assertExpect(session, start, step.Submit, *step.Expect)...)
		case step.Advance != nil:
			session.AdvanceClock(step.Advance.Duration())
		case step.Reconcile != "":
			if err := session.Reconcile(step.Reconcile); err != nil {
				failures = append(failures, fmt.Sprintf("step %d: reconcile %q: %v", i+1, step.Reconcile, err))
			}
			failures = append(failures, assertExpect(session, start, step.Reconcile, *step.Expect)...)
		}
	}
	return Result{Failures: failures, Notes: session.Notes()}, nil
}

// recordedDecision is the latest booking decision in a run's stream, both
// decoded and raw. The raw form is where target-contract fields (Booking,
// RentalSchedule evidence, cache evidence) are asserted before the domain
// types carry them.
type recordedDecision struct {
	decision domain.BookingDecision
	raw      map[string]json.RawMessage
}

func latestDecision(events []eventlog.StoredEvent) (recordedDecision, bool) {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type != orchestrator.EventBookingDecided {
			continue
		}
		var payload struct {
			Decision json.RawMessage `json:"decision"`
		}
		if err := json.Unmarshal(events[i].Data, &payload); err != nil {
			continue
		}
		var rec recordedDecision
		if err := json.Unmarshal(payload.Decision, &rec.decision); err != nil {
			continue
		}
		if err := json.Unmarshal(payload.Decision, &rec.raw); err != nil {
			continue
		}
		return rec, true
	}
	return recordedDecision{}, false
}

func latestDisposition(events []eventlog.StoredEvent) (string, bool) {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type != orchestrator.EventLaunchIntentRecorded {
			continue
		}
		var payload struct {
			Disposition string `json:"disposition"`
		}
		if err := json.Unmarshal(events[i].Data, &payload); err != nil {
			continue
		}
		return payload.Disposition, true
	}
	return "", false
}

// bookingRecord is the target contract for the durable Booking created by
// a decision that selects an existing Rental.
type bookingRecord struct {
	BookingID        string       `json:"id"`
	RentalID         string       `json:"rental_id"`
	State            BookingState `json:"state"`
	AfterBookingID   string       `json:"after_booking_id,omitempty"`
	ProjectedStartAt *time.Time   `json:"projected_start_at,omitempty"`
	LatestStartAt    *time.Time   `json:"latest_start_at,omitempty"`
	ScheduleVersion  uint64       `json:"schedule_version"`
}

func (rec recordedDecision) booking() (bookingRecord, bool) {
	raw, ok := rec.raw["booking"]
	if !ok {
		return bookingRecord{}, false
	}
	var booking bookingRecord
	if err := json.Unmarshal(raw, &booking); err != nil {
		return bookingRecord{}, false
	}
	return booking, true
}

func (rec recordedDecision) outcome() Outcome {
	if rec.decision.SelectedOfferSnapshotID != "" {
		return OutcomePlace
	}
	return OutcomeFail
}

func (rec recordedDecision) describe() string {
	switch rec.outcome() {
	case OutcomePlace:
		if booking, ok := rec.booking(); ok {
			return fmt.Sprintf("placed on %q as %s Booking %q", rec.decision.SelectedOfferSnapshotID, booking.State, booking.BookingID)
		}
		return fmt.Sprintf("selected offer %q without a recorded Booking", rec.decision.SelectedOfferSnapshotID)
	default:
		return fmt.Sprintf("no offer selected (reasons %v)", rec.decision.SelectionReasonCodes)
	}
}

func assertExpect(session Session, start time.Time, name string, expect ExpectSpec) []string {
	events, err := session.RunEvents(name)
	if err != nil {
		return []string{fmt.Sprintf("run %q: read events: %v", name, err)}
	}
	rec, ok := latestDecision(events)
	if !ok {
		return []string{fmt.Sprintf("run %q: no booking decision recorded", name)}
	}
	var failures []string
	fail := func(format string, args ...any) {
		failures = append(failures, fmt.Sprintf("run %q: ", name)+fmt.Sprintf(format, args...))
	}

	if actual := rec.outcome(); actual != expect.Outcome {
		fail("expected outcome %q, but the decision %s", expect.Outcome, rec.describe())
	}
	if expect.Offer != "" && rec.decision.SelectedOfferSnapshotID != expect.Offer {
		fail("expected %q to win, but the decision %s", expect.Offer, rec.describe())
	}
	for _, reason := range expect.Reasons {
		if !slices.Contains(rec.decision.SelectionReasonCodes, reason) {
			fail("expected selection reason %q, got %v", reason, rec.decision.SelectionReasonCodes)
		}
	}
	if expect.Booking != nil {
		failures = append(failures, assertBooking(rec, start, name, *expect.Booking)...)
	}
	if expect.Disposition != "" {
		disposition, ok := latestDisposition(events)
		if !ok {
			fail("expected a launch intent with disposition %q, but none was recorded", expect.Disposition)
		} else if disposition != expect.Disposition {
			fail("expected disposition %q, got %q", expect.Disposition, disposition)
		}
	}
	for _, id := range sortedKeys(expect.Candidates) {
		failures = append(failures, assertCandidate(rec, name, id, expect.Candidates[id])...)
	}
	return failures
}

func assertBooking(rec recordedDecision, start time.Time, name string, expect BookingExpectation) []string {
	booking, ok := rec.booking()
	if !ok {
		return []string{fmt.Sprintf("run %q: expected Booking %q, but the decision records none", name, expect.BookingID)}
	}
	var failures []string
	fail := func(format string, args ...any) {
		failures = append(failures, fmt.Sprintf("run %q: Booking %q: ", name, expect.BookingID)+fmt.Sprintf(format, args...))
	}
	if booking.BookingID != expect.BookingID {
		fail("expected id %q, got %q", expect.BookingID, booking.BookingID)
	}
	if booking.RentalID != expect.RentalID {
		fail("expected Rental %q, got %q", expect.RentalID, booking.RentalID)
	}
	if booking.State != expect.State {
		fail("expected state %q, got %q", expect.State, booking.State)
	}
	if booking.AfterBookingID != expect.AfterBooking {
		fail("expected predecessor %q, got %q", expect.AfterBooking, booking.AfterBookingID)
	}
	if booking.ScheduleVersion != expect.ScheduleVersion {
		fail("expected schedule version %d, got %d", expect.ScheduleVersion, booking.ScheduleVersion)
	}
	if expect.ProjectedStart != nil {
		want := start.Add(expect.ProjectedStart.Duration())
		if booking.ProjectedStartAt == nil || !booking.ProjectedStartAt.Equal(want) {
			fail("expected projected start %s, got %s", want.Format(time.RFC3339), describeTime(booking.ProjectedStartAt))
		}
	}
	if expect.LatestStart != nil {
		want := expect.LatestStart.Resolve(start)
		if booking.LatestStartAt == nil || !booking.LatestStartAt.Equal(want) {
			fail("expected latest start %s, got %s", want.Format(time.RFC3339), describeTime(booking.LatestStartAt))
		}
	}
	return failures
}

func describeTime(value *time.Time) string {
	if value == nil {
		return "none"
	}
	return value.Format(time.RFC3339)
}

func assertCandidate(rec recordedDecision, name, id string, expect CandidateExpectation) []string {
	var failures []string
	fail := func(format string, args ...any) {
		failures = append(failures, fmt.Sprintf("run %q: candidate %q: ", name, id)+fmt.Sprintf(format, args...))
	}
	candidate, ok := findCandidate(rec.decision, id)
	if !ok {
		fail("not among the decision's %d candidates", len(rec.decision.Candidates))
		return failures
	}
	if expect.Feasible != nil && candidate.Feasible != *expect.Feasible {
		fail("expected feasible=%v, got %v (rejections %s)", *expect.Feasible, candidate.Feasible, describeRejections(candidate.Rejections))
	}
	for _, rejection := range expect.Rejected {
		if !hasRejection(candidate.Rejections, rejection) {
			fail("expected rejection %s at %s, got %s", rejection.Code, rejection.Path, describeRejections(candidate.Rejections))
		}
	}
	checkBound := func(field string, bound *Bound, actual float64) {
		if bound == nil {
			return
		}
		if problem := bound.Check(actual); problem != "" {
			fail("%s: %s", field, problem)
		}
	}
	checkBound("queue_seconds", expect.QueueSeconds, candidate.Estimates.QueueSeconds.Expected)
	checkBound("provision_seconds", expect.ProvisionSeconds, candidate.Estimates.ProvisionSeconds.Expected)
	checkBound("pull_seconds", expect.PullSeconds, candidate.Estimates.PullSeconds.Expected)
	if expect.Schedule != nil {
		failures = append(failures, assertScheduleEvidence(rec, name, id, *expect.Schedule)...)
	}
	for _, artifactID := range sortedKeys(expect.Artifacts) {
		present, ok := candidateArtifactEvidence(rec, id, artifactID)
		if !ok {
			fail("records no Artifact evidence for %q", artifactID)
			continue
		}
		if want := expect.Artifacts[artifactID] == "hit"; present != want {
			fail("Artifact %q: expected %s, recorded %s", artifactID, expect.Artifacts[artifactID], hitOrMiss(present))
		}
	}
	return failures
}

type scheduleEvidenceRecord struct {
	Version uint64 `json:"version"`
	Running *struct {
		BookingID                       string  `json:"booking_id"`
		RunID                           string  `json:"run_id"`
		RemainingMaxRuntimeSeconds      float64 `json:"remaining_max_runtime_seconds"`
		RemainingExpectedRuntimeSeconds float64 `json:"remaining_expected_runtime_seconds"`
	} `json:"running,omitempty"`
	Preceding []struct {
		BookingID              string  `json:"booking_id"`
		RunID                  string  `json:"run_id"`
		MaxRuntimeSeconds      float64 `json:"max_runtime_seconds"`
		ExpectedRuntimeSeconds float64 `json:"expected_runtime_seconds"`
	} `json:"preceding,omitempty"`
	ProjectedStartSeconds float64 `json:"projected_start_seconds"`
}

func assertScheduleEvidence(rec recordedDecision, name, id string, expect ScheduleEvidenceExpectation) []string {
	actual, ok := candidateScheduleEvidence(rec, id)
	if !ok {
		return []string{fmt.Sprintf("run %q: candidate %q: records no RentalSchedule evidence", name, id)}
	}
	var failures []string
	fail := func(format string, args ...any) {
		failures = append(failures, fmt.Sprintf("run %q: candidate %q: ", name, id)+fmt.Sprintf(format, args...))
	}
	if actual.Version != expect.Version {
		fail("expected schedule version %d, got %d", expect.Version, actual.Version)
	}
	if actual.Running == nil || actual.Running.BookingID != expect.Running.BookingID || actual.Running.RunID != expect.Running.RunID ||
		actual.Running.RemainingMaxRuntimeSeconds != expect.Running.RemainingMaxRuntime.Duration().Seconds() ||
		actual.Running.RemainingExpectedRuntimeSeconds != expect.Running.expectedRemaining().Duration().Seconds() {
		fail("running Booking evidence does not match %+v", *expect.Running)
	}
	if len(actual.Preceding) != len(expect.Preceding) {
		fail("expected %d preceding Bookings, got %d", len(expect.Preceding), len(actual.Preceding))
	} else {
		for i, want := range expect.Preceding {
			got := actual.Preceding[i]
			if got.BookingID != want.BookingID || got.RunID != want.RunID ||
				got.MaxRuntimeSeconds != want.MaxRuntime.Duration().Seconds() ||
				got.ExpectedRuntimeSeconds != want.expected().Duration().Seconds() {
				fail("preceding[%d] does not match %+v", i, want)
			}
		}
	}
	if actual.ProjectedStartSeconds != expect.ProjectedStart.Duration().Seconds() {
		fail("expected projected_start_seconds %.0f, got %.0f", expect.ProjectedStart.Duration().Seconds(), actual.ProjectedStartSeconds)
	}
	return failures
}

func candidateScheduleEvidence(rec recordedDecision, id string) (scheduleEvidenceRecord, bool) {
	var candidates []map[string]json.RawMessage
	if err := json.Unmarshal(rec.raw["candidates"], &candidates); err != nil {
		return scheduleEvidenceRecord{}, false
	}
	for _, candidate := range candidates {
		var candidateID string
		if err := json.Unmarshal(candidate["offer_snapshot_id"], &candidateID); err != nil || candidateID != id {
			continue
		}
		var evidence scheduleEvidenceRecord
		if err := json.Unmarshal(candidate["rental_schedule"], &evidence); err != nil {
			return scheduleEvidenceRecord{}, false
		}
		return evidence, true
	}
	return scheduleEvidenceRecord{}, false
}

// candidateArtifactEvidence reads the target contract for Artifact evidence
// from the decision's raw JSON: each candidate carries
// {"artifact_evidence": [{"artifact_id", "present"}]} once Artifact scoring
// exists.
func candidateArtifactEvidence(rec recordedDecision, id, artifactID string) (bool, bool) {
	var candidates []map[string]json.RawMessage
	if err := json.Unmarshal(rec.raw["candidates"], &candidates); err != nil {
		return false, false
	}
	for _, candidate := range candidates {
		var candidateID string
		if err := json.Unmarshal(candidate["offer_snapshot_id"], &candidateID); err != nil || candidateID != id {
			continue
		}
		var evidence []struct {
			ArtifactID string `json:"artifact_id"`
			Present    bool   `json:"present"`
		}
		if err := json.Unmarshal(candidate["artifact_evidence"], &evidence); err != nil {
			return false, false
		}
		for _, entry := range evidence {
			if entry.ArtifactID == artifactID {
				return entry.Present, true
			}
		}
	}
	return false, false
}

func findCandidate(decision domain.BookingDecision, id string) (domain.CandidateDecision, bool) {
	for _, candidate := range decision.Candidates {
		if candidate.OfferSnapshotID == id {
			return candidate, true
		}
	}
	return domain.CandidateDecision{}, false
}

func hasRejection(rejections []domain.Violation, want RejectionSpec) bool {
	for _, rejection := range rejections {
		if rejection.Code == want.Code && rejection.Path == want.Path {
			return true
		}
	}
	return false
}

func describeRejections(rejections []domain.Violation) string {
	if len(rejections) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(rejections))
	for _, rejection := range rejections {
		parts = append(parts, rejection.Code+"@"+rejection.Path)
	}
	return fmt.Sprintf("%v", parts)
}

func hitOrMiss(hit bool) string {
	if hit {
		return "hit"
	}
	return "miss"
}

func sortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}
