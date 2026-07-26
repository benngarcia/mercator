package prediction_test

import (
	"testing"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/prediction"
)

// TestAMachineRepublishedUnderANewListingKeepsItsLaunches is the case the whole
// key exists for. A marketplace mints an ask ID per search, so the machine
// measured yesterday arrives today under a number nothing has ever seen, and its
// own launches have to answer for it anyway.
func TestAMachineRepublishedUnderANewListingKeepsItsLaunches(t *testing.T) {
	yesterday := marketplaceAsk("off_vast_11111", "machine-77")
	today := marketplaceAsk("off_vast_99999", "machine-77")
	history := prediction.NewHistory([]prediction.Observation{
		readiness(yesterday, 30),
		readiness(yesterday, 50),
	})

	answer := history.Predict(domain.CandidateIdentityOf(today, "sha256:image"), domain.StageApplicationReady)

	if answer.Level != domain.LevelExactCandidate || answer.SampleCount != 2 {
		t.Fatalf("the machine's own two launches answered %+v", answer)
	}
	if answer.P50 != 40 || answer.P90 != 48 {
		t.Fatalf("two launches of 30s and 50s produced p50 %v and p90 %v", answer.P50, answer.P90)
	}
}

// TestAnUnmeasuredMachineFallsThroughTheDeclaredLevels walks the ladder. The
// candidate has no launches of its own, so the answer comes from the province it
// is in, then from its provider, and a provider nobody has measured is answered
// by nothing at all.
func TestAnUnmeasuredMachineFallsThroughTheDeclaredLevels(t *testing.T) {
	measured := marketplaceAsk("off_vast_11111", "machine-77")
	history := prediction.NewHistory([]prediction.Observation{readiness(measured, 60)})

	neighbour := marketplaceAsk("off_vast_22222", "machine-88")
	elsewhere := marketplaceAsk("off_vast_33333", "machine-91")
	elsewhere.Region = "EU-DE"
	stranger := marketplaceAsk("off_pool_44444", "machine-92")
	stranger.AdapterType = "simpool"

	for name, expected := range map[string]struct {
		offer domain.OfferSnapshot
		level domain.PredictionLevel
	}{
		"a machine in the same place":    {neighbour, domain.LevelProviderAndRegion},
		"a machine of the same provider": {elsewhere, domain.LevelProvider},
		"a machine of another provider":  {stranger, domain.LevelPrior},
	} {
		t.Run(name, func(t *testing.T) {
			answer := history.Predict(
				domain.CandidateIdentityOf(expected.offer, "sha256:image"),
				domain.StageApplicationReady,
			)
			if answer.Level != expected.level {
				t.Fatalf("answered at %q from %d samples, want %q", answer.Level, answer.SampleCount, expected.level)
			}
		})
	}
}

// TestCapacityThatCannotRecurIsNeverAnExactCandidate is the reading half of the
// same key. A one-shot pool publishing nothing but its provider gets no key of
// its own, so what it did is still worth knowing about that provider's one-shot
// lane and can never be reported as evidence about this exact candidate.
func TestCapacityThatCannotRecurIsNeverAnExactCandidate(t *testing.T) {
	pool := domain.OfferSnapshot{ID: "off_pool_7f3a", AdapterType: "simpool", Lane: domain.LaneEphemeral}
	identity := domain.CandidateIdentityOf(pool, "sha256:image")

	history := prediction.NewHistory([]prediction.Observation{{
		Candidate: identity, Stage: domain.StageApplicationReady, Seconds: 60,
	}})

	answer := history.Predict(identity, domain.StageApplicationReady)
	if answer.Level != domain.LevelProvider {
		t.Fatalf("a one-shot execution of this provider was answered %+v", answer)
	}
	if answer.Key != identity.ProviderKey() {
		t.Fatalf("the answer was read under %q, and this listing recurs as %q", answer.Key, identity.ProviderKey())
	}
}

// TestOneMachinesStagesAreLearnedApart is why a bucket carries the stage. The
// stages of a launch are different work with different causes, so a machine
// measured coming up says nothing about what it spends creating a container.
func TestOneMachinesStagesAreLearnedApart(t *testing.T) {
	ask := marketplaceAsk("off_vast_11111", "machine-77")
	history := prediction.NewHistory([]prediction.Observation{readiness(ask, 60)})

	identity := domain.CandidateIdentityOf(ask, "sha256:image")
	if answer := history.Predict(identity, domain.StageContainerStart); answer.Answered() {
		t.Fatalf("a readiness measurement answered the container start: %+v", answer)
	}
}

// TestAContentStageIsLearnedPerImageAndAMachineStageIsNot holds the other half of
// the key. What a machine spends pulling is a property of what it pulled; what it
// spends booting is not, and splitting the second per image would leave one
// machine's boot history in as many buckets as the fleet has images.
func TestAContentStageIsLearnedPerImageAndAMachineStageIsNot(t *testing.T) {
	ask := marketplaceAsk("off_vast_11111", "machine-77")
	trained := domain.CandidateIdentityOf(ask, "sha256:trainer")
	scored := domain.CandidateIdentityOf(ask, "sha256:scorer")
	history := prediction.NewHistory([]prediction.Observation{
		{Candidate: trained, Stage: domain.StageApplicationReady, Seconds: 60},
		{Candidate: trained, Stage: domain.StageBoot, Seconds: 200},
	})

	if answer := history.Predict(scored, domain.StageApplicationReady); answer.Level == domain.LevelExactCandidate {
		t.Fatalf("another image's readiness answered as this exact candidate: %+v", answer)
	}
	if answer := history.Predict(scored, domain.StageBoot); answer.Level != domain.LevelExactCandidate || answer.P50 != 200 {
		t.Fatalf("the machine's own boot did not answer for a second image: %+v", answer)
	}
}

// TestALaunchWithNoReadinessMeasuresNothing holds what an Observation is made
// of. A workload that never reported it could do work leaves the stage with no
// actual, and a zero there teaches a fleet that every application is serving the
// instant its process exists.
func TestALaunchWithNoReadinessMeasuresNothing(t *testing.T) {
	ask := marketplaceAsk("off_vast_11111", "machine-77")
	launch := prediction.Launch{Candidate: domain.CandidateIdentityOf(ask, "sha256:image")}

	if observations := launch.Observations(); len(observations) != 0 {
		t.Fatalf("a launch nobody timed measured %+v", observations)
	}
}

func marketplaceAsk(offerID, machineID string) domain.OfferSnapshot {
	return domain.OfferSnapshot{
		ID:          offerID,
		NativeRef:   offerID,
		MachineID:   machineID,
		AdapterType: "simvast",
		Region:      "US-CA",
		Kind:        domain.OfferKindProvisionable,
		Lane:        domain.LaneEphemeral,
		Resources: domain.ResourceInventory{Accelerators: []domain.AcceleratorInventory{{
			Vendor: "NVIDIA", CanonicalModel: "nvidia-a100", Count: 8, MemoryBytes: 80_000_000_000,
		}}},
	}
}

func readiness(offer domain.OfferSnapshot, seconds float64) prediction.Observation {
	return prediction.Observation{
		Candidate: domain.CandidateIdentityOf(offer, "sha256:image"),
		Stage:     domain.StageApplicationReady,
		Seconds:   seconds,
	}
}
