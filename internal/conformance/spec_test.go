package conformance_test

import (
	"strings"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/conformance"
)

func TestTrialRequiresEveryBillingAndCleanupBound(t *testing.T) {
	valid := conformance.Trial{
		AdapterType:        "runpod",
		CredentialEnv:      "RUNPOD_API_KEY",
		Image:              probeImage(),
		MaxExpectedCostUSD: 0.50,
		Timeout:            12 * time.Minute,
		Mode:               conformance.ModeProbe,
	}
	tests := []struct {
		name    string
		change  func(*conformance.Trial)
		wantErr string
	}{
		{name: "image", change: func(trial *conformance.Trial) { trial.Image = "probe:latest" }, wantErr: "digest-pinned"},
		{name: "budget", change: func(trial *conformance.Trial) { trial.MaxExpectedCostUSD = 0 }, wantErr: "must be positive"},
		{name: "timeout", change: func(trial *conformance.Trial) { trial.Timeout = 0 }, wantErr: "must be positive"},
		{name: "credential", change: func(trial *conformance.Trial) { trial.CredentialEnv = "" }, wantErr: "requires credential_env"},
		{name: "mode", change: func(trial *conformance.Trial) { trial.Mode = "future" }, wantErr: "unsupported mode"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			trial := valid
			test.change(&trial)
			err := conformance.ValidateTrial(trial, credentialEnv)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("ValidateTrial() error = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}

func TestTrialAcceptsEveryProductionProvider(t *testing.T) {
	trials := []conformance.Trial{
		{AdapterType: "docker", Mode: conformance.ModeProbe, Image: probeImage(), MaxExpectedCostUSD: 0.50, Timeout: time.Minute},
		{AdapterType: "runpod", CredentialEnv: "RUNPOD_API_KEY", Image: probeImage(), MaxExpectedCostUSD: 0.50, Timeout: time.Minute},
		{AdapterType: "shadeform", CredentialEnv: "SHADEFORM_API_KEY", Image: probeImage(), MaxExpectedCostUSD: 0.50, Timeout: time.Minute},
		{AdapterType: "vast", CredentialEnv: "VAST_API_KEY", Image: probeImage(), MaxExpectedCostUSD: 0.50, Timeout: time.Minute},
	}
	for _, trial := range trials {
		if err := conformance.ValidateTrial(trial, credentialEnv); err != nil {
			t.Errorf("ValidateTrial(%s): %v", trial.AdapterType, err)
		}
	}
}

// TestATrialMaySetEveryKeyProductionReads is the disagreement this test exists to
// keep closed. A trial's configuration is checked against the adapter's own
// manifest, and a key the adapter reads but does not declare is one production
// accepts silently and conformance rejects: the same connection configured the
// same way is legal in one and refused in the other, and the refusal arrives
// only when somebody tries to verify it against a real account.
func TestATrialMaySetEveryKeyProductionReads(t *testing.T) {
	trial := conformance.Trial{
		AdapterType:        "shadeform",
		CredentialEnv:      "SHADEFORM_API_KEY",
		Image:              probeImage(),
		MaxExpectedCostUSD: 0.50,
		Timeout:            time.Minute,
		Config: map[string]string{
			"agent_download_url": "https://downloads.example.com/mercator-node/{version}/linux-amd64",
			"base_url":           "https://api.shadeform.ai/v1",
			"os":                 "ubuntu22.04_cuda12.2_shade_os",
			"shade_cloud":        "true",
			"allowed_clouds":     "hyperstack,lambdalabs",
			"max_lifetime_hours": "6",
		},
	}

	if err := conformance.ValidateTrial(trial, credentialEnv); err != nil {
		t.Fatalf("ValidateTrial(shadeform with the keys its adapter reads): %v", err)
	}
}

func probeImage() string {
	return "ghcr.io/benngarcia/mercator-conformance-probe@sha256:" + strings.Repeat("0", 64)
}

func credentialEnv(name string) (string, bool) {
	values := map[string]string{
		"RUNPOD_API_KEY":    "runpod-test-sentinel",
		"SHADEFORM_API_KEY": "shadeform-test-sentinel",
		"VAST_API_KEY":      "vast-test-sentinel",
	}
	value, found := values[name]
	return value, found
}
