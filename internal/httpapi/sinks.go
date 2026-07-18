package httpapi

import (
	"net/http"

	sinkspkg "github.com/benngarcia/mercator/internal/sinks"
)

func (s *Server) GetSinkStatus(w http.ResponseWriter, r *http.Request, sinkID string) {
	if s.sinks == nil {
		writeError(w, http.StatusNotImplemented, "SINKS_DISABLED", "Sink manager is not configured.")
		return
	}
	status, err := s.sinks.Status(r.Context(), sinkID)
	if err != nil {
		writeError(w, http.StatusNotFound, "SINK_NOT_FOUND", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) DeliverSink(w http.ResponseWriter, r *http.Request, sinkID string) {
	if s.sinks == nil {
		writeError(w, http.StatusNotImplemented, "SINKS_DISABLED", "Sink manager is not configured.")
		return
	}
	result, err := s.sinks.DeliverOnce(r.Context(), sinkID)
	if err != nil {
		writeInternalError(w, http.StatusBadGateway, "SINK_DELIVERY_FAILED", err)
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}

func (s *Server) ReplaySink(w http.ResponseWriter, r *http.Request, sinkID string) {
	if s.sinks == nil {
		writeError(w, http.StatusNotImplemented, "SINKS_DISABLED", "Sink manager is not configured.")
		return
	}
	var body replaySinkBody
	if !decodeJSONBody(w, r, &body) {
		return
	}
	result, err := s.sinks.Replay(r.Context(), sinkspkg.ReplayRequest{SinkID: sinkID, FromExclusive: body.FromExclusive, Limit: body.Limit, ReplayID: body.ReplayID})
	if err != nil {
		writeInternalError(w, http.StatusBadGateway, "SINK_REPLAY_FAILED", err)
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}
