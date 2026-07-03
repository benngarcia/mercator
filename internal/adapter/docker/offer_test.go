package docker

import (
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
)

func TestDeriveIdentityDefaultsToLoopbackNotLocal(t *testing.T) {
	id := DeriveIdentity("", "")
	if id.ConnectionID != "conn_docker_loopback" {
		t.Errorf("ConnectionID = %q, want conn_docker_loopback", id.ConnectionID)
	}
	if id.OfferID != "offer_docker_loopback" {
		t.Errorf("OfferID = %q, want offer_docker_loopback", id.OfferID)
	}
	if id.NativeRef != "loopback" {
		t.Errorf("NativeRef = %q, want loopback", id.NativeRef)
	}
}

func TestDeriveIdentityFromContext(t *testing.T) {
	id := DeriveIdentity("", "dockerhost")
	if id.Context != "dockerhost" {
		t.Errorf("Context = %q, want dockerhost", id.Context)
	}
	if id.ConnectionID != "conn_docker_dockerhost" {
		t.Errorf("ConnectionID = %q, want conn_docker_dockerhost", id.ConnectionID)
	}
	if id.NativeRef != "dockerhost" {
		t.Errorf("NativeRef = %q, want dockerhost", id.NativeRef)
	}
}

func TestDeriveIdentityLabelFromRemoteHost(t *testing.T) {
	id := DeriveIdentity("ssh://user@dockerhost", "")
	if id.Host != "ssh://user@dockerhost" {
		t.Errorf("Host = %q, want ssh://user@dockerhost", id.Host)
	}
	if id.ConnectionID != "conn_docker_dockerhost" {
		t.Errorf("ConnectionID = %q, want conn_docker_dockerhost (host label)", id.ConnectionID)
	}
	if id.NativeRef != "dockerhost" {
		t.Errorf("NativeRef = %q, want dockerhost", id.NativeRef)
	}
}

func TestStandingOfferUsesProbedCapacity(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	id := DeriveIdentity("", "dockerhost")
	info := HostInfo{Architecture: "x86_64", OSType: "linux", NCPU: 8, MemTotalBytes: 16 * 1024 * 1024 * 1024, Name: "dockerhost"}

	offer := StandingOffer(id, "", info, now)

	if offer.AdapterType != "docker" {
		t.Errorf("AdapterType = %q, want docker", offer.AdapterType)
	}
	if offer.ID != "offer_docker_dockerhost" || offer.ConnectionID != "conn_docker_dockerhost" {
		t.Errorf("identity not applied: id=%q conn=%q", offer.ID, offer.ConnectionID)
	}
	if offer.NativeRef != "dockerhost" {
		t.Errorf("NativeRef = %q, want dockerhost", offer.NativeRef)
	}
	if offer.Platform.Architecture != "amd64" {
		t.Errorf("Architecture = %q, want amd64 (normalized from x86_64)", offer.Platform.Architecture)
	}
	if offer.Resources.CPUMillis != 8000 {
		t.Errorf("CPUMillis = %d, want 8000 (NCPU*1000)", offer.Resources.CPUMillis)
	}
	if offer.Resources.MemoryBytes != 16*1024*1024*1024 {
		t.Errorf("MemoryBytes = %d, want 16GiB", offer.Resources.MemoryBytes)
	}
}

func TestStandingOfferFallsBackWhenProbeEmpty(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	// Empty HostInfo simulates an unreachable endpoint / failed probe.
	offer := StandingOffer(DeriveIdentity("", ""), "", HostInfo{}, now)

	if offer.Platform.Architecture == "" {
		t.Error("Architecture must fall back to a default, got empty")
	}
	if offer.Resources.CPUMillis <= 0 || offer.Resources.MemoryBytes <= 0 {
		t.Errorf("capacity must fall back to positive defaults, got cpu=%d mem=%d", offer.Resources.CPUMillis, offer.Resources.MemoryBytes)
	}
	if !offer.Capabilities.Container.SupportsDigestRefs {
		t.Error("docker offer must advertise digest-ref support")
	}
}

func TestStandingOfferArchOverrideWinsOverProbe(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	info := HostInfo{Architecture: "x86_64", NCPU: 4, MemTotalBytes: 1 << 30}
	offer := StandingOffer(DeriveIdentity("", ""), "arm64", info, now)
	if offer.Platform.Architecture != "arm64" {
		t.Errorf("explicit arch override should win: got %q, want arm64", offer.Platform.Architecture)
	}
}

func TestOfferingAdapterServesFreshOffersPerCall(t *testing.T) {
	// The offer must be rebuilt on every ListOffers call: a snapshot frozen at
	// adapter construction expires one hour in and permanently fails placement.
	client := NewCLIClient("false") // probe fails instantly; capacity falls back
	ad := NewOffering(client, DeriveIdentity("", ""), "")

	first, err := ad.ListOffers(t.Context(), adapter.OfferRequest{})
	if err != nil || len(first) != 1 {
		t.Fatalf("first ListOffers: offers=%v err=%v", first, err)
	}
	time.Sleep(5 * time.Millisecond)
	second, err := ad.ListOffers(t.Context(), adapter.OfferRequest{})
	if err != nil || len(second) != 1 {
		t.Fatalf("second ListOffers: offers=%v err=%v", second, err)
	}
	if !second[0].ObservedAt.After(first[0].ObservedAt) {
		t.Fatalf("offer is frozen: first ObservedAt=%v, second ObservedAt=%v", first[0].ObservedAt, second[0].ObservedAt)
	}
	if !second[0].ExpiresAt.After(time.Now().Add(30 * time.Minute)) {
		t.Fatalf("offer expiry did not refresh: %v", second[0].ExpiresAt)
	}
}
