package conformanceprobe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const UserAgent = "mercator-conformance-probe/1"

type configuration struct {
	reportURL string
	runID     string
	runToken  string
}

type report struct {
	Type     string     `json:"type"`
	Data     *readyData `json:"data,omitempty"`
	ExitCode *int       `json:"exit_code,omitempty"`
}

// readyData is what a readiness report carries. The moment is required, because
// application readiness is the last stage of a launch and the application is the
// only thing that can say when it happened: a report that said only "ready" left
// that stage with no actual, which is the untyped callback this field replaced.
type readyData struct {
	Scenario string    `json:"scenario"`
	ReadyAt  time.Time `json:"ready_at"`
}

func Run(ctx context.Context, args []string, env map[string]string, _ io.Writer, stderr io.Writer) int {
	if len(args) != 1 || (args[0] != "success" && args[0] != "wait-for-cancel") {
		_, _ = fmt.Fprintln(stderr, "usage: mercator-conformance-probe success | wait-for-cancel")
		return 2
	}
	config, err := configurationFromEnvironment(env)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}
	reporter := newReporter(config)
	ready := readyData{Scenario: args[0], ReadyAt: time.Now().UTC()}
	if err := reporter.post(ctx, report{Type: "ready", Data: &ready}); err != nil {
		if args[0] == "wait-for-cancel" && ctx.Err() != nil {
			return 0
		}
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	if args[0] == "wait-for-cancel" {
		<-ctx.Done()
		return 0
	}
	exitCode := 0
	if err := reporter.post(ctx, report{Type: "exit", ExitCode: &exitCode}); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func configurationFromEnvironment(env map[string]string) (configuration, error) {
	config := configuration{
		reportURL: env["MERCATOR_REPORT_URL"],
		runID:     env["MERCATOR_RUN_ID"],
		runToken:  env["MERCATOR_RUN_TOKEN"],
	}
	missing := make([]string, 0, 3)
	for name, value := range map[string]string{
		"MERCATOR_REPORT_URL": config.reportURL,
		"MERCATOR_RUN_ID":     config.runID,
		"MERCATOR_RUN_TOKEN":  config.runToken,
	} {
		if strings.TrimSpace(value) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return configuration{}, fmt.Errorf("missing required reporting environment: %s", strings.Join(missing, ", "))
	}
	return config, nil
}

type reporter struct {
	endpoint string
	token    string
	client   *http.Client
}

func newReporter(config configuration) reporter {
	base := strings.TrimRight(config.reportURL, "/")
	endpoint := base + "/v1/runs/" + url.PathEscape(config.runID) + "/report"
	return reporter{endpoint: endpoint, token: config.runToken, client: http.DefaultClient}
}

func (r reporter) post(ctx context.Context, payload report) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode report: %w", err)
	}
	var lastErr error
	const maxAttempts = 5
	for attempt := 0; attempt < maxAttempts; attempt++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, r.endpoint, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("create report request: %w", err)
		}
		request.Header.Set("Authorization", "Bearer "+r.token)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("User-Agent", UserAgent)
		response, err := r.client.Do(request)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusAccepted {
				return nil
			}
			err = fmt.Errorf("unexpected HTTP status %d", response.StatusCode)
		}
		lastErr = err
		if attempt == maxAttempts-1 {
			break
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return fmt.Errorf("send report after %d attempts: %w", maxAttempts, lastErr)
}
