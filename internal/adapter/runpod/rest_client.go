package runpod

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const defaultRESTBaseURL = "https://rest.runpod.io/v1"

type restClient struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

func newRESTClient(apiKey, baseURL string, httpClient *http.Client) *restClient {
	if baseURL == "" {
		baseURL = defaultRESTBaseURL
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &restClient{baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, http: httpClient}
}

// flexEnv decodes RunPod's pod env, which the API may return either as a JSON
// object ({"K":"V"}) or as an array of "K=V" strings. We always present it as a
// map internally.
type flexEnv map[string]string

func (e *flexEnv) UnmarshalJSON(b []byte) error {
	trimmed := strings.TrimSpace(string(b))
	if trimmed == "null" {
		*e = nil
		return nil
	}
	if strings.HasPrefix(trimmed, "{") {
		var m map[string]string
		if err := json.Unmarshal(b, &m); err != nil {
			return err
		}
		*e = m
		return nil
	}
	var arr []string
	if err := json.Unmarshal(b, &arr); err != nil {
		return err
	}
	m := make(map[string]string, len(arr))
	for _, kv := range arr {
		if i := strings.IndexByte(kv, '='); i >= 0 {
			m[kv[:i]] = kv[i+1:]
		}
	}
	*e = m
	return nil
}

type podCreateInput struct {
	Name             string            `json:"name"`
	ImageName        string            `json:"imageName"`
	GPUTypeIDs       []string          `json:"gpuTypeIds,omitempty"`
	GPUCount         int               `json:"gpuCount,omitempty"`
	ContainerDiskGB  int               `json:"containerDiskInGb,omitempty"`
	CloudType        string            `json:"cloudType,omitempty"`
	Env              map[string]string `json:"env,omitempty"`
	Ports            []string          `json:"ports,omitempty"`
	DockerEntrypoint []string          `json:"dockerEntrypoint,omitempty"`
	DockerStartCmd   []string          `json:"dockerStartCmd,omitempty"`
}

type pod struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Image         string  `json:"image"`
	DesiredStatus string  `json:"desiredStatus"`
	PublicIP      string  `json:"publicIp"`
	Env           flexEnv `json:"env"`
	CostPerHr     float64 `json:"costPerHr"`
}

func (c *restClient) do(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("runpod: marshal request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("runpod: %s %s: %w", method, path, err)
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
	return fmt.Errorf("runpod: %s %s -> %d: %s", method, path, status, snippet)
}

func (c *restClient) createPod(ctx context.Context, in podCreateInput) (pod, error) {
	status, body, err := c.do(ctx, http.MethodPost, "/pods", in)
	if err != nil {
		return pod{}, err
	}
	if status < 200 || status >= 300 {
		return pod{}, httpError(http.MethodPost, "/pods", status, body)
	}
	var p pod
	if err := json.Unmarshal(body, &p); err != nil {
		return pod{}, fmt.Errorf("runpod: decode pod: %w", err)
	}
	return p, nil
}

func (c *restClient) listPodsByName(ctx context.Context, namePrefix string) ([]pod, error) {
	q := url.Values{"name": {namePrefix}}
	status, body, err := c.do(ctx, http.MethodGet, "/pods?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, httpError(http.MethodGet, "/pods", status, body)
	}
	var pods []pod
	if err := json.Unmarshal(body, &pods); err != nil {
		return nil, fmt.Errorf("runpod: decode pods: %w", err)
	}
	// RunPod's name filter is not an exact/prefix guarantee; enforce the prefix
	// defensively so we never act on a pod that isn't ours.
	filtered := pods[:0]
	for _, p := range pods {
		if strings.HasPrefix(p.Name, namePrefix) {
			filtered = append(filtered, p)
		}
	}
	return filtered, nil
}

func (c *restClient) deletePod(ctx context.Context, id string) error {
	status, body, err := c.do(ctx, http.MethodDelete, "/pods/"+id, nil)
	if err != nil {
		return err
	}
	if status == http.StatusNotFound || (status >= 200 && status < 300) {
		return nil
	}
	return httpError(http.MethodDelete, "/pods/"+id, status, body)
}

func (c *restClient) ping(ctx context.Context) error {
	status, body, err := c.do(ctx, http.MethodGet, "/pods", nil)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return httpError(http.MethodGet, "/pods", status, body)
	}
	return nil
}
