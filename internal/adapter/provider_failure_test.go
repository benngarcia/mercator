package adapter_test

import (
	"testing"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/domain"
)

func TestDiagnosticCorrelationDoesNotChangeCleanupOperationIdentity(t *testing.T) {
	request := adapter.ReleaseRequest{
		WorkspaceID:       "ws_1",
		ConnectionID:      "conn_1",
		OperationKey:      "release_att_1",
		LaunchKey:         "launch_att_1",
		OwnershipToken:    "own_att_1",
		LaunchRequestHash: "sha256:launch",
	}
	first := request
	first.DiagnosticContext = adapter.ProviderOperationContext{
		RunID:           "run_1",
		AttemptID:       "att_1",
		OfferSnapshotID: "off_1",
		OfferNativeRef:  "cloud/region/type-a",
	}
	second := request
	second.DiagnosticContext = adapter.ProviderOperationContext{
		RunID:           "run_2",
		AttemptID:       "att_2",
		OfferSnapshotID: "off_2",
		OfferNativeRef:  "cloud/region/type-b",
	}

	firstHash, err := domain.CanonicalHash(first)
	if err != nil {
		t.Fatalf("hash first cleanup request: %v", err)
	}
	secondHash, err := domain.CanonicalHash(second)
	if err != nil {
		t.Fatalf("hash second cleanup request: %v", err)
	}

	if firstHash != secondHash {
		t.Fatalf("diagnostic correlation changed operation identity: %q != %q", firstHash, secondHash)
	}
}

func TestDiagnosticActionabilityDoesNotChangeLaunchOperationIdentity(t *testing.T) {
	request := adapter.LaunchRequest{
		OperationKey:              "launch_att_1",
		WorkspaceID:               "ws_1",
		RunID:                     "run_1",
		AttemptID:                 "att_1",
		SelectedOfferConnectionID: "conn_1",
		SelectedOfferSnapshotID:   "off_1",
	}
	warning := request
	warning.DiagnosticContext.AlternativesExhausted = false
	actionable := request
	actionable.DiagnosticContext.AlternativesExhausted = true

	warningHash, err := domain.CanonicalHash(warning)
	if err != nil {
		t.Fatalf("hash warning launch request: %v", err)
	}
	actionableHash, err := domain.CanonicalHash(actionable)
	if err != nil {
		t.Fatalf("hash actionable launch request: %v", err)
	}

	if warningHash != actionableHash {
		t.Fatalf("diagnostic actionability changed launch identity: %q != %q", warningHash, actionableHash)
	}
}
