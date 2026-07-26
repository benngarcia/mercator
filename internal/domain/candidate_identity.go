package domain

import (
	"slices"
	"sort"
	"strconv"
	"strings"
)

// This file is what a placement is allowed to learn about. Every stage of a
// launch is predicted from what earlier launches spent, and a history is only
// worth reading if the thing it is filed under is the same thing twice. That is
// the whole difficulty: an offer snapshot ID recurs for one provider and never
// for another, and nothing about the ID says which.
//
// broker.offerSnapshotID hashes a connection ID together with whatever the
// adapter called its offer. For a Docker endpoint that is a label derived from
// the host, so it is the same string every listing. For Vast it is a bundle ask
// ID: a new integer for every search, for a machine that was already there.
// Both arrive in OfferSnapshot.ID, so a history keyed on that field would
// accumulate real evidence about one backend and, about another, a growing pile
// of one-sample keys that can never be read again. Worse, it would report the
// second as an exact-candidate answer, which is a claim of candidate-specific
// evidence made out of a key that cannot have any.
//
// So identity is derived here from typed facts rather than from an ID, and the
// two cases are told apart by the one thing that actually distinguishes them:
// whether the capacity survives the workload. ADR 0005 makes that evidence
// rather than a claim, which is what lets a key rest on it.

// CandidateIdentity is the recurring thing a launch prediction is filed under:
// the machine if Mercator holds one, otherwise the product a provider sells,
// plus the content that machine was asked to run.
//
// It deliberately carries no offer snapshot ID, no native ref, and no
// connection ID. A native ref is the provider's name for the same
// non-recurring listing, and a connection is an operator's credential rather
// than anything about the machine: filing history under either would key
// learned behaviour on how Mercator happened to reach a host.
type CandidateIdentity struct {
	// Machine is the capacity Mercator keeps, named by its Rental. It is set
	// only for an offer that survives its workload, because that is the only
	// case where "this exact machine again" is a thing that can happen. For
	// everything else it is empty and the product fields below carry the
	// identity.
	Machine string
	// Provider is the backend the capacity comes from, which is the coarsest
	// thing worth learning about and the only field every candidate has.
	Provider string
	// Region is where the machine is. It reaches here from the offer, which is
	// where the adapters now state it: the Blueprint schema has carried a region
	// on rentals and marketplace listings since it was authored and nothing has
	// ever read it, because no offer field existed to put it on.
	Region string
	// InstanceType is the product name a provider sells, where it sells one.
	// Vast sells individual asks against machines and has no such name, so it
	// states none and the accelerator below is what distinguishes its products.
	InstanceType string
	// Accelerator is the canonical accelerator inventory, which is what makes
	// two listings in one region different products. It is canonicalized through
	// gpunorm at the source, so "RTX 5090" and "nvidia-rtx-5090" are one key
	// rather than two histories of the same card.
	Accelerator string
	// ImageDigest is the content this candidate was asked to run. Stages whose
	// duration is a property of the content read it; stages that are a property
	// of the machine do not, so one machine's boot history is not split across
	// every image the fleet has ever run on it.
	ImageDigest string
}

// CandidateIdentityOf derives what a candidate can be learned about from the
// offer as it was found and the image the Run asked for.
//
// The Rental is read only from capacity that keeps what it runs. Reading it from
// anything else would key history on an identity minted per Booking, which is a
// fresh string for every launch and therefore a sample set that can only ever
// hold one sample.
func CandidateIdentityOf(offer OfferSnapshot, imageDigest string) CandidateIdentity {
	identity := CandidateIdentity{
		Provider:     offer.AdapterType,
		Region:       offer.Region,
		InstanceType: offer.InstanceType,
		Accelerator:  acceleratorKey(offer.Resources.Accelerators),
		ImageDigest:  imageDigest,
	}
	if offer.KeepsWhatItRuns() {
		identity.Machine = offer.RentalID
	}
	return identity
}

// Recurs reports whether this identity can ever hold more than one sample.
//
// A machine Mercator keeps recurs by definition. A product recurs when the
// provider says something about it that outlives one listing: a region, a
// product name, or an accelerator. A candidate with none of those is a one-shot
// execution whose only handle was its listing ID, and history about it is
// unfilable: a predictor that answered "exact candidate, one sample" there would
// be reporting evidence about a key that cannot grow.
func (identity CandidateIdentity) Recurs() bool {
	if identity.Machine != "" {
		return true
	}
	return identity.Provider != "" &&
		(identity.Region != "" || identity.InstanceType != "" || identity.Accelerator != "")
}

// Candidate is the key for this exact candidate, which is the machine where
// Mercator holds one and the product where it does not. It carries the image
// only where the stage being predicted is about the content, so a machine's
// boot history is one sample set rather than one per image.
//
// The empty string is the answer for a candidate that cannot recur, and callers
// must read it as "no key" rather than as a key that happens to be blank.
// Recurs is the question, and this returning nothing is the same answer stated
// where a store would otherwise be written to.
func (identity CandidateIdentity) Candidate(includeImage bool) string {
	if !identity.Recurs() {
		return ""
	}
	parts := []string{"provider=" + identity.Provider}
	if identity.Machine != "" {
		parts = append(parts, "machine="+identity.Machine)
	} else {
		parts = append(parts,
			"region="+identity.Region,
			"instance="+identity.InstanceType,
			"accelerator="+identity.Accelerator,
		)
	}
	if includeImage {
		parts = append(parts, "image="+identity.ImageDigest)
	}
	return strings.Join(parts, ";")
}

// ProviderAndRegion is the key for every candidate of this provider in this
// region. A provider that states no region has no such key, and says so rather
// than filing every machine it sells under one blank region: that would make the
// level indistinguishable from the provider level while claiming to be narrower.
func (identity CandidateIdentity) ProviderAndRegion() string {
	if identity.Provider == "" || identity.Region == "" {
		return ""
	}
	return "provider=" + identity.Provider + ";region=" + identity.Region
}

// ProviderKey is the key for everything this provider sells.
func (identity CandidateIdentity) ProviderKey() string {
	if identity.Provider == "" {
		return ""
	}
	return "provider=" + identity.Provider
}

// acceleratorKey is this machine's accelerator inventory as one deterministic
// string. It is sorted because an inventory is a set: a host that lists two
// cards in the other order is the same product, and a key that disagreed would
// split its history in half.
//
// A machine with no accelerator states nothing rather than "none". Holding no
// card is a real fact about a product and it distinguishes no two products from
// each other, so counting it as a nameable one would make every unnamed CPU
// listing in the fleet recur under a single key and be reported as an exact
// candidate match.
func acceleratorKey(inventory []AcceleratorInventory) string {
	if len(inventory) == 0 {
		return ""
	}
	parts := make([]string, 0, len(inventory))
	for _, accelerator := range inventory {
		model := accelerator.CanonicalModel
		if model == "" {
			model = accelerator.Model
		}
		parts = append(parts, model+"x"+strconv.Itoa(accelerator.Count))
	}
	sort.Strings(parts)
	return strings.Join(slices.Compact(parts), ",")
}
