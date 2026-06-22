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

// Synthetic-digest mode resolves an arbitrary tag (e.g. "busybox") to a
// deterministic digest with no network, so the minimal create path works in
// fake mode without a pre-pinned image.
func TestSyntheticResolverResolvesArbitraryTagDeterministically(t *testing.T) {
	resolver := NewStaticResolver(nil, WithSyntheticDigests())

	first, err := resolver.Resolve(context.Background(), ResolveRequest{Image: "busybox", Platform: "linux/amd64"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if first.Digest == "" || first.AlreadyPinned {
		t.Fatalf("expected a synthetic digest, got %+v", first)
	}
	if !digestRefPattern.MatchString(first.Image) {
		t.Fatalf("synthetic image is not digest-pinned: %q", first.Image)
	}

	// Determinism: same tag + platform resolves to the same digest.
	second, err := resolver.Resolve(context.Background(), ResolveRequest{Image: "busybox", Platform: "linux/amd64"})
	if err != nil {
		t.Fatalf("resolve again: %v", err)
	}
	if first.Image != second.Image || first.Digest != second.Digest {
		t.Fatalf("synthetic resolution is not deterministic: %q vs %q", first.Image, second.Image)
	}

	// A different platform yields a different digest.
	other, err := resolver.Resolve(context.Background(), ResolveRequest{Image: "busybox", Platform: "linux/arm64"})
	if err != nil {
		t.Fatalf("resolve arm64: %v", err)
	}
	if other.Digest == first.Digest {
		t.Fatalf("expected platform to vary the synthetic digest")
	}
}

// An already-tagged registry-with-port reference keeps its repository when
// pinned (the port colon must not be mistaken for a tag).
func TestSyntheticResolverStripsTagNotRegistryPort(t *testing.T) {
	resolver := NewStaticResolver(nil, WithSyntheticDigests())
	out, err := resolver.Resolve(context.Background(), ResolveRequest{Image: "registry:5000/team/app:v2", Platform: "linux/amd64"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got, want := repository("registry:5000/team/app:v2"), "registry:5000/team/app"; got != want {
		t.Fatalf("repository(): got %q want %q", got, want)
	}
	if !digestRefPattern.MatchString(out.Image) {
		t.Fatalf("expected digest-pinned image, got %q", out.Image)
	}
}
