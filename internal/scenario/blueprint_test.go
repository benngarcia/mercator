package scenario

import (
	"strings"
	"testing"
)

func TestLoadBlueprintAdaptsLegacyPlacementFixture(t *testing.T) {
	blueprint, err := LoadBlueprint("testdata/blueprints/legacy/idle-rental.json")
	if err != nil {
		t.Fatalf("load Blueprint: %v", err)
	}

	if blueprint.Schema != BlueprintSchemaV1 {
		t.Errorf("schema = %q, want %q", blueprint.Schema, BlueprintSchemaV1)
	}
	if blueprint.Name != "idle-rental" {
		t.Errorf("name = %q, want idle-rental", blueprint.Name)
	}
	if blueprint.Classification != ClassificationGreen {
		t.Errorf("classification = %q, want %q", blueprint.Classification, ClassificationGreen)
	}
}

func TestLoadBlueprintReadsVersionedContract(t *testing.T) {
	blueprint, err := LoadBlueprint("testdata/blueprints/v1/idle-rental.json")
	if err != nil {
		t.Fatalf("load Blueprint: %v", err)
	}

	if blueprint.Schema != BlueprintSchemaV1 {
		t.Errorf("schema = %q, want %q", blueprint.Schema, BlueprintSchemaV1)
	}
	if blueprint.Classification != ClassificationGreen {
		t.Errorf("classification = %q, want %q", blueprint.Classification, ClassificationGreen)
	}
	if blueprint.World.Rentals[0].ID != "rental-a" {
		t.Errorf("rental = %q, want rental-a", blueprint.World.Rentals[0].ID)
	}
}

func TestLoadAdaptsVersionedBlueprintForPlacementRunner(t *testing.T) {
	scenario, err := Load("testdata/blueprints/v1/idle-rental.json")
	if err != nil {
		t.Fatalf("load placement Scenario: %v", err)
	}

	if scenario.Status != StatusGreen {
		t.Errorf("status = %q, want %q", scenario.Status, StatusGreen)
	}
	if scenario.Name != "idle-rental" {
		t.Errorf("name = %q, want idle-rental", scenario.Name)
	}
}

func TestLoadBlueprintRejectsUnsupportedSchema(t *testing.T) {
	_, err := LoadBlueprint("testdata/blueprints/invalid/unsupported-v2.json")

	if err == nil || !strings.Contains(err.Error(), `unsupported Blueprint schema "mercator.lab/blueprint.v2"`) {
		t.Fatalf("unsupported schema must fail loudly, got %v", err)
	}
}

func TestOpenCatalogPreservesPlacementClassifications(t *testing.T) {
	catalog, err := OpenCatalog("scenarios")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}

	counts := map[Classification]int{}
	var regressions int
	for _, entry := range catalog.Entries() {
		if entry.Blueprint.Kind != KindRegression {
			continue
		}
		regressions++
		counts[entry.Blueprint.Classification]++
	}

	if regressions != 12 {
		t.Errorf("regression Blueprints = %d, want 12", regressions)
	}
	if counts[ClassificationGreen] != 4 {
		t.Errorf("green Blueprints = %d, want 4", counts[ClassificationGreen])
	}
	if counts[ClassificationTarget] != 8 {
		t.Errorf("target Blueprints = %d, want 8", counts[ClassificationTarget])
	}
}

func TestLoadBlueprintModelsImmutableArtifactsSeparatelyFromCacheMounts(t *testing.T) {
	blueprint, err := LoadBlueprint("testdata/blueprints/v1/artifact-locality.json")
	if err != nil {
		t.Fatalf("load Blueprint: %v", err)
	}

	if blueprint.World.Artifacts[0].ID != "artifact:dataset:v1" {
		t.Errorf("Artifact = %q, want artifact:dataset:v1", blueprint.World.Artifacts[0].ID)
	}
	if blueprint.Request.CacheMounts[0].Name != "compiler-cache" {
		t.Errorf("Cache Mount = %q, want compiler-cache", blueprint.Request.CacheMounts[0].Name)
	}
	if blueprint.Request.CacheMounts[0].Name == blueprint.Request.ConsumesArtifacts[0] {
		t.Errorf("Cache Mount name must not become immutable Artifact identity")
	}
}

func TestLoadBlueprintRejectsContentKeyedCacheMountIdentity(t *testing.T) {
	fixtures := map[string]string{
		"rental named caches": "testdata/blueprints/invalid/named-caches.json",
		"request cache key":   "testdata/blueprints/invalid/keyed-cache-mount.json",
	}

	for name, path := range fixtures {
		t.Run(name, func(t *testing.T) {
			_, err := LoadBlueprint(path)

			if err == nil || !strings.Contains(err.Error(), "unknown field") {
				t.Fatalf("content-keyed cache identity must fail loudly, got %v", err)
			}
		})
	}
}

func TestOpenCatalogLoadsDemoWithUISidecar(t *testing.T) {
	catalog, err := OpenCatalog("testdata/catalog")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	entry, ok := catalog.Lookup("artifact-lifecycle")
	if !ok {
		t.Fatalf("catalog has no artifact-lifecycle entry")
	}

	if entry.Blueprint.Kind != KindDemo {
		t.Errorf("kind = %q, want %q", entry.Blueprint.Kind, KindDemo)
	}
	if entry.UI == nil || len(entry.UI.Checkpoints) != 1 {
		t.Fatalf("UI checkpoints = %#v, want one", entry.UI)
	}
	if entry.UI.Checkpoints[0].Assertions[0].Role != "heading" {
		t.Errorf("assertion role = %q, want heading", entry.UI.Checkpoints[0].Assertions[0].Role)
	}
}

func TestCatalogPinsCompleteArtifactWarmthRestartDemonstration(t *testing.T) {
	catalog, err := OpenCatalog("scenarios")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	entry, ok := catalog.Lookup("artifact-warmth-restart")
	if !ok {
		t.Fatalf("catalog has no artifact-warmth-restart target")
	}

	wantEvidence := []ProofEvidence{
		EvidenceProducerSubmitted,
		EvidenceExistingVsFreshCompared,
		EvidencePartialImageReuse,
		EvidenceCapacityPrepared,
		EvidenceArtifactPublished,
		EvidenceConsumerUnblocked,
		EvidenceWarmthObserved,
		EvidenceQueueVsFreshCompared,
		EvidenceAmbiguousDelivery,
		EvidenceReconciledWithoutDuplicate,
		EvidenceControlPlaneRestarted,
		EvidenceRestartEquivalent,
		EvidenceUIRendered,
		EvidenceBundleReplayed,
		EvidenceInvariantsPassed,
	}
	if entry.Blueprint.Classification != ClassificationTarget {
		t.Errorf("classification = %q, want target", entry.Blueprint.Classification)
	}
	if entry.Blueprint.Kind != KindDemo {
		t.Errorf("kind = %q, want demo", entry.Blueprint.Kind)
	}
	if len(entry.Blueprint.Proof) != len(wantEvidence) {
		t.Fatalf("proof checkpoints = %d, want %d", len(entry.Blueprint.Proof), len(wantEvidence))
	}
	for index, want := range wantEvidence {
		checkpoint := entry.Blueprint.Proof[index]
		if checkpoint.Step != index+1 {
			t.Errorf("checkpoint %d step = %d, want %d", index, checkpoint.Step, index+1)
		}
		if checkpoint.Evidence != want {
			t.Errorf("checkpoint %d evidence = %q, want %q", index+1, checkpoint.Evidence, want)
		}
	}
	if entry.UI == nil {
		t.Fatalf("vertical demonstration has no UI sidecar")
	}
}

func TestLoadBlueprintRequiresExactLayerDigests(t *testing.T) {
	blueprint, err := LoadBlueprint("testdata/blueprints/v1/exact-digests.json")
	if err != nil {
		t.Fatalf("load Blueprint: %v", err)
	}

	image := "app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	layer := blueprint.World.Images[image].Layers[0]
	if layer.Digest != "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Errorf("layer digest = %q", layer.Digest)
	}

	_, err = LoadBlueprint("testdata/blueprints/invalid/layer-name.json")
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("versioned layer names must fail loudly, got %v", err)
	}
}

func TestLoadBlueprintRejectsTaggedImageIdentity(t *testing.T) {
	_, err := LoadBlueprint("testdata/blueprints/invalid/image-tag.json")

	if err == nil || !strings.Contains(err.Error(), "digest-pinned") {
		t.Fatalf("tagged image identity must fail loudly, got %v", err)
	}
}
