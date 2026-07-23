package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Config struct {
	// BaseURL, Token, and WorkspaceID are the environment-derived values
	// (MERCATOR_API_URL / MERCATOR_API_TOKEN / MERCATOR_WORKSPACE_ID). They
	// win over the config file's current context, so CI setups keep working
	// with no config file at all.
	BaseURL string
	Token   string
	// WorkspaceID is the default workspace applied to run subcommands when
	// --workspace-id is not passed. An explicit --workspace-id flag always
	// overrides it.
	WorkspaceID string
	// ConfigPath is where named contexts live (see DefaultConfigPath).
	ConfigPath string
	Args       []string
	Stdin      io.Reader
	Stdout     io.Writer
	Stderr     io.Writer
	Client     *http.Client
	// OpenBrowser overrides how `mercator login` launches the browser
	// (injected in tests).
	OpenBrowser func(url string) error
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
	if text, ok := helpText(cfg.Args); ok {
		_, _ = io.WriteString(cfg.Stdout, text)
		return 0
	}
	globalBaseURL, explicitURL, args, err := parseGlobalFlags(cfg.BaseURL, cfg.Args)
	if err != nil {
		writeCLIError(cfg.Stderr, "INVALID_ARGUMENTS", err.Error())
		return 2
	}
	if len(args) == 0 {
		writeCLIError(cfg.Stderr, "COMMAND_REQUIRED", "command is required")
		return 2
	}
	// Local commands (login/logout/context) manage credentials themselves. An
	// explicit --api-url reaches them distinctly from the env default: context
	// set stores only an explicit URL, never the ambient environment.
	flagURL := ""
	if explicitURL {
		flagURL = globalBaseURL
	}
	switch args[0] {
	case "login":
		loginCfg := cfg
		loginCfg.BaseURL = globalBaseURL
		return runLogin(ctx, loginCfg, args[1:])
	case "logout":
		return runLogout(cfg, args[1:])
	case "context":
		return runContext(cfg, flagURL, args[1:])
	}

	// API commands: env wins, then the config file's current context.
	resolvedCfg := cfg
	resolvedCfg.BaseURL = globalBaseURL
	baseURL, token, workspaceID, warnings := resolveCredentials(resolvedCfg, time.Now())
	for _, warning := range warnings {
		fmt.Fprintln(cfg.Stderr, "warning: "+warning)
	}
	if baseURL == "" {
		writeCLIError(cfg.Stderr, "BASE_URL_REQUIRED", "MERCATOR_API_URL, --api-url, or a configured context is required")
		return 2
	}
	req, err := buildRequest(ctx, baseURL, workspaceID, cfg.Stdin, args)
	if err != nil {
		writeCLIError(cfg.Stderr, "INVALID_ARGUMENTS", err.Error())
		return 2
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
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

// parseGlobalFlags extracts the global --api-url flag from anywhere in the
// argument list (before or after the command), leaving every other token in
// order. It reports whether the flag was passed explicitly, so commands that
// treat an explicit URL differently from the environment default (context set,
// login) can tell them apart. Scanning stops at a bare "--": everything after
// it belongs to the workload verbatim.
func parseGlobalFlags(baseURL string, args []string) (apiURL string, explicit bool, rest []string, err error) {
	apiURL = baseURL
	rest = make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			rest = append(rest, args[i:]...)
			return apiURL, explicit, rest, nil
		case arg == "--api-url":
			if i+1 >= len(args) {
				return "", false, nil, fmt.Errorf("--api-url requires a value")
			}
			i++
			apiURL = args[i]
			explicit = true
		case strings.HasPrefix(arg, "--api-url="):
			apiURL = strings.TrimPrefix(arg, "--api-url=")
			explicit = true
		default:
			rest = append(rest, arg)
		}
	}
	return apiURL, explicit, rest, nil
}

// parseFlagsAnywhere parses fs against args accepting flags in any position:
// each positional token is set aside and parsing resumes after it, so
// `run create busybox --workspace-id ws_1` and
// `run create --workspace-id ws_1 busybox` are the same command. An unknown
// flag-looking token errors loudly instead of silently becoming a positional;
// literal arguments that begin with "-" belong after a bare "--".
func parseFlagsAnywhere(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}

func buildRequest(ctx context.Context, baseURL, defaultWorkspaceID string, stdin io.Reader, args []string) (*http.Request, error) {
	if len(args) < 2 {
		return nil, fmt.Errorf("%s subcommand is required", args[0])
	}
	switch args[0] {
	case "run":
		return buildRunRequest(ctx, baseURL, defaultWorkspaceID, args[1:])
	case "sink":
		return buildSinkRequest(ctx, baseURL, args[1:])
	case "connection":
		return buildConnectionRequest(ctx, baseURL, defaultWorkspaceID, stdin, args[1:])
	case "workload":
		return buildWorkloadRequest(ctx, baseURL, defaultWorkspaceID, args[1:])
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
	positional, err := parseFlagsAnywhere(fs, flagArgs)
	if err != nil {
		return nil, err
	}
	// Only create takes positionals (the image shorthand and container args);
	// anywhere else a stray token is a mistake worth a loud error.
	if command != "create" && len(positional) > 0 {
		return nil, fmt.Errorf("unexpected argument %q", positional[0])
	}
	switch command {
	case "create":
		// A bare positional first arg is the image shorthand:
		//   run create busybox -- echo hi
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
			path += "/wait"
		}
		if command == "events" || command == "decision" {
			path += "/" + command
		}
		return http.NewRequestWithContext(ctx, http.MethodGet, mustURL(baseURL, path, query("workspace_id", *workspaceID)), nil)
	case "refresh", "cancel":
		if *workspaceID == "" || *runID == "" {
			return nil, fmt.Errorf("%s requires --workspace-id and --run-id", command)
		}
		path := "/v1/runs/" + url.PathEscape(*runID) + "/" + command
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
	positional, err := parseFlagsAnywhere(fs, args[1:])
	if err != nil {
		return nil, err
	}
	if len(positional) > 0 {
		return nil, fmt.Errorf("unexpected argument %q", positional[0])
	}
	if *sinkID == "" {
		return nil, fmt.Errorf("%s requires --sink-id", command)
	}
	switch command {
	case "status":
		return http.NewRequestWithContext(ctx, http.MethodGet, mustURL(baseURL, "/v1/sinks/"+url.PathEscape(*sinkID), nil), nil)
	case "deliver":
		return http.NewRequestWithContext(ctx, http.MethodPost, mustURL(baseURL, "/v1/sinks/"+url.PathEscape(*sinkID)+"/deliver", nil), nil)
	case "replay":
		body, err := json.Marshal(map[string]any{
			"from_exclusive": *from,
			"limit":          *limit,
			"replay_id":      *replayID,
		})
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, mustURL(baseURL, "/v1/sinks/"+url.PathEscape(*sinkID)+"/replay", nil), bytes.NewReader(body))
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

// randomToken returns a cryptographically random, text-safe token with at least
// 128 bits of entropy.
func randomToken() string {
	return rand.Text()
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
