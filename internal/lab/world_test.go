package lab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/ociresolver"
	"github.com/benngarcia/mercator/internal/scenario"
)

// TestPlacementReadsTheHostAWorkloadWarmed is the claim the corpus is built on,
// asserted at the seam Mercator actually reads. Running the image is what puts
// it on the host, and the offer catalog says so on the next placement.
func TestPlacementReadsTheHostAWorkloadWarmed(t *testing.T) {
	world, arrival := openWorldFixture(t, "producer")
	world.prepareRun("run-producer", arrival)
	if _, err := world.Launch(context.Background(), worldLaunchRequest(arrival)); !errors.Is(err, adapter.ErrLaunchIndeterminate) {
		t.Fatalf("launch: %v", err)
	}

	world.setNow(world.nowTime().Add(time.Hour))

	offers, err := world.ListOffers(context.Background(), adapter.OfferRequest{WorkspaceID: labWorkspace})
	if err != nil {
		t.Fatalf("list offers: %v", err)
	}
	offer := offerByID(t, offers, "rental-warm")
	// The host reports the image under the digest it pulled by, which is what a
	// resolved manifest names it too.
	if !offer.Images.Holds(domain.ReferenceDigest(arrival.Request.Image)) {
		t.Fatalf("Placement cannot see the image the host just ran: %+v", offer.Images)
	}
	if !offer.Images.ObservedAt.Equal(world.nowTime()) {
		t.Fatalf("inventory observed at %s, want the time the provider answered, %s", offer.Images.ObservedAt, world.nowTime())
	}
}

// TestPlacementCanReadAnOfferTheWorldHasAlreadyReclaimed is the separation ADR
// 0004 requires: Mercator reads published observations, never world state, so a
// world that moved on since the last publication is a stale answer a fixture can
// write down and a launch the provider then refuses.
func TestPlacementCanReadAnOfferTheWorldHasAlreadyReclaimed(t *testing.T) {
	world, arrival := openWorldFixture(t, "producer")
	world.prepareRun("run-producer", arrival)

	world.setOfferAvailable("rental-warm", false)

	stale, err := world.ListOffers(context.Background(), adapter.OfferRequest{WorkspaceID: labWorkspace})
	if err != nil {
		t.Fatalf("list offers: %v", err)
	}
	if !offerByID(t, stale, "rental-warm").Capacity.Available {
		t.Fatal("world state reached Placement without being observed, so no observation can ever be stale")
	}
	var failure *adapter.ProviderFailure
	if _, err := world.Launch(context.Background(), worldLaunchRequest(arrival)); !errors.As(err, &failure) ||
		failure.Kind != adapter.ProviderFailureCapacityUnavailable {
		t.Fatalf("launching onto capacity the world reclaimed returned %v, want capacity unavailable", err)
	}

	world.setNow(world.nowTime().Add(time.Minute))

	fresh, err := world.ListOffers(context.Background(), adapter.OfferRequest{WorkspaceID: labWorkspace})
	if err != nil {
		t.Fatalf("list offers: %v", err)
	}
	if offerByID(t, fresh, "rental-warm").Capacity.Available {
		t.Fatal("the published observation never caught up with the world")
	}
}

func TestWorldEffectLedgerRecordsAmbiguousAndDuplicateLaunches(t *testing.T) {
	world, arrival := openWorldFixture(t, "producer")
	world.prepareRun("run-producer", arrival)
	request := worldLaunchRequest(arrival)

	if _, err := world.Launch(context.Background(), request); !errors.Is(err, adapter.ErrLaunchIndeterminate) {
		t.Fatalf("first launch error = %v, want indeterminate", err)
	}
	if got := len(world.truthSnapshot().ActiveExecutions); got != 1 {
		t.Fatalf("active executions = %d, want 1", got)
	}
	first := launchEffects(world)[0]
	if first.Operation != OperationProviderLaunch ||
		first.Command != EffectCommandAccepted ||
		first.Response != EffectResponseLost {
		t.Fatalf("ambiguous launch effect = %+v", first)
	}
	if first.CorrelationID != "run-producer" || first.CausationID != request.OperationKey {
		t.Fatalf("effect causation = %+v", first)
	}

	receipt, err := world.Launch(context.Background(), request)
	if err != nil {
		t.Fatalf("repeat launch: %v", err)
	}
	if !receipt.Duplicate {
		t.Fatal("repeat launch receipt is not marked duplicate")
	}
	if got := len(world.truthSnapshot().ActiveExecutions); got != 1 {
		t.Fatalf("repeat launch created %d active executions", got)
	}
	repeated := launchEffects(world)[1]
	if repeated.Command != EffectCommandDuplicate || repeated.Response != EffectResponseDelivered {
		t.Fatalf("duplicate launch effect = %+v", repeated)
	}

	conflict := request
	conflict.RequestHash = "sha256:conflict"
	if _, err := world.Launch(context.Background(), conflict); !errors.Is(err, adapter.ErrIdempotencyConflict) {
		t.Fatalf("conflicting launch error = %v, want idempotency conflict", err)
	}
	rejected := launchEffects(world)[2]
	if rejected.Command != EffectCommandRejected {
		t.Fatalf("rejected launch effect = %+v", rejected)
	}
}

func TestWorldEffectLedgerDistinguishesDelayedAndDuplicateResponses(t *testing.T) {
	cases := []struct {
		name         string
		action       scenario.FaultAction
		wantResponse EffectResponse
		wantError    error
	}{
		{name: "delayed", action: scenario.FaultDelayResponse, wantResponse: EffectResponseDelayed, wantError: adapter.ErrLaunchIndeterminate},
		{name: "duplicate", action: scenario.FaultDuplicateResponse, wantResponse: EffectResponseDuplicate},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			blueprint, err := scenario.LoadBlueprint("../scenario/scenarios/demos/artifact-warmth-restart.json")
			if err != nil {
				t.Fatalf("load Blueprint: %v", err)
			}
			tape, _, err := Compile(blueprint, CompileOptions{})
			if err != nil {
				t.Fatalf("compile Blueprint: %v", err)
			}
			delay := scenario.Duration(5 * time.Minute)
			tape.Faults = []scenario.FaultSpec{{
				ID: "fixture-response",
				Trigger: scenario.FaultTriggerSpec{
					Operation: OperationProviderLaunch,
					Run:       "producer",
					Attempt:   1,
				},
				Action: test.action,
				Delay:  &delay,
			}}
			if test.action != scenario.FaultDelayResponse {
				tape.Faults[0].Delay = nil
			}
			world, err := newSimulatedWorld(tape)
			if err != nil {
				t.Fatalf("open simulated world: %v", err)
			}
			arrival := findRunArrival(t, tape, "producer")
			world.prepareRun("run-producer", arrival)

			_, err = world.Launch(context.Background(), worldLaunchRequest(arrival))
			if !errors.Is(err, test.wantError) {
				t.Fatalf("launch error = %v, want %v", err, test.wantError)
			}
			effects := world.effectRecords()
			found := false
			for _, effect := range effects {
				found = found || effect.Response == test.wantResponse
			}
			if !found {
				t.Fatalf("effects have no %q response: %+v", test.wantResponse, effects)
			}
			if len(world.truthSnapshot().ActiveExecutions) != 1 {
				t.Fatal("accepted launch consequence was not preserved")
			}
		})
	}
}

// launchEffects is the ledger read back for one operation. Launching also
// records what the workload left on the host, so the launch commands are a
// subsequence of the ledger rather than its first entries.
func launchEffects(world *simulatedWorld) []EffectRecord {
	var launches []EffectRecord
	for _, effect := range world.effectRecords() {
		if effect.Operation == OperationProviderLaunch {
			launches = append(launches, effect)
		}
	}
	return launches
}

func openWorldFixture(t *testing.T, runName string) (*simulatedWorld, RunArrival) {
	t.Helper()
	blueprint, err := scenario.LoadBlueprint("../scenario/scenarios/demos/artifact-warmth-restart.json")
	if err != nil {
		t.Fatalf("load Blueprint: %v", err)
	}
	tape, _, err := Compile(blueprint, CompileOptions{})
	if err != nil {
		t.Fatalf("compile Blueprint: %v", err)
	}
	world, err := newSimulatedWorld(tape)
	if err != nil {
		t.Fatalf("open simulated world: %v", err)
	}
	return world, findRunArrival(t, tape, runName)
}

func offerByID(t *testing.T, offers []domain.OfferSnapshot, id string) domain.OfferSnapshot {
	t.Helper()
	for _, offer := range offers {
		if offer.ID == id {
			return offer
		}
	}
	t.Fatalf("offers have no %q: %+v", id, offers)
	return domain.OfferSnapshot{}
}

func TestWorldActualRuntimeComesFromTheTape(t *testing.T) {
	world, arrival := openWorldFixture(t, "producer")
	world.prepareRun("run-producer", arrival)
	request := worldLaunchRequest(arrival)
	_, _ = world.Launch(context.Background(), request)

	observation, err := world.Observe(context.Background(), adapter.ObserveRequest{
		WorkspaceID:    labWorkspace,
		ConnectionID:   "connection:lab",
		LaunchKey:      request.LaunchKey,
		OwnershipToken: request.OwnershipToken,
		RequestHash:    request.RequestHash,
	})
	if err != nil {
		t.Fatalf("observe running execution: %v", err)
	}
	if observation.Phase != adapter.ExternalPhaseRunning {
		t.Fatalf("initial phase = %q, want running", observation.Phase)
	}

	// The tape's runtime is measured from the moment the container started, and it
	// cannot have started before the image it runs finished arriving.
	world.setNow(world.nowTime().Add(arrival.ActualRuntime.Duration()))
	observation, err = world.Observe(context.Background(), adapter.ObserveRequest{
		WorkspaceID:    labWorkspace,
		ConnectionID:   "connection:lab",
		LaunchKey:      request.LaunchKey,
		OwnershipToken: request.OwnershipToken,
		RequestHash:    request.RequestHash,
	})
	if err != nil {
		t.Fatalf("observe execution still waiting on its image: %v", err)
	}
	if observation.Phase != adapter.ExternalPhaseRunning {
		t.Fatalf("phase after the runtime alone = %q, want a Run still owed its pull", observation.Phase)
	}

	world.setNow(world.executionHorizon())
	observation, err = world.Observe(context.Background(), adapter.ObserveRequest{
		WorkspaceID:    labWorkspace,
		ConnectionID:   "connection:lab",
		LaunchKey:      request.LaunchKey,
		OwnershipToken: request.OwnershipToken,
		RequestHash:    request.RequestHash,
	})
	if err != nil {
		t.Fatalf("observe completed execution: %v", err)
	}
	if observation.Phase != adapter.ExternalPhaseSucceeded {
		t.Fatalf("completed phase = %q, want succeeded", observation.Phase)
	}
	if observation.ObservedAt.Sub(world.nowTime()) > time.Nanosecond {
		t.Fatalf("observation time = %s, world now = %s", observation.ObservedAt, world.nowTime())
	}

	truth := world.truthSnapshot()
	if !hasArtifactReplica(truth.ArtifactReplicas, "artifact:model-checkpoint:v1", "rental-warm") {
		t.Fatalf("the producer left no copy on the host it ran on: %+v", truth.ArtifactReplicas)
	}
	if revision := cacheMountRevision(truth.CacheMounts, "rental-warm", "compiler-cache"); revision != 2 {
		t.Fatalf("mutable Cache Mount revision = %d, want 2", revision)
	}
	assertEffect(
		t,
		world.effectRecords(),
		OperationArtifactReplicated,
		"run-producer",
		EffectCommandAccepted,
		EffectResponseDelivered,
	)
}

func worldLaunchRequest(arrival RunArrival) adapter.LaunchRequest {
	return worldLaunchRequestOn(arrival, "rental-warm")
}

func worldLaunchRequestOn(arrival RunArrival, offerID string) adapter.LaunchRequest {
	return worldLaunchAttempt(arrival, offerID, 1)
}

func worldLaunchAttempt(arrival RunArrival, offerID string, attempt int) adapter.LaunchRequest {
	ordinal := fmt.Sprint(attempt)
	return adapter.LaunchRequest{
		OperationKey:              "launch:producer:" + ordinal,
		RequestHash:               "sha256:producer-launch-" + ordinal,
		WorkspaceID:               labWorkspace,
		RunID:                     "run-producer",
		AttemptID:                 "attempt-producer-" + ordinal,
		OwnershipToken:            "owner-producer-" + ordinal,
		LaunchKey:                 "launch-producer-" + ordinal,
		CleanupLocator:            "cleanup-producer-" + ordinal,
		Image:                     arrival.Request.Image,
		Platform:                  domain.Platform{OS: "linux", Architecture: "amd64"},
		SelectedOfferSnapshotID:   offerID,
		SelectedOfferConnectionID: "connection:lab",
		SelectedOfferAdapterType:  "lab",
		SelectedOfferNativeRef:    offerID,
		Disposition:               domain.DispositionRelease,
	}
}

func hasArtifactReplica(replicas []ArtifactReplica, artifactID, offerID string) bool {
	for _, replica := range replicas {
		if replica.ArtifactID == artifactID && replica.OfferID == offerID {
			return true
		}
	}
	return false
}

func cacheMountRevision(mounts []CacheMountState, offerID, name string) uint64 {
	for _, mount := range mounts {
		if mount.OfferID == offerID && mount.Name == name {
			return mount.Revision
		}
	}
	return 0
}

// TestARentalHoldsWhatItRanOnlyOnceThePullCompletes is the honest version of
// "running warms the host": the content is on the machine when its bytes have
// arrived, and not at the instant the container was dispatched.
func TestARentalHoldsWhatItRanOnlyOnceThePullCompletes(t *testing.T) {
	world, arrival := openWorldFixture(t, "producer")
	world.prepareRun("run-producer", arrival)

	if _, err := world.Launch(context.Background(), worldLaunchRequest(arrival)); !errors.Is(err, adapter.ErrLaunchIndeterminate) {
		t.Fatalf("launch: %v", err)
	}

	dispatched := offerByID(t, world.truthSnapshot().Offers, "rental-warm").Images
	if dispatched.Holds(domain.ReferenceDigest(arrival.Request.Image)) {
		t.Fatalf("the host holds the image at dispatch, before its bytes moved: %+v", dispatched)
	}
	world.setNow(world.nowTime().Add(time.Hour))
	held := offerByID(t, world.truthSnapshot().Offers, "rental-warm").Images
	if !held.Holds(domain.ReferenceDigest(arrival.Request.Image)) {
		t.Fatalf("Rental that ran %q does not hold it whole: %+v", arrival.Request.Image, held)
	}
	for _, layer := range world.images[arrival.Request.Image].Layers {
		if !held.HoldsLayer(domain.ImageLayer{Digest: layer.Digest}) {
			t.Fatalf("Rental that ran the image does not hold layer %s: %+v", layer.Digest, held)
		}
	}
	assertEffect(
		t,
		world.effectRecords(),
		OperationImagePull,
		"run-producer",
		EffectCommandAccepted,
		EffectResponseDelivered,
	)
	// The retention is recorded where the content appeared, which is the only
	// record that can explain what the host holds.
	if retained := retentionEffects(world, "rental-warm"); len(retained) != 1 {
		t.Fatalf("the host holds the image with %d retentions recorded, want exactly one", len(retained))
	}
}

// TestAWarmStartRecordsAPullThatMovedNothing keeps the ledger able to tell a
// warm placement from a cold one. Reading back an accepted whole-image pull for
// a launch where zero bytes moved is what would make phase 6's waterfall lie.
func TestAWarmStartRecordsAPullThatMovedNothing(t *testing.T) {
	world, arrival := openWorldFixture(t, "producer")
	world.prepareRun("run-producer", arrival)
	if _, err := world.Launch(context.Background(), worldLaunchRequest(arrival)); !errors.Is(err, adapter.ErrLaunchIndeterminate) {
		t.Fatalf("first launch: %v", err)
	}
	world.setNow(world.nowTime().Add(time.Hour))
	// The first Booking released, so the same image can run here again.
	world.setOfferAvailable("rental-warm", true)

	if _, err := world.Launch(context.Background(), worldLaunchAttempt(arrival, "rental-warm", 2)); err != nil {
		t.Fatalf("second launch: %v", err)
	}

	pull := lastEffectConsequence(t, world, OperationImagePull)
	if fetched := pull["fetched_digests"]; len(fetched.([]any)) != 0 {
		t.Fatalf("a launch on a host that already held the image fetched %v", fetched)
	}
	if bytes := pull["fetched_bytes"]; bytes != float64(0) {
		t.Fatalf("a warm start moved %v bytes", bytes)
	}
}

func TestOneShotCapacityKeepsNothingItPulled(t *testing.T) {
	world, arrival := openWorldFixture(t, "producer")
	world.prepareRun("run-producer", arrival)

	if _, err := world.Launch(context.Background(), worldLaunchRequestOn(arrival, "fresh-4090")); !errors.Is(err, adapter.ErrLaunchIndeterminate) {
		t.Fatalf("launch: %v", err)
	}
	world.setNow(world.nowTime().Add(24 * time.Hour))

	held := offerByID(t, world.truthSnapshot().Offers, "fresh-4090").Images
	if len(held.ImageDigests) > 0 || len(held.LayerDigests) > 0 {
		t.Fatalf("capacity Mercator does not keep held %+v", held)
	}
	if retained := retentionEffects(world, "fresh-4090"); len(retained) > 0 {
		t.Fatalf("capacity Mercator does not keep recorded keeping content: %v", retained)
	}
	pull := lastEffectConsequence(t, world, OperationImagePull)
	if fetched := pull["fetched_digests"]; len(fetched.([]any)) == 0 {
		t.Fatal("the one-shot execution fetched nothing, so it never paid for the image at all")
	}
}

// TestAnAbandonedPullLeavesNothingBehind is what stops the ledger from recording
// content a host never received. An execution released while its image is still
// moving cancels the transfer, so no retention is ever recorded for it and a Run
// Bundle exported mid-pull cannot claim 18GB landed somewhere.
func TestAnAbandonedPullLeavesNothingBehind(t *testing.T) {
	world, arrival := openWorldFixture(t, "producer")
	world.prepareRun("run-producer", arrival)
	request := worldLaunchRequest(arrival)
	if _, err := world.Launch(context.Background(), request); !errors.Is(err, adapter.ErrLaunchIndeterminate) {
		t.Fatalf("launch: %v", err)
	}

	if _, err := world.Terminate(context.Background(), adapter.TerminateRequest{
		OperationKey:      "terminate:producer:1",
		RequestHash:       "sha256:producer-terminate",
		LaunchKey:         request.LaunchKey,
		OwnershipToken:    request.OwnershipToken,
		LaunchRequestHash: request.RequestHash,
	}); err != nil {
		t.Fatalf("terminate: %v", err)
	}
	world.setNow(world.nowTime().Add(time.Hour))

	held := offerByID(t, world.truthSnapshot().Offers, "rental-warm").Images
	if held.Holds(arrival.Request.Image) {
		t.Fatalf("a pull nothing was waiting on still warmed the host: %+v", held)
	}
	if retained := retentionEffects(world, "rental-warm"); len(retained) > 0 {
		t.Fatalf("the ledger records content a cancelled transfer never delivered: %v", retained)
	}
}

func retentionEffects(world *simulatedWorld, offerID string) []EffectRecord {
	var retained []EffectRecord
	for _, effect := range world.effectRecords() {
		var host struct {
			OfferID string `json:"offer_id"`
		}
		if effect.Operation != OperationImageRetained || json.Unmarshal(effect.Request, &host) != nil || host.OfferID != offerID {
			continue
		}
		retained = append(retained, effect)
	}
	return retained
}

func lastEffectConsequence(t *testing.T, world *simulatedWorld, operation string) map[string]any {
	t.Helper()
	var consequence map[string]any
	for _, effect := range world.effectRecords() {
		if effect.Operation != operation {
			continue
		}
		if err := json.Unmarshal(effect.Consequence, &consequence); err != nil {
			t.Fatalf("decode %s consequence: %v", operation, err)
		}
	}
	if consequence == nil {
		t.Fatalf("the ledger records no %s", operation)
	}
	return consequence
}

// TestLocalityProvenanceRejectsOneShotCapacityThatKeptItsImage proves the
// second half of the guard independently: an explained pull is still not a
// licence for capacity Mercator does not keep to hold anything.
func TestLocalityProvenanceRejectsOneShotCapacityThatKeptItsImage(t *testing.T) {
	observation := InvariantObservation{
		World: WorldTruthSnapshot{Offers: []domain.OfferSnapshot{{
			ID:     "oneshot-container",
			Kind:   domain.OfferKindProvisionable,
			Lane:   domain.LaneEphemeral,
			Images: domain.ImageInventory{Known: true, LayerDigests: []string{"sha256:pulled"}},
		}}},
		Effects: []EffectRecord{{
			Operation:   OperationImageRetained,
			Command:     EffectCommandAccepted,
			Request:     []byte(`{"offer_id":"oneshot-container"}`),
			Consequence: []byte(`{"retained_digests":["sha256:pulled"]}`),
		}},
		SeededLocality: map[string]map[string]bool{},
	}

	err := localityProvenance(observation)

	if err == nil {
		t.Fatal("one-shot capacity was allowed to keep what it pulled")
	}
}

// TestIdempotentExternalCommandsCoversImagePulls keeps the pull inside the
// idempotency guard. Leaving an image on a host is a change to the world, so
// one launch's pull, however many times it is reported, has one consequence.
// A later launch of the same image on the same host is a different pull, which
// is why the operation is keyed by launch: the second one legitimately moves
// nothing.
func TestIdempotentExternalCommandsCoversImagePulls(t *testing.T) {
	pull := func(fetched string) EffectRecord {
		return EffectRecord{
			Operation:   OperationImagePull,
			OperationID: "image-pull/launch-producer-1/trainer",
			Command:     EffectCommandAccepted,
			Consequence: []byte(`{"fetched_digests":[` + fetched + `]}`),
		}
	}

	err := idempotentExternalCommands(InvariantObservation{
		Effects: []EffectRecord{pull(`"sha256:a"`), pull("")},
	})

	if err == nil {
		t.Fatal("one launch's pull was reported with two different consequences and accepted")
	}
}

// TestLocalityProvenanceAllowsAHostToLoseWhatItHeld keeps locality decay out of
// the safety rules. Content is reclaimed under disk pressure and machines are
// replaced, so a host holding less than the World Tape put there is a fact to
// state rather than a control-plane failure. The host still holds one seeded
// digest and one it retained, so the rule inspects everything it has to inspect
// while a third digest goes missing.
func TestLocalityProvenanceAllowsAHostToLoseWhatItHeld(t *testing.T) {
	observation := InvariantObservation{
		World: WorldTruthSnapshot{Offers: []domain.OfferSnapshot{{
			ID:   "rental-warm",
			Kind: domain.OfferKindStanding,
			Lane: domain.LaneReusable,
			Images: domain.ImageInventory{
				Known:        true,
				ImageDigests: []string{"trainer@sha256:ran-here"},
				LayerDigests: []string{"sha256:seeded"},
			},
		}}},
		Effects: []EffectRecord{{
			Operation:   OperationImageRetained,
			Command:     EffectCommandAccepted,
			Request:     []byte(`{"offer_id":"rental-warm"}`),
			Consequence: []byte(`{"retained_digests":["trainer@sha256:ran-here"]}`),
		}},
		SeededLocality: map[string]map[string]bool{
			"rental-warm": {"sha256:seeded": true, "sha256:reclaimed": true},
		},
	}

	err := localityProvenance(observation)

	if err != nil {
		t.Fatalf("a host that lost content it held was reported as a violation: %v", err)
	}
}

// TestLocalityProvenanceCoversContentAHostFetchedAndNeverAssembled closes the
// gap the pulled-but-not-unpacked vocabulary opened. Content is content whether
// or not a container can be started on it, so a host reporting an image nothing
// delivered is the same violation under either name. A rule that only inspected
// what was ready to run would have left the other half of every inventory
// unpoliced.
func TestLocalityProvenanceCoversContentAHostFetchedAndNeverAssembled(t *testing.T) {
	observation := InvariantObservation{
		World: WorldTruthSnapshot{Offers: []domain.OfferSnapshot{{
			ID:   "rental-warm",
			Kind: domain.OfferKindStanding,
			Lane: domain.LaneReusable,
			Images: domain.ImageInventory{
				Known:              true,
				PulledImageDigests: []string{"trainer@sha256:never-delivered"},
			},
		}}},
		SeededLocality: map[string]map[string]bool{"rental-warm": {}},
	}

	err := localityProvenance(observation)

	if err == nil {
		t.Fatal("a host reported holding unassembled content nothing delivered and nothing objected")
	}
}

// TestSimulatedRegistryRefusesTheSameThreeWaysARealOneDoes keeps the Lab honest
// about what it is standing in for. A registry that collapses "nobody pushed
// this", "there is no build for this platform", and "your credentials were
// refused" into one empty manifest is a world that cannot express the failure an
// operator most often has to fix.
func TestSimulatedRegistryRefusesTheSameThreeWaysARealOneDoes(t *testing.T) {
	resolvable := "trainer@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	unresolvable := "mystery@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	forbidden := "private@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	world, err := newSimulatedWorld(WorldTape{
		Seed:  "registry-answers",
		Start: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		InitialWorld: scenario.WorldSpec{Images: map[string]scenario.ImageSpec{
			resolvable: {Layers: []scenario.LayerSpec{{
				Digest: "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
				DiffID: "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
				Size:   scenario.ByteSize(1000),
			}}},
			unresolvable: {Registry: scenario.RegistryUnresolvable},
			forbidden:    {Registry: scenario.RegistryUnauthorized},
		}},
	})
	if err != nil {
		t.Fatalf("open simulated world: %v", err)
	}

	testCases := []struct {
		name  string
		image string
		want  error
	}{
		{"an image nobody pushed", "absent@sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", ociresolver.ErrImageUnknown},
		{"an image with no resolvable manifest", unresolvable, ociresolver.ErrManifestUnresolvable},
		{"an image the credentials cannot read", forbidden, ociresolver.ErrUnauthorized},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := world.ResolveManifest(context.Background(), testCase.image, domain.Platform{OS: "linux", Architecture: "amd64"})

			if !errors.Is(err, testCase.want) {
				t.Fatalf("resolve error = %v, want %v", err, testCase.want)
			}
		})
	}

	manifest, err := world.ResolveManifest(context.Background(), resolvable, domain.Platform{OS: "linux", Architecture: "amd64"})
	if err != nil {
		t.Fatalf("resolve a readable image: %v", err)
	}
	if len(manifest.Layers) != 1 || manifest.Layers[0].DiffID == "" {
		t.Fatalf("a readable manifest states both digest spaces, got %+v", manifest.Layers)
	}
}
