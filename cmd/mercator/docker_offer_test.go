package main

import (
	"testing"
	"time"

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
	id := dockerIdentity(map[string]string{"MERCATOR_DOCKER_CONTEXT": "homeserver"})
	if id.Context != "homeserver" {
		t.Errorf("Context = %q, want homeserver", id.Context)
	}
	if id.ConnectionID != "conn_docker_homeserver" {
		t.Errorf("ConnectionID = %q, want conn_docker_homeserver", id.ConnectionID)
	}
	if id.NativeRef != "homeserver" {
		t.Errorf("NativeRef = %q, want homeserver", id.NativeRef)
	}
}

func TestDockerIdentityDerivesLabelFromRemoteHost(t *testing.T) {
	id := dockerIdentity(map[string]string{"MERCATOR_DOCKER_HOST": "ssh://beng@homeserver"})
	if id.Host != "ssh://beng@homeserver" {
		t.Errorf("Host = %q, want ssh://beng@homeserver", id.Host)
	}
	if id.ConnectionID != "conn_docker_homeserver" {
		t.Errorf("ConnectionID = %q, want conn_docker_homeserver (host label)", id.ConnectionID)
	}
	if id.NativeRef != "homeserver" {
		t.Errorf("NativeRef = %q, want homeserver", id.NativeRef)
	}
}

func TestDockerIdentityHonorsExplicitOverrides(t *testing.T) {
	id := dockerIdentity(map[string]string{
		"MERCATOR_DOCKER_CONTEXT":       "homeserver",
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
	id := dockerIdentity(map[string]string{"MERCATOR_DOCKER_CONTEXT": "homeserver"})
	info := dockeradapter.HostInfo{Architecture: "x86_64", OSType: "linux", NCPU: 8, MemTotalBytes: 16 * 1024 * 1024 * 1024, Name: "homeserver"}

	offer := dockerOfferFromInfo(map[string]string{"MERCATOR_DOCKER_CONTEXT": "homeserver"}, id, info, now)

	if offer.AdapterType != "docker" {
		t.Errorf("AdapterType = %q, want docker", offer.AdapterType)
	}
	if offer.ID != "offer_docker_homeserver" || offer.ConnectionID != "conn_docker_homeserver" {
		t.Errorf("identity not applied: id=%q conn=%q", offer.ID, offer.ConnectionID)
	}
	if offer.NativeRef != "homeserver" {
		t.Errorf("NativeRef = %q, want homeserver", offer.NativeRef)
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
