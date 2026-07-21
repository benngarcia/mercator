// Package conformance launches bounded provider trials through Mercator's
// public lifecycle and proves terminal cleanup.
package conformance

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/providers"
)

var environmentName = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// Trial is one real, billable provider verification. CredentialEnv names the
// environment variable to resolve at execution time; credential material is
// absent so it cannot enter arguments, evidence, or persisted events.
type Trial struct {
	AdapterType        string
	CredentialEnv      string
	Config             map[string]string
	Image              string
	MaxExpectedCostUSD float64
	Timeout            time.Duration
}

type EnvLookup func(string) (string, bool)

// ValidateTrial rejects unsafe input before opening SQLite, contacting a
// provider, or creating billable infrastructure.
func ValidateTrial(trial Trial, lookup EnvLookup) error {
	if !digestReference(trial.Image) {
		return fmt.Errorf("conformance: image must be a digest-pinned OCI reference")
	}
	if trial.MaxExpectedCostUSD <= 0 {
		return fmt.Errorf("conformance: max_expected_cost_usd must be positive")
	}
	if trial.Timeout <= 0 {
		return fmt.Errorf("conformance: timeout must be positive")
	}
	manifest, ok := providers.Manifest(trial.AdapterType)
	if !ok {
		return fmt.Errorf("conformance: unsupported adapter %q", trial.AdapterType)
	}
	if err := validateCredential(manifest, trial.CredentialEnv, lookup); err != nil {
		return err
	}
	if err := validateConfig(manifest, trial.Config); err != nil {
		return err
	}
	if manifest.Type == "docker" &&
		strings.TrimSpace(trial.Config["host"]) != "" &&
		strings.TrimSpace(trial.Config["context"]) != "" {
		return fmt.Errorf("conformance: docker config cannot set both host and context")
	}
	return nil
}

func digestReference(image string) bool {
	_, digest, found := strings.Cut(strings.TrimSpace(image), "@sha256:")
	if !found || len(digest) != 64 {
		return false
	}
	for _, character := range digest {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func validateCredential(manifest adapter.Manifest, credentialEnv string, lookup EnvLookup) error {
	credentialEnv = strings.TrimSpace(credentialEnv)
	if !manifest.Credential.Required {
		if credentialEnv != "" {
			return fmt.Errorf("conformance: %s does not accept a credential environment variable", manifest.Type)
		}
		return nil
	}
	if credentialEnv == "" {
		return fmt.Errorf("conformance: %s requires credential_env", manifest.Type)
	}
	if strings.HasPrefix(strings.ToUpper(credentialEnv), "MERCATOR_") {
		return fmt.Errorf("conformance: credential_env %q is reserved for broker configuration", credentialEnv)
	}
	if !environmentName.MatchString(credentialEnv) {
		return fmt.Errorf("conformance: credential_env must be an uppercase environment variable name, got %q", credentialEnv)
	}
	if lookup == nil {
		return fmt.Errorf("conformance: credential environment lookup is required for %s", manifest.Type)
	}
	value, found := lookup(credentialEnv)
	if !found || strings.TrimSpace(value) == "" {
		return fmt.Errorf("conformance: credential environment variable %q is empty", credentialEnv)
	}
	return nil
}

func validateConfig(manifest adapter.Manifest, config map[string]string) error {
	fields := make(map[string]adapter.ConfigField, len(manifest.ConfigFields))
	for _, field := range manifest.ConfigFields {
		fields[field.Name] = field
	}
	keys := make([]string, 0, len(config))
	for key := range config {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		field, ok := fields[key]
		if !ok {
			return fmt.Errorf("conformance: %s config key %q is not public", manifest.Type, key)
		}
		value := strings.TrimSpace(config[key])
		if value == "" {
			continue
		}
		switch field.Type {
		case "bool":
			if value != "true" && value != "false" {
				return fmt.Errorf("conformance: %s config %s must be true or false", manifest.Type, key)
			}
		case "int":
			number, err := strconv.ParseInt(value, 10, 64)
			if err != nil || number <= 0 {
				return fmt.Errorf("conformance: %s config %s must be a positive integer", manifest.Type, key)
			}
		}
	}
	return nil
}
