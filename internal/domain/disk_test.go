package domain

import "testing"

const gigabyte = int64(1e9)

// TestContentThatFitsCostsNoEviction is the ordinary machine: it has to fetch
// what it does not hold, it has the room, and holding content stays worth what
// holding content is worth.
func TestContentThatFitsCostsNoEviction(t *testing.T) {
	demand := DiskDemand{
		FreeBytes:          182 * gigabyte,
		ImageHeldBytes:     18 * gigabyte,
		ImageFetchBytes:    40 * gigabyte / 1000,
		ArtifactFetchBytes: 40 * gigabyte,
	}

	eviction := demand.Eviction()

	if !eviction.None() {
		t.Fatalf("a machine with 182GB free and 40GB to fetch gave up %+v", eviction)
	}
}

// TestAHostShortOfRoomGivesUpWhatItHolds is the whole rule in one case. The
// shortfall is what has to be deleted, and what gets deleted has to be fetched
// again, so it lands on the estimate of the content it belongs to.
func TestAHostShortOfRoomGivesUpWhatItHolds(t *testing.T) {
	demand := DiskDemand{
		FreeBytes:          37 * gigabyte,
		ImageHeldBytes:     18 * gigabyte,
		ImageFetchBytes:    40 * gigabyte / 1000,
		ArtifactFetchBytes: 40 * gigabyte,
	}

	eviction := demand.Eviction()

	if want := 3*gigabyte + 40*gigabyte/1000; eviction.ImageBytes != want {
		t.Fatalf("image eviction = %d, want the %d byte shortfall", eviction.ImageBytes, want)
	}
	if eviction.ArtifactBytes != 0 {
		t.Fatalf("artifact eviction = %d, and this host holds no copy to give up", eviction.ArtifactBytes)
	}
}

// TestAHostIsNeverChargedForMoreThanItHolds is the cap. Anything else on that
// disk belongs to somebody else, and charging this Run for fetching it back
// would be inventing an eviction policy Mercator cannot observe. A host that
// gives up everything is priced exactly like one that never held any of it.
func TestAHostIsNeverChargedForMoreThanItHolds(t *testing.T) {
	demand := DiskDemand{
		FreeBytes:          gigabyte,
		ImageHeldBytes:     2 * gigabyte,
		ArtifactFetchBytes: 500 * gigabyte,
	}

	eviction := demand.Eviction()

	if eviction.ImageBytes != 2*gigabyte || eviction.ArtifactBytes != 0 {
		t.Fatalf("eviction = %+v, want the two gigabytes this host holds and nothing beyond them", eviction)
	}
}

// TestTwoKindsOfContentShareOneShortfall is what happens when a disk fills up
// with both an image and a dataset on it. Nothing can say which the machine
// gives up, so each gives up the share its own residency represents rather than
// one of them always being the victim.
func TestTwoKindsOfContentShareOneShortfall(t *testing.T) {
	demand := DiskDemand{
		FreeBytes:          0,
		ImageHeldBytes:     30 * gigabyte,
		ArtifactHeldBytes:  10 * gigabyte,
		ArtifactFetchBytes: 4 * gigabyte,
	}

	eviction := demand.Eviction()

	if eviction.ImageBytes != 3*gigabyte || eviction.ArtifactBytes != gigabyte {
		t.Fatalf("eviction = %+v, want the shortfall split three to one with what is here", eviction)
	}
}

// TestTheEvictionShareSurvivesRealContentSizes is the arithmetic at the sizes
// this model actually runs at. Eighteen gigabytes of held image multiplied by a
// three gigabyte shortfall is more than an int64 holds, and the wrapped answer
// charged the whole eviction to content the host was not holding.
func TestTheEvictionShareSurvivesRealContentSizes(t *testing.T) {
	demand := DiskDemand{
		FreeBytes:          37 * gigabyte,
		ImageHeldBytes:     18 * gigabyte,
		ImageFetchBytes:    40 * gigabyte / 1000,
		ArtifactFetchBytes: 40 * gigabyte,
	}

	eviction := demand.Eviction()

	if eviction.ImageBytes < 0 || eviction.ArtifactBytes < 0 {
		t.Fatalf("eviction = %+v, and no machine gives up a negative number of bytes", eviction)
	}
	if total := eviction.ImageBytes + eviction.ArtifactBytes; total != 3*gigabyte+40*gigabyte/1000 {
		t.Fatalf("eviction totals %d bytes, want the shortfall", total)
	}
}
