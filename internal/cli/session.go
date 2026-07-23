package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// LocalBrokerURL is where `mercator serve` listens unless told otherwise, so it
// is also where the CLI looks when nothing else names a broker.
const LocalBrokerURL = "http://127.0.0.1:8080"

// session is one CLI invocation's connection to a broker: where it points, how
// it authenticates, and the facts it can discover for itself. An operator
// should have to name only what is genuinely ambiguous, so the workspace and
// the run resolve on demand whenever there is exactly one sensible answer.
type session struct {
	baseURL string
	token   string
	client  *http.Client

	// workspaceID is seeded from the environment or the current context, and
	// filled in by workspace() when neither named one.
	workspaceID string
}

// workspace returns the workspace to act on. A named workspace always wins.
// Otherwise a broker holding exactly one workspace has nothing to
// disambiguate; a broker holding several names them instead of guessing.
func (s *session) workspace(ctx context.Context) (string, error) {
	if s.workspaceID != "" {
		return s.workspaceID, nil
	}
	var page struct {
		Workspaces []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"workspaces"`
	}
	if err := s.get(ctx, "/v1/workspaces", nil, &page); err != nil {
		return "", fmt.Errorf("look up workspaces: %w", err)
	}
	switch len(page.Workspaces) {
	case 0:
		return "", fmt.Errorf("this broker has no workspaces")
	case 1:
		s.workspaceID = page.Workspaces[0].ID
		return s.workspaceID, nil
	}
	names := make([]string, 0, len(page.Workspaces))
	for _, item := range page.Workspaces {
		names = append(names, item.ID)
	}
	return "", fmt.Errorf("this broker has %d workspaces; pass --workspace-id or set MERCATOR_WORKSPACE_ID (%s)",
		len(page.Workspaces), strings.Join(names, ", "))
}

// latestRun returns the most recent run in a workspace, which is what "the run
// I just started" means at a prompt. Run ids are UUIDv7, so the id order the
// API returns is creation order.
func (s *session) latestRun(ctx context.Context, workspaceID string) (string, error) {
	var page struct {
		Runs []struct {
			ID string `json:"id"`
		} `json:"runs"`
	}
	if err := s.get(ctx, "/v1/runs", query("workspace_id", workspaceID), &page); err != nil {
		return "", fmt.Errorf("look up runs: %w", err)
	}
	if len(page.Runs) == 0 {
		return "", fmt.Errorf("workspace %s has no runs yet", workspaceID)
	}
	return page.Runs[len(page.Runs)-1].ID, nil
}

// soleConnection returns the workspace's only connection. A workspace with one
// provider registered has nothing to disambiguate; more than one has to be
// named, because authorizing or deleting the wrong provider is not recoverable
// by retrying.
func (s *session) soleConnection(ctx context.Context, workspaceID string) (string, error) {
	var page struct {
		Connections []struct {
			ID string `json:"id"`
		} `json:"connections"`
	}
	if err := s.get(ctx, "/v1/connections", query("workspace_id", workspaceID), &page); err != nil {
		return "", fmt.Errorf("look up connections: %w", err)
	}
	switch len(page.Connections) {
	case 0:
		return "", fmt.Errorf("workspace %s has no connections", workspaceID)
	case 1:
		return page.Connections[0].ID, nil
	}
	names := make([]string, 0, len(page.Connections))
	for _, item := range page.Connections {
		names = append(names, item.ID)
	}
	return "", fmt.Errorf("workspace %s has %d connections; pass --connection-id (%s)",
		workspaceID, len(page.Connections), strings.Join(names, ", "))
}

// get performs one authenticated read against the broker and decodes it. It is
// only used for the lookups that spare an operator from restating an id the
// broker already knows.
func (s *session) get(ctx context.Context, path string, params url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mustURL(s.baseURL, path, params), nil)
	if err != nil {
		return err
	}
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	// These lookups exist to save typing, so their failures must name the real
	// problem rather than the lookup that tripped over it.
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("%s rejected the credential; set MERCATOR_API_TOKEN or run `mercator login`", s.baseURL)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("%s returned %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.Unmarshal(body, out)
}
