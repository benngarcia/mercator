package docker

import "testing"

// The daemon reports the machine architecture in uname vocabulary; Mercator's
// domain speaks OCI. An unconverted aarch64 would never match an arm64 offer.
func TestParseImageInspectNormalizesArchitecture(t *testing.T) {
	raw := []byte(`{"Architecture":"arm64","Os":"linux","RepoDigests":["busybox@sha256:abc"]}`)

	info, err := parseImageInspect(raw)

	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if info.Architecture != "arm64" || info.OS != "linux" {
		t.Fatalf("platform: got %s/%s want linux/arm64", info.OS, info.Architecture)
	}
	if info.RepoDigest != "busybox@sha256:abc" {
		t.Fatalf("repo digest: got %q", info.RepoDigest)
	}
}

// An image built locally has an empty RepoDigests array. Reporting that as an
// empty digest lets the resolver refuse it with a useful message.
func TestParseImageInspectReportsMissingRepoDigest(t *testing.T) {
	raw := []byte(`{"Architecture":"x86_64","Os":"linux","RepoDigests":[]}`)

	info, err := parseImageInspect(raw)

	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if info.RepoDigest != "" {
		t.Fatalf("repo digest: got %q want empty", info.RepoDigest)
	}
	if info.Architecture != "amd64" {
		t.Fatalf("architecture: got %q want amd64", info.Architecture)
	}
}
