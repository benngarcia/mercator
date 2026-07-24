package broker

import (
	"context"
	"testing"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/capability"
	"github.com/benngarcia/mercator/internal/domain"
)

type stubAdapter struct{ oneShotLane }

func (stubAdapter) ListOffers(context.Context, adapter.OfferRequest) ([]domain.OfferSnapshot, error) {
	return nil, nil
}
func (stubAdapter) Verify(context.Context) error { return nil }

func TestFactoryBuildsRegisteredType(t *testing.T) {
	f := NewFactory()
	f.Register(adapter.Manifest{Type: "stub"}, func(map[string]string, string) (capability.Backend, error) {
		return stubAdapter{}, nil
	})
	if _, err := f.Build("stub", nil, ""); err != nil {
		t.Fatalf("build stub: %v", err)
	}
	if _, err := f.Build("nope", nil, ""); err == nil {
		t.Fatal("expected error for unknown adapter type")
	}
}
