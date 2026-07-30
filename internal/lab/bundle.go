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
	"slices"
	"time"

	"github.com/benngarcia/mercator/internal/scenario"
)

const RunBundleSchemaV1 = "mercator.lab/run-bundle.v1"

var requiredBundleEntries = []string{
	"manifest.json",
	"configuration.json",
	"blueprint.json",
	"world-tape.json",
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

type bundleManifest struct {
	Schema           string `json:"schema"`
	MercatorRevision string `json:"mercator_revision"`
	WorldTapeSHA256  string `json:"world_tape_sha256"`
}

type bundleConfiguration struct {
	Limits Limits `json:"limits"`
	Policy string `json:"policy"`
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
	samples, err := jsonLines(execution.config.Samples)
	if err != nil {
		return RunBundle{}, err
	}
	worldEvents, err := jsonLines(execution.processed)
	if err != nil {
		return RunBundle{}, err
	}
	entries = append(entries,
		bundleEntry{name: "samples.jsonl", data: samples},
		bundleEntry{name: "events/mercator.jsonl", data: nil},
		bundleEntry{name: "events/world.jsonl", data: worldEvents},
		bundleEntry{name: "effects.jsonl", data: nil},
		bundleEntry{name: "predictions.jsonl", data: nil},
		bundleEntry{name: "invariants.json", data: []byte("{}\n")},
		bundleEntry{name: "metrics.json", data: []byte("{}\n")},
	)
	return RunBundle{entries: entries}, nil
}

func (bundle RunBundle) EntryNames() []string {
	names := make([]string, len(bundle.entries))
	for index, entry := range bundle.entries {
		names[index] = entry.name
	}
	return names
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
	for _, entry := range bundle.entries {
		_, _ = hash.Write([]byte(entry.name))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(entry.data)
		_, _ = hash.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func Replay(ctx context.Context, archive []byte) (*Execution, error) {
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
	var blueprint scenario.Blueprint
	if err := decodeBundleJSON("blueprint.json", entries["blueprint.json"], &blueprint); err != nil {
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
	allowed := make(map[string]bool, len(requiredBundleEntries))
	for _, name := range requiredBundleEntries {
		allowed[name] = true
	}
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read Run Bundle: %w", err)
		}
		if !allowed[header.Name] {
			return nil, fmt.Errorf("unexpected Run Bundle entry %q", header.Name)
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return nil, fmt.Errorf("Run Bundle entry %q is not a regular file", header.Name)
		}
		if _, exists := entries[header.Name]; exists {
			return nil, fmt.Errorf("duplicate Run Bundle entry %q", header.Name)
		}
		expected := requiredBundleEntries[len(entries)]
		if header.Name != expected {
			return nil, fmt.Errorf(
				"Run Bundle entry %q is out of order, expected %q",
				header.Name,
				expected,
			)
		}
		data, err := io.ReadAll(reader)
		if err != nil {
			return nil, fmt.Errorf("read Run Bundle entry %q: %w", header.Name, err)
		}
		entries[header.Name] = data
	}
	if len(entries) != len(requiredBundleEntries) {
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
