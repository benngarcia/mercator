package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"runtime/debug"
	"strings"
	"sync/atomic"
)

const (
	storageEpoch = "single-scope-v1"
	apiEpoch     = "single-scope-v2"
)

var supportedClientEpochs = []string{"workspace-client-v1", "single-scope-client-v2"}
var compatibilityFeatures = []string{"legacy_workspace_selectors", "singular_decision"}
var buildRevisionOverride string

type legacyWorkspaceRoute struct {
	method        string
	pattern       string
	query         bool
	bodyPositions []string
	report        bool
}

var legacyWorkspaceRoutes = []legacyWorkspaceRoute{
	{method: http.MethodGet, pattern: "/v1/console/events", query: true},
	{method: http.MethodGet, pattern: "/v1/runs", query: true},
	{method: http.MethodPost, pattern: "/v1/runs", query: true, bodyPositions: []string{"top", "workload"}},
	{method: http.MethodGet, pattern: "/v1/runs/{run_id}", query: true},
	{method: http.MethodGet, pattern: "/v1/runs/{run_id}/wait", query: true},
	{method: http.MethodPost, pattern: "/v1/runs/{run_id}/refresh", query: true},
	{method: http.MethodPost, pattern: "/v1/runs/{run_id}/cancel", query: true},
	{method: http.MethodGet, pattern: "/v1/runs/{run_id}/events", query: true},
	{method: http.MethodGet, pattern: "/v1/runs/{run_id}/decision", query: true},
	{method: http.MethodPost, pattern: "/v1/runs/{run_id}/report", query: true, report: true},
	{method: http.MethodPost, pattern: "/v1/placements:preview", query: true, bodyPositions: []string{"top", "workload"}},
	{method: http.MethodGet, pattern: "/v1/connections", query: true},
	{method: http.MethodPost, pattern: "/v1/connections", query: true, bodyPositions: []string{"top"}},
	{method: http.MethodDelete, pattern: "/v1/connections/{connection_id}", query: true},
	{method: http.MethodPost, pattern: "/v1/connections/{connection_id}/authorize", query: true},
	{method: http.MethodGet, pattern: "/v1/offers", query: true},
	{method: http.MethodPost, pattern: "/v1/workloads", bodyPositions: []string{"top"}},
	{method: http.MethodGet, pattern: "/v1/workloads/{workload_id}/revisions", query: true},
	{method: http.MethodPost, pattern: "/v1/workloads/{workload_id}/revisions", query: true, bodyPositions: []string{"revision"}},
	{method: http.MethodGet, pattern: "/v1/workloads/{workload_id}/revisions/{revision_id}", query: true},
}

var legacyWorkspaceBridgeRequests atomic.Uint64

func (s *Server) bridgeLegacyWorkspaceRequest(w http.ResponseWriter, r *http.Request) bool {
	route := legacyWorkspaceRouteFor(r.Method, r.URL.Path)
	used, workspaceID, rejected := bridgeLegacyWorkspaceQuery(r, route)
	if rejected {
		writeRemovedWorkspaceSelector(w)
		return true
	}
	if route != nil && route.report && workspaceID != "" {
		s.bridgeLegacyReportToken(r, workspaceID)
	}
	bodyUsed, rejected := bridgeLegacyWorkspaceBody(w, r, route)
	if rejected {
		return true
	}
	s.recordLegacyWorkspaceBridge(w, r, used || bodyUsed)
	return false
}

func bridgeLegacyWorkspaceQuery(r *http.Request, route *legacyWorkspaceRoute) (bool, string, bool) {
	used := false
	workspaceID := ""
	query := r.URL.Query()
	for key := range query {
		if !isWorkspaceSelector(key) {
			continue
		}
		if key != "workspace_id" || route == nil || !route.query {
			return false, "", true
		}
		used = true
		workspaceID = query.Get(key)
		query.Del(key)
	}
	r.URL.RawQuery = query.Encode()
	return used, workspaceID, false
}

func bridgeLegacyWorkspaceBody(w http.ResponseWriter, r *http.Request, route *legacyWorkspaceRoute) (bool, bool) {
	if r.Body == nil {
		return false, false
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "BODY_TOO_LARGE", "Request body exceeds the 1 MiB limit.")
		} else {
			writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "Request body could not be read.")
		}
		return false, true
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	if len(bytes.TrimSpace(body)) == 0 {
		return false, false
	}
	var value map[string]json.RawMessage
	if json.Unmarshal(body, &value) != nil {
		return false, false
	}
	used, allowed := stripLegacyWorkspaceSelectors(route, value)
	if !allowed {
		writeRemovedWorkspaceSelector(w)
		return false, true
	}
	if used {
		body, _ = json.Marshal(value)
		r.Body = io.NopCloser(bytes.NewReader(body))
	}
	return used, false
}

func stripLegacyWorkspaceSelectors(route *legacyWorkspaceRoute, value map[string]json.RawMessage) (bool, bool) {
	used := false
	workspaceIDs := []string{}
	for key := range value {
		if !isWorkspaceSelector(key) {
			continue
		}
		if key != "workspace_id" || !routeAllowsBody(route, "top") {
			return false, false
		}
		workspaceID, ok := decodeLegacyWorkspaceID(value[key])
		if !ok {
			return false, false
		}
		workspaceIDs = append(workspaceIDs, workspaceID)
		delete(value, key)
		used = true
	}
	for _, position := range []string{"workload", "revision"} {
		encoded, present := value[position]
		if !present {
			continue
		}
		var nested map[string]json.RawMessage
		if json.Unmarshal(encoded, &nested) != nil {
			continue
		}
		changed := false
		for nestedKey := range nested {
			if !isWorkspaceSelector(nestedKey) {
				continue
			}
			if nestedKey != "workspace_id" || !routeAllowsBody(route, position) {
				return false, false
			}
			workspaceID, ok := decodeLegacyWorkspaceID(nested[nestedKey])
			if !ok {
				return false, false
			}
			workspaceIDs = append(workspaceIDs, workspaceID)
			delete(nested, nestedKey)
			changed = true
			used = true
		}
		if changed {
			value[position], _ = json.Marshal(nested)
		}
	}
	if len(workspaceIDs) > 1 {
		for _, workspaceID := range workspaceIDs[1:] {
			if workspaceID != workspaceIDs[0] {
				return false, false
			}
		}
	}
	return used, true
}

func decodeLegacyWorkspaceID(raw json.RawMessage) (string, bool) {
	var workspaceID string
	if json.Unmarshal(raw, &workspaceID) != nil || workspaceID == "" {
		return "", false
	}
	return workspaceID, true
}

func legacyWorkspaceRouteFor(method, path string) *legacyWorkspaceRoute {
	for index := range legacyWorkspaceRoutes {
		route := &legacyWorkspaceRoutes[index]
		if route.method == method && legacyPathMatches(route.pattern, path) {
			return route
		}
	}
	return nil
}

func legacyPathMatches(pattern, path string) bool {
	want := strings.Split(strings.TrimPrefix(pattern, "/"), "/")
	got := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(want) != len(got) {
		return false
	}
	for index := range want {
		if strings.HasPrefix(want[index], "{") && strings.HasSuffix(want[index], "}") {
			if got[index] == "" {
				return false
			}
			continue
		}
		if want[index] != got[index] {
			return false
		}
	}
	return true
}

func routeAllowsBody(route *legacyWorkspaceRoute, position string) bool {
	if route == nil {
		return false
	}
	for _, allowed := range route.bodyPositions {
		if allowed == position {
			return true
		}
	}
	return false
}

func (s *Server) bridgeLegacyReportToken(r *http.Request, workspaceID string) {
	if s.reportSigner == nil {
		return
	}
	runID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/runs/"), "/report")
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if s.reportSigner.VerifyLegacy(workspaceID, runID, token) {
		r.Header.Set("Authorization", "Bearer "+s.reportSigner.Token(runID))
	}
}

func (s *Server) recordLegacyWorkspaceBridge(w http.ResponseWriter, r *http.Request, used bool) {
	if !used {
		return
	}
	w.Header().Set("Deprecation", "true")
	count := legacyWorkspaceBridgeRequests.Add(1)
	if count&(count-1) == 0 {
		log.Printf("httpapi: legacy workspace selector bridge used count=%d method=%s path=%s", count, r.Method, r.URL.Path)
	}
}

func writeRemovedWorkspaceSelector(w http.ResponseWriter) {
	writeError(w, http.StatusBadRequest, "REMOVED_WORKSPACE_SELECTOR", "Mercator no longer accepts a workspace selector; address the deployment directly.")
}

func isWorkspaceSelector(key string) bool {
	key = strings.ToLower(strings.ReplaceAll(key, "-", "_"))
	return key == "workspace" || key == "workspace_id" || key == "workspaceid"
}

func buildRevision() string {
	if buildRevisionOverride != "" {
		return buildRevisionOverride
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "development"
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" && setting.Value != "" {
			return setting.Value
		}
	}
	if info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "development"
}
