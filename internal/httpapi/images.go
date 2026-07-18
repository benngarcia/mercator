package httpapi

import (
	"net/http"

	"github.com/benngarcia/mercator/internal/ociresolver"
)

func (s *Server) ResolveImage(w http.ResponseWriter, r *http.Request) {
	if s.resolver == nil {
		writeError(w, http.StatusNotImplemented, "IMAGE_RESOLVER_DISABLED", "Image resolver is not configured.")
		return
	}
	var body resolveImageBody
	if !decodeJSONBody(w, r, &body) {
		return
	}
	resolved, err := s.resolver.Resolve(r.Context(), ociresolver.ResolveRequest{Image: body.Image, Platform: body.Platform})
	if err != nil {
		writeError(w, http.StatusBadRequest, "IMAGE_RESOLUTION_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resolveImageResponse{Image: resolved})
}
