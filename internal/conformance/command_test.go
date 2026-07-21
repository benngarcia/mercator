package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestVerifyCommandReadsATrialFileAndWritesEvidence(t *testing.T) {
	var received Trial
	var stdout, stderr bytes.Buffer

	exitCode := runCommand(context.Background(), []string{"--spec", "testdata/docker_trial.json"}, map[string]string{}, &stdout, &stderr,
		func(_ context.Context, trial Trial) (Evidence, error) {
			received = trial
			return Evidence{AdapterType: trial.AdapterType, Verdict: VerdictPassed}, nil
		})

	if exitCode != 0 {
		t.Fatalf("runCommand() = %d, stderr = %s", exitCode, stderr.String())
	}
	if received.AdapterType != "docker" || received.Mode != ModeProbe || received.Timeout != 12*time.Minute || received.MaxExpectedCostUSD != 0.5 {
		t.Fatalf("trial = %+v", received)
	}
	var evidence Evidence
	if err := json.Unmarshal(stdout.Bytes(), &evidence); err != nil {
		t.Fatalf("stdout = %q: %v", stdout.String(), err)
	}
	if evidence.Verdict != VerdictPassed {
		t.Fatalf("evidence = %+v", evidence)
	}
}

func TestVerifyCommandReadsLaunchCancelModeFromTheTrial(t *testing.T) {
	var received Trial
	exitCode := runCommand(context.Background(), []string{"--spec", "testdata/docker_cancel_trial.json"}, map[string]string{}, &bytes.Buffer{}, &bytes.Buffer{},
		func(_ context.Context, trial Trial) (Evidence, error) {
			received = trial
			return Evidence{Mode: trial.Mode, Verdict: VerdictPassed}, nil
		})

	if exitCode != 0 || received.Mode != ModeLaunchCancel {
		t.Fatalf("runCommand() = %d, trial = %+v", exitCode, received)
	}
}

func TestVerifyCommandPrintsHelpWithoutLaunching(t *testing.T) {
	var stdout bytes.Buffer

	exitCode := runCommand(context.Background(), []string{"--help"}, map[string]string{}, &stdout, &bytes.Buffer{},
		func(context.Context, Trial) (Evidence, error) {
			t.Fatal("Verify called for help")
			return Evidence{}, nil
		})

	if exitCode != 0 || !strings.Contains(stdout.String(), "mercator verify --spec FILE") {
		t.Fatalf("runCommand() = %d, stdout = %s", exitCode, stdout.String())
	}
}

func TestVerifyCommandRejectsUnknownTrialFields(t *testing.T) {
	var stdout, stderr bytes.Buffer

	exitCode := runCommand(context.Background(), []string{"--spec", "testdata/unknown_field_trial.json"}, map[string]string{}, &stdout, &stderr,
		func(context.Context, Trial) (Evidence, error) {
			t.Fatal("Verify called for an invalid trial document")
			return Evidence{}, nil
		})

	if exitCode != 2 {
		t.Fatalf("runCommand() = %d, stderr = %s", exitCode, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}

func TestVerifyCommandRejectsTrailingJSON(t *testing.T) {
	var stdout, stderr bytes.Buffer

	exitCode := runCommand(context.Background(), []string{"--spec", "testdata/trailing_json_trial.json"}, map[string]string{}, &stdout, &stderr,
		func(context.Context, Trial) (Evidence, error) {
			t.Fatal("Verify called for an invalid trial document")
			return Evidence{}, nil
		})

	if exitCode != 2 || !strings.Contains(stderr.String(), "expected one JSON object") {
		t.Fatalf("runCommand() = %d, stderr = %s", exitCode, stderr.String())
	}
}

func TestVerifyCommandRedactsOnlyTheSelectedCredential(t *testing.T) {
	var stdout bytes.Buffer
	environment := map[string]string{
		"RUNPOD_API_KEY": "secret-provider-token",
		"HOME":           "/Users/operator",
	}

	exitCode := runCommand(context.Background(), []string{"--spec", "testdata/runpod_trial.json"}, environment, &stdout, &bytes.Buffer{},
		func(context.Context, Trial) (Evidence, error) {
			return Evidence{CleanupFailure: &TrialFailure{Code: "CLEANUP_FAILED", Message: "cleanup secret-provider-token"}}, errors.New("provider secret-provider-token failed under /Users/operator")
		})

	if exitCode != 1 {
		t.Fatalf("runCommand() = %d", exitCode)
	}
	if strings.Contains(stdout.String(), "secret-provider-token") || !strings.Contains(stdout.String(), "/Users/operator") || !strings.Contains(stdout.String(), "cleanup [REDACTED]") {
		t.Fatalf("stdout = %s", stdout.String())
	}
}
