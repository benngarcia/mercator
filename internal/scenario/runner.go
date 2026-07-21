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
	// Reevaluate drives the named run's next advancement (a deferred run
	// re-entering placement, a placed run being observed).
	Reevaluate(name string) error
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
		case step.Reevaluate != "":
			if err := session.Reevaluate(step.Reevaluate); err != nil {
				failures = append(failures, fmt.Sprintf("step %d: reevaluate %q: %v", i+1, step.Reevaluate, err))
			}
			failures = append(failures, assertExpect(session, start, step.Reevaluate, *step.Expect)...)
		}
	}
	return Result{Failures: failures, Notes: session.Notes()}, nil
}

// recordedDecision is the latest placement decision in a run's stream, both
// decoded and raw. The raw form is where target-contract fields (defer,
// cache evidence) are asserted before the domain types carry them.
type recordedDecision struct {
	decision domain.PlacementDecision
	raw      map[string]json.RawMessage
}

func latestDecision(events []eventlog.StoredEvent) (recordedDecision, bool) {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type != orchestrator.EventPlacementDecided {
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

// deferRecord is the target contract for a recorded deferral, asserted
// against the decision's raw JSON until the domain type exists: a decision
// with no selected offer carrying {"defer": {"reason", "deadline"}}.
type deferRecord struct {
	Reason   string    `json:"reason"`
	Deadline time.Time `json:"deadline"`
}

func (rec recordedDecision) deferral() (deferRecord, bool) {
	raw, ok := rec.raw["defer"]
	if !ok {
		return deferRecord{}, false
	}
	var d deferRecord
	if err := json.Unmarshal(raw, &d); err != nil {
		return deferRecord{}, false
	}
	return d, true
}

func (rec recordedDecision) outcome() string {
	if rec.decision.SelectedOfferSnapshotID != "" {
		return "place"
	}
	if _, ok := rec.deferral(); ok {
		return "defer"
	}
	return "fail"
}

func (rec recordedDecision) describe() string {
	switch rec.outcome() {
	case "place":
		return fmt.Sprintf("placed on %q", rec.decision.SelectedOfferSnapshotID)
	case "defer":
		d, _ := rec.deferral()
		return fmt.Sprintf("deferred (%s until %s)", d.Reason, d.Deadline.Format(time.RFC3339))
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
		return []string{fmt.Sprintf("run %q: no placement decision recorded", name)}
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
	if expect.Defer != nil {
		if d, ok := rec.deferral(); ok {
			if d.Reason != expect.Defer.Reason {
				fail("expected defer reason %q, got %q", expect.Defer.Reason, d.Reason)
			}
			if want := expect.Defer.Deadline.Resolve(start); !d.Deadline.Equal(want) {
				fail("expected defer deadline %s, got %s", want.Format(time.RFC3339), d.Deadline.Format(time.RFC3339))
			}
		} else {
			fail("expected a recorded deferral, but the decision %s", rec.describe())
		}
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
	for _, key := range sortedKeys(expect.Caches) {
		hit, ok := candidateCacheEvidence(rec, id, key)
		if !ok {
			fail("records no cache evidence for %q", key)
			continue
		}
		if want := expect.Caches[key] == "hit"; hit != want {
			fail("cache %q: expected %s, recorded %s", key, expect.Caches[key], hitOrMiss(hit))
		}
	}
	return failures
}

// candidateCacheEvidence reads the target contract for named-cache evidence
// from the decision's raw JSON: each candidate carries
// {"cache_evidence": [{"key", "hit"}]} once cache scoring exists.
func candidateCacheEvidence(rec recordedDecision, id, key string) (bool, bool) {
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
			Key string `json:"key"`
			Hit bool   `json:"hit"`
		}
		if err := json.Unmarshal(candidate["cache_evidence"], &evidence); err != nil {
			return false, false
		}
		for _, entry := range evidence {
			if entry.Key == key {
				return entry.Hit, true
			}
		}
	}
	return false, false
}

func findCandidate(decision domain.PlacementDecision, id string) (domain.CandidateDecision, bool) {
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
