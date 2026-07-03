package main

import (
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	dockeradapter "github.com/benngarcia/mercator/internal/adapter/docker"
)

func TestDockerIdentityDefaultsToLoopbackNotLocal(t *testing.T) {
	id := dockerIdentity(map[string]string{})
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

func TestDockerIdentityDerivesFromContext(t *testing.T) {
	id := dockerIdentity(map[string]string{"MERCATOR_DOCKER_CONTEXT": "dockerhost"})
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

func TestDockerIdentityDerivesLabelFromRemoteHost(t *testing.T) {
	id := dockerIdentity(map[string]string{"MERCATOR_DOCKER_HOST": "ssh://user@dockerhost"})
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

func TestDockerIdentityHonorsExplicitOverrides(t *testing.T) {
	id := dockerIdentity(map[string]string{
		"MERCATOR_DOCKER_CONTEXT":       "dockerhost",
		"MERCATOR_DOCKER_CONNECTION_ID": "conn_custom",
		"MERCATOR_DOCKER_OFFER_ID":      "offer_custom",
		"MERCATOR_DOCKER_NATIVE_REF":    "my-ref",
	})
	if id.ConnectionID != "conn_custom" || id.OfferID != "offer_custom" || id.NativeRef != "my-ref" {
		t.Errorf("explicit overrides not honored: %+v", id)
	}
}

func TestDockerOfferFromInfoUsesProbedCapacity(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	id := dockerIdentity(map[string]string{"MERCATOR_DOCKER_CONTEXT": "dockerhost"})
	info := dockeradapter.HostInfo{Architecture: "x86_64", OSType: "linux", NCPU: 8, MemTotalBytes: 16 * 1024 * 1024 * 1024, Name: "dockerhost"}

	offer := dockerOfferFromInfo(map[string]string{"MERCATOR_DOCKER_CONTEXT": "dockerhost"}, id, info, now)

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

func TestDockerOfferFromInfoFallsBackWhenProbeEmpty(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	id := dockerIdentity(map[string]string{})
	// Empty HostInfo simulates an unreachable endpoint / failed probe.
	offer := dockerOfferFromInfo(map[string]string{}, id, dockeradapter.HostInfo{}, now)

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

func TestDockerArchOverrideWinsOverProbe(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	values := map[string]string{"MERCATOR_DOCKER_ARCH": "arm64"}
	id := dockerIdentity(values)
	info := dockeradapter.HostInfo{Architecture: "x86_64", NCPU: 4, MemTotalBytes: 1 << 30}
	offer := dockerOfferFromInfo(values, id, info, now)
	if offer.Platform.Architecture != "arm64" {
		t.Errorf("explicit MERCATOR_DOCKER_ARCH should win: got %q, want arm64", offer.Platform.Architecture)
	}
}

func TestDockerIdentityForConfigKeepsBootstrapOverrides(t *testing.T) {
	values := map[string]string{
		"MERCATOR_DOCKER_HOST":     "ssh://ops@gpu-1",
		"MERCATOR_DOCKER_OFFER_ID": "offer_custom",
	}
	id := dockerIdentityForConfig(values, map[string]string{"host": "ssh://ops@gpu-1"})
	if id.OfferID != "offer_custom" {
		t.Errorf("bootstrap config OfferID = %q, want env override offer_custom", id.OfferID)
	}
	other := dockerIdentityForConfig(values, map[string]string{"host": "ssh://ops@gpu-2"})
	if other.OfferID != "offer_docker_gpu-2" || other.NativeRef != "gpu-2" {
		t.Errorf("second endpoint identity = %+v, want offer_docker_gpu-2/gpu-2", other)
	}
	if other.OfferID == id.OfferID {
		t.Error("two docker endpoints must not share an offer id")
	}
}

func TestDockerOfferingAdapterServesFreshOffersPerCall(t *testing.T) {
	// The offer must be rebuilt on every ListOffers call: a snapshot frozen at
	// adapter construction expires one hour in and permanently fails placement.
	client := dockeradapter.NewCLIClient("false") // probe fails instantly; capacity falls back
	values := map[string]string{}
	ad := dockerOfferingAdapter{client: client, values: values, id: dockerIdentity(values)}

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
