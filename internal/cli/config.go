package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// FileConfig is the on-disk CLI configuration: named contexts (a server plus
// how to talk to it) and which one is current. Environment variables always
// win over the file so CI keeps working unchanged.
type FileConfig struct {
	CurrentContext string                    `json:"current_context,omitempty"`
	Contexts       map[string]*ContextConfig `json:"contexts,omitempty"`
}

// ContextConfig names one Mercator deployment: where it is, the default
// workspace, and a credential — either a static API token or a login-minted
// CLI token tied to a user identity.
type ContextConfig struct {
	APIURL      string `json:"api_url,omitempty"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	APIToken    string `json:"api_token,omitempty"`

	CLIToken          string `json:"cli_token,omitempty"`
	CLITokenEmail     string `json:"cli_token_email,omitempty"`
	CLITokenExpiresAt string `json:"cli_token_expires_at,omitempty"`
}

// cliTokenValid reports whether the context holds a login credential that has
// not expired yet.
func (c *ContextConfig) cliTokenValid(now time.Time) bool {
	if c.CLIToken == "" {
		return false
	}
	expiry, err := time.Parse(time.RFC3339, c.CLITokenExpiresAt)
	if err != nil {
		return false
	}
	return now.Before(expiry)
}

// DefaultConfigPath resolves where the CLI config lives: MERCATOR_CONFIG,
// else $XDG_CONFIG_HOME/mercator/config.json, else ~/.config/mercator/config.json.
func DefaultConfigPath(env map[string]string) string {
	if path := env["MERCATOR_CONFIG"]; path != "" {
		return path
	}
	base := env["XDG_CONFIG_HOME"]
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "mercator", "config.json")
}

// loadFileConfig reads the config file; a missing file is an empty config.
func loadFileConfig(path string) (FileConfig, error) {
	cfg := FileConfig{Contexts: map[string]*ContextConfig{}}
	if path == "" {
		return cfg, nil
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("read config %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse config %s: %w", path, err)
	}
	if cfg.Contexts == nil {
		cfg.Contexts = map[string]*ContextConfig{}
	}
	return cfg, nil
}

// saveFileConfig writes the config with owner-only permissions (it holds
// credentials).
func saveFileConfig(path string, cfg FileConfig) error {
	if path == "" {
		return fmt.Errorf("no config path resolved; set MERCATOR_CONFIG or HOME")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

// LocalContextName is the context `mercator serve` writes for a loopback
// broker whose token it generated itself.
const LocalContextName = "local"

// WriteLocalContext records how to reach a loopback broker the operator never
// configured a credential for, so the CLI on the same machine needs no exports
// to talk to the server they just started. It reports whether the file changed.
//
// It only claims current_context when nothing else has: silently redirecting
// commands away from an operator's chosen deployment would be far worse than
// asking them to run `mercator context use local`.
func WriteLocalContext(configPath, apiURL, token string) (bool, error) {
	cfg, err := loadFileConfig(configPath)
	if err != nil {
		return false, err
	}
	if existing := cfg.Contexts[LocalContextName]; existing != nil &&
		existing.APIURL == apiURL && existing.APIToken == token && cfg.CurrentContext != "" {
		return false, nil
	}
	cfg.Contexts[LocalContextName] = &ContextConfig{APIURL: apiURL, APIToken: token}
	if cfg.CurrentContext == "" {
		cfg.CurrentContext = LocalContextName
	}
	return true, saveFileConfig(configPath, cfg)
}

// currentContext returns the active context, or nil when none is selected.
func (f FileConfig) currentContext() (string, *ContextConfig) {
	if f.CurrentContext == "" {
		return "", nil
	}
	return f.CurrentContext, f.Contexts[f.CurrentContext]
}

// contextNames returns the configured context names, sorted for stable output.
func (f FileConfig) contextNames() []string {
	names := make([]string, 0, len(f.Contexts))
	for name := range f.Contexts {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// resolveCredentials merges the environment-derived Config values (which win,
// for CI) with the current context from the config file. It returns the
// effective connection settings for API commands.
func resolveCredentials(cfg Config, now time.Time) (baseURL, token, workspaceID string, warnings []string) {
	baseURL, token, workspaceID = cfg.BaseURL, cfg.Token, cfg.WorkspaceID
	file, err := loadFileConfig(cfg.ConfigPath)
	if err != nil {
		// A corrupt config must not brick unrelated env-configured usage; it
		// is surfaced as a warning and, for context commands, a hard error.
		warnings = append(warnings, err.Error())
		return baseURL, token, workspaceID, warnings
	}
	name, current := file.currentContext()
	if current == nil {
		return baseURL, token, workspaceID, warnings
	}
	if baseURL == "" {
		baseURL = current.APIURL
	}
	if workspaceID == "" {
		workspaceID = current.WorkspaceID
	}
	if token == "" {
		switch {
		case current.cliTokenValid(now):
			token = current.CLIToken
		case current.CLIToken != "":
			warnings = append(warnings, fmt.Sprintf("context %q: login expired; run `mercator login`", name))
			if current.APIToken != "" {
				token = current.APIToken
			}
		case current.APIToken != "":
			token = current.APIToken
		}
	}
	return baseURL, token, workspaceID, warnings
}
