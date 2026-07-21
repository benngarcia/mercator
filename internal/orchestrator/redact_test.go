package orchestrator

import (
	"fmt"
	"testing"

	"github.com/benngarcia/mercator/internal/adapter"
)

func TestPublicAdapterErrorRedactsRegistryAuthenticationFailure(t *testing.T) {
	err := fmt.Errorf("%w: unauthorized: secret-token", adapter.ErrRegistryAuthentication)
	got := publicAdapterError(err, "launch_1")
	if got.Code != "ADAPTER_REGISTRY_AUTHENTICATION_FAILED" {
		t.Fatalf("code = %q, want ADAPTER_REGISTRY_AUTHENTICATION_FAILED", got.Code)
	}
	if got.Message != "Registry authentication failed." {
		t.Fatalf("message = %q, want redacted registry authentication message", got.Message)
	}
	if got.Retryable {
		t.Fatal("invalid registry credential must not retry without a configuration change")
	}
}

func TestPublicAdapterErrorMapsProviderKindsWithoutPrivateDiagnostics(t *testing.T) {
	tests := []struct {
		kind adapter.ProviderFailureKind
		code string
	}{
		{adapter.ProviderFailureCapacityUnavailable, "PROVIDER_CAPACITY_UNAVAILABLE"},
		{adapter.ProviderFailureInvalidRequest, "PROVIDER_INVALID_REQUEST"},
		{adapter.ProviderFailureAuthentication, "PROVIDER_AUTHENTICATION_FAILED"},
		{adapter.ProviderFailureRateLimited, "PROVIDER_RATE_LIMITED"},
		{adapter.ProviderFailureTransport, "PROVIDER_TRANSPORT_FAILURE"},
		{adapter.ProviderFailureInternal, "PROVIDER_INTERNAL_ERROR"},
	}
	for _, test := range tests {
		t.Run(string(test.kind), func(t *testing.T) {
			err := &adapter.ProviderFailure{
				Kind:         test.kind,
				Status:       409,
				ProviderCode: "PRIVATE_PROVIDER_CODE",
				Retryable:    true,
				ResponseBody: `{"private":"provider body"}`,
			}

			got := publicAdapterError(err, "launch_1")

			if got.Code != test.code || !got.Retryable || got.LaunchKey != "launch_1" {
				t.Fatalf("public error = %+v", got)
			}
			if got.Message == "" || got.Message == err.Error() {
				t.Fatalf("public message must be stable and redacted: %+v", got)
			}
		})
	}
}
