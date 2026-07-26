package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

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
			name: "requires a non-empty image (digests are no longer mandatory; tags resolve server-side)",
			edit: func(rev *WorkloadRevision) {
				rev.Spec.Containers[0].Image = ""
			},
			code: "IMAGE_REQUIRED",
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
			name: "rejects raw extension payloads",
			edit: func(rev *WorkloadRevision) {
				rev.Spec.Raw = mustRawMap(t, map[string]string{"docker": "--privileged"})
			},
			code: "UNSUPPORTED_RAW_EXTENSION",
			path: "spec.raw",
		},
		{
			name: "rejects empty env bindings",
			edit: func(rev *WorkloadRevision) {
				rev.Spec.Containers[0].Env["EMPTY_BINDING"] = EnvBinding{}
			},
			code: "ENV_VALUE_REQUIRED",
			path: "spec.containers[0].env.EMPTY_BINDING",
		},
		{
			name: "rejects invalid env names",
			edit: func(rev *WorkloadRevision) {
				rev.Spec.Containers[0].Env["invalid-name"] = EnvBinding{Value: ptr("not-public")}
			},
			code: "ENV_NAME_INVALID",
			path: "spec.containers[0].env.invalid-name",
		},
		{
			name: "rejects oversized env data",
			edit: func(rev *WorkloadRevision) {
				huge := strings.Repeat("x", 33*1024)
				rev.Spec.Containers[0].Env["HUGE_VALUE"] = EnvBinding{Value: &huge}
			},
			code: "ENV_VALUE_TOO_LARGE",
			path: "spec.containers[0].env.HUGE_VALUE",
		},
		{
			name: "rejects invalid container ports",
			edit: func(rev *WorkloadRevision) {
				rev.Spec.Containers[0].Ports = []PortSpec{{
					Name: "bad", ContainerPort: 0, Protocol: "tcp", Exposure: PortExposurePublic,
				}}
			},
			code: "PORT_INVALID",
			path: "spec.containers[0].ports[0].container_port",
		},
		{
			name: "rejects expected runtime above the enforced maximum",
			edit: func(rev *WorkloadRevision) {
				rev.Spec.Execution.MaxRuntimeSeconds = 3600
				rev.Spec.Placement.ExpectedRuntimeSeconds = 7200
			},
			code: "EXPECTED_RUNTIME_EXCEEDS_MAX",
			path: "spec.placement.expected_runtime_seconds",
		},
		{
			name: "rejects expected runtime above the default maximum when max is omitted",
			edit: func(rev *WorkloadRevision) {
				rev.Spec.Execution.MaxRuntimeSeconds = 0
				rev.Spec.Placement.ExpectedRuntimeSeconds = DefaultMaxRuntimeSeconds + 1
			},
			code: "EXPECTED_RUNTIME_EXCEEDS_MAX",
			path: "spec.placement.expected_runtime_seconds",
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
					"API_TOKEN": {Value: ptr("env-or-provider-managed-token")},
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
			Placement: PlacementPolicy{Class: ClassStandard, MaxP90StartSeconds: 180, ExpectedRuntimeSeconds: 900},
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

func mustRawMap(t *testing.T, value map[string]string) map[string]json.RawMessage {
	t.Helper()
	out := make(map[string]json.RawMessage, len(value))
	for key, raw := range value {
		data, err := json.Marshal(raw)
		if err != nil {
			t.Fatalf("marshal raw value: %v", err)
		}
		out[key] = data
	}
	return out
}

func TestDispositionForOfferKind(t *testing.T) {
	cases := []struct {
		kind OfferKind
		want Disposition
	}{
		{OfferKindProvisionable, DispositionTerminate},
		{OfferKindStanding, DispositionRelease},
	}
	for _, tc := range cases {
		got, err := DispositionForOfferKind(tc.kind)
		if err != nil {
			t.Fatalf("DispositionForOfferKind(%q): %v", tc.kind, err)
		}
		if got != tc.want {
			t.Fatalf("DispositionForOfferKind(%q) = %q, want %q", tc.kind, got, tc.want)
		}
	}
	for _, kind := range []OfferKind{"", "unknown"} {
		if _, err := DispositionForOfferKind(kind); err == nil {
			t.Fatalf("DispositionForOfferKind(%q) accepted an unknown offer kind", kind)
		}
	}
}

// TestARegistryLinkIsWorthWhatItsPublisherSaid is the rule that a number
// nothing stands behind cannot produce a confident duration. The existence of a
// throughput fact used to be read as a measurement, so a literal an adapter
// stamped onto every offer priced transfer durations at full certainty beside
// enrolled nodes honestly recording an assumption.
func TestARegistryLinkIsWorthWhatItsPublisherSaid(t *testing.T) {
	observed := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	fact := func(mbps, confidence float64, validUntil time.Time) NetworkFact {
		return NetworkFact{
			Scope:      NetworkScopeRegistry,
			Statistic:  "p10",
			ValueMbps:  mbps,
			ObservedAt: observed,
			ValidUntil: validUntil,
			Confidence: confidence,
		}
	}
	cases := map[string]struct {
		facts []NetworkFact
		want  LinkSpeed
	}{
		"nothing measured this link": {
			want: LinkSpeed{Mbps: DefaultRegistryDownloadMbps, Confidence: AssumedLinkConfidence, Assumption: AssumptionRegistryRate},
		},
		"a valid measurement is worth what it says it is": {
			facts: []NetworkFact{fact(250, 0.9, observed.Add(time.Hour))},
			want:  LinkSpeed{Mbps: 250, Confidence: 0.9},
		},
		"a measurement that expired before this offer was observed is not one": {
			facts: []NetworkFact{fact(250, 0.9, observed.Add(-time.Hour))},
			want:  LinkSpeed{Mbps: DefaultRegistryDownloadMbps, Confidence: AssumedLinkConfidence, Assumption: AssumptionRegistryRate},
		},
		"a fact about another link says nothing about this one": {
			facts: []NetworkFact{{Scope: NetworkScopePublicInternet, Statistic: "p10", ValueMbps: 900, Confidence: 1}},
			want:  LinkSpeed{Mbps: DefaultRegistryDownloadMbps, Confidence: AssumedLinkConfidence, Assumption: AssumptionRegistryRate},
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			offer := OfferSnapshot{ObservedAt: observed, Network: NetworkFacts{Download: testCase.facts}}

			if got := offer.DownloadRate(NetworkScopeRegistry); got != testCase.want {
				t.Fatalf("registry link = %+v, want %+v", got, testCase.want)
			}
		})
	}
}
