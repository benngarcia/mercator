package vast

import "github.com/benngarcia/mercator/internal/adapter"

// Manifest is the vast adapter's onboarding contract for GET /v1/adapters.
func Manifest() adapter.Manifest {
	return adapter.Manifest{
		Type:        "vast",
		DisplayName: "Vast.ai",
		Logo:        "vast",
		Description: "Certified-datacenter GPUs from the Vast.ai marketplace — secure tier only, community hosts are never offered.",
		Credential: adapter.CredentialSpec{
			Required: true,
			Label:    "API key",
			Format:   "Account API key from the Vast console.",
		},
		ConfigFields: []adapter.ConfigField{
			{
				Name:        "gpu_names",
				Label:       "GPU allowlist",
				Type:        "string",
				Placeholder: "RTX 4090,H100 SXM",
				Help:        "Comma-separated Vast GPU names (underscores also accepted). Empty offers any secure-tier GPU.",
			},
			{
				Name:    "container_disk_gb",
				Label:   "Container disk (GB)",
				Type:    "int",
				Default: "20",
				Help:    "Disk rented with each instance when the workload does not request its own size.",
			},
			{
				Name:    "offer_limit",
				Label:   "Offer limit",
				Type:    "int",
				Default: "20",
				Help:    "How many of the cheapest matching secure offers to consider per query.",
			},
		},
		SetupSteps: []adapter.SetupStep{
			{
				Text: "Create a Vast.ai account.",
				URL:  "https://cloud.vast.ai/",
			},
			{
				Text: "Open the account settings page in the Vast console and find the API key section.",
				URL:  "https://cloud.vast.ai/account/",
			},
			{
				Text: "Copy the account API key (create one if none exists).",
			},
			{
				Text: "Paste the key into the form and verify. Mercator only rents Vast's secure tier: verified, certified-datacenter machines, never community peer hosts.",
			},
		},
	}
}
