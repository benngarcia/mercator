package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/benngarcia/mercator/internal/orchestrator"
)

func (s *Server) PreviewPlacement(ctx context.Context, request PreviewPlacementRequestObject) (PreviewPlacementResponseObject, error) {
	body := request.Body
	decision, err := s.orch.PreviewPlacement(ctx, body.RunId, body.Workload)
	if err != nil {
		if errors.Is(err, orchestrator.ErrOfferQuery) {
			return PreviewPlacement502JSONResponse(internalAPIError(http.StatusBadGateway, "OFFER_QUERY_FAILED", err)), nil
		}
		var verr *orchestrator.ValidationError
		if errors.As(err, &verr) && len(verr.Violations) > 0 {
			return PreviewPlacement400JSONResponse(apiErrorWithDetails(verr.Violations[0].Code, verr.Violations[0].Message, verr.Violations)), nil
		}
		return PreviewPlacement400JSONResponse(apiError(errorCode(err, "PLACEMENT_FAILED"), errorMessage(err))), nil
	}
	return PreviewPlacement200JSONResponse{Decision: decision}, nil
}
