package shadeform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultBaseURL = "https://api.shadeform.ai/v1"

// errCreateIndeterminate marks a create whose outcome is unknown: a 5xx or
// transport failure after the request may have executed. Launch translates it
// (after failing to adopt a landed instance) into adapter.ErrLaunchIndeterminate
// so the orchestrator keeps observing instead of closing the run cleanup-free.
var errCreateIndeterminate = errors.New("shadeform: create outcome indeterminate")

type client struct {
	baseURL string
	apiKey  string
	http    *http.Client
	// backoff is the base delay before the first retry; each further retry
	// doubles it. Tests set it to 0.
	backoff time.Duration
}

func newClient(apiKey, baseURL string, httpClient *http.Client) *client {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		http:    httpClient,
		backoff: 500 * time.Millisecond,
	}
}

type instance struct {
	ID                string    `json:"id"`
	Cloud             string    `json:"cloud"`
	Region            string    `json:"region"`
	ShadeInstanceType string    `json:"shade_instance_type"`
	ShadeCloud        bool      `json:"shade_cloud"`
	Name              string    `json:"name"`
	Status            string    `json:"status"`
	Tags              []string  `json:"tags,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
}

type availability struct {
	Region    string `json:"region"`
	Available bool   `json:"available"`
}

type bootTime struct {
	MinBootInSec int `json:"min_boot_in_sec"`
	MaxBootInSec int `json:"max_boot_in_sec"`
}

type typeConfiguration struct {
	MemoryInGB      int      `json:"memory_in_gb"`
	StorageInGB     int      `json:"storage_in_gb"`
	VCPUs           int      `json:"vcpus"`
	NumGPUs         int      `json:"num_gpus"`
	GPUType         string   `json:"gpu_type"`
	VRAMPerGPUInGB  int      `json:"vram_per_gpu_in_gb"`
	GPUManufacturer string   `json:"gpu_manufacturer"`
	OSOptions       []string `json:"os_options"`
}

type instanceType struct {
	Cloud             string            `json:"cloud"`
	ShadeInstanceType string            `json:"shade_instance_type"`
	Configuration     typeConfiguration `json:"configuration"`
	HourlyPrice       int               `json:"hourly_price"`
	DeploymentType    string            `json:"deployment_type"`
	Availability      []availability    `json:"availability"`
	BootTime          *bootTime         `json:"boot_time,omitempty"`
}

type envVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type registryCredentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type dockerConfiguration struct {
	Image               string               `json:"image"`
	Args                string               `json:"args,omitempty"`
	Envs                []envVar             `json:"envs,omitempty"`
	RegistryCredentials *registryCredentials `json:"registry_credentials,omitempty"`
}

type launchConfiguration struct {
	Type                string               `json:"type"`
	DockerConfiguration *dockerConfiguration `json:"docker_configuration,omitempty"`
}

type autoDelete struct {
	DateThreshold  string `json:"date_threshold,omitempty"`
	SpendThreshold string `json:"spend_threshold,omitempty"`
}

type createRequest struct {
	Cloud               string               `json:"cloud"`
	Region              string               `json:"region"`
	ShadeInstanceType   string               `json:"shade_instance_type"`
	ShadeCloud          bool                 `json:"shade_cloud"`
	Name                string               `json:"name"`
	OS                  string               `json:"os,omitempty"`
	LaunchConfiguration *launchConfiguration `json:"launch_configuration,omitempty"`
	AutoDelete          *autoDelete          `json:"auto_delete,omitempty"`
	Tags                []string             `json:"tags,omitempty"`
}

func (c *client) do(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("shadeform: marshal request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("X-API-KEY", c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("shadeform: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, respBody, nil
}

// doRetry runs the request with exponential backoff. A 429 is always retried:
// the request was rejected before execution, so repeating it is safe even for
// create. 5xx and transport errors are retried only when retry5xx is set —
// safe for reads and deletes (which converge on the same terminal state), NOT
// for create, whose outcome after such a failure is indeterminate and must be
// reconciled by re-listing rather than blind retry. Shadeform documents no
// rate limits; the 429 handling is conservative insurance.
func (c *client) doRetry(ctx context.Context, method, path string, body any, retry5xx bool) (int, []byte, error) {
	const attempts = 4
	var status int
	var respBody []byte
	var err error
	for i := range attempts {
		status, respBody, err = c.do(ctx, method, path, body)
		transient := status == http.StatusTooManyRequests || (retry5xx && (err != nil || status >= 500))
		if !transient {
			return status, respBody, err
		}
		if i < attempts-1 {
			if werr := c.wait(ctx, i); werr != nil {
				return 0, nil, werr
			}
		}
	}
	return status, respBody, err
}

func (c *client) wait(ctx context.Context, attempt int) error {
	delay := c.backoff << attempt
	if delay <= 0 {
		return ctx.Err()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(delay):
		return nil
	}
}

// httpError formats a non-2xx response WITHOUT ever including the request's
// X-API-KEY header. Bodies are truncated to keep logs/errors bounded.
func httpError(method, path string, status int, body []byte) error {
	snippet := string(body)
	if len(snippet) > 300 {
		snippet = snippet[:300]
	}
	return fmt.Errorf("shadeform: %s %s -> %d: %s", method, path, status, snippet)
}

// getJSON performs an idempotent request and decodes the 2xx body into out.
func (c *client) getJSON(ctx context.Context, method, path string, out any) error {
	status, body, err := c.doRetry(ctx, method, path, nil, true)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return httpError(method, path, status, body)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("shadeform: decode %s: %w", path, err)
	}
	return nil
}

func (c *client) listInstances(ctx context.Context) ([]instance, error) {
	var out struct {
		Instances []instance `json:"instances"`
	}
	if err := c.getJSON(ctx, http.MethodGet, "/instances", &out); err != nil {
		return nil, err
	}
	return out.Instances, nil
}

func (c *client) instanceTypes(ctx context.Context, query url.Values) ([]instanceType, error) {
	path := "/instances/types"
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	var out struct {
		InstanceTypes []instanceType `json:"instance_types"`
	}
	if err := c.getJSON(ctx, http.MethodGet, path, &out); err != nil {
		return nil, err
	}
	return out.InstanceTypes, nil
}

func (c *client) createInstance(ctx context.Context, in createRequest) (string, error) {
	status, body, err := c.doRetry(ctx, http.MethodPost, "/instances/create", in, false)
	if err != nil {
		return "", fmt.Errorf("%w: %v", errCreateIndeterminate, err)
	}
	if status >= 500 {
		return "", fmt.Errorf("%w: %v", errCreateIndeterminate, httpError(http.MethodPost, "/instances/create", status, body))
	}
	if status < 200 || status >= 300 {
		return "", httpError(http.MethodPost, "/instances/create", status, body)
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("shadeform: decode create response: %w", err)
	}
	if out.ID == "" {
		return "", fmt.Errorf("shadeform: create response missing instance id")
	}
	return out.ID, nil
}

func (c *client) deleteInstance(ctx context.Context, id string) error {
	path := "/instances/" + id + "/delete"
	status, body, err := c.doRetry(ctx, http.MethodPost, path, nil, true)
	if err != nil {
		return err
	}
	if status == http.StatusNotFound || (status >= 200 && status < 300) {
		return nil
	}
	return httpError(http.MethodPost, path, status, body)
}
