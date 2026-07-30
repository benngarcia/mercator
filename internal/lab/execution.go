package lab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/benngarcia/mercator/internal/scenario"
)

var (
	ErrTransitionLimit    = errors.New("Lab transition limit exceeded")
	ErrVirtualTimeLimit   = errors.New("Lab virtual-time limit exceeded")
	ErrSameTimestampLimit = errors.New("Lab same-timestamp limit exceeded")
	ErrLivelock           = errors.New("Lab repeated-transition livelock")
	ErrConditionUnmet     = errors.New("Lab drive condition was not met")
)

type Limits struct {
	MaxTransitions     uint64        `json:"max_transitions"`
	MaxVirtualDuration time.Duration `json:"max_virtual_duration"`
	MaxSameTimestamp   uint64        `json:"max_same_timestamp"`
	MaxRepeatedEvent   uint64        `json:"max_repeated_event"`
}

func (limits Limits) validate() error {
	if limits.MaxTransitions == 0 ||
		limits.MaxVirtualDuration <= 0 ||
		limits.MaxSameTimestamp == 0 ||
		limits.MaxRepeatedEvent == 0 {
		return fmt.Errorf("all Lab execution limits are required and positive")
	}
	return nil
}

type Config struct {
	Blueprint        scenario.Blueprint `json:"-"`
	Tape             WorldTape          `json:"-"`
	Samples          []Sample           `json:"-"`
	Limits           Limits             `json:"limits"`
	Policy           string             `json:"policy"`
	MercatorRevision string             `json:"mercator_revision"`
}

type driveKind uint8

const (
	driveStep driveKind = iota + 1
	driveDuration
	driveEvent
	drivePredicate
	driveQuiescence
)

type DriveCommand struct {
	kind      driveKind
	duration  time.Duration
	eventKind string
	predicate func(Checkpoint) bool
}

func Step() DriveCommand { return DriveCommand{kind: driveStep} }

func Advance(duration time.Duration) DriveCommand {
	return DriveCommand{kind: driveDuration, duration: duration}
}

func UntilEvent(kind string) DriveCommand {
	return DriveCommand{kind: driveEvent, eventKind: kind}
}

func Until(predicate func(Checkpoint) bool) DriveCommand {
	return DriveCommand{kind: drivePredicate, predicate: predicate}
}

func Quiesce() DriveCommand { return DriveCommand{kind: driveQuiescence} }

type Checkpoint struct {
	Now         time.Time
	Transitions uint64
	LastEvent   *WorldEvent
	Quiescent   bool
	WorldHash   string
}

type Execution struct {
	config Config
	queue  []WorldEvent

	now         time.Time
	transitions uint64
	processed   []WorldEvent
	lastEvent   *WorldEvent

	sameTimestamp       time.Time
	sameTimestampCount  uint64
	repeatedAt          time.Time
	repeatedFingerprint []byte
	repeatedCount       uint64
}

func Open(ctx context.Context, config Config) (*Execution, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := config.Tape.Validate(); err != nil {
		return nil, err
	}
	if err := config.Limits.validate(); err != nil {
		return nil, err
	}
	if config.Policy == "" || config.MercatorRevision == "" {
		return nil, fmt.Errorf("Lab policy and Mercator revision are required")
	}
	tape, err := config.Tape.clone()
	if err != nil {
		return nil, err
	}
	blueprint, err := cloneBlueprint(config.Blueprint)
	if err != nil {
		return nil, err
	}
	config.Blueprint = blueprint
	config.Tape = tape
	config.Samples = cloneSamples(config.Samples)
	return &Execution{
		config: config,
		queue:  slices.Clone(config.Tape.Events),
		now:    config.Tape.Start,
	}, nil
}

func (execution *Execution) Drive(ctx context.Context, command DriveCommand) (Checkpoint, error) {
	if err := ctx.Err(); err != nil {
		return execution.checkpoint(), err
	}
	switch command.kind {
	case driveStep:
		if len(execution.queue) == 0 {
			return execution.checkpoint(), nil
		}
		if err := execution.transition(ctx); err != nil {
			return execution.checkpoint(), err
		}
		return execution.checkpoint(), nil
	case driveDuration:
		if command.duration <= 0 {
			return execution.checkpoint(), fmt.Errorf("Lab advance duration must be positive")
		}
		target := execution.now.Add(command.duration)
		if target.Sub(execution.config.Tape.Start) > execution.config.Limits.MaxVirtualDuration {
			return execution.checkpoint(), fmt.Errorf(
				"%w: target %s exceeds %s",
				ErrVirtualTimeLimit,
				target.Sub(execution.config.Tape.Start),
				execution.config.Limits.MaxVirtualDuration,
			)
		}
		for len(execution.queue) > 0 && !execution.queue[0].At.After(target) {
			if err := execution.transition(ctx); err != nil {
				return execution.checkpoint(), err
			}
		}
		execution.now = target
		return execution.checkpoint(), nil
	case driveEvent:
		if command.eventKind == "" {
			return execution.checkpoint(), fmt.Errorf("Lab event drive needs an event kind")
		}
		for len(execution.queue) > 0 {
			if err := execution.transition(ctx); err != nil {
				return execution.checkpoint(), err
			}
			if execution.lastEvent.Kind == command.eventKind {
				return execution.checkpoint(), nil
			}
		}
		return execution.checkpoint(), ErrConditionUnmet
	case drivePredicate:
		if command.predicate == nil {
			return execution.checkpoint(), fmt.Errorf("Lab predicate drive needs a predicate")
		}
		if command.predicate(execution.checkpoint()) {
			return execution.checkpoint(), nil
		}
		for len(execution.queue) > 0 {
			if err := execution.transition(ctx); err != nil {
				return execution.checkpoint(), err
			}
			checkpoint := execution.checkpoint()
			if command.predicate(checkpoint) {
				return checkpoint, nil
			}
		}
		return execution.checkpoint(), ErrConditionUnmet
	case driveQuiescence:
		for len(execution.queue) > 0 {
			if err := execution.transition(ctx); err != nil {
				return execution.checkpoint(), err
			}
		}
		return execution.checkpoint(), nil
	default:
		return execution.checkpoint(), fmt.Errorf("unknown Lab drive command")
	}
}

func (execution *Execution) transition(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	event := execution.queue[0]
	if execution.transitions+1 > execution.config.Limits.MaxTransitions {
		return fmt.Errorf(
			"%w: transition %d exceeds %d",
			ErrTransitionLimit,
			execution.transitions+1,
			execution.config.Limits.MaxTransitions,
		)
	}
	if event.At.Sub(execution.config.Tape.Start) > execution.config.Limits.MaxVirtualDuration {
		return fmt.Errorf(
			"%w: event at %s exceeds %s",
			ErrVirtualTimeLimit,
			event.At.Sub(execution.config.Tape.Start),
			execution.config.Limits.MaxVirtualDuration,
		)
	}
	if event.At.Equal(execution.sameTimestamp) {
		execution.sameTimestampCount++
	} else {
		execution.sameTimestamp = event.At
		execution.sameTimestampCount = 1
	}
	if execution.sameTimestampCount > execution.config.Limits.MaxSameTimestamp {
		return fmt.Errorf(
			"%w: timestamp %s has transition %d, maximum %d",
			ErrSameTimestampLimit,
			event.At.Format(time.RFC3339Nano),
			execution.sameTimestampCount,
			execution.config.Limits.MaxSameTimestamp,
		)
	}
	fingerprint := eventFingerprint(event)
	if event.At.Equal(execution.repeatedAt) && bytes.Equal(fingerprint, execution.repeatedFingerprint) {
		execution.repeatedCount++
	} else {
		execution.repeatedAt = event.At
		execution.repeatedFingerprint = fingerprint
		execution.repeatedCount = 1
	}
	if execution.repeatedCount > execution.config.Limits.MaxRepeatedEvent {
		return fmt.Errorf(
			"%w: event %q repeated %d times at %s, maximum %d",
			ErrLivelock,
			event.Kind,
			execution.repeatedCount,
			event.At.Format(time.RFC3339Nano),
			execution.config.Limits.MaxRepeatedEvent,
		)
	}

	execution.queue = execution.queue[1:]
	execution.now = event.At
	execution.transitions++
	execution.processed = append(execution.processed, event)
	eventCopy := event
	eventCopy.Data = slices.Clone(event.Data)
	execution.lastEvent = &eventCopy
	return nil
}

func cloneBlueprint(blueprint scenario.Blueprint) (scenario.Blueprint, error) {
	encoded, err := json.Marshal(blueprint)
	if err != nil {
		return scenario.Blueprint{}, fmt.Errorf("clone Blueprint: %w", err)
	}
	var cloned scenario.Blueprint
	if err := json.Unmarshal(encoded, &cloned); err != nil {
		return scenario.Blueprint{}, fmt.Errorf("clone Blueprint: %w", err)
	}
	cloned.Name = blueprint.Name
	return cloned, nil
}

func cloneSamples(samples []Sample) []Sample {
	cloned := slices.Clone(samples)
	for index := range cloned {
		cloned[index].Value = slices.Clone(cloned[index].Value)
	}
	return cloned
}

func eventFingerprint(event WorldEvent) []byte {
	fingerprint := make([]byte, 0, len(event.Kind)+len(event.Data)+1)
	fingerprint = append(fingerprint, event.Kind...)
	fingerprint = append(fingerprint, 0)
	return append(fingerprint, event.Data...)
}

func (execution *Execution) checkpoint() Checkpoint {
	var last *WorldEvent
	if execution.lastEvent != nil {
		copy := *execution.lastEvent
		copy.Data = slices.Clone(copy.Data)
		last = &copy
	}
	return Checkpoint{
		Now:         execution.now,
		Transitions: execution.transitions,
		LastEvent:   last,
		Quiescent:   len(execution.queue) == 0,
		WorldHash:   execution.config.Tape.SHA256(),
	}
}
