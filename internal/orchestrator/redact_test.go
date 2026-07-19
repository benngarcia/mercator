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
