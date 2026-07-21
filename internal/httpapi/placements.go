package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/benngarcia/mercator/internal/orchestrator"
)

func (s *Server) PreviewPlacement(ctx context.Context, request PreviewPlacementRequestObject) (PreviewPlacementResponseObject, error) {
	body := request.Body
	bodyWS := body.WorkspaceId
	if bodyWS == "" {
		bodyWS = body.Workload.WorkspaceID
	}
	workspaceID, workspaceErr := s.resolveWorkspace(ctx, bodyWS, request.Params.WorkspaceId)
	if workspaceErr != nil {
		if workspaceErr.Forbidden {
			return PreviewPlacement403JSONResponse(workspaceErr.Response), nil
		}
		return PreviewPlacement400JSONResponse(workspaceErr.Response), nil
	}
	decision, err := s.orch.PreviewPlacement(ctx, workspaceID, body.RunId, body.Workload)
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
