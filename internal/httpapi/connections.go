package httpapi

import (
	"context"
	"errors"
	"log"
	"net/http"

	"github.com/benngarcia/mercator/internal/connection"
	"github.com/benngarcia/mercator/internal/credential"
	"github.com/benngarcia/mercator/internal/eventlog"
)

func (s *Server) CreateConnection(ctx context.Context, request CreateConnectionRequestObject) (CreateConnectionResponseObject, error) {
	if s.conns == nil {
		return CreateConnection501JSONResponse(apiError("CONNECTION_SERVICE_DISABLED", "Connection service is not configured.")), nil
	}
	body := request.Body
	workspaceID, workspaceErr := s.resolveWorkspace(ctx, body.WorkspaceId, request.Params.WorkspaceId)
	if workspaceErr != nil {
		if workspaceErr.Forbidden {
			return CreateConnection403JSONResponse(workspaceErr.Response), nil
		}
		return CreateConnection400JSONResponse(workspaceErr.Response), nil
	}

	cred := body.Credential
	// For mercator-source connections with a provided secret, seal the secret
	// out-of-band and store the ciphertext in the secret store. The plaintext
	// never enters the event log or the response.
	if cred.Source == credential.SourceMercator && body.Secret != "" {
		if s.credentials == nil || s.secretStore == nil {
			return CreateConnection400JSONResponse(apiError("SECRET_STORE_DISABLED", "Secret store is not configured; cannot accept mercator credentials.")), nil
		}
		blob, ok := s.credentials.Seal([]byte(body.Secret))
		if !ok {
			return CreateConnection400JSONResponse(apiError("SECRET_STORE_DISABLED", "Master key is not set; cannot seal mercator credentials.")), nil
		}
		if err := s.secretStore.Put(ctx, workspaceID, body.ConnectionId, blob); err != nil {
			return CreateConnection500JSONResponse(internalAPIError(http.StatusInternalServerError, "SECRET_STORE_FAILED", err)), nil
		}
		cred.Ref = body.ConnectionId
	}

	record, err := s.conns.Create(ctx, connection.CreateRequest{
		WorkspaceID:  workspaceID,
		ConnectionID: body.ConnectionId,
		AdapterType:  body.AdapterType,
		Config:       body.Config,
		Credential:   cred,
		Actor:        requestActor(ctx),
	})
	if err != nil {
		if errors.Is(err, eventlog.ErrIdempotencyConflict) {
			return CreateConnection409JSONResponse(apiError("IDEMPOTENCY_CONFLICT", "Idempotency key was reused with a different request hash.")), nil
		}
		return CreateConnection400JSONResponse(apiError(errorCode(err, "CREATE_CONNECTION_FAILED"), errorMessage(err))), nil
	}
	return CreateConnection201JSONResponse{Connection: record}, nil
}

func (s *Server) AuthorizeConnection(ctx context.Context, request AuthorizeConnectionRequestObject) (AuthorizeConnectionResponseObject, error) {
	if s.conns == nil {
		return AuthorizeConnection501JSONResponse(apiError("CONNECTION_SERVICE_DISABLED", "Connection service is not configured.")), nil
	}
	workspaceID, workspaceErr := s.requiredWorkspace(ctx, request.Params.WorkspaceId)
	if workspaceErr != nil {
		if workspaceErr.Forbidden {
			return AuthorizeConnection403JSONResponse(workspaceErr.Response), nil
		}
		return AuthorizeConnection400JSONResponse(workspaceErr.Response), nil
	}
	if s.verifier == nil {
		return AuthorizeConnection501JSONResponse(apiError("CONNECTION_VERIFY_DISABLED", "Connection verification is not configured.")), nil
	}
	if err := s.verifier.VerifyConnection(ctx, workspaceID, request.ConnectionId); err != nil {
		// The adapter's own error text is the operator's diagnostic (a provider
		// 401, an unreachable daemon): return it verbatim rather than the
		// generic internal-error message. Still logged server-side.
		log.Printf("httpapi: 502 CONNECTION_VERIFY_FAILED: %v", err)
		return AuthorizeConnection502JSONResponse(apiError("CONNECTION_VERIFY_FAILED", errorMessage(err))), nil
	}
	if err := s.conns.UpdateAuthorization(ctx, connection.UpdateAuthorizationRequest{
		WorkspaceID:  workspaceID,
		ConnectionID: request.ConnectionId,
		Authorized:   true,
		Actor:        requestActor(ctx),
	}); err != nil {
		return AuthorizeConnection500JSONResponse(internalAPIError(http.StatusInternalServerError, "CONNECTION_AUTHORIZE_FAILED", err)), nil
	}
	record, err := s.conns.Get(ctx, workspaceID, request.ConnectionId)
	if err != nil {
		return AuthorizeConnection500JSONResponse(internalAPIError(http.StatusInternalServerError, "CONNECTION_NOT_FOUND", err)), nil
	}
	return AuthorizeConnection200JSONResponse{Connection: record}, nil
}

// deleteConnection appends the deleted fact and removes the sealed credential
// blob. The event stream itself is retained (append-only log); the id cannot
// be reused — recreating means a fresh connection id.
func (s *Server) DeleteConnection(ctx context.Context, request DeleteConnectionRequestObject) (DeleteConnectionResponseObject, error) {
	if s.conns == nil {
		return DeleteConnection501JSONResponse(apiError("CONNECTION_SERVICE_DISABLED", "Connection service is not configured.")), nil
	}
	workspaceID, workspaceErr := s.requiredWorkspace(ctx, request.Params.WorkspaceId)
	if workspaceErr != nil {
		if workspaceErr.Forbidden {
			return DeleteConnection403JSONResponse(workspaceErr.Response), nil
		}
		return DeleteConnection400JSONResponse(workspaceErr.Response), nil
	}
	if err := s.conns.Delete(ctx, connection.DeleteRequest{
		WorkspaceID:  workspaceID,
		ConnectionID: request.ConnectionId,
		Actor:        requestActor(ctx),
	}); err != nil {
		if errors.Is(err, connection.ErrNotFound) {
			return DeleteConnection404JSONResponse(apiError("CONNECTION_NOT_FOUND", "Connection not found.")), nil
		}
		return DeleteConnection500JSONResponse(internalAPIError(http.StatusInternalServerError, "CONNECTION_DELETE_FAILED", err)), nil
	}
	// The blob is unreachable once the record is deleted; failing to remove it
	// must not resurrect the connection. Retrying the (idempotent) delete
	// re-attempts the removal.
	if s.secretStore != nil {
		if err := s.secretStore.Delete(ctx, workspaceID, request.ConnectionId); err != nil {
			return DeleteConnection500JSONResponse(internalAPIError(http.StatusInternalServerError, "SECRET_STORE_FAILED", err)), nil
		}
	}
	return DeleteConnection200JSONResponse{Deleted: true}, nil
}

func (s *Server) ListConnections(ctx context.Context, request ListConnectionsRequestObject) (ListConnectionsResponseObject, error) {
	workspaceID, workspaceErr := s.requiredWorkspace(ctx, request.Params.WorkspaceId)
	if workspaceErr != nil {
		if workspaceErr.Forbidden {
			return ListConnections403JSONResponse(workspaceErr.Response), nil
		}
		return ListConnections400JSONResponse(workspaceErr.Response), nil
	}
	if s.conns == nil {
		return ListConnections200JSONResponse{Connections: []connection.Record{}}, nil
	}
	records, err := s.conns.List(ctx, workspaceID)
	if err != nil {
		return ListConnections500JSONResponse(internalAPIError(http.StatusInternalServerError, "LIST_CONNECTIONS_FAILED", err)), nil
	}
	if records == nil {
		records = []connection.Record{}
	}
	return ListConnections200JSONResponse{Connections: records}, nil
}
