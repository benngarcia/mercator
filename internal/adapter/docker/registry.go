package docker

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type RegistryCredential struct {
	Server   string
	Username string
	Password string
}

func NewRegistryCredential(server, username, password string) (*RegistryCredential, error) {
	configured := server != "" || username != "" || password != ""
	complete := server != "" && username != "" && password != ""
	if configured && !complete {
		return nil, errors.New("docker: registry_server, registry_username, and connection credential must be configured together")
	}
	if !configured {
		return nil, nil
	}
	return &RegistryCredential{Server: server, Username: username, Password: password}, nil
}

func (c *CLIClient) registryConfigArgs() ([]string, func(), error) {
	if c.Registry == nil {
		return nil, func() {}, nil
	}
	dir, err := c.Registry.writeDockerConfig()
	if err != nil {
		return nil, func() {}, err
	}
	return []string{"--config", dir}, func() { _ = os.RemoveAll(dir) }, nil
}

func (c RegistryCredential) writeDockerConfig() (string, error) {
	dir, err := os.MkdirTemp("", "mercator-docker-config-")
	if err != nil {
		return "", fmt.Errorf("docker: create registry config: %w", err)
	}
	contents, err := c.dockerConfigJSON()
	if err == nil {
		err = os.WriteFile(filepath.Join(dir, "config.json"), contents, 0o600)
	}
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("docker: write registry config: %w", err)
	}
	return dir, nil
}

func (c RegistryCredential) dockerConfigJSON() ([]byte, error) {
	server := c.Server
	if server == "docker.io" || server == "index.docker.io" {
		server = "https://index.docker.io/v1/"
	}
	auth := base64.StdEncoding.EncodeToString([]byte(c.Username + ":" + c.Password))
	document := struct {
		Auths map[string]struct {
			Auth string `json:"auth"`
		} `json:"auths"`
	}{Auths: map[string]struct {
		Auth string `json:"auth"`
	}{server: {Auth: auth}}}
	return json.Marshal(document)
}

func (c RegistryCredential) commandEnvironment() []string {
	environment := os.Environ()
	filtered := environment[:0]
	for _, variable := range environment {
		_, value, _ := strings.Cut(variable, "=")
		if value != c.Password {
			filtered = append(filtered, variable)
		}
	}
	return filtered
}
