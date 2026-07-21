package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"
)

type CommandConfig struct {
	Args        []string
	Environment map[string]string
	Stdout      io.Writer
	Stderr      io.Writer
	RunTrial    func(context.Context, TrialSpec) (TrialReport, error)
}

func RunCommand(ctx context.Context, cfg CommandConfig) int {
	spec, err := parseCommand(cfg.Args)
	if err != nil {
		writeCommandError(cfg.Stderr, err)
		return 2
	}
	if err := ValidateSpec(spec, func(name string) (string, bool) {
		value, found := cfg.Environment[name]
		return value, found
	}); err != nil {
		writeCommandError(cfg.Stderr, err)
		return 2
	}
	if cfg.RunTrial == nil {
		writeCommandError(cfg.Stderr, errors.New("conformance trial runner is not configured"))
		return 2
	}

	report, err := cfg.RunTrial(ctx, spec)
	if err != nil {
		report.AdapterType = spec.AdapterType
		report.Mode = spec.Mode
		report.Verdict = VerdictBlocked
		report.Failure = &TrialFailure{Code: "TRIAL_SETUP_FAILED", Message: err.Error()}
	}
	if err := json.NewEncoder(cfg.Stdout).Encode(report); err != nil {
		writeCommandError(cfg.Stderr, fmt.Errorf("write trial report: %w", err))
		return 2
	}
	if report.Verdict == VerdictPassed {
		return 0
	}
	return 1
}

func parseCommand(args []string) (TrialSpec, error) {
	if len(args) == 0 || args[0] != "connection" {
		return TrialSpec{}, errors.New("usage: mercator verify connection [options]")
	}
	fs := flag.NewFlagSet("verify connection", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	adapterType := fs.String("adapter", "", "provider adapter type")
	credentialEnv := fs.String("credential-env", "", "environment variable containing the provider credential")
	image := fs.String("image", "", "digest-pinned probe or workload image")
	mode := fs.String("mode", string(ProbeMode), "probe or launch-cancel")
	listenAddress := fs.String("listen-address", "", "embedded Mercator listener address")
	publicURL := fs.String("public-url", "", "workload-reachable Mercator base URL")
	maxCost := fs.Float64("max-expected-cost-usd", 0.50, "maximum expected provider cost")
	timeout := fs.Duration("timeout", 12*time.Minute, "trial wall-clock timeout")
	jsonOutput := fs.Bool("json", false, "write the structured trial report")
	config := configValues{}
	fs.Var(&config, "config", "provider config key=value (repeatable)")
	if err := fs.Parse(args[1:]); err != nil {
		return TrialSpec{}, err
	}
	if fs.NArg() != 0 {
		return TrialSpec{}, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	if !*jsonOutput {
		return TrialSpec{}, errors.New("--json is required")
	}
	return TrialSpec{
		AdapterType:        *adapterType,
		CredentialEnv:      *credentialEnv,
		Config:             map[string]string(config),
		Image:              *image,
		Mode:               Mode(*mode),
		ListenAddress:      *listenAddress,
		PublicURL:          strings.TrimRight(*publicURL, "/"),
		MaxExpectedCostUSD: *maxCost,
		Timeout:            *timeout,
	}, nil
}

type configValues map[string]string

func (values *configValues) String() string { return "" }

func (values *configValues) Set(raw string) error {
	key, value, ok := strings.Cut(raw, "=")
	if !ok || strings.TrimSpace(key) == "" {
		return fmt.Errorf("provider config must be key=value, got %q", raw)
	}
	if *values == nil {
		*values = configValues{}
	}
	if _, exists := (*values)[key]; exists {
		return fmt.Errorf("provider config %q was supplied more than once", key)
	}
	(*values)[key] = value
	return nil
}

func writeCommandError(stderr io.Writer, err error) {
	if stderr == nil {
		return
	}
	_ = json.NewEncoder(stderr).Encode(struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{Code: "INVALID_ARGUMENT", Message: err.Error()})
}
