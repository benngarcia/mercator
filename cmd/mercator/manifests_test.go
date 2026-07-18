package main

import (
	"strings"
	"testing"

	"github.com/benngarcia/mercator/internal/adapter"
	dockeradapter "github.com/benngarcia/mercator/internal/adapter/docker"
	runpodadapter "github.com/benngarcia/mercator/internal/adapter/runpod"
	shadeformadapter "github.com/benngarcia/mercator/internal/adapter/shadeform"
	vastadapter "github.com/benngarcia/mercator/internal/adapter/vast"
)

// TestShippedManifestsAreWellFormed guards the onboarding contract every
// registered adapter serves through GET /v1/adapters: identity fields present,
// ordered setup steps with https links, and config fields typed for the
// console's form renderer.
func TestShippedManifestsAreWellFormed(t *testing.T) {
	manifests := []adapter.Manifest{
		dockeradapter.Manifest(),
		runpodadapter.Manifest(),
		shadeformadapter.Manifest(),
		vastadapter.Manifest(),
	}
	validTypes := map[string]bool{"string": true, "bool": true, "int": true}
	seen := map[string]bool{}
	for _, m := range manifests {
		if m.Type == "" || m.DisplayName == "" || m.Logo == "" || m.Description == "" {
			t.Errorf("%q: manifest missing identity fields: %+v", m.Type, m)
		}
		if seen[m.Type] {
			t.Errorf("%q: duplicate adapter type", m.Type)
		}
		seen[m.Type] = true
		if len(m.SetupSteps) == 0 {
			t.Errorf("%q: manifest has no setup steps", m.Type)
		}
		for i, step := range m.SetupSteps {
			if strings.TrimSpace(step.Text) == "" {
				t.Errorf("%q: setup step %d has empty text", m.Type, i)
			}
			if step.URL != "" && !strings.HasPrefix(step.URL, "https://") {
				t.Errorf("%q: setup step %d has non-https url %q", m.Type, i, step.URL)
			}
		}
		for _, f := range m.ConfigFields {
			if f.Name == "" || f.Label == "" {
				t.Errorf("%q: config field missing name/label: %+v", m.Type, f)
			}
			if !validTypes[f.Type] {
				t.Errorf("%q: config field %q has invalid type %q", m.Type, f.Name, f.Type)
			}
		}
		if m.Credential.Required && m.Credential.Label == "" {
			t.Errorf("%q: required credential needs a label", m.Type)
		}
	}
}
