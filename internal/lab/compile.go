package lab

import (
	"encoding/json"
	"fmt"
	"sort"

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
	events := make([]WorldEvent, 0, len(blueprint.Arrivals.Runs))
	for _, arrival := range blueprint.Arrivals.Runs {
		data, err := json.Marshal(RunArrival{Name: arrival.Name, Request: arrival.Request})
		if err != nil {
			return WorldTape{}, nil, fmt.Errorf("encode Run arrival %q: %w", arrival.Name, err)
		}
		events = append(events, WorldEvent{
			ID:   DeterministicID(seed, "event", "run-arrival/"+arrival.Name),
			At:   start.Add(arrival.At.Duration()),
			Kind: EventRunArrived,
			Data: data,
		})
	}
	sort.Slice(events, func(i, j int) bool {
		if events[i].At.Equal(events[j].At) {
			return events[i].ID < events[j].ID
		}
		return events[i].At.Before(events[j].At)
	})
	for index := range events {
		events[index].Sequence = uint64(index + 1)
	}
	tape := WorldTape{
		Schema:        WorldTapeSchemaV1,
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
	return tape, nil, nil
}
