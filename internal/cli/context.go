package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"time"
)

// runContext implements `mercator context <list|use|set|delete>`. Context
// commands never touch the network; they manage the config file.
func runContext(cfg Config, args []string) int {
	if len(args) == 0 {
		writeCLIError(cfg.Stderr, "COMMAND_REQUIRED", "context requires a subcommand: list, use, set, delete")
		return 2
	}
	file, err := loadFileConfig(cfg.ConfigPath)
	if err != nil {
		writeCLIError(cfg.Stderr, "CONFIG_INVALID", err.Error())
		return 1
	}
	switch args[0] {
	case "list":
		return contextList(cfg, file)
	case "use":
		return contextUse(cfg, file, args[1:])
	case "set":
		return contextSet(cfg, file, args[1:])
	case "delete":
		return contextDelete(cfg, file, args[1:])
	default:
		writeCLIError(cfg.Stderr, "INVALID_ARGUMENTS", fmt.Sprintf("unknown context command %q", args[0]))
		return 2
	}
}

// contextSummary is the JSON shape context commands print. Credentials are
// summarized, never echoed.
type contextSummary struct {
	Name        string `json:"name"`
	Current     bool   `json:"current"`
	APIURL      string `json:"api_url,omitempty"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	Credential  string `json:"credential"`
}

func summarize(name string, current bool, c *ContextConfig) contextSummary {
	credential := "none"
	switch {
	case c.cliTokenValid(time.Now()):
		credential = fmt.Sprintf("login (%s, expires %s)", c.CLITokenEmail, c.CLITokenExpiresAt)
	case c.CLIToken != "":
		credential = fmt.Sprintf("login (%s, EXPIRED — run `mercator login`)", c.CLITokenEmail)
	case c.APIToken != "":
		credential = "api-token"
	}
	return contextSummary{
		Name:        name,
		Current:     current,
		APIURL:      c.APIURL,
		WorkspaceID: c.WorkspaceID,
		Credential:  credential,
	}
}

func contextList(cfg Config, file FileConfig) int {
	summaries := make([]contextSummary, 0, len(file.Contexts))
	for _, name := range file.contextNames() {
		summaries = append(summaries, summarize(name, name == file.CurrentContext, file.Contexts[name]))
	}
	writeJSONLine(cfg.Stdout, map[string]any{
		"current":  file.CurrentContext,
		"contexts": summaries,
	})
	return 0
}

func contextUse(cfg Config, file FileConfig, args []string) int {
	if len(args) != 1 || args[0] == "" {
		writeCLIError(cfg.Stderr, "INVALID_ARGUMENTS", "usage: mercator context use <name>")
		return 2
	}
	name := args[0]
	if _, ok := file.Contexts[name]; !ok {
		writeCLIError(cfg.Stderr, "CONTEXT_NOT_FOUND", fmt.Sprintf("context %q does not exist; create it with `mercator context set %s --api-url URL`", name, name))
		return 1
	}
	file.CurrentContext = name
	if err := saveFileConfig(cfg.ConfigPath, file); err != nil {
		writeCLIError(cfg.Stderr, "CONFIG_WRITE_FAILED", err.Error())
		return 1
	}
	writeJSONLine(cfg.Stdout, map[string]any{"current": name})
	return 0
}

func contextSet(cfg Config, file FileConfig, args []string) int {
	if len(args) == 0 || args[0] == "" || args[0][0] == '-' {
		writeCLIError(cfg.Stderr, "INVALID_ARGUMENTS", "usage: mercator context set <name> [--api-url URL] [--workspace-id ID] [--token TOKEN]")
		return 2
	}
	name := args[0]
	fs := flag.NewFlagSet("context set", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	apiURL := fs.String("api-url", "", "API base URL for this context")
	workspaceID := fs.String("workspace-id", "", "default workspace id for this context")
	token := fs.String("token", "", "static API token for this context")
	if err := fs.Parse(args[1:]); err != nil {
		writeCLIError(cfg.Stderr, "INVALID_ARGUMENTS", err.Error())
		return 2
	}
	if len(fs.Args()) > 0 {
		writeCLIError(cfg.Stderr, "INVALID_ARGUMENTS", fmt.Sprintf("unexpected argument %q", fs.Args()[0]))
		return 2
	}
	current := file.Contexts[name]
	if current == nil {
		current = &ContextConfig{}
		file.Contexts[name] = current
	}
	if *apiURL != "" {
		current.APIURL = *apiURL
	}
	if *workspaceID != "" {
		current.WorkspaceID = *workspaceID
	}
	if *token != "" {
		current.APIToken = *token
	}
	if file.CurrentContext == "" {
		file.CurrentContext = name
	}
	if err := saveFileConfig(cfg.ConfigPath, file); err != nil {
		writeCLIError(cfg.Stderr, "CONFIG_WRITE_FAILED", err.Error())
		return 1
	}
	writeJSONLine(cfg.Stdout, summarize(name, name == file.CurrentContext, current))
	return 0
}

func contextDelete(cfg Config, file FileConfig, args []string) int {
	if len(args) != 1 || args[0] == "" {
		writeCLIError(cfg.Stderr, "INVALID_ARGUMENTS", "usage: mercator context delete <name>")
		return 2
	}
	name := args[0]
	if _, ok := file.Contexts[name]; !ok {
		writeCLIError(cfg.Stderr, "CONTEXT_NOT_FOUND", fmt.Sprintf("context %q does not exist", name))
		return 1
	}
	delete(file.Contexts, name)
	if file.CurrentContext == name {
		file.CurrentContext = ""
	}
	if err := saveFileConfig(cfg.ConfigPath, file); err != nil {
		writeCLIError(cfg.Stderr, "CONFIG_WRITE_FAILED", err.Error())
		return 1
	}
	writeJSONLine(cfg.Stdout, map[string]any{"deleted": name})
	return 0
}

func writeJSONLine(w io.Writer, v any) {
	data, _ := json.Marshal(v)
	_, _ = w.Write(data)
	_, _ = w.Write([]byte("\n"))
}
