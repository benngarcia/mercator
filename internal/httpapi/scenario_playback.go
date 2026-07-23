package httpapi

import (
	"context"
	"errors"

	"github.com/benngarcia/mercator/internal/scenario"
)

func (s *Server) CommandScenarioPlayback(_ context.Context, request CommandScenarioPlaybackRequestObject) (CommandScenarioPlaybackResponseObject, error) {
	if s.scenarios == nil {
		return CommandScenarioPlayback501JSONResponse(apiError("DASHBOARD_SCENARIOS_DISABLED", "Dashboard scenarios are available only from the local development server.")), nil
	}
	if request.Body == nil {
		return CommandScenarioPlayback400JSONResponse(apiError("INVALID_SCENARIO_COMMAND", "Scenario command body is required.")), nil
	}
	err := s.scenarios.Command(request.WorkspaceId, scenario.DashboardCommand{
		Type:  string(request.Body.Type),
		Speed: int(request.Body.Speed),
	})
	switch {
	case err == nil:
		return CommandScenarioPlayback200JSONResponse{Accepted: true}, nil
	case errors.Is(err, scenario.ErrDashboardPlaybackNotFound):
		return CommandScenarioPlayback404JSONResponse(apiError("SCENARIO_SESSION_NOT_FOUND", err.Error())), nil
	case errors.Is(err, scenario.ErrInvalidDashboardCommand):
		return CommandScenarioPlayback400JSONResponse(apiError("INVALID_SCENARIO_COMMAND", err.Error())), nil
	default:
		return nil, err
	}
}
