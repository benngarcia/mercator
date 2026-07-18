package httpapi

import (
	"net/http"

	"github.com/benngarcia/mercator/internal/adapter"
)

// ListAdapters serves the registered adapters' onboarding manifests. The list
// is static per process and carries no workspace state, but it sits behind the
// same /v1 auth gate as everything else.
func (s *Server) ListAdapters(w http.ResponseWriter, _ *http.Request) {
	if s.manifests == nil {
		writeJSON(w, http.StatusOK, adapterListResponse{Adapters: []adapter.Manifest{}})
		return
	}
	manifests := s.manifests()
	if manifests == nil {
		manifests = []adapter.Manifest{}
	}
	writeJSON(w, http.StatusOK, adapterListResponse{Adapters: manifests})
}

func (s *Server) ListOffers(w http.ResponseWriter, r *http.Request, _ ListOffersParams) {
	workspaceID, ok := s.requiredWorkspace(w, r)
	if !ok {
		return
	}
	records, err := s.adapter.ListOffers(r.Context(), adapter.OfferRequest{WorkspaceID: workspaceID})
	if err != nil {
		writeInternalError(w, http.StatusBadGateway, "LIST_OFFERS_FAILED", err)
		return
	}
	writeJSON(w, http.StatusOK, offerListResponse{Offers: records})
}
