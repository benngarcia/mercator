package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/gpunorm"
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
	// Runtimes are the daemon's registered OCI runtime names (sorted). A GPU
	// host provisioned with nvidia-container-toolkit registers "nvidia".
	Runtimes []string
}

// HasNvidiaRuntime reports whether the endpoint's daemon registered the NVIDIA
// container runtime — the precondition for `--gpus` device requests, and the
// gate that keeps CPU-only endpoints from ever paying for (or logging failures
// from) the one-shot GPU probe container.
func (h HostInfo) HasNvidiaRuntime() bool {
	return slices.Contains(h.Runtimes, "nvidia")
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
		Architecture  string                     `json:"Architecture"`
		OSType        string                     `json:"OSType"`
		NCPU          int                        `json:"NCPU"`
		MemTotal      int64                      `json:"MemTotal"`
		ServerVersion string                     `json:"ServerVersion"`
		Name          string                     `json:"Name"`
		Runtimes      map[string]json.RawMessage `json:"Runtimes"`
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
		Runtimes:      slices.Sorted(maps.Keys(doc.Runtimes)),
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

// diskProbeImage runs the one-shot disk probe container. busybox is tiny
// (~2 MiB, multi-arch), ships df, and is pulled once per host — subsequent
// probes reuse the daemon's cached image.
const diskProbeImage = "busybox:1.37"

// DiskFreeBytes measures the ephemeral disk actually available to workload
// containers on the endpoint by running a one-shot probe container and reading
// POSIX `df` of its root filesystem. A container's `/` sits on the daemon's
// storage-driver filesystem (the one that holds every writable layer), so its
// Available figure is exactly the disk a workload container can consume.
// `docker info` reports no free-disk fact for modern storage drivers, and the
// daemon host's paths are not visible to this process (Mercator itself usually
// runs in a container with only the Docker socket mounted, or against a remote
// ssh://tcp:// endpoint), so a probe container is the only honest measurement
// that works uniformly across endpoint types.
func (c *CLIClient) DiskFreeBytes(ctx context.Context) (int64, error) {
	stdout, stderr, err := c.runSplit(ctx,
		"run", "--rm", "--network=none", "--label", "mercator.probe=disk_free",
		diskProbeImage, "df", "-Pk", "/")
	if err != nil {
		return 0, fmt.Errorf("docker disk probe: %w: %s", err, strings.TrimSpace(stderr))
	}
	return parseDFAvailableBytes(stdout)
}

// AcceleratorInventory measures the endpoint's GPUs by running `nvidia-smi`
// in a one-shot probe container launched with `--gpus all`. The NVIDIA
// container runtime injects nvidia-smi and the driver libraries into any
// container it starts, so the same tiny busybox image as the disk probe
// suffices — no CUDA image pull. Like the disk probe, this works uniformly
// across endpoint types (loopback socket, remote ssh:// or tcp:// over the
// tailnet) because the measurement happens on the daemon's side. Callers
// should gate on HostInfo.HasNvidiaRuntime(); on a host without the NVIDIA
// runtime the launch itself fails and the error surfaces here.
func (c *CLIClient) AcceleratorInventory(ctx context.Context) ([]domain.AcceleratorInventory, error) {
	stdout, stderr, err := c.runSplit(ctx,
		"run", "--rm", "--network=none", "--gpus", "all", "--label", "mercator.probe=gpu_inventory",
		diskProbeImage, "nvidia-smi", "--query-gpu=name,memory.total", "--format=csv,noheader,nounits")
	if err != nil {
		return nil, fmt.Errorf("docker gpu probe: %w: %s", err, strings.TrimSpace(stderr))
	}
	return parseNvidiaSMIInventory(stdout)
}

// parseNvidiaSMIInventory groups the CSV lines of
// `nvidia-smi --query-gpu=name,memory.total --format=csv,noheader,nounits`
// (one line per physical GPU, memory in MiB) into accelerator inventory:
// identical (name, memory) GPUs collapse into one entry with a count. The
// canonical model id comes from the same gpunorm mapping the runpod adapter
// uses, so a workload's ModelAnyOf matches the GPU regardless of provider.
func parseNvidiaSMIInventory(output string) ([]domain.AcceleratorInventory, error) {
	var inventory []domain.AcceleratorInventory
	index := map[string]int{}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name, memory, ok := strings.Cut(line, ",")
		if !ok {
			return nil, fmt.Errorf("unexpected nvidia-smi line: %q", line)
		}
		name = strings.TrimSpace(name)
		memoryMiB, err := strconv.ParseInt(strings.TrimSpace(memory), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse nvidia-smi memory.total in line %q: %w", line, err)
		}
		key := name + "|" + strconv.FormatInt(memoryMiB, 10)
		if i, seen := index[key]; seen {
			inventory[i].Count++
			continue
		}
		index[key] = len(inventory)
		inventory = append(inventory, domain.AcceleratorInventory{
			Vendor:         "NVIDIA",
			Model:          name,
			CanonicalModel: gpunorm.Canonical("NVIDIA", name),
			Count:          1,
			MemoryBytes:    memoryMiB * 1024 * 1024,
		})
	}
	if len(inventory) == 0 {
		return nil, fmt.Errorf("no GPUs in nvidia-smi output: %q", strings.TrimSpace(output))
	}
	return inventory, nil
}

// parseDFAvailableBytes extracts the Available column of the root mount from
// POSIX `df -Pk` output (KiB units) and returns it in bytes.
func parseDFAvailableBytes(output string) (int64, error) {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 || fields[len(fields)-1] != "/" {
			continue
		}
		kib, err := strconv.ParseInt(fields[3], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse df available column %q: %w", fields[3], err)
		}
		return kib * 1024, nil
	}
	return 0, fmt.Errorf("no root filesystem line in df output: %q", strings.TrimSpace(output))
}
