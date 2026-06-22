package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
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
	Token   string
	// WorkspaceID is the default workspace applied to run subcommands when
	// --workspace-id is not passed. Sourced from MERCATOR_WORKSPACE_ID. An
	// explicit --workspace-id flag always overrides it.
	WorkspaceID string
	Args        []string
	Stdin       io.Reader
	Stdout      io.Writer
	Stderr      io.Writer
	Client      *http.Client
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
	baseURL, args, err := parseGlobalFlags(cfg.BaseURL, cfg.Args)
	if err != nil {
		writeCLIError(cfg.Stderr, "INVALID_ARGUMENTS", err.Error())
		return 2
	}
	if baseURL == "" {
		writeCLIError(cfg.Stderr, "BASE_URL_REQUIRED", "MERCATOR_API_URL or --api-url is required")
		return 2
	}
	if len(args) == 0 {
		writeCLIError(cfg.Stderr, "COMMAND_REQUIRED", "command is required")
		return 2
	}
	req, err := buildRequest(ctx, baseURL, cfg.WorkspaceID, args)
	if err != nil {
		writeCLIError(cfg.Stderr, "INVALID_ARGUMENTS", err.Error())
		return 2
	}
	if cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
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

func parseGlobalFlags(baseURL string, args []string) (string, []string, error) {
	fs := flag.NewFlagSet("mercator", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	apiURL := fs.String("api-url", baseURL, "Mercator API URL")
	if err := fs.Parse(args); err != nil {
		return "", nil, err
	}
	return *apiURL, fs.Args(), nil
}

func buildRequest(ctx context.Context, baseURL, defaultWorkspaceID string, args []string) (*http.Request, error) {
	if len(args) < 2 {
		return nil, fmt.Errorf("%s subcommand is required", args[0])
	}
	switch args[0] {
	case "run":
		return buildRunRequest(ctx, baseURL, defaultWorkspaceID, args[1:])
	case "sink":
		return buildSinkRequest(ctx, baseURL, args[1:])
	default:
		return nil, fmt.Errorf("unknown command %q", args[0])
	}
}

func buildRunRequest(ctx context.Context, baseURL, defaultWorkspaceID string, args []string) (*http.Request, error) {
	command := args[0]

	// Split off container args after a `--` separator (used by the image
	// shorthand, e.g. `run create busybox -- echo hi`). Everything after the
	// first bare `--` is passed verbatim as container args and is not flag-parsed.
	flagArgs, containerArgs := splitDoubleDash(args[1:])

	fs := flag.NewFlagSet("run "+command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	workspaceID := fs.String("workspace-id", defaultWorkspaceID, "workspace id (defaults to MERCATOR_WORKSPACE_ID)")
	runID := fs.String("run-id", "", "run id (optional on create; server generates one when omitted)")
	idempotencyKey := fs.String("idempotency-key", "", "idempotency key (optional on create; derived from the run when omitted)")
	workloadJSON := fs.String("workload-json", "", "workload revision json")
	image := fs.String("image", "", "container image shorthand (alternative to --workload-json)")
	if err := fs.Parse(flagArgs); err != nil {
		return nil, err
	}
	switch command {
	case "create":
		// A bare positional first arg is the image shorthand:
		//   run create busybox -- echo hi
		positional := fs.Args()
		if *image == "" && len(positional) > 0 {
			*image = positional[0]
			positional = positional[1:]
		}
		// Any positional tokens that appear BEFORE a `--` (other than the image)
		// are also treated as container args, so `run create busybox echo hi`
		// works as well as the `--`-separated form.
		if len(positional) > 0 {
			containerArgs = append(append([]string{}, positional...), containerArgs...)
		}

		if *workspaceID == "" {
			return nil, fmt.Errorf("create requires --workspace-id or MERCATOR_WORKSPACE_ID")
		}
		hasWorkload := *workloadJSON != ""
		hasImage := *image != ""
		if hasWorkload == hasImage {
			return nil, fmt.Errorf("create requires exactly one of an image (positional arg or --image) or --workload-json")
		}

		payload := map[string]any{"workspace_id": *workspaceID}
		// run_id is optional: omit it so the server generates one and returns it.
		if *runID != "" {
			payload["run_id"] = *runID
		}
		if hasWorkload {
			var workload json.RawMessage
			if err := json.Unmarshal([]byte(*workloadJSON), &workload); err != nil {
				return nil, fmt.Errorf("invalid --workload-json: %w", err)
			}
			payload["workload"] = workload
		} else {
			payload["image"] = *image
			if len(containerArgs) > 0 {
				payload["args"] = containerArgs
			}
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, mustURL(baseURL, "/v1/runs", nil), bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		// Idempotency-Key is required by the server. When the caller omits it, mint
		// a stable key so the simplest invocation is still retry-safe within a run
		// id; when run_id is also omitted, fall back to a fresh random key (a
		// generated run is single-shot, so there is no stable key to derive).
		key := *idempotencyKey
		if key == "" {
			if *runID != "" {
				key = *runID + ":create"
			} else {
				key = "idem-" + randomToken()
			}
		}
		req.Header.Set("Idempotency-Key", key)
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

// splitDoubleDash splits args at the first bare "--": everything before is
// returned for flag parsing, everything after is returned verbatim (used as
// container args by the image shorthand). When there is no "--", the second
// result is nil.
func splitDoubleDash(args []string) ([]string, []string) {
	for i, a := range args {
		if a == "--" {
			return args[:i], append([]string{}, args[i+1:]...)
		}
	}
	return args, nil
}

// randomToken returns a short random hex string for minting an idempotency key
// when neither a key nor a run id is supplied.
func randomToken() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		// rand.Read only fails if the system RNG is unavailable; fall back to a
		// fixed token rather than panic (still unique enough per-process use).
		return "00000000000000000000000000000000"
	}
	return hex.EncodeToString(buf)
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
