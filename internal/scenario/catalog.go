package scenario

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// UISidecarSchema identifies one test-only UI checkpoint contract.
type UISidecarSchema string

const (
	UISidecarSchemaV1 UISidecarSchema = "mercator.lab/ui-checkpoints.v1"
)

// UISidecar keeps browser checkpoints outside the Blueprint domain model.
type UISidecar struct {
	Schema      UISidecarSchema `json:"schema"`
	Checkpoints []UICheckpoint  `json:"checkpoints"`
}

type UICheckpoint struct {
	ID         string        `json:"id"`
	After      UIEventRef    `json:"after"`
	Assertions []UIAssertion `json:"assertions"`
	Screenshot bool          `json:"screenshot,omitempty"`
}

type UIEventRef struct {
	Event string `json:"event"`
	Run   string `json:"run,omitempty"`
}

type UIAssertion struct {
	Role string `json:"role"`
	Name string `json:"name"`
}

type CatalogEntry struct {
	Blueprint Blueprint
	UI        *UISidecar
}

// Catalog owns one validated, name-unique collection of Blueprint entries.
type Catalog struct {
	entries []CatalogEntry
	byName  map[string]int
}

// OpenCatalog recursively loads Blueprints and their sibling *.ui.json
// sidecars.
func OpenCatalog(root string) (Catalog, error) {
	paths, err := catalogBlueprintPaths(root)
	if err != nil {
		return Catalog{}, err
	}
	catalog := Catalog{byName: make(map[string]int, len(paths))}
	for _, path := range paths {
		entry, err := loadCatalogEntry(path)
		if err != nil {
			return Catalog{}, err
		}
		name := entry.Blueprint.Name
		if _, exists := catalog.byName[name]; exists {
			return Catalog{}, fmt.Errorf("catalog has duplicate Blueprint name %q", name)
		}
		catalog.byName[name] = len(catalog.entries)
		catalog.entries = append(catalog.entries, entry)
	}
	return catalog, nil
}

// Entries returns catalog entries in deterministic path order.
func (c Catalog) Entries() []CatalogEntry {
	return slices.Clone(c.entries)
}

// Lookup returns the catalog entry with name.
func (c Catalog) Lookup(name string) (CatalogEntry, bool) {
	index, ok := c.byName[name]
	if !ok {
		return CatalogEntry{}, false
	}
	return c.entries[index], true
}

func catalogBlueprintPaths(root string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Ext(path) != ".json" || strings.HasSuffix(path, ".ui.json") {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	sort.Strings(paths)
	return paths, err
}

func loadCatalogEntry(path string) (CatalogEntry, error) {
	blueprint, err := LoadBlueprint(path)
	if err != nil {
		return CatalogEntry{}, err
	}
	ui, err := loadUISidecar(strings.TrimSuffix(path, ".json") + ".ui.json")
	if err != nil {
		return CatalogEntry{}, err
	}
	return CatalogEntry{Blueprint: blueprint, UI: ui}, nil
}

func loadUISidecar(path string) (*UISidecar, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var sidecar UISidecar
	if err := strictUnmarshal(data, &sidecar); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := sidecar.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &sidecar, nil
}

func (s UISidecar) validate() error {
	if s.Schema != UISidecarSchemaV1 {
		return fmt.Errorf("unsupported UI sidecar schema %q", s.Schema)
	}
	if len(s.Checkpoints) == 0 {
		return fmt.Errorf("UI sidecars need at least one checkpoint")
	}
	ids := map[string]bool{}
	for _, checkpoint := range s.Checkpoints {
		if checkpoint.ID == "" || checkpoint.After.Event == "" {
			return fmt.Errorf("UI checkpoints need an id and after.event")
		}
		if ids[checkpoint.ID] {
			return fmt.Errorf("duplicate UI checkpoint %q", checkpoint.ID)
		}
		ids[checkpoint.ID] = true
		if len(checkpoint.Assertions) == 0 {
			return fmt.Errorf("UI checkpoint %q needs at least one assertion", checkpoint.ID)
		}
		for _, assertion := range checkpoint.Assertions {
			if assertion.Role == "" || assertion.Name == "" {
				return fmt.Errorf("UI checkpoint %q assertions need role and name", checkpoint.ID)
			}
		}
	}
	return nil
}
