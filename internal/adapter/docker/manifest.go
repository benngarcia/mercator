package docker

import "github.com/benngarcia/mercator/internal/adapter"

// Manifest is the docker adapter's onboarding contract for GET /v1/adapters.
func Manifest() adapter.Manifest {
	return adapter.Manifest{
		Type:        "docker",
		DisplayName: "Docker",
		Logo:        "docker",
		Description: "Run workloads on a Docker engine you already have — the local daemon or a remote host over SSH.",
		Credential: adapter.CredentialSpec{
			Required: false,
			Format:   "None — the broker reaches the engine through its socket, context, or an ssh:// host.",
		},
		ConfigFields: []adapter.ConfigField{
			{
				Name:        "host",
				Label:       "Docker host",
				Type:        "string",
				Placeholder: "ssh://ben@gpu-box",
				Help:        "Empty uses the local daemon. ssh://user@host reaches a remote engine; tcp://host:2376 a TLS endpoint.",
			},
			{
				Name:        "context",
				Label:       "Docker context",
				Type:        "string",
				Placeholder: "remote-gpu",
				Help:        "A named context from `docker context ls`. Ignored when Docker host is set.",
			},
			{
				Name:        "bin",
				Label:       "Docker binary",
				Type:        "string",
				Placeholder: "/usr/local/bin/docker",
				Help:        "Path to the docker CLI on the broker host. Empty resolves from PATH.",
			},
			{
				Name:        "arch",
				Label:       "Architecture override",
				Type:        "string",
				Placeholder: "arm64",
				Help:        "Optional OCI architecture for this endpoint when Docker deliberately runs emulated workloads. Empty uses Docker's reported architecture.",
			},
		},
		SetupSteps: []adapter.SetupStep{
			{
				Text: "Install Docker Engine on the machine that will run workloads.",
				URL:  "https://docs.docker.com/engine/install/",
			},
			{
				Text: "To use the broker machine's own daemon, leave every field empty and verify.",
			},
			{
				Text: "To use a remote engine, set Docker host to ssh://user@host. The broker's user needs SSH access to that machine and permission to run docker on it.",
				URL:  "https://docs.docker.com/engine/security/protect-access/",
			},
		},
	}
}
