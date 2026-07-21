package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

type verifyTrial func(context.Context, Trial) (Evidence, error)

// RunCommand implements `mercator verify --spec FILE`.
func RunCommand(ctx context.Context, args []string, environment map[string]string, stdout, stderr io.Writer) int {
	runner := NewRunner(RunnerConfig{
		Environment:   environment,
		ListenAddress: environment["MERCATOR_CONFORMANCE_LISTEN_ADDR"],
		PublicURL:     environment["MERCATOR_CONFORMANCE_PUBLIC_URL"],
	})
	return runCommand(ctx, args, environment, stdout, stderr, runner.Verify)
}

func runCommand(ctx context.Context, args []string, environment map[string]string, stdout, stderr io.Writer, verify verifyTrial) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		_, _ = io.WriteString(stdout, verifyHelp)
		return 0
	}
	trial, err := readTrial(args)
	if err != nil {
		writeCommandError(stderr, "INVALID_TRIAL", err.Error())
		return 2
	}
	evidence, err := verify(ctx, trial)
	credential := environment[trial.CredentialEnv]
	if err != nil {
		evidence.Verdict = VerdictBlocked
		evidence.Failure = &TrialFailure{
			Code:    "TRIAL_SETUP_FAILED",
			Message: err.Error(),
		}
	}
	if evidence.Failure != nil {
		evidence.Failure.Message = sanitize(evidence.Failure.Message, credential)
	}
	if err := json.NewEncoder(stdout).Encode(evidence); err != nil {
		writeCommandError(stderr, "WRITE_EVIDENCE_FAILED", err.Error())
		return 2
	}
	if evidence.Verdict == VerdictPassed {
		return 0
	}
	return 1
}

const verifyHelp = `Usage: mercator verify --spec FILE

Launch a real, bounded provider Conformance Trial. The JSON result proves
provider authorization, placement, signed probe exit, and terminal cleanup.

Cloud trials read the credential named by credential_env and require
MERCATOR_CONFORMANCE_PUBLIC_URL for probe reports.
`

type trialDocument struct {
	AdapterType        string            `json:"adapter_type"`
	CredentialEnv      string            `json:"credential_env,omitempty"`
	Config             map[string]string `json:"config,omitempty"`
	Image              string            `json:"image"`
	MaxExpectedCostUSD float64           `json:"max_expected_cost_usd"`
	Timeout            string            `json:"timeout"`
}

func readTrial(args []string) (Trial, error) {
	flags := flag.NewFlagSet("verify", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("spec", "", "path to a Conformance Trial JSON document")
	if err := flags.Parse(args); err != nil {
		return Trial{}, err
	}
	if flags.NArg() != 0 {
		return Trial{}, fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	if *path == "" {
		return Trial{}, errors.New("verify requires --spec FILE")
	}
	raw, err := os.ReadFile(*path)
	if err != nil {
		return Trial{}, fmt.Errorf("read trial document: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var document trialDocument
	if err := decoder.Decode(&document); err != nil {
		return Trial{}, fmt.Errorf("decode trial document: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Trial{}, errors.New("decode trial document: expected one JSON object")
	}
	timeout, err := time.ParseDuration(document.Timeout)
	if err != nil {
		return Trial{}, fmt.Errorf("decode trial timeout: %w", err)
	}
	return Trial{
		AdapterType:        document.AdapterType,
		CredentialEnv:      document.CredentialEnv,
		Config:             document.Config,
		Image:              document.Image,
		MaxExpectedCostUSD: document.MaxExpectedCostUSD,
		Timeout:            timeout,
	}, nil
}

func writeCommandError(stderr io.Writer, code, message string) {
	if stderr == nil {
		return
	}
	_ = json.NewEncoder(stderr).Encode(struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{Code: code, Message: message})
}

func sanitize(message, credential string) string {
	if credential != "" {
		message = strings.ReplaceAll(message, credential, "[REDACTED]")
	}
	return message
}
