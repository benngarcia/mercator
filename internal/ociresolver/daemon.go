package ociresolver

import (
	"context"
	"errors"
	"fmt"
)

// InspectedImage is what a local image store can report about an image it
// already holds.
type InspectedImage struct {
	// RepoDigest is the repository@sha256:... reference the image was pulled
	// by, or empty when the image exists only on this host.
	RepoDigest   string
	OS           string
	Architecture string
}

// Platform renders the OCI platform string the domain uses, or empty when the
// store did not report both halves.
func (i InspectedImage) Platform() string {
	if i.OS == "" || i.Architecture == "" {
		return ""
	}
	return i.OS + "/" + i.Architecture
}

// InspectFunc looks up one image reference in a local image store.
type InspectFunc func(ctx context.Context, ref string) (InspectedImage, error)

// DaemonResolver pins tag-form references against a local image store, so a
// caller can say `busybox` and still get the reproducible, digest-pinned run
// Mercator records. The image answers both questions Mercator has about it:
// which digest to launch, and which platform it was built for. Neither is
// something an operator should have to retype.
//
// It resolves against the broker host's Docker endpoint, which is the endpoint
// that launches local runs. A remote Docker connection may hold a different
// image set; registry-backed resolution is the general answer and is not built
// yet.
type DaemonResolver struct {
	inspect InspectFunc
}

func NewDaemonResolver(inspect InspectFunc) *DaemonResolver {
	return &DaemonResolver{inspect: inspect}
}

// Resolve returns the digest-pinned reference and the platform to record for
// one image. An empty request platform means the image decides; a stated
// platform that contradicts the image is an error rather than a silent
// mismatch that would fail later at launch.
func (r *DaemonResolver) Resolve(ctx context.Context, req ResolveRequest) (ResolvedImage, error) {
	if req.Image == "" {
		return ResolvedImage{}, errors.New("ociresolver: image is required")
	}
	if r.inspect == nil {
		return ResolvedImage{}, errors.New("ociresolver: image inspection is not configured")
	}
	pinned := digestRefPattern.MatchString(req.Image)
	info, err := r.inspect(ctx, req.Image)
	if err != nil {
		// A reference the caller already pinned and described completely needs
		// nothing from the local store, so a store that cannot see it is not
		// fatal. Anything else is missing information we refuse to invent.
		if pinned && req.Platform != "" {
			return ResolvedImage{
				Image:         req.Image,
				Digest:        digestFromImage(req.Image),
				Platform:      req.Platform,
				AlreadyPinned: true,
			}, nil
		}
		return ResolvedImage{}, fmt.Errorf("image %q is not on the Docker host; pull it first (docker pull %s): %w", req.Image, req.Image, err)
	}

	platform := info.Platform()
	if req.Platform != "" {
		if platform != "" && req.Platform != platform {
			return ResolvedImage{}, fmt.Errorf("image %q is %s but the workload asks for %s", req.Image, platform, req.Platform)
		}
		platform = req.Platform
	}
	if platform == "" {
		return ResolvedImage{}, fmt.Errorf("the Docker host did not report a platform for image %q", req.Image)
	}

	if pinned {
		return ResolvedImage{
			Image:         req.Image,
			Digest:        digestFromImage(req.Image),
			Platform:      platform,
			AlreadyPinned: true,
		}, nil
	}
	if info.RepoDigest == "" {
		return ResolvedImage{}, fmt.Errorf("image %q has no registry digest on this host, so no other host could pull it; push it or reference it by digest", req.Image)
	}
	return ResolvedImage{
		Image:    info.RepoDigest,
		Digest:   digestFromImage(info.RepoDigest),
		Platform: platform,
	}, nil
}
