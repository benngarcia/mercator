package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

type CLIClient struct {
	Binary string
	// Host and Context select the Docker endpoint. Empty means the ambient
	// default (the loopback socket / active context). Host maps to the global
	// `--host` flag (unix://, tcp://, ssh://); Context maps to `--context`.
	// Docker treats them as mutually exclusive, so Context wins when both are set.
	Host    string
	Context string
}

func NewCLIClient(binary string) *CLIClient {
	if binary == "" {
		binary = "docker"
	}
	return &CLIClient{Binary: binary}
}

// globalArgs returns the endpoint-selecting global flags that must precede every
// docker subcommand. Empty when the ambient default endpoint is used.
func (c *CLIClient) globalArgs() []string {
	if c.Context != "" {
		return []string{"--context", c.Context}
	}
	if c.Host != "" {
		return []string{"--host", c.Host}
	}
	return nil
}

func (c *CLIClient) CreateContainer(ctx context.Context, req CreateContainerRequest) (Container, error) {
	args := []string{"create", "--name", req.Name}
	if req.Platform != "" {
		args = append(args, "--platform", req.Platform)
	}
	if req.GPUCount > 0 {
		args = append(args, "--gpus", strconv.Itoa(req.GPUCount))
	}
	for _, key := range sortedKeys(req.Labels) {
		args = append(args, "--label", key+"="+req.Labels[key])
	}
	for _, key := range sortedKeys(req.Env) {
		args = append(args, "--env", key+"="+req.Env[key])
	}
	// Publish each requested container port to an ephemeral host port;
	// dropping these silently would strand a workload that asked for ingress.
	for _, port := range req.Ports {
		args = append(args, "--publish", strconv.Itoa(port))
	}
	if len(req.Entrypoint) > 0 {
		args = append(args, "--entrypoint", req.Entrypoint[0])
	}
	args = append(args, req.Image)
	if len(req.Entrypoint) > 1 {
		args = append(args, req.Entrypoint[1:]...)
	}
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

func (c *CLIClient) StartContainer(ctx context.Context, name string) error {
	output, err := c.run(ctx, "start", name)
	if err != nil {
		if strings.Contains(output, "No such container") {
			return ErrNotFound
		}
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(output))
	}
	return nil
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
			Status   string `json:"Status"`
			ExitCode int    `json:"ExitCode"`
		} `json:"State"`
	}
	if err := json.Unmarshal([]byte(output), &raw); err != nil {
		return Container{}, err
	}
	if len(raw) == 0 {
		return Container{}, ErrNotFound
	}
	created, _ := time.Parse(time.RFC3339Nano, raw[0].Created)
	exitCode := raw[0].State.ExitCode
	return Container{ID: raw[0].ID, Name: strings.TrimPrefix(raw[0].Name, "/"), Labels: raw[0].Config.Labels, State: raw[0].State.Status, ExitCode: &exitCode, CreatedAt: created}, nil
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
	cmd := exec.CommandContext(ctx, c.Binary, append(c.globalArgs(), args...)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), err
	}
	return string(output), nil
}

// runSplit keeps stdout and stderr separate for commands whose stdout must be
// parsed exactly. `docker run` writes pull progress to stderr; merging it into
// stdout (as run does) would corrupt the parse.
func (c *CLIClient) runSplit(ctx context.Context, args ...string) (stdout, stderr string, err error) {
	cmd := exec.CommandContext(ctx, c.Binary, append(c.globalArgs(), args...)...)
	var out, errOut strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err = cmd.Run()
	return out.String(), errOut.String(), err
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
