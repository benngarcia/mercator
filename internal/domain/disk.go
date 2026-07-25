package domain

// DiskDemand is what one Run's content asks of one candidate's disk: how much
// of each kind of content is already on the machine, how much of it still has
// to arrive, and how much room the machine has left to put it in.
//
// It is stated as one question over every kind of content at once because the
// disk is one resource. What an image costs on a host depends on whether the
// Artifact beside it fits, and pricing the two independently is how a machine
// with nowhere to put forty gigabytes gets recorded as the warmest candidate in
// the fleet.
type DiskDemand struct {
	// FreeBytes is the room the offer says this machine has. It is what a host
	// can take without giving anything up, so everything below is measured
	// against it.
	FreeBytes int64
	// ImageHeldBytes and ArtifactHeldBytes are this Run's content that is
	// already resident here. Content nobody could enumerate is not held: an
	// unknown inventory establishes nothing about what is on the disk, so it
	// buys no room and is never charged for losing any.
	ImageHeldBytes    int64
	ArtifactHeldBytes int64
	// ImageFetchBytes and ArtifactFetchBytes are what still has to land before
	// the Run can start, which is exactly the room that has to be found.
	ImageFetchBytes    int64
	ArtifactFetchBytes int64
}

// DiskEviction is the content a candidate would have to give up to make room
// for what it still has to fetch. Giving it up is not free: it is content this
// same Run was credited with holding, so whatever leaves has to be fetched
// again before the workload can start, and the charge belongs on the estimate
// beside the fetch it is part of.
type DiskEviction struct {
	ImageBytes    int64
	ArtifactBytes int64
}

// None reports that everything this Run needs fits in the room the machine
// already has.
func (eviction DiskEviction) None() bool {
	return eviction.ImageBytes == 0 && eviction.ArtifactBytes == 0
}

// Eviction is what this candidate has to delete to fit what it must fetch.
//
// A machine short of room makes it by deleting something, and the only content
// Mercator can say is on that disk is the content it credited this candidate
// for holding. So a shortfall is charged against that credit and never beyond
// it: a host may be asked to give up everything it holds of this Run's content,
// which prices it exactly like a host that holds none of it, and never more.
// Whatever else is on the machine is somebody else's content, and charging this
// Run for fetching that back would be inventing an eviction policy Mercator has
// no way to observe.
//
// Which kind of content goes is unknowable in the same way, so each kind gives
// up the share of the shortfall its own residency represents. A proportion is
// not an exchange rate: it states that Mercator cannot tell an image layer from
// a dataset when a disk fills up, rather than presuming one is always the
// victim.
func (demand DiskDemand) Eviction() DiskEviction {
	resident := demand.ImageHeldBytes + demand.ArtifactHeldBytes
	shortfall := demand.ImageFetchBytes + demand.ArtifactFetchBytes - demand.FreeBytes
	evicted := min(max(shortfall, 0), resident)
	if evicted == 0 {
		return DiskEviction{}
	}
	// The share is taken in floating point because these are byte counts: an
	// eighteen gigabyte image beside three gigabytes of shortfall multiplies
	// past what an int64 holds, and the wrapped answer prices a warm machine
	// like a cold one in the other direction.
	image := min(int64(float64(evicted)*(float64(demand.ImageHeldBytes)/float64(resident))), evicted)
	return DiskEviction{ImageBytes: image, ArtifactBytes: evicted - image}
}
