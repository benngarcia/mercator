// Package providers owns Mercator's shipped provider catalog.
package providers

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/benngarcia/mercator/internal/adapter"
	dockeradapter "github.com/benngarcia/mercator/internal/adapter/docker"
	runpodadapter "github.com/benngarcia/mercator/internal/adapter/runpod"
	shadeformadapter "github.com/benngarcia/mercator/internal/adapter/shadeform"
	vastadapter "github.com/benngarcia/mercator/internal/adapter/vast"
	"github.com/benngarcia/mercator/internal/broker"
)

type Definition struct {
	Manifest adapter.Manifest
	Build    broker.FactoryFunc
	Validate func(map[string]string) error
}

type Catalog struct {
	definitions map[string]Definition
}

func Default() Catalog {
	return newCatalog([]Definition{
		definition(dockeradapter.Manifest(), newDockerAdapter, validateDockerConfig),
		definition(runpodadapter.Manifest(), func(config map[string]string, secret string) (adapter.Provider, error) {
			return runpodadapter.New(secret, config)
		}, nil),
		definition(shadeformadapter.Manifest(), func(config map[string]string, secret string) (adapter.Provider, error) {
			return shadeformadapter.New(secret, config)
		}, nil),
		definition(vastadapter.Manifest(), func(config map[string]string, secret string) (adapter.Provider, error) {
			return vastadapter.New(secret, config)
		}, nil),
	})
}

func newCatalog(definitions []Definition) Catalog {
	byType := make(map[string]Definition, len(definitions))
	for _, provider := range definitions {
		if provider.Manifest.Type == "" {
			panic("providers: definition has empty adapter type")
		}
		byType[provider.Manifest.Type] = provider
	}
	return Catalog{definitions: byType}
}

func (catalog Catalog) Definition(adapterType string) (Definition, bool) {
	provider, found := catalog.definitions[adapterType]
	return provider, found
}

func (catalog Catalog) Factory() *broker.Factory {
	factory := broker.NewFactory()
	for _, provider := range catalog.definitions {
		factory.Register(provider.Manifest, provider.Build)
	}
	return factory
}

func definition(manifest adapter.Manifest, build broker.FactoryFunc, validate func(map[string]string) error) Definition {
	return Definition{
		Manifest: manifest,
		Build:    build,
		Validate: func(config map[string]string) error {
			if err := validateManifestConfig(manifest, config); err != nil {
				return err
			}
			if validate != nil {
				return validate(config)
			}
			return nil
		},
	}
}

func validateManifestConfig(manifest adapter.Manifest, config map[string]string) error {
	fields := make(map[string]adapter.ConfigField, len(manifest.ConfigFields))
	for _, field := range manifest.ConfigFields {
		fields[field.Name] = field
	}
	for key, raw := range config {
		field, found := fields[key]
		if !found {
			return fmt.Errorf("providers: %s config key %q is not public", manifest.Type, key)
		}
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		switch field.Type {
		case "bool":
			if value != "true" && value != "false" {
				return fmt.Errorf("providers: %s config %s must be true or false", manifest.Type, key)
			}
		case "int":
			value, err := strconv.ParseInt(value, 10, 64)
			if err != nil || value <= 0 {
				return fmt.Errorf("providers: %s config %s must be a positive integer", manifest.Type, key)
			}
		}
	}
	return nil
}

func validateDockerConfig(config map[string]string) error {
	if strings.TrimSpace(config["host"]) != "" && strings.TrimSpace(config["context"]) != "" {
		return fmt.Errorf("providers: docker config cannot set both host and context")
	}
	return nil
}

func newDockerAdapter(config map[string]string, secret string) (adapter.Provider, error) {
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
