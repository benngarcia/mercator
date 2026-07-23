package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ImageInfo is what a Docker endpoint already knows about an image it holds:
// the digest-pinned reference to launch, and the platform the image was built
// for. Both answers come from the image itself, so no caller has to restate
// them on the command line.
type ImageInfo struct {
	// RepoDigest is the repository@sha256:... reference the image was pulled
	// by. Empty for an image built locally and never pushed, because such an
	// image has no digest any other host could pull.
	RepoDigest   string
	OS           string
	Architecture string
}

// InspectImage reads one image's digest and platform from the configured
// Docker endpoint. It reports a missing image distinctly from a malformed
// response so callers can tell "pull it first" from "this daemon is broken".
func (c *CLIClient) InspectImage(ctx context.Context, ref string) (ImageInfo, error) {
	output, err := c.run(ctx, "image", "inspect", ref, "--format", "{{json .}}")
	if err != nil {
		return ImageInfo{}, fmt.Errorf("docker image inspect %s: %w: %s", ref, err, strings.TrimSpace(output))
	}
	return parseImageInspect([]byte(output))
}

// parseImageInspect decodes the JSON emitted by
// `docker image inspect --format '{{json .}}'`.
func parseImageInspect(raw []byte) (ImageInfo, error) {
	var doc struct {
		Architecture string   `json:"Architecture"`
		Os           string   `json:"Os"`
		RepoDigests  []string `json:"RepoDigests"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return ImageInfo{}, fmt.Errorf("parse docker image inspect: %w", err)
	}
	info := ImageInfo{
		OS:           doc.Os,
		Architecture: normalizeArch(doc.Architecture),
	}
	if len(doc.RepoDigests) > 0 {
		info.RepoDigest = doc.RepoDigests[0]
	}
	return info, nil
}
