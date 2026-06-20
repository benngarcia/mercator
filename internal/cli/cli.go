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

type Config struct {
	BaseURL string
	Args    []string
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
	Client  *http.Client
}

func Run(ctx context.Context, cfg Config) int {
	if cfg.Stdout == nil {
		cfg.Stdout = io.Discard
	}
	if cfg.Stderr == nil {
		cfg.Stderr = io.Discard
	}
	if cfg.Client == nil {
		cfg.Client = http.DefaultClient
	}
	if cfg.BaseURL == "" {
		writeCLIError(cfg.Stderr, "BASE_URL_REQUIRED", "MERCATOR_API_URL or --api-url is required")
		return 2
	}
	if len(cfg.Args) == 0 {
		writeCLIError(cfg.Stderr, "COMMAND_REQUIRED", "command is required")
		return 2
	}
	req, err := buildRequest(ctx, cfg.BaseURL, cfg.Args)
	if err != nil {
		writeCLIError(cfg.Stderr, "INVALID_ARGUMENTS", err.Error())
		return 2
	}
	resp, err := cfg.Client.Do(req)
	if err != nil {
		writeCLIError(cfg.Stderr, "REQUEST_FAILED", err.Error())
		return 1
	}
	defer resp.Body.Close()
	output := cfg.Stdout
	exit := 0
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		output = cfg.Stderr
		exit = 1
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		writeCLIError(cfg.Stderr, "READ_RESPONSE_FAILED", err.Error())
		return 1
	}
	if len(bytes.TrimSpace(data)) == 0 {
		data = []byte(`{}`)
	}
	if !json.Valid(data) {
		wrapped, _ := json.Marshal(map[string]any{"code": "NON_JSON_RESPONSE", "message": string(data), "status": resp.StatusCode})
		data = wrapped
	}
	_, _ = output.Write(bytes.TrimSpace(data))
	_, _ = output.Write([]byte("\n"))
	return exit
}

func buildRequest(ctx context.Context, baseURL string, args []string) (*http.Request, error) {
	if len(args) < 2 {
		return nil, fmt.Errorf("%s subcommand is required", args[0])
	}
	switch args[0] {
	case "run":
		return buildRunRequest(ctx, baseURL, args[1:])
	case "sink":
		return buildSinkRequest(ctx, baseURL, args[1:])
	default:
		return nil, fmt.Errorf("unknown command %q", args[0])
	}
}

func buildRunRequest(ctx context.Context, baseURL string, args []string) (*http.Request, error) {
	command := args[0]
	fs := flag.NewFlagSet("run "+command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	workspaceID := fs.String("workspace-id", "", "workspace id")
	runID := fs.String("run-id", "", "run id")
	idempotencyKey := fs.String("idempotency-key", "", "idempotency key")
	workloadJSON := fs.String("workload-json", "", "workload revision json")
	if err := fs.Parse(args[1:]); err != nil {
		return nil, err
	}
	switch command {
	case "create":
		if *workspaceID == "" || *runID == "" || *idempotencyKey == "" || *workloadJSON == "" {
			return nil, fmt.Errorf("create requires --workspace-id, --run-id, --idempotency-key, and --workload-json")
		}
		var workload json.RawMessage
		if err := json.Unmarshal([]byte(*workloadJSON), &workload); err != nil {
			return nil, fmt.Errorf("invalid --workload-json: %w", err)
		}
		body, err := json.Marshal(map[string]any{
			"workspace_id": *workspaceID,
			"run_id":       *runID,
			"workload":     workload,
		})
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, mustURL(baseURL, "/v1/runs", nil), bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", *idempotencyKey)
		return req, nil
	case "list":
		if *workspaceID == "" {
			return nil, fmt.Errorf("list requires --workspace-id")
		}
		return http.NewRequestWithContext(ctx, http.MethodGet, mustURL(baseURL, "/v1/runs", query("workspace_id", *workspaceID)), nil)
	case "get", "wait", "events", "decision":
		if *workspaceID == "" || *runID == "" {
			return nil, fmt.Errorf("%s requires --workspace-id and --run-id", command)
		}
		path := "/v1/runs/" + url.PathEscape(*runID)
		if command == "wait" {
			path += ":wait"
		}
		if command == "events" || command == "decision" {
			path += "/" + command
		}
		return http.NewRequestWithContext(ctx, http.MethodGet, mustURL(baseURL, path, query("workspace_id", *workspaceID)), nil)
	case "refresh", "cancel":
		if *workspaceID == "" || *runID == "" {
			return nil, fmt.Errorf("%s requires --workspace-id and --run-id", command)
		}
		path := "/v1/runs/" + url.PathEscape(*runID) + ":" + command
		return http.NewRequestWithContext(ctx, http.MethodPost, mustURL(baseURL, path, query("workspace_id", *workspaceID)), nil)
	default:
		return nil, fmt.Errorf("unknown run command %q", command)
	}
}

func buildSinkRequest(ctx context.Context, baseURL string, args []string) (*http.Request, error) {
	command := args[0]
	fs := flag.NewFlagSet("sink "+command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	sinkID := fs.String("sink-id", "", "sink id")
	from := fs.Uint64("from", 0, "exclusive global position to replay after")
	limit := fs.Int("limit", 100, "maximum events")
	replayID := fs.String("replay-id", "", "replay id")
	if err := fs.Parse(args[1:]); err != nil {
		return nil, err
	}
	if *sinkID == "" {
		return nil, fmt.Errorf("%s requires --sink-id", command)
	}
	switch command {
	case "status":
		return http.NewRequestWithContext(ctx, http.MethodGet, mustURL(baseURL, "/v1/sinks/"+url.PathEscape(*sinkID), nil), nil)
	case "deliver":
		return http.NewRequestWithContext(ctx, http.MethodPost, mustURL(baseURL, "/v1/sinks/"+url.PathEscape(*sinkID)+":deliver", nil), nil)
	case "replay":
		body, err := json.Marshal(map[string]any{
			"from_exclusive": *from,
			"limit":          *limit,
			"replay_id":      *replayID,
		})
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, mustURL(baseURL, "/v1/sinks/"+url.PathEscape(*sinkID)+":replay", nil), bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	default:
		return nil, fmt.Errorf("unknown sink command %q", command)
	}
}

func mustURL(baseURL, path string, values url.Values) string {
	base := strings.TrimRight(baseURL, "/")
	if values == nil {
		return base + path
	}
	return base + path + "?" + values.Encode()
}

func query(key, value string) url.Values {
	values := url.Values{}
	values.Set(key, value)
	return values
}

func writeCLIError(w io.Writer, code, message string) {
	body, _ := json.Marshal(map[string]string{"code": code, "message": message})
	_, _ = w.Write(body)
	_, _ = w.Write([]byte("\n"))
}
