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

func supportedLinuxArch(arch string) bool {
	return arch == "amd64" || arch == "arm64"
}
