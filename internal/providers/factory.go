// Package providers owns Mercator's production provider catalog.
package providers

import (
	"github.com/benngarcia/mercator/internal/adapter"
	dockeradapter "github.com/benngarcia/mercator/internal/adapter/docker"
	runpodadapter "github.com/benngarcia/mercator/internal/adapter/runpod"
	shadeformadapter "github.com/benngarcia/mercator/internal/adapter/shadeform"
	vastadapter "github.com/benngarcia/mercator/internal/adapter/vast"
	"github.com/benngarcia/mercator/internal/broker"
	"github.com/benngarcia/mercator/internal/capability"
)

// Factory returns the complete production provider catalog. A provider is
// registered here once so serving, conformance validation, and onboarding all
// share the same manifests and constructors.
func Factory() *broker.Factory {
	factory := broker.NewFactory()
	factory.Register(dockeradapter.Manifest(), newDocker)
	factory.Register(runpodadapter.Manifest(), func(config map[string]string, secret string) (capability.Backend, error) {
		return runpodadapter.New(secret, config)
	})
	factory.Register(shadeformadapter.Manifest(), func(config map[string]string, secret string) (capability.Backend, error) {
		return shadeformadapter.New(secret, config)
	})
	factory.Register(vastadapter.Manifest(), func(config map[string]string, secret string) (capability.Backend, error) {
		return vastadapter.New(secret, config)
	})
	return factory
}

// Manifest returns the canonical manifest for one production provider.
func Manifest(adapterType string) (adapter.Manifest, bool) {
	for _, manifest := range Factory().Manifests() {
		if manifest.Type == adapterType {
			return manifest, true
		}
	}
	return adapter.Manifest{}, false
}

func newDocker(config map[string]string, secret string) (capability.Backend, error) {
	registry, err := dockeradapter.NewRegistryCredential(config["registry_server"], config["registry_username"], secret)
	if err != nil {
		return nil, err
	}
	client := dockeradapter.NewCLIClient(config["bin"])
	client.Host = config["host"]
	client.Context = config["context"]
	client.Registry = registry
	identity := dockeradapter.DeriveIdentity(config["host"], config["context"])
	return dockeradapter.NewOffering(client, identity, config["arch"]), nil
}
