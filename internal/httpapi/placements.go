package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/scheduler"
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
	if violations := domain.ValidateWorkloadRevision(body.Workload); len(violations) > 0 {
		return PreviewPlacement400JSONResponse(apiErrorWithDetails(violations[0].Code, violations[0].Message, violations)), nil
	}
	offers, err := s.adapter.ListOffers(ctx, adapter.OfferRequest{
		WorkspaceID: workspaceID,
		Resources:   body.Workload.Spec.Resources,
	})
	if err != nil {
		return PreviewPlacement502JSONResponse(internalAPIError(http.StatusBadGateway, "OFFER_QUERY_FAILED", err)), nil
	}
	decision, err := s.scheduler.Evaluate(ctx, scheduler.SchedulingInput{
		RunID:        body.RunId,
		Workload:     body.Workload,
		Offers:       offers,
		ModelVersion: "latency-v1",
		EvaluatedAt:  time.Now().UTC(),
	})
	if err != nil {
		return PreviewPlacement400JSONResponse(apiError("PLACEMENT_FAILED", err.Error())), nil
	}
	return PreviewPlacement200JSONResponse{Decision: decision}, nil
}
