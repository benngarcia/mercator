package scenario

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/capability"
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

	if regressions != 63 {
		t.Errorf("regression Blueprints = %d, want 63", regressions)
	}
	if counts[ClassificationGreen] != 62 {
		t.Errorf("green Blueprints = %d, want 62", counts[ClassificationGreen])
	}
	if counts[ClassificationTarget] != 1 {
		t.Errorf("target Blueprints = %d, want 1", counts[ClassificationTarget])
	}
}

// TestTheEnrolledNodeCaseNamesTheMachineApartFromTheListing reads the case that
// waited on phase 5 since phase 1 and has now been paid off, and pins the
// distinction that paying it turned on. The listing and the machine behind it are
// two candidates with two names: the first Run wins the listing, and the second
// wins the machine that listing became, because a machine an agent enrolled on
// publishes standing reusable capacity of its own and a marketplace goes on
// selling the product either way.
//
// It waits on nothing now, and the assertion is kept as an assertion rather than
// deleted: a fixture that quietly grew a pending reason again would be a green
// case standing in for capability the tree does not have.
func TestTheEnrolledNodeCaseNamesTheMachineApartFromTheListing(t *testing.T) {
	blueprint, err := LoadBlueprint("scenarios/enrolled-node-survives-its-first-run.json")
	if err != nil {
		t.Fatalf("load Blueprint: %v", err)
	}

	if len(blueprint.MissingCapabilities) > 0 {
		t.Fatalf("the case waits on %v, and it is green", blueprint.MissingCapabilities)
	}
	listing := blueprint.World.Marketplace[0]
	won := blueprint.Timeline[len(blueprint.Timeline)-1].Expect.Offer
	if won != listing.Machine {
		t.Errorf("the second Run wins %q, want the machine %q rather than a listing", won, listing.Machine)
	}
	if listing.Machine != "simcloud-4090-0f31" {
		t.Errorf("the listing names machine %q, and a reused machine has to be nameable", listing.Machine)
	}
	if listing.Capacity == nil {
		t.Fatal("the listing states nothing about what its provider does with capacity")
	}
	if !listing.Capacity.Stop || !listing.Capacity.Resume || !listing.Capacity.PersistentDisk {
		t.Errorf("this provider holds a machine across Runs, and negotiated %+v", *listing.Capacity)
	}
	if listing.Bootstrap == nil || listing.Bootstrap.Deadline == nil {
		t.Errorf("the listing states bootstrap %+v, and a late agent is only late against a bound", listing.Bootstrap)
	}
}

// TestTheStrandedCapacityCaseStatesBothHalvesOfItsOwnName reads the case that
// the corpus could not describe at all: a provider that allocates and boots a
// machine whose agent never opens a session. Silence about the enrolment stage
// already means a stage that costs nothing, so without a word for it the failure a
// provider bills for read as the fastest possible success.
//
// It pins the reclaim half too, because that half is the one a fixture can promise
// in its name and assert nowhere. Stating only the outcome, the offer, and the two
// absent stages described an indefinite wait: a control plane that provisions the
// silent machine and then does nothing at all satisfies every one of those, so the
// fixture would have been promoted to green as evidence of a reclamation nobody
// built.
//
// Three things together make that half sayable, and each of them was found by an
// expectation that could not fail. A reconcile on both sides of the stated deadline,
// because a fixture that only looked afterwards would read the same against a
// control plane that gave up on any machine whose start it had not yet seen, which
// would abandon the healthy machine in this very world. A confirmed terminate on the
// silent machine before the work moves, because giving up on capacity and rerunning
// the work are two acts and only the first ends the bill. And a reason of its own,
// because the launch failure this fixture used to name means the provider refused
// and created nothing, which is the opposite of what happened to a machine Mercator
// is being billed for.
func TestTheStrandedCapacityCaseStatesBothHalvesOfItsOwnName(t *testing.T) {
	blueprint, err := LoadBlueprint("scenarios/provisioned-capacity-enrolls-or-is-reclaimed.json")
	if err != nil {
		t.Fatalf("load Blueprint: %v", err)
	}

	if blueprint.Classification != ClassificationGreen {
		t.Errorf("classification = %q, want green", blueprint.Classification)
	}
	if len(blueprint.MissingCapabilities) != 0 {
		t.Fatalf("the case still waits on %v, and a green Blueprint waits on nothing", blueprint.MissingCapabilities)
	}
	stranded, patient := blueprint.World.Marketplace[0], blueprint.World.Marketplace[1]
	if !stranded.NeverEnrolls() {
		t.Fatalf("listing %q states bootstrap %+v, want an agent that never enrols", stranded.ID, stranded.Bootstrap)
	}
	if stranded.Provisioning.AcquisitionSpend() == 0 || stranded.Provisioning.BootSpend() == 0 {
		t.Errorf("acquisition and boot must succeed here, and the world spends %v on them", stranded.Provisioning.Spend())
	}
	if stranded.Bootstrap.Deadline == nil {
		t.Errorf("a machine nothing enrols on needs an end to its bill, and the listing names %+v", stranded.Bootstrap)
	}
	// The dearer machine whose agent does arrive. Without somewhere for the work to
	// go, giving up on the silent one could only be stated as a Run left nowhere.
	if patient.NeverEnrolls() || patient.RatePerHourUSD <= stranded.RatePerHourUSD {
		t.Fatalf("listing %q costs %v and states bootstrap %+v, want a dearer machine whose agent arrives",
			patient.ID, patient.RatePerHourUSD, patient.Bootstrap)
	}

	patience := stranded.Bootstrap.Deadline.Duration()
	looks := reconcilesByMoment(blueprint.Timeline)
	if len(looks) < 2 {
		t.Fatalf("the fixture reconciles %d times, and patience is only patience if it is read from both sides", len(looks))
	}
	waiting, gaveUp := looks[0], looks[len(looks)-1]

	if waiting.at >= patience || gaveUp.at <= patience {
		t.Fatalf("the fixture reconciles at %v and %v, and the deadline it is about is %v", waiting.at, gaveUp.at, patience)
	}
	if waiting.expect.Offer != stranded.ID {
		t.Errorf("inside the patience the fixture expects offer %q, and Mercator said it would wait", waiting.expect.Offer)
	}
	if waiting.expect.Decision == nil || waiting.expect.Decision.Recorded != 1 || waiting.expect.Decision.Supersedes != 0 {
		t.Errorf("inside the patience the fixture expects decisions %+v, want the first answer still standing", waiting.expect.Decision)
	}
	if gaveUp.expect.Offer != patient.ID {
		t.Errorf("the last step expects offer %q, and a machine nobody gave up on is still the answer", gaveUp.expect.Offer)
	}
	if gaveUp.expect.Decision == nil || gaveUp.expect.Decision.Recorded != 2 || gaveUp.expect.Decision.Supersedes != 1 {
		t.Fatalf("the last step expects decisions %+v, and a re-decision is what separates reclaiming from waiting", gaveUp.expect.Decision)
	}
	if gaveUp.expect.Decision.SupersedesReason != domain.SupersededCapacityReclaimed {
		t.Errorf("the re-decision gives reason %q, want the capacity Mercator handed back", gaveUp.expect.Decision.SupersedesReason)
	}
	// The bill ends before the work moves. Without this the whole chain is equally
	// true of a control plane that runs the Run again on the dearer machine and
	// leaves the silent one to the provider's backstop.
	reclaimed := gaveUp.expect.Reclaimed
	if reclaimed == nil || reclaimed.Offer != stranded.ID || reclaimed.Disposition != domain.DispositionTerminate {
		t.Errorf("the last step reclaims %+v, want the silent machine terminated", reclaimed)
	}
	// And the listing whose machine was just given back is struck out of the answer
	// that replaces it, because the cheaper listing would otherwise win the work
	// straight back and strand it again.
	struckOut, weighed := gaveUp.expect.Candidates[stranded.ID]
	if !weighed || struckOut.Feasible == nil || *struckOut.Feasible || len(struckOut.Rejected) == 0 {
		t.Errorf("the last step weighs %q as %+v, want it refused with a reason", stranded.ID, struckOut)
	}
}

// timedReconcile is one reconcile in a timeline and how far into the world it
// happens. A fixture about a bound Mercator waits out has to be read this way: the
// expectations alone cannot say whether a step is inside the patience or past it,
// and a fixture whose every look happens after the deadline states nothing about the
// deadline.
type timedReconcile struct {
	at     time.Duration
	expect ExpectSpec
}

func reconcilesByMoment(timeline []StepSpec) []timedReconcile {
	var elapsed time.Duration
	var looks []timedReconcile
	for _, step := range timeline {
		if step.Advance != nil {
			elapsed += step.Advance.Duration()
		}
		if step.Reconcile != "" && step.Expect != nil {
			looks = append(looks, timedReconcile{at: elapsed, expect: *step.Expect})
		}
	}
	return looks
}

// TestLoadBlueprintRefusesACapacityAccountNoProviderCouldKeep holds the two
// shapes of listing that would read green while stating a world no provider
// produces. Both are refused at load, because a Blueprint is the public contract
// and a fixture that got past this door would be asserting the negotiation rather
// than describing it.
func TestLoadBlueprintRefusesACapacityAccountNoProviderCouldKeep(t *testing.T) {
	for _, testCase := range []struct{ path, want string }{
		{
			path: "testdata/blueprints/invalid/provisionable-listing-without-a-machine.json",
			want: "every promise in that set is about one machine keeping its identity",
		},
		{
			path: "testdata/blueprints/invalid/bootstrap-nobody-gives-up-on.json",
			want: "a machine nobody gives up on bills for ever",
		},
		{
			path: "testdata/blueprints/invalid/ephemeral-listing-that-stops-and-resumes.json",
			want: "has no machine to stop, resume, or enrol an agent on",
		},
		{
			path: "testdata/blueprints/invalid/ephemeral-listing-whose-agent-never-enrols.json",
			want: "has no machine to stop, resume, or enrol an agent on",
		},
		{
			path: "testdata/blueprints/invalid/listing-that-states-no-lane.json",
			want: "the end of a Run destroys one lane and hands the other back",
		},
	} {
		_, err := LoadBlueprint(testCase.path)

		if err == nil || !strings.Contains(err.Error(), testCase.want) {
			t.Fatalf("loading %s gave %v, want a refusal naming %q", testCase.path, err, testCase.want)
		}
	}
}

// TestABlueprintStatesStopWithoutResume is why the negotiated set is carried as
// the promises a CapacityProvider really answers with rather than as a list of the
// capability names a provider ticked. A provider that suspends a machine and
// cannot bring the same one back exists, and a list of names cannot tell a resume
// nobody offers from a resume nobody mentioned, so it would encode the difference
// away on the first round trip through the public contract.
func TestABlueprintStatesStopWithoutResume(t *testing.T) {
	blueprint, err := LoadBlueprint("testdata/blueprints/v1/stop-without-resume.json")
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

	negotiated := decoded.World.Marketplace[0].Capacity
	if negotiated == nil {
		t.Fatal("the round trip dropped what this provider negotiated")
	}
	if !negotiated.Stop || negotiated.Resume {
		t.Fatalf("the round trip made this provider %+v, and it stops without resuming", *negotiated)
	}
	if negotiated.IdempotentProvision != capability.IdempotentProvisionNone || !negotiated.ListOwned {
		t.Fatalf("this provider deduplicates nothing and is reconciled by listing, and the round trip says %+v", *negotiated)
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
