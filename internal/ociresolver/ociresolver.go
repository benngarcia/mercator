package ociresolver

import (
	"context"
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
}

func NewStaticResolver(images map[string]ResolvedImage) *StaticResolver {
	cloned := make(map[string]ResolvedImage, len(images))
	for key, value := range images {
		cloned[key] = value
	}
	return &StaticResolver{images: cloned}
}

func (r *StaticResolver) Resolve(_ context.Context, req ResolveRequest) (ResolvedImage, error) {
	if req.Image == "" || req.Platform == "" {
		return ResolvedImage{}, fmt.Errorf("ociresolver: image and platform are required")
	}
	if digestRefPattern.MatchString(req.Image) {
		return ResolvedImage{Image: req.Image, Digest: digestFromImage(req.Image), Platform: req.Platform, AlreadyPinned: true}, nil
	}
	key := req.Image + "|" + req.Platform
	result, ok := r.images[key]
	if !ok {
		return ResolvedImage{}, fmt.Errorf("ociresolver: tag resolution not found")
	}
	if result.Platform == "" {
		result.Platform = req.Platform
	}
	return result, nil
}

func digestFromImage(image string) string {
	for i := len(image) - len("sha256:") - 64; i >= 0; i-- {
		if image[i:i+len("sha256:")] == "sha256:" {
			return image[i:]
		}
	}
	return ""
}
