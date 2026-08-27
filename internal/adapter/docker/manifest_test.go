package docker

import "testing"

func TestManifestExposesDockerRuntimeConfiguration(t *testing.T) {
	fields := map[string]bool{}
	for _, field := range Manifest().ConfigFields {
		fields[field.Name] = true
	}
	for _, name := range []string{"arch", "deployment_id"} {
		if !fields[name] {
			t.Fatalf("docker manifest must expose %s", name)
		}
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
