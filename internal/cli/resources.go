package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// configFlag collects repeated --config k=v pairs into a map.
type configFlag map[string]string

func (c configFlag) String() string { return "" }

func (c configFlag) Set(value string) error {
	key, val, found := strings.Cut(value, "=")
	if !found || key == "" {
		return fmt.Errorf("--config expects key=value, got %q", value)
	}
	c[key] = val
	return nil
}

// buildConnectionRequest implements `mercator connection <list|create|authorize|delete>`,
// covering the /v1/connections surface that previously required hand-written
// curl with Idempotency-Key headers.
func buildConnectionRequest(ctx context.Context, s *session, stdin io.Reader, args []string) (*http.Request, error) {
	command := args[0]
	baseURL := s.baseURL
	fs := flag.NewFlagSet("connection "+command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	workspaceID := fs.String("workspace-id", "", "workspace id (defaults to the broker's only workspace)")
	connectionID := fs.String("connection-id", "", "connection id (defaults to the adapter type on create, otherwise the workspace's only connection)")
	adapterType := fs.String("adapter-type", "", "adapter type (docker, runpod)")
	credentialSource := fs.String("credential-source", "", "credential source (env, mercator)")
	credentialRef := fs.String("credential-ref", "", "credential reference (e.g. env var name)")
	secret := fs.String("secret", "", "secret for mercator-source credentials (prefer --secret-stdin)")
	secretStdin := fs.Bool("secret-stdin", false, "read the secret from stdin (keeps it out of shell history)")
	idempotencyKey := fs.String("idempotency-key", "", "idempotency key (derived from the connection id when omitted)")
	config := configFlag{}
	fs.Var(config, "config", "adapter config as key=value; repeatable")
	positional, err := parseFlagsAnywhere(fs, args[1:])
	if err != nil {
		return nil, err
	}
	if len(positional) > 0 {
		return nil, fmt.Errorf("unexpected argument %q", positional[0])
	}
	if *secretStdin {
		if *secret != "" {
			return nil, fmt.Errorf("pass --secret or --secret-stdin, not both")
		}
		if stdin == nil {
			return nil, fmt.Errorf("--secret-stdin requires stdin")
		}
		raw, err := io.ReadAll(io.LimitReader(stdin, 1<<16))
		if err != nil {
			return nil, fmt.Errorf("read secret from stdin: %w", err)
		}
		*secret = strings.TrimRight(string(raw), "\r\n")
		if *secret == "" {
			return nil, fmt.Errorf("--secret-stdin read an empty secret")
		}
	}
	if *workspaceID == "" {
		resolved, err := s.workspace(ctx)
		if err != nil {
			return nil, err
		}
		*workspaceID = resolved
	}
	// Naming a connection is only meaningful once a workspace holds more than
	// one. The first `docker` connection may as well be called "docker".
	if *connectionID == "" {
		switch command {
		case "create":
			*connectionID = *adapterType
		case "authorize", "delete":
			resolved, err := s.soleConnection(ctx, *workspaceID)
			if err != nil {
				return nil, err
			}
			*connectionID = resolved
		}
	}
	switch command {
	case "list":
		return http.NewRequestWithContext(ctx, http.MethodGet, mustURL(baseURL, "/v1/connections", query("workspace_id", *workspaceID)), nil)
	case "create":
		if *adapterType == "" {
			return nil, fmt.Errorf("create requires --adapter-type")
		}
		payload := map[string]any{
			"workspace_id":  *workspaceID,
			"connection_id": *connectionID,
			"adapter_type":  *adapterType,
		}
		if len(config) > 0 {
			payload["config"] = map[string]string(config)
		}
		if *credentialSource != "" || *credentialRef != "" {
			payload["credential"] = map[string]string{"source": *credentialSource, "ref": *credentialRef}
		}
		if *secret != "" {
			payload["secret"] = *secret
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, mustURL(baseURL, "/v1/connections", nil), bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		key := *idempotencyKey
		if key == "" {
			key = "connection:" + *connectionID + ":create"
		}
		req.Header.Set("Idempotency-Key", key)
		return req, nil
	case "authorize":
		path := "/v1/connections/" + url.PathEscape(*connectionID) + "/authorize"
		return http.NewRequestWithContext(ctx, http.MethodPost, mustURL(baseURL, path, query("workspace_id", *workspaceID)), nil)
	case "delete":
		path := "/v1/connections/" + url.PathEscape(*connectionID)
		return http.NewRequestWithContext(ctx, http.MethodDelete, mustURL(baseURL, path, query("workspace_id", *workspaceID)), nil)
	default:
		return nil, fmt.Errorf("unknown connection command %q", command)
	}
}

// buildWorkloadRequest implements `mercator workload <create|revision ...>`.
func buildWorkloadRequest(ctx context.Context, s *session, args []string) (*http.Request, error) {
	baseURL := s.baseURL
	if args[0] == "revision" {
		if len(args) < 2 {
			return nil, fmt.Errorf("workload revision requires a subcommand: create, list, get")
		}
		return buildRevisionRequest(ctx, s, args[1:])
	}
	command := args[0]
	fs := flag.NewFlagSet("workload "+command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	workspaceID := fs.String("workspace-id", "", "workspace id (defaults to the broker's only workspace)")
	workloadID := fs.String("workload-id", "", "workload id")
	name := fs.String("name", "", "workload display name")
	idempotencyKey := fs.String("idempotency-key", "", "idempotency key (derived from the workload id when omitted)")
	positional, err := parseFlagsAnywhere(fs, args[1:])
	if err != nil {
		return nil, err
	}
	if len(positional) > 0 {
		return nil, fmt.Errorf("unexpected argument %q", positional[0])
	}
	if *workspaceID == "" {
		resolved, err := s.workspace(ctx)
		if err != nil {
			return nil, err
		}
		*workspaceID = resolved
	}
	switch command {
	case "create":
		if *workloadID == "" {
			return nil, fmt.Errorf("create requires --workload-id")
		}
		body, err := json.Marshal(map[string]string{
			"workspace_id": *workspaceID,
			"workload_id":  *workloadID,
			"name":         *name,
		})
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, mustURL(baseURL, "/v1/workloads", nil), bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		key := *idempotencyKey
		if key == "" {
			key = "workload:" + *workloadID + ":create"
		}
		req.Header.Set("Idempotency-Key", key)
		return req, nil
	default:
		return nil, fmt.Errorf("unknown workload command %q", command)
	}
}

func buildRevisionRequest(ctx context.Context, s *session, args []string) (*http.Request, error) {
	command := args[0]
	baseURL := s.baseURL
	fs := flag.NewFlagSet("workload revision "+command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	workspaceID := fs.String("workspace-id", "", "workspace id (defaults to the broker's only workspace)")
	workloadID := fs.String("workload-id", "", "workload id")
	revisionID := fs.String("revision-id", "", "revision id (get)")
	revisionJSON := fs.String("revision-json", "", "workload revision JSON (create)")
	idempotencyKey := fs.String("idempotency-key", "", "idempotency key (derived when omitted)")
	positional, err := parseFlagsAnywhere(fs, args[1:])
	if err != nil {
		return nil, err
	}
	if len(positional) > 0 {
		return nil, fmt.Errorf("unexpected argument %q", positional[0])
	}
	if *workloadID == "" {
		return nil, fmt.Errorf("%s requires --workload-id", command)
	}
	if *workspaceID == "" {
		resolved, err := s.workspace(ctx)
		if err != nil {
			return nil, err
		}
		*workspaceID = resolved
	}
	base := "/v1/workloads/" + url.PathEscape(*workloadID) + "/revisions"
	switch command {
	case "create":
		if *revisionJSON == "" {
			return nil, fmt.Errorf("create requires --revision-json")
		}
		var revision json.RawMessage
		if err := json.Unmarshal([]byte(*revisionJSON), &revision); err != nil {
			return nil, fmt.Errorf("invalid --revision-json: %w", err)
		}
		body, err := json.Marshal(map[string]any{"revision": revision})
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, mustURL(baseURL, base, query("workspace_id", *workspaceID)), bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		key := *idempotencyKey
		if key == "" {
			key = "workload:" + *workloadID + ":revision:" + randomToken()
		}
		req.Header.Set("Idempotency-Key", key)
		return req, nil
	case "list":
		return http.NewRequestWithContext(ctx, http.MethodGet, mustURL(baseURL, base, query("workspace_id", *workspaceID)), nil)
	case "get":
		if *revisionID == "" {
			return nil, fmt.Errorf("get requires --revision-id")
		}
		return http.NewRequestWithContext(ctx, http.MethodGet, mustURL(baseURL, base+"/"+url.PathEscape(*revisionID), query("workspace_id", *workspaceID)), nil)
	default:
		return nil, fmt.Errorf("unknown workload revision command %q", command)
	}
}
