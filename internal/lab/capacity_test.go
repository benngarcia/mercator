package lab

import (
	"context"
	"strings"
	"testing"

	"github.com/benngarcia/mercator/internal/capability"
	"github.com/benngarcia/mercator/internal/node"
)

// TestTheWorldAnswersAboutANodeAndAGenerationTogether holds the Lab's registry to
// what node.Registry does with the same question, because a simulator that is
// more forgiving than the authority it stands in for turns a deployment that
// cannot make progress into a green fixture.
//
// The real registry refuses a question about a generation the identity is not on:
// the pair is what every act against a machine is addressed to, and answering
// "enrolled, healthy" about the wrong one would be answering about a machine that
// no longer exists. A world that looked the node up by name alone would report
// capacity ready to launch on where production errors out of the same look.
func TestTheWorldAnswersAboutANodeAndAGenerationTogether(t *testing.T) {
	ctx := context.Background()
	world := labWorldFor(t, "../scenario/scenarios/conformance/provisioned-capacity-becomes-a-machine-mercator-holds.json")
	if _, err := world.Invite(ctx, node.Invitation{
		WorkspaceID:           labWorkspace,
		NodeID:                "nod_fenced",
		RentalID:              strandedRental,
		Generation:            1,
		ShadowPriceUSDPerHour: 2,
	}); err != nil {
		t.Fatalf("invite the node: %v", err)
	}

	invited, err := world.Enrolled(ctx, capability.NodeRef{WorkspaceID: labWorkspace, NodeID: "nod_fenced", Generation: 1})
	if err != nil {
		t.Fatalf("the generation this node was invited for was refused: %v", err)
	}
	if invited {
		t.Fatal("a node nothing has enrolled on reads as though its agent had opened a session")
	}

	_, err = world.Enrolled(ctx, capability.NodeRef{WorkspaceID: labWorkspace, NodeID: "nod_fenced", Generation: 2})
	if err == nil {
		t.Fatal("a question about a generation this node is not on was answered rather than refused")
	}
	if !strings.Contains(err.Error(), "is generation 1, not 2") {
		t.Fatalf("the refusal does not say which generation the node is on: %v", err)
	}
}
