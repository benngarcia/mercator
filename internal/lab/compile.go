package lab

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/benngarcia/mercator/internal/scenario"
)

type CompileOptions struct {
	Seed string
}

type Sample struct {
	Key          string          `json:"key"`
	Distribution string          `json:"distribution"`
	Value        json.RawMessage `json:"value"`
}

func Compile(blueprint scenario.Blueprint, options CompileOptions) (WorldTape, []Sample, error) {
	if blueprint.Schema != scenario.BlueprintSchemaV1 {
		return WorldTape{}, nil, fmt.Errorf("unsupported Blueprint schema %q", blueprint.Schema)
	}
	if blueprint.Arrivals == nil {
		return WorldTape{}, nil, fmt.Errorf("Lab compilation requires an arrival-driven Blueprint")
	}
	seed := options.Seed
	if seed == "" {
		seed = blueprint.Seed
	}
	if seed == "" {
		return WorldTape{}, nil, fmt.Errorf("Lab compilation seed is required")
	}
	start := blueprint.World.Start()
	entropy, err := NewEntropy(seed)
	if err != nil {
		return WorldTape{}, nil, err
	}
	events := make([]WorldEvent, 0, len(blueprint.Arrivals.Runs))
	samples := make([]Sample, 0, len(blueprint.Arrivals.Runs))
	for index, arrival := range blueprint.Arrivals.Runs {
		actualRuntime, sample, err := sampleActualRuntime(entropy, arrival)
		if err != nil {
			return WorldTape{}, nil, err
		}
		samples = append(samples, sample)
		data, err := json.Marshal(RunArrival{
			Name:          arrival.Name,
			Request:       arrival.Request,
			ActualRuntime: scenario.Duration(actualRuntime),
		})
		if err != nil {
			return WorldTape{}, nil, fmt.Errorf("encode Run arrival %q: %w", arrival.Name, err)
		}
		events = append(events, WorldEvent{
			ID:       DeterministicID(seed, "event", "run-arrival/"+arrival.Name),
			At:       start.Add(arrival.At.Duration()),
			Sequence: uint64(index + 1),
			Kind:     EventRunArrived,
			Data:     data,
		})
	}
	sort.Slice(events, func(i, j int) bool {
		if events[i].At.Equal(events[j].At) {
			return events[i].Sequence < events[j].Sequence
		}
		return events[i].At.Before(events[j].At)
	})
	for index := range events {
		events[index].Sequence = uint64(index + 1)
	}
	tape := WorldTape{
		Schema:        WorldTapeSchemaV2,
		BlueprintName: blueprint.Name,
		Seed:          seed,
		Start:         start,
		InitialWorld:  blueprint.World,
		Faults:        blueprint.Faults,
		Events:        events,
	}
	if err := tape.Validate(); err != nil {
		return WorldTape{}, nil, err
	}
	return tape, samples, nil
}

func sampleActualRuntime(entropy Entropy, arrival scenario.RunArrivalSpec) (time.Duration, Sample, error) {
	key := "world.actual-runtime/run/" + arrival.Name
	draw, err := entropy.Uint64(key)
	if err != nil {
		return 0, Sample{}, err
	}
	maximum := time.Hour
	if arrival.Request.MaxRuntime != nil {
		maximum = arrival.Request.MaxRuntime.Duration()
	}
	maximumNanos := uint64(max(maximum, time.Nanosecond))
	actual := time.Duration(1 + draw%maximumNanos)
	encoded, err := json.Marshal(actual.String())
	if err != nil {
		return 0, Sample{}, fmt.Errorf("encode actual runtime sample for %q: %w", arrival.Name, err)
	}
	return actual, Sample{
		Key:          key,
		Distribution: "uniform_duration_1ns_to_max_runtime",
		Value:        encoded,
	}, nil
}
