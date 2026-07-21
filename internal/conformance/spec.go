// Package conformance launches bounded provider trials through Mercator's
// public lifecycle and proves terminal cleanup.
package conformance

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/providers"
)

var environmentName = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

type Mode string

const (
	ModeProbe        Mode = "probe"
	ModeLaunchCancel Mode = "launch-cancel"
)

// Trial is one real, billable provider verification. CredentialEnv names the
// environment variable to resolve at execution time; credential material is
// absent so it cannot enter arguments, evidence, or persisted events.
type Trial struct {
	AdapterType        string
	CredentialEnv      string
	Config             map[string]string
	Image              string
	Mode               Mode
	MaxExpectedCostUSD float64
	Timeout            time.Duration
}

type EnvLookup func(string) (string, bool)

// ValidateTrial rejects unsafe input before opening SQLite, contacting a
// provider, or creating billable infrastructure.
func ValidateTrial(trial Trial, lookup EnvLookup) error {
	trial = normalizeTrial(trial)
	if !digestReference(trial.Image) {
		return fmt.Errorf("conformance: image must be a digest-pinned OCI reference")
	}
	if trial.MaxExpectedCostUSD <= 0 {
		return fmt.Errorf("conformance: max_expected_cost_usd must be positive")
	}
	if trial.Timeout <= 0 {
		return fmt.Errorf("conformance: timeout must be positive")
	}
	if trial.Mode != ModeProbe && trial.Mode != ModeLaunchCancel {
		return fmt.Errorf("conformance: unsupported mode %q", trial.Mode)
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

func normalizeTrial(trial Trial) Trial {
	if trial.Mode == "" {
		trial.Mode = ModeProbe
	}
	return trial
}

func validateTopology(trial Trial, config RunnerConfig) error {
	localDocker := trial.AdapterType == "docker" && strings.TrimSpace(trial.Config["host"]) == "" && strings.TrimSpace(trial.Config["context"]) == ""
	listenAddress := strings.TrimSpace(config.ListenAddress)
	publicURL := strings.TrimSpace(config.PublicURL)
	if listenAddress != "" {
		_, port, err := net.SplitHostPort(listenAddress)
		if err != nil {
			return fmt.Errorf("conformance: MERCATOR_CONFORMANCE_LISTEN_ADDR must be host:port: %w", err)
		}
		if !localDocker && port == "0" {
			return fmt.Errorf("conformance: MERCATOR_CONFORMANCE_LISTEN_ADDR must use a fixed port for %s", trial.AdapterType)
		}
	} else if !localDocker {
		return fmt.Errorf("conformance: MERCATOR_CONFORMANCE_LISTEN_ADDR is required for %s", trial.AdapterType)
	}
	if publicURL == "" {
		if localDocker {
			return nil
		}
		return fmt.Errorf("conformance: MERCATOR_CONFORMANCE_PUBLIC_URL is required for %s", trial.AdapterType)
	}
	parsed, err := url.Parse(publicURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("conformance: MERCATOR_CONFORMANCE_PUBLIC_URL must be an absolute HTTP or HTTPS origin")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return fmt.Errorf("conformance: MERCATOR_CONFORMANCE_PUBLIC_URL must be an origin without a path, query, or fragment")
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
