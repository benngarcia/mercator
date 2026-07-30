package scenario

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// BlueprintSchema identifies one public Scenario Blueprint contract.
type BlueprintSchema string

const (
	// BlueprintSchemaV1 is the first Mercator Lab Blueprint contract.
	BlueprintSchemaV1 BlueprintSchema = "mercator.lab/blueprint.v1"
)

// Classification controls how the canonical catalog treats a Blueprint.
type Classification string

const (
	ClassificationGreen  Classification = "green"
	ClassificationTarget Classification = "target"
)

// Kind names how a Blueprint entered or is used by the catalog.
type Kind string

const (
	KindRegression  Kind = "regression"
	KindGenerated   Kind = "generated"
	KindMinimized   Kind = "minimized"
	KindDemo        Kind = "demo"
	KindConformance Kind = "conformance"
)

var knownKinds = map[Kind]bool{
	KindRegression:  true,
	KindGenerated:   true,
	KindMinimized:   true,
	KindDemo:        true,
	KindConformance: true,
}

// Blueprint is the canonical catalog representation.
type Blueprint struct {
	Schema              BlueprintSchema   `json:"schema"`
	Name                string            `json:"-"`
	Summary             string            `json:"summary"`
	Classification      Classification    `json:"classification"`
	Kind                Kind              `json:"kind,omitempty"`
	MissingCapabilities []Capability      `json:"missing_capabilities,omitempty"`
	Seed                string            `json:"seed,omitempty"`
	World               WorldSpec         `json:"world"`
	Request             *RequestSpec      `json:"request,omitempty"`
	Expect              *ExpectSpec       `json:"expect,omitempty"`
	Timeline            []StepSpec        `json:"timeline,omitempty"`
	Arrivals            *ArrivalPlan      `json:"arrivals,omitempty"`
	Faults              []FaultSpec       `json:"faults,omitempty"`
	Proof               []ProofCheckpoint `json:"proof,omitempty"`
}

// LoadBlueprint reads Blueprint v1 or adapts one current placement fixture
// when the document has no schema.
func LoadBlueprint(path string) (Blueprint, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Blueprint{}, err
	}
	var header struct {
		Schema BlueprintSchema `json:"schema"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return Blueprint{}, fmt.Errorf("%s: %w", path, err)
	}
	if header.Schema == "" {
		data, err = adaptLegacyBlueprint(data)
		if err != nil {
			return Blueprint{}, fmt.Errorf("%s: adapt legacy placement fixture: %w", path, err)
		}
	} else if header.Schema != BlueprintSchemaV1 {
		return Blueprint{}, fmt.Errorf("%s: unsupported Blueprint schema %q", path, header.Schema)
	}
	return decodeBlueprintV1(path, data)
}

func decodeBlueprintV1(path string, data []byte) (Blueprint, error) {
	var blueprint Blueprint
	if err := strictUnmarshal(data, &blueprint); err != nil {
		return Blueprint{}, fmt.Errorf("%s: %w", path, err)
	}
	blueprint.Name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if blueprint.Kind == "" {
		blueprint.Kind = KindRegression
	}
	if !knownKinds[blueprint.Kind] {
		return Blueprint{}, fmt.Errorf("%s: unknown Blueprint kind %q", path, blueprint.Kind)
	}
	if err := blueprint.validate(); err != nil {
		return Blueprint{}, fmt.Errorf("%s: %w", path, err)
	}
	return blueprint, nil
}

func (blueprint Blueprint) validate() error {
	hasPlacement := blueprint.Request != nil || blueprint.Expect != nil || len(blueprint.Timeline) > 0
	hasLabPlan := blueprint.Arrivals != nil
	if hasPlacement == hasLabPlan {
		return fmt.Errorf("a Blueprint needs exactly one placement fixture or arrival plan")
	}
	if hasPlacement {
		scenario, _ := blueprint.PlacementScenario()
		return scenario.validate()
	}
	if err := validateClassification(Status(blueprint.Classification), blueprint.MissingCapabilities); err != nil {
		return err
	}
	if blueprint.Summary == "" {
		return fmt.Errorf("summary is required")
	}
	if err := blueprint.World.validate(); err != nil {
		return err
	}
	if blueprint.Seed == "" {
		return fmt.Errorf("arrival-driven Blueprints need a seed")
	}
	if err := blueprint.Arrivals.validate(blueprint.World); err != nil {
		return err
	}
	runs, err := blueprint.Arrivals.ExpandedRuns()
	if err != nil {
		return err
	}
	runNames := blueprint.Arrivals.runNames()
	for _, model := range blueprint.World.RuntimeModels {
		if model.Run != "" && !runNames[model.Run] {
			return fmt.Errorf("runtime model references unknown Run %q", model.Run)
		}
		for _, run := range runs {
			if model.Run != "" && model.Run != run.Name {
				continue
			}
			if run.Request.MaxRuntime != nil && model.Maximum.Duration() > run.Request.MaxRuntime.Duration() {
				return fmt.Errorf(
					"runtime model for Run %q candidate %q exceeds max_runtime",
					run.Name,
					model.Candidate,
				)
			}
		}
	}
	if err := validateFaults(blueprint.Faults, runNames); err != nil {
		return err
	}
	if blueprint.Kind == KindDemo && len(blueprint.Proof) == 0 {
		return fmt.Errorf("demo Blueprints need proof checkpoints")
	}
	return validateProof(blueprint.Proof)
}

// PlacementScenario adapts a placement Blueprint to the existing runner seam.
func (blueprint Blueprint) PlacementScenario() (Scenario, bool) {
	if blueprint.Request == nil && blueprint.Expect == nil && len(blueprint.Timeline) == 0 {
		return Scenario{}, false
	}
	return Scenario{
		Name:                blueprint.Name,
		Summary:             blueprint.Summary,
		Status:              Status(blueprint.Classification),
		MissingCapabilities: slices.Clone(blueprint.MissingCapabilities),
		World:               blueprint.World,
		Request:             blueprint.Request,
		Expect:              blueprint.Expect,
		Timeline:            slices.Clone(blueprint.Timeline),
	}, true
}
