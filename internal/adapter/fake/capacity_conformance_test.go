package fake

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/capability/capacitytest"
	"github.com/benngarcia/mercator/internal/domain"
)

// TestTheSimulatedProviderKeepsEveryCapacityPromise runs the shared
// CapacityProvider suite against the world every Blueprint in the corpus is
// written against.
//
// It is the reason a Lab result about capacity means anything. A simulated
// provider that quietly allocated two machines for one command, or went on
// owning a machine it destroyed, would make every scenario built on it agree
// with a control plane that was wrong, and no Blueprint could tell.
func TestTheSimulatedProviderKeepsEveryCapacityPromise(t *testing.T) {
	subject := capacitytest.Subject{
		Name:     "fake",
		Provider: worldSellingOneProduct(t),
		Lease: capacitytest.Lease{
			TrialID: "sim01",

			ConnectionID:    "conn_fake",
			ControlPlaneURL: "https://mercator.test",
			AgentVersion:    "v0.7.1",
			EnrollmentToken: "enrolment-nothing-minted",
			MaxLifetime:     30 * time.Minute,
		},
		Capacity: func(context.Context) (capacitytest.Origin, error) {
			return capacitytest.Origin{OfferSnapshotID: productListing, NativeRef: productNativeRef}, nil
		},
	}

	for _, promise := range capacitytest.Promises() {
		t.Run(promise.Name, func(t *testing.T) {
			err := promise.Keep(t.Context(), subject)
			if errors.Is(err, capacitytest.ErrNotApplicable) {
				t.Skip(err.Error())
			}
			if err != nil {
				t.Fatalf("%s (%s): %v", promise.Name, promise.Rule, err)
			}
		})
	}
}

const (
	productListing   = "sim-a6000"
	productNativeRef = "simcloud/eu-1/A6000"
)

// worldSellingOneProduct is a marketplace selling a machine type rather than a
// named machine, which is what lets every promise rent its own host: a listing
// that names one machine can be bought once, and the suite rents several.
func worldSellingOneProduct(t *testing.T) *World {
	t.Helper()
	world := NewWorld(NewClock(worldStart))
	listing := &Machine{
		Offer: domain.OfferSnapshot{
			ID:        productListing,
			NativeRef: productNativeRef,
			Kind:      domain.OfferKindProvisionable,
			Lane:      domain.LaneReusable,
			Pricing:   domain.PriceModel{Currency: "USD", RatePerSecondUSD: 0.0005, Known: true},
			Resources: domain.ResourceInventory{EphemeralDiskBytes: 200 << 30, EphemeralDiskKnown: true},
		},
		AcquisitionSpend: time.Minute,
		BootSpend:        time.Minute,
		AgentReadySpend:  time.Minute,
	}
	if err := world.AddMachine(listing); err != nil {
		t.Fatalf("add listing: %v", err)
	}
	return world
}
