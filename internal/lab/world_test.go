package lab

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/scenario"
)

func TestWorldTruthChangesDoNotLeakIntoObservedOffers(t *testing.T) {
	world, arrival := openWorldFixture(t, "producer")
	world.prepareRun("run-producer", arrival)

	before, err := world.ListOffers(context.Background(), adapter.OfferRequest{WorkspaceID: labWorkspace})
	if err != nil {
		t.Fatalf("list observed offers: %v", err)
	}
	if !offerByID(t, before, "rental-warm").Capacity.Available {
		t.Fatal("fixture rental is not initially observed available")
	}

	world.setTruthOfferAvailable("rental-warm", false)

	truth := world.truthSnapshot()
	if offerByID(t, truth.Offers, "rental-warm").Capacity.Available {
		t.Fatal("truth rental stayed available")
	}
	stale, err := world.ListOffers(context.Background(), adapter.OfferRequest{WorkspaceID: labWorkspace})
	if err != nil {
		t.Fatalf("list stale observed offers: %v", err)
	}
	if !offerByID(t, stale, "rental-warm").Capacity.Available {
		t.Fatal("truth leaked into observed offers before delivery")
	}

	world.deliverOfferObservation("rental-warm")

	delivered, err := world.ListOffers(context.Background(), adapter.OfferRequest{WorkspaceID: labWorkspace})
	if err != nil {
		t.Fatalf("list delivered observed offers: %v", err)
	}
	if offerByID(t, delivered, "rental-warm").Capacity.Available {
		t.Fatal("delivered observation did not expose unavailable capacity")
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
	first := world.effectRecords()[0]
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
	repeated := world.effectRecords()[1]
	if repeated.Command != EffectCommandDuplicate || repeated.Response != EffectResponseDelivered {
		t.Fatalf("duplicate launch effect = %+v", repeated)
	}

	conflict := request
	conflict.RequestHash = "sha256:conflict"
	if _, err := world.Launch(context.Background(), conflict); !errors.Is(err, adapter.ErrIdempotencyConflict) {
		t.Fatalf("conflicting launch error = %v, want idempotency conflict", err)
	}
	rejected := world.effectRecords()[2]
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

	world.setNow(world.nowTime().Add(arrival.ActualRuntime.Duration()))
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
		t.Fatalf("producer output Artifact was not published: %+v", truth.ArtifactReplicas)
	}
	if revision := cacheMountRevision(truth.CacheMounts, "rental-warm", "compiler-cache"); revision != 2 {
		t.Fatalf("mutable Cache Mount revision = %d, want 2", revision)
	}
	assertEffect(
		t,
		world.effectRecords(),
		OperationArtifactPut,
		"run-producer",
		EffectCommandAccepted,
		EffectResponseDelivered,
	)
}

func worldLaunchRequest(arrival RunArrival) adapter.LaunchRequest {
	return adapter.LaunchRequest{
		OperationKey:              "launch:producer:1",
		RequestHash:               "sha256:producer-launch",
		WorkspaceID:               labWorkspace,
		RunID:                     "run-producer",
		AttemptID:                 "attempt-producer-1",
		OwnershipToken:            "owner-producer-1",
		LaunchKey:                 "launch-producer-1",
		CleanupLocator:            "cleanup-producer-1",
		Image:                     arrival.Request.Image,
		Platform:                  domain.Platform{OS: "linux", Architecture: "amd64"},
		SelectedOfferSnapshotID:   "rental-warm",
		SelectedOfferConnectionID: "connection:lab",
		SelectedOfferAdapterType:  "lab",
		SelectedOfferNativeRef:    "rental-warm",
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
