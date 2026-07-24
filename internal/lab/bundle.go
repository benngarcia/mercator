package lab

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/benngarcia/mercator/internal/eventlog"
	"github.com/benngarcia/mercator/internal/scenario"
)

const RunBundleSchemaV1 = "mercator.lab/run-bundle.v1"

var requiredBundleEntries = []string{
	"manifest.json",
	"configuration.json",
	"blueprint.json",
	"world-tape.json",
	"drives.jsonl",
	"samples.jsonl",
	"events/mercator.jsonl",
	"events/world.jsonl",
	"effects.jsonl",
	"predictions.jsonl",
	"invariants.json",
	"metrics.json",
}

type bundleEntry struct {
	name string
	data []byte
}

type RunBundle struct {
	entries []bundleEntry
}

type UIEvidence struct {
	Trace       []byte
	Screenshots map[string][]byte
}

type bundleManifest struct {
	Schema           string `json:"schema"`
	MercatorRevision string `json:"mercator_revision"`
	WorldTapeSHA256  string `json:"world_tape_sha256"`
}

type bundleConfiguration struct {
	Limits Limits `json:"limits"`
	Policy string `json:"policy"`
}

type predictionActualRecord struct {
	RunID            string  `json:"run_id"`
	Metric           string  `json:"metric"`
	PredictedSeconds float64 `json:"predicted_seconds"`
	ActualSeconds    float64 `json:"actual_seconds"`
	PredictionSource string  `json:"prediction_source"`
	ActualSource     string  `json:"actual_source"`
}

type bundleMetrics struct {
	Transitions    uint64 `json:"transitions"`
	MercatorEvents int    `json:"mercator_events"`
	WorldEvents    int    `json:"world_events"`
	Effects        int    `json:"effects"`
	Restarts       uint64 `json:"control_plane_restarts"`
}

type normalizedEffect struct {
	At            time.Time       `json:"at"`
	Operation     string          `json:"operation"`
	OperationID   string          `json:"operation_id"`
	Command       EffectCommand   `json:"command"`
	Response      EffectResponse  `json:"response"`
	CorrelationID string          `json:"correlation_id"`
	CausationID   string          `json:"causation_id"`
	RequestHash   string          `json:"request_hash,omitempty"`
	Request       json.RawMessage `json:"request"`
	Consequence   json.RawMessage `json:"consequence"`
	FaultID       string          `json:"fault_id,omitempty"`
}

func (execution *Execution) Export(ctx context.Context) (RunBundle, error) {
	if err := ctx.Err(); err != nil {
		return RunBundle{}, err
	}
	if execution.config.Blueprint.Schema != scenario.BlueprintSchemaV1 {
		return RunBundle{}, fmt.Errorf(
			"cannot export unsupported Blueprint schema %q",
			execution.config.Blueprint.Schema,
		)
	}
	manifest := bundleManifest{
		Schema:           RunBundleSchemaV1,
		MercatorRevision: execution.config.MercatorRevision,
		WorldTapeSHA256:  execution.config.Tape.SHA256(),
	}
	configuration := bundleConfiguration{
		Limits: execution.config.Limits,
		Policy: execution.config.Policy,
	}
	values := []any{
		manifest,
		configuration,
		execution.config.Blueprint,
		execution.config.Tape,
	}
	entries := make([]bundleEntry, 0, len(requiredBundleEntries))
	for index, name := range requiredBundleEntries[:4] {
		encoded, err := canonicalJSON(values[index])
		if err != nil {
			return RunBundle{}, fmt.Errorf("encode %s: %w", name, err)
		}
		entries = append(entries, bundleEntry{name: name, data: encoded})
	}
	drives, err := jsonLines(execution.drives)
	if err != nil {
		return RunBundle{}, err
	}
	entries = append(entries, bundleEntry{name: "drives.jsonl", data: drives})
	samples, err := jsonLines(execution.config.Samples)
	if err != nil {
		return RunBundle{}, err
	}
	worldEvents, err := jsonLines(execution.processed)
	if err != nil {
		return RunBundle{}, err
	}
	mercatorEvents, effects, restarts, err := execution.bundleRuntimeData(ctx)
	if err != nil {
		return RunBundle{}, err
	}
	encodedMercatorEvents, err := jsonLines(mercatorEvents)
	if err != nil {
		return RunBundle{}, err
	}
	encodedEffects, err := jsonLines(effects)
	if err != nil {
		return RunBundle{}, err
	}
	predictions, err := predictionActualRecords(execution.config.Tape, effects)
	if err != nil {
		return RunBundle{}, err
	}
	encodedPredictions, err := jsonLines(predictions)
	if err != nil {
		return RunBundle{}, err
	}
	metrics, err := canonicalJSON(bundleMetrics{
		Transitions:    execution.transitions,
		MercatorEvents: len(mercatorEvents),
		WorldEvents:    len(execution.processed),
		Effects:        len(effects),
		Restarts:       restarts,
	})
	if err != nil {
		return RunBundle{}, err
	}
	invariants, err := canonicalJSON(latestInvariantResults(execution.invariants))
	if err != nil {
		return RunBundle{}, err
	}
	entries = append(entries,
		bundleEntry{name: "samples.jsonl", data: samples},
		bundleEntry{name: "events/mercator.jsonl", data: encodedMercatorEvents},
		bundleEntry{name: "events/world.jsonl", data: worldEvents},
		bundleEntry{name: "effects.jsonl", data: encodedEffects},
		bundleEntry{name: "predictions.jsonl", data: encodedPredictions},
		bundleEntry{name: "invariants.json", data: invariants},
		bundleEntry{name: "metrics.json", data: metrics},
	)
	return RunBundle{entries: entries}, nil
}

func (execution *Execution) bundleRuntimeData(ctx context.Context) ([]eventlog.CloudEvent, []EffectRecord, uint64, error) {
	if execution.runtime == nil {
		return nil, nil, 0, nil
	}
	stored, err := execution.runtime.mercatorEvents(ctx)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("read Mercator events for Run Bundle: %w", err)
	}
	events := make([]eventlog.CloudEvent, len(stored))
	for index, event := range stored {
		events[index] = event.CloudEvent()
	}
	return events, execution.runtime.world.effectRecords(), execution.runtime.restarts, nil
}

func predictionActualRecords(tape WorldTape, effects []EffectRecord) ([]predictionActualRecord, error) {
	actuals := acceptedActualRuntimes(effects)
	records := make([]predictionActualRecord, 0, len(tape.Events))
	for _, event := range tape.Events {
		if event.Kind != EventRunArrived {
			continue
		}
		var arrival RunArrival
		if err := json.Unmarshal(event.Data, &arrival); err != nil {
			return nil, fmt.Errorf("decode prediction record from World event %q: %w", event.ID, err)
		}
		predicted := float64(0)
		if arrival.Request.ExpectedRuntime != nil {
			predicted = arrival.Request.ExpectedRuntime.Duration().Seconds()
		}
		actual := arrival.ActualRuntime.Duration().Seconds()
		if observed, exists := actuals["run-"+arrival.Name]; exists {
			actual = observed
		}
		records = append(records, predictionActualRecord{
			RunID:            "run-" + arrival.Name,
			Metric:           "runtime_seconds",
			PredictedSeconds: predicted,
			ActualSeconds:    actual,
			PredictionSource: "workload.expected_runtime",
			ActualSource:     "world_tape.actual_runtime",
		})
	}
	return records, nil
}

func acceptedActualRuntimes(effects []EffectRecord) map[string]float64 {
	actuals := map[string]float64{}
	for _, effect := range effects {
		if effect.Operation != OperationProviderLaunch || effect.Command != EffectCommandAccepted {
			continue
		}
		var consequence struct {
			ActualRuntimeSeconds float64 `json:"actual_runtime_seconds"`
		}
		if json.Unmarshal(effect.Consequence, &consequence) == nil && consequence.ActualRuntimeSeconds > 0 {
			actuals[effect.CorrelationID] = consequence.ActualRuntimeSeconds
		}
	}
	return actuals
}

func (bundle RunBundle) EntryNames() []string {
	names := make([]string, len(bundle.entries))
	for index, entry := range bundle.entries {
		names[index] = entry.name
	}
	return names
}

func DecodeRunBundle(archive []byte) (RunBundle, error) {
	decoded, err := readBundleEntries(archive)
	if err != nil {
		return RunBundle{}, err
	}
	names := append([]string(nil), requiredBundleEntries...)
	var optional []string
	for name := range decoded {
		if strings.HasPrefix(name, "ui/") {
			optional = append(optional, name)
		}
	}
	sort.Slice(optional, func(i, j int) bool {
		if optional[i] == "ui/trace.zip" {
			return true
		}
		if optional[j] == "ui/trace.zip" {
			return false
		}
		return optional[i] < optional[j]
	})
	names = append(names, optional...)
	entries := make([]bundleEntry, 0, len(names))
	for _, name := range names {
		entries = append(entries, bundleEntry{name: name, data: slices.Clone(decoded[name])})
	}
	return RunBundle{entries: entries}, nil
}

func (bundle RunBundle) Blueprint() (scenario.Blueprint, error) {
	var blueprint scenario.Blueprint
	if err := decodeBundleJSON("blueprint.json", bundle.entry("blueprint.json"), &blueprint); err != nil {
		return scenario.Blueprint{}, err
	}
	var tape WorldTape
	if err := decodeBundleJSON("world-tape.json", bundle.entry("world-tape.json"), &tape); err != nil {
		return scenario.Blueprint{}, err
	}
	blueprint.Name = tape.BlueprintName
	return blueprint, nil
}

func (bundle RunBundle) Effects() ([]EffectRecord, error) {
	var effects []EffectRecord
	for _, line := range bytes.Split(bundle.entry("effects.jsonl"), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var effect EffectRecord
		if err := decodeBundleJSON("effects.jsonl", line, &effect); err != nil {
			return nil, err
		}
		effects = append(effects, effect)
	}
	return effects, nil
}

func (bundle RunBundle) Drives() ([]DriveRecord, error) {
	var drives []DriveRecord
	for _, line := range bytes.Split(bundle.entry("drives.jsonl"), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var drive DriveRecord
		if err := decodeBundleJSON("drives.jsonl", line, &drive); err != nil {
			return nil, err
		}
		drives = append(drives, drive)
	}
	return drives, nil
}

func (bundle RunBundle) entry(name string) []byte {
	for _, entry := range bundle.entries {
		if entry.name == name {
			return entry.data
		}
	}
	return nil
}

func (bundle RunBundle) WithUIEvidence(evidence UIEvidence) (RunBundle, error) {
	entries := make([]bundleEntry, 0, len(requiredBundleEntries)+1+len(evidence.Screenshots))
	for _, entry := range bundle.entries {
		if !strings.HasPrefix(entry.name, "ui/") {
			entries = append(entries, bundleEntry{name: entry.name, data: slices.Clone(entry.data)})
		}
	}
	if len(evidence.Trace) > 0 {
		entries = append(entries, bundleEntry{name: "ui/trace.zip", data: slices.Clone(evidence.Trace)})
	}
	names := make([]string, 0, len(evidence.Screenshots))
	for name := range evidence.Screenshots {
		if path.Base(name) != name || !strings.HasSuffix(name, ".png") || name == ".png" {
			return RunBundle{}, fmt.Errorf("invalid UI screenshot name %q", name)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		entries = append(entries, bundleEntry{
			name: "ui/screenshots/" + name,
			data: slices.Clone(evidence.Screenshots[name]),
		})
	}
	return RunBundle{entries: entries}, nil
}

func (bundle RunBundle) Bytes() ([]byte, error) {
	var output bytes.Buffer
	writer := tar.NewWriter(&output)
	for _, entry := range bundle.entries {
		header := &tar.Header{
			Name:    entry.name,
			Mode:    0o644,
			Size:    int64(len(entry.data)),
			ModTime: time.Unix(0, 0).UTC(),
			Format:  tar.FormatUSTAR,
		}
		if err := writer.WriteHeader(header); err != nil {
			return nil, err
		}
		if _, err := writer.Write(entry.data); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func (bundle RunBundle) NormalizedSHA256() string {
	hash := sha256.New()
	effects, effectCount := bundle.normalizedEffects()
	for _, entry := range bundle.entries {
		if strings.HasPrefix(entry.name, "ui/") || entry.name == "drives.jsonl" {
			continue
		}
		data := bundle.normalizedEntry(entry, effects, effectCount)
		_, _ = hash.Write([]byte(entry.name))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(data)
		_, _ = hash.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func (bundle RunBundle) normalizedEntry(entry bundleEntry, effects []byte, effectCount int) []byte {
	switch entry.name {
	case "effects.jsonl":
		return effects
	case "metrics.json":
		return bundle.normalizedMetrics(effectCount)
	default:
		return entry.data
	}
}

func (bundle RunBundle) normalizedEffects() ([]byte, int) {
	effects, err := bundle.Effects()
	if err != nil {
		return bundle.entry("effects.jsonl"), -1
	}
	normalized := make([]normalizedEffect, 0, len(effects))
	for _, effect := range effects {
		if effect.Operation == OperationControlPlaneRestart || !effectMutatesWorld(effect.Operation) {
			continue
		}
		normalized = append(normalized, normalizedEffect{
			At:            effect.At,
			Operation:     effect.Operation,
			OperationID:   effect.OperationID,
			Command:       effect.Command,
			Response:      effect.Response,
			CorrelationID: effect.CorrelationID,
			CausationID:   effect.CausationID,
			RequestHash:   effect.RequestHash,
			Request:       effect.Request,
			Consequence:   effect.Consequence,
			FaultID:       effect.FaultID,
		})
	}
	encoded, err := jsonLines(normalized)
	if err != nil {
		return bundle.entry("effects.jsonl"), -1
	}
	return encoded, len(normalized)
}

func (bundle RunBundle) normalizedMetrics(effectCount int) []byte {
	if effectCount < 0 {
		return bundle.entry("metrics.json")
	}
	var metrics bundleMetrics
	if err := decodeBundleJSON("metrics.json", bundle.entry("metrics.json"), &metrics); err != nil {
		return bundle.entry("metrics.json")
	}
	metrics.Effects = effectCount
	metrics.Restarts = 0
	encoded, err := canonicalJSON(metrics)
	if err != nil {
		return bundle.entry("metrics.json")
	}
	return encoded
}

func Replay(ctx context.Context, archive []byte) (*Execution, error) {
	bundle, err := DecodeRunBundle(archive)
	if err != nil {
		return nil, err
	}
	entries, err := readBundleEntries(archive)
	if err != nil {
		return nil, err
	}
	var manifest bundleManifest
	if err := decodeBundleJSON("manifest.json", entries["manifest.json"], &manifest); err != nil {
		return nil, err
	}
	if manifest.Schema != RunBundleSchemaV1 {
		return nil, fmt.Errorf("unsupported Run Bundle schema %q", manifest.Schema)
	}
	var configuration bundleConfiguration
	if err := decodeBundleJSON("configuration.json", entries["configuration.json"], &configuration); err != nil {
		return nil, err
	}
	blueprint, err := bundle.Blueprint()
	if err != nil {
		return nil, err
	}
	if blueprint.Schema != scenario.BlueprintSchemaV1 {
		return nil, fmt.Errorf("unsupported bundled Blueprint schema %q", blueprint.Schema)
	}
	var tape WorldTape
	if err := decodeBundleJSON("world-tape.json", entries["world-tape.json"], &tape); err != nil {
		return nil, err
	}
	if tape.SHA256() != manifest.WorldTapeSHA256 {
		return nil, fmt.Errorf("Run Bundle World Tape hash does not match its manifest")
	}
	samples, err := decodeSamples(entries["samples.jsonl"])
	if err != nil {
		return nil, err
	}
	return Open(ctx, Config{
		Blueprint:        blueprint,
		Tape:             tape,
		Samples:          samples,
		Limits:           configuration.Limits,
		Policy:           configuration.Policy,
		MercatorRevision: manifest.MercatorRevision,
	})
}

func Reconstruct(ctx context.Context, bundle RunBundle) (*Execution, error) {
	archive, err := bundle.Bytes()
	if err != nil {
		return nil, err
	}
	execution, err := Replay(ctx, archive)
	if err != nil {
		return nil, err
	}
	drives, err := bundle.Drives()
	if err != nil {
		_ = execution.Close()
		return nil, err
	}
	execution.recordDrive = false
	for _, record := range drives {
		if err := execution.replayDrive(ctx, record); err != nil {
			_ = execution.Close()
			return nil, err
		}
	}
	execution.recordDrive = true
	execution.drives = slices.Clone(drives)
	return execution, nil
}

func (execution *Execution) replayDrive(ctx context.Context, record DriveRecord) error {
	switch record.Kind {
	case "step":
		_, err := execution.Drive(ctx, Step())
		return err
	case "advance":
		duration, err := time.ParseDuration(record.Duration)
		if err != nil {
			return fmt.Errorf("decode recorded Lab advance: %w", err)
		}
		_, err = execution.Drive(ctx, Advance(duration))
		return err
	case "until_event":
		_, err := execution.Drive(ctx, UntilEvent(record.Event))
		return err
	case "until_predicate":
		for execution.transitions < record.TargetTransitions {
			before := execution.transitions
			if _, err := execution.Drive(ctx, Step()); err != nil {
				return err
			}
			if execution.transitions == before {
				return fmt.Errorf(
					"recorded Lab predicate target %d exceeds the World Tape",
					record.TargetTransitions,
				)
			}
		}
		return nil
	case "quiesce":
		_, err := execution.Drive(ctx, Quiesce())
		return err
	case "restart":
		return execution.Restart(ctx)
	default:
		return fmt.Errorf("unknown recorded Lab drive %q", record.Kind)
	}
}

func decodeBundleJSON(name string, data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("decode Run Bundle entry %q: %w", name, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("decode Run Bundle entry %q: expected one JSON document", name)
	}
	return nil
}

func canonicalJSON(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func jsonLines[T any](values []T) ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	for _, value := range values {
		if err := encoder.Encode(value); err != nil {
			return nil, err
		}
	}
	return output.Bytes(), nil
}

func readBundleEntries(archive []byte) (map[string][]byte, error) {
	reader := tar.NewReader(bytes.NewReader(archive))
	entries := make(map[string][]byte, len(requiredBundleEntries))
	optionalNames := []string{}
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read Run Bundle: %w", err)
		}
		required := len(entries) < len(requiredBundleEntries)
		if required && header.Name != requiredBundleEntries[len(entries)] {
			return nil, fmt.Errorf(
				"Run Bundle entry %q is out of order, expected %q",
				header.Name,
				requiredBundleEntries[len(entries)],
			)
		}
		if !required && !validOptionalBundleEntry(header.Name) {
			return nil, fmt.Errorf("unexpected Run Bundle entry %q", header.Name)
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return nil, fmt.Errorf("Run Bundle entry %q is not a regular file", header.Name)
		}
		if _, exists := entries[header.Name]; exists {
			return nil, fmt.Errorf("duplicate Run Bundle entry %q", header.Name)
		}
		if !required {
			if len(optionalNames) > 0 {
				previous := optionalNames[len(optionalNames)-1]
				if header.Name == "ui/trace.zip" ||
					previous != "ui/trace.zip" && header.Name <= previous {
					return nil, fmt.Errorf("Run Bundle UI entry %q is out of order", header.Name)
				}
			}
			optionalNames = append(optionalNames, header.Name)
		}
		if header.Size > 64<<20 {
			return nil, fmt.Errorf("Run Bundle entry %q exceeds 64 MiB", header.Name)
		}
		data, err := io.ReadAll(reader)
		if err != nil {
			return nil, fmt.Errorf("read Run Bundle entry %q: %w", header.Name, err)
		}
		entries[header.Name] = data
	}
	if len(entries) < len(requiredBundleEntries) {
		var missing []string
		for _, name := range requiredBundleEntries {
			if _, exists := entries[name]; !exists {
				missing = append(missing, name)
			}
		}
		return nil, fmt.Errorf("Run Bundle is missing entries %v", missing)
	}
	return entries, nil
}

func validOptionalBundleEntry(name string) bool {
	if name == "ui/trace.zip" {
		return true
	}
	const prefix = "ui/screenshots/"
	return strings.HasPrefix(name, prefix) &&
		path.Base(name) == strings.TrimPrefix(name, prefix) &&
		strings.HasSuffix(name, ".png") &&
		name != prefix+".png"
}

func decodeSamples(data []byte) ([]Sample, error) {
	var samples []Sample
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var sample Sample
		if err := decodeBundleJSON("samples.jsonl", line, &sample); err != nil {
			return nil, err
		}
		samples = append(samples, sample)
	}
	return slices.Clone(samples), nil
}
