package domain

// Defaults applied by NormalizeWorkloadRevision when a caller omits a field.
// These let a minimal create body (just an image) expand into a fully-specified,
// validatable revision. Defaulting only ever fills omissions; it never overrides
// an explicit value, so genuinely ambiguous or explicit-bad input still fails
// ValidateWorkloadRevision.
const (
	DefaultContainerName       = "main"
	DefaultPlatformOS          = "linux"
	DefaultPlatformArch        = "amd64"
	DefaultCPUMillis           = 250
	DefaultMemoryBytes         = 256 * 1024 * 1024      // 256Mi
	DefaultEphemeralDiskBytes  = 1024 * 1024 * 1024     // 1Gi
	DefaultMaxRuntimeSeconds   = 3600                   // bounded 1h
	DefaultMaxPreStartAttempts = 1
)

// NormalizeWorkloadRevision fills omitted, defaultable fields on a workload
// revision so that a minimal create body expands into a fully-specified spec.
// It MUST run BEFORE ValidateWorkloadRevision. Explicit values are preserved
// verbatim; only zero/empty fields are populated. The original is not mutated;
// a normalized copy is returned.
func NormalizeWorkloadRevision(rev WorkloadRevision) WorkloadRevision {
	out := rev

	// Container name + platform defaulting. We only touch the single-container
	// case; multi-container input is left untouched so validation can reject it.
	containers := make([]ContainerSpec, len(rev.Spec.Containers))
	copy(containers, rev.Spec.Containers)
	if len(containers) == 1 {
		c := containers[0]
		if c.Name == "" {
			c.Name = DefaultContainerName
		}
		if c.Platform.OS == "" {
			c.Platform.OS = DefaultPlatformOS
		}
		if c.Platform.Architecture == "" {
			c.Platform.Architecture = DefaultPlatformArch
		}
		containers[0] = c
	}
	out.Spec.Containers = containers

	// Resources: a small sane default when nothing was requested.
	res := rev.Spec.Resources
	if res.CPU.MinMillis == 0 {
		res.CPU.MinMillis = DefaultCPUMillis
	}
	if res.Memory.MinBytes == 0 {
		res.Memory.MinBytes = DefaultMemoryBytes
	}
	if res.EphemeralDisk.MinBytes == 0 {
		res.EphemeralDisk.MinBytes = DefaultEphemeralDiskBytes
	}
	out.Spec.Resources = res

	// Network inbound defaults to "none".
	if out.Spec.Network.Inbound == "" {
		out.Spec.Network.Inbound = InboundNetworkNone
	}

	// Placement objective defaults to "balanced".
	if out.Spec.Placement.Objective == "" {
		out.Spec.Placement.Objective = ObjectiveBalanced
	}

	// Execution: bounded max runtime and a single pre-start attempt.
	if out.Spec.Execution.MaxRuntimeSeconds == 0 {
		out.Spec.Execution.MaxRuntimeSeconds = DefaultMaxRuntimeSeconds
	}
	if out.Spec.Execution.MaxPreStartAttempts == 0 {
		out.Spec.Execution.MaxPreStartAttempts = DefaultMaxPreStartAttempts
	}

	return out
}
