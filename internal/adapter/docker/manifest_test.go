package docker

import "testing"

func TestManifestExposesPerConnectionArchitectureOverride(t *testing.T) {
	fields := map[string]bool{}
	for _, field := range Manifest().ConfigFields {
		fields[field.Name] = true
	}
	if !fields["arch"] {
		t.Fatal("docker manifest must expose the per-connection arch override")
	}
}
