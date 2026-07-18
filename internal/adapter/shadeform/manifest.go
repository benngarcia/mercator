package shadeform

import "github.com/benngarcia/mercator/internal/adapter"

// Manifest is the shadeform adapter's onboarding contract for GET /v1/adapters.
func Manifest() adapter.Manifest {
	return adapter.Manifest{
		Type:        "shadeform",
		DisplayName: "Shadeform",
		Logo:        "shadeform",
		Description: "One API for GPU VMs across ~21 provider clouds via the Shadeform marketplace.",
		Credential: adapter.CredentialSpec{
			Required: true,
			Label:    "API key",
			Format:   "Sent as X-API-KEY. Every Shadeform key is admin-scoped — it can launch and delete instances account-wide.",
		},
		ConfigFields: []adapter.ConfigField{
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
				Help:    "Provider-side auto-delete backstop: bounds spend if the broker dies mid-run. Normal cleanup happens well before this.",
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
		},
	}
}
