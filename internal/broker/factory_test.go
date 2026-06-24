package broker

import (
	"context"
	"testing"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/domain"
)

type stubAdapter struct{ adapter.Adapter }

func (stubAdapter) ListOffers(context.Context, adapter.OfferRequest) ([]domain.OfferSnapshot, error) {
	return nil, nil
}
func (stubAdapter) Verify(context.Context) error { return nil }

func TestFactoryBuildsRegisteredType(t *testing.T) {
	f := NewFactory()
	f.Register("stub", func(map[string]string, string) (adapter.Adapter, error) {
		return stubAdapter{}, nil
	})
	if _, err := f.Build("stub", nil, ""); err != nil {
		t.Fatalf("build stub: %v", err)
	}
	if _, err := f.Build("nope", nil, ""); err == nil {
		t.Fatal("expected error for unknown adapter type")
	}
}
