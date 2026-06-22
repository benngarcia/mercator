package registry

import (
	"testing"

	"github.com/benngarcia/mercator/internal/adapter"
)

func TestRegistryRegistersAndFindsAdapters(t *testing.T) {
	reg := New()
	fakeAdapter := stubAdapter{}
	reg.Register("fake", fakeAdapter)

	got, ok := reg.Get("fake")
	if !ok || got == nil {
		t.Fatalf("expected registered adapter")
	}
}

func TestRegistryReportsMissingAdapters(t *testing.T) {
	if _, ok := New().Get("missing"); ok {
		t.Fatal("expected missing adapter")
	}
}

type stubAdapter struct{ adapter.Adapter }
