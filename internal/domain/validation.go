package domain

import (
	"fmt"
	"regexp"
	"strings"
)

var digestImagePattern = regexp.MustCompile(`^.+@sha256:[a-fA-F0-9]{64}$`)

func ValidateWorkloadRevision(rev WorkloadRevision) []Violation {
	var violations []Violation
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
	if !digestImagePattern.MatchString(container.Image) {
		violations = append(violations, Violation{
			Code: "IMAGE_DIGEST_REQUIRED", Path: "spec.containers[0].image", Required: "image@sha256:<digest>", Offered: container.Image,
			Message: "Workload revisions must reference digest-pinned images before placement.",
		})
	}
	if container.Platform.OS != "linux" || !supportedLinuxArch(container.Platform.Architecture) {
		violations = append(violations, Violation{
			Code: "UNSUPPORTED_PLATFORM", Path: "spec.containers[0].platform", Required: "linux/amd64 or linux/arm64", Offered: container.Platform.String(),
			Message: "V1 supports Linux containers on amd64 and arm64 platforms.",
		})
	}
	for key, binding := range container.Env {
		if binding.Value != nil && binding.SecretRef != nil {
			violations = append(violations, Violation{
				Code: "ENV_BINDING_AMBIGUOUS", Path: "spec.containers[0].env." + key,
				Message: "An environment key may have either a literal value or a secret reference, not both.",
			})
		}
		if binding.SecretRef != nil && (binding.SecretRef.Name == "" || binding.SecretRef.Version <= 0) {
			violations = append(violations, Violation{
				Code: "SECRET_VERSION_REQUIRED", Path: "spec.containers[0].env." + key,
				Message: "Secret environment bindings must reference an exact positive secret version.",
			})
		}
	}
	for i, port := range container.Ports {
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
