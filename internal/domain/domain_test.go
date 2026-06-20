package domain

import "testing"

func TestValidateWorkloadRevisionEnforcesV1OCIContract(t *testing.T) {
	tests := []struct {
		name string
		edit func(*WorkloadRevision)
		code string
		path string
	}{
		{
			name: "requires exactly one container",
			edit: func(rev *WorkloadRevision) {
				rev.Spec.Containers = append(rev.Spec.Containers, rev.Spec.Containers[0])
			},
			code: "V1_ONE_CONTAINER",
			path: "spec.containers",
		},
		{
			name: "requires main container name",
			edit: func(rev *WorkloadRevision) {
				rev.Spec.Containers[0].Name = "worker"
			},
			code: "V1_MAIN_CONTAINER",
			path: "spec.containers[0].name",
		},
		{
			name: "requires digest pinned image",
			edit: func(rev *WorkloadRevision) {
				rev.Spec.Containers[0].Image = "ghcr.io/acme/inference:latest"
			},
			code: "IMAGE_DIGEST_REQUIRED",
			path: "spec.containers[0].image",
		},
		{
			name: "requires linux platform",
			edit: func(rev *WorkloadRevision) {
				rev.Spec.Containers[0].Platform = Platform{OS: "windows", Architecture: "amd64"}
			},
			code: "UNSUPPORTED_PLATFORM",
			path: "spec.containers[0].platform",
		},
		{
			name: "rejects duplicate env keys across literal and secret bindings",
			edit: func(rev *WorkloadRevision) {
				rev.Spec.Containers[0].Env["LOG_LEVEL"] = EnvBinding{
					Value:     ptr("info"),
					SecretRef: &SecretReference{Name: "log-level", Version: 1},
				}
			},
			code: "ENV_BINDING_AMBIGUOUS",
			path: "spec.containers[0].env.LOG_LEVEL",
		},
		{
			name: "public ports require public inbound network",
			edit: func(rev *WorkloadRevision) {
				rev.Spec.Containers[0].Ports = []PortSpec{{
					Name: "http", ContainerPort: 8080, Protocol: "tcp", Exposure: PortExposurePublic,
				}}
				rev.Spec.Network.Inbound = InboundNetworkNone
			},
			code: "CAPABILITY_MISMATCH",
			path: "spec.network.inbound",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rev := validRevision()
			tt.edit(&rev)
			violations := ValidateWorkloadRevision(rev)
			if !hasViolation(violations, tt.code, tt.path) {
				t.Fatalf("expected violation code=%s path=%s, got %+v", tt.code, tt.path, violations)
			}
		})
	}
}

func TestValidateWorkloadRevisionAcceptsValidDigestPinnedLinuxWorkload(t *testing.T) {
	violations := ValidateWorkloadRevision(validRevision())
	if len(violations) != 0 {
		t.Fatalf("expected no violations, got %+v", violations)
	}
}

func TestCanonicalHashIsStableAndOrderIndependent(t *testing.T) {
	a := map[string]any{"b": 2, "a": []any{"x", "y"}}
	b := map[string]any{"a": []any{"x", "y"}, "b": 2}

	hashA, err := CanonicalHash(a)
	if err != nil {
		t.Fatalf("hash a: %v", err)
	}
	hashB, err := CanonicalHash(b)
	if err != nil {
		t.Fatalf("hash b: %v", err)
	}
	if hashA != hashB {
		t.Fatalf("expected stable hash, got %q and %q", hashA, hashB)
	}
	if len(hashA) != len("sha256:")+64 {
		t.Fatalf("unexpected hash format: %q", hashA)
	}
}

func validRevision() WorkloadRevision {
	return WorkloadRevision{
		ID:          "wrev_1",
		WorkspaceID: "ws_1",
		WorkloadID:  "wrk_1",
		Digest:      "sha256:revision",
		Spec: WorkloadSpec{
			Containers: []ContainerSpec{{
				Name:     "main",
				Image:    "ghcr.io/acme/inference@sha256:0000000000000000000000000000000000000000000000000000000000000000",
				Platform: Platform{OS: "linux", Architecture: "amd64"},
				Args:     []string{"--batch-size", "128"},
				Env: map[string]EnvBinding{
					"LOG_LEVEL": {Value: ptr("info")},
					"API_TOKEN": {SecretRef: &SecretReference{Name: "inference-api-token", Version: 7}},
				},
			}},
			Resources: ResourceRequirements{
				CPU:    CPURequirement{MinMillis: 4000},
				Memory: MemoryRequirement{MinBytes: 17179869184},
			},
			Network: NetworkRequirements{
				Inbound: InboundNetworkPublicPort,
				Download: &NetworkDownloadRequirement{
					Scope:                    NetworkScopeRegistry,
					MinP10Mbps:               500,
					MaxMeasurementAgeSeconds: 86400,
					AllowUnknown:             false,
				},
			},
			Placement: PlacementPolicy{Objective: ObjectiveBalanced, MaxP90StartSeconds: 180, ExpectedRuntimeSeconds: 900},
			Execution: ExecutionPolicy{MaxRuntimeSeconds: 1800, MaxPreStartAttempts: 3},
		},
	}
}

func hasViolation(violations []Violation, code, path string) bool {
	for _, violation := range violations {
		if violation.Code == code && violation.Path == path {
			return true
		}
	}
	return false
}

func ptr(value string) *string {
	return &value
}
