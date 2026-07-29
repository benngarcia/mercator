package shadeform

import "github.com/benngarcia/mercator/internal/adapter"

// Manifest is the shadeform adapter's onboarding contract for GET /v1/adapters.
func Manifest() adapter.Manifest {
	return adapter.Manifest{
		Type:        "shadeform",
		DisplayName: "Shadeform",
		Logo:        "shadeform",
		Description: "Rent GPU VMs across ~21 provider clouds through the Shadeform marketplace, each bootstrapped with a Mercator node agent.",
		Credential: adapter.CredentialSpec{
			Required: true,
			Label:    "API key",
			Format:   "Sent as X-API-KEY. Every Shadeform key is admin-scoped — it can launch and delete instances account-wide.",
		},
		// Every key the adapter actually reads is declared here. An undeclared key
		// is one the conformance validator rejects and production accepts, so a
		// trial and a connection configured identically disagree about whether the
		// configuration is legal at all.
		ConfigFields: []adapter.ConfigField{
			{
				Name:        "agent_download_url",
				Label:       "Node agent download URL",
				Type:        "string",
				Required:    true,
				Placeholder: "https://downloads.example.com/mercator-node/{version}/linux-amd64",
				Help:        "Where a rented machine fetches the Mercator node agent. Must be https and must contain {version}, which is replaced with the build the bootstrap pinned. Without it the connection can list capacity but cannot rent any: a machine with no agent is capacity nothing can execute on.",
			},
			{
				Name:    "shade_cloud",
				Label:   "Shade Cloud",
				Type:    "bool",
				Default: "true",
				Help:    "On: instances launch in Shadeform's managed accounts and bill through Shadeform. Off: launches go to cloud accounts you have linked yourself (bring-your-own-cloud).",
			},
			{
				Name:        "allowed_clouds",
				Label:       "Cloud allowlist",
				Type:        "string",
				Placeholder: "lambdalabs,datacrunch",
				Help:        "Comma-separated provider cloud slugs. Empty allows every cloud Shadeform fronts; set it to pin workloads to providers you trust.",
			},
			{
				Name:    "max_lifetime_hours",
				Label:   "Max instance lifetime (hours)",
				Type:    "int",
				Default: "24",
				Help:    "Provider-side auto-delete backstop: bounds spend if the broker dies mid-lease. Normal teardown happens well before this.",
			},
			{
				Name:        "os",
				Label:       "OS image",
				Type:        "string",
				Placeholder: "ubuntu22.04_cuda12.2_shade_os",
				Help:        "Explicit Shadeform OS image. Left empty, the adapter picks the instance type's first shade_os image, which bakes in the GPU driver and container runtime the node agent needs; a type offering none is refused rather than rented blind.",
			},
			{
				Name:        "base_url",
				Label:       "API base URL",
				Type:        "string",
				Placeholder: "https://api.shadeform.ai/v1",
				Help:        "Shadeform API origin. Leave empty for the public API; set it to reach Shadeform through an egress proxy of your own.",
			},
		},
		SetupSteps: []adapter.SetupStep{
			{
				Text: "Create a Shadeform account.",
				URL:  "https://platform.shadeform.ai",
			},
			{
				Text: "Open Settings → API in the Shadeform platform.",
				URL:  "https://platform.shadeform.ai/settings/api",
			},
			{
				Text: "Generate an API key and copy it. Treat it like a root credential: all Shadeform keys are admin-scoped.",
			},
			{
				Text: "Paste the key into the form and verify.",
			},
			{
				Text: "Publish the mercator-node binary where a rented machine can fetch it over https, and set the download URL with {version} in place of the build. Every machine this connection rents installs the agent from there and enrolls itself.",
			},
		},
	}
}
