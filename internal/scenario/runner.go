package scenario

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
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
	// AdvanceClock moves the scripted clock forward, carrying whatever the
	// world owed the control plane at the moments it passed.
	AdvanceClock(d time.Duration) error
	// RunEvents returns the named run's recorded event stream.
	RunEvents(name string) ([]eventlog.StoredEvent, error)
	// RunRecord is the named Run as Mercator's own read model has it. Readiness is
	// asserted through it because a readiness report is a claim and the record is
	// what Mercator adopted: a fixture reading the report could not state the world
	// where a workload published a moment the control plane refused, which is the
	// world a host with the wrong clock puts every one of its workloads in.
	RunRecord(name string) (domain.RunRecord, error)
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
	bookings := seededBookings(sc.World)
	var failures []string
	for i, step := range sc.Steps() {
		switch {
		case step.Submit != "":
			if err := session.Submit(step.Submit, *step.Request); err != nil {
				failures = append(failures, fmt.Sprintf("step %d: submit %q: %v", i+1, step.Submit, err))
			}
			failures = append(failures, assertExpect(session, start, bookings, step.Submit, *step.Expect)...)
		case step.Advance != nil:
			if err := session.AdvanceClock(step.Advance.Duration()); err != nil {
				failures = append(failures, fmt.Sprintf("step %d: advance %s: %v", i+1, step.Advance.Duration(), err))
			}
		case step.Reconcile != "":
			if err := session.Reconcile(step.Reconcile); err != nil {
				failures = append(failures, fmt.Sprintf("step %d: reconcile %q: %v", i+1, step.Reconcile, err))
			}
			failures = append(failures, assertExpect(session, start, bookings, step.Reconcile, *step.Expect)...)
		}
	}
	return Result{Failures: failures, Notes: session.Notes()}, nil
}

// recordedDecision is one booking decision in a run's stream, both decoded and
// raw. The raw form is where target-contract fields (Booking, RentalSchedule
// evidence, cache evidence) are asserted before the domain types carry them.
type recordedDecision struct {
	decision domain.BookingDecision
	raw      map[string]json.RawMessage
}

func latestDecision(events []eventlog.StoredEvent) (recordedDecision, bool) {
	chain := decisionChain(events)
	if len(chain) == 0 {
		return recordedDecision{}, false
	}
	return chain[len(chain)-1], true
}

// decisionChain is every Booking Decision this Run's stream holds, in the order
// Mercator appended them. It is the whole record rather than its last element,
// because a changed answer appends: a reader given only the newest cannot tell a
// Run answered once from a Run whose first answer was replaced, and telling them
// apart is what this corpus states.
func decisionChain(events []eventlog.StoredEvent) []recordedDecision {
	var chain []recordedDecision
	for _, event := range events {
		if event.Type != orchestrator.EventBookingDecided {
			continue
		}
		var payload struct {
			Decision json.RawMessage `json:"decision"`
		}
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			continue
		}
		var rec recordedDecision
		if err := json.Unmarshal(payload.Decision, &rec.decision); err != nil {
			continue
		}
		if err := json.Unmarshal(payload.Decision, &rec.raw); err != nil {
			continue
		}
		chain = append(chain, rec)
	}
	return chain
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

// bookingNames is what each name a fixture gives a Booking turns out to be. A
// Booking a world starts with answers to the name it was seeded under, because
// that is the identity the Broker holds for it. A Booking a decision creates
// answers to whatever Mercator hashed, which no fixture can predict and none
// should: the fixture's name is the corpus's handle for it, so the corpus can
// say which Booking a later one waits behind.
type bookingNames map[string]string

func seededBookings(world WorldSpec) bookingNames {
	names := bookingNames{}
	for _, schedule := range world.RentalSchedules {
		if schedule.Running != nil {
			names[schedule.Running.BookingID] = schedule.Running.BookingID
		}
		for _, queued := range schedule.Queued {
			names[queued.BookingID] = queued.BookingID
		}
	}
	return names
}

// bind records which Booking a fixture's name turned out to mean, and refuses
// both ways it can go wrong: a name that meant one Booking and now means
// another, and two names for one Booking, which is Mercator handing a second Run
// an identity another Run already holds.
func (names bookingNames) bind(name, id string) error {
	if id == "" {
		return fmt.Errorf("the decision recorded a Booking with no identity")
	}
	if held, ok := names[name]; ok && held != id {
		return fmt.Errorf("expected the Booking already recorded as %q, got %q", held, id)
	}
	for other, held := range names {
		if held == id && other != name {
			return fmt.Errorf("expected a Booking of its own, got %q, which Mercator already minted for %q", id, other)
		}
	}
	names[name] = id
	return nil
}

func assertExpect(session Session, start time.Time, bookings bookingNames, name string, expect ExpectSpec) []string {
	events, err := session.RunEvents(name)
	if err != nil {
		return []string{fmt.Sprintf("run %q: read events: %v", name, err)}
	}
	if expect.Outcome == OutcomeDefer || expect.Outcome == OutcomeRefuse {
		failures := assertAdmission(events, name, expect)
		return append(failures, assertDecisionChain(events, name, expect)...)
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
		failures = append(failures, assertBooking(rec, start, bookings, name, *expect.Booking)...)
	}
	if expect.Disposition != "" {
		disposition, ok := latestDisposition(events)
		if !ok {
			fail("expected a launch intent with disposition %q, but none was recorded", expect.Disposition)
		} else if disposition != expect.Disposition {
			fail("expected disposition %q, got %q", expect.Disposition, disposition)
		}
	}
	failures = append(failures, assertDecisionChain(events, name, expect)...)
	failures = append(failures, assertCapacityHandedBack(events, name, expect)...)
	failures = append(failures, assertStartMoment(events, name, expect)...)
	failures = append(failures, assertReadyMoment(session, events, name, expect)...)
	for _, id := range sortedKeys(expect.Candidates) {
		failures = append(failures, assertCandidate(rec, bookings, name, id, expect.Candidates[id])...)
	}
	return failures
}

// assertDecisionChain reads the record of the decisions themselves: how many
// this Run holds, and whether the newest one names the one it replaces and why.
//
// The predecessor is checked by identity and not by position alone, because
// naming a decision is the whole claim. A chain whose newest entry points at
// anything other than the record immediately before it is a chain a reader
// cannot walk, and a decision that names nothing leaves the reader taking the
// last record silently, which is the state this stage replaced.
func assertDecisionChain(events []eventlog.StoredEvent, name string, expect ExpectSpec) []string {
	if expect.Decision == nil {
		return nil
	}
	want := *expect.Decision
	chain := decisionChain(events)
	fail := func(format string, args ...any) []string {
		return []string{fmt.Sprintf("run %q: ", name) + fmt.Sprintf(format, args...)}
	}
	if len(chain) != want.Recorded {
		return fail("expected %d recorded decisions, and the record holds %d", want.Recorded, len(chain))
	}
	newest := chain[len(chain)-1].decision
	if want.Supersedes == 0 {
		if newest.Supersedes != "" {
			return fail("expected a first decision that replaces nothing, and it supersedes %q", newest.Supersedes)
		}
		return nil
	}
	var failures []string
	if predecessor := chain[want.Supersedes-1].decision.ID; newest.Supersedes != predecessor {
		failures = append(failures, fail("expected the newest decision to supersede %q, and it names %q", predecessor, newest.Supersedes)...)
	}
	if newest.SupersedesReason != want.SupersedesReason {
		failures = append(failures, fail("expected the supersession reason %q, recorded %q", want.SupersedesReason, newest.SupersedesReason)...)
	}
	return failures
}

// assertCapacityHandedBack reads whether the capacity an earlier decision took was
// given back before the newest decision was recorded, and how.
//
// Before, rather than at some point, is the whole assertion. A control plane that
// runs the work elsewhere and reclaims the first machine eventually is a control
// plane an operator is paying twice, and one that never reclaims it at all leaves
// the machine to whatever its provider does with capacity nobody is talking to.
// Reading the order out of the Run's own stream is the only way this corpus can
// tell those apart from the answer that changed.
func assertCapacityHandedBack(events []eventlog.StoredEvent, name string, expect ExpectSpec) []string {
	if expect.Reclaimed == nil {
		return nil
	}
	want := *expect.Reclaimed
	handed := handedBackBefore(events, lastIndexOf(events, orchestrator.EventBookingDecided))
	disposition, ok := handed[want.Offer]
	if !ok {
		return []string{fmt.Sprintf(
			"run %q: expected the capacity on %q to be handed back before this decision, and the record hands back %v",
			name, want.Offer, handed,
		)}
	}
	if disposition != want.Disposition {
		return []string{fmt.Sprintf(
			"run %q: expected %q to be handed back as %q, and the record confirms %q",
			name, want.Offer, want.Disposition, disposition,
		)}
	}
	return nil
}

// handedBackBefore is the capacity this Run gave back before the event at index,
// by the machine it was taken on and what the confirmed cleanup did with it.
//
// The two halves come from two events. A cleanup names the launch key alone, and
// the machine that key was launched on is named on the intent that recorded it, so
// the stream is read forwards and the key resolved through the intent it belongs
// to. A cleanup Mercator requested and never confirmed is deliberately not here:
// capacity is handed back when the provider says it is.
func handedBackBefore(events []eventlog.StoredEvent, index int) map[string]domain.Disposition {
	offers := map[string]string{}
	handed := map[string]domain.Disposition{}
	for position, event := range events {
		if position >= index {
			break
		}
		var payload struct {
			LaunchKey   string             `json:"launch_key"`
			Offer       string             `json:"selected_offer_snapshot_id"`
			Disposition domain.Disposition `json:"disposition"`
		}
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			continue
		}
		switch event.Type {
		case orchestrator.EventLaunchIntentRecorded:
			offers[payload.LaunchKey] = payload.Offer
		case orchestrator.EventCleanupConfirmed:
			handed[offers[payload.LaunchKey]] = payload.Disposition
		}
	}
	return handed
}

func lastIndexOf(events []eventlog.StoredEvent, eventType string) int {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == eventType {
			return i
		}
	}
	return -1
}

// placedInstead names the machine a Run the fixture said would be waiting was
// actually placed on. Being placed appends no admission fact of its own, so the
// last thing admission said about such a Run is the wait it was in beforehand, and
// a diagnostic reading only that reports a Run that ran as a Run still queued.
func placedInstead(events []eventlog.StoredEvent) string {
	rec, ok := latestDecision(events)
	if !ok || rec.decision.SelectedOfferSnapshotID == "" {
		return ""
	}
	return fmt.Sprintf(", and the Run was then placed on %q", rec.decision.SelectedOfferSnapshotID)
}

// assertAdmission reads what admission recorded about a Run that is not running:
// the last thing it said, and whether it said it as a wait or as a refusal. Both
// are read off the Run's own stream, which is the only place an operator has to
// look and the only place this claim can be made from: a Run that waits and
// records nothing is exactly the state the queue replaced.
func assertAdmission(events []eventlog.StoredEvent, name string, expect ExpectSpec) []string {
	var failures []string
	fail := func(format string, args ...any) {
		failures = append(failures, fmt.Sprintf("run %q: ", name)+fmt.Sprintf(format, args...))
	}
	wanted := orchestrator.EventAdmissionDeferred
	if expect.Outcome == OutcomeRefuse {
		wanted = orchestrator.EventAdmissionRefused
	}
	deferral, recordedAs, ok := latestAdmission(events)
	switch {
	case !ok:
		fail("expected outcome %q, and admission recorded nothing at all about this Run waiting", expect.Outcome)
		return failures
	case recordedAs != wanted:
		fail("expected outcome %q, and admission recorded %q with reason %q%s",
			expect.Outcome, recordedAs, deferral.Reason, placedInstead(events))
		return failures
	}
	want := *expect.Deferral
	if deferral.Reason != want.Reason {
		fail("expected the reason %q, recorded %q", want.Reason, deferral.Reason)
	}
	if want.Behind != nil {
		behind := make([]string, 0, len(deferral.Behind))
		for _, waiting := range deferral.Behind {
			behind = append(behind, strings.TrimPrefix(waiting.RunID, "run-"))
		}
		slices.Sort(behind)
		expected := slices.Sorted(slices.Values(want.Behind))
		if !slices.Equal(behind, expected) {
			fail("expected to be waiting behind %v, and the record names %v", expected, behind)
		}
	}
	if want.Priority != nil {
		if problem := want.Priority.Check(deferral.EffectivePriority); problem != "" {
			fail("effective_priority: %s", problem)
		}
	}
	if want.QueuedSeconds != nil {
		if problem := want.QueuedSeconds.Check(deferral.QueuedSeconds); problem != "" {
			fail("queued_seconds: %s", problem)
		}
	}
	if want.Fleet != nil {
		for _, problem := range fleetAnswerProblems(*want.Fleet, deferral.Fleet) {
			fail("%s", problem)
		}
	}
	if want.ProjectedWait != nil {
		if problem := want.ProjectedWait.Check(deferral.ProjectedWaitSeconds); problem != "" {
			fail("projected_wait_seconds: %s", problem)
		}
	}
	return failures
}

// fleetAnswerProblems is the fixture's claim about the fleet's answer read against
// the answer the record carries. Whether there is one at all is checked first,
// because a wait the queue caused and a fleet that published nothing an ask matches
// both weighed no machines and are opposite statements about capacity.
func fleetAnswerProblems(want FleetExpectation, answer *domain.FleetAnswer) []string {
	if want.Absent {
		if answer == nil {
			return nil
		}
		return []string{fmt.Sprintf("expected a wait resting on no answer about capacity, and the record weighed %d machines", answer.Weighed)}
	}
	if answer == nil {
		return []string{"expected the record to say what the fleet answered, and it carries no answer at all"}
	}
	var problems []string
	if want.Weighed != nil {
		if problem := want.Weighed.Check(float64(answer.Weighed)); problem != "" {
			problems = append(problems, "the machines weighed: "+problem)
		}
	}
	if want.CouldHold != nil {
		if problem := want.CouldHold.Check(float64(answer.CouldHold)); problem != "" {
			problems = append(problems, "the machines that could hold this Run once free: "+problem)
		}
	}
	if want.Unstated != nil {
		if problem := want.Unstated.Check(float64(answer.Unstated)); problem != "" {
			problems = append(problems, "the machines that said too little to tell: "+problem)
		}
	}
	return problems
}

// latestAdmission is the last thing admission said about this Run, and which of
// the two things it was.
func latestAdmission(events []eventlog.StoredEvent) (domain.AdmissionDeferral, string, bool) {
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if event.Type != orchestrator.EventAdmissionDeferred && event.Type != orchestrator.EventAdmissionRefused {
			continue
		}
		var payload struct {
			Deferral domain.AdmissionDeferral `json:"deferral"`
		}
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			continue
		}
		return payload.Deferral, event.Type, true
	}
	return domain.AdmissionDeferral{}, "", false
}

// assertStartMoment reads the two moments a start latency is the difference
// between, both out of the Run's own stream: when the provider took the launch,
// and when the workload was observed beginning. It asserts nothing unless the
// fixture states one of them, because most fixtures are about a placement decision
// and never advance far enough for any container to start.
func assertStartMoment(events []eventlog.StoredEvent, name string, expect ExpectSpec) []string {
	if expect.StartLatency == nil && !expect.NoStartObserved {
		return nil
	}
	fail := func(format string, args ...any) []string {
		return []string{fmt.Sprintf("run %q: ", name) + fmt.Sprintf(format, args...)}
	}
	accepted, taken := launchAcceptedAt(events)
	started, observed := executionStartedAt(events)
	switch {
	case expect.NoStartObserved && observed:
		return fail("records a start moment of %s, and the fixture says nobody observed one", started.Format(time.RFC3339))
	case expect.NoStartObserved:
		return nil
	case !taken:
		return fail("has no accepted launch, so there is no moment to measure a start from")
	case !observed:
		return fail("records no start moment, so nothing observed its workload begin")
	}
	if problem := expect.StartLatency.Check(started.Sub(accepted).Seconds()); problem != "" {
		return fail("start_latency_seconds: %s", problem)
	}
	return nil
}

// assertReadyMoment reads the last stage of a launch out of the Run's own stream:
// the moment its process began, and the moment its application said it could do
// work. It asserts nothing unless the fixture states one of them, because most
// fixtures are about a placement decision and never run an application at all.
func assertReadyMoment(session Session, events []eventlog.StoredEvent, name string, expect ExpectSpec) []string {
	if expect.ReadyLatency == nil && !expect.NoReadyReported {
		return nil
	}
	fail := func(format string, args ...any) []string {
		return []string{fmt.Sprintf("run %q: ", name) + fmt.Sprintf(format, args...)}
	}
	started, observed := executionStartedAt(events)
	ready, reported, err := applicationReadyAt(session, name)
	if err != nil {
		return fail("read run record: %v", err)
	}
	switch {
	case expect.NoReadyReported && reported:
		return fail("records its application ready at %s, and the fixture says it has not said so", ready.Format(time.RFC3339))
	case expect.NoReadyReported:
		return nil
	case !observed:
		return fail("records no start moment, so there is nothing to measure a readiness from")
	case !reported:
		return fail("records no readiness, so nothing said this workload can do work")
	}
	if problem := expect.ReadyLatency.Check(ready.Sub(started).Seconds()); problem != "" {
		return fail("ready_latency_seconds: %s", problem)
	}
	return nil
}

// applicationReadyAt is the readiness Mercator adopted for this Run, which exists
// only where a workload stated a moment the control plane could defend. It is read
// off the record rather than off the report for the same reason the start moment is
// read off compute.run.execution_started.v1 rather than off the observation that
// carried it: what a foreign clock published and what Mercator will measure against
// are different facts, and only the second one is the Run's.
func applicationReadyAt(session Session, name string) (time.Time, bool, error) {
	record, err := session.RunRecord(name)
	if err != nil {
		return time.Time{}, false, err
	}
	if record.ReadyAt == nil {
		return time.Time{}, false, nil
	}
	return record.ReadyAt.UTC(), true, nil
}

// launchAcceptedAt is the provider's own accepted moment, which is when this
// machine started getting ready.
func launchAcceptedAt(events []eventlog.StoredEvent) (time.Time, bool) {
	for index := len(events) - 1; index >= 0; index-- {
		if events[index].Type != orchestrator.EventLaunchAccepted {
			continue
		}
		var payload struct {
			AcceptedAt time.Time `json:"accepted_at"`
		}
		if err := json.Unmarshal(events[index].Data, &payload); err != nil {
			continue
		}
		return payload.AcceptedAt, true
	}
	return time.Time{}, false
}

// executionStartedAt is the moment the run stream records the workload beginning,
// which exists only when something observed one.
func executionStartedAt(events []eventlog.StoredEvent) (time.Time, bool) {
	for index := len(events) - 1; index >= 0; index-- {
		if events[index].Type != orchestrator.EventExecutionStarted {
			continue
		}
		var payload struct {
			StartedAt time.Time `json:"started_at"`
		}
		if err := json.Unmarshal(events[index].Data, &payload); err != nil {
			continue
		}
		return payload.StartedAt, true
	}
	return time.Time{}, false
}

func assertBooking(rec recordedDecision, start time.Time, bookings bookingNames, name string, expect BookingExpectation) []string {
	booking, ok := rec.booking()
	if !ok {
		return []string{fmt.Sprintf("run %q: expected Booking %q, but the decision records none", name, expect.BookingID)}
	}
	var failures []string
	fail := func(format string, args ...any) {
		failures = append(failures, fmt.Sprintf("run %q: Booking %q: ", name, expect.BookingID)+fmt.Sprintf(format, args...))
	}
	if err := bookings.bind(expect.BookingID, booking.BookingID); err != nil {
		fail("%v", err)
	}
	if booking.RentalID != expect.RentalID {
		fail("expected Rental %q, got %q", expect.RentalID, booking.RentalID)
	}
	if booking.State != expect.State {
		fail("expected state %q, got %q", expect.State, booking.State)
	}
	if after := bookings[expect.AfterBooking]; booking.AfterBookingID != after {
		fail("expected predecessor %q (%q), got %q", expect.AfterBooking, after, booking.AfterBookingID)
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

func assertCandidate(rec recordedDecision, bookings bookingNames, name, id string, expect CandidateExpectation) []string {
	var failures []string
	fail := func(format string, args ...any) {
		failures = append(failures, fmt.Sprintf("run %q: candidate %q: ", name, id)+fmt.Sprintf(format, args...))
	}
	candidate, ok := findCandidate(rec.decision, id)
	if !ok {
		fail("not among the decision's %d candidates", len(rec.decision.Candidates))
		return failures
	}
	if expect.Candidate != nil {
		if recorded := candidate.Candidate.Candidate(false); recorded != *expect.Candidate {
			fail("candidate: want %q, got %q", *expect.Candidate, recorded)
		}
	}
	if expect.Content != nil {
		if recorded := candidate.Candidate.Candidate(true); recorded != *expect.Content {
			fail("content: want %q, got %q", *expect.Content, recorded)
		}
	}
	if expect.Feasible != nil && candidate.Feasible != *expect.Feasible {
		fail("expected feasible=%v, got %v (rejections %s)", *expect.Feasible, candidate.Feasible, describeRejections(candidate.Rejections))
	}
	if expect.Disposition != "" && candidate.Disposition != expect.Disposition {
		fail("expected disposition %q, got %q", expect.Disposition, candidate.Disposition)
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
	// In the order a launch goes through the stages, so a fixture stating several
	// of them reads its failures as a waterfall rather than alphabetically.
	for _, stage := range domain.LaunchStages {
		want, stated := expect.Stages[stage]
		if !stated {
			continue
		}
		recorded := candidate.Estimates.Stages.Stage(stage)
		checkBound(string(stage)+"_seconds", want.Seconds, recorded.Expected)
		if want.Source != "" && recorded.Source != want.Source {
			fail("%s source: want %q, got %q", stage, want.Source, recorded.Source)
		}
		if want.Confidence != nil && recorded.Confidence != *want.Confidence {
			fail("%s confidence: want %v, got %v", stage, *want.Confidence, recorded.Confidence)
		}
		// The level and the count are asserted together with the seconds, because
		// what a fixture about a hierarchy is pinning is which evidence answered:
		// the same ninety seconds from this machine's own launches and from a
		// province of other machines are two different claims, and only these say
		// which one the record made.
		if want.Level != "" && string(recorded.Level) != want.Level {
			fail("%s level: want %q, answered at %q from %d samples", stage, want.Level, recorded.Level, recorded.SampleCount)
		}
		if want.Samples != nil && recorded.SampleCount != *want.Samples {
			fail("%s samples: want %d, answered from %d at level %q", stage, *want.Samples, recorded.SampleCount, recorded.Level)
		}
		assertTransferRate(fail, stage, candidate.TransferRates, want.Rate)
	}
	if expect.ImageLocality != "" && candidate.ImageLocality != expect.ImageLocality {
		fail("image_locality: want %q, got %q", expect.ImageLocality, candidate.ImageLocality)
	}
	if expect.Schedule != nil {
		failures = append(failures, assertScheduleEvidence(rec, bookings, name, id, *expect.Schedule)...)
	}
	if recorded, ok := candidateScheduleEvidence(rec, id); expect.NoSchedule && ok {
		fail("records a RentalSchedule at version %d with a wait of %.0fs, and there is no queue here to have read",
			recorded.Version, recorded.ProjectedStartSeconds)
	}
	for _, artifactID := range sortedKeys(expect.Artifacts) {
		found, ok := artifactEvidence(candidate, artifactID)
		if !ok {
			fail("records no Artifact evidence for %q", artifactID)
			continue
		}
		if want := ArtifactExpectations[expect.Artifacts[artifactID]]; found.Locality != want {
			fail("Artifact %q: expected %q, recorded %q", artifactID, want, found.Locality)
		}
	}
	for _, cache := range sortedKeys(expect.Caches) {
		found, ok := cacheEvidence(candidate, cache)
		if !ok {
			fail("records no cache evidence for %q", cache)
			continue
		}
		if want := CacheExpectations[expect.Caches[cache]]; found.Locality != want {
			fail("cache %q: expected %q, recorded %q", cache, want, found.Locality)
		}
	}
	// The price is asserted term by term and not only as a total, because the
	// total is the number two opposite mistakes agree on: an owned machine
	// charged for nothing at all and one charged the whole hour it is committed
	// to differ in which term the dollars sit in.
	if want := expect.Cost; want != nil {
		assertCost(fail, checkBound, candidate, *want)
	}
	// The uncertainty is read off the candidate's own recorded confidences rather
	// than recomputed, because that record is the whole input to the term and a
	// fixture reading anything else would assert a number the score was not
	// computed from.
	checkBound("uncertainty", expect.Uncertainty, candidate.Uncertainty())
	checkBound("score_usd", expect.ScoreUSD, candidate.ScoreUSD)
	// The risk history is asserted against the record and never against the world
	// that published it. A rate reaches a placement through the offer, so reading
	// the fixture's own declaration back would assert that the fixture says what
	// it says.
	checkRate := func(field string, want *float64, recorded domain.StatedRate) {
		if want == nil {
			return
		}
		if !recorded.Stated() {
			fail("%s: want %v, and the record states no such measurement", field, *want)
			return
		}
		if *want != recorded.Rate {
			fail("%s: want %v, recorded %v", field, *want, recorded.Rate)
		}
		if expect.RiskConfidence != nil && *expect.RiskConfidence != recorded.Confidence {
			fail("%s: want its publisher standing %v behind it, recorded %v", field, *expect.RiskConfidence, recorded.Confidence)
		}
	}
	checkRate("start_failure_rate", expect.StartFailureRate, candidate.Reliability.StartFailures)
	checkRate("interruption_rate", expect.InterruptionRate, candidate.Reliability.Interruptions)
	if expect.NoRiskHistory && candidate.Reliability.Measured() {
		fail("records the risk history %+v, and nobody has measured this machine", candidate.Reliability)
	}
	return failures
}

// assertCost holds one candidate to the price a fixture says it carries and to
// the parts of a sale that price is made of. A term the fixture names and the
// record has none of is named as the absence it is: a candidate charged nothing
// for the tail of a billing increment records no such term, and a bound read
// against a missing term would pass on a zero the record never stated.
func assertCost(fail func(string, ...any), checkBound func(string, *Bound, float64), candidate domain.CandidateDecision, want CostExpectation) {
	if want.Unpriced {
		if candidate.Priced() {
			fail("cost: want a machine nobody quoted, recorded %.6f USD from %q", candidate.Estimates.CostUSD.Expected, candidate.Estimates.CostUSD.Source)
		}
		return
	}
	if !candidate.Priced() {
		fail("cost: want a priced machine, and the record says nobody quoted this one")
		return
	}
	checkBound("cost_usd", want.USD, candidate.Estimates.CostUSD.Expected)
	for _, name := range domain.CostTermNames() {
		bound, stated := want.Terms[name]
		if !stated {
			continue
		}
		charged, recorded := candidate.Estimates.CostTermUSD(name)
		if !recorded {
			fail("cost term %q: want %s, and the price is not made of that term at all", name, bound)
			continue
		}
		checkBound("cost term "+name, &bound, charged)
	}
	checkBound("committed_seconds", want.CommittedSeconds, candidate.Estimates.Committed.Seconds)
}

// assertTransferRate holds one stage to the rate a fixture says it was priced at
// and to where that number came from. A stage a fixture states a rate for and
// the record has none of is the failure worth naming twice: it means the
// candidate owed no bytes at that stage, so nothing was priced, and the seconds
// beside it would have read as a satisfied assertion.
func assertTransferRate(fail func(string, ...any), stage domain.LaunchStage, recorded []domain.TransferRate, want *TransferRateExpectation) {
	if want == nil {
		return
	}
	index := slices.IndexFunc(recorded, func(rate domain.TransferRate) bool { return rate.Stage == stage })
	if index < 0 {
		fail("%s rate: want %v Mbps, and the record prices no transfer at that stage at all", stage, want.Mbps)
		return
	}
	rate := recorded[index]
	if rate.Mbps != want.Mbps {
		fail("%s rate: want %v Mbps, priced at %v", stage, want.Mbps, rate.Mbps)
	}
	if rate.Measurement != want.Measurement {
		fail("%s rate: want the measurement %q, priced from %q", stage, want.Measurement, rate.Measurement)
	}
	if rate.Assumption != want.Assumption {
		fail("%s rate: want the assumption %q, priced from %q", stage, want.Assumption, rate.Assumption)
	}
}

// CacheExpectations is what a fixture's word for cache warmth means. A Cache
// Mount is the application's own state under a name, so there is no partial
// answer: this host holds the generation the workload asked for, it does not, or
// nobody could ask it.
var CacheExpectations = map[string]domain.LocalityState{
	"hit":     domain.LocalityHot,
	"miss":    domain.LocalityCold,
	"unknown": domain.LocalityUnknown,
}

func cacheEvidence(candidate domain.CandidateDecision, name string) (domain.CacheEvidence, bool) {
	for _, found := range candidate.CacheEvidence {
		if found.Name == name {
			return found, true
		}
	}
	return domain.CacheEvidence{}, false
}

// ArtifactExpectations is what a fixture's word for Artifact locality means. A
// fixture asks the question an operator asks, "was it here", and the third
// answer is the one this architecture insists on: nobody could say.
var ArtifactExpectations = map[string]domain.LocalityState{
	"hit":     domain.LocalityHot,
	"miss":    domain.LocalityCold,
	"unknown": domain.LocalityUnknown,
}

func artifactEvidence(candidate domain.CandidateDecision, artifactID string) (domain.ArtifactEvidence, bool) {
	for _, found := range candidate.ArtifactEvidence {
		if found.ArtifactID == artifactID {
			return found, true
		}
	}
	return domain.ArtifactEvidence{}, false
}

type scheduleEvidenceRecord struct {
	Version uint64 `json:"version"`
	Running *struct {
		BookingID                       string  `json:"booking_id"`
		RunID                           string  `json:"run_id"`
		RemainingMaxRuntimeSeconds      float64 `json:"remaining_max_runtime_seconds"`
		RemainingExpectedRuntimeSeconds float64 `json:"remaining_expected_runtime_seconds"`
		OverrunSeconds                  float64 `json:"overrun_seconds"`
	} `json:"running,omitempty"`
	Preceding []struct {
		BookingID              string  `json:"booking_id"`
		RunID                  string  `json:"run_id"`
		MaxRuntimeSeconds      float64 `json:"max_runtime_seconds"`
		ExpectedRuntimeSeconds float64 `json:"expected_runtime_seconds"`
	} `json:"preceding,omitempty"`
	ProjectedStartSeconds float64 `json:"projected_start_seconds"`
}

func assertScheduleEvidence(rec recordedDecision, bookings bookingNames, name, id string, expect ScheduleEvidenceExpectation) []string {
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
	if actual.Running == nil || actual.Running.BookingID != bookings[expect.Running.BookingID] || actual.Running.RunID != expect.Running.RunID ||
		actual.Running.RemainingMaxRuntimeSeconds != expect.Running.RemainingMaxRuntime.Duration().Seconds() ||
		actual.Running.RemainingExpectedRuntimeSeconds != expect.Running.expectedRemaining().Duration().Seconds() ||
		actual.Running.OverrunSeconds != durationValue(expect.Running.Overrun).Seconds() {
		fail("running Booking evidence does not match %+v", *expect.Running)
	}
	if len(actual.Preceding) != len(expect.Preceding) {
		fail("expected %d preceding Bookings, got %d", len(expect.Preceding), len(actual.Preceding))
	} else {
		for i, want := range expect.Preceding {
			got := actual.Preceding[i]
			if got.BookingID != bookings[want.BookingID] || got.RunID != want.RunID ||
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

func sortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}
