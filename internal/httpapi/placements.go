package httpapi

import (
	"net/http"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/scheduler"
)

func (s *Server) PreviewPlacement(w http.ResponseWriter, r *http.Request) {
	var body placementPreviewBody
	if !decodeJSONBody(w, r, &body) {
		return
	}
	workspaceID := body.WorkspaceID
	if workspaceID == "" {
		workspaceID = body.Workload.WorkspaceID
	}
	if !s.authorizeRequestWorkspace(w, r, workspaceID) {
		return
	}
	if violations := domain.ValidateWorkloadRevision(body.Workload); len(violations) > 0 {
		writeError(w, http.StatusBadRequest, violations[0].Code, violations[0].Message)
		return
	}
	offers, err := s.adapter.ListOffers(r.Context(), adapter.OfferRequest{
		WorkspaceID: workspaceID,
		Resources:   body.Workload.Spec.Resources,
	})
	if err != nil {
		writeInternalError(w, http.StatusBadGateway, "OFFER_QUERY_FAILED", err)
		return
	}
	decision, err := s.scheduler.Evaluate(r.Context(), scheduler.SchedulingInput{
		RunID:        body.RunID,
		Workload:     body.Workload,
		Offers:       offers,
		ModelVersion: "latency-v1",
		EvaluatedAt:  time.Now().UTC(),
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "PLACEMENT_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, placementPreviewResponse{Decision: decision})
}
