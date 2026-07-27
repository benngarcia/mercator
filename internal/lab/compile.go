package lab

import (
	"encoding/json"
	"fmt"
	"slices"
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
	// A seeded Rental Schedule is Broker state, and the Lab drives the real
	// control plane against its own storage: this world builds the machines and
	// the Bookings on them belong to Runs no event log here ever saw. Nothing
	// loads them, so a Blueprint stating one would be compiled into a world with
	// every Rental idle, and the fixture would pass while asserting the opposite
	// of what it says. Refusing is the honest answer until the Lab can seed the
	// Broker; every queue in this world is made by Runs it created itself.
	if len(blueprint.World.RentalSchedules) > 0 {
		return WorldTape{}, nil, fmt.Errorf(
			"Lab compilation cannot seed Broker Rental Schedule state, and Blueprint %q states %d",
			blueprint.Name, len(blueprint.World.RentalSchedules),
		)
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
	arrivals, err := blueprint.Arrivals.ExpandedRuns()
	if err != nil {
		return WorldTape{}, nil, err
	}
	events := make([]WorldEvent, 0, len(arrivals))
	samples := make([]Sample, 0, len(arrivals))
	for index, arrival := range arrivals {
		actualRuntime, actualByOffer, runtimeSamples, err := sampleActualRuntimes(entropy, blueprint.World, arrival)
		if err != nil {
			return WorldTape{}, nil, err
		}
		samples = append(samples, runtimeSamples...)
		data, err := json.Marshal(RunArrival{
			Name:                 arrival.Name,
			Workspace:            arrival.Workspace,
			Request:              arrival.Request,
			ActualRuntime:        scenario.Duration(actualRuntime),
			ActualRuntimeByOffer: actualByOffer,
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
	withdrawals, err := cancellationEvents(seed, start, arrivals, blueprint.Arrivals.Cancellations)
	if err != nil {
		return WorldTape{}, nil, err
	}
	events = append(events, withdrawals...)
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

// cancellationEvents is every withdrawal this Blueprint declared, carrying the
// workspace its Run arrived into so the cancellation reaches the same tenant.
// The sequence is offset past every arrival so a withdrawal at the same instant
// as an arrival is ordered after it; the tape resequences everything once it is
// sorted by time.
func cancellationEvents(
	seed string,
	start time.Time,
	arrivals []scenario.RunArrivalSpec,
	cancellations []scenario.RunCancellationSpec,
) ([]WorldEvent, error) {
	workspaces := make(map[string]string, len(arrivals))
	for _, arrival := range arrivals {
		workspaces[arrival.Name] = arrival.Workspace
	}
	events := make([]WorldEvent, 0, len(cancellations))
	for index, cancellation := range cancellations {
		data, err := json.Marshal(RunCancellation{
			Name:      cancellation.Run,
			Workspace: workspaces[cancellation.Run],
		})
		if err != nil {
			return nil, fmt.Errorf("encode Run cancellation %q: %w", cancellation.Run, err)
		}
		events = append(events, WorldEvent{
			ID:       DeterministicID(seed, "event", "run-cancellation/"+cancellation.Run),
			At:       start.Add(cancellation.At.Duration()),
			Sequence: uint64(len(arrivals) + index + 1),
			Kind:     EventRunCancelled,
			Data:     data,
		})
	}
	return events, nil
}

func sampleActualRuntimes(entropy Entropy, world scenario.WorldSpec, arrival scenario.RunArrivalSpec) (time.Duration, map[string]scenario.Duration, []Sample, error) {
	actual, sample, err := sampleActualRuntime(entropy, arrival, "", time.Nanosecond, maximumRuntime(arrival))
	if err != nil {
		return 0, nil, nil, err
	}
	models := slices.Clone(world.RuntimeModels)
	sort.Slice(models, func(i, j int) bool {
		if models[i].Run == models[j].Run {
			return models[i].Candidate < models[j].Candidate
		}
		return models[i].Run < models[j].Run
	})
	byOffer := map[string]scenario.Duration{}
	samples := []Sample{sample}
	for _, model := range models {
		if model.Run != "" && model.Run != arrival.Name {
			continue
		}
		value, candidateSample, err := sampleActualRuntime(
			entropy,
			arrival,
			model.Candidate,
			model.Minimum.Duration(),
			model.Maximum.Duration(),
		)
		if err != nil {
			return 0, nil, nil, err
		}
		byOffer[model.Candidate] = scenario.Duration(value)
		samples = append(samples, candidateSample)
	}
	if len(byOffer) == 0 {
		byOffer = nil
	}
	return actual, byOffer, samples, nil
}

func sampleActualRuntime(entropy Entropy, arrival scenario.RunArrivalSpec, candidate string, minimum, maximum time.Duration) (time.Duration, Sample, error) {
	key := "world.actual-runtime/run/" + arrival.Name
	if candidate != "" {
		key += "/candidate/" + candidate
	}
	draw, err := entropy.Uint64(key)
	if err != nil {
		return 0, Sample{}, err
	}
	if minimum <= 0 || maximum < minimum {
		return 0, Sample{}, fmt.Errorf("actual runtime bounds for %q are invalid", arrival.Name)
	}
	span := uint64(maximum - minimum + 1)
	actual := minimum + time.Duration(draw%span)
	encoded, err := json.Marshal(actual.String())
	if err != nil {
		return 0, Sample{}, fmt.Errorf("encode actual runtime sample for %q: %w", arrival.Name, err)
	}
	distribution := "uniform_duration_inclusive"
	if candidate == "" && minimum == time.Nanosecond {
		distribution = "uniform_duration_1ns_to_max_runtime"
	}
	return actual, Sample{
		Key:          key,
		Distribution: distribution,
		Value:        encoded,
	}, nil
}

func maximumRuntime(arrival scenario.RunArrivalSpec) time.Duration {
	if arrival.Request.MaxRuntime != nil {
		return arrival.Request.MaxRuntime.Duration()
	}
	return time.Hour
}
