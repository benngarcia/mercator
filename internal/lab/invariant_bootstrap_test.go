package lab

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/capability"
	"github.com/benngarcia/mercator/internal/eventlog"
	"github.com/benngarcia/mercator/internal/node"
)

// The credential every case here is about, and the node it was minted for. It is
// named once because the whole rule turns on whether this exact string is used
// twice or written down anywhere.
const (
	renewingNode  = "nod_renewing"
	renewingLease = "rnt_renewing"
	mintedToken   = "enrollment-material-nobody-may-keep"
)

// TestEveryClauseOfTheBootstrapCredentialRuleCanFail reads
// safety.bootstrap_credential_is_short_lived_and_single_use the way every law
// here has to be readable. Three clauses, three worlds, and each world is the
// one defect that clause exists to catch.
//
// A credential carried to two machines is one invitation two hosts can enrol as,
// and Mercator would then address every command about the first to whichever
// answered last. A credential redeemed twice is an invitation that was never
// spent by being used, which is the whole of what makes a bootstrap short-lived.
// A credential in Mercator's own event log is a credential in every export of
// that log for as long as the record exists, which is far longer than the thirty
// minutes an invitation stays redeemable.
func TestEveryClauseOfTheBootstrapCredentialRuleCanFail(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	cases := map[string]func(*InvariantObservation){
		"one invitation carried to two machines": func(observation *InvariantObservation) {
			observation.BootstrapCredentials = []bootstrapCredential{mintedFor(2, 1)}
		},
		"one invitation redeemed twice": func(observation *InvariantObservation) {
			observation.BootstrapCredentials = []bootstrapCredential{mintedFor(1, 2)}
		},
		"an invitation with no material to present": func(observation *InvariantObservation) {
			credential := mintedFor(1, 1)
			credential.Token = ""
			observation.BootstrapCredentials = []bootstrapCredential{credential}
		},
		"the invitation written into a Mercator event": func(observation *InvariantObservation) {
			observation.BootstrapCredentials = []bootstrapCredential{mintedFor(1, 1)}
			observation.MercatorEvents = []eventlog.CloudEvent{{
				Subject: "nodes/" + renewingNode,
				Data:    []byte(`{"node_id":"` + renewingNode + `","enrollment_token":"` + mintedToken + `"}`),
			}}
		},
	}

	for name, world := range cases {
		t.Run(name, func(t *testing.T) {
			observation := handWrittenLedger(now)
			world(&observation)

			if err := bootstrapCredentialIsShortLivedAndSingleUse(observation); err == nil {
				t.Fatal("a bootstrap credential was reused or written down and nothing objected")
			}
		})
	}
}

// TestABootstrapUsedOnceAndKeptNowhereHolds is the other half, and the half that
// stops the rule from being a rule against bootstrapping. One credential, one
// machine, one redemption, and a record that carries the node's identity in the
// open because identity is not a secret.
func TestABootstrapUsedOnceAndKeptNowhereHolds(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	observation := handWrittenLedger(now, enrolmentEffect())
	observation.BootstrapCredentials = []bootstrapCredential{mintedFor(1, 1)}
	observation.MercatorEvents = []eventlog.CloudEvent{{
		Subject: "nodes/" + renewingNode,
		Data:    []byte(`{"node_id":"` + renewingNode + `","generation":1}`),
	}}

	if err := bootstrapCredentialIsShortLivedAndSingleUse(observation); err != nil {
		t.Fatalf("a bootstrap redeemed once and kept nowhere was refused: %v", err)
	}
}

// TestTheWorldHandsOneInvitationToTwoMachinesAndSaysSo is what the hand-written
// worlds above cannot be: the proof that this world really produces the
// disagreement the first clause is about, so a control plane that made it would
// be caught rather than agreed with.
//
// The arrangement is exactly the defect. One bootstrap is minted for one node,
// and two allocations are asked for carrying it, which is what a control plane
// that reused a bootstrap across a retry under a fresh lease would do. The world
// records the credential arriving on both machines, because that is what
// happened, and the rule reads it off World Truth rather than off anything
// Mercator wrote about itself.
func TestTheWorldHandsOneInvitationToTwoMachinesAndSaysSo(t *testing.T) {
	ctx := context.Background()
	world := labWorldFor(t, "../scenario/scenarios/conformance/a-machine-keeps-working-past-its-first-session.json")
	registry := labRegistryFor(world)
	bootstrap, err := registry.Invite(ctx, node.Invitation{
		WorkspaceID:           labWorkspace,
		NodeID:                renewingNode,
		RentalID:              renewingLease,
		Generation:            1,
		ShadowPriceUSDPerHour: 3,
	})
	if err != nil {
		t.Fatalf("invite the node: %v", err)
	}

	for _, rentalID := range []string{renewingLease, renewingLease + "-again"} {
		if _, err := world.ProvisionCapacity(ctx, capability.ProvisionCommand{
			WorkspaceID:     labWorkspace,
			ConnectionID:    labConnection,
			OperationKey:    "provision_" + rentalID,
			RequestHash:     "sha256:one-bootstrap-two-machines",
			RentalID:        rentalID,
			Generation:      1,
			OfferSnapshotID: "fresh-h100",
			Bootstrap:       bootstrap,
		}); err != nil {
			t.Fatalf("allocate the machine for %s: %v", rentalID, err)
		}
	}

	observation := handWrittenLedger(world.now, world.effectRecords()...)
	observation.BootstrapCredentials = world.invariantFacts().BootstrapCredentials
	err = bootstrapCredentialIsShortLivedAndSingleUse(observation)

	if err == nil {
		t.Fatal("one invitation was handed to two machines and the ledger reads as though each had its own")
	}
	if want := "handed to 2 machines"; !strings.Contains(err.Error(), want) {
		t.Fatalf("violation = %q, want it to say %q", err, want)
	}
}

// TestThisWorldRefusesAnInvitationASecondMachineAlreadyRedeemed is the fidelity
// claim behind the second clause. A double redemption cannot be produced through
// this world's own registry, because this world refuses it exactly as production
// does: the invitation is spent by the machine that redeems it, and the second
// machine holding the same material opens no session at all.
//
// It is asserted rather than assumed. A world that quietly admitted the second
// machine would make the clause above unfalsifiable, and the hand-written world
// would then be checking a rule against a state nothing can reach.
func TestThisWorldRefusesAnInvitationASecondMachineAlreadyRedeemed(t *testing.T) {
	ctx := context.Background()
	world := labWorldFor(t, "../scenario/scenarios/conformance/a-machine-keeps-working-past-its-first-session.json")
	registry := labRegistryFor(world)
	bootstrap, err := registry.Invite(ctx, node.Invitation{
		WorkspaceID:           labWorkspace,
		NodeID:                renewingNode,
		RentalID:              renewingLease,
		Generation:            1,
		ShadowPriceUSDPerHour: 3,
	})
	if err != nil {
		t.Fatalf("invite the node: %v", err)
	}
	for _, rentalID := range []string{renewingLease, renewingLease + "-again"} {
		if _, err := world.ProvisionCapacity(ctx, capability.ProvisionCommand{
			WorkspaceID:     labWorkspace,
			ConnectionID:    labConnection,
			OperationKey:    "provision_" + rentalID,
			RequestHash:     "sha256:one-bootstrap-two-machines",
			RentalID:        rentalID,
			Generation:      1,
			OfferSnapshotID: "fresh-h100",
			Bootstrap:       bootstrap,
		}); err != nil {
			t.Fatalf("allocate the machine for %s: %v", rentalID, err)
		}
	}

	world.setNow(world.now.Add(10 * time.Minute))
	if err := world.deliverEnrolments(ctx, registry); err != nil {
		t.Fatalf("deliver the enrolment: %v", err)
	}

	credentials := world.invariantFacts().BootstrapCredentials
	if len(credentials) != 1 {
		t.Fatalf("this world minted %d credentials, want the one it was asked for", len(credentials))
	}
	if credentials[0].Redemptions != 1 {
		t.Fatalf("the invitation was redeemed %d times, and an invitation is spent by redeeming it", credentials[0].Redemptions)
	}
}

// TestAnEnrollmentTokenFiledUnderAnHonestNameIsStillASecret is the deliberate
// failing world for the clause safety.secrets_absent grew. The rule matched field
// names, so material in a field called credential, password, or secret was
// caught and the same material in a field called enrollment_token was not, which
// is the field a bootstrap would honestly be filed under.
//
// The three clauses are shown failing separately, because the name clause on its
// own passed both of the others for the whole life of the rule.
func TestEveryClauseOfTheSecretsRuleCanFail(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	cases := map[string]func(*InvariantObservation){
		"material in a field that says what it is": func(observation *InvariantObservation) {
			observation.MercatorEvents = []eventlog.CloudEvent{{Data: []byte(`{"password":"exposed"}`)}}
		},
		"a presigned read written down as a location": func(observation *InvariantObservation) {
			observation.Effects = []EffectRecord{{
				Operation: OperationNodePrepareArtifact,
				Command:   EffectCommandAccepted,
				Request:   []byte(`{"source":"https://objects.test/datasets/corpus?X-Amz-Signature=deadbeef"}`),
			}}
		},
		"an enrollment token in the field it belongs in": func(observation *InvariantObservation) {
			observation.BootstrapCredentials = []bootstrapCredential{mintedFor(1, 1)}
			observation.Effects = []EffectRecord{{
				Operation: OperationCapacityProvision,
				Command:   EffectCommandAccepted,
				Request:   []byte(`{"rental_id":"` + renewingLease + `","enrollment_token":"` + mintedToken + `"}`),
			}}
		},
	}

	for name, world := range cases {
		t.Run(name, func(t *testing.T) {
			observation := handWrittenLedger(now)
			world(&observation)

			if err := secretsAbsent(observation); err == nil {
				t.Fatal("credential material reached Mercator's record and nothing objected")
			}
		})
	}
}

// refusedOnTheMachine is the credential a machine presented and the registry
// would not take: it reached a host, that host offered it, and the one door it
// has was shut.
func refusedOnTheMachine() bootstrapCredential {
	credential := mintedFor(1, 0)
	credential.Refused = true
	return credential
}

func mintedFor(provisions, redemptions int) bootstrapCredential {
	return bootstrapCredential{
		NodeID:      renewingNode,
		Generation:  1,
		Token:       mintedToken,
		Provisions:  provisions,
		Redemptions: redemptions,
	}
}
