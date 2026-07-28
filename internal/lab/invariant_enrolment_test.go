package lab

import (
	"fmt"
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
// case, because it is what keeps the rule from refusing every Rental in the
// corpus. A machine Mercator holds may already have been sitting on the image and
// on the dataset when the world's clock started, and that is a fact about the host
// rather than something an agent of Mercator's fetched.
func TestContentTheWorldTapeSeededNeedsNoEnrolment(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	observation := enrolmentObservation(now)
	observation.SeededLocality["rental-nobody-is-on"] = map[string]bool{strandedLayer: true}
	observation.SeededReplicas["rental-nobody-is-on"] = map[string]bool{strandedDataset: true}
	observation.World.Offers = []domain.OfferSnapshot{strandedOffer(func(offer *domain.OfferSnapshot) {
		offer.Images = domain.ImageInventory{Known: true, LayerDigests: []string{strandedLayer}}
		offer.Artifacts = domain.ArtifactInventory{Known: true, ObservedAt: now, Replicas: []domain.ArtifactReplica{{
			ArtifactID: strandedDataset,
			State:      domain.ArtifactReplicaVerified,
			VerifiedAt: now,
		}}}
	})}

	if err := reusableCapacityHasAnEnrolledRuntime(observation); err != nil {
		t.Fatalf("a machine was refused the content the World Tape put on it: %v", err)
	}
}

// TestCapacityThatKeepsNothingIsAnsweredByTheRuleAboutKeeping is the division of
// labour between this rule and safety.locality_provenance, and it is the reason
// this rule asks only capacity that keeps what it runs. A listing describes a
// machine that does not exist yet and a one-shot host holds nothing once its
// workload exits, so neither has an agent to enrol: reading them here would
// answer first with a remedy the ephemeral lane must never apply, and the rule
// that owns them names the reason that is actually theirs.
func TestCapacityThatKeepsNothingIsAnsweredByTheRuleAboutKeeping(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	for name, offer := range map[string]domain.OfferSnapshot{
		"a listing nobody allocated": {
			ID:     "listing-nobody-rented",
			Kind:   domain.OfferKindProvisionable,
			Lane:   domain.LaneReusable,
			Images: domain.ImageInventory{Known: true, LayerDigests: []string{strandedLayer}},
		},
		"a one-shot host": {
			ID:        "local-docker",
			MachineID: "daemon-1",
			Kind:      domain.OfferKindStanding,
			Lane:      domain.LaneEphemeral,
			Images:    domain.ImageInventory{Known: true, LayerDigests: []string{strandedLayer}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			observation := enrolmentObservation(now)
			observation.Effects = []EffectRecord{enrolmentEffect()}
			observation.World.Offers = []domain.OfferSnapshot{offer}

			if err := reusableCapacityHasAnEnrolledRuntime(observation); err != nil {
				t.Fatalf("capacity that keeps nothing was refused an enrolment it could never have: %v", err)
			}
			if err := localityProvenance(observation); err == nil {
				t.Fatal("capacity that keeps nothing accumulated content and no rule objected")
			}
		})
	}
}

// TestAnEnrolmentThatNamesNoMachineIsRefusedRatherThanDropped is what stops this
// rule from weakening in silence. A session is opened on one machine under one
// lease, so a ledger entry that names neither is a record the rule cannot read,
// and skipping it would have made every listing in the world pass on the strength
// of one malformed entry somewhere else.
func TestAnEnrolmentThatNamesNoMachineIsRefusedRatherThanDropped(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	observation := enrolmentObservation(now)
	observation.Effects = []EffectRecord{{
		Operation:   OperationNodeEnrolled,
		OperationID: "nod_nameless/generation-1",
		Command:     EffectCommandAccepted,
		Request:     []byte(`{"node_id":"nod_nameless","generation":1}`),
		Consequence: []byte(`{"node_id":"nod_nameless","fencing_token":1}`),
	}}

	err := reusableCapacityHasAnEnrolledRuntime(observation)

	if err == nil {
		t.Fatal("an enrolment naming neither a machine nor a lease was read as an enrolment")
	}
	if want := "an enrolment names both"; !strings.Contains(err.Error(), want) {
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

// TestAnEnrolmentIsBoundToTheGenerationItsMachineWasInvitedFor is the deliberate
// failing world for safety.enrolment_names_the_generation_it_was_invited_for. It
// is the one property in the O1 ontology that keeps a Node bound to a single
// Rental generation, and it is stated over the ledger because the ledger is the
// only account of which enrolment happened on which machine.
//
// Three worlds, and the rule has to tell them apart. A lease whose second machine
// was invited after the first generation ended enrols under either, because both
// were really allocated. A lease with no provision behind it is standing capacity
// this world seeded, and there is no invitation to be right or wrong about. A
// session filed under a generation nothing was ever allocated for is the
// violation: Mercator would address every later act about that machine, the
// fencing token included, to a machine that does not exist.
func TestAnEnrolmentIsBoundToTheGenerationItsMachineWasInvitedFor(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	for name, ledger := range map[string]struct {
		effects []EffectRecord
		holds   bool
	}{
		"the generation its machine was allocated for": {
			effects: []EffectRecord{provisionEffect(1), leasedEnrolmentEffect(1)},
			holds:   true,
		},
		"a second machine invited when the first generation ended": {
			effects: []EffectRecord{provisionEffect(1), provisionEffect(2), leasedEnrolmentEffect(2)},
			holds:   true,
		},
		"standing capacity nothing ever allocated": {
			effects: []EffectRecord{leasedEnrolmentEffect(1)},
			holds:   true,
		},
		"a generation no machine was ever invited for": {
			effects: []EffectRecord{provisionEffect(1), leasedEnrolmentEffect(8)},
			holds:   false,
		},
	} {
		t.Run(name, func(t *testing.T) {
			observation := handWrittenLedger(now, ledger.effects...)

			err := enrolmentNamesTheGenerationItWasInvitedFor(observation)

			if ledger.holds && err != nil {
				t.Fatalf("a session opened on a machine its lease really allocated was refused: %v", err)
			}
			if !ledger.holds && err == nil {
				t.Fatal("an agent enrolled under a generation the machine was never invited for and nothing objected")
			}
		})
	}
}

func provisionEffect(generation uint64) EffectRecord {
	return EffectRecord{
		Operation:   OperationCapacityProvision,
		OperationID: fmt.Sprintf("provision_%s/generation-%d", strandedRental, generation),
		Command:     EffectCommandAccepted,
		Request: []byte(fmt.Sprintf(
			`{"rental_id":%q,"generation":%d,"offer_snapshot_id":"fresh-4090","node_id":"nod_stranded"}`,
			strandedRental, generation,
		)),
		Consequence: []byte(`{"native_ref":"lab-machine","state":"requested"}`),
	}
}

func leasedEnrolmentEffect(generation uint64) EffectRecord {
	return EffectRecord{
		Operation:   OperationNodeEnrolled,
		OperationID: fmt.Sprintf("nod_stranded/generation-%d", generation),
		Command:     EffectCommandAccepted,
		Request: []byte(fmt.Sprintf(
			`{"machine_id":%q,"rental_id":%q,"node_id":"nod_stranded","generation":%d}`,
			strandedMachine, strandedRental, generation,
		)),
		Consequence: []byte(fmt.Sprintf(`{"node_id":"nod_stranded","fencing_token":%d}`, generation)),
	}
}
