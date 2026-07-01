package ociresolver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
)

var digestRefPattern = regexp.MustCompile(`^.+@sha256:[a-fA-F0-9]{64}$`)

type ResolveRequest struct {
	Image    string
	Platform string
}

type ResolvedImage struct {
	Image         string `json:"image"`
	Digest        string `json:"digest"`
	Platform      string `json:"platform"`
	AlreadyPinned bool   `json:"already_pinned,omitempty"`
}

type StaticResolver struct {
	images map[string]ResolvedImage
	// synthetic, when true, resolves any otherwise-unknown tag to a
	// deterministic synthetic digest derived from the image+platform. This is a
	// development/fake-mode convenience: it performs NO network access, so the
	// minimal create path ({"image":"busybox"}) is exercisable end-to-end in
	// fake mode with no pre-pinned digest. It is never enabled by default.
	synthetic bool
}

type Option func(*StaticResolver)

// WithSyntheticDigests enables permissive, network-free tag resolution: any tag
// not found in the static map resolves to a deterministic synthetic digest.
func WithSyntheticDigests() Option {
	return func(r *StaticResolver) {
		r.synthetic = true
	}
}

func NewStaticResolver(images map[string]ResolvedImage, options ...Option) *StaticResolver {
	cloned := make(map[string]ResolvedImage, len(images))
	for key, value := range images {
		cloned[key] = value
	}
	r := &StaticResolver{images: cloned}
	for _, option := range options {
		option(r)
	}
	return r
}

func (r *StaticResolver) Resolve(_ context.Context, req ResolveRequest) (ResolvedImage, error) {
	if req.Image == "" || req.Platform == "" {
		return ResolvedImage{}, fmt.Errorf("ociresolver: image and platform are required")
	}
	if digestRefPattern.MatchString(req.Image) {
		return ResolvedImage{Image: req.Image, Digest: digestFromImage(req.Image), Platform: req.Platform, AlreadyPinned: true}, nil
	}
	key := req.Image + "|" + req.Platform
	if result, ok := r.images[key]; ok {
		if result.Platform == "" {
			result.Platform = req.Platform
		}
		return result, nil
	}
	if r.synthetic {
		digest := syntheticDigest(req.Image, req.Platform)
		return ResolvedImage{
			Image:    repository(req.Image) + "@" + digest,
			Digest:   digest,
			Platform: req.Platform,
		}, nil
	}
	return ResolvedImage{}, fmt.Errorf("image %q is not digest-pinned and registry tag resolution is not implemented; pin it as repository@sha256:<digest> (e.g. via `docker inspect --format '{{index .RepoDigests 0}}' <tag>`)", req.Image)
}

// syntheticDigest derives a stable sha256:<hex> from the image tag + platform so
// the same tag always resolves to the same synthetic digest within a build.
func syntheticDigest(image, platform string) string {
	sum := sha256.Sum256([]byte("mercator-synthetic\x00" + image + "\x00" + platform))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// repository strips an existing tag (":tag") from an image reference, leaving
// the repository portion so the digest can be appended as repo@sha256:...
func repository(image string) string {
	// A tag is the last ':' that appears after the last '/' (to avoid matching a
	// registry-port colon like registry:5000/foo).
	lastSlash := -1
	for i := 0; i < len(image); i++ {
		if image[i] == '/' {
			lastSlash = i
		}
	}
	for i := len(image) - 1; i > lastSlash; i-- {
		if image[i] == ':' {
			return image[:i]
		}
	}
	return image
}

func digestFromImage(image string) string {
	for i := len(image) - len("sha256:") - 64; i >= 0; i-- {
		if image[i:i+len("sha256:")] == "sha256:" {
			return image[i:]
		}
	}
	return ""
}
