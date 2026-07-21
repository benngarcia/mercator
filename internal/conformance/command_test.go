package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestVerifyConnectionCommandReportsTheTrialVerdict(t *testing.T) {
	tests := []struct {
		name     string
		verdict  Verdict
		wantCode int
	}{
		{name: "passed", verdict: VerdictPassed, wantCode: 0},
		{name: "failed", verdict: VerdictFailed, wantCode: 1},
		{name: "blocked", verdict: VerdictBlocked, wantCode: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var gotSpec TrialSpec
			var stdout, stderr bytes.Buffer
			code := RunCommand(context.Background(), CommandConfig{
				Args: []string{
					"connection",
					"--adapter", "runpod",
					"--credential-env", "RUNPOD_API_KEY",
					"--image", "ghcr.io/benngarcia/mercator-conformance@sha256:0000000000000000000000000000000000000000000000000000000000000000",
					"--listen-address", "127.0.0.1:8091",
					"--public-url", "https://mercator-conformance.example.com",
					"--max-expected-cost-usd", "0.50",
					"--timeout", "12m",
					"--json",
				},
				Environment: map[string]string{"RUNPOD_API_KEY": "sentinel-secret"},
				Stdout:      &stdout,
				Stderr:      &stderr,
				RunTrial: func(_ context.Context, spec TrialSpec) (TrialReport, error) {
					gotSpec = spec
					return TrialReport{
						TrialID:      "trial_fixture",
						WorkspaceID:  "ws_fixture",
						ConnectionID: "conn_fixture",
						AdapterType:  "runpod",
						Verdict:      test.verdict,
					}, nil
				},
			})

			if code != test.wantCode {
				t.Fatalf("RunCommand() = %d, want %d; stderr=%s", code, test.wantCode, stderr.String())
			}
			if stderr.Len() != 0 {
				t.Fatalf("stderr = %q, want empty", stderr.String())
			}
			if gotSpec.AdapterType != "runpod" || gotSpec.CredentialEnv != "RUNPOD_API_KEY" {
				t.Fatalf("trial spec = %+v", gotSpec)
			}
			if gotSpec.ListenAddress != "127.0.0.1:8091" || gotSpec.PublicURL != "https://mercator-conformance.example.com" {
				t.Fatalf("trial topology = %+v", gotSpec)
			}
			if gotSpec.Image != "ghcr.io/benngarcia/mercator-conformance@sha256:0000000000000000000000000000000000000000000000000000000000000000" {
				t.Fatalf("image = %q", gotSpec.Image)
			}

			var report TrialReport
			if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
				t.Fatalf("stdout is not a trial report: %q: %v", stdout.String(), err)
			}
			if report.Verdict != test.verdict {
				t.Fatalf("verdict = %q, want %q", report.Verdict, test.verdict)
			}
			if strings.Contains(stdout.String()+stderr.String(), "sentinel-secret") {
				t.Fatal("command output leaked the provider credential")
			}
		})
	}
}

func TestVerifyConnectionCommandRejectsMissingCredentialBeforeStartingATrial(t *testing.T) {
	called := false
	var stdout, stderr bytes.Buffer

	code := RunCommand(context.Background(), CommandConfig{
		Args: []string{
			"connection",
			"--adapter", "vast",
			"--credential-env", "VAST_API_KEY",
			"--image", "ghcr.io/benngarcia/mercator-conformance@sha256:0000000000000000000000000000000000000000000000000000000000000000",
			"--listen-address", "127.0.0.1:8091",
			"--public-url", "https://mercator-conformance.example.com",
			"--json",
		},
		Environment: map[string]string{},
		Stdout:      &stdout,
		Stderr:      &stderr,
		RunTrial: func(context.Context, TrialSpec) (TrialReport, error) {
			called = true
			return TrialReport{}, nil
		},
	})

	if code != 2 {
		t.Fatalf("RunCommand() = %d, want 2", code)
	}
	if called {
		t.Fatal("trial started with a missing provider credential")
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "VAST_API_KEY") {
		t.Fatalf("stderr = %q, want the missing environment variable name", stderr.String())
	}
}

func TestVerifyConnectionCommandRetainsPartialEvidenceWhenTrialSetupFails(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := RunCommand(context.Background(), CommandConfig{
		Args: []string{
			"connection",
			"--adapter", "docker",
			"--image", "ghcr.io/benngarcia/mercator-conformance@sha256:0000000000000000000000000000000000000000000000000000000000000000",
			"--json",
		},
		Environment: map[string]string{},
		Stdout:      &stdout,
		Stderr:      &stderr,
		RunTrial: func(context.Context, TrialSpec) (TrialReport, error) {
			return TrialReport{TrialID: "trial_retained", WorkspaceID: "ws_retained"}, errors.New("bind trial listener: address unavailable")
		},
	})

	if code != 1 || stderr.Len() != 0 {
		t.Fatalf("code = %d stderr = %q, want structured trial failure", code, stderr.String())
	}
	var report TrialReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	if report.TrialID != "trial_retained" || report.WorkspaceID != "ws_retained" {
		t.Fatalf("partial evidence was discarded: %+v", report)
	}
	if report.Failure == nil || report.Failure.Code != "TRIAL_SETUP_FAILED" || !strings.Contains(report.Failure.Message, "address unavailable") {
		t.Fatalf("failure = %+v, want actionable setup diagnostic", report.Failure)
	}
}
