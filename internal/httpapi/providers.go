package httpapi

import (
	"context"

	"github.com/benngarcia/mercator/internal/adapter"
)

// ListAdapters serves the registered adapters' onboarding manifests. The list
// is static per process and carries no workspace state, but it sits behind the
// same /v1 auth gate as everything else.
func (s *Server) ListAdapters(context.Context, ListAdaptersRequestObject) (ListAdaptersResponseObject, error) {
	if s.manifests == nil {
		return ListAdapters200JSONResponse{Adapters: []adapter.Manifest{}}, nil
	}
	manifests := s.manifests()
	if manifests == nil {
		manifests = []adapter.Manifest{}
	}
	return ListAdapters200JSONResponse{Adapters: manifests}, nil
}

func (s *Server) ListOffers(ctx context.Context, request ListOffersRequestObject) (ListOffersResponseObject, error) {
	workspaceID, workspaceErr := s.requiredWorkspace(ctx, request.Params.WorkspaceId)
	if workspaceErr != nil {
		if workspaceErr.Forbidden {
			return ListOffers403JSONResponse(workspaceErr.Response), nil
		}
		return ListOffers400JSONResponse(workspaceErr.Response), nil
	}
	records, err := s.adapter.ListOffers(ctx, adapter.OfferRequest{WorkspaceID: workspaceID})
	if err != nil {
		return ListOffers502JSONResponse(internalAPIError(502, "LIST_OFFERS_FAILED", err)), nil
	}
	return ListOffers200JSONResponse{Offers: records}, nil
}
