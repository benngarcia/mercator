package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// HostInfo is the subset of `docker info` we use to build an honest offer for a
// Docker endpoint, whether that endpoint is the loopback socket or a remote
// host reached over tcp:// or ssh://.
type HostInfo struct {
	Architecture  string
	OSType        string
	NCPU          int
	MemTotalBytes int64
	ServerVersion string
	Name          string
}

// OCIArch returns the host's architecture normalized to the OCI platform
// vocabulary Mercator's domain and image refs use (e.g. aarch64 -> arm64).
func (h HostInfo) OCIArch() string {
	return normalizeArch(h.Architecture)
}

// normalizeArch maps a Docker-reported machine architecture (uname-style, as
// `docker info` reports it) to the OCI platform architecture Mercator's domain
// and image refs use. Unknown values pass through unchanged so we never silently
// mislabel an exotic host; empty stays empty so the caller can apply a default.
func normalizeArch(arch string) string {
	switch arch {
	case "aarch64", "arm64":
		return "arm64"
	case "x86_64", "amd64":
		return "amd64"
	default:
		return arch
	}
}

// parseDockerInfo decodes the JSON emitted by `docker info --format '{{json .}}'`.
func parseDockerInfo(raw []byte) (HostInfo, error) {
	var doc struct {
		Architecture  string `json:"Architecture"`
		OSType        string `json:"OSType"`
		NCPU          int    `json:"NCPU"`
		MemTotal      int64  `json:"MemTotal"`
		ServerVersion string `json:"ServerVersion"`
		Name          string `json:"Name"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return HostInfo{}, fmt.Errorf("parse docker info: %w", err)
	}
	return HostInfo{
		Architecture:  doc.Architecture,
		OSType:        doc.OSType,
		NCPU:          doc.NCPU,
		MemTotalBytes: doc.MemTotal,
		ServerVersion: doc.ServerVersion,
		Name:          doc.Name,
	}, nil
}

// Info probes the configured Docker endpoint for its host facts. It honors the
// client's endpoint (Host/Context) just like every other command.
func (c *CLIClient) Info(ctx context.Context) (HostInfo, error) {
	output, err := c.run(ctx, "info", "--format", "{{json .}}")
	if err != nil {
		return HostInfo{}, fmt.Errorf("docker info: %w: %s", err, strings.TrimSpace(output))
	}
	return parseDockerInfo([]byte(output))
}
