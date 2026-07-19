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

func TestManifestExposesOptionalRegistryPullCredential(t *testing.T) {
	manifest := Manifest()
	if manifest.Credential.Required {
		t.Fatal("public-image Docker connections must remain credential-free")
	}
	fields := map[string]bool{}
	for _, field := range manifest.ConfigFields {
		fields[field.Name] = true
	}
	for _, name := range []string{"registry_server", "registry_username"} {
		if !fields[name] {
			t.Fatalf("docker manifest must expose %s", name)
		}
	}
}
