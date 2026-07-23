package ociresolver

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func inspectorReturning(image InspectedImage) InspectFunc {
	return func(context.Context, string) (InspectedImage, error) {
		return image, nil
	}
}

func inspectorFailing(err error) InspectFunc {
	return func(context.Context, string) (InspectedImage, error) {
		return InspectedImage{}, err
	}
}

// The whole point of daemon-backed resolution: a bare tag becomes a pinned
// reference and the image says which platform it is, so the caller states
// neither.
func TestDaemonResolverPinsTagAndReadsPlatformFromImage(t *testing.T) {
	resolver := NewDaemonResolver(inspectorReturning(InspectedImage{
		RepoDigest:   "busybox@sha256:" + strings.Repeat("a", 64),
		OS:           "linux",
		Architecture: "arm64",
	}))

	resolved, err := resolver.Resolve(context.Background(), ResolveRequest{Image: "busybox"})

	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved.Image != "busybox@sha256:"+strings.Repeat("a", 64) {
		t.Fatalf("image: got %q want the repo digest", resolved.Image)
	}
	if resolved.Platform != "linux/arm64" {
		t.Fatalf("platform: got %q want linux/arm64", resolved.Platform)
	}
	if resolved.AlreadyPinned {
		t.Fatal("a tag reference must not be reported as already pinned")
	}
}

// A digest the caller supplied is kept verbatim, and the image still supplies
// the platform the caller left unstated.
func TestDaemonResolverKeepsSuppliedDigestAndFillsPlatform(t *testing.T) {
	digestRef := "busybox@sha256:" + strings.Repeat("b", 64)
	resolver := NewDaemonResolver(inspectorReturning(InspectedImage{
		RepoDigest:   digestRef,
		OS:           "linux",
		Architecture: "amd64",
	}))

	resolved, err := resolver.Resolve(context.Background(), ResolveRequest{Image: digestRef})

	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved.Image != digestRef {
		t.Fatalf("image: got %q want %q", resolved.Image, digestRef)
	}
	if resolved.Platform != "linux/amd64" {
		t.Fatalf("platform: got %q want linux/amd64", resolved.Platform)
	}
	if !resolved.AlreadyPinned {
		t.Fatal("a digest reference must be reported as already pinned")
	}
}

// Asking for a platform the image is not must fail at intake, not at launch.
func TestDaemonResolverRejectsPlatformTheImageContradicts(t *testing.T) {
	resolver := NewDaemonResolver(inspectorReturning(InspectedImage{
		RepoDigest:   "busybox@sha256:" + strings.Repeat("c", 64),
		OS:           "linux",
		Architecture: "arm64",
	}))

	_, err := resolver.Resolve(context.Background(), ResolveRequest{Image: "busybox", Platform: "linux/amd64"})

	if err == nil {
		t.Fatal("expected a platform mismatch error")
	}
	if !strings.Contains(err.Error(), "linux/arm64") || !strings.Contains(err.Error(), "linux/amd64") {
		t.Fatalf("error should name both platforms, got %q", err)
	}
}

// An image the host does not have is a setup mistake with an obvious fix, so
// the message has to say what to run.
func TestDaemonResolverTellsYouToPullAMissingImage(t *testing.T) {
	resolver := NewDaemonResolver(inspectorFailing(errors.New("no such image")))

	_, err := resolver.Resolve(context.Background(), ResolveRequest{Image: "busybox"})

	if err == nil {
		t.Fatal("expected an error for an image the host does not hold")
	}
	if !strings.Contains(err.Error(), "docker pull busybox") {
		t.Fatalf("error should suggest the pull command, got %q", err)
	}
}

// A reference the caller already pinned and described needs nothing from the
// local host, so an unreachable host must not break it.
func TestDaemonResolverAcceptsFullySpecifiedReferenceWithoutTheHost(t *testing.T) {
	digestRef := "busybox@sha256:" + strings.Repeat("d", 64)
	resolver := NewDaemonResolver(inspectorFailing(errors.New("cannot connect to the Docker daemon")))

	resolved, err := resolver.Resolve(context.Background(), ResolveRequest{Image: digestRef, Platform: "linux/amd64"})

	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved.Image != digestRef || resolved.Platform != "linux/amd64" {
		t.Fatalf("resolved: got %+v want the reference unchanged", resolved)
	}
}

// An image built locally and never pushed has no digest another host could
// pull, so pinning it would record a reference that does not resolve anywhere.
func TestDaemonResolverRejectsImageWithNoRegistryDigest(t *testing.T) {
	resolver := NewDaemonResolver(inspectorReturning(InspectedImage{OS: "linux", Architecture: "arm64"}))

	_, err := resolver.Resolve(context.Background(), ResolveRequest{Image: "my-local-build"})

	if err == nil {
		t.Fatal("expected an error for an image with no registry digest")
	}
	if !strings.Contains(err.Error(), "no registry digest") {
		t.Fatalf("error should explain the missing digest, got %q", err)
	}
}
