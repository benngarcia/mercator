package scenario

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
	"github.com/benngarcia/mercator/internal/orchestrator"
)

// TestAReclaimIsReadFromTheOrderOfTheRecord is the reclaim expectation read from
// both sides. Every other expectation in this corpus is exercised both ways by the
// fixtures themselves, because some green world produces the fact and some target
// world does not. This one cannot be: a Run's cleanup is the last thing that
// happens to it, so no world in the corpus hands capacity back and then decides
// again, and an expectation that could only ever fail would be the same defect as
// one that could only ever pass.
//
// The three streams are the three answers a control plane can give about a machine
// it stopped waiting for: it ended the bill before moving the work, it moved the
// work and left the machine running, and it ended the workload on a machine it
// owns without destroying the machine.
func TestAReclaimIsReadFromTheOrderOfTheRecord(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		events []eventlog.StoredEvent
		want   string
	}{
		{
			name: "the machine is handed back before the work moves",
			events: recordedRun(
				launchIntent("launch-1", "silent-4090", domain.DispositionTerminate),
				bookingDecided("dec_1", "silent-4090"),
				cleanupConfirmed("launch-1", domain.DispositionTerminate),
				bookingDecided("dec_2", "patient-4090"),
			),
		},
		{
			name: "the work moves and the machine is still running",
			events: recordedRun(
				launchIntent("launch-1", "silent-4090", domain.DispositionTerminate),
				bookingDecided("dec_1", "silent-4090"),
				bookingDecided("dec_2", "patient-4090"),
				cleanupConfirmed("launch-1", domain.DispositionTerminate),
			),
			want: `expected the capacity on "silent-4090" to be handed back before this decision`,
		},
		{
			name: "only the workload was ended on a machine Mercator owns",
			events: recordedRun(
				launchIntent("launch-1", "silent-4090", domain.DispositionTerminate),
				bookingDecided("dec_1", "silent-4090"),
				cleanupConfirmed("launch-1", domain.DispositionRelease),
				bookingDecided("dec_2", "patient-4090"),
			),
			want: `expected "silent-4090" to be handed back as "terminate", and the record confirms "release"`,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			result, err := Run(replayedRun{events: testCase.events}, reclaimScenario())

			if err != nil {
				t.Fatalf("run scenario: %v", err)
			}
			if testCase.want == "" {
				if len(result.Failures) > 0 {
					t.Fatalf("the record holds the reclaim and the runner reports %v", result.Failures)
				}
				return
			}
			if len(result.Failures) != 1 || !strings.Contains(result.Failures[0], testCase.want) {
				t.Fatalf("the runner reports %v, want one failure naming %q", result.Failures, testCase.want)
			}
		})
	}
}

// reclaimScenario is one Run whose answer changed, asserting only what this test is
// about: the work is on the second machine and the first was terminated before it
// moved.
func reclaimScenario() Scenario {
	return Scenario{
		Name:    "a-reclaim-is-read-from-the-order-of-the-record",
		Summary: "capacity handed back before the answer changed",
		Status:  StatusGreen,
		Timeline: []StepSpec{{
			Reconcile: "moved",
			Expect: &ExpectSpec{
				Outcome:   OutcomePlace,
				Offer:     "patient-4090",
				Reclaimed: &ReclaimExpectation{Offer: "silent-4090", Disposition: domain.DispositionTerminate},
			},
		}},
	}
}

func launchIntent(launchKey, offerID string, disposition domain.Disposition) eventlog.StoredEvent {
	return storedEvent(orchestrator.EventLaunchIntentRecorded, map[string]any{
		"launch_key":                 launchKey,
		"selected_offer_snapshot_id": offerID,
		"disposition":                disposition,
	})
}

func cleanupConfirmed(launchKey string, disposition domain.Disposition) eventlog.StoredEvent {
	return storedEvent(orchestrator.EventCleanupConfirmed, map[string]any{
		"launch_key":  launchKey,
		"disposition": disposition,
	})
}

func bookingDecided(decisionID, offerID string) eventlog.StoredEvent {
	return storedEvent(orchestrator.EventBookingDecided, map[string]any{
		"decision": map[string]any{
			"id":                         decisionID,
			"run_id":                     "run-moved",
			"selected_offer_snapshot_id": offerID,
			"selection_reason_codes":     []string{"CHEAPEST_FEASIBLE"},
		},
	})
}

func storedEvent(eventType string, data map[string]any) eventlog.StoredEvent {
	encoded, err := json.Marshal(data)
	if err != nil {
		panic(err)
	}
	return eventlog.StoredEvent{Type: eventType, Data: encoded}
}

func recordedRun(events ...eventlog.StoredEvent) []eventlog.StoredEvent {
	for i := range events {
		events[i].StreamVersion = uint64(i + 1)
	}
	return events
}

// replayedRun is a backend that hands the runner a stream Mercator could have
// recorded. It reads no world and drives nothing: what it stands in for is the
// world a corpus fixture would need, and the reason it is here rather than in
// scenarios/ is that the fact under test is one no world in the corpus produces.
type replayedRun struct {
	events []eventlog.StoredEvent
}

func (r replayedRun) StartWorld(WorldSpec) (Session, error) { return r, nil }

func (r replayedRun) Submit(string, RequestSpec) error { return nil }

func (r replayedRun) Reconcile(string) error { return nil }

func (r replayedRun) AdvanceClock(time.Duration) error { return nil }

func (r replayedRun) RunEvents(string) ([]eventlog.StoredEvent, error) { return r.events, nil }

func (r replayedRun) RunRecord(string) (domain.RunRecord, error) { return domain.RunRecord{}, nil }

func (r replayedRun) Notes() []string { return nil }

func (r replayedRun) Close() {}
