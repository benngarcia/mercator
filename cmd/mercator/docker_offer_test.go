package main

import (
	"testing"
)

func TestDockerIdentityDerivesFromConnectionConfig(t *testing.T) {
	id := dockerIdentityForConfig(map[string]string{"host": "ssh://ops@gpu-2"})
	if id.ConnectionID != "conn_docker_gpu-2" || id.OfferID != "offer_docker_gpu-2" || id.NativeRef != "gpu-2" {
		t.Errorf("connection identity = %+v, want ids derived from gpu-2", id)
	}
}
