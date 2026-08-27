package httpapi

import (
	"context"
	"errors"
	"log"
	"net/http"

	"github.com/benngarcia/mercator/internal/connection"
	"github.com/benngarcia/mercator/internal/eventlog"
)

func (s *Server) CreateConnection(ctx context.Context, request CreateConnectionRequestObject) (CreateConnectionResponseObject, error) {
	if s.conns == nil {
		return CreateConnection501JSONResponse(apiError("CONNECTION_SERVICE_DISABLED", "Connection service is not configured.")), nil
	}
	body := request.Body
	record, err := s.conns.Create(ctx, connection.CreateRequest{

		ConnectionID: body.ConnectionId,
		AdapterType:  body.AdapterType,
		Config:       body.Config,
		Credential:   body.Credential,
		Secret:       []byte(body.Secret),
		Actor:        requestActor(ctx),
	})
	if err != nil {
		if errors.Is(err, eventlog.ErrIdempotencyConflict) {
			return CreateConnection409JSONResponse(apiError("IDEMPOTENCY_CONFLICT", "Idempotency key was reused with a different request hash.")), nil
		}
		if errors.Is(err, connection.ErrSecretStore) {
			return CreateConnection500JSONResponse(internalAPIError(http.StatusInternalServerError, "SECRET_STORE_FAILED", err)), nil
		}
		if errors.Is(err, connection.ErrSecretStoreDisabled) {
			return CreateConnection400JSONResponse(apiError("SECRET_STORE_DISABLED", err.Error())), nil
		}
		return CreateConnection400JSONResponse(apiError(errorCode(err, "CREATE_CONNECTION_FAILED"), errorMessage(err))), nil
	}
	return CreateConnection201JSONResponse{Connection: record}, nil
}

func (s *Server) AuthorizeConnection(ctx context.Context, request AuthorizeConnectionRequestObject) (AuthorizeConnectionResponseObject, error) {
	if s.conns == nil {
		return AuthorizeConnection501JSONResponse(apiError("CONNECTION_SERVICE_DISABLED", "Connection service is not configured.")), nil
	}
	if s.verifier == nil {
		return AuthorizeConnection501JSONResponse(apiError("CONNECTION_VERIFY_DISABLED", "Connection verification is not configured.")), nil
	}
	if err := s.verifier.VerifyConnection(ctx, request.ConnectionId); err != nil {
		// The adapter's own error text is the operator's diagnostic (a provider
		// 401, an unreachable daemon): return it verbatim rather than the
		// generic internal-error message. Still logged server-side.
		log.Printf("httpapi: 502 CONNECTION_VERIFY_FAILED: %v", err)
		return AuthorizeConnection502JSONResponse(apiError("CONNECTION_VERIFY_FAILED", errorMessage(err))), nil
	}
	if err := s.conns.UpdateAuthorization(ctx, connection.UpdateAuthorizationRequest{

		ConnectionID: request.ConnectionId,
		Authorized:   true,
		Actor:        requestActor(ctx),
	}); err != nil {
		return AuthorizeConnection500JSONResponse(internalAPIError(http.StatusInternalServerError, "CONNECTION_AUTHORIZE_FAILED", err)), nil
	}
	record, err := s.conns.Get(ctx, request.ConnectionId)
	if err != nil {
		return AuthorizeConnection500JSONResponse(internalAPIError(http.StatusInternalServerError, "CONNECTION_NOT_FOUND", err)), nil
	}
	return AuthorizeConnection200JSONResponse{Connection: record}, nil
}

// DeleteConnection appends the deleted fact. The event stream itself is
// retained; the connection service removes sealed credentials atomically.
func (s *Server) DeleteConnection(ctx context.Context, request DeleteConnectionRequestObject) (DeleteConnectionResponseObject, error) {
	if s.conns == nil {
		return DeleteConnection501JSONResponse(apiError("CONNECTION_SERVICE_DISABLED", "Connection service is not configured.")), nil
	}
	if err := s.conns.Delete(ctx, connection.DeleteRequest{

		ConnectionID: request.ConnectionId,
		Actor:        requestActor(ctx),
	}); err != nil {
		if errors.Is(err, connection.ErrNotFound) {
			return DeleteConnection404JSONResponse(apiError("CONNECTION_NOT_FOUND", "Connection not found.")), nil
		}
		return DeleteConnection500JSONResponse(internalAPIError(http.StatusInternalServerError, "CONNECTION_DELETE_FAILED", err)), nil
	}
	return DeleteConnection200JSONResponse{Deleted: true}, nil
}

func (s *Server) ListConnections(ctx context.Context, request ListConnectionsRequestObject) (ListConnectionsResponseObject, error) {
	if s.conns == nil {
		return ListConnections200JSONResponse{Connections: []connection.Record{}}, nil
	}
	records, err := s.conns.List(ctx)
	if err != nil {
		return ListConnections500JSONResponse(internalAPIError(http.StatusInternalServerError, "LIST_CONNECTIONS_FAILED", err)), nil
	}
	if records == nil {
		records = []connection.Record{}
	}
	return ListConnections200JSONResponse{Connections: records}, nil
}
