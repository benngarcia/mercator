package ociresolver

import (
	"context"
	"testing"
)

func TestResolverLeavesDigestPinnedReferencesUnchanged(t *testing.T) {
	resolver := NewStaticResolver(nil)
	result, err := resolver.Resolve(context.Background(), ResolveRequest{
		Image:    "ghcr.io/acme/trainer@sha256:0000000000000000000000000000000000000000000000000000000000000000",
		Platform: "linux/amd64",
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if result.Image != "ghcr.io/acme/trainer@sha256:0000000000000000000000000000000000000000000000000000000000000000" || !result.AlreadyPinned {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestResolverResolvesTagsToDigest(t *testing.T) {
	resolver := NewStaticResolver(map[string]ResolvedImage{
		"ghcr.io/acme/trainer:latest|linux/amd64": {
			Image:    "ghcr.io/acme/trainer@sha256:1111111111111111111111111111111111111111111111111111111111111111",
			Digest:   "sha256:1111111111111111111111111111111111111111111111111111111111111111",
			Platform: "linux/amd64",
		},
	})
	result, err := resolver.Resolve(context.Background(), ResolveRequest{Image: "ghcr.io/acme/trainer:latest", Platform: "linux/amd64"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if result.Digest == "" || result.AlreadyPinned {
		t.Fatalf("expected tag to resolve to digest, got %+v", result)
	}
}

func TestResolverReportsUnknownTags(t *testing.T) {
	resolver := NewStaticResolver(nil)
	if _, err := resolver.Resolve(context.Background(), ResolveRequest{Image: "ghcr.io/acme/missing:latest", Platform: "linux/amd64"}); err == nil {
		t.Fatal("expected unknown tag error")
	}
}
