package httpapi

import (
	"context"

	"github.com/benngarcia/mercator/internal/ociresolver"
)

func (s *Server) ResolveImage(ctx context.Context, request ResolveImageRequestObject) (ResolveImageResponseObject, error) {
	if s.resolver == nil {
		return ResolveImage501JSONResponse(apiError("IMAGE_RESOLVER_DISABLED", "Image resolver is not configured.")), nil
	}
	body := request.Body
	resolved, err := s.resolver.Resolve(ctx, ociresolver.ResolveRequest{Image: body.Image, Platform: body.Platform})
	if err != nil {
		return ResolveImage400JSONResponse(apiError("IMAGE_RESOLUTION_FAILED", err.Error())), nil
	}
	return ResolveImage200JSONResponse{Image: resolved}, nil
}
