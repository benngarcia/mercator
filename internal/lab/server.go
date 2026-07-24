package lab

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/broker"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/httpapi"
	"github.com/benngarcia/mercator/internal/workload"
)

const WorkspaceID = labWorkspace

type ServerConfig struct {
	Execution     Config
	OperatorToken string
	WebAuth       httpapi.WebAuth
}

type Server struct {
	execution  *Execution
	token      string
	handler    http.Handler
	http       *http.Server
	webAuth    httpapi.WebAuth
	production *handlerSlot

	mu           sync.Mutex
	shutdownOnce sync.Once
	shutdownErr  error
}

type driveRequest struct {
	Kind     string `json:"kind"`
	Duration string `json:"duration,omitempty"`
	Event    string `json:"event,omitempty"`
}

type statusResponse struct {
	Blueprint  string     `json:"blueprint"`
	Workspace  string     `json:"workspace_id"`
	Checkpoint Checkpoint `json:"checkpoint"`
}

func NewServer(ctx context.Context, config ServerConfig) (*Server, error) {
	if config.OperatorToken == "" {
		return nil, errors.New("Lab server operator token is required")
	}
	execution, err := Open(ctx, config.Execution)
	if err != nil {
		return nil, err
	}
	if execution.runtime == nil {
		_ = execution.Close()
		return nil, errors.New("Lab server requires an arrival-driven Blueprint")
	}
	server := &Server{
		execution: execution,
		token:     config.OperatorToken,
		webAuth:   config.WebAuth,
	}
	server.production = &handlerSlot{handler: server.productionHandler()}
	mux := http.NewServeMux()
	mux.Handle("GET /v1/lab/status", server.labOnly(http.HandlerFunc(server.status)))
	mux.Handle("POST /v1/lab/drive", server.labOnly(http.HandlerFunc(server.drive)))
	mux.Handle("POST /v1/lab/restart", server.labOnly(http.HandlerFunc(server.restart)))
	mux.Handle("GET /v1/lab/truth", server.labOnly(http.HandlerFunc(server.truth)))
	mux.Handle("GET /v1/lab/bundle", server.labOnly(http.HandlerFunc(server.bundle)))
	mux.Handle("/", server.production)
	server.handler = mux
	server.http = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      90 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	return server, nil
}

func (server *Server) productionHandler() http.Handler {
	options := []httpapi.Option{httpapi.WithBearerAuth(server.token)}
	if server.webAuth != nil {
		options = append(options, httpapi.WithWebAuth(server.webAuth))
	}
	return httpapi.New(httpapi.Deps{
		Orchestrator: server.execution.runtime.orchestrator,
		Offers:       labOfferAggregator{world: server.execution.runtime.world},
		Workloads:    workload.New(server.execution.runtime.storage.EventLog()),
		Workspaces:   server.execution.runtime.storage.Workspaces(),
		Events:       server.execution.runtime.storage.EventLog(),
	}, options...)
}

func (server *Server) Handler() http.Handler {
	return server.handler
}

func (server *Server) Serve(listener net.Listener) error {
	if listener == nil {
		return errors.New("Lab server listener is required")
	}
	return server.http.Serve(listener)
}

func (server *Server) Shutdown(ctx context.Context) error {
	server.shutdownOnce.Do(func() {
		server.shutdownErr = errors.Join(server.http.Shutdown(ctx), server.execution.Close())
	})
	return server.shutdownErr
}

func (server *Server) labOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		authorization := request.Header.Get("Authorization")
		if !strings.HasPrefix(authorization, "Bearer ") {
			writeLabError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Lab operator token is required.")
			return
		}
		presented := strings.TrimPrefix(authorization, "Bearer ")
		if presented == "" || subtle.ConstantTimeCompare([]byte(presented), []byte(server.token)) != 1 {
			writeLabError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Lab operator token is required.")
			return
		}
		request.Body = http.MaxBytesReader(w, request.Body, 1<<20)
		next.ServeHTTP(w, request)
	})
}

func (server *Server) status(w http.ResponseWriter, _ *http.Request) {
	server.mu.Lock()
	defer server.mu.Unlock()
	writeLabJSON(w, http.StatusOK, statusResponse{
		Blueprint:  server.execution.config.Blueprint.Name,
		Workspace:  WorkspaceID,
		Checkpoint: server.execution.checkpoint(),
	})
}

func (server *Server) drive(w http.ResponseWriter, request *http.Request) {
	command, err := decodeDriveRequest(request.Body)
	if err != nil {
		writeLabError(w, http.StatusBadRequest, "INVALID_DRIVE_COMMAND", err.Error())
		return
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	checkpoint, err := server.execution.Drive(request.Context(), command)
	if err != nil {
		writeLabError(w, http.StatusConflict, "DRIVE_FAILED", err.Error())
		return
	}
	writeLabJSON(w, http.StatusOK, checkpoint)
}

func (server *Server) restart(w http.ResponseWriter, request *http.Request) {
	server.mu.Lock()
	defer server.mu.Unlock()
	if err := server.execution.Restart(request.Context()); err != nil {
		writeLabError(w, http.StatusConflict, "RESTART_FAILED", err.Error())
		return
	}
	server.production.Replace(server.productionHandler())
	writeLabJSON(w, http.StatusOK, server.execution.checkpoint())
}

func (server *Server) truth(w http.ResponseWriter, _ *http.Request) {
	server.mu.Lock()
	defer server.mu.Unlock()
	writeLabJSON(w, http.StatusOK, server.execution.runtime.world.truthSnapshot())
}

func (server *Server) bundle(w http.ResponseWriter, request *http.Request) {
	server.mu.Lock()
	defer server.mu.Unlock()
	bundle, err := server.execution.Export(request.Context())
	if err != nil {
		writeLabError(w, http.StatusInternalServerError, "BUNDLE_EXPORT_FAILED", err.Error())
		return
	}
	data, err := bundle.Bytes()
	if err != nil {
		writeLabError(w, http.StatusInternalServerError, "BUNDLE_ENCODE_FAILED", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/vnd.mercator.lab+tar")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, server.execution.config.Blueprint.Name+".mlab"))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func decodeDriveRequest(body io.Reader) (DriveCommand, error) {
	var request driveRequest
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return DriveCommand{}, fmt.Errorf("decode command: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return DriveCommand{}, errors.New("drive command must contain one JSON document")
	}
	switch request.Kind {
	case "step":
		if request.Duration != "" || request.Event != "" {
			return DriveCommand{}, errors.New("step does not accept duration or event")
		}
		return Step(), nil
	case "advance":
		if request.Duration == "" || request.Event != "" {
			return DriveCommand{}, errors.New("advance requires duration and does not accept event")
		}
		duration, err := time.ParseDuration(request.Duration)
		if err != nil {
			return DriveCommand{}, fmt.Errorf("parse duration: %w", err)
		}
		return Advance(duration), nil
	case "until_event":
		if request.Event == "" || request.Duration != "" {
			return DriveCommand{}, errors.New("until_event requires event and does not accept duration")
		}
		return UntilEvent(request.Event), nil
	case "quiesce":
		if request.Duration != "" || request.Event != "" {
			return DriveCommand{}, errors.New("quiesce does not accept duration or event")
		}
		return Quiesce(), nil
	default:
		return DriveCommand{}, fmt.Errorf("unknown drive kind %q", request.Kind)
	}
}

func writeLabJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeLabError(w http.ResponseWriter, status int, code, message string) {
	writeLabJSON(w, status, map[string]string{"code": code, "message": message})
}

type labOfferAggregator struct {
	world *simulatedWorld
}

type handlerSlot struct {
	mu      sync.RWMutex
	handler http.Handler
}

func (slot *handlerSlot) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	slot.mu.RLock()
	handler := slot.handler
	slot.mu.RUnlock()
	handler.ServeHTTP(w, request)
}

func (slot *handlerSlot) Replace(handler http.Handler) {
	slot.mu.Lock()
	defer slot.mu.Unlock()
	slot.handler = handler
}

func (aggregator labOfferAggregator) AggregateOffers(ctx context.Context, request adapter.OfferRequest) (broker.OfferAggregation, error) {
	offers, err := aggregator.world.ListOffers(ctx, request)
	if offers == nil {
		offers = []domain.OfferSnapshot{}
	}
	return broker.OfferAggregation{
		Offers:   offers,
		Failures: broker.ConnectionErrors{},
	}, err
}
