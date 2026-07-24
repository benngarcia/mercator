package lab

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/scenario"
)

func TestExecutionOrdersEventsByVirtualTimeAndSequence(t *testing.T) {
	execution := openExecution(t, tapeWithEvents(
		worldEvent("event-a", time.Second, 1, "first"),
		worldEvent("event-b", time.Second, 2, "second"),
		worldEvent("event-c", 2*time.Second, 3, "third"),
	), testLimits())

	var kinds []string
	for range 3 {
		checkpoint, err := execution.Drive(context.Background(), Step())
		if err != nil {
			t.Fatalf("step: %v", err)
		}
		kinds = append(kinds, checkpoint.LastEvent.Kind)
	}

	if got := kinds[0] + "," + kinds[1] + "," + kinds[2]; got != "first,second,third" {
		t.Fatalf("event order = %s", got)
	}
}

func TestExecutionSupportsEveryDriveMode(t *testing.T) {
	tape := tapeWithEvents(
		worldEvent("event-a", time.Minute, 1, "arrived"),
		worldEvent("event-b", 2*time.Minute, 2, "accepted"),
		worldEvent("event-c", 3*time.Minute, 3, "closed"),
	)

	t.Run("duration", func(t *testing.T) {
		execution := openExecution(t, tape, testLimits())
		checkpoint, err := execution.Drive(context.Background(), Advance(90*time.Second))
		if err != nil {
			t.Fatalf("advance: %v", err)
		}
		if checkpoint.Now != tape.Start.Add(90*time.Second) || checkpoint.Transitions != 1 {
			t.Fatalf("checkpoint = %+v", checkpoint)
		}
	})

	t.Run("event", func(t *testing.T) {
		execution := openExecution(t, tape, testLimits())
		checkpoint, err := execution.Drive(context.Background(), UntilEvent("accepted"))
		if err != nil {
			t.Fatalf("until event: %v", err)
		}
		if checkpoint.LastEvent.Kind != "accepted" || checkpoint.Transitions != 2 {
			t.Fatalf("checkpoint = %+v", checkpoint)
		}
	})

	t.Run("predicate", func(t *testing.T) {
		execution := openExecution(t, tape, testLimits())
		checkpoint, err := execution.Drive(context.Background(), Until(func(checkpoint Checkpoint) bool {
			return checkpoint.Transitions == 2
		}))
		if err != nil {
			t.Fatalf("until predicate: %v", err)
		}
		if checkpoint.LastEvent.Kind != "accepted" {
			t.Fatalf("checkpoint = %+v", checkpoint)
		}
	})

	t.Run("quiescence", func(t *testing.T) {
		execution := openExecution(t, tape, testLimits())
		checkpoint, err := execution.Drive(context.Background(), Quiesce())
		if err != nil {
			t.Fatalf("quiesce: %v", err)
		}
		if !checkpoint.Quiescent || checkpoint.Transitions != 3 {
			t.Fatalf("checkpoint = %+v", checkpoint)
		}
	})
}

func TestExecutionFailsLoudlyAtEveryLimit(t *testing.T) {
	cases := []struct {
		name   string
		tape   WorldTape
		limits Limits
		want   error
	}{
		{
			name: "transitions",
			tape: tapeWithEvents(
				worldEvent("event-a", time.Second, 1, "a"),
				worldEvent("event-b", 2*time.Second, 2, "b"),
			),
			limits: Limits{MaxTransitions: 1, MaxVirtualDuration: time.Hour, MaxSameTimestamp: 10, MaxRepeatedEvent: 10},
			want:   ErrTransitionLimit,
		},
		{
			name:   "virtual duration",
			tape:   tapeWithEvents(worldEvent("event-a", 2*time.Hour, 1, "a")),
			limits: Limits{MaxTransitions: 10, MaxVirtualDuration: time.Hour, MaxSameTimestamp: 10, MaxRepeatedEvent: 10},
			want:   ErrVirtualTimeLimit,
		},
		{
			name: "same timestamp",
			tape: tapeWithEvents(
				worldEvent("event-a", time.Second, 1, "a"),
				worldEvent("event-b", time.Second, 2, "b"),
			),
			limits: Limits{MaxTransitions: 10, MaxVirtualDuration: time.Hour, MaxSameTimestamp: 1, MaxRepeatedEvent: 10},
			want:   ErrSameTimestampLimit,
		},
		{
			name: "livelock",
			tape: tapeWithEvents(
				worldEvent("event-a", time.Second, 1, "repeat"),
				worldEvent("event-b", time.Second, 2, "repeat"),
			),
			limits: Limits{MaxTransitions: 10, MaxVirtualDuration: time.Hour, MaxSameTimestamp: 10, MaxRepeatedEvent: 1},
			want:   ErrLivelock,
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			execution := openExecution(t, test.tape, test.limits)
			_, err := execution.Drive(context.Background(), Quiesce())
			if !errors.Is(err, test.want) {
				t.Fatalf("drive error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestTwoPoliciesConsumeTheSameWorldTape(t *testing.T) {
	tape := tapeWithEvents(
		worldEvent("event-a", time.Second, 1, "arrival"),
		worldEvent("event-b", 2*time.Second, 2, "market-change"),
	)
	first := openExecutionWithPolicy(t, tape, testLimits(), "policy-a")
	second := openExecutionWithPolicy(t, tape, testLimits(), "policy-b")

	firstCheckpoint, err := first.Drive(context.Background(), Quiesce())
	if err != nil {
		t.Fatalf("drive first policy: %v", err)
	}
	secondCheckpoint, err := second.Drive(context.Background(), Quiesce())
	if err != nil {
		t.Fatalf("drive second policy: %v", err)
	}

	if firstCheckpoint.WorldHash != secondCheckpoint.WorldHash {
		t.Fatalf("world hashes differ: %s vs %s", firstCheckpoint.WorldHash, secondCheckpoint.WorldHash)
	}
}

func TestOpenOwnsAnImmutableCopyOfItsInputs(t *testing.T) {
	tape := tapeWithEvents(worldEvent("event-a", time.Second, 1, "arrival"))
	tape.InitialWorld.Images = map[string]scenario.ImageSpec{
		"worker": {Layers: []scenario.LayerSpec{{Digest: "sha256:original", Size: 1}}},
	}
	expectedHash := tape.SHA256()
	execution := openExecution(t, tape, testLimits())

	tape.Events[0].Data[0] = '['
	tape.InitialWorld.Images["worker"].Layers[0].Digest = "sha256:mutated"

	checkpoint, err := execution.Drive(context.Background(), Step())
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	if string(checkpoint.LastEvent.Data) != `{"fixture":true}` {
		t.Fatalf("event data = %s", checkpoint.LastEvent.Data)
	}
	if checkpoint.WorldHash != expectedHash {
		t.Fatalf("world hash = %s, want %s", checkpoint.WorldHash, expectedHash)
	}
}

func TestRepeatedEventsAtDifferentVirtualTimesAreNotLivelock(t *testing.T) {
	limits := testLimits()
	limits.MaxRepeatedEvent = 1
	execution := openExecution(t, tapeWithEvents(
		worldEvent("event-a", time.Second, 1, "heartbeat"),
		worldEvent("event-b", 2*time.Second, 2, "heartbeat"),
	), limits)

	checkpoint, err := execution.Drive(context.Background(), Quiesce())
	if err != nil {
		t.Fatalf("quiesce: %v", err)
	}
	if checkpoint.Transitions != 2 {
		t.Fatalf("transitions = %d, want 2", checkpoint.Transitions)
	}
}

func TestDriveHonorsAnAlreadyCanceledContext(t *testing.T) {
	execution := openExecution(t, tapeWithEvents(
		worldEvent("event-a", time.Second, 1, "arrival"),
	), testLimits())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	checkpoint, err := execution.Drive(ctx, Quiesce())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("drive error = %v, want context canceled", err)
	}
	if checkpoint.Transitions != 0 {
		t.Fatalf("transitions = %d, want 0", checkpoint.Transitions)
	}
}

func openExecution(t *testing.T, tape WorldTape, limits Limits) *Execution {
	t.Helper()
	return openExecutionWithPolicy(t, tape, limits, "policy:test")
}

func openExecutionWithPolicy(t *testing.T, tape WorldTape, limits Limits, policy string) *Execution {
	t.Helper()
	execution, err := Open(context.Background(), Config{
		Tape:             tape,
		Limits:           limits,
		Policy:           policy,
		MercatorRevision: "revision:test",
	})
	if err != nil {
		t.Fatalf("open execution: %v", err)
	}
	return execution
}

func testLimits() Limits {
	return Limits{
		MaxTransitions:     100,
		MaxVirtualDuration: 24 * time.Hour,
		MaxSameTimestamp:   20,
		MaxRepeatedEvent:   10,
	}
}

func tapeWithEvents(events ...WorldEvent) WorldTape {
	return WorldTape{
		Schema:        WorldTapeSchemaV1,
		BlueprintName: "fixture",
		Seed:          "seed:test",
		Start:         time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC),
		Events:        events,
	}
}

func worldEvent(id string, after time.Duration, sequence uint64, kind string) WorldEvent {
	return WorldEvent{
		ID:       id,
		At:       time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC).Add(after),
		Sequence: sequence,
		Kind:     kind,
		Data:     []byte(`{"fixture":true}`),
	}
}
