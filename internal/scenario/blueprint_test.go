package scenario

import (
	"slices"
	"strings"
	"testing"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/orchestrator"
)

// TestLegacyPresenceMigratesAsAnUncheckedCopy pins what the old vocabulary
// could and could not say. A legacy named cache stated that a machine has the
// content a key names and nothing more, so the migrated copy is one nobody
// checked: translating it as verified would assert a hash comparison against a
// catalog that model had no concept of, and would price at zero a copy a
// consumer still owes a fetch for.
func TestLegacyPresenceMigratesAsAnUncheckedCopy(t *testing.T) {
	blueprint, err := LoadBlueprint("testdata/blueprints/legacy/named-cache.json")
	if err != nil {
		t.Fatalf("load Blueprint: %v", err)
	}

	replicas := blueprint.World.Rentals[0].ArtifactReplicas
	if len(replicas) != 1 {
		t.Fatalf("migrated Artifact copies = %+v, want one", replicas)
	}
	if replicas[0].State != domain.ArtifactReplicaUnverified {
		t.Fatalf("the migrated copy is %q, and the fixture only ever said the machine has it", replicas[0].State)
	}
}

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

	if regressions != 56 {
		t.Errorf("regression Blueprints = %d, want 56", regressions)
	}
	if counts[ClassificationGreen] != 53 {
		t.Errorf("green Blueprints = %d, want 53", counts[ClassificationGreen])
	}
	if counts[ClassificationTarget] != 3 {
		t.Errorf("target Blueprints = %d, want 3", counts[ClassificationTarget])
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

// TestABlueprintStatesTheTenantEachRunBelongsTo is what makes a cross-workspace
// claim expressible at all. Before it, every Blueprint ran in one workspace, so
// "no cache crosses a workspace" was not merely unimplemented: no fixture could
// build a world in which it could be false.
func TestABlueprintStatesTheTenantEachRunBelongsTo(t *testing.T) {
	blueprint, err := LoadBlueprint("scenarios/conformance/cache-mounts-never-cross-a-workspace.json")
	if err != nil {
		t.Fatalf("load Blueprint: %v", err)
	}

	workspaces := blueprint.Arrivals.Workspaces()

	if !slices.Equal(workspaces, []string{"", "alpha", "beta"}) {
		t.Fatalf("the Blueprint names workspaces %v, want the default beside its two tenants", workspaces)
	}
	runs, err := blueprint.Arrivals.ExpandedRuns()
	if err != nil {
		t.Fatalf("expand arrivals: %v", err)
	}
	if runs[0].Workspace != "alpha" || runs[1].Workspace != "beta" {
		t.Fatalf("the first two Runs belong to %q and %q, want one tenant each", runs[0].Workspace, runs[1].Workspace)
	}
	declared := runs[0].Request.CacheRequirements()
	if len(declared) != 1 || declared[0].CompatibilityKey != "cuda-12.4" || declared[0].SizeBytes != 8_000_000_000 {
		t.Fatalf("the Run declares %+v, want one cache with its generation and the room it expects", declared)
	}
}

// TestABlueprintRefusesAnArtifactOutsideTheWorkspaceThatDeclaredIt keeps one rule
// from being quietly broken by another. An Artifact belongs to the workspace that
// declared it, and a Blueprint's catalog is declared in the default one, so a Run
// in another tenant naming one of those versions is a fixture implying content
// crosses a workspace. It is refused at load rather than left to an admission
// gate that could never be satisfied.
func TestABlueprintRefusesAnArtifactOutsideTheWorkspaceThatDeclaredIt(t *testing.T) {
	blueprint, err := LoadBlueprint("scenarios/conformance/artifact-must-be-durable-before-a-consumer-runs.json")
	if err != nil {
		t.Fatalf("load Blueprint: %v", err)
	}
	blueprint.Arrivals.Runs[2].Workspace = "beta"

	err = blueprint.Arrivals.validate(blueprint.World)

	if err == nil || !strings.Contains(err.Error(), "an Artifact belongs to the workspace that declared it") {
		t.Fatalf("a Run in another tenant reading this catalog's Artifact gave %v", err)
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
	if entry.Blueprint.Classification != ClassificationGreen {
		t.Errorf("classification = %q, want green", entry.Blueprint.Classification)
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

// TestLoadBlueprintRejectsAWorldNoRegistryCouldServe holds the two shapes that
// would let a fixture read green while stating a world production cannot
// produce. A real config blob names one diff ID for every layer or none, and a
// host that reports diff IDs against layers naming none enumerates nothing at
// all: it would offer an inventory that is Known and empty, so a machine
// holding every byte would be priced a full cold pull at full confidence while
// the world's own transfer model moved nothing.
func TestLoadBlueprintRejectsAWorldNoRegistryCouldServe(t *testing.T) {
	for _, testCase := range []struct{ path, want string }{
		{path: "testdata/blueprints/invalid/half-named-diff-ids.json", want: "one for every layer or none"},
		{path: "testdata/blueprints/invalid/diff-id-host-without-diff-ids.json", want: "reports diff IDs"},
	} {
		_, err := LoadBlueprint(testCase.path)

		if err == nil || !strings.Contains(err.Error(), testCase.want) {
			t.Fatalf("loading %s gave %v, want a refusal naming %q", testCase.path, err, testCase.want)
		}
	}
}

func TestLoadBlueprintRejectsTaggedImageIdentity(t *testing.T) {
	_, err := LoadBlueprint("testdata/blueprints/invalid/image-tag.json")

	if err == nil || !strings.Contains(err.Error(), "digest-pinned") {
		t.Fatalf("tagged image identity must fail loudly, got %v", err)
	}
}

// TestLoadBlueprintRejectsARiskHistoryThatStatesNoRate keeps silence and a clean
// record two worlds a fixture cannot confuse. A history is a measurement, and a
// confidence with no rate under it measured nothing: read as two rates of zero it
// would state a machine measured and never seen to fail, which is the claim its
// provider never made.
func TestLoadBlueprintRejectsARiskHistoryThatStatesNoRate(t *testing.T) {
	_, err := LoadBlueprint("testdata/blueprints/invalid/reliability-history-without-a-rate.json")

	if err == nil || !strings.Contains(err.Error(), "states no rate") {
		t.Fatalf("a published history with nothing measured in it must fail loudly, got %v", err)
	}
}

func TestLoadBlueprintRejectsFaultsTriggeringOnUnrecordedEvents(t *testing.T) {
	_, err := LoadBlueprint("testdata/blueprints/invalid/unknown-fault-event.json")

	if err == nil || !strings.Contains(err.Error(), "unknown event") {
		t.Fatalf("a fault on an event Mercator never records must fail loudly, got %v", err)
	}
}

func TestCatalogFaultsTriggerOnEventsMercatorRecords(t *testing.T) {
	catalog, err := OpenCatalog("scenarios")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}

	for _, entry := range catalog.Entries() {
		for _, fault := range entry.Blueprint.Faults {
			if fault.Trigger.Event == "" {
				continue
			}
			if !orchestrator.IsRunEventType(fault.Trigger.Event) {
				t.Errorf("Blueprint %q fault %q triggers on unrecorded event %q", entry.Blueprint.Name, fault.ID, fault.Trigger.Event)
			}
		}
	}
}

func TestEncodeBlueprintRoundTripsThePublicContract(t *testing.T) {
	blueprint, err := LoadBlueprint("scenarios/demos/artifact-warmth-restart.json")
	if err != nil {
		t.Fatalf("load Blueprint: %v", err)
	}
	encoded, err := EncodeBlueprint(blueprint)
	if err != nil {
		t.Fatalf("encode Blueprint: %v", err)
	}
	decoded, err := DecodeBlueprint("round-trip.json", encoded)
	if err != nil {
		t.Fatalf("decode Blueprint: %v", err)
	}

	if decoded.Name != "round-trip" {
		t.Fatalf("decoded name = %q", decoded.Name)
	}
	if decoded.Schema != blueprint.Schema || decoded.Seed != blueprint.Seed {
		t.Fatalf("decoded Blueprint = %+v", decoded)
	}
}

func TestPromoteBlueprintClearsOnlyTheTargetClassificationDebt(t *testing.T) {
	blueprint, err := LoadBlueprint("scenarios/demos/artifact-warmth-restart.json")
	if err != nil {
		t.Fatalf("load Blueprint: %v", err)
	}
	blueprint.Classification = ClassificationTarget
	blueprint.MissingCapabilities = []Capability{CapabilityLabUI}

	promoted, err := PromoteBlueprint(blueprint)
	if err != nil {
		t.Fatalf("promote Blueprint: %v", err)
	}

	if promoted.Classification != ClassificationGreen {
		t.Fatalf("classification = %q", promoted.Classification)
	}
	if len(promoted.MissingCapabilities) != 0 {
		t.Fatalf("missing capabilities = %v", promoted.MissingCapabilities)
	}
	if promoted.Seed != blueprint.Seed || len(promoted.Proof) != len(blueprint.Proof) {
		t.Fatal("promotion changed executable scenario semantics")
	}
}
