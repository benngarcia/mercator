package lab

import (
	"context"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/domain"
)

// privatePullImage is the image a-private-pull-uses-a-credential-that-expires is
// about: one digest at a registry that serves nothing to an anonymous reader.
const privatePullImage = "registry.mercator.test/analyst@sha256:7a1c4e9b2d6f8a0c3e5b7d9f1a3c5e7b9d1f3a5c7e9b1d3f5a7c9e1b3d5f7a9c"

// publicPullImage is the other Run's image, at a registry anyone can read. It is
// in the same Blueprint on purpose: the two answers have to be visible beside
// each other, because minting for content that needs none is its own way of
// putting an account on a machine.
const publicPullImage = "runner@sha256:1b3d5f7a9c1e3b5d7f9a1c3e5b7d9f1a3c5e7b9d1f3a5c7e9b1d3f5a7c9e1b3d"

// TestAPrivatePullAndAnArtifactReadAreMintedForOneOperationEach is the slice's
// claim at L1: through the real orchestrator, the real event log and the real
// Broker seam, the two fetches a queued Run needs reach the machine carrying
// material that names one operation, one deployment, one piece of content and an
// expiry, and the machine that would otherwise have been refused takes them on.
//
// It reads what the world watched cross the seam rather than what Mercator wrote
// about itself. A control plane that minted nothing would have every private
// fetch refused by the registry and the object store here, so the assertion that
// the transfers happened is itself an assertion that the material was minted.
func TestAPrivatePullAndAnArtifactReadAreMintedForOneOperationEach(t *testing.T) {
	execution := openConformanceExecution(t, "a-private-pull-uses-a-credential-that-expires")
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	// The occupant holds the machine for forty minutes and the queued Run's
	// content is prepared underneath it, one piece at a time at the rate the
	// Blueprint allows.
	for range 12 {
		if _, err := execution.Drive(context.Background(), Advance(5*time.Minute)); err != nil {
			t.Fatalf("drive the occupancy and the preparation under it: %v", err)
		}
	}

	credentials := execution.runtime.world.invariantFacts().ContentCredentials
	minted := map[adapter.PrepareKind]contentCredential{}
	for _, credential := range credentials {
		minted[credential.Kind] = credential
	}
	if len(minted) != 2 {
		t.Fatalf("the machine was handed material for %d kinds of fetch, want the private pull and the Artifact read", len(minted))
	}
	for kind, credential := range minted {
		if credential.Scope.Operation != credential.Operation {
			t.Fatalf("the %s credential was minted for %q and arrived on %q", kind, credential.Scope.Operation, credential.Operation)
		}
		if window := credential.Scope.ExpiresAt.Sub(credential.At); window <= 0 || window > contentCredentialWindow {
			t.Fatalf("the %s credential stays presentable for %s", kind, window)
		}
	}
	if content := minted[adapter.PrepareImage].Content; content != domain.ReferenceDigest(privatePullImage) {
		t.Fatalf("the pull was minted for %q, want the private image's own digest", content)
	}
	if content := minted[adapter.PrepareArtifact].Content; content != "artifact:corpus:v3" {
		t.Fatalf("the read was minted for %q, want the version the queued Run consumes", content)
	}
}

// TestThePublicImageInThatWorldIsFetchedWithNothing is the other half of the same
// execution, and the half that stops the claim above from being "Mercator mints
// for everything". The occupant's image is at a registry anyone can read, and no
// credential for it is ever handed to a machine.
func TestThePublicImageInThatWorldIsFetchedWithNothing(t *testing.T) {
	execution := openConformanceExecution(t, "a-private-pull-uses-a-credential-that-expires")
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	for range 12 {
		if _, err := execution.Drive(context.Background(), Advance(5*time.Minute)); err != nil {
			t.Fatalf("drive the occupancy and the preparation under it: %v", err)
		}
	}

	for _, credential := range execution.runtime.world.invariantFacts().ContentCredentials {
		if credential.Content == domain.ReferenceDigest(publicPullImage) {
			t.Fatalf("a machine was handed an account to read an image any anonymous reader can have: %+v", credential.Scope)
		}
	}
}
