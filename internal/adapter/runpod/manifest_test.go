package runpod

import "testing"

func TestManifestExposesOptionalContainerRegistryAuthentication(t *testing.T) {
	for _, field := range Manifest().ConfigFields {
		if field.Name != "container_registry_auth_id" {
			continue
		}
		if field.Type != "string" {
			t.Fatalf("container_registry_auth_id type = %q, want string", field.Type)
		}
		if field.Required {
			t.Fatal("public-image RunPod connections must not require registry authentication")
		}
		return
	}

	t.Fatal("runpod manifest must expose container_registry_auth_id")
}
