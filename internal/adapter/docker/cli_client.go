package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"
)

type CLIClient struct {
	Binary string
}

func NewCLIClient(binary string) *CLIClient {
	if binary == "" {
		binary = "docker"
	}
	return &CLIClient{Binary: binary}
}

func (c *CLIClient) CreateContainer(ctx context.Context, req CreateContainerRequest) (Container, error) {
	args := []string{"create", "--name", req.Name}
	if req.Platform != "" {
		args = append(args, "--platform", req.Platform)
	}
	for _, key := range sortedKeys(req.Labels) {
		args = append(args, "--label", key+"="+req.Labels[key])
	}
	for _, key := range sortedKeys(req.Env) {
		args = append(args, "--env", key+"="+req.Env[key])
	}
	if len(req.Entrypoint) > 0 {
		args = append(args, "--entrypoint", strings.Join(req.Entrypoint, " "))
	}
	args = append(args, req.Image)
	args = append(args, req.Args...)
	output, err := c.run(ctx, args...)
	if err != nil {
		if strings.Contains(output, "already in use") || strings.Contains(output, "Conflict.") {
			return Container{}, ErrAlreadyExists
		}
		return Container{}, fmt.Errorf("%w: %s", err, strings.TrimSpace(output))
	}
	id := strings.TrimSpace(output)
	container, err := c.InspectContainer(ctx, req.Name)
	if err != nil {
		return Container{ID: id, Name: req.Name, Labels: req.Labels, State: "created", CreatedAt: time.Now().UTC()}, nil
	}
	return container, nil
}

func (c *CLIClient) InspectContainer(ctx context.Context, name string) (Container, error) {
	output, err := c.run(ctx, "inspect", name)
	if err != nil {
		if strings.Contains(output, "No such object") {
			return Container{}, ErrNotFound
		}
		return Container{}, fmt.Errorf("%w: %s", err, strings.TrimSpace(output))
	}
	var raw []struct {
		ID      string `json:"Id"`
		Name    string `json:"Name"`
		Created string `json:"Created"`
		Config  struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
		State struct {
			Status string `json:"Status"`
		} `json:"State"`
	}
	if err := json.Unmarshal([]byte(output), &raw); err != nil {
		return Container{}, err
	}
	if len(raw) == 0 {
		return Container{}, ErrNotFound
	}
	created, _ := time.Parse(time.RFC3339Nano, raw[0].Created)
	return Container{ID: raw[0].ID, Name: strings.TrimPrefix(raw[0].Name, "/"), Labels: raw[0].Config.Labels, State: raw[0].State.Status, CreatedAt: created}, nil
}

func (c *CLIClient) RemoveContainer(ctx context.Context, name string) error {
	output, err := c.run(ctx, "rm", "-f", name)
	if err != nil {
		if strings.Contains(output, "No such container") {
			return ErrNotFound
		}
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(output))
	}
	return nil
}

func (c *CLIClient) ListContainers(ctx context.Context, labels map[string]string) ([]Container, error) {
	args := []string{"ps", "-a", "--format", "{{.Names}}"}
	for key, value := range labels {
		args = append(args, "--filter", "label="+key+"="+value)
	}
	output, err := c.run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(output))
	}
	names := strings.Fields(output)
	containers := make([]Container, 0, len(names))
	for _, name := range names {
		container, err := c.InspectContainer(ctx, name)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return nil, err
		}
		containers = append(containers, container)
	}
	return containers, nil
}

func (c *CLIClient) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, c.Binary, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), err
	}
	return string(output), nil
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
