package runpod

import "github.com/benngarcia/mercator/internal/adapter"

// Manifest is the runpod adapter's onboarding contract for GET /v1/adapters.
func Manifest() adapter.Manifest {
	return adapter.Manifest{
		Type:        "runpod",
		DisplayName: "RunPod",
		Logo:        "runpod",
		Description: "Rent GPU pods from RunPod's secure datacenter cloud.",
		Credential: adapter.CredentialSpec{
			Required: true,
			Label:    "API key",
			Format:   "API key from Settings → API Keys. Needs read and write permission.",
		},
		ConfigFields: []adapter.ConfigField{
			{
				Name:        "gpu_types",
				Label:       "GPU allowlist",
				Type:        "string",
				Default:     "NVIDIA RTX A2000,NVIDIA RTX A4000",
				Placeholder: "NVIDIA RTX A4000,NVIDIA A100 80GB PCIe",
				Help:        "Comma-separated RunPod GPU type names offered to the scheduler.",
			},
			{
				Name:    "allow_community_cloud",
				Label:   "Allow community cloud",
				Type:    "bool",
				Default: "false",
				Help:    "Community pods run on third-party hosts, not RunPod datacenters: cheaper, but your image and data execute on hardware RunPod does not control. Off means secure cloud only.",
			},
			{
				Name:    "container_disk_gb",
				Label:   "Container disk (GB)",
				Type:    "int",
				Default: "20",
				Help:    "Disk attached to each pod when the workload does not request its own size.",
			},
		},
		SetupSteps: []adapter.SetupStep{
			{
				Text: "Create a RunPod account.",
				URL:  "https://www.runpod.io/console/signup",
			},
			{
				Text: "Open Settings → API Keys in the RunPod console.",
				URL:  "https://www.runpod.io/console/user/settings",
			},
			{
				Text: "Create an API key with read and write permission and copy it — RunPod shows it only once.",
			},
			{
				Text: "Paste the key into the form and verify.",
			},
		},
	}
}
