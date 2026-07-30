package domain

// DiskDemand is what one Run asks of one candidate's disk: the room the machine
// says it has left, the room the Run reserved for its own working state, and the
// content that still has to land here before the workload can start.
//
// It is stated as one question over every kind of content at once because the
// disk is one resource. What an image costs on a host depends on whether the
// Artifact beside it fits, and pricing the two independently is how a machine
// with nowhere to put forty gigabytes gets recorded as the warmest candidate in
// the fleet.
//
// What a machine short of room cannot do is make room out of this Run's own
// content. Deleting a layer this Run needs frees exactly as many bytes as
// fetching it back consumes, so the disk ends where it began and the workload is
// no closer to starting: a candidate whose content does not fit is not a
// candidate that costs more, it is a machine that cannot run this Run. The only
// content that would help is somebody else's, and Mercator neither observes it
// nor commands its removal, because no runtime in this tree implements garbage
// collection. When one does, what it reclaims will be a fact this demand can
// read rather than a policy the scheduler assumes.
//
// It is recorded on the candidate for the same reason the localities are. A Run
// refused for room has to say so out loud, and a reader who could see only the
// seconds could not tell a machine that was passed over from one that had
// nowhere to put the work.
type DiskDemand struct {
	// FreeBytes is the room the offer says this machine has left. A machine
	// that could not measure its disk offers none, which is the same silence
	// every other unmeasured fact states, and it costs placements rather than
	// enrollment.
	FreeBytes int64 `json:"free_bytes"`
	// ReservedBytes is the ephemeral disk the workload declared it needs. It is
	// room for what the Run itself writes, so it is asked for beside the
	// content rather than out of it: a Run admitted on a fifty gigabyte floor
	// and then handed a machine whose fifty gigabytes are its own dataset was
	// promised nothing.
	ReservedBytes int64 `json:"reserved_bytes,omitempty"`
	// LandBytes is everything this Run's content still has to put on this disk:
	// the image bytes it must transfer, the Artifact versions it must read out
	// of the object store, and the caches it declared that this host does not
	// hold.
	LandBytes int64 `json:"land_bytes,omitempty"`
	// EstablishedLandBytes is the part of that somebody enumerated. A host that
	// could not say what it holds is charged the whole content in seconds and
	// establishes none of it here, because nothing said those bytes have to
	// arrive and refusing a machine for a silence is what turns uncertainty into
	// infeasibility.
	EstablishedLandBytes int64 `json:"established_land_bytes,omitempty"`
}

// RequiredBytes is the room this candidate has to have for the Run to be
// admitted here: what the Run reserved, plus the content somebody established it
// would still have to land.
func (demand DiskDemand) RequiredBytes() int64 {
	return demand.ReservedBytes + demand.EstablishedLandBytes
}

// Fits reports whether this machine has the room the Run needs. It is asked of
// established bytes only, so a host nobody could enumerate is never refused for
// content it may well be holding already.
func (demand DiskDemand) Fits() bool {
	return demand.RequiredBytes() <= demand.FreeBytes
}
