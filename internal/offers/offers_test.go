package offers

import (
	"context"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
)

func TestServiceIngestsListsAndRebuildsOfferSnapshots(t *testing.T) {
	ctx := context.Background()
	log := openOffersTestLog(t)
	svc := New(log)
	now := time.Date(2026, 6, 20, 18, 31, 22, 0, time.UTC)
	offer := offerSnapshot("offer_1", now)

	if err := svc.Ingest(ctx, IngestRequest{WorkspaceID: "ws_1", ConnectionID: "conn_1", Offers: []domain.OfferSnapshot{offer}}); err != nil {
		t.Fatalf("ingest offers: %v", err)
	}
	list, err := svc.ListCached(ctx, "ws_1", now)
	if err != nil {
		t.Fatalf("list cached: %v", err)
	}
	if len(list) != 1 || list[0].ID != "offer_1" {
		t.Fatalf("unexpected cached offers: %+v", list)
	}

	rebuilt := New(log)
	list, err = rebuilt.ListCached(ctx, "ws_1", now)
	if err != nil {
		t.Fatalf("list rebuilt: %v", err)
	}
	if len(list) != 1 || list[0].ConnectionID != "conn_1" {
		t.Fatalf("offer cache should rebuild from events, got %+v", list)
	}

	expired, err := rebuilt.ListCached(ctx, "ws_1", now.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("list expired: %v", err)
	}
	if len(expired) != 0 {
		t.Fatalf("expired offer should not be cached: %+v", expired)
	}
}

func openOffersTestLog(t *testing.T) *eventlog.SQLiteEventLog {
	t.Helper()
	log, err := eventlog.OpenSQLite(context.Background(), "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open event log: %v", err)
	}
	t.Cleanup(func() {
		if err := log.Close(); err != nil {
			t.Fatalf("close event log: %v", err)
		}
	})
	return log
}

func offerSnapshot(id string, now time.Time) domain.OfferSnapshot {
	return domain.OfferSnapshot{
		ID:           id,
		ConnectionID: "conn_1",
		AdapterType:  "fake",
		Kind:         domain.OfferKindStanding,
		NativeRef:    "native_1",
		ObservedAt:   now.Add(-time.Minute),
		ExpiresAt:    now.Add(time.Hour),
		Platform:     domain.Platform{OS: "linux", Architecture: "amd64"},
		Resources: domain.ResourceInventory{
			CPUMillis:          2000,
			MemoryBytes:        2 << 30,
			EphemeralDiskBytes: 2 << 30,
		},
		Capabilities: domain.CapabilityProfile{
			Container: domain.ContainerCapabilities{MaxContainers: 1, SupportsDigestRefs: true, MaxEnvironmentBytes: 32768},
			Network:   domain.NetworkCapabilities{Inbound: domain.InboundNetworkNone, Protocols: []string{"tcp"}},
			Pricing:   domain.PricingCapabilities{Known: true},
			Lifecycle: domain.LifecycleCapabilities{IdempotentLaunch: "deterministic_name", ListOwned: true},
		},
		Pricing:    domain.PriceModel{Currency: "USD", RatePerSecondUSD: 0.0001, Known: true},
		Queue:      &domain.QueueSnapshot{},
		Capacity:   domain.CapacityEvidence{Available: true, Confidence: 1},
		ImageCache: domain.ImageCacheEvidence{Known: true, ManifestCached: true},
	}
}

func TestServiceIngestsRepeatedlyForSameConnection(t *testing.T) {
	// A periodic offer refresh ingests the same connection's stream over and
	// over; a fixed ExpectedStreamVersion of 0 allowed exactly one ingest ever.
	ctx := context.Background()
	svc := New(openOffersTestLog(t))
	now := time.Date(2026, 6, 20, 18, 31, 22, 0, time.UTC)

	for i := 0; i < 3; i++ {
		offer := offerSnapshot("offer_1", now.Add(time.Duration(i)*time.Minute))
		if err := svc.Ingest(ctx, IngestRequest{WorkspaceID: "ws_1", ConnectionID: "conn_1", Offers: []domain.OfferSnapshot{offer}}); err != nil {
			t.Fatalf("ingest %d: %v", i, err)
		}
	}
	list, err := svc.ListCached(ctx, "ws_1", now)
	if err != nil {
		t.Fatalf("list cached: %v", err)
	}
	// offerSnapshot sets ObservedAt one minute before its argument; the last
	// ingest used now+2m, so the winning snapshot observes at now+1m.
	if len(list) != 1 || !list[0].ObservedAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("expected the latest ingest to win, got %+v", list)
	}
}
