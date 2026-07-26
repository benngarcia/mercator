package domain_test

import (
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
)

const dataset = "artifact:imagenet:v2.41"

// TestTheProducingHostIsPreferredOnlyWhereNothingElseAnswered is the whole
// affinity rule in one table, stated as what each candidate would owe on the
// read. The record of where content was published is evidence, and it is the
// weakest kind there is: nothing re-checked that the copy outlived the Run that
// wrote it. So it answers where a machine said nothing about the version and
// nowhere else, and it never contradicts a machine that did answer.
func TestTheProducingHostIsPreferredOnlyWhereNothingElseAnswered(t *testing.T) {
	looked := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	version := domain.ArtifactVersion{
		ID:                 dataset,
		WorkspaceID:        "ws_alpha",
		ContentDigest:      "sha256:1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a",
		SizeBytes:          40_000_000_000,
		Location:           domain.ArtifactLocation("ws_alpha", dataset),
		ProducedOnRentalID: "rnt_producer",
		PublishedAt:        looked,
	}
	checkedCopy := domain.ArtifactInventory{Known: true, ObservedAt: looked, Replicas: []domain.ArtifactReplica{{
		ArtifactID:    version.ID,
		ContentDigest: version.ContentDigest,
		SizeBytes:     version.SizeBytes,
		State:         domain.ArtifactReplicaVerified,
		VerifiedAt:    looked,
	}}}

	for name, testCase := range map[string]struct {
		rentalID         string
		inventory        domain.ArtifactInventory
		wantLocality     domain.LocalityState
		wantFetch        int64
		wantProducedHere bool
	}{
		"the machine the bytes were written on owes nothing where nobody could ask": {
			rentalID:         "rnt_producer",
			inventory:        domain.ArtifactInventory{},
			wantLocality:     domain.LocalityUnknown,
			wantFetch:        0,
			wantProducedHere: true,
		},
		"a silent machine that produced nothing owes the whole read": {
			rentalID:     "rnt_neighbour",
			inventory:    domain.ArtifactInventory{},
			wantLocality: domain.LocalityUnknown,
			wantFetch:    version.SizeBytes,
		},
		// The host answered about itself, now, and that beats a record of what
		// was true when the content was published. A consumer sent here on the
		// record would pay a read this estimate promised it would not.
		"a producing machine that enumerated and holds no copy owes the whole read": {
			rentalID:         "rnt_producer",
			inventory:        domain.ArtifactInventory{Known: true, ObservedAt: looked},
			wantLocality:     domain.LocalityCold,
			wantFetch:        version.SizeBytes,
			wantProducedHere: true,
		},
		"a producing machine holding a checked copy is hot on its own evidence": {
			rentalID:         "rnt_producer",
			inventory:        checkedCopy,
			wantLocality:     domain.LocalityHot,
			wantFetch:        0,
			wantProducedHere: true,
		},
		// Capacity that keeps nothing carries no Rental identity, and neither
		// does a version nobody recorded a machine for. Comparing the two empty
		// answers would make every one-shot product the producer of every such
		// version and price it a free read of content it has never seen.
		"capacity with no Rental identity is nobody's producing host": {
			rentalID:     "",
			inventory:    domain.ArtifactInventory{},
			wantLocality: domain.LocalityUnknown,
			wantFetch:    version.SizeBytes,
		},
	} {
		t.Run(name, func(t *testing.T) {
			offer := domain.OfferSnapshot{ID: "off_1", RentalID: testCase.rentalID, Artifacts: testCase.inventory}

			fetch, evidence := domain.ArtifactFetchWork([]domain.ArtifactVersion{version}, offer)

			if len(evidence) != 1 {
				t.Fatalf("recorded %d entries for one declared input", len(evidence))
			}
			if fetch != testCase.wantFetch {
				t.Errorf("owes %d bytes, want %d", fetch, testCase.wantFetch)
			}
			if evidence[0].Locality != testCase.wantLocality {
				t.Errorf("locality = %q, want %q", evidence[0].Locality, testCase.wantLocality)
			}
			if evidence[0].ProducedHere != testCase.wantProducedHere {
				t.Errorf("produced here = %v, want %v", evidence[0].ProducedHere, testCase.wantProducedHere)
			}
		})
	}
}

// TestAVersionWithNoProducingHostRecordIsNobodysAffinity holds the direction
// that matters when the whole fleet is silent. Every candidate then answers
// unknown, and the read has to be charged to all of them: an affinity that fired
// on a version with no recorded machine would make every candidate look like the
// producer and take the read off the whole world at once.
func TestAVersionWithNoProducingHostRecordIsNobodysAffinity(t *testing.T) {
	version := domain.ArtifactVersion{
		ID:            dataset,
		WorkspaceID:   "ws_alpha",
		ContentDigest: "sha256:1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a",
		SizeBytes:     40_000_000_000,
		Location:      domain.ArtifactLocation("ws_alpha", dataset),
		PublishedAt:   time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	offer := domain.OfferSnapshot{ID: "off_1", RentalID: "rnt_anyone"}

	fetch, evidence := domain.ArtifactFetchWork([]domain.ArtifactVersion{version}, offer)

	if fetch != version.SizeBytes {
		t.Errorf("owes %d bytes of a read nobody has been recorded producing, want %d", fetch, version.SizeBytes)
	}
	if evidence[0].ProducedHere {
		t.Error("a candidate is named as the machine content nobody recorded a producer for was written on")
	}
}
