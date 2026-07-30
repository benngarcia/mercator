package lab

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/benngarcia/mercator/internal/scenario"
)

type WorldTapeSchema string

const WorldTapeSchemaV2 WorldTapeSchema = "mercator.lab/world-tape.v2"

const EventRunArrived = "world.run.arrived.v1"

type WorldTape struct {
	Schema        WorldTapeSchema      `json:"schema"`
	BlueprintName string               `json:"blueprint_name"`
	Seed          string               `json:"seed"`
	Start         time.Time            `json:"start"`
	InitialWorld  scenario.WorldSpec   `json:"initial_world"`
	Faults        []scenario.FaultSpec `json:"faults,omitempty"`
	Events        []WorldEvent         `json:"events"`
}

type WorldEvent struct {
	ID       string          `json:"id"`
	At       time.Time       `json:"at"`
	Sequence uint64          `json:"sequence"`
	Kind     string          `json:"kind"`
	Data     json.RawMessage `json:"data"`
}

type RunArrival struct {
	Name          string               `json:"name"`
	Request       scenario.RequestSpec `json:"request"`
	ActualRuntime scenario.Duration    `json:"actual_runtime"`
	Policy        string               `json:"-"`
}

func (tape WorldTape) Validate() error {
	if tape.Schema != WorldTapeSchemaV2 {
		return fmt.Errorf("unsupported World Tape schema %q", tape.Schema)
	}
	if tape.BlueprintName == "" || tape.Seed == "" || tape.Start.IsZero() {
		return fmt.Errorf("World Tape Blueprint name, seed, and start are required")
	}
	ids := map[string]bool{}
	var previous WorldEvent
	for index, event := range tape.Events {
		if event.ID == "" || event.Kind == "" || event.Sequence == 0 || len(event.Data) == 0 {
			return fmt.Errorf("World Tape event %d needs id, kind, sequence, and data", index)
		}
		if !json.Valid(event.Data) {
			return fmt.Errorf("World Tape event %q data is not JSON", event.ID)
		}
		if event.Kind == EventRunArrived {
			var arrival RunArrival
			if err := json.Unmarshal(event.Data, &arrival); err != nil {
				return fmt.Errorf("decode World Tape Run arrival %q: %w", event.ID, err)
			}
			if arrival.Name == "" || arrival.ActualRuntime.Duration() <= 0 {
				return fmt.Errorf("World Tape Run arrival %q needs a name and positive actual runtime", event.ID)
			}
		}
		if ids[event.ID] {
			return fmt.Errorf("duplicate World Tape event %q", event.ID)
		}
		ids[event.ID] = true
		if event.At.Before(tape.Start) {
			return fmt.Errorf("World Tape event %q occurs before start", event.ID)
		}
		if index > 0 && (event.At.Before(previous.At) || event.Sequence <= previous.Sequence) {
			return fmt.Errorf("World Tape events are not ordered by time and sequence")
		}
		previous = event
	}
	return nil
}

func (tape WorldTape) SHA256() string {
	encoded, _ := json.Marshal(tape)
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (tape WorldTape) clone() (WorldTape, error) {
	encoded, err := json.Marshal(tape)
	if err != nil {
		return WorldTape{}, fmt.Errorf("clone World Tape: %w", err)
	}
	var cloned WorldTape
	if err := json.Unmarshal(encoded, &cloned); err != nil {
		return WorldTape{}, fmt.Errorf("clone World Tape: %w", err)
	}
	return cloned, nil
}
