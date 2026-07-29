package lab

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/domain"
)

// The fetch every case here is about. It is one workspace's pull of one private
// image, named once because the whole rule turns on whether the credential that
// arrived on it says the same four things the command does.
const (
	pulledContent    = "sha256:4f1c9b2e7d5a3c8f0b6e4d2a9c7f5b3e1d8a6c4f2b0e9d7a5c3f1b8e6d4a2c09"
	pullingWorkspace = "ws_lab"
	pullSecret       = "registry-material-nobody-may-keep"
)

// TestEveryClauseOfTheContentCredentialRuleCanFail reads
// safety.content_credentials_are_scoped_and_expiring the way every law here has
// to be readable. Four clauses, four worlds, and each world is the one defect
// that clause exists to catch.
//
// A credential with no expiry is the registry account under another name: an
// attacker who takes the host next month reads the workspace's private images
// with it. A credential minted for another workspace's pull is the tenancy
// boundary crossed by a string, and it is internally consistent, so only reading
// it against the command it arrived on catches it. A credential handed over after
// it lapsed is material the far side will refuse, which an operator reads as a
// broken registry rather than as a control plane minting too late. And a
// credential good for a day is ahead of every moment in any execution and is
// still a standing right.
func TestEveryClauseOfTheContentCredentialRuleCanFail(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	cases := map[string]func(*InvariantObservation){
		"a registry credential with nothing to expire it": func(observation *InvariantObservation) {
			observation.ContentCredentials = []contentCredential{credentialWithNoExpiry(now)}
		},
		"one pull's credential spent on another workspace's pull": func(observation *InvariantObservation) {
			reused := mintedForThePull(now)
			reused.WorkspaceID = "ws_other_tenant"
			observation.ContentCredentials = []contentCredential{reused}
		},
		"a credential handed over after it lapsed": func(observation *InvariantObservation) {
			lapsed := mintedForThePull(now)
			lapsed.Scope.ExpiresAt = now.Add(-time.Minute)
			observation.ContentCredentials = []contentCredential{lapsed}
		},
		"a read of the object store that stays good for a day": func(observation *InvariantObservation) {
			standing := mintedForThePull(now)
			standing.Scope.ExpiresAt = now.Add(24 * time.Hour)
			observation.ContentCredentials = []contentCredential{standing}
		},
	}

	for name, world := range cases {
		t.Run(name, func(t *testing.T) {
			observation := handWrittenLedger(now)
			world(&observation)

			if err := contentCredentialsAreScopedAndExpiring(observation); err == nil {
				t.Fatal("a machine was handed a credential wider than its fetch and nothing objected")
			}
		})
	}
}

// TestACredentialMintedForOneFetchAndBoundedHolds is the other half, and the half
// that stops the rule from being a rule against fetching content at all. One
// operation, one workspace, one digest, and a window measured in minutes.
func TestACredentialMintedForOneFetchAndBoundedHolds(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	observation := handWrittenLedger(now)
	observation.ContentCredentials = []contentCredential{mintedForThePull(now)}

	if err := contentCredentialsAreScopedAndExpiring(observation); err != nil {
		t.Fatalf("a credential minted for one fetch and expiring inside it was refused: %v", err)
	}
}

// TestTheRegistryMaterialAMachineWasHandedIsStillASecret is the clause
// safety.secrets_absent grew for the same reason it grew one for enrollment
// tokens. The rule matched three field names and five signed-URL markers, so a
// registry password filed under a name nobody thought of was written into the
// record and read back out of every export of it.
func TestTheRegistryMaterialAMachineWasHandedIsStillASecret(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	observation := handWrittenLedger(now)
	observation.ContentCredentials = []contentCredential{mintedForThePull(now)}
	observation.Effects = []EffectRecord{{
		Operation: OperationNodePrepareImage,
		Command:   EffectCommandAccepted,
		Request:   []byte(`{"content":"` + pulledContent + `","pull_token":"` + pullSecret + `"}`),
	}}

	err := secretsAbsent(observation)

	if err == nil {
		t.Fatal("the material a machine pulls with reached Mercator's record and nothing objected")
	}
	if want := "whatever field it is filed under"; !strings.Contains(err.Error(), want) {
		t.Fatalf("violation = %q, want it to say %q", err, want)
	}
}

// TestThisWorldRefusesAPrivatePullNobodyMintedFor is the fidelity claim behind
// the whole rule. The hand-written worlds above prove the rule can fail; this
// proves the world can produce the failure, which is what stops the rule being a
// law about a state nothing reaches.
//
// The arrangement is the defect exactly. A private image is asked for on a
// machine with no credential at all, which is what every Mercator before this
// slice sent: RegistryCredential had been declared since phase 2 and populated by
// nobody. The registry answers the way a real one does, the fetch is refused, and
// nothing lands on the disk.
func TestThisWorldRefusesAPrivatePullNobodyMintedFor(t *testing.T) {
	world := labWorldFor(t, "../scenario/scenarios/conformance/a-private-pull-uses-a-credential-that-expires.json")
	anonymous := adapter.PrepareItem{
		Kind:            adapter.PrepareImage,
		OfferSnapshotID: "builder",
		RunID:           "run-analysis",
		Image:           privatePullImage,
	}

	receipt, err := world.Prepare(context.Background(), adapter.PrepareRequest{
		WorkspaceID:  labWorkspace,
		OperationKey: "prepare/anonymous",
		Wanted:       []adapter.PrepareItem{anonymous},
	})

	if err != nil {
		t.Fatalf("ask the machine to prepare the private image: %v", err)
	}
	if len(receipt.Refused) != 1 {
		t.Fatalf("the registry refused %d of the fetches it was asked for, want the one anonymous read", len(receipt.Refused))
	}
	if len(receipt.Started) != 0 {
		t.Fatalf("the machine started %d transfers of an image nobody may read anonymously", len(receipt.Started))
	}
}

// TestThisWorldServesAPrivatePullTheControlPlaneMintedFor is the other side of
// the same seam, and the one that stops the case above from passing because this
// world refuses every pull. The same image, the same machine, and material minted
// for that one operation: the fetch is taken on and the bytes move.
func TestThisWorldServesAPrivatePullTheControlPlaneMintedFor(t *testing.T) {
	world := labWorldFor(t, "../scenario/scenarios/conformance/a-private-pull-uses-a-credential-that-expires.json")
	item := adapter.PrepareItem{
		Kind:            adapter.PrepareImage,
		OfferSnapshotID: "builder",
		RunID:           "run-analysis",
		Image:           privatePullImage,
	}
	item.RegistryCredential = domain.RegistryPull{
		ContentCredentialScope: domain.ContentCredentialScope{
			Operation:   item.Operation(),
			WorkspaceID: labWorkspace,
			Content:     item.Content(),
			ExpiresAt:   world.now.Add(15 * time.Minute),
		},
		Registry: domain.ReferenceRegistry(privatePullImage),
		Username: "mercator-lab",
		Secret:   pullSecret,
	}

	receipt, err := world.Prepare(context.Background(), adapter.PrepareRequest{
		WorkspaceID:  labWorkspace,
		OperationKey: "prepare/minted",
		Wanted:       []adapter.PrepareItem{item},
	})

	if err != nil {
		t.Fatalf("ask the machine to prepare the private image: %v", err)
	}
	if len(receipt.Refused) != 0 {
		t.Fatalf("a pull carrying material minted for it was refused: %v", receipt.Refused)
	}
	if len(receipt.Started) != 1 {
		t.Fatalf("the machine started %d transfers, want the one it was authorised for", len(receipt.Started))
	}
}

func mintedForThePull(now time.Time) contentCredential {
	return contentCredential{
		Kind:        adapter.PrepareImage,
		Operation:   "prepare:image:builder:" + pulledContent,
		WorkspaceID: pullingWorkspace,
		Content:     pulledContent,
		Scope: domain.ContentCredentialScope{
			Operation:   "prepare:image:builder:" + pulledContent,
			WorkspaceID: pullingWorkspace,
			Content:     pulledContent,
			ExpiresAt:   now.Add(15 * time.Minute),
		},
		Material: pullSecret,
		At:       now,
	}
}

func credentialWithNoExpiry(now time.Time) contentCredential {
	credential := mintedForThePull(now)
	credential.Scope.ExpiresAt = time.Time{}
	return credential
}
