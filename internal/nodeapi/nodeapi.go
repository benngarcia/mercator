// Package nodeapi is the transport nodes use to reach the control plane.
//
// It is deliberately not part of the operator API contract: the audience,
// the credentials, and the shapes are different, and mixing them would let an
// operator token act as a node or a node credential reach operator surfaces.
//
// Every exchange is initiated by the node. It opens one long-lived session and
// reads commands as newline-delimited JSON, and posts its events and command
// results back on separate paths. Nothing here dials a node, and a node needs
// no inbound listener and no exposed container runtime socket.
package nodeapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/benngarcia/mercator/internal/capability"
	"github.com/benngarcia/mercator/internal/node"
)

// Registry is what this transport needs from the node registry.
type Registry interface {
	Enroll(ctx context.Context, request capability.EnrollmentRequest) (capability.Enrollment, error)
	OpenSession(ctx context.Context, nodeID, sessionToken string) (*node.Session, error)
	CloseSession(session *node.Session)
	RecordEvents(ctx context.Context, nodeID, sessionToken string, events []node.Event) error
	RecordResult(ctx context.Context, nodeID, sessionToken string, result node.Result) error
}

// maxBody bounds one enrollment, event batch, or result. Facts and inventories
// are the largest thing a node sends, and they are summaries rather than
// contents.
const maxBody = 1 << 20

// New mounts the node protocol. The returned handler expects to be reached at
// its own prefix and never shares authentication with the operator API.
func New(registry Registry) http.Handler {
	handler := &server{registry: registry, mux: http.NewServeMux()}
	handler.mux.HandleFunc("POST /v1/nodes/enroll", handler.enroll)
	handler.mux.HandleFunc("POST /v1/nodes/{node}/session", handler.session)
	handler.mux.HandleFunc("POST /v1/nodes/{node}/events", handler.events)
	handler.mux.HandleFunc("POST /v1/nodes/{node}/results", handler.results)
	return handler
}

type server struct {
	registry Registry
	mux      *http.ServeMux
}

func (handler *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	handler.mux.ServeHTTP(w, r)
}

// EnrollmentResponse is what a machine receives for a redeemed invitation.
type EnrollmentResponse struct {
	NodeID         string `json:"node_id"`
	SessionToken   string `json:"session_token"`
	SessionExpires string `json:"session_expires"`
	FencingToken   uint64 `json:"fencing_token"`
	LeaseExpires   string `json:"lease_expires"`
}

func (handler *server) enroll(w http.ResponseWriter, r *http.Request) {
	var request capability.EnrollmentRequest
	if !decode(w, r, &request) {
		return
	}
	enrollment, err := handler.registry.Enroll(r.Context(), request)
	if err != nil {
		// Enrollment failures are told apart by the caller's own logs, not by
		// the response: a machine presenting bad material learns only that it
		// was refused.
		writeError(w, http.StatusUnauthorized, "ENROLLMENT_REFUSED", "Enrollment was refused.")
		return
	}
	writeJSON(w, http.StatusOK, EnrollmentResponse{
		NodeID:         enrollment.NodeID,
		SessionToken:   enrollment.SessionToken,
		SessionExpires: enrollment.SessionExpires.UTC().Format(timeFormat),
		FencingToken:   enrollment.FencingToken,
		LeaseExpires:   enrollment.LeaseExpires.UTC().Format(timeFormat),
	})
}

// session streams commands to one node until the connection drops or the
// control plane supersedes the session. The node holds this open; the control
// plane writes down it.
func (handler *server) session(w http.ResponseWriter, r *http.Request) {
	nodeID, sessionToken, ok := credentials(w, r)
	if !ok {
		return
	}
	session, err := handler.registry.OpenSession(r.Context(), nodeID, sessionToken)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "SESSION_REFUSED", "Session was refused.")
		return
	}
	defer handler.registry.CloseSession(session)

	flusher, streamable := w.(http.Flusher)
	if !streamable {
		writeError(w, http.StatusInternalServerError, "STREAMING_UNSUPPORTED", "This server cannot stream node commands.")
		return
	}
	// A session is a long-lived read, so the server's ordinary write deadline
	// would cut a healthy node off on a schedule. Clearing it here keeps that
	// deadline protecting every other route.
	if err := http.NewResponseController(w).SetWriteDeadline(time.Time{}); err != nil {
		writeError(w, http.StatusInternalServerError, "STREAMING_UNSUPPORTED", "This server cannot hold a node session open.")
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	encoder := json.NewEncoder(w)
	for {
		select {
		case <-r.Context().Done():
			return
		case <-session.Done():
			return
		case command := <-session.Commands():
			if err := encoder.Encode(command); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// EventBatch is what a node posts, including a spool it accumulated while
// disconnected. Every event carries an ID, so replaying the spool changes
// nothing.
type EventBatch struct {
	Events []node.Event `json:"events"`
}

func (handler *server) events(w http.ResponseWriter, r *http.Request) {
	nodeID, sessionToken, ok := credentials(w, r)
	if !ok {
		return
	}
	var batch EventBatch
	if !decode(w, r, &batch) {
		return
	}
	if err := handler.registry.RecordEvents(r.Context(), nodeID, sessionToken, batch.Events); err != nil {
		writeError(w, statusForSession(err), "EVENTS_REJECTED", err.Error())
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (handler *server) results(w http.ResponseWriter, r *http.Request) {
	nodeID, sessionToken, ok := credentials(w, r)
	if !ok {
		return
	}
	var result node.Result
	if !decode(w, r, &result) {
		return
	}
	if err := handler.registry.RecordResult(r.Context(), nodeID, sessionToken, result); err != nil {
		writeError(w, statusForSession(err), "RESULT_REJECTED", err.Error())
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

const timeFormat = "2006-01-02T15:04:05.000000000Z07:00"

func credentials(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	nodeID := r.PathValue("node")
	token, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if nodeID == "" || !found || token == "" {
		writeError(w, http.StatusUnauthorized, "SESSION_REQUIRED", "A node session credential is required.")
		return "", "", false
	}
	return nodeID, token, true
}

func decode(w http.ResponseWriter, r *http.Request, into any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "BODY_TOO_LARGE", fmt.Sprintf("Request body exceeds %d bytes.", maxBody))
			return false
		}
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return false
	}
	return true
}

func statusForSession(err error) int {
	switch {
	case errors.Is(err, node.ErrNotFound):
		return http.StatusUnauthorized
	case errors.Is(err, node.ErrFenced):
		return http.StatusConflict
	default:
		return http.StatusBadRequest
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	writer := bufio.NewWriter(w)
	defer func() { _ = writer.Flush() }()
	_ = json.NewEncoder(writer).Encode(body)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"code": code, "message": message})
}
