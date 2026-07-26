package adapter

import (
	"time"

	"github.com/benngarcia/mercator/internal/domain"
)

// This file is the preparation seam: content Mercator asks a machine to fetch
// before it has been asked to run anything with it. It is stated as desired
// state rather than as a stream of orders, because that is the only shape a
// control plane can reconcile: a Run that goes away has to stop costing a host
// disk and bandwidth, and "stop" is expressible only as an absence from the set
// of what is still wanted.
//
// Preparation is speculative by construction. Nothing here may ever compete
// with work Mercator has already admitted, which is why the whole desired set
// crosses the boundary at once and carries no more items than the control plane
// is willing to have in flight.

// PrepareKind names which of the two durable authorities a wanted item comes
// from. They are separate because they are different content over different
// links: an image is what a container runtime fetches to start at all, an
// Artifact is what the workload reads once it is running, and one host is
// routinely warm for one and cold for the other.
type PrepareKind string

const (
	PrepareImage    PrepareKind = "image"
	PrepareArtifact PrepareKind = "artifact"
)

// PrepareItem is one piece of content one machine should be holding. It names
// the machine as well as the content, so a desired set spanning several hosts
// is one message rather than one message per host: what may be in flight at
// once is a control-plane-wide bound, and a per-host command could not express
// it.
type PrepareItem struct {
	Kind            PrepareKind
	OfferSnapshotID string
	ConnectionID    string
	AdapterType     string
	NativeRef       string
	// RunID is the Run whose speculative placement wants this content. It is
	// provenance for an operator reading the ledger, and never permission: the
	// content is wanted because a queued Booking names this host, and it stops
	// being wanted when that Booking does.
	RunID string
	// Image is the digest-pinned reference to pull, and Platform is the build
	// this host would run. A tag is never image identity.
	Image    string
	Platform domain.Platform
	// ArtifactID and ContentDigest are the version to replicate and what its
	// bytes must hash to. A copy that does not match the catalog digest is
	// worth exactly what no copy is worth.
	ArtifactID    string
	ContentDigest string
	// Source is where the durable copy is read from. The control plane mints
	// it, so no object-store credential of Mercator's ever lands on a machine.
	Source    string
	SizeBytes int64
}

// Content is what this item names, in the vocabulary the holder reports. It is
// the identity a ledger and a host both address this content by, so one item
// wanted by two Runs is one piece of content rather than two.
func (item PrepareItem) Content() string {
	if item.Kind == PrepareImage {
		return domain.ReferenceDigest(item.Image)
	}
	return item.ArtifactID
}

// PrepareRequest is the whole of what Mercator wants prepared right now, for
// one workspace. Anything a host is preparing that is absent from Wanted is
// preparation Mercator has stopped asking for, and a machine that keeps going
// is consuming disk and bandwidth for work that will never happen.
type PrepareRequest struct {
	WorkspaceID string
	// OperationKey is the identity of this desired state. Two requests naming
	// one key state one desire, so a redelivered command changes nothing.
	OperationKey string
	Wanted       []PrepareItem
}

// PrepareReceipt is what the far side did with a desired set: what it took on,
// what it stopped, and what it cannot do at all. Unsupported is stated rather
// than silently dropped, because a machine that cannot prepare is a machine
// whose next Run pays the whole fetch, and an operator reading a decision
// should not have to infer that from a missing effect.
type PrepareReceipt struct {
	OperationKey string
	AcceptedAt   time.Time
	Duplicate    bool
	Started      []string
	Abandoned    []string
	Unsupported  []string
}
