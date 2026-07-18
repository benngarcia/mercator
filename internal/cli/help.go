package cli

import (
	"fmt"
	"strings"
)

func helpText(args []string) (string, bool) {
	tokens := helpTokens(args)
	if len(tokens) == 0 {
		return rootHelp, true
	}
	if isHelpArg(tokens[0]) {
		return rootHelp, true
	}
	if tokens[0] == "help" {
		return helpForTopic(tokens[1:], true)
	}
	return helpForTopic(tokens, false)
}

func helpForTopic(tokens []string, explicit bool) (string, bool) {
	if len(tokens) == 0 {
		return rootHelp, true
	}
	switch tokens[0] {
	case "run":
		if explicit && len(tokens) == 1 {
			return runHelp, true
		}
		if len(tokens) > 1 && isHelpArg(tokens[1]) {
			return runHelp, true
		}
		if len(tokens) > 1 {
			return runCommandHelp(tokens[1:], explicit)
		}
	case "sink":
		if explicit && len(tokens) == 1 {
			return sinkHelp, true
		}
		if len(tokens) > 1 && isHelpArg(tokens[1]) {
			return sinkHelp, true
		}
		if len(tokens) > 1 {
			return sinkCommandHelp(tokens[1:], explicit)
		}
	case "login":
		if explicit || (len(tokens) > 1 && isHelpArg(tokens[1])) {
			return loginHelp, true
		}
	case "logout":
		if explicit || (len(tokens) > 1 && isHelpArg(tokens[1])) {
			return logoutHelp, true
		}
	case "context":
		if explicit && len(tokens) == 1 {
			return contextHelp, true
		}
		if len(tokens) > 1 && isHelpArg(tokens[1]) {
			return contextHelp, true
		}
		if explicit && len(tokens) > 1 {
			return contextHelp, true
		}
	}
	return "", false
}

func runCommandHelp(tokens []string, explicit bool) (string, bool) {
	if len(tokens) == 0 {
		return runHelp, true
	}
	if !explicit && (len(tokens) < 2 || !isHelpArg(tokens[1])) {
		return "", false
	}
	switch tokens[0] {
	case "create":
		return runCreateHelp, true
	case "list":
		return runListHelp, true
	case "get":
		return runReadHelp("get"), true
	case "wait":
		return runReadHelp("wait"), true
	case "events":
		return runReadHelp("events"), true
	case "decision":
		return runReadHelp("decision"), true
	case "refresh":
		return runActionHelp("refresh"), true
	case "cancel":
		return runActionHelp("cancel"), true
	}
	return "", false
}

func sinkCommandHelp(tokens []string, explicit bool) (string, bool) {
	if len(tokens) == 0 {
		return sinkHelp, true
	}
	if !explicit && (len(tokens) < 2 || !isHelpArg(tokens[1])) {
		return "", false
	}
	switch tokens[0] {
	case "status":
		return sinkStatusHelp, true
	case "deliver":
		return sinkDeliverHelp, true
	case "replay":
		return sinkReplayHelp, true
	}
	return "", false
}

func helpTokens(args []string) []string {
	tokens := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		if arg == "--api-url" {
			i++
			continue
		}
		if strings.HasPrefix(arg, "--api-url=") {
			continue
		}
		tokens = append(tokens, arg)
	}
	return tokens
}

func isHelpArg(arg string) bool {
	return arg == "-h" || arg == "--help" || arg == "help"
}

const rootHelp = `Usage: mercator [--api-url URL] <command>

Mercator is an OCI run broker with an HTTP API, JSON CLI, and embedded console.

Commands:
  serve                 Start the Mercator HTTP server and console
  login                 Sign in through the server's OIDC login and store a CLI token
  logout                Clear the stored login credential
  context <command>     Manage named server contexts (list, use, set, delete)
  run <command>         Create, read, wait for, and inspect runs
  sink <command>        Inspect and replay event sinks
  help [topic]          Show help for run, run create, sink, login, or context

Environment (always wins over the config file, for CI):
  MERCATOR_API_URL      API URL for CLI commands, for example http://127.0.0.1:8080
  MERCATOR_API_TOKEN    Bearer token for CLI commands
  MERCATOR_WORKSPACE_ID Default workspace for run commands
  MERCATOR_CONFIG       Config file path (default ~/.config/mercator/config.json)

Examples:
  mercator context set staging --api-url https://mercator.example.com --workspace-id ws_1
  mercator login
  mercator run create busybox -- echo hi
  mercator context use production
`

const runHelp = `Usage: mercator run <command> [flags]

Run commands:
  create    Create a run from an image shorthand or workload JSON
  list      List runs in a workspace
  get       Read one run
  wait      Wait for one run to close
  events    List public run events
  decision  Read the placement decision for a run
  refresh   Refresh run state from the adapter
  cancel    Request cancellation

Common flags:
  --workspace-id ID     Workspace id; defaults to MERCATOR_WORKSPACE_ID
  --run-id ID           Run id for read/action commands

Examples:
  mercator run create busybox -- echo hi
  mercator run get --run-id run_...
  mercator help run create
`

const runCreateHelp = `Usage: mercator run create [--workspace-id ID] [--run-id ID] [--idempotency-key KEY] <image> [-- args...]
       mercator run create [--workspace-id ID] [--run-id ID] [--idempotency-key KEY] --workload-json JSON

Create a run. The simplest path is an image shorthand:

  mercator run create busybox -- echo hi

Flags:
  --workspace-id ID       Workspace id; defaults to MERCATOR_WORKSPACE_ID
  --run-id ID             Optional caller-supplied run id
  --idempotency-key KEY   Optional idempotency key; derived or minted when omitted
  --image IMAGE           Image shorthand, alternative to positional image
  --workload-json JSON    Full workload revision JSON
`

const runListHelp = `Usage: mercator run list [--workspace-id ID]

List runs in a workspace. --workspace-id defaults to MERCATOR_WORKSPACE_ID.
`

const loginHelp = `Usage: mercator login [--context NAME] [--api-url URL]

Sign in through the server's OIDC login (the standard native-app flow: a
browser opens, you authenticate with the identity provider, and the CLI
receives a token tied to your identity on a localhost redirect).

The token is stored in the named context (default: the current context, else
"default") in the config file. API commands then authenticate as you, and
mutations are audited under your email.

Requires the server to have MERCATOR_OIDC_* configured. Token lifetime is 30
days; run login again to renew.
`

const logoutHelp = `Usage: mercator logout [--context NAME]

Clear the stored login credential from the named (or current) context.
`

const contextHelp = `Usage: mercator context <command>

Contexts name Mercator deployments so you can target staging or production by
name. The current context supplies api_url, workspace_id, and a credential to
every API command; MERCATOR_* environment variables always win over it.

Commands:
  list                        Show all contexts and which is current
  use <name>                  Switch the current context
  set <name> [flags]          Create or update a context
      --api-url URL           API base URL
      --workspace-id ID       Default workspace
      --token TOKEN           Static API token (machine credential)
  delete <name>               Remove a context

Examples:
  mercator context set staging --api-url https://staging.example.com --workspace-id ws_1
  mercator context use staging
  mercator login
`

const sinkHelp = `Usage: mercator sink <command> [flags]

Sink commands:
  status     Read sink cursor state
  deliver    Deliver pending events to a sink
  replay     Replay events after a global position

Common flags:
  --sink-id ID          Sink id, for example audit
`

const sinkStatusHelp = `Usage: mercator sink status --sink-id ID

Read sink cursor state.
`

const sinkDeliverHelp = `Usage: mercator sink deliver --sink-id ID

Deliver pending events to a sink.
`

const sinkReplayHelp = `Usage: mercator sink replay --sink-id ID [--from POSITION] [--limit N] [--replay-id ID]

Replay events after a global position.
`

func runReadHelp(command string) string {
	return fmt.Sprintf(`Usage: mercator run %s [--workspace-id ID] --run-id ID

Read run data. --workspace-id defaults to MERCATOR_WORKSPACE_ID.
`, command)
}

func runActionHelp(command string) string {
	return fmt.Sprintf(`Usage: mercator run %s [--workspace-id ID] --run-id ID

Post a run action. --workspace-id defaults to MERCATOR_WORKSPACE_ID.
`, command)
}
