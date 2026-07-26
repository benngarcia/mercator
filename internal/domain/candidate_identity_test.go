package domain_test

import (
	"strings"
	"testing"

	"github.com/benngarcia/mercator/internal/domain"
)

// vastListing is one Vast ask as the adapter states it: a bundle ID that will be
// a different integer the next time the same machine is searched for, and a
// geolocation and a card that will not be.
func vastListing(askID string) domain.OfferSnapshot {
	return domain.OfferSnapshot{
		ID:          askID,
		AdapterType: "vast",
		Kind:        domain.OfferKindProvisionable,
		Lane:        domain.LaneEphemeral,
		NativeRef:   askID,
		Region:      "US-CA",
		Resources: domain.ResourceInventory{
			Accelerators: []domain.AcceleratorInventory{{
				Vendor:         "NVIDIA",
				Model:          "RTX 5090",
				CanonicalModel: "nvidia-rtx-5090",
				Count:          2,
			}},
		},
	}
}

// enrolledNode is capacity Mercator keeps: the same Rental every heartbeat, with
// nothing published about where it is or what product it is.
func enrolledNode(nodeID, rentalID string) domain.OfferSnapshot {
	return domain.OfferSnapshot{
		ID:          nodeID,
		RentalID:    rentalID,
		AdapterType: "node",
		Kind:        domain.OfferKindStanding,
		Lane:        domain.LaneReusable,
		NativeRef:   nodeID,
	}
}

// oneShotPool is the case the whole file exists for: a provider-native one-shot
// execution product that publishes no region, no product name, and no
// accelerator. Its listing ID is the only handle it has, and that handle never
// comes back.
func oneShotPool(offerID string) domain.OfferSnapshot {
	return domain.OfferSnapshot{
		ID:          offerID,
		AdapterType: "someprovider",
		Kind:        domain.OfferKindStanding,
		Lane:        domain.LaneEphemeral,
		NativeRef:   offerID,
	}
}

func TestTwoListingsOfOneProductShareOneIdentity(t *testing.T) {
	first := domain.CandidateIdentityOf(vastListing("off_vast_11111"), "sha256:image")
	second := domain.CandidateIdentityOf(vastListing("off_vast_99999"), "sha256:image")

	if !first.Recurs() || !second.Recurs() {
		t.Fatalf("a listing with a region and a card recurs; got %+v and %+v", first, second)
	}
	if first.Candidate(true) != second.Candidate(true) {
		t.Fatalf("two asks for one machine keyed differently:\n%s\n%s", first.Candidate(true), second.Candidate(true))
	}
}

func TestAnIdentityNamesNoOfferSnapshotID(t *testing.T) {
	offer := vastListing("off_vast_11111")

	identity := domain.CandidateIdentityOf(offer, "sha256:image")

	for _, key := range []string{identity.Candidate(true), identity.ProviderAndRegion(), identity.ProviderKey()} {
		if key == "" {
			continue
		}
		if strings.Contains(key, offer.ID) || strings.Contains(key, offer.NativeRef) {
			t.Fatalf("key %q names the listing %q, which never recurs", key, offer.ID)
		}
	}
}

func TestAMachineMercatorKeepsIsItsOwnCandidate(t *testing.T) {
	first := domain.CandidateIdentityOf(enrolledNode("node-1", "rnt_abc"), "sha256:image")
	again := domain.CandidateIdentityOf(enrolledNode("node-1", "rnt_abc"), "sha256:image")
	other := domain.CandidateIdentityOf(enrolledNode("node-2", "rnt_def"), "sha256:image")

	if first.Candidate(true) != again.Candidate(true) {
		t.Fatalf("one node keyed two ways: %q and %q", first.Candidate(true), again.Candidate(true))
	}
	if first.Candidate(true) == other.Candidate(true) {
		t.Fatalf("two nodes share the key %q", first.Candidate(true))
	}
}

// TestAProvisionableRentalIdentityIsNeverTheBookingsRental is the clause that
// keeps the machine level honest. A provisionable offer's Rental is minted per
// Booking, so reading it here would file every fresh machine under a key holding
// exactly one sample and report it as this exact candidate again.
func TestAProvisionableRentalIdentityIsNeverTheBookingsRental(t *testing.T) {
	listing := vastListing("off_vast_11111")
	listing.RentalID = "rnt_minted_for_this_booking"

	identity := domain.CandidateIdentityOf(listing, "sha256:image")

	if identity.Machine != "" {
		t.Fatalf("a machine that does not exist yet claims to be Rental %q", identity.Machine)
	}
}

// TestAOneShotProductWithNothingPublishedCannotRecur is the falsifier for
// safety.prediction_provenance stated as a unit test: this candidate has no key,
// so nothing may ever claim candidate-specific evidence about it.
func TestAOneShotProductWithNothingPublishedCannotRecur(t *testing.T) {
	identity := domain.CandidateIdentityOf(oneShotPool("off_pool_7f3a"), "sha256:image")

	if identity.Recurs() {
		t.Fatalf("a one-shot product publishing no region, product, or card claims to recur: %+v", identity)
	}
	if key := identity.Candidate(true); key != "" {
		t.Fatalf("a candidate that cannot recur produced the key %q", key)
	}
}

func TestAMachineStageIgnoresTheImageItWasAskedToRun(t *testing.T) {
	node := enrolledNode("node-1", "rnt_abc")

	first := domain.CandidateIdentityOf(node, "sha256:one")
	second := domain.CandidateIdentityOf(node, "sha256:two")

	if first.Candidate(false) != second.Candidate(false) {
		t.Fatalf("one machine's boot history split across two images: %q and %q",
			first.Candidate(false), second.Candidate(false))
	}
	if first.Candidate(true) == second.Candidate(true) {
		t.Fatalf("two images share one content key %q", first.Candidate(true))
	}
}

func TestAcceleratorOrderIsNotAProduct(t *testing.T) {
	forward := vastListing("off_vast_1")
	forward.Resources.Accelerators = append(forward.Resources.Accelerators, domain.AcceleratorInventory{
		CanonicalModel: "nvidia-a100", Count: 1,
	})
	reversed := vastListing("off_vast_2")
	reversed.Resources.Accelerators = append(
		[]domain.AcceleratorInventory{{CanonicalModel: "nvidia-a100", Count: 1}},
		reversed.Resources.Accelerators...,
	)

	if got, want := domain.CandidateIdentityOf(reversed, "i").Candidate(true), domain.CandidateIdentityOf(forward, "i").Candidate(true); got != want {
		t.Fatalf("listing order changed the product key:\n%s\n%s", got, want)
	}
}
