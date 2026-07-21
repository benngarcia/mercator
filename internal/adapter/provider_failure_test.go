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
		AdapterType:     "shadeform",
		OfferSnapshotID: "off_1",
		OfferNativeRef:  "cloud/region/type-a",
	}
	second := request
	second.DiagnosticContext = adapter.ProviderOperationContext{
		RunID:           "run_2",
		AttemptID:       "att_2",
		AdapterType:     "shadeform",
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
