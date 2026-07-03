package main

import (
	"testing"
)

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

func TestDockerIdentityDerivesFromEnvEndpoint(t *testing.T) {
	id := dockerIdentity(map[string]string{"MERCATOR_DOCKER_HOST": "ssh://beng@homeserver"})
	if id.ConnectionID != "conn_docker_homeserver" || id.NativeRef != "homeserver" {
		t.Errorf("identity not derived from env endpoint: %+v", id)
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
