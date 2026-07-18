package adapter

// Manifest is an adapter's self-description for onboarding surfaces (the
// console's Connections cards, docs). It lives next to the adapter's code so a
// new provider ships its own onboarding, and is served verbatim by
// GET /v1/adapters. It carries no per-connection state and never any secret
// material.
type Manifest struct {
	// Type is the adapter type string used at connection create time
	// ("docker", "runpod", …) and the registration key in the broker factory.
	Type        string `json:"type"`
	DisplayName string `json:"display_name"`
	// Logo is a well-known slug the console maps to a bundled logomark asset
	// (the console cannot fetch external images under its CSP). Consumers fall
	// back to a typographic monogram when they have no asset for the slug.
	Logo        string `json:"logo"`
	Description string `json:"description"`

	Credential   CredentialSpec `json:"credential"`
	ConfigFields []ConfigField  `json:"config_fields"`
	// SetupSteps is the ordered how-do-I-get-a-credential walkthrough. Step
	// text is user-facing UI copy: short, imperative, one action per step.
	SetupSteps []SetupStep `json:"setup_steps"`
}

// CredentialSpec describes what the adapter expects as its secret, not where
// it is stored (env vs sealed store is the connection's choice).
type CredentialSpec struct {
	// Required is false only for adapters that authenticate out-of-band
	// (docker reaches the daemon via socket/ssh, not a token).
	Required bool   `json:"required"`
	Label    string `json:"label,omitempty"`
	// Format is a one-line hint about the expected token shape or scope,
	// e.g. "API key (all Shadeform keys are admin-scoped)".
	Format string `json:"format,omitempty"`
}

type ConfigField struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	// Type is "string", "bool", or "int" — the console renders the matching
	// input control and the adapter still receives the value as a string.
	Type     string `json:"type"`
	Required bool   `json:"required"`
	// Secret marks values that must be masked in the console and never echoed
	// back after save (e.g. a registry password carried in config).
	Secret      bool   `json:"secret,omitempty"`
	Default     string `json:"default,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	// Help is a one-line validation hint or consequence statement shown under
	// the field.
	Help string `json:"help,omitempty"`
}

type SetupStep struct {
	Text string `json:"text"`
	URL  string `json:"url,omitempty"`
}
