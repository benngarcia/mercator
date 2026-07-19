package httpapi

import (
	"context"
	"net/http"

	"github.com/benngarcia/mercator/internal/eventlog"
	sinkspkg "github.com/benngarcia/mercator/internal/sinks"
)

func (s *Server) GetSinkStatus(ctx context.Context, request GetSinkStatusRequestObject) (GetSinkStatusResponseObject, error) {
	if s.sinks == nil {
		return GetSinkStatus501JSONResponse(apiError("SINKS_DISABLED", "Sink manager is not configured.")), nil
	}
	status, err := s.sinks.Status(ctx, request.SinkId)
	if err != nil {
		return GetSinkStatus404JSONResponse(apiError("SINK_NOT_FOUND", err.Error())), nil
	}
	return GetSinkStatus200JSONResponse(status), nil
}

func (s *Server) DeliverSink(ctx context.Context, request DeliverSinkRequestObject) (DeliverSinkResponseObject, error) {
	if s.sinks == nil {
		return DeliverSink501JSONResponse(apiError("SINKS_DISABLED", "Sink manager is not configured.")), nil
	}
	result, err := s.sinks.DeliverOnce(ctx, request.SinkId)
	if err != nil {
		return DeliverSink502JSONResponse(internalAPIError(http.StatusBadGateway, "SINK_DELIVERY_FAILED", err)), nil
	}
	return DeliverSink202JSONResponse(result), nil
}

func (s *Server) ReplaySink(ctx context.Context, request ReplaySinkRequestObject) (ReplaySinkResponseObject, error) {
	if s.sinks == nil {
		return ReplaySink501JSONResponse(apiError("SINKS_DISABLED", "Sink manager is not configured.")), nil
	}
	body := request.Body
	result, err := s.sinks.Replay(ctx, sinkspkg.ReplayRequest{
		SinkID:        request.SinkId,
		FromExclusive: eventlog.GlobalPosition(body.FromExclusive),
		Limit:         body.Limit,
		ReplayID:      body.ReplayId,
	})
	if err != nil {
		return ReplaySink502JSONResponse(internalAPIError(http.StatusBadGateway, "SINK_REPLAY_FAILED", err)), nil
	}
	return ReplaySink202JSONResponse(result), nil
}
