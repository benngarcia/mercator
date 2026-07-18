package vast

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const defaultBaseURL = "https://console.vast.ai"

type apiClient struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

func newAPIClient(apiKey, baseURL string, httpClient *http.Client) *apiClient {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &apiClient{baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, http: httpClient}
}

// offer is one marketplace ask from POST /api/v0/bundles/. Numeric fields are
// pointers where absence must be distinguishable from zero.
type offer struct {
	ID                int64    `json:"id"`
	GPUName           string   `json:"gpu_name"`
	GPUArch           string   `json:"gpu_arch"`
	NumGPUs           int      `json:"num_gpus"`
	GPURAMMb          float64  `json:"gpu_ram"`
	CPUCoresEffective float64  `json:"cpu_cores_effective"`
	CPURAMMb          float64  `json:"cpu_ram"`
	DiskSpaceGB       float64  `json:"disk_space"`
	DPHTotal          *float64 `json:"dph_total"`
	Reliability       float64  `json:"reliability2"`
	Verification      string   `json:"verification"`
	MachineID         int64    `json:"machine_id"`
	Geolocation       string   `json:"geolocation"`
	StaticIP          bool     `json:"static_ip"`
}

// instance is one row from the instances endpoints. extra_env round-trips the
// create request's env as [key, value] pairs.
type instance struct {
	ID            int64      `json:"id"`
	Label         string     `json:"label"`
	ActualStatus  string     `json:"actual_status"`
	IntendedState string     `json:"intended_status"`
	StatusMsg     string     `json:"status_msg"`
	Verification  string     `json:"verification"`
	PublicIP      string     `json:"public_ipaddr"`
	ExtraEnv      [][]string `json:"extra_env"`
}

// env flattens the instance's extra_env pairs into a map. Non-pair entries
// (port mappings ride the same channel as ["-p 8080:8080", "1"]) are kept
// verbatim keyed by their first element, which is harmless for MERCATOR_*
// lookups.
func (i instance) env() map[string]string {
	m := make(map[string]string, len(i.ExtraEnv))
	for _, kv := range i.ExtraEnv {
		if len(kv) == 2 {
			m[kv[0]] = kv[1]
		}
	}
	return m
}

type createInstanceInput struct {
	ClientID      string            `json:"client_id"`
	Image         string            `json:"image"`
	Env           map[string]string `json:"env"`
	Disk          float64           `json:"disk"`
	Label         string            `json:"label"`
	Runtype       string            `json:"runtype"`
	Args          []string          `json:"args,omitempty"`
	TargetState   string            `json:"target_state"`
	CancelUnavail bool              `json:"cancel_unavail"`
}

func (c *apiClient) do(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("vast: marshal request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("vast: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, respBody, nil
}

// httpError formats a non-2xx response WITHOUT ever including the request's
// Authorization header. Bodies are truncated to keep logs/errors bounded.
func httpError(method, path string, status int, body []byte) error {
	snippet := string(body)
	if len(snippet) > 300 {
		snippet = snippet[:300]
	}
	return fmt.Errorf("vast: %s %s -> %d: %s", method, path, status, snippet)
}

// ping validates the API key via the cheapest authenticated read.
func (c *apiClient) ping(ctx context.Context) error {
	status, body, err := c.do(ctx, http.MethodGet, "/api/v0/users/current/", nil)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return httpError(http.MethodGet, "/api/v0/users/current/", status, body)
	}
	return nil
}

func (c *apiClient) searchOffers(ctx context.Context, query map[string]any) ([]offer, error) {
	status, body, err := c.do(ctx, http.MethodPost, "/api/v0/bundles/", query)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, httpError(http.MethodPost, "/api/v0/bundles/", status, body)
	}
	var out struct {
		Offers []offer `json:"offers"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("vast: decode offers: %w", err)
	}
	return out.Offers, nil
}

func (c *apiClient) createInstance(ctx context.Context, askID int64, in createInstanceInput) (int64, error) {
	path := "/api/v0/asks/" + strconv.FormatInt(askID, 10) + "/"
	status, body, err := c.do(ctx, http.MethodPut, path, in)
	if err != nil {
		return 0, err
	}
	if status < 200 || status >= 300 {
		return 0, httpError(http.MethodPut, path, status, body)
	}
	var out struct {
		Success     bool   `json:"success"`
		NewContract int64  `json:"new_contract"`
		Msg         string `json:"msg"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return 0, fmt.Errorf("vast: decode create response: %w", err)
	}
	if !out.Success || out.NewContract == 0 {
		return 0, fmt.Errorf("vast: create instance on ask %d not accepted: %s", askID, out.Msg)
	}
	return out.NewContract, nil
}

// listInstances pages through /api/v1/instances/. An empty label lists every
// instance on the account; otherwise the label filters server-side (exact
// match).
func (c *apiClient) listInstances(ctx context.Context, label string) ([]instance, error) {
	filters := map[string]any{}
	if label != "" {
		filters = map[string]any{"label": map[string]any{"eq": label}}
	}
	filtersJSON, err := json.Marshal(filters)
	if err != nil {
		return nil, fmt.Errorf("vast: marshal filters: %w", err)
	}
	orderJSON := `[{"col":"id","dir":"asc"}]`
	var rows []instance
	afterToken := ""
	for {
		q := url.Values{
			"select_filters": {string(filtersJSON)},
			"order_by":       {orderJSON},
			"limit":          {"100"},
		}
		if afterToken != "" {
			q.Set("after_token", afterToken)
		}
		path := "/api/v1/instances/?" + q.Encode()
		status, body, err := c.do(ctx, http.MethodGet, path, nil)
		if err != nil {
			return nil, err
		}
		if status < 200 || status >= 300 {
			return nil, httpError(http.MethodGet, "/api/v1/instances/", status, body)
		}
		var out struct {
			Instances []instance `json:"instances"`
			NextToken string     `json:"next_token"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			return nil, fmt.Errorf("vast: decode instances: %w", err)
		}
		rows = append(rows, out.Instances...)
		if out.NextToken == "" {
			return rows, nil
		}
		afterToken = out.NextToken
	}
}

func (c *apiClient) destroyInstance(ctx context.Context, id int64) error {
	path := "/api/v0/instances/" + strconv.FormatInt(id, 10) + "/"
	status, body, err := c.do(ctx, http.MethodDelete, path, map[string]any{})
	if err != nil {
		return err
	}
	if status == http.StatusNotFound || (status >= 200 && status < 300) {
		return nil
	}
	return httpError(http.MethodDelete, path, status, body)
}
