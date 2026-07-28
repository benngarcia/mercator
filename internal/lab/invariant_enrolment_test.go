package lab

import (
	"strings"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
)

// The machine and lease every case here is about, and the enrolment that would
// make them reachable. They are named once because the whole rule turns on
// whether one record in the ledger names this machine and this lease.
const (
	strandedMachine = "simcloud-4090-a17c"
	strandedRental  = "rnt_never_enrolled"
	strandedDataset = "dataset-imagenet"
	strandedLayer   = "sha256:nobody-enumerated-this"
)

// TestEveryClauseOfTheEnrolledRuntimeRuleCanFail reads
// safety.reusable_capacity_has_an_enrolled_runtime the way every law here has to
// be readable. Four different things a machine can accumulate are four clauses,
// and the registry's single deliberate case drives two of them, so the other two
// would survive being deleted.
//
// Removing the enrolment the Lab world records for its own Rentals fails the
// corpus on the image clause and the cache clause and the queue clause, which is
// the proof those three are load-bearing. Nothing a World Tape can state reaches
// the Artifact clause on its own: every Blueprint holding a copy on a Rental is
// holding image content there too, so the image clause answers first and the copy
// clause is never asked.
func TestEveryClauseOfTheEnrolledRuntimeRuleCanFail(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	cases := map[string]func(*InvariantObservation){
		"an inventory nobody enumerated": func(observation *InvariantObservation) {
			observation.World.Offers = []domain.OfferSnapshot{strandedOffer(func(offer *domain.OfferSnapshot) {
				offer.Images = domain.ImageInventory{Known: true, LayerDigests: []string{strandedLayer}}
			})}
		},
		"a cache no workload wrote": func(observation *InvariantObservation) {
			observation.World.Offers = []domain.OfferSnapshot{strandedOffer(func(offer *domain.OfferSnapshot) {
				offer.Caches = domain.CacheInventory{Known: true, ObservedAt: now, Mounts: []domain.CacheMount{{
					WorkspaceID: labWorkspace,
					Name:        "compiler-cache",
				}}}
			})}
		},
		"a copy nothing fetched": func(observation *InvariantObservation) {
			observation.World.Offers = []domain.OfferSnapshot{strandedOffer(func(offer *domain.OfferSnapshot) {
				offer.Artifacts = domain.ArtifactInventory{Known: true, ObservedAt: now, Replicas: []domain.ArtifactReplica{{
					ArtifactID:    strandedDataset,
					ContentDigest: "sha256:whatever-is-on-the-disk",
					State:         domain.ArtifactReplicaVerified,
					VerifiedAt:    now,
				}}}
			})}
		},
		"a queue nothing can dispatch": func(observation *InvariantObservation) {
			observation.RentalSchedules[strandedRental] = domain.RentalSchedule{
				RentalID: strandedRental,
				Version:  2,
				Bookings: []domain.ScheduledBooking{
					{Booking: domain.Booking{ID: "booking-running", State: domain.BookingStateRunning, ScheduleVersion: 1}},
					{Booking: domain.Booking{ID: "booking-waiting", State: domain.BookingStateQueued, ScheduleVersion: 2}},
				},
			}
		},
	}

	for name, state := range cases {
		t.Run(name, func(t *testing.T) {
			observation := enrolmentObservation(now)
			state(&observation)

			if err := reusableCapacityHasAnEnrolledRuntime(observation); err == nil {
				t.Fatal("a machine nothing enrolled on accumulated something and nothing objected")
			}
		})
	}
}

// TestAnEnrolledMachineAccumulatesFreely is the other half, and the half that
// stops the rule above from being a rule against warmth. A machine an agent
// opened a session on may hold everything the four clauses forbid, because the
// agent is what did all four.
func TestAnEnrolledMachineAccumulatesFreely(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	observation := enrolmentObservation(now)
	observation.Effects = []EffectRecord{enrolmentEffect()}
	observation.World.Offers = []domain.OfferSnapshot{strandedOffer(func(offer *domain.OfferSnapshot) {
		offer.Images = domain.ImageInventory{Known: true, LayerDigests: []string{strandedLayer}}
		offer.Caches = domain.CacheInventory{Known: true, ObservedAt: now, Mounts: []domain.CacheMount{{
			WorkspaceID: labWorkspace,
			Name:        "compiler-cache",
		}}}
		offer.Artifacts = domain.ArtifactInventory{Known: true, ObservedAt: now, Replicas: []domain.ArtifactReplica{{
			ArtifactID: strandedDataset,
			State:      domain.ArtifactReplicaVerified,
			VerifiedAt: now,
		}}}
	})}
	observation.RentalSchedules[strandedRental] = domain.RentalSchedule{
		RentalID: strandedRental,
		Version:  2,
		Bookings: []domain.ScheduledBooking{
			{Booking: domain.Booking{ID: "booking-running", State: domain.BookingStateRunning, ScheduleVersion: 1}},
			{Booking: domain.Booking{ID: "booking-waiting", State: domain.BookingStateQueued, ScheduleVersion: 2}},
		},
	}

	if err := reusableCapacityHasAnEnrolledRuntime(observation); err != nil {
		t.Fatalf("a machine Mercator has a session to may hold what its agent put there: %v", err)
	}
}

// TestContentTheWorldTapeSeededNeedsNoEnrolment is the exemption stated as its own
// case, because it is what keeps the rule from refusing every borrowed host in the
// corpus. A machine Mercator rents a slot on may already be sitting on the image
// and on the dataset, and that is a fact about the host rather than something an
// agent of Mercator's fetched.
func TestContentTheWorldTapeSeededNeedsNoEnrolment(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	observation := enrolmentObservation(now)
	observation.SeededLocality["local-docker"] = map[string]bool{strandedLayer: true}
	observation.SeededReplicas["local-docker"] = map[string]bool{strandedDataset: true}
	observation.World.Offers = []domain.OfferSnapshot{{
		ID:        "local-docker",
		MachineID: "daemon-1",
		Kind:      domain.OfferKindStanding,
		Lane:      domain.LaneEphemeral,
		Images:    domain.ImageInventory{Known: true, LayerDigests: []string{strandedLayer}},
		Artifacts: domain.ArtifactInventory{Known: true, ObservedAt: now, Replicas: []domain.ArtifactReplica{{
			ArtifactID: strandedDataset,
			State:      domain.ArtifactReplicaVerified,
			VerifiedAt: now,
		}}},
	}}

	if err := reusableCapacityHasAnEnrolledRuntime(observation); err != nil {
		t.Fatalf("a borrowed host was refused the content the World Tape put on it: %v", err)
	}
}

// TestAListingThatAccumulatedAnythingNamesNoMachineToHaveEnrolledOn is the reading
// the rule gives a marketplace template, and it is the reason the clause is keyed
// on the machine handle rather than on the offer. A listing describes a machine
// that does not exist yet, so there is no handle any enrolment could have named,
// and content on one is content on a host nothing has allocated.
func TestAListingThatAccumulatedAnythingNamesNoMachineToHaveEnrolledOn(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	observation := enrolmentObservation(now)
	observation.Effects = []EffectRecord{enrolmentEffect()}
	observation.World.Offers = []domain.OfferSnapshot{{
		ID:     "listing-nobody-rented",
		Kind:   domain.OfferKindProvisionable,
		Lane:   domain.LaneReusable,
		Images: domain.ImageInventory{Known: true, LayerDigests: []string{strandedLayer}},
	}}

	err := reusableCapacityHasAnEnrolledRuntime(observation)

	if err == nil {
		t.Fatal("a listing for a machine nobody allocated reported an inventory and nothing objected")
	}
	if want := "names no machine, because the machine does not exist yet"; !strings.Contains(err.Error(), want) {
		t.Fatalf("violation = %q, want it to say %q", err, want)
	}
}

// strandedOffer is the machine every clause is asked about: standing capacity in
// the reusable lane, carrying a lease, on a host no agent ever reached. The offer
// is the shape safety.locality_provenance and
// safety.a_rental_identity_is_capacity_mercator_holds both accept, which is the
// point: nothing else in the registry can tell this machine from a healthy one.
func strandedOffer(state func(*domain.OfferSnapshot)) domain.OfferSnapshot {
	offer := domain.OfferSnapshot{
		ID:        "rental-nobody-is-on",
		MachineID: strandedMachine,
		Kind:      domain.OfferKindStanding,
		Lane:      domain.LaneReusable,
		RentalID:  strandedRental,
	}
	state(&offer)
	return offer
}

// enrolmentEffect is the ledger entry that makes the stranded machine reachable,
// in the shape the Lab world writes for the Rentals it holds.
func enrolmentEffect() EffectRecord {
	return EffectRecord{
		Operation:   OperationNodeEnrolled,
		OperationID: "nod_stranded/generation-1",
		Command:     EffectCommandAccepted,
		Request: []byte(`{"machine_id":"` + strandedMachine +
			`","rental_id":"` + strandedRental +
			`","node_id":"nod_stranded","generation":1}`),
		Consequence: []byte(`{"node_id":"nod_stranded","fencing_token":1}`),
	}
}

func enrolmentObservation(now time.Time) InvariantObservation {
	return InvariantObservation{
		StartedAt:       now,
		Now:             now,
		World:           WorldTruthSnapshot{At: now},
		Workloads:       map[string]domain.WorkloadRevision{},
		RentalSchedules: map[string]domain.RentalSchedule{},
		RunRequirements: map[string]RunArrival{},
		ArtifactCatalog: map[string]domain.ArtifactVersion{},
		SeededLocality:  map[string]map[string]bool{},
		SeededReplicas:  map[string]map[string]bool{},
	}
}
