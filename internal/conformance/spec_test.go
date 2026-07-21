package conformance_test

import (
	"strings"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/conformance"
)

func TestTrialSpecAcceptsProviderConfiguration(t *testing.T) {
	tests := []struct {
		name string
		spec conformance.TrialSpec
	}{
		{name: "local docker daemon", spec: trialSpec("docker", "", map[string]string{"bin": "/usr/local/bin/docker"})},
		{name: "runpod secure cloud", spec: trialSpec("runpod", "RUNPOD_API_KEY", map[string]string{"gpu_types": "NVIDIA RTX A4000", "allow_community_cloud": "false", "container_disk_gb": "40"})},
		{name: "shadeform managed cloud", spec: trialSpec("shadeform", "SHADEFORM_API_KEY", map[string]string{"shade_cloud": "true", "allowed_clouds": "lambdalabs,datacrunch", "max_lifetime_hours": "2"})},
		{name: "vast secure tier", spec: trialSpec("vast", "VAST_API_KEY", map[string]string{"gpu_names": "RTX_4090,H100 SXM", "container_disk_gb": "30", "offer_limit": "10"})},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := conformance.ValidateSpec(test.spec, credentialEnv); err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func TestTrialSpecRejectsInvalidProviderConfiguration(t *testing.T) {
	tests := []struct {
		name    string
		spec    conformance.TrialSpec
		wantErr string
	}{
		{name: "unknown adapter", spec: trialSpec("fake", "", nil), wantErr: `unsupported adapter "fake"`},
		{name: "docker credential", spec: trialSpec("docker", "DOCKER_API_KEY", nil), wantErr: "docker does not accept a credential environment variable"},
		{name: "docker host and context", spec: trialSpec("docker", "", map[string]string{"host": "ssh://gpu.test", "context": "gpu"}), wantErr: "docker config cannot set both host and context"},
		{name: "remote docker public URL omitted", spec: trialSpec("docker", "", map[string]string{"host": "ssh://gpu.test"}), wantErr: "docker requires listen_address"},
		{name: "remote docker listener uses ephemeral port", spec: func() conformance.TrialSpec {
			spec := trialSpec("docker", "", map[string]string{"host": "ssh://gpu.test"})
			spec.ListenAddress = "127.0.0.1:0"
			spec.PublicURL = "https://mercator-conformance.example.com"
			return spec
		}(), wantErr: "docker listen_address must use a fixed port"},
		{name: "cloud credential omitted", spec: trialSpec("runpod", "", nil), wantErr: "runpod requires credential_env"},
		{name: "cloud listener omitted", spec: func() conformance.TrialSpec {
			spec := trialSpec("runpod", "RUNPOD_API_KEY", nil)
			spec.ListenAddress = ""
			return spec
		}(), wantErr: "runpod requires listen_address"},
		{name: "cloud listener uses ephemeral port", spec: func() conformance.TrialSpec {
			spec := trialSpec("runpod", "RUNPOD_API_KEY", nil)
			spec.ListenAddress = "127.0.0.1:0"
			return spec
		}(), wantErr: "runpod listen_address must use a fixed port"},
		{name: "cloud public URL omitted", spec: func() conformance.TrialSpec {
			spec := trialSpec("runpod", "RUNPOD_API_KEY", nil)
			spec.PublicURL = ""
			return spec
		}(), wantErr: "runpod requires public_url"},
		{name: "cloud public URL is not HTTP", spec: func() conformance.TrialSpec {
			spec := trialSpec("runpod", "RUNPOD_API_KEY", nil)
			spec.PublicURL = "ssh://mercator.example.com"
			return spec
		}(), wantErr: "public_url must use http or https"},
		{name: "literal credential", spec: trialSpec("runpod", "rpa_live_123", nil), wantErr: "credential_env must be an uppercase environment variable name"},
		{name: "reserved broker environment", spec: trialSpec("shadeform", "MERCATOR_SECRET_KEY", nil), wantErr: `credential_env "MERCATOR_SECRET_KEY" is reserved`},
		{name: "credential environment is empty", spec: trialSpec("vast", "EMPTY_API_KEY", nil), wantErr: `credential environment variable "EMPTY_API_KEY" is empty`},
		{name: "runpod removed config", spec: trialSpec("runpod", "RUNPOD_API_KEY", map[string]string{"cloud_type": "SECURE"}), wantErr: `runpod config key "cloud_type" is not public`},
		{name: "shadeform internal config", spec: trialSpec("shadeform", "SHADEFORM_API_KEY", map[string]string{"base_url": "https://shadeform.test"}), wantErr: `shadeform config key "base_url" is not public`},
		{name: "vast internal config", spec: trialSpec("vast", "VAST_API_KEY", map[string]string{"base_url": "https://vast.test"}), wantErr: `vast config key "base_url" is not public`},
		{name: "runpod invalid boolean", spec: trialSpec("runpod", "RUNPOD_API_KEY", map[string]string{"allow_community_cloud": "yes"}), wantErr: "runpod config allow_community_cloud must be true or false"},
		{name: "runpod zero disk", spec: trialSpec("runpod", "RUNPOD_API_KEY", map[string]string{"container_disk_gb": "0"}), wantErr: "runpod config container_disk_gb must be a positive integer"},
		{name: "shadeform negative lifetime", spec: trialSpec("shadeform", "SHADEFORM_API_KEY", map[string]string{"max_lifetime_hours": "-1"}), wantErr: "shadeform config max_lifetime_hours must be a positive integer"},
		{name: "vast nonnumeric disk", spec: trialSpec("vast", "VAST_API_KEY", map[string]string{"container_disk_gb": "large"}), wantErr: "vast config container_disk_gb must be a positive integer"},
		{name: "vast zero offer limit", spec: trialSpec("vast", "VAST_API_KEY", map[string]string{"offer_limit": "0"}), wantErr: "vast config offer_limit must be a positive integer"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := conformance.ValidateSpec(test.spec, credentialEnv)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Validate() error = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}

func trialSpec(adapter, credentialEnv string, config map[string]string) conformance.TrialSpec {
	spec := conformance.TrialSpec{
		AdapterType:        adapter,
		CredentialEnv:      credentialEnv,
		Config:             config,
		Image:              "ghcr.io/benngarcia/mercator-conformance@sha256:0000000000000000000000000000000000000000000000000000000000000000",
		Mode:               conformance.ProbeMode,
		MaxExpectedCostUSD: 0.50,
		Timeout:            time.Minute,
	}
	if adapter != "docker" && adapter != "fake" {
		spec.ListenAddress = "127.0.0.1:8091"
		spec.PublicURL = "https://mercator-conformance.example.com"
	}
	return spec
}

func credentialEnv(name string) (string, bool) {
	values := map[string]string{"RUNPOD_API_KEY": "runpod-test-sentinel", "SHADEFORM_API_KEY": "shadeform-test-sentinel", "VAST_API_KEY": "vast-test-sentinel"}
	value, found := values[name]
	return value, found
}
