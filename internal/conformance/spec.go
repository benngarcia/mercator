// Package conformance verifies Mercator provider connections through the same
// public lifecycle used by operators.
package conformance

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/providers"
)

var environmentName = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

type Mode string

const (
	ProbeMode        Mode = "probe"
	LaunchCancelMode Mode = "launch-cancel"
)

// TrialSpec describes one provider verification. CredentialEnv names an
// environment variable; credential material is intentionally absent from this
// contract so it cannot enter arguments, reports, or persisted evidence.
type TrialSpec struct {
	AdapterType        string            `json:"adapter_type"`
	CredentialEnv      string            `json:"credential_env,omitempty"`
	Config             map[string]string `json:"config,omitempty"`
	Image              string            `json:"image,omitempty"`
	Mode               Mode              `json:"mode,omitempty"`
	ListenAddress      string            `json:"listen_address,omitempty"`
	PublicURL          string            `json:"public_url,omitempty"`
	MaxExpectedCostUSD float64           `json:"max_expected_cost_usd,omitempty"`
	Timeout            time.Duration     `json:"timeout,omitempty"`
}

type EnvLookup func(string) (string, bool)

// ValidateSpec rejects a trial before it opens SQLite, starts a server,
// contacts a provider, or creates billable infrastructure. The lookup reads
// only the named credential environment variable; its value is never retained.
func ValidateSpec(spec TrialSpec, lookup EnvLookup) error {
	if !digestReference(spec.Image) {
		return fmt.Errorf("conformance: image must be a digest-pinned OCI reference")
	}
	if spec.Mode != ProbeMode && spec.Mode != LaunchCancelMode {
		return fmt.Errorf("conformance: unsupported mode %q", spec.Mode)
	}
	if spec.MaxExpectedCostUSD <= 0 {
		return fmt.Errorf("conformance: max_expected_cost_usd must be positive")
	}
	if spec.Timeout <= 0 {
		return fmt.Errorf("conformance: timeout must be positive")
	}
	definition, found := providers.Default().Definition(spec.AdapterType)
	if !found {
		return fmt.Errorf("conformance: unsupported adapter %q", spec.AdapterType)
	}
	if err := validateCredential(definition.Manifest, spec.CredentialEnv, lookup); err != nil {
		return err
	}
	if err := definition.Validate(spec.Config); err != nil {
		return fmt.Errorf("conformance: %w", err)
	}
	return validateTopology(spec)
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
		return fmt.Errorf("conformance: credential_env must be an uppercase environment variable name")
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

func validateTopology(spec TrialSpec) error {
	listenAddress := strings.TrimSpace(spec.ListenAddress)
	publicURL := strings.TrimSpace(spec.PublicURL)
	localDocker := spec.AdapterType == "docker" && strings.TrimSpace(spec.Config["host"]) == "" && strings.TrimSpace(spec.Config["context"]) == ""
	if localDocker && listenAddress == "" && publicURL == "" {
		return nil
	}
	if listenAddress == "" {
		return fmt.Errorf("conformance: %s requires listen_address", spec.AdapterType)
	}
	_, port, err := net.SplitHostPort(listenAddress)
	if err != nil {
		return fmt.Errorf("conformance: listen_address must be host:port: %w", err)
	}
	if !localDocker && port == "0" {
		return fmt.Errorf("conformance: %s listen_address must use a fixed port", spec.AdapterType)
	}
	if publicURL == "" {
		if spec.AdapterType == "docker" {
			return nil
		}
		return fmt.Errorf("conformance: %s requires public_url", spec.AdapterType)
	}
	parsed, err := url.Parse(publicURL)
	if err != nil || parsed.Host == "" {
		return fmt.Errorf("conformance: public_url must be an absolute URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("conformance: public_url must use http or https")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return fmt.Errorf("conformance: public_url must be an origin without a path, query, or fragment")
	}
	return nil
}
