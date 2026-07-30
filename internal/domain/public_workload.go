package domain

import "encoding/json"

// This file is what may be published about a workload. A revision carries the
// environment a container runs with, and an environment value is where a caller
// puts a token: every door that writes a revision into a public event writes it
// through here, so there is one answer to what a public reader may see rather
// than one per door. The two doors disagreed about that answer for as long as
// both existed, and the one that disagreed published secrets.
//
// The types mirror the domain types on the wire. Changing a field here changes
// the public event schema.

// PublicWorkloadRevision is one revision as a public reader may see it.
type PublicWorkloadRevision struct {
	ID          string             `json:"id"`
	WorkspaceID string             `json:"workspace_id"`
	WorkloadID  string             `json:"workload_id"`
	Digest      string             `json:"digest"`
	Spec        publicWorkloadSpec `json:"spec"`
}

type publicWorkloadSpec struct {
	Containers []publicContainerSpec `json:"containers"`
	Resources  ResourceRequirements  `json:"resources"`
	Network    NetworkRequirements   `json:"network"`
	Placement  PlacementPolicy       `json:"placement"`
	Execution  ExecutionPolicy       `json:"execution"`
	// Artifacts names immutable content by version. Version identities carry no
	// secret material, and what a Run reads and publishes is exactly what a
	// reader of the public log needs to reconstruct the dependency graph.
	Artifacts ArtifactRequirements       `json:"artifacts"`
	Metadata  map[string]string          `json:"metadata,omitempty"`
	Raw       map[string]json.RawMessage `json:"raw,omitempty"`
}

type publicContainerSpec struct {
	Name       string                      `json:"name"`
	Image      string                      `json:"image"`
	Platform   Platform                    `json:"platform"`
	Entrypoint *[]string                   `json:"entrypoint,omitempty"`
	Args       []string                    `json:"args,omitempty"`
	Env        map[string]publicEnvBinding `json:"env,omitempty"`
	Ports      []PortSpec                  `json:"ports,omitempty"`
}

type publicEnvBinding struct {
	Kind string `json:"kind"`
}

// Public is this revision with every environment value replaced by its kind. A
// public reader learns which variables the container is given and never what any
// of them is set to.
func (rev WorkloadRevision) Public() PublicWorkloadRevision {
	out := PublicWorkloadRevision{
		ID:          rev.ID,
		WorkspaceID: rev.WorkspaceID,
		WorkloadID:  rev.WorkloadID,
		Digest:      rev.Digest,
		Spec: publicWorkloadSpec{
			Resources: rev.Spec.Resources,
			Network:   rev.Spec.Network,
			Placement: rev.Spec.Placement,
			Execution: rev.Spec.Execution,
			Artifacts: rev.Spec.Artifacts,
			Metadata:  rev.Spec.Metadata,
			Raw:       rev.Spec.Raw,
		},
	}
	out.Spec.Containers = make([]publicContainerSpec, 0, len(rev.Spec.Containers))
	for _, container := range rev.Spec.Containers {
		public := publicContainerSpec{
			Name:       container.Name,
			Image:      container.Image,
			Platform:   container.Platform,
			Entrypoint: container.Entrypoint,
			Args:       container.Args,
			Ports:      container.Ports,
		}
		if len(container.Env) > 0 {
			public.Env = make(map[string]publicEnvBinding, len(container.Env))
			for key, binding := range container.Env {
				public.Env[key] = publicEnvBinding{Kind: EnvKind(binding.Value)}
			}
		}
		out.Spec.Containers = append(out.Spec.Containers, public)
	}
	return out
}

// EnvKind is all a public reader learns about one environment value: whether the
// caller set it to something or left it empty.
func EnvKind(value *string) string {
	if value != nil {
		return "literal"
	}
	return "empty"
}
