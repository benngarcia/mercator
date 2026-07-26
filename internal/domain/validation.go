package domain

import (
	"fmt"
	"regexp"
	"strings"
)

var envNamePattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

const maxEnvValueBytes = 32 * 1024

func ValidateWorkloadRevision(rev WorkloadRevision) []Violation {
	var violations []Violation
	if len(rev.Spec.Raw) > 0 {
		violations = append(violations, Violation{
			Code: "UNSUPPORTED_RAW_EXTENSION", Path: "spec.raw",
			Message: "V1 rejects raw extension payloads; all workload fields must be validated explicitly.",
		})
	}
	if len(rev.Spec.Containers) != 1 {
		violations = append(violations, Violation{
			Code: "V1_ONE_CONTAINER", Path: "spec.containers", Required: 1, Offered: len(rev.Spec.Containers),
			Message: "V1 requires exactly one container.",
		})
		return violations
	}
	container := rev.Spec.Containers[0]
	if container.Name != "main" {
		violations = append(violations, Violation{
			Code: "V1_MAIN_CONTAINER", Path: "spec.containers[0].name", Required: "main", Offered: container.Name,
			Message: "V1 requires the only container to be named main.",
		})
	}
	if container.Image == "" {
		violations = append(violations, Violation{
			Code: "IMAGE_REQUIRED", Path: "spec.containers[0].image", Required: "image reference",
			Message: "Workload revisions must reference a container image.",
		})
	}
	if container.Platform.OS != "linux" || !supportedLinuxArch(container.Platform.Architecture) {
		violations = append(violations, Violation{
			Code: "UNSUPPORTED_PLATFORM", Path: "spec.containers[0].platform", Required: "linux/amd64 or linux/arm64", Offered: container.Platform.String(),
			Message: "V1 supports Linux containers on amd64 and arm64 platforms.",
		})
	}
	for key, binding := range container.Env {
		if !envNamePattern.MatchString(key) {
			violations = append(violations, Violation{
				Code: "ENV_NAME_INVALID", Path: "spec.containers[0].env." + key,
				Required: "^[A-Z_][A-Z0-9_]*$", Message: "Environment variable names must be portable uppercase identifiers.",
			})
		}
		if binding.Value == nil {
			violations = append(violations, Violation{
				Code: "ENV_VALUE_REQUIRED", Path: "spec.containers[0].env." + key,
				Message: "Environment bindings must provide a literal value.",
			})
		}
		if binding.Value != nil && len([]byte(*binding.Value)) > maxEnvValueBytes {
			violations = append(violations, Violation{
				Code: "ENV_VALUE_TOO_LARGE", Path: "spec.containers[0].env." + key,
				Required: maxEnvValueBytes, Offered: len([]byte(*binding.Value)),
				Message: "Literal environment values exceed the V1 size limit.",
			})
		}
	}
	// Compare against the effective bound so validation stays correct on
	// revisions that have not passed NormalizeWorkloadRevision yet.
	maxRuntime := rev.Spec.Execution.MaxRuntimeSeconds
	if maxRuntime == 0 {
		maxRuntime = DefaultMaxRuntimeSeconds
	}
	if rev.Spec.Placement.ExpectedRuntimeSeconds > float64(maxRuntime) {
		violations = append(violations, Violation{
			Code: "EXPECTED_RUNTIME_EXCEEDS_MAX", Path: "spec.placement.expected_runtime_seconds",
			Required: maxRuntime, Offered: rev.Spec.Placement.ExpectedRuntimeSeconds,
			Message: "Expected runtime cannot exceed the enforced maximum runtime.",
		})
	}
	// The class is what waiting is priced at, so a class Mercator cannot price is
	// refused where the Run enters. Ranking such a Run on cost alone would place
	// interactive work on the slowest machine in the fleet and record a reason
	// naming a class nothing declared.
	//
	// Unlike the bound above, this one is deliberately not read as an effective
	// value: an omitted class is NormalizeWorkloadRevision's to fill, and filling
	// it a second time here would let a revision be stored with no class at all
	// while validation said it had one. Every caller normalises first, so what
	// reaches this check is a class somebody stated.
	if !rev.Spec.Placement.Class.Known() {
		violations = append(violations, Violation{
			Code: "SERVICE_CLASS_UNKNOWN", Path: "spec.placement.service_class",
			Required: KnownServiceClasses, Offered: rev.Spec.Placement.Class,
			Message: "A Run states the class of work it is, and Mercator prices only the classes it knows.",
		})
	}
	violations = append(violations, validateArtifactRequirements(rev.Spec.Artifacts)...)
	violations = append(violations, validateCacheRequirements(rev.Spec.Caches)...)
	for i, port := range container.Ports {
		if port.ContainerPort <= 0 || port.ContainerPort > 65535 {
			violations = append(violations, Violation{
				Code: "PORT_INVALID", Path: fmt.Sprintf("spec.containers[0].ports[%d].container_port", i),
				Required: "1-65535", Offered: port.ContainerPort, Message: "Container ports must be in the TCP/UDP port range.",
			})
		}
		if port.Protocol != "" && strings.ToLower(port.Protocol) != "tcp" {
			violations = append(violations, Violation{
				Code: "UNSUPPORTED_PORT_PROTOCOL", Path: fmt.Sprintf("spec.containers[0].ports[%d].protocol", i),
				Required: "tcp", Offered: port.Protocol, Message: "V1 only supports TCP ports.",
			})
		}
		if port.Exposure == PortExposurePublic && rev.Spec.Network.Inbound != InboundNetworkPublicPort {
			violations = append(violations, Violation{
				Code: "CAPABILITY_MISMATCH", Path: "spec.network.inbound", Required: InboundNetworkPublicPort, Offered: rev.Spec.Network.Inbound,
				Message: "Public port exposure requires public inbound network capability.",
			})
		}
	}
	return violations
}

// validateCacheRequirements refuses a cache declaration that cannot name one
// cache. The name is the whole identity, so it is checked where it enters rather
// than escaped wherever a volume gets built from it, and one name twice is two
// mounts of one cache into one container.
func validateCacheRequirements(required []CacheMountRequirement) []Violation {
	var violations []Violation
	named := map[string]bool{}
	for index, requirement := range required {
		path := fmt.Sprintf("spec.caches[%d].name", index)
		switch {
		case !ValidCacheName(requirement.Name):
			violations = append(violations, Violation{
				Code: "CACHE_NAME_INVALID", Path: path, Required: cacheNamePattern.String(), Offered: requirement.Name,
				Message: "A Cache Mount name is its identity and names a volume on the host, so it must be a lowercase label.",
			})
		case named[requirement.Name]:
			violations = append(violations, Violation{
				Code: "CACHE_NAME_REPEATED", Path: path, Offered: requirement.Name,
				Message: "A Cache Mount name identifies one cache, so it can be declared once.",
			})
		}
		named[requirement.Name] = true
		if !ValidCacheCompatibilityKey(requirement.CompatibilityKey) {
			violations = append(violations, Violation{
				Code:     "CACHE_KEY_INVALID",
				Path:     fmt.Sprintf("spec.caches[%d].compatibility_key", index),
				Required: cacheKeyPattern.String(), Offered: requirement.CompatibilityKey,
				Message: "A compatibility key is recorded beside the storage it names, so it must be a printable label.",
			})
		}
		if requirement.SizeBytes < 0 {
			violations = append(violations, Violation{
				Code: "CACHE_SIZE_INVALID", Path: fmt.Sprintf("spec.caches[%d].size_bytes", index), Required: ">= 0",
				Offered: requirement.SizeBytes, Message: "A Cache Mount cannot expect negative room.",
			})
		}
	}
	return violations
}

// validateArtifactRequirements refuses the two declarations that cannot mean
// anything. A version named twice on one side says nothing the first mention
// did not, and a version a workload both reads and publishes claims to be its
// own input: an Artifact version is immutable, so one Run cannot depend on
// content it is about to create.
func validateArtifactRequirements(requirements ArtifactRequirements) []Violation {
	var violations []Violation
	consumed := map[string]bool{}
	for index, id := range requirements.Consumes {
		violations = append(violations, artifactDeclarationViolations(id, index, "consumes", consumed)...)
		consumed[id] = true
	}
	produced := map[string]bool{}
	for index, id := range requirements.Produces {
		violations = append(violations, artifactDeclarationViolations(id, index, "produces", produced)...)
		produced[id] = true
		if consumed[id] {
			violations = append(violations, Violation{
				Code: "ARTIFACT_CONSUMED_AND_PRODUCED", Path: fmt.Sprintf("spec.artifacts.produces[%d]", index), Offered: id,
				Message: "An Artifact version is immutable, so one workload cannot both read and publish it.",
			})
		}
	}
	return violations
}

func artifactDeclarationViolations(id string, index int, field string, seen map[string]bool) []Violation {
	path := fmt.Sprintf("spec.artifacts.%s[%d]", field, index)
	if id == "" {
		return []Violation{{
			Code: "ARTIFACT_ID_REQUIRED", Path: path, Required: "Artifact version identity",
			Message: "Artifact declarations must name a version.",
		}}
	}
	if seen[id] {
		return []Violation{{
			Code: "ARTIFACT_DECLARED_TWICE", Path: path, Offered: id,
			Message: "An Artifact version may appear once in a declaration.",
		}}
	}
	return nil
}

func supportedLinuxArch(arch string) bool {
	return arch == "amd64" || arch == "arm64"
}
