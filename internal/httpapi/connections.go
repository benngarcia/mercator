package httpapi

import (
	"errors"
	"log"
	"net/http"

	"github.com/benngarcia/mercator/internal/connection"
	"github.com/benngarcia/mercator/internal/credential"
	"github.com/benngarcia/mercator/internal/eventlog"
)

func (s *Server) CreateConnection(w http.ResponseWriter, r *http.Request, _ CreateConnectionParams) {
	if s.conns == nil {
		writeError(w, http.StatusNotImplemented, "CONNECTION_SERVICE_DISABLED", "Connection service is not configured.")
		return
	}
	if r.Header.Get("Idempotency-Key") == "" {
		writeError(w, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "Mutation requests require Idempotency-Key.")
		return
	}
	var body createConnectionBody
	if !decodeJSONBody(w, r, &body) {
		return
	}
	workspaceID := body.WorkspaceID
	if workspaceID == "" {
		workspaceID = r.URL.Query().Get("workspace_id")
	}
	if workspaceID == "" {
		workspaceID = s.defaultWorkspace()
	}
	if !s.authorizeRequestWorkspace(w, r, workspaceID) {
		return
	}

	cred := body.Credential
	// For mercator-source connections with a provided secret, seal the secret
	// out-of-band and store the ciphertext in the secret store. The plaintext
	// never enters the event log or the response.
	if cred.Source == credential.SourceMercator && body.Secret != "" {
		if s.credentials == nil || s.secretStore == nil {
			writeError(w, http.StatusBadRequest, "SECRET_STORE_DISABLED", "Secret store is not configured; cannot accept mercator credentials.")
			return
		}
		blob, ok := s.credentials.Seal([]byte(body.Secret))
		if !ok {
			writeError(w, http.StatusBadRequest, "SECRET_STORE_DISABLED", "Master key is not set; cannot seal mercator credentials.")
			return
		}
		if err := s.secretStore.Put(r.Context(), workspaceID, body.ConnectionID, blob); err != nil {
			writeInternalError(w, http.StatusInternalServerError, "SECRET_STORE_FAILED", err)
			return
		}
		cred.Ref = body.ConnectionID
	}

	record, err := s.conns.Create(r.Context(), connection.CreateRequest{
		WorkspaceID:  workspaceID,
		ConnectionID: body.ConnectionID,
		AdapterType:  body.AdapterType,
		Config:       body.Config,
		Credential:   cred,
		Actor:        requestActor(r),
	})
	if err != nil {
		if errors.Is(err, eventlog.ErrIdempotencyConflict) {
			writeError(w, http.StatusConflict, "IDEMPOTENCY_CONFLICT", "Idempotency key was reused with a different request hash.")
			return
		}
		writeError(w, http.StatusBadRequest, errorCode(err, "CREATE_CONNECTION_FAILED"), errorMessage(err))
		return
	}
	writeJSON(w, http.StatusCreated, connectionResponse{Connection: record})
}

func (s *Server) AuthorizeConnection(w http.ResponseWriter, r *http.Request, id string, _ AuthorizeConnectionParams) {
	if s.conns == nil {
		writeError(w, http.StatusNotImplemented, "CONNECTION_SERVICE_DISABLED", "Connection service is not configured.")
		return
	}
	workspaceID, ok := s.requiredWorkspace(w, r)
	if !ok {
		return
	}
	if s.verifier == nil {
		writeError(w, http.StatusNotImplemented, "CONNECTION_VERIFY_DISABLED", "Connection verification is not configured.")
		return
	}
	if err := s.verifier.VerifyConnection(r.Context(), workspaceID, id); err != nil {
		// The adapter's own error text is the operator's diagnostic (a provider
		// 401, an unreachable daemon): return it verbatim rather than the
		// generic internal-error message. Still logged server-side.
		log.Printf("httpapi: 502 CONNECTION_VERIFY_FAILED: %v", err)
		writeError(w, http.StatusBadGateway, "CONNECTION_VERIFY_FAILED", errorMessage(err))
		return
	}
	if err := s.conns.UpdateAuthorization(r.Context(), connection.UpdateAuthorizationRequest{
		WorkspaceID:  workspaceID,
		ConnectionID: id,
		Authorized:   true,
		Actor:        requestActor(r),
	}); err != nil {
		writeInternalError(w, http.StatusInternalServerError, "CONNECTION_AUTHORIZE_FAILED", err)
		return
	}
	record, err := s.conns.Get(r.Context(), workspaceID, id)
	if err != nil {
		writeInternalError(w, http.StatusInternalServerError, "CONNECTION_NOT_FOUND", err)
		return
	}
	writeJSON(w, http.StatusOK, connectionResponse{Connection: record})
}

// deleteConnection appends the deleted fact and removes the sealed credential
// blob. The event stream itself is retained (append-only log); the id cannot
// be reused — recreating means a fresh connection id.
func (s *Server) DeleteConnection(w http.ResponseWriter, r *http.Request, id string, _ DeleteConnectionParams) {
	if s.conns == nil {
		writeError(w, http.StatusNotImplemented, "CONNECTION_SERVICE_DISABLED", "Connection service is not configured.")
		return
	}
	workspaceID, ok := s.requiredWorkspace(w, r)
	if !ok {
		return
	}
	if err := s.conns.Delete(r.Context(), connection.DeleteRequest{
		WorkspaceID:  workspaceID,
		ConnectionID: id,
		Actor:        requestActor(r),
	}); err != nil {
		if errors.Is(err, connection.ErrNotFound) {
			writeError(w, http.StatusNotFound, "CONNECTION_NOT_FOUND", "Connection not found.")
			return
		}
		writeInternalError(w, http.StatusInternalServerError, "CONNECTION_DELETE_FAILED", err)
		return
	}
	// The blob is unreachable once the record is deleted; failing to remove it
	// must not resurrect the connection. Retrying the (idempotent) delete
	// re-attempts the removal.
	if s.secretStore != nil {
		if err := s.secretStore.Delete(r.Context(), workspaceID, id); err != nil {
			writeInternalError(w, http.StatusInternalServerError, "SECRET_STORE_FAILED", err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (s *Server) ListConnections(w http.ResponseWriter, r *http.Request, _ ListConnectionsParams) {
	workspaceID, ok := s.requiredWorkspace(w, r)
	if !ok {
		return
	}
	if s.conns == nil {
		writeJSON(w, http.StatusOK, connectionListResponse{Connections: []connection.Record{}})
		return
	}
	records, err := s.conns.List(r.Context(), workspaceID)
	if err != nil {
		writeInternalError(w, http.StatusInternalServerError, "LIST_CONNECTIONS_FAILED", err)
		return
	}
	if records == nil {
		records = []connection.Record{}
	}
	writeJSON(w, http.StatusOK, connectionListResponse{Connections: records})
}
