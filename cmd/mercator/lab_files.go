package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/benngarcia/mercator/internal/lab"
	"github.com/benngarcia/mercator/internal/scenario"
)

type labBlueprintOptions struct {
	output      string
	seed        string
	arrivalType string
	runs        int
	rentals     int
	offers      int
	images      int
	artifacts   int
	faults      bool
}

func runLabAuthor(args []string, stdout, stderr io.Writer) int {
	options, err := parseBlueprintOptions("author", args, false)
	if err != nil {
		return commandError(stderr, err)
	}
	options.seed = "author-template-v1"
	options.runs = 2
	options.rentals = 2
	options.offers = 2
	options.images = 2
	options.artifacts = 1
	blueprint, _, err := generateBlueprint(options)
	if err != nil {
		return commandFailure(stderr, "author Blueprint", err)
	}
	blueprint.Kind = scenario.KindRegression
	blueprint.Summary = "Describe the behavior this Blueprint proves."
	if err := writeBlueprint(options.output, blueprint); err != nil {
		return commandFailure(stderr, "write Blueprint", err)
	}
	return writeCommandResult(stdout, map[string]any{"blueprint": options.output})
}

func runLabGenerate(args []string, stdout, stderr io.Writer) int {
	options, err := parseBlueprintOptions("generate", args, true)
	if err != nil {
		return commandError(stderr, err)
	}
	blueprint, _, err := generateBlueprint(options)
	if err != nil {
		return commandFailure(stderr, "generate Blueprint", err)
	}
	if err := writeBlueprint(options.output, blueprint); err != nil {
		return commandFailure(stderr, "write Blueprint", err)
	}
	return writeCommandResult(stdout, map[string]any{
		"blueprint": options.output,
		"seed":      options.seed,
	})
}

func parseBlueprintOptions(command string, args []string, generated bool) (labBlueprintOptions, error) {
	flags := flag.NewFlagSet("mercator lab "+command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	options := labBlueprintOptions{}
	flags.StringVar(&options.output, "output", "", "output Blueprint")
	if generated {
		flags.StringVar(&options.seed, "seed", "", "semantic seed")
		flags.StringVar(&options.arrivalType, "arrival", "fixed", "fixed, periodic, or burst")
		flags.IntVar(&options.runs, "runs", 3, "Run count")
		flags.IntVar(&options.rentals, "rentals", 2, "Rental count")
		flags.IntVar(&options.offers, "offers", 2, "marketplace offer count")
		flags.IntVar(&options.images, "images", 2, "image count")
		flags.IntVar(&options.artifacts, "artifacts", 1, "Artifact count")
		flags.BoolVar(&options.faults, "faults", false, "include deterministic faults")
	}
	if err := flags.Parse(args); err != nil {
		return labBlueprintOptions{}, fmt.Errorf("mercator lab %s: %w", command, err)
	}
	if flags.NArg() != 0 {
		return labBlueprintOptions{}, fmt.Errorf("mercator lab %s: unexpected arguments", command)
	}
	if options.output == "" {
		return labBlueprintOptions{}, fmt.Errorf("mercator lab %s: --output is required", command)
	}
	if generated && options.seed == "" {
		return labBlueprintOptions{}, errors.New("mercator lab generate: --seed is required")
	}
	return options, nil
}

func generateBlueprint(options labBlueprintOptions) (scenario.Blueprint, []scenario.GenerationSample, error) {
	return scenario.GenerateBlueprint(scenario.GeneratorConfig{
		Seed:             options.seed,
		ArrivalType:      scenario.ArrivalType(options.arrivalType),
		RunCount:         options.runs,
		RentalCount:      options.rentals,
		MarketplaceCount: options.offers,
		ImageCount:       options.images,
		ArtifactCount:    options.artifacts,
		IncludeFaults:    options.faults,
	})
}

func runLabExecute(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("mercator lab run", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var blueprintPath, bundlePath, seed, policy string
	flags.StringVar(&blueprintPath, "blueprint", "", "Blueprint path")
	flags.StringVar(&bundlePath, "bundle", "", "Run Bundle path")
	flags.StringVar(&seed, "seed", "", "keyed entropy seed override")
	flags.StringVar(&policy, "policy", "default", "policy identity")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return commandError(stderr, fmt.Errorf("usage: mercator lab run --blueprint FILE --bundle FILE [--seed SEED] [--policy NAME]"))
	}
	if blueprintPath == "" || bundlePath == "" {
		return commandError(stderr, errors.New("mercator lab run: --blueprint and --bundle are required"))
	}
	blueprint, err := scenario.LoadBlueprint(blueprintPath)
	if err != nil {
		return commandFailure(stderr, "load Blueprint", err)
	}
	bundle, err := executeBlueprint(ctx, blueprint, seed, policy)
	if err != nil {
		return commandFailure(stderr, "execute Blueprint", err)
	}
	if err := writeBundle(bundlePath, bundle); err != nil {
		return commandFailure(stderr, "write Run Bundle", err)
	}
	return writeCommandResult(stdout, map[string]any{
		"bundle":            bundlePath,
		"normalized_sha256": bundle.NormalizedSHA256(),
		"replay":            "mercator lab replay --bundle " + bundlePath,
	})
}

func runLabReplay(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("mercator lab replay", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var bundlePath, output string
	flags.StringVar(&bundlePath, "bundle", "", "Run Bundle path")
	flags.StringVar(&output, "output", "", "replayed Run Bundle path")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return commandError(stderr, errors.New("usage: mercator lab replay --bundle FILE [--output FILE]"))
	}
	if bundlePath == "" {
		return commandError(stderr, errors.New("mercator lab replay: --bundle is required"))
	}
	archive, err := os.ReadFile(bundlePath)
	if err != nil {
		return commandFailure(stderr, "read Run Bundle", err)
	}
	original, err := lab.DecodeRunBundle(archive)
	if err != nil {
		return commandFailure(stderr, "decode Run Bundle", err)
	}
	execution, err := lab.Reconstruct(ctx, original)
	if err != nil {
		return commandFailure(stderr, "open replay", err)
	}
	defer execution.Close()
	replayed, err := execution.Export(ctx)
	if err != nil {
		return commandFailure(stderr, "export replay", err)
	}
	if replayed.NormalizedSHA256() != original.NormalizedSHA256() {
		return commandFailure(
			stderr,
			"verify replay",
			fmt.Errorf(
				"normalized hash %s differs from recorded %s",
				replayed.NormalizedSHA256(),
				original.NormalizedSHA256(),
			),
		)
	}
	if output != "" {
		if err := writeBundle(output, replayed); err != nil {
			return commandFailure(stderr, "write replayed Run Bundle", err)
		}
	}
	return writeCommandResult(stdout, map[string]any{
		"bundle":            bundlePath,
		"normalized_sha256": replayed.NormalizedSHA256(),
		"replayed_bundle":   output,
	})
}

func runLabMinimize(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("mercator lab minimize", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var bundlePath, output string
	flags.StringVar(&bundlePath, "bundle", "", "failing Run Bundle path")
	flags.StringVar(&output, "output", "", "minimized Blueprint path")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return commandError(stderr, errors.New("usage: mercator lab minimize --bundle FILE --output FILE"))
	}
	if bundlePath == "" || output == "" {
		return commandError(stderr, errors.New("mercator lab minimize: --bundle and --output are required"))
	}
	bundle, err := readRunBundle(bundlePath)
	if err != nil {
		return commandFailure(stderr, "read Run Bundle", err)
	}
	blueprint, err := bundle.Blueprint()
	if err != nil {
		return commandFailure(stderr, "read bundled Blueprint", err)
	}
	fingerprint, err := firstFailureFingerprint(bundle)
	if err != nil {
		return commandFailure(stderr, "identify failure fingerprint", err)
	}
	result, err := scenario.ShrinkBlueprint(ctx, blueprint, func(ctx context.Context, candidate scenario.Blueprint) (bool, error) {
		candidateBundle, err := executeBlueprint(ctx, candidate, "", "minimize")
		if err != nil {
			return false, err
		}
		effects, err := candidateBundle.Effects()
		if err != nil {
			return false, err
		}
		return fingerprint.matches(effects), nil
	})
	if err != nil {
		return commandFailure(stderr, "minimize failure", err)
	}
	result.Blueprint.Kind = scenario.KindMinimized
	if err := writeBlueprint(output, result.Blueprint); err != nil {
		return commandFailure(stderr, "write minimized Blueprint", err)
	}
	return writeCommandResult(stdout, map[string]any{
		"blueprint": output,
		"fingerprint": map[string]any{
			"operation": fingerprint.Operation,
			"command":   fingerprint.Command,
			"response":  fingerprint.Response,
			"fault_id":  fingerprint.FaultID,
		},
		"steps": len(result.Steps),
	})
}

func runLabPromote(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("mercator lab promote", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var blueprintPath, bundlePath, output string
	flags.StringVar(&blueprintPath, "blueprint", "", "target Blueprint path")
	flags.StringVar(&bundlePath, "bundle", "", "proving Run Bundle path")
	flags.StringVar(&output, "output", "", "promoted Blueprint path")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return commandError(stderr, errors.New("usage: mercator lab promote --blueprint FILE --bundle FILE --output FILE"))
	}
	if blueprintPath == "" || bundlePath == "" || output == "" {
		return commandError(stderr, errors.New("mercator lab promote: --blueprint, --bundle, and --output are required"))
	}
	blueprint, err := scenario.LoadBlueprint(blueprintPath)
	if err != nil {
		return commandFailure(stderr, "load target Blueprint", err)
	}
	bundle, err := readRunBundle(bundlePath)
	if err != nil {
		return commandFailure(stderr, "read proving Run Bundle", err)
	}
	if err := sameBlueprint(blueprint, bundle); err != nil {
		return commandFailure(stderr, "match proving Run Bundle", err)
	}
	report, err := lab.VerifyVerticalProof(ctx, bundle)
	if err != nil {
		return commandFailure(stderr, "verify promotion proof", err)
	}
	promoted, err := scenario.PromoteBlueprint(blueprint)
	if err != nil {
		return commandFailure(stderr, "promote Blueprint", err)
	}
	if err := writeBlueprint(output, promoted); err != nil {
		return commandFailure(stderr, "write promoted Blueprint", err)
	}
	return writeCommandResult(stdout, map[string]any{
		"blueprint":         output,
		"checkpoints":       len(report.Checkpoints),
		"normalized_sha256": report.NormalizedSHA256,
	})
}

type failureFingerprint struct {
	Operation string
	Command   lab.EffectCommand
	Response  lab.EffectResponse
	FaultID   string
}

func firstFailureFingerprint(bundle lab.RunBundle) (failureFingerprint, error) {
	effects, err := bundle.Effects()
	if err != nil {
		return failureFingerprint{}, err
	}
	for _, effect := range effects {
		if effect.Command == lab.EffectCommandRejected || effect.Response != lab.EffectResponseDelivered {
			return failureFingerprint{
				Operation: effect.Operation,
				Command:   effect.Command,
				Response:  effect.Response,
				FaultID:   effect.FaultID,
			}, nil
		}
	}
	return failureFingerprint{}, errors.New("Run Bundle contains no rejected or ambiguous effect")
}

func (fingerprint failureFingerprint) matches(effects []lab.EffectRecord) bool {
	for _, effect := range effects {
		if effect.Operation == fingerprint.Operation &&
			effect.Command == fingerprint.Command &&
			effect.Response == fingerprint.Response &&
			effect.FaultID == fingerprint.FaultID {
			return true
		}
	}
	return false
}

func readRunBundle(path string) (lab.RunBundle, error) {
	archive, err := os.ReadFile(path)
	if err != nil {
		return lab.RunBundle{}, err
	}
	return lab.DecodeRunBundle(archive)
}

func sameBlueprint(blueprint scenario.Blueprint, bundle lab.RunBundle) error {
	recorded, err := bundle.Blueprint()
	if err != nil {
		return err
	}
	recorded.Name = blueprint.Name
	expected, err := scenario.EncodeBlueprint(blueprint)
	if err != nil {
		return err
	}
	actual, err := scenario.EncodeBlueprint(recorded)
	if err != nil {
		return err
	}
	if !bytes.Equal(actual, expected) {
		return errors.New("Run Bundle was produced from a different Blueprint")
	}
	return nil
}

func executeBlueprint(ctx context.Context, blueprint scenario.Blueprint, seed, policy string) (lab.RunBundle, error) {
	tape, samples, err := lab.Compile(blueprint, lab.CompileOptions{Seed: seed})
	if err != nil {
		return lab.RunBundle{}, err
	}
	execution, err := lab.Open(ctx, lab.Config{
		Blueprint:        blueprint,
		Tape:             tape,
		Samples:          samples,
		Limits:           lab.DefaultLimits(),
		Policy:           policy,
		MercatorRevision: currentRevision(),
	})
	if err != nil {
		return lab.RunBundle{}, err
	}
	defer execution.Close()
	if _, err := execution.DriveToCompletion(ctx); err != nil {
		return lab.RunBundle{}, err
	}
	return execution.Export(ctx)
}

func writeBlueprint(path string, blueprint scenario.Blueprint) error {
	encoded, err := scenario.EncodeBlueprint(blueprint)
	if err != nil {
		return err
	}
	return writeNewFile(path, encoded)
}

func writeBundle(path string, bundle lab.RunBundle) error {
	encoded, err := bundle.Bytes()
	if err != nil {
		return err
	}
	return writeNewFile(path, encoded)
}

func writeNewFile(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func writeCommandResult(output io.Writer, value any) int {
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return 1
	}
	return 0
}

func commandError(stderr io.Writer, err error) int {
	_, _ = fmt.Fprintln(stderr, err)
	return 2
}

func commandFailure(stderr io.Writer, action string, err error) int {
	_, _ = fmt.Fprintf(stderr, "mercator lab: %s: %v\n", action, err)
	return 1
}
