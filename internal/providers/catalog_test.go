package providers

import (
	"testing"
)

func TestDefaultCatalogOwnsProviderDefinitions(t *testing.T) {
	catalog := Default()

	manifests := catalog.Factory().Manifests()
	if len(manifests) != 4 {
		t.Fatalf("manifests = %+v, want four shipped providers", manifests)
	}
	for _, adapterType := range []string{"docker", "runpod", "shadeform", "vast"} {
		definition, found := catalog.Definition(adapterType)
		if !found {
			t.Errorf("definition %q is missing", adapterType)
			continue
		}
		if definition.Manifest.Type != adapterType {
			t.Errorf("definition %q manifest type = %q", adapterType, definition.Manifest.Type)
		}
	}
}

func TestDefaultCatalogValidatesProviderConfiguration(t *testing.T) {
	catalog := Default()
	docker, found := catalog.Definition("docker")
	if !found {
		t.Fatal("docker definition is missing")
	}

	if err := docker.Validate(map[string]string{"host": "tcp://gpu:2376", "context": "gpu"}); err == nil {
		t.Fatal("docker validation accepted host and context together")
	}
	if err := docker.Validate(map[string]string{"not_public": "true"}); err == nil {
		t.Fatal("docker validation accepted a non-public config key")
	}
	if err := docker.Validate(map[string]string{"arch": "arm64"}); err != nil {
		t.Fatalf("docker validation rejected public config: %v", err)
	}
}
