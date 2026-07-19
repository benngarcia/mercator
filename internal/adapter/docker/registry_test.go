package docker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestNewRegistryCredentialRequiresCompleteConfiguration(t *testing.T) {
	for _, values := range [][3]string{
		{"docker.io", "", "token"},
		{"docker.io", "bucketrobotics", ""},
		{"", "bucketrobotics", "token"},
	} {
		if _, err := NewRegistryCredential(values[0], values[1], values[2]); err == nil {
			t.Fatalf("NewRegistryCredential(%q, %q, secret) accepted partial configuration", values[0], values[1])
		}
	}
	credential, err := NewRegistryCredential("", "", "")
	if err != nil || credential != nil {
		t.Fatalf("credential-free connection = (%+v, %v), want (nil, nil)", credential, err)
	}
}

func TestRegistryConfigArgsContainCredentialOnlyInTemporaryConfig(t *testing.T) {
	const secret = "pull-only-token"
	client := &CLIClient{
		Host: "ssh://operator@gpu-host",
		Registry: &RegistryCredential{
			Server:   "docker.io",
			Username: "bucketrobotics",
			Password: secret,
		},
	}

	configArgs, cleanup, err := client.registryConfigArgs()
	if err != nil {
		t.Fatalf("registryConfigArgs: %v", err)
	}
	if len(configArgs) != 2 || configArgs[0] != "--config" {
		t.Fatalf("config args = %v, want [--config DIR]", configArgs)
	}
	dir := configArgs[1]
	t.Cleanup(cleanup)
	if strings.Contains(strings.Join(append(configArgs, client.globalArgs()...), " "), secret) {
		t.Fatal("registry secret leaked into Docker argv")
	}

	contents, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatalf("read temporary config: %v", err)
	}
	var config struct {
		Auths map[string]struct {
			Auth string `json:"auth"`
		} `json:"auths"`
	}
	if err := json.Unmarshal(contents, &config); err != nil {
		t.Fatalf("decode temporary config: %v", err)
	}
	wantAuth := base64.StdEncoding.EncodeToString([]byte("bucketrobotics:" + secret))
	if got := config.Auths["https://index.docker.io/v1/"].Auth; got != wantAuth {
		t.Fatalf("Docker Hub auth = %q, want encoded username and token", got)
	}
	info, err := os.Stat(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatalf("stat temporary config: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("temporary config mode = %o, want 600", info.Mode().Perm())
	}

	cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("temporary config remains after cleanup: %v", err)
	}
}

func TestCredentialFreeDockerCommandHasNoConfigOverride(t *testing.T) {
	client := &CLIClient{Host: "ssh://operator@gpu-host"}
	configArgs, cleanup, err := client.registryConfigArgs()
	if err != nil {
		t.Fatalf("registryConfigArgs: %v", err)
	}
	defer cleanup()
	if len(configArgs) != 0 {
		t.Fatalf("credential-free config args = %v, want none", configArgs)
	}
	if !slices.Equal(client.globalArgs(), []string{"--host", "ssh://operator@gpu-host"}) {
		t.Fatalf("endpoint args changed: %v", client.globalArgs())
	}
}

func TestAuthenticatedCommandRemovesOperationConfig(t *testing.T) {
	tempRoot := t.TempDir()
	t.Setenv("TMPDIR", tempRoot)
	client := &CLIClient{
		Binary: "true",
		Registry: &RegistryCredential{
			Server:   "registry.example.com",
			Username: "reader",
			Password: "pull-token",
		},
	}

	if _, err := client.runAuthenticated(context.Background(), "create", "private.example/image@sha256:d34db33f"); err != nil {
		t.Fatalf("runAuthenticated: %v", err)
	}
	entries, err := os.ReadDir(tempRoot)
	if err != nil {
		t.Fatalf("read temp root: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("operation config remains after Docker command: %v", entries)
	}
}

func TestRegistryAuthenticationFailedRecognizesDockerErrors(t *testing.T) {
	for _, output := range []string{
		"pull access denied for private/image, repository does not exist or may require 'docker login'",
		"unauthorized: incorrect username or password",
		"denied: requested access to the resource is denied: authentication required",
	} {
		if !registryAuthenticationFailed(output) {
			t.Fatalf("registryAuthenticationFailed(%q) = false", output)
		}
	}
	if registryAuthenticationFailed("manifest unknown: manifest unknown") {
		t.Fatal("missing image classified as registry authentication failure")
	}
}

func TestRegistryCredentialIsRemovedFromDockerCommandEnvironment(t *testing.T) {
	const secret = "pull-token-that-must-not-reach-docker-env"
	t.Setenv("MERCATOR_TEST_REGISTRY_TOKEN", secret)
	t.Setenv("MERCATOR_TEST_NON_SECRET", "preserved")
	credential := RegistryCredential{Password: secret}

	environment := credential.commandEnvironment()
	if slices.Contains(environment, "MERCATOR_TEST_REGISTRY_TOKEN="+secret) {
		t.Fatal("registry credential leaked into Docker command environment")
	}
	if !slices.Contains(environment, "MERCATOR_TEST_NON_SECRET=preserved") {
		t.Fatal("non-secret environment was not preserved")
	}
}
