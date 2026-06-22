package domain

import "testing"

// A near-empty single-container spec (just an image) must normalize into a
// fully-specified, validatable revision.
func TestNormalizeFillsOmittedDefaults(t *testing.T) {
	rev := WorkloadRevision{
		Spec: WorkloadSpec{
			Containers: []ContainerSpec{{Image: "busybox"}},
		},
	}
	out := NormalizeWorkloadRevision(rev)

	c := out.Spec.Containers[0]
	if c.Name != DefaultContainerName {
		t.Fatalf("container name: got %q want %q", c.Name, DefaultContainerName)
	}
	if c.Platform.OS != DefaultPlatformOS || c.Platform.Architecture != DefaultPlatformArch {
		t.Fatalf("platform: got %q want linux/amd64", c.Platform.String())
	}
	if out.Spec.Resources.CPU.MinMillis != DefaultCPUMillis {
		t.Fatalf("cpu: got %d want %d", out.Spec.Resources.CPU.MinMillis, DefaultCPUMillis)
	}
	if out.Spec.Resources.Memory.MinBytes != DefaultMemoryBytes {
		t.Fatalf("memory: got %d want %d", out.Spec.Resources.Memory.MinBytes, DefaultMemoryBytes)
	}
	if out.Spec.Resources.EphemeralDisk.MinBytes != DefaultEphemeralDiskBytes {
		t.Fatalf("disk: got %d want %d", out.Spec.Resources.EphemeralDisk.MinBytes, DefaultEphemeralDiskBytes)
	}
	if out.Spec.Network.Inbound != InboundNetworkNone {
		t.Fatalf("network inbound: got %q want none", out.Spec.Network.Inbound)
	}
	if out.Spec.Placement.Objective != ObjectiveBalanced {
		t.Fatalf("objective: got %q want balanced", out.Spec.Placement.Objective)
	}
	if out.Spec.Execution.MaxRuntimeSeconds != DefaultMaxRuntimeSeconds {
		t.Fatalf("max runtime: got %d want %d", out.Spec.Execution.MaxRuntimeSeconds, DefaultMaxRuntimeSeconds)
	}
	if out.Spec.Execution.MaxPreStartAttempts != DefaultMaxPreStartAttempts {
		t.Fatalf("max pre-start attempts: got %d want %d", out.Spec.Execution.MaxPreStartAttempts, DefaultMaxPreStartAttempts)
	}

	// The normalized revision must pass validation with only an image supplied.
	if v := ValidateWorkloadRevision(out); len(v) > 0 {
		t.Fatalf("normalized minimal revision failed validation: %+v", v)
	}
}

// Defaulting never overrides explicit values.
func TestNormalizeNeverOverridesExplicitValues(t *testing.T) {
	rev := WorkloadRevision{
		Spec: WorkloadSpec{
			Containers: []ContainerSpec{{
				Name:     "main",
				Image:    "busybox",
				Platform: Platform{OS: "linux", Architecture: "arm64"},
			}},
			Resources: ResourceRequirements{
				CPU:           CPURequirement{MinMillis: 4000},
				Memory:        MemoryRequirement{MinBytes: 8 << 30},
				EphemeralDisk: DiskRequirement{MinBytes: 32 << 30},
			},
			Network:   NetworkRequirements{Inbound: InboundNetworkPublicPort},
			Placement: PlacementPolicy{Objective: ObjectiveCheapest},
			Execution: ExecutionPolicy{MaxRuntimeSeconds: 99, MaxPreStartAttempts: 5},
		},
	}
	out := NormalizeWorkloadRevision(rev)
	if out.Spec.Containers[0].Platform.Architecture != "arm64" {
		t.Fatalf("explicit arch overridden: %q", out.Spec.Containers[0].Platform.Architecture)
	}
	if out.Spec.Resources.CPU.MinMillis != 4000 {
		t.Fatalf("explicit cpu overridden: %d", out.Spec.Resources.CPU.MinMillis)
	}
	if out.Spec.Network.Inbound != InboundNetworkPublicPort {
		t.Fatalf("explicit inbound overridden: %q", out.Spec.Network.Inbound)
	}
	if out.Spec.Placement.Objective != ObjectiveCheapest {
		t.Fatalf("explicit objective overridden: %q", out.Spec.Placement.Objective)
	}
	if out.Spec.Execution.MaxRuntimeSeconds != 99 || out.Spec.Execution.MaxPreStartAttempts != 5 {
		t.Fatalf("explicit execution overridden: %+v", out.Spec.Execution)
	}
}

// Validation still rejects genuinely ambiguous / explicit-bad input even after
// normalization (>1 container, explicit non-main name, explicit bad platform).
func TestValidationRejectsExplicitBadInputAfterNormalize(t *testing.T) {
	cases := []struct {
		name string
		rev  WorkloadRevision
		code string
	}{
		{
			name: "more than one container",
			rev: WorkloadRevision{Spec: WorkloadSpec{Containers: []ContainerSpec{
				{Image: "busybox"}, {Image: "redis"},
			}}},
			code: "V1_ONE_CONTAINER",
		},
		{
			name: "explicit non-main container name",
			rev: WorkloadRevision{Spec: WorkloadSpec{Containers: []ContainerSpec{
				{Name: "worker", Image: "busybox"},
			}}},
			code: "V1_MAIN_CONTAINER",
		},
		{
			name: "explicit unsupported arch",
			rev: WorkloadRevision{Spec: WorkloadSpec{Containers: []ContainerSpec{
				{Image: "busybox", Platform: Platform{OS: "linux", Architecture: "riscv64"}},
			}}},
			code: "UNSUPPORTED_PLATFORM",
		},
		{
			name: "explicit unsupported os",
			rev: WorkloadRevision{Spec: WorkloadSpec{Containers: []ContainerSpec{
				{Image: "busybox", Platform: Platform{OS: "windows", Architecture: "amd64"}},
			}}},
			code: "UNSUPPORTED_PLATFORM",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := NormalizeWorkloadRevision(tc.rev)
			violations := ValidateWorkloadRevision(out)
			found := false
			for _, v := range violations {
				if v.Code == tc.code {
					found = true
				}
			}
			if !found {
				t.Fatalf("expected violation %s, got %+v", tc.code, violations)
			}
		})
	}
}
