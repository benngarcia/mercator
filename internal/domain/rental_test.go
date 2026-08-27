package domain

import (
	"strings"
	"testing"
	"time"
)

var leaseStart = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)

func leaseIdentity() RentalIdentity {
	return RentalIdentity{
		RentalID:       "rnt_1",
		ConnectionID:   "con_simcloud",
		OwnershipToken: "own_1",
	}
}

// TestALeaseOpensWithTheRuntimeItInvited holds the order provisioning happens
// in. Mercator mints the lease and invites the runtime before it asks a provider
// for anything, so the lease exists with a generation on it and no machine in it,
// which is what makes an accepted-but-lost provision reconcilable.
func TestALeaseOpensWithTheRuntimeItInvited(t *testing.T) {
	lease, err := OpenRental(leaseIdentity(), "nod_1", leaseStart)

	if err != nil {
		t.Fatalf("open the lease: %v", err)
	}
	current, open := lease.Current()
	if !open {
		t.Fatal("a lease Mercator just took has no generation to provision against")
	}
	if current.Number != 1 || current.NodeID != "nod_1" {
		t.Fatalf("generation = %+v, want generation 1 invited for nod_1", current)
	}
	if current.NativeRef != "" {
		t.Fatalf("machine = %q, want a lease that has not heard from the provider to name none", current.NativeRef)
	}
	if !lease.Held() {
		t.Fatal("a lease Mercator just took is not held")
	}
}

// TestARetriedProvisionAnswersWithTheSameMachine is what makes the provision path
// safe to repeat. A provider answering twice about one machine changes nothing,
// and a second machine is refused rather than quietly replacing the first, which
// would leave the first billing with nothing able to name it.
func TestARetriedProvisionAnswersWithTheSameMachine(t *testing.T) {
	lease := mustOpen(t)

	acquired, err := lease.Acquire("i-0abc")
	if err != nil {
		t.Fatalf("acquire the machine: %v", err)
	}
	again, err := acquired.Acquire("i-0abc")
	if err != nil {
		t.Fatalf("acquire the same machine again: %v", err)
	}
	_, err = acquired.Acquire("i-0def")

	if again.Version != acquired.Version {
		t.Fatalf("version = %d, want the same answer to change nothing at %d", again.Version, acquired.Version)
	}
	if err == nil {
		t.Fatal("one generation took a second machine and said nothing")
	}
	if !strings.Contains(err.Error(), "cannot also hold") {
		t.Fatalf("refusal = %q, want it to name the machine the generation already holds", err)
	}
}

// TestAStoppedLeaseResumesOntoAFreshGenerationAndRuntime is the reason a
// generation exists at all. The machine comes back under the same lease, and the
// runtime on it is a different runtime: node identity carries the generation it
// was invited for, so reusing the previous one could not tell the resumed machine
// from the one before the stop.
func TestAStoppedLeaseResumesOntoAFreshGenerationAndRuntime(t *testing.T) {
	lease := mustOpen(t)

	stopped, ended, err := lease.EndGeneration(1, RentalStopped, leaseStart.Add(time.Hour))
	if err != nil {
		t.Fatalf("stop the machine: %v", err)
	}
	resumed, err := stopped.BeginGeneration("nod_2", leaseStart.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("resume the lease: %v", err)
	}

	if ended.Number != 1 || ended.NodeID != "nod_1" {
		t.Fatalf("ended generation = %+v, want the one whose runtime is now retired", ended)
	}
	if !stopped.Held() {
		t.Fatal("a stopped machine gave up the lease over it")
	}
	if _, open := stopped.Current(); open {
		t.Fatal("a stopped lease still has a generation commands can be sent to")
	}
	current, open := resumed.Current()
	if !open || current.Number != 2 || current.NodeID != "nod_2" {
		t.Fatalf("resumed generation = %+v open=%v, want generation 2 on a fresh runtime", current, open)
	}
	if len(resumed.Generations) != 2 {
		t.Fatalf("generations = %d, want the stopped one kept beside the resumed one", len(resumed.Generations))
	}
}

// TestAnEndingThatLeavesNothingReleasesTheLease separates the two kinds of end.
// A stop suspends capacity that is still Mercator's; a termination and a
// provider's reclamation both destroy it, and a lease over a machine that no
// longer exists is not capacity anything may be placed on.
func TestAnEndingThatLeavesNothingReleasesTheLease(t *testing.T) {
	for ending, held := range map[RentalGenerationEnding]bool{
		RentalStopped:    true,
		RentalTerminated: false,
		RentalReclaimed:  false,
	} {
		t.Run(string(ending), func(t *testing.T) {
			lease := mustOpen(t)

			next, _, err := lease.EndGeneration(1, ending, leaseStart.Add(time.Hour))

			if err != nil {
				t.Fatalf("end the generation: %v", err)
			}
			if next.Held() != held {
				t.Fatalf("held = %v after %q, want %v", next.Held(), ending, held)
			}
			if _, err := next.BeginGeneration("nod_2", leaseStart.Add(2*time.Hour)); (err == nil) != held {
				t.Fatalf("resuming after %q = %v, want resumable=%v", ending, err, held)
			}
		})
	}
}

// TestAnEndingNamesTheGenerationItDecidedAbout is the confusion binding a Node to
// a generation exists to prevent, one level up. The decision to end a generation
// and the write that records it are separated by a network, so a retry comes back
// to a lease that has already stopped and resumed. Ending it again by number ends
// the machine the caller meant, and ends nothing else.
//
// The retry is stamped after generation 2 began on purpose. A moment before that
// would be refused by the rule that a generation cannot end before it started,
// whichever generation the code picked, and the refusal would stand in for the
// answer: the case would fail with the naming broken and prove only that the
// clock disagreed.
func TestAnEndingNamesTheGenerationItDecidedAbout(t *testing.T) {
	stopped, _, err := mustOpen(t).EndGeneration(1, RentalStopped, leaseStart.Add(time.Hour))
	if err != nil {
		t.Fatalf("stop the machine: %v", err)
	}
	resumed, err := stopped.BeginGeneration("nod_2", leaseStart.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("resume the lease: %v", err)
	}

	retried, ended, err := resumed.EndGeneration(1, RentalStopped, leaseStart.Add(3*time.Hour))

	if err != nil {
		t.Fatalf("retry the ending of generation 1: %v", err)
	}
	if ended.Number != 1 || ended.NodeID != "nod_1" {
		t.Fatalf("ended generation = %+v, want the one the caller decided about", ended)
	}
	if retried.Version != resumed.Version {
		t.Fatalf("version = %d, want a retry to change nothing at %d", retried.Version, resumed.Version)
	}
	current, open := retried.Current()
	if !open || current.Number != 2 || current.NodeID != "nod_2" {
		t.Fatalf("current generation = %+v open=%v, want the live machine untouched", current, open)
	}
}

// TestAGenerationRecordsOneEnding refuses the other half of the same confusion. A
// generation says what became of one machine, and a second answer about it is a
// history in which both happened.
func TestAGenerationRecordsOneEnding(t *testing.T) {
	stopped, _, err := mustOpen(t).EndGeneration(1, RentalStopped, leaseStart.Add(time.Hour))
	if err != nil {
		t.Fatalf("stop the machine: %v", err)
	}

	_, _, err = stopped.EndGeneration(1, RentalTerminated, leaseStart.Add(2*time.Hour))

	if err == nil {
		t.Fatal("a stopped generation was also terminated and said nothing")
	}
	if !strings.Contains(err.Error(), "cannot also be") {
		t.Fatalf("refusal = %q, want it to name the ending the generation already carries", err)
	}
	if _, _, err := stopped.EndGeneration(2, RentalStopped, leaseStart.Add(2*time.Hour)); err == nil {
		t.Fatal("a generation this lease has never been through was ended")
	}
}

// TestALeaseMercatorCouldNotHaveReachedIsRefused is what a store asks before it
// writes. Each of these is a history that did not happen, and each of them reads
// back as a machine somebody could act on.
func TestALeaseMercatorCouldNotHaveReachedIsRefused(t *testing.T) {
	for name, lease := range map[string]Rental{
		"a lease over nothing": {
			ID: "rnt_1", ConnectionID: "con_1", OwnershipToken: "own_1", Version: 1,
		},
		"two generations open at once": withGenerations(
			RentalGeneration{Number: 1, NodeID: "nod_1", BeganAt: leaseStart},
			RentalGeneration{Number: 2, NodeID: "nod_2", BeganAt: leaseStart},
		),
		"a generation out of sequence": withGenerations(
			RentalGeneration{Number: 2, NodeID: "nod_1", BeganAt: leaseStart},
		),
		"a generation that ended and does not say how": withGenerations(
			RentalGeneration{Number: 1, NodeID: "nod_1", BeganAt: leaseStart, EndedAt: leaseStart.Add(time.Hour)},
		),
		"a generation with no runtime": withGenerations(
			RentalGeneration{Number: 1, BeganAt: leaseStart},
		),
		"more generations than transitions": func() Rental {
			lease := withGenerations(
				RentalGeneration{
					Number: 1, NodeID: "nod_1", BeganAt: leaseStart,
					EndedAt: leaseStart.Add(time.Hour), Ending: RentalStopped,
				},
				RentalGeneration{Number: 2, NodeID: "nod_2", BeganAt: leaseStart.Add(2 * time.Hour)},
			)
			lease.Version = 1
			return lease
		}(),
		"a destroyed machine still held": withGenerations(
			RentalGeneration{
				Number: 1, NodeID: "nod_1", BeganAt: leaseStart,
				EndedAt: leaseStart.Add(time.Hour), Ending: RentalTerminated,
			},
		),
		// A lease whose machine was destroyed has nothing to resume, so a
		// generation after that ending is a machine Mercator never took. Read back
		// it is worse than a lease that will not open: Current reports the later
		// generation open and Placement sends a Run to a host the record itself
		// says was destroyed.
		"a generation after the lease was given up": withGenerations(
			RentalGeneration{
				Number: 1, NodeID: "nod_1", BeganAt: leaseStart,
				EndedAt: leaseStart.Add(time.Hour), Ending: RentalTerminated,
			},
			RentalGeneration{Number: 2, NodeID: "nod_2", BeganAt: leaseStart.Add(2 * time.Hour)},
		),
	} {
		t.Run(name, func(t *testing.T) {
			if err := lease.Validate(); err == nil {
				t.Fatalf("a lease Mercator could not have reached passed validation: %+v", lease)
			}
		})
	}
}

// TestALeaseKeepsWhatItWasWhenTheNextTransitionIsTaken holds the value semantics
// every transition here relies on. A caller that still holds the previous lease
// is holding what it read, so a store that refused the write has not already had
// its record edited underneath it.
func TestALeaseKeepsWhatItWasWhenTheNextTransitionIsTaken(t *testing.T) {
	lease := mustOpen(t)

	if _, err := lease.Acquire("i-0abc"); err != nil {
		t.Fatalf("acquire the machine: %v", err)
	}

	current, _ := lease.Current()
	if current.NativeRef != "" {
		t.Fatalf("machine = %q, want the lease the caller still holds to name none", current.NativeRef)
	}
}

func mustOpen(t *testing.T) Rental {
	t.Helper()
	lease, err := OpenRental(leaseIdentity(), "nod_1", leaseStart)
	if err != nil {
		t.Fatalf("open the lease: %v", err)
	}
	return lease
}

// withGenerations is a lease whose identity is fine and whose history is the
// thing under test. The version is stated as one per generation so every case
// fails on the shape it is about rather than on the transition count.
func withGenerations(generations ...RentalGeneration) Rental {
	return Rental{
		ID:             "rnt_1",
		ConnectionID:   "con_1",
		OwnershipToken: "own_1",
		Version:        uint64(len(generations)),
		OpenedAt:       leaseStart,
		Generations:    generations,
	}
}
