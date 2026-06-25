# RunPod Adapter Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `runpod` as the first real cloud-provider adapter and make a workload's self-reported exit code the authoritative run outcome (RunPod exposes no container exit code).

**Architecture:** A new `internal/adapter/runpod` package implements `adapter.Adapter` using two HTTP clients — REST (`rest.runpod.io/v1`) for pod create/get/list/delete and GraphQL (`api.runpod.io/graphql`) only for `gpuTypes` pricing/availability. Ownership is encoded in the pod `name` (`mercator-<launchKey>`) plus pod `env` (RunPod has no tags). A small additive orchestrator change makes an exit report record the authoritative outcome and request prompt cleanup; the existing Observe path is the backstop. The adapter is registered in the broker factory; the operator adds a RunPod connection through the existing UI. Two live example workloads validate the path end-to-end.

**Tech Stack:** Go 1.25, stdlib `net/http` + `encoding/json` only (no RunPod SDK, no new deps), module `github.com/benngarcia/mercator`.

## Global Constraints

- Module path is `github.com/benngarcia/mercator`. Pure Go, stdlib only for the adapter — **no new third-party dependencies**.
- The RunPod API key is a secret: it is **only** ever passed via the resolved connection credential (`RUNPOD_API_KEY` env in practice). It must **never** be written to the repo, the event log, any read API, or any log line. Error messages may include HTTP status and a truncated response body but **never** the `Authorization` header or key.
- REST base URL default `https://rest.runpod.io/v1`; GraphQL base URL default `https://api.runpod.io/graphql`. Both overridable via connection config keys `rest_base_url` / `graphql_base_url` (for tests).
- Auth header for both transports: `Authorization: Bearer <secret>` plus `Content-Type: application/json`.
- RunPod offers are `domain.OfferKindProvisionable` (⇒ disposition `terminate` ⇒ cleanup routes `Terminate` → `DELETE /pods/{id}`).
- Ownership: pod `name = "mercator-" + req.LaunchKey`; pod `env` also carries `MERCATOR_OWNERSHIP_TOKEN`, `MERCATOR_REQUEST_HASH`, `MERCATOR_WORKSPACE_ID`, `MERCATOR_RUN_ID`, `MERCATOR_ATTEMPT_ID`, `MERCATOR_LAUNCH_KEY`, `MERCATOR_CLEANUP_LOCATOR`.
- Default GPU allow-list: `["NVIDIA RTX A2000","NVIDIA RTX A4000"]` (overridable via connection config `gpu_types`, comma-separated). Default `cloud_type` `COMMUNITY`; default `container_disk_gb` `20`.
- All Go code must be `gofmt`-clean. TDD: write the failing test first, watch it fail, implement minimally, watch it pass, commit.
- Run `gofmt -l .` (expect no output) and `go vet ./...` before each commit.

---

## File Structure

| File | Responsibility |
|------|----------------|
| `internal/adapter/runpod/helpers_test.go` | Shared test transport (`roundTripFunc`, `jsonResponse`, `newFakeHTTPClient`). |
| `internal/adapter/runpod/rest_client.go` | REST client: `createPod`/`getPod`/`listPodsByName`/`deletePod`/`ping`; `flexEnv` (object-or-array env decode). |
| `internal/adapter/runpod/graphql_client.go` | GraphQL client: `gpuTypes()` only. |
| `internal/adapter/runpod/offers.go` | `buildOffers(gpus, allowlist, now)` → `[]domain.OfferSnapshot`. |
| `internal/adapter/runpod/runpod.go` | `Adapter` implementing `adapter.Adapter`, composing the clients. |
| `internal/orchestrator/orchestrator.go` | Additive: capture reported exit code in `reduceRun`; `finalizeReportedExit` driven from `RecordReport`. |
| `cmd/mercator/main.go` | Register `factory.Register("runpod", ...)`. |
| `docs/production/runpod.md` | Operator runbook + live verification checklist. |
| `examples/runpod/busybox-report/README.md` | Raw-HTTP reporting workload (start command). |
| `examples/runpod/python-sdk/run.py` + `README.md` | Custom-event workload using the Mercator Python SDK. |

---

## Task 1: RunPod REST client

**Files:**
- Create: `internal/adapter/runpod/rest_client.go`
- Create: `internal/adapter/runpod/helpers_test.go`
- Test: `internal/adapter/runpod/rest_client_test.go`

**Interfaces:**
- Consumes: nothing (stdlib only).
- Produces:
  - `type restClient struct{...}` with `func newRESTClient(apiKey, baseURL string, httpClient *http.Client) *restClient`
  - `type podCreateInput struct{...}` (JSON-tagged for RunPod `POST /pods`)
  - `type pod struct{ ID, Name, Image, DesiredStatus, PublicIP string; Env flexEnv; CostPerHr float64 }`
  - `type flexEnv map[string]string` with `UnmarshalJSON` accepting a JSON object **or** an array of `"K=V"` strings
  - `func (c *restClient) createPod(ctx, in podCreateInput) (pod, error)`
  - `func (c *restClient) getPod(ctx, id string) (pod, error)` (HTTP 404 → `errPodNotFound`)
  - `func (c *restClient) listPodsByName(ctx, namePrefix string) ([]pod, error)`
  - `func (c *restClient) deletePod(ctx, id string) error` (204 or 404 → nil)
  - `func (c *restClient) ping(ctx) error` (cheap authed `GET /pods`; 401 → error)
  - `var errPodNotFound = errors.New("runpod: pod not found")`
  - Test helpers in `helpers_test.go`: `type roundTripFunc func(*http.Request) (*http.Response, error)` (implements `http.RoundTripper`), `func jsonResponse(status int, body string) *http.Response`, `func newFakeHTTPClient(fn roundTripFunc) *http.Client`.

- [ ] **Step 1: Write the shared test helpers** (`helpers_test.go`)

```go
package runpod

import (
	"io"
	"net/http"
	"strings"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func newFakeHTTPClient(fn roundTripFunc) *http.Client {
	return &http.Client{Transport: fn}
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}
```

- [ ] **Step 2: Write the failing tests** (`rest_client_test.go`)

```go
package runpod

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestCreatePodSendsBearerAndBody(t *testing.T) {
	var gotAuth, gotPath, gotMethod, gotBody string
	client := newRESTClient("secret-key", "https://rest.test/v1", newFakeHTTPClient(func(r *http.Request) (*http.Response, error) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		gotMethod = r.Method
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		return jsonResponse(201, `{"id":"pod_1","name":"mercator-lk","desiredStatus":"RUNNING"}`), nil
	}))

	p, err := client.createPod(context.Background(), podCreateInput{
		Name:       "mercator-lk",
		ImageName:  "busybox",
		GPUTypeIDs: []string{"NVIDIA RTX A2000"},
		Env:        map[string]string{"MERCATOR_RUN_ID": "run_1"},
	})
	if err != nil {
		t.Fatalf("createPod: %v", err)
	}
	if gotAuth != "Bearer secret-key" {
		t.Errorf("auth header = %q, want Bearer secret-key", gotAuth)
	}
	if gotMethod != http.MethodPost || gotPath != "/v1/pods" {
		t.Errorf("method/path = %s %s, want POST /v1/pods", gotMethod, gotPath)
	}
	if !strings.Contains(gotBody, `"imageName":"busybox"`) || !strings.Contains(gotBody, `"MERCATOR_RUN_ID":"run_1"`) {
		t.Errorf("body missing fields: %s", gotBody)
	}
	if p.ID != "pod_1" || p.DesiredStatus != "RUNNING" {
		t.Errorf("decoded pod = %+v", p)
	}
}

func TestGetPodNotFound(t *testing.T) {
	client := newRESTClient("k", "https://rest.test/v1", newFakeHTTPClient(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(404, `{"error":"not found"}`), nil
	}))
	_, err := client.getPod(context.Background(), "pod_x")
	if !errors.Is(err, errPodNotFound) {
		t.Fatalf("expected errPodNotFound, got %v", err)
	}
}

func TestListPodsByNameFiltersPrefixClientSide(t *testing.T) {
	client := newRESTClient("k", "https://rest.test/v1", newFakeHTTPClient(func(r *http.Request) (*http.Response, error) {
		if got := r.URL.Query().Get("name"); got != "mercator-" {
			t.Errorf("name filter = %q, want mercator-", got)
		}
		// RunPod's filter is non-exact; include a non-matching pod to prove the
		// defensive client-side prefix check.
		return jsonResponse(200, `[{"id":"p1","name":"mercator-lk1"},{"id":"p2","name":"someone-else"}]`), nil
	}))
	pods, err := client.listPodsByName(context.Background(), "mercator-")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(pods) != 1 || pods[0].ID != "p1" {
		t.Fatalf("expected only the prefixed pod, got %+v", pods)
	}
}

func TestDeletePodTreats404AsSuccess(t *testing.T) {
	client := newRESTClient("k", "https://rest.test/v1", newFakeHTTPClient(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s, want DELETE", r.Method)
		}
		return jsonResponse(404, ``), nil
	}))
	if err := client.deletePod(context.Background(), "pod_gone"); err != nil {
		t.Fatalf("delete 404 should be nil, got %v", err)
	}
}

func TestPingRejectsUnauthorized(t *testing.T) {
	client := newRESTClient("bad", "https://rest.test/v1", newFakeHTTPClient(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(401, `{"error":"unauthorized"}`), nil
	}))
	if err := client.ping(context.Background()); err == nil {
		t.Fatal("ping with 401 must error")
	}
}

func TestFlexEnvDecodesObjectAndArray(t *testing.T) {
	var obj flexEnv
	if err := json.Unmarshal([]byte(`{"A":"1","B":"2"}`), &obj); err != nil {
		t.Fatalf("object: %v", err)
	}
	if obj["A"] != "1" || obj["B"] != "2" {
		t.Fatalf("object decode = %+v", obj)
	}
	var arr flexEnv
	if err := json.Unmarshal([]byte(`["A=1","B=2=extra"]`), &arr); err != nil {
		t.Fatalf("array: %v", err)
	}
	if arr["A"] != "1" || arr["B"] != "2=extra" {
		t.Fatalf("array decode = %+v", arr)
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/adapter/runpod/`
Expected: FAIL — `undefined: newRESTClient` (and the other symbols).

- [ ] **Step 4: Implement `rest_client.go`**

```go
package runpod

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const defaultRESTBaseURL = "https://rest.runpod.io/v1"

var errPodNotFound = errors.New("runpod: pod not found")

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

func (c *restClient) getPod(ctx context.Context, id string) (pod, error) {
	status, body, err := c.do(ctx, http.MethodGet, "/pods/"+id, nil)
	if err != nil {
		return pod{}, err
	}
	if status == http.StatusNotFound {
		return pod{}, errPodNotFound
	}
	if status < 200 || status >= 300 {
		return pod{}, httpError(http.MethodGet, "/pods/"+id, status, body)
	}
	var p pod
	if err := json.Unmarshal(body, &p); err != nil {
		return pod{}, fmt.Errorf("runpod: decode pod: %w", err)
	}
	return p, nil
}

func (c *restClient) listPodsByName(ctx context.Context, namePrefix string) ([]pod, error) {
	status, body, err := c.do(ctx, http.MethodGet, "/pods?name="+url.QueryEscape(namePrefix), nil)
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
```

Note: add `"net/url"` to the import block (used by `listPodsByName`).

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/adapter/runpod/`
Expected: PASS.

- [ ] **Step 6: gofmt/vet + Commit**

```bash
gofmt -w internal/adapter/runpod/
go vet ./internal/adapter/runpod/
git add internal/adapter/runpod/rest_client.go internal/adapter/runpod/helpers_test.go internal/adapter/runpod/rest_client_test.go
git commit -m "feat(runpod): REST client for pod create/get/list/delete"
```

---

## Task 2: RunPod GraphQL client (`gpuTypes`)

**Files:**
- Create: `internal/adapter/runpod/graphql_client.go`
- Test: `internal/adapter/runpod/graphql_client_test.go`

**Interfaces:**
- Consumes: `roundTripFunc`/`jsonResponse`/`newFakeHTTPClient` from Task 1's `helpers_test.go`.
- Produces:
  - `type gpuType struct{ ID, DisplayName string; MemoryInGb int; CommunityPrice *float64; SecurePrice *float64; StockStatus string }`
  - `type graphqlClient struct{...}` with `func newGraphQLClient(apiKey, baseURL string, httpClient *http.Client) *graphqlClient`
  - `func (c *graphqlClient) gpuTypes(ctx) ([]gpuType, error)`

- [ ] **Step 1: Write the failing test** (`graphql_client_test.go`)

```go
package runpod

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestGPUTypesQueryAndDecode(t *testing.T) {
	var gotAuth, gotBody string
	client := newGraphQLClient("gkey", "https://gql.test/graphql", newFakeHTTPClient(func(r *http.Request) (*http.Response, error) {
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		return jsonResponse(200, `{"data":{"gpuTypes":[
			{"id":"NVIDIA RTX A2000","displayName":"RTX A2000","memoryInGb":6,"communityPrice":0.12,"securePrice":0.2,"lowestPrice":{"stockStatus":"High"}},
			{"id":"NVIDIA H100","displayName":"H100","memoryInGb":80,"communityPrice":3.5,"securePrice":4.0,"lowestPrice":{"stockStatus":null}}
		]}}`), nil
	}))

	gpus, err := client.gpuTypes(context.Background())
	if err != nil {
		t.Fatalf("gpuTypes: %v", err)
	}
	if gotAuth != "Bearer gkey" {
		t.Errorf("auth = %q", gotAuth)
	}
	if !strings.Contains(gotBody, "gpuTypes") {
		t.Errorf("body missing query: %s", gotBody)
	}
	if len(gpus) != 2 {
		t.Fatalf("expected 2 gpu types, got %d", len(gpus))
	}
	if gpus[0].ID != "NVIDIA RTX A2000" || gpus[0].MemoryInGb != 6 || gpus[0].CommunityPrice == nil || *gpus[0].CommunityPrice != 0.12 || gpus[0].StockStatus != "High" {
		t.Errorf("gpu[0] decode = %+v", gpus[0])
	}
	if gpus[1].StockStatus != "" {
		t.Errorf("null stock should decode to empty string, got %q", gpus[1].StockStatus)
	}
}

func TestGPUTypesSurfacesGraphQLErrors(t *testing.T) {
	client := newGraphQLClient("gkey", "https://gql.test/graphql", newFakeHTTPClient(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, `{"errors":[{"message":"boom"}]}`), nil
	}))
	if _, err := client.gpuTypes(context.Background()); err == nil {
		t.Fatal("graphql errors must surface as an error")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/adapter/runpod/ -run GPUTypes`
Expected: FAIL — `undefined: newGraphQLClient`.

- [ ] **Step 3: Implement `graphql_client.go`**

```go
package runpod

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const defaultGraphQLBaseURL = "https://api.runpod.io/graphql"

const gpuTypesQuery = `{ gpuTypes { id displayName memoryInGb communityPrice securePrice lowestPrice { stockStatus } } }`

type gpuType struct {
	ID             string
	DisplayName    string
	MemoryInGb     int
	CommunityPrice *float64
	SecurePrice    *float64
	StockStatus    string
}

type graphqlClient struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

func newGraphQLClient(apiKey, baseURL string, httpClient *http.Client) *graphqlClient {
	if baseURL == "" {
		baseURL = defaultGraphQLBaseURL
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &graphqlClient{baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, http: httpClient}
}

func (c *graphqlClient) gpuTypes(ctx context.Context) ([]gpuType, error) {
	reqBody, err := json.Marshal(map[string]string{"query": gpuTypesQuery})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("runpod: gpuTypes: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, httpError(http.MethodPost, "/graphql", resp.StatusCode, body)
	}
	var out struct {
		Data struct {
			GPUTypes []struct {
				ID             string   `json:"id"`
				DisplayName    string   `json:"displayName"`
				MemoryInGb     int      `json:"memoryInGb"`
				CommunityPrice *float64 `json:"communityPrice"`
				SecurePrice    *float64 `json:"securePrice"`
				LowestPrice    struct {
					StockStatus string `json:"stockStatus"`
				} `json:"lowestPrice"`
			} `json:"gpuTypes"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("runpod: decode gpuTypes: %w", err)
	}
	if len(out.Errors) > 0 {
		return nil, fmt.Errorf("runpod: gpuTypes graphql error: %s", out.Errors[0].Message)
	}
	gpus := make([]gpuType, 0, len(out.Data.GPUTypes))
	for _, g := range out.Data.GPUTypes {
		gpus = append(gpus, gpuType{
			ID:             g.ID,
			DisplayName:    g.DisplayName,
			MemoryInGb:     g.MemoryInGb,
			CommunityPrice: g.CommunityPrice,
			SecurePrice:    g.SecurePrice,
			StockStatus:    g.LowestPrice.StockStatus,
		})
	}
	return gpus, nil
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/adapter/runpod/ -run GPUTypes`
Expected: PASS.

- [ ] **Step 5: gofmt/vet + Commit**

```bash
gofmt -w internal/adapter/runpod/
go vet ./internal/adapter/runpod/
git add internal/adapter/runpod/graphql_client.go internal/adapter/runpod/graphql_client_test.go
git commit -m "feat(runpod): GraphQL client for gpuTypes pricing/availability"
```

---

## Task 3: Offer mapping

**Files:**
- Create: `internal/adapter/runpod/offers.go`
- Test: `internal/adapter/runpod/offers_test.go`

**Interfaces:**
- Consumes: `gpuType` (Task 2); `domain.OfferSnapshot` and friends.
- Produces:
  - `func buildOffers(gpus []gpuType, allowlist []string, now time.Time) []domain.OfferSnapshot`
  - `func stockAvailable(status string) bool`

Behavior: include a GPU only if (a) its `ID` is in `allowlist` (case-insensitive exact match), (b) `stockAvailable(StockStatus)` is true, and (c) `CommunityPrice != nil`. Each kept GPU → one provisionable offer priced from `communityPrice/3600`, advertising one NVIDIA accelerator so GPU-requiring workloads are feasible.

- [ ] **Step 1: Write the failing test** (`offers_test.go`)

```go
package runpod

import (
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
)

func pricePtr(v float64) *float64 { return &v }

func TestBuildOffersFiltersByAllowlistStockAndPrice(t *testing.T) {
	now := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	gpus := []gpuType{
		{ID: "NVIDIA RTX A2000", DisplayName: "RTX A2000", MemoryInGb: 6, CommunityPrice: pricePtr(0.12), StockStatus: "High"},
		{ID: "NVIDIA RTX A4000", DisplayName: "RTX A4000", MemoryInGb: 16, CommunityPrice: pricePtr(0.17), StockStatus: "None"},     // out of stock
		{ID: "NVIDIA H100", DisplayName: "H100", MemoryInGb: 80, CommunityPrice: pricePtr(3.5), StockStatus: "High"},               // not in allow-list
		{ID: "NVIDIA RTX A5000", DisplayName: "RTX A5000", MemoryInGb: 24, CommunityPrice: nil, StockStatus: "High"},               // no price (and not allowed)
	}
	offers := buildOffers(gpus, []string{"NVIDIA RTX A2000", "NVIDIA RTX A4000"}, now)

	if len(offers) != 1 {
		t.Fatalf("expected exactly 1 offer (A2000), got %d: %+v", len(offers), offers)
	}
	o := offers[0]
	if o.NativeRef != "NVIDIA RTX A2000" {
		t.Errorf("native ref = %q", o.NativeRef)
	}
	if o.Kind != domain.OfferKindProvisionable {
		t.Errorf("kind = %q, want provisionable", o.Kind)
	}
	if o.Platform.OS != "linux" || o.Platform.Architecture != "amd64" {
		t.Errorf("platform = %+v", o.Platform)
	}
	// 0.12 / 3600 ~= 3.333e-5
	if o.Pricing.RatePerSecondUSD < 3.3e-5 || o.Pricing.RatePerSecondUSD > 3.4e-5 {
		t.Errorf("rate per second = %v", o.Pricing.RatePerSecondUSD)
	}
	if len(o.Resources.Accelerators) != 1 || o.Resources.Accelerators[0].Vendor != "NVIDIA" || o.Resources.Accelerators[0].Count != 1 {
		t.Errorf("accelerators = %+v", o.Resources.Accelerators)
	}
	if o.Resources.Accelerators[0].MemoryBytes != int64(6)*1024*1024*1024 {
		t.Errorf("accelerator memory = %d", o.Resources.Accelerators[0].MemoryBytes)
	}
	if !o.Capacity.Available {
		t.Errorf("capacity should be available")
	}
}

func TestStockAvailable(t *testing.T) {
	for _, c := range []struct {
		in   string
		want bool
	}{{"High", true}, {"Medium", true}, {"Low", true}, {"", false}, {"None", false}, {"unavailable", false}} {
		if got := stockAvailable(c.in); got != c.want {
			t.Errorf("stockAvailable(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/adapter/runpod/ -run 'Offers|Stock'`
Expected: FAIL — `undefined: buildOffers`.

- [ ] **Step 3: Implement `offers.go`**

```go
package runpod

import (
	"strings"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
)

const gib = int64(1024) * 1024 * 1024

func stockAvailable(status string) bool {
	s := strings.ToLower(strings.TrimSpace(status))
	return s != "" && s != "none" && s != "unavailable"
}

func buildOffers(gpus []gpuType, allowlist []string, now time.Time) []domain.OfferSnapshot {
	allowed := make(map[string]bool, len(allowlist))
	for _, a := range allowlist {
		allowed[strings.ToLower(strings.TrimSpace(a))] = true
	}
	offers := make([]domain.OfferSnapshot, 0, len(gpus))
	for _, g := range gpus {
		if !allowed[strings.ToLower(strings.TrimSpace(g.ID))] {
			continue
		}
		if !stockAvailable(g.StockStatus) || g.CommunityPrice == nil {
			continue
		}
		offers = append(offers, domain.OfferSnapshot{
			ID:         "off_runpod_" + offerSlug(g.ID),
			Kind:       domain.OfferKindProvisionable,
			NativeRef:  g.ID,
			ObservedAt: now,
			ExpiresAt:  now.Add(5 * time.Minute),
			Platform:   domain.Platform{OS: "linux", Architecture: "amd64"},
			Resources: domain.ResourceInventory{
				CPUMillis:          8000,
				MemoryBytes:        16 * gib,
				EphemeralDiskBytes: 20 * gib,
				Accelerators: []domain.AcceleratorInventory{{
					Vendor:      "NVIDIA",
					Model:       g.DisplayName,
					Count:       1,
					MemoryBytes: int64(g.MemoryInGb) * gib,
				}},
			},
			Capabilities: domain.CapabilityProfile{
				Container: domain.ContainerCapabilities{MaxContainers: 1, SupportsDigestRefs: true, MaxEnvironmentBytes: 32768},
				Lifecycle: domain.LifecycleCapabilities{IdempotentLaunch: "launch_key", ListOwned: true},
				Resources: domain.ResourceCapabilities{GPUVendors: []string{"NVIDIA"}},
				Network:   domain.NetworkCapabilities{Inbound: domain.InboundNetworkPublicPort, PublicIPv4: true},
				Pricing:   domain.PricingCapabilities{Known: true},
			},
			Pricing: domain.PriceModel{
				Currency:           "USD",
				RatePerSecondUSD:   *g.CommunityPrice / 3600.0,
				GranularitySeconds: 1,
				Known:              true,
			},
			Capacity: domain.CapacityEvidence{Available: true, Confidence: 1},
		})
	}
	return offers
}

func offerSlug(id string) string {
	s := strings.ToLower(id)
	s = strings.ReplaceAll(s, " ", "_")
	return s
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/adapter/runpod/ -run 'Offers|Stock'`
Expected: PASS.

- [ ] **Step 5: gofmt/vet + Commit**

```bash
gofmt -w internal/adapter/runpod/
go vet ./internal/adapter/runpod/
git add internal/adapter/runpod/offers.go internal/adapter/runpod/offers_test.go
git commit -m "feat(runpod): map gpuTypes to provisionable GPU offers"
```

---

## Task 4: The adapter

**Files:**
- Create: `internal/adapter/runpod/runpod.go`
- Test: `internal/adapter/runpod/runpod_test.go`

**Interfaces:**
- Consumes: `restClient`, `graphqlClient`, `buildOffers`, `pod`, `gpuType` (Tasks 1-3); `adapter.*` types; `domain.*`.
- Produces:
  - `func New(secret string, config map[string]string) (*Adapter, error)`
  - `type Adapter struct{...}` satisfying `adapter.Adapter` (compile-time assertion `var _ adapter.Adapter = (*Adapter)(nil)`).

**Key behaviors:**
- `podName(launchKey) = "mercator-" + launchKey`.
- Ownership env stamped on launch; verified on Observe/Release/Terminate: a pod is "ours" iff its `Name` has the `mercator-` prefix **and**, when `env` is present, `env["MERCATOR_OWNERSHIP_TOKEN"]` matches the request's ownership token. Name is the lookup key; the token guards against any name reuse.
- `desiredStatus` → phase: `RUNNING`+`publicIp` → `running`; `RUNNING` w/o ip → `queued`; `EXITED` → `failed` (pessimistic — the authoritative outcome comes from the workload's exit report, wired in Task 5); `TERMINATED` → `released`; unknown → `queued`.
- `Launch` builds `gpuTypeIds = [SelectedOfferNativeRef] + (allowlist minus the selected id)`.

- [ ] **Step 1: Write the failing tests** (`runpod_test.go`)

```go
package runpod

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/benngarcia/mercator/internal/adapter"
)

// newTestAdapter builds an Adapter whose REST + GraphQL clients share one fake
// transport. The transport routes by method+path.
func newTestAdapter(t *testing.T, fn roundTripFunc) *Adapter {
	t.Helper()
	a, err := New("secret", map[string]string{
		"rest_base_url":    "https://rest.test/v1",
		"graphql_base_url": "https://gql.test/graphql",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a.rest.http = newFakeHTTPClient(fn)
	a.graphql.http = newFakeHTTPClient(fn)
	return a
}

func TestVerifyPingsREST(t *testing.T) {
	a := newTestAdapter(t, func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, `[]`), nil
	})
	if err := a.Verify(context.Background()); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestListOffersUsesGraphQLAndAllowlist(t *testing.T) {
	a := newTestAdapter(t, func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Host, "gql.test") {
			return jsonResponse(200, `{"data":{"gpuTypes":[
				{"id":"NVIDIA RTX A2000","displayName":"A2000","memoryInGb":6,"communityPrice":0.12,"lowestPrice":{"stockStatus":"High"}}
			]}}`), nil
		}
		return jsonResponse(200, `[]`), nil
	})
	offers, err := a.ListOffers(context.Background(), adapter.OfferRequest{WorkspaceID: "ws_1"})
	if err != nil {
		t.Fatalf("list offers: %v", err)
	}
	if len(offers) != 1 || offers[0].NativeRef != "NVIDIA RTX A2000" {
		t.Fatalf("offers = %+v", offers)
	}
}

func TestLaunchPostsPodWithOwnershipEnvAndName(t *testing.T) {
	var body string
	a := newTestAdapter(t, func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		return jsonResponse(201, `{"id":"pod_1","name":"mercator-lk1","desiredStatus":"RUNNING"}`), nil
	})
	val := "v"
	receipt, err := a.Launch(context.Background(), adapter.LaunchRequest{
		WorkspaceID:            "ws_1",
		RunID:                  "run_1",
		AttemptID:              "att_1",
		LaunchKey:              "lk1",
		OwnershipToken:         "own1",
		RequestHash:            "rh1",
		CleanupLocator:         "cl1",
		Image:                  "busybox",
		Args:                   []string{"sh", "-c", "echo hi"},
		SelectedOfferNativeRef: "NVIDIA RTX A2000",
		Environment:            []adapter.EnvironmentBinding{{Name: "FOO", Value: &val}},
	})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if receipt.ExternalID != "pod_1" || receipt.Phase != adapter.ExternalPhaseRunning {
		t.Fatalf("receipt = %+v", receipt)
	}
	for _, want := range []string{`"name":"mercator-lk1"`, `"imageName":"busybox"`, `"MERCATOR_OWNERSHIP_TOKEN":"own1"`, `"MERCATOR_REQUEST_HASH":"rh1"`, `"FOO":"v"`, `"NVIDIA RTX A2000"`} {
		if !strings.Contains(body, want) {
			t.Errorf("launch body missing %s\nbody=%s", want, body)
		}
	}
	// dockerStartCmd carries the args
	if !strings.Contains(body, `"dockerStartCmd":["sh","-c","echo hi"]`) {
		t.Errorf("missing dockerStartCmd: %s", body)
	}
}

func TestObserveMapsStatusAndVerifiesOwnership(t *testing.T) {
	a := newTestAdapter(t, func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, `[{"id":"pod_1","name":"mercator-lk1","desiredStatus":"RUNNING","publicIp":"1.2.3.4","env":{"MERCATOR_OWNERSHIP_TOKEN":"own1"}}]`), nil
	})
	obs, err := a.Observe(context.Background(), adapter.ObserveRequest{LaunchKey: "lk1", OwnershipToken: "own1", RequestHash: "rh1"})
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if obs.Phase != adapter.ExternalPhaseRunning {
		t.Fatalf("phase = %q, want running", obs.Phase)
	}
}

func TestObserveExitedMapsToFailed(t *testing.T) {
	a := newTestAdapter(t, func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, `[{"id":"pod_1","name":"mercator-lk1","desiredStatus":"EXITED","env":{"MERCATOR_OWNERSHIP_TOKEN":"own1"}}]`), nil
	})
	obs, err := a.Observe(context.Background(), adapter.ObserveRequest{LaunchKey: "lk1", OwnershipToken: "own1"})
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if obs.Phase != adapter.ExternalPhaseFailed {
		t.Fatalf("EXITED should map to failed (report is authoritative), got %q", obs.Phase)
	}
	if obs.ExitCode != nil {
		t.Fatalf("provider exposes no exit code; want nil, got %v", *obs.ExitCode)
	}
}

func TestObserveOwnershipMismatchIsConflict(t *testing.T) {
	a := newTestAdapter(t, func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, `[{"id":"pod_1","name":"mercator-lk1","desiredStatus":"RUNNING","env":{"MERCATOR_OWNERSHIP_TOKEN":"someone-else"}}]`), nil
	})
	_, err := a.Observe(context.Background(), adapter.ObserveRequest{LaunchKey: "lk1", OwnershipToken: "own1"})
	if err != adapter.ErrIdempotencyConflict {
		t.Fatalf("expected ErrIdempotencyConflict, got %v", err)
	}
}

func TestObserveMissingPodIsReleased(t *testing.T) {
	a := newTestAdapter(t, func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, `[]`), nil
	})
	obs, err := a.Observe(context.Background(), adapter.ObserveRequest{LaunchKey: "lk1", OwnershipToken: "own1"})
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if obs.Phase != adapter.ExternalPhaseReleased {
		t.Fatalf("missing pod should be released, got %q", obs.Phase)
	}
}

func TestTerminateResolvesByNameAndDeletes(t *testing.T) {
	var deleted string
	a := newTestAdapter(t, func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodDelete {
			deleted = strings.TrimPrefix(r.URL.Path, "/v1/pods/")
			return jsonResponse(204, ``), nil
		}
		return jsonResponse(200, `[{"id":"pod_1","name":"mercator-lk1","desiredStatus":"RUNNING","env":{"MERCATOR_OWNERSHIP_TOKEN":"own1"}}]`), nil
	})
	rec, err := a.Terminate(context.Background(), adapter.TerminateRequest{LaunchKey: "lk1", OwnershipToken: "own1", LaunchRequestHash: "rh1"})
	if err != nil {
		t.Fatalf("terminate: %v", err)
	}
	if !rec.Terminated || deleted != "pod_1" {
		t.Fatalf("terminate rec=%+v deleted=%q", rec, deleted)
	}
}

func TestListOwnedMapsEnvBackToFields(t *testing.T) {
	a := newTestAdapter(t, func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, `[{"id":"pod_1","name":"mercator-lk1","desiredStatus":"RUNNING","env":{"MERCATOR_WORKSPACE_ID":"ws_1","MERCATOR_RUN_ID":"run_1","MERCATOR_OWNERSHIP_TOKEN":"own1","MERCATOR_LAUNCH_KEY":"lk1","MERCATOR_REQUEST_HASH":"rh1"}}]`), nil
	})
	owned, err := a.ListOwned(context.Background(), adapter.OwnershipQuery{WorkspaceID: "ws_1"})
	if err != nil {
		t.Fatalf("list owned: %v", err)
	}
	if len(owned) != 1 || owned[0].RunID != "run_1" || owned[0].OwnershipToken != "own1" || owned[0].LaunchKey != "lk1" {
		t.Fatalf("owned = %+v", owned)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/adapter/runpod/ -run 'Verify|Offers|Launch|Observe|Terminate|ListOwned'`
Expected: FAIL — `undefined: New` / `a.rest` etc.

- [ ] **Step 3: Implement `runpod.go`**

```go
package runpod

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/domain"
)

var defaultAllowlist = []string{"NVIDIA RTX A2000", "NVIDIA RTX A4000"}

type Adapter struct {
	rest      *restClient
	graphql   *graphqlClient
	allowlist []string
	cloudType string
	diskGB    int
	now       func() time.Time
}

func New(secret string, config map[string]string) (*Adapter, error) {
	allow := defaultAllowlist
	if raw := strings.TrimSpace(config["gpu_types"]); raw != "" {
		parts := strings.Split(raw, ",")
		allow = allow[:0]
		for _, p := range parts {
			if t := strings.TrimSpace(p); t != "" {
				allow = append(allow, t)
			}
		}
	}
	cloud := config["cloud_type"]
	if cloud == "" {
		cloud = "COMMUNITY"
	}
	disk := 20
	if d := strings.TrimSpace(config["container_disk_gb"]); d != "" {
		if n, err := strconv.Atoi(d); err == nil && n > 0 {
			disk = n
		}
	}
	return &Adapter{
		rest:      newRESTClient(secret, config["rest_base_url"], http.DefaultClient),
		graphql:   newGraphQLClient(secret, config["graphql_base_url"], http.DefaultClient),
		allowlist: allow,
		cloudType: cloud,
		diskGB:    disk,
		now:       time.Now,
	}, nil
}

func (a *Adapter) Verify(ctx context.Context) error { return a.rest.ping(ctx) }

func (a *Adapter) ListOffers(ctx context.Context, _ adapter.OfferRequest) ([]domain.OfferSnapshot, error) {
	gpus, err := a.graphql.gpuTypes(ctx)
	if err != nil {
		return nil, err
	}
	return buildOffers(gpus, a.allowlist, a.now().UTC()), nil
}

func (a *Adapter) Launch(ctx context.Context, req adapter.LaunchRequest) (adapter.LaunchReceipt, error) {
	name := podName(req.LaunchKey)
	in := podCreateInput{
		Name:            name,
		ImageName:       req.Image,
		GPUTypeIDs:      a.gpuTypeIDs(req.SelectedOfferNativeRef),
		GPUCount:        1,
		ContainerDiskGB: a.diskGB,
		CloudType:       a.cloudType,
		Env:             a.launchEnv(req),
		DockerStartCmd:  append([]string(nil), req.Args...),
	}
	if req.Entrypoint != nil {
		in.DockerEntrypoint = append([]string(nil), (*req.Entrypoint)...)
	}
	p, err := a.rest.createPod(ctx, in)
	if err != nil {
		return adapter.LaunchReceipt{}, err
	}
	return adapter.LaunchReceipt{
		ExternalID:     p.ID,
		LaunchKey:      req.LaunchKey,
		OwnershipToken: req.OwnershipToken,
		CleanupLocator: req.CleanupLocator,
		Phase:          phaseFromPod(p),
		AcceptedAt:     a.now().UTC(),
	}, nil
}

func (a *Adapter) Observe(ctx context.Context, req adapter.ObserveRequest) (adapter.ExternalObservation, error) {
	p, found, err := a.findOwned(ctx, req.LaunchKey, req.OwnershipToken)
	if err != nil {
		return adapter.ExternalObservation{}, err
	}
	if !found {
		return adapter.ExternalObservation{LaunchKey: req.LaunchKey, Phase: adapter.ExternalPhaseReleased, ObservedAt: a.now().UTC()}, nil
	}
	return adapter.ExternalObservation{ExternalID: p.ID, LaunchKey: req.LaunchKey, Phase: phaseFromPod(p), ObservedAt: a.now().UTC()}, nil
}

func (a *Adapter) Cancel(ctx context.Context, req adapter.CancelRequest) (adapter.CancelReceipt, error) {
	// Best-effort: deleting a not-yet-started pod is the same resolve+delete path.
	if _, err := a.deleteOwned(ctx, req.LaunchKey, ""); err != nil {
		return adapter.CancelReceipt{}, err
	}
	return adapter.CancelReceipt{Cancelled: true}, nil
}

func (a *Adapter) Release(ctx context.Context, req adapter.ReleaseRequest) (adapter.ReleaseReceipt, error) {
	deleted, err := a.deleteOwned(ctx, req.LaunchKey, req.OwnershipToken)
	if err != nil {
		return adapter.ReleaseReceipt{}, err
	}
	return adapter.ReleaseReceipt{Released: deleted}, nil
}

func (a *Adapter) Terminate(ctx context.Context, req adapter.TerminateRequest) (adapter.TerminateReceipt, error) {
	deleted, err := a.deleteOwned(ctx, req.LaunchKey, req.OwnershipToken)
	if err != nil {
		return adapter.TerminateReceipt{}, err
	}
	return adapter.TerminateReceipt{Terminated: deleted}, nil
}

func (a *Adapter) ListOwned(ctx context.Context, req adapter.OwnershipQuery) ([]adapter.OwnedExternalObject, error) {
	pods, err := a.rest.listPodsByName(ctx, "mercator-")
	if err != nil {
		return nil, err
	}
	owned := make([]adapter.OwnedExternalObject, 0, len(pods))
	for _, p := range pods {
		if req.WorkspaceID != "" && p.Env["MERCATOR_WORKSPACE_ID"] != req.WorkspaceID {
			continue
		}
		owned = append(owned, adapter.OwnedExternalObject{
			ExternalID:     p.ID,
			WorkspaceID:    p.Env["MERCATOR_WORKSPACE_ID"],
			RunID:          p.Env["MERCATOR_RUN_ID"],
			AttemptID:      p.Env["MERCATOR_ATTEMPT_ID"],
			OwnershipToken: p.Env["MERCATOR_OWNERSHIP_TOKEN"],
			LaunchKey:      p.Env["MERCATOR_LAUNCH_KEY"],
			CleanupLocator: p.Env["MERCATOR_CLEANUP_LOCATOR"],
			RequestHash:    p.Env["MERCATOR_REQUEST_HASH"],
			Phase:          phaseFromPod(p),
		})
	}
	return owned, nil
}

// --- helpers ---

func podName(launchKey string) string { return "mercator-" + launchKey }

func (a *Adapter) gpuTypeIDs(selected string) []string {
	ids := []string{}
	if selected != "" {
		ids = append(ids, selected)
	}
	for _, g := range a.allowlist {
		if g != selected {
			ids = append(ids, g)
		}
	}
	return ids
}

func (a *Adapter) launchEnv(req adapter.LaunchRequest) map[string]string {
	env := map[string]string{}
	for _, b := range req.Environment {
		if b.Value != nil {
			env[b.Name] = *b.Value
		}
	}
	env["MERCATOR_WORKSPACE_ID"] = req.WorkspaceID
	env["MERCATOR_RUN_ID"] = req.RunID
	env["MERCATOR_ATTEMPT_ID"] = req.AttemptID
	env["MERCATOR_LAUNCH_KEY"] = req.LaunchKey
	env["MERCATOR_OWNERSHIP_TOKEN"] = req.OwnershipToken
	env["MERCATOR_REQUEST_HASH"] = req.RequestHash
	env["MERCATOR_CLEANUP_LOCATOR"] = req.CleanupLocator
	return env
}

// findOwned locates our pod by name and verifies ownership. The boolean is
// false when no such pod exists (treated as released by callers).
func (a *Adapter) findOwned(ctx context.Context, launchKey, ownershipToken string) (pod, bool, error) {
	name := podName(launchKey)
	pods, err := a.rest.listPodsByName(ctx, name)
	if err != nil {
		return pod{}, false, err
	}
	for _, p := range pods {
		if p.Name != name {
			continue
		}
		if ownershipToken != "" && p.Env != nil {
			if tok, ok := p.Env["MERCATOR_OWNERSHIP_TOKEN"]; ok && tok != ownershipToken {
				return pod{}, false, adapter.ErrIdempotencyConflict
			}
		}
		return p, true, nil
	}
	return pod{}, false, nil
}

func (a *Adapter) deleteOwned(ctx context.Context, launchKey, ownershipToken string) (bool, error) {
	p, found, err := a.findOwned(ctx, launchKey, ownershipToken)
	if err != nil {
		return false, err
	}
	if !found {
		return true, nil // already gone — idempotent
	}
	if err := a.rest.deletePod(ctx, p.ID); err != nil {
		return false, err
	}
	return true, nil
}

func phaseFromPod(p pod) adapter.ExternalPhase {
	switch strings.ToUpper(p.DesiredStatus) {
	case "RUNNING":
		if strings.TrimSpace(p.PublicIP) != "" {
			return adapter.ExternalPhaseRunning
		}
		return adapter.ExternalPhaseQueued
	case "EXITED":
		// Pod stopped, but RunPod never tells us success vs failure. Map to
		// failed (pessimistic); the workload's exit report is authoritative and
		// overrides this via the orchestrator's report finalize path.
		return adapter.ExternalPhaseFailed
	case "TERMINATED":
		return adapter.ExternalPhaseReleased
	default:
		return adapter.ExternalPhaseQueued
	}
}

var _ adapter.Adapter = (*Adapter)(nil)
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/adapter/runpod/`
Expected: PASS (all tests).

- [ ] **Step 5: gofmt/vet + Commit**

```bash
gofmt -w internal/adapter/runpod/
go vet ./internal/adapter/runpod/
git add internal/adapter/runpod/runpod.go internal/adapter/runpod/runpod_test.go
git commit -m "feat(runpod): adapter lifecycle (launch/observe/ownership/cleanup/verify)"
```

---

## Task 5: Orchestrator — reported exit becomes authoritative outcome

**Files:**
- Modify: `internal/orchestrator/orchestrator.go`
- Test: `internal/orchestrator/reporting_outcome_test.go` (create)

**Interfaces:**
- Consumes: existing `reduceRun`, `RecordReport`, `appendEvents`, `releaseAndClose`, `mustEvent`, event-type constants, `runState`.
- Produces: `func (o *Orchestrator) finalizeReportedExit(ctx, workspaceID, runID string) error`; `reduceRun` now captures the reported exit code into `runState.exitCode`.

**Design:** When an exit report (`exit_code != nil`) is ingested, the run records the authoritative `outcome_recorded` (0→succeeded, else→failed) plus `cleanup_requested`, then `releaseAndClose` runs (terminating the RunPod pod). The existing terminal-Observe path is the backstop: a RunPod pod that shows `EXITED` (mapped to phase `failed`) with no report yields outcome `failed` via the unchanged `outcomeForPhase`. The change is purely additive — `recordObservation`/`outcomeForPhase` are NOT modified, so docker/fake success paths are unaffected.

- [ ] **Step 1: Write the failing tests** (`reporting_outcome_test.go`)

```go
package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/adapter/fake"
	"github.com/benngarcia/mercator/internal/domain"
)

func intPtr(v int) *int { return &v }

// runningProvisionableRun drives a fresh run to RUNNING on a provisionable
// (terminate-disposition) offer and returns the orchestrator + fake adapter.
func runningProvisionableRun(t *testing.T, ctx context.Context) (*Orchestrator, *fake.Adapter) {
	t.Helper()
	ad := fake.New(
		fake.WithOffers([]domain.OfferSnapshot{orchProvisionableOffer("off_prov", time.Now().UTC())}),
		fake.WithLaunchOutcome(adapter.ExternalPhaseRunning),
	)
	orch := newTestOrchestrator(t, ad)
	createRun(t, ctx, orch)
	if err := orch.AdvanceRun(ctx, "ws_1", "run_1"); err != nil {
		t.Fatalf("advance to running: %v", err)
	}
	return orch, ad
}

func TestExitReportZeroRecordsSucceededAndTerminates(t *testing.T) {
	ctx := context.Background()
	orch, ad := runningProvisionableRun(t, ctx)

	if err := orch.RecordReport(ctx, "ws_1", "run_1", "exit", nil, intPtr(0)); err != nil {
		t.Fatalf("record report: %v", err)
	}

	record, err := orch.GetRun(ctx, "ws_1", "run_1")
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if !record.Closed || record.Outcome != domain.RunOutcomeSucceeded {
		t.Fatalf("expected closed+succeeded, got closed=%v outcome=%q", record.Closed, record.Outcome)
	}
	if record.ExitCode == nil || *record.ExitCode != 0 {
		t.Fatalf("expected exit code 0 on record, got %v", record.ExitCode)
	}
	if ad.TerminateCount() != 1 {
		t.Fatalf("expected pod terminated once on exit report, got %d", ad.TerminateCount())
	}
}

func TestExitReportNonzeroRecordsFailed(t *testing.T) {
	ctx := context.Background()
	orch, ad := runningProvisionableRun(t, ctx)

	if err := orch.RecordReport(ctx, "ws_1", "run_1", "exit", nil, intPtr(2)); err != nil {
		t.Fatalf("record report: %v", err)
	}
	record, err := orch.GetRun(ctx, "ws_1", "run_1")
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if record.Outcome != domain.RunOutcomeFailed {
		t.Fatalf("expected failed, got %q", record.Outcome)
	}
	if ad.TerminateCount() != 1 {
		t.Fatalf("expected terminate once, got %d", ad.TerminateCount())
	}
}

func TestProgressReportDoesNotFinalize(t *testing.T) {
	ctx := context.Background()
	orch, ad := runningProvisionableRun(t, ctx)

	if err := orch.RecordReport(ctx, "ws_1", "run_1", "progress", []byte(`{"pct":50}`), nil); err != nil {
		t.Fatalf("record report: %v", err)
	}
	record, err := orch.GetRun(ctx, "ws_1", "run_1")
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if record.Closed {
		t.Fatalf("a progress report must not close the run")
	}
	if ad.TerminateCount() != 0 {
		t.Fatalf("a progress report must not terminate, got %d", ad.TerminateCount())
	}
}

func TestExitReportAfterRunClosedIsNoop(t *testing.T) {
	ctx := context.Background()
	// Drive a run to terminal via the normal succeeded path first.
	ad := fake.New(
		fake.WithOffers([]domain.OfferSnapshot{orchProvisionableOffer("off_prov", time.Now().UTC())}),
		fake.WithLaunchOutcome(adapter.ExternalPhaseSucceeded),
	)
	orch := newTestOrchestrator(t, ad)
	createRun(t, ctx, orch)
	if err := orch.AdvanceRun(ctx, "ws_1", "run_1"); err != nil {
		t.Fatalf("advance: %v", err)
	}
	beforeTerm := ad.TerminateCount()

	// A late exit report must not double-finalize or double-terminate.
	if err := orch.RecordReport(ctx, "ws_1", "run_1", "exit", nil, intPtr(0)); err != nil {
		t.Fatalf("late report: %v", err)
	}
	if ad.TerminateCount() != beforeTerm {
		t.Fatalf("late report must not terminate again: before=%d after=%d", beforeTerm, ad.TerminateCount())
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/orchestrator/ -run 'ExitReport|ProgressReport'`
Expected: FAIL — the exit report records only `compute.run.reported.v1`, so `record.Closed`/`Outcome` are unset and `TerminateCount()==0`.

- [ ] **Step 3a: Capture the reported exit code in `reduceRun`**

In `internal/orchestrator/orchestrator.go`, inside the `reduceRun` switch, add a case (place it next to the existing `EventExternalStateObserved` case):

```go
		case EventRunReported:
			var data struct {
				ExitCode *int `json:"exit_code"`
			}
			if err := json.Unmarshal(event.Data, &data); err == nil && data.ExitCode != nil {
				code := *data.ExitCode
				state.exitCode = &code
			}
```

- [ ] **Step 3b: Add `finalizeReportedExit` and call it from `RecordReport`**

In `RecordReport`, change the success branch from:

```go
		if appendErr == nil {
			return nil
		}
```

to:

```go
		if appendErr == nil {
			if exitCode != nil {
				// Drive the authoritative outcome + prompt cleanup from the
				// reported exit code. Best-effort: any error here is non-fatal —
				// the AdvanceRun reconcile and Observe backstop still finalize.
				_ = o.finalizeReportedExit(ctx, workspaceID, runID)
			}
			return nil
		}
```

Then add this method (next to `RecordReport`):

```go
// finalizeReportedExit makes a reported exit code authoritative: it records the
// run outcome (0 -> succeeded, else -> failed) and requests cleanup, then closes
// the run by releasing/terminating its external resource. It is a no-op when the
// run is already outcome-recorded or closed (the Observe backstop or a prior
// report won the race), or when there is no launch intent / no reported exit
// code to act on.
func (o *Orchestrator) finalizeReportedExit(ctx context.Context, workspaceID, runID string) error {
	events, err := o.GetRunEvents(ctx, workspaceID, runID)
	if err != nil {
		return err
	}
	state, err := reduceRun(events)
	if err != nil {
		return err
	}
	if state.outcomeRecorded || state.closed || state.launchIntent == nil || state.exitCode == nil {
		return nil
	}
	outcome := string(domain.RunOutcomeSucceeded)
	if *state.exitCode != 0 {
		outcome = string(domain.RunOutcomeFailed)
	}
	version := uint64(len(events))
	if err := o.appendEvents(ctx, workspaceID, runID, version, "advance:report-finalize", []eventlog.NewEvent{
		mustEvent(runID, "outcome_recorded", EventRunOutcomeRecorded, map[string]any{"outcome": outcome}, o.now()),
		mustEvent(runID, "cleanup_requested", EventCleanupRequested, map[string]any{"launch_key": state.launchIntent.LaunchKey}, o.now()),
	}); err != nil {
		return err
	}
	return o.releaseAndClose(ctx, workspaceID, runID, version+2, state.launchIntent)
}
```

- [ ] **Step 4: Run to verify the new tests pass and nothing regressed**

Run: `go test ./internal/orchestrator/`
Expected: PASS (new reporting-outcome tests + all existing orchestrator/disposition/hardening tests).

- [ ] **Step 5: gofmt/vet + Commit**

```bash
gofmt -w internal/orchestrator/
go vet ./internal/orchestrator/
git add internal/orchestrator/orchestrator.go internal/orchestrator/reporting_outcome_test.go
git commit -m "feat(orchestrator): reported exit code becomes authoritative run outcome"
```

---

## Task 6: Factory registration + operator runbook

**Files:**
- Modify: `cmd/mercator/main.go` (the `factory.Register(...)` block near the docker registration, ~line 206)
- Create: `docs/production/runpod.md`

**Interfaces:**
- Consumes: `runpod.New` (Task 4); the existing `broker.NewFactory()` / `factory.Register` (in `buildServerDeps`).

- [ ] **Step 1: Register the adapter type**

In `cmd/mercator/main.go`, add an import for the package:

```go
	runpodadapter "github.com/benngarcia/mercator/internal/adapter/runpod"
```

Immediately after the existing `factory.Register("docker", ...)` block (before `br := broker.NewBroker(...)`), add:

```go
	factory.Register("runpod", func(config map[string]string, secret string) (adapter.Adapter, error) {
		return runpodadapter.New(secret, config)
	})
```

- [ ] **Step 2: Build to verify it compiles**

Run: `go build ./cmd/mercator/`
Expected: builds with no error.

- [ ] **Step 3: Smoke-test registration**

Run:
```bash
go test ./cmd/mercator/ 2>/dev/null || echo "no cmd tests — relying on build + manual"
go build ./... && echo BUILD_OK
```
Expected: `BUILD_OK`.

- [ ] **Step 4: Write the runbook** (`docs/production/runpod.md`)

Create `docs/production/runpod.md` with this content:

````markdown
# RunPod Provider Runbook

Mercator's `runpod` adapter launches container **Pods** on RunPod, observes
them, and terminates them on cleanup. RunPod's API never exposes a container
exit code, so the **workload self-reports its exit code** (see
`workload-reporting.md`); Mercator treats that report as the authoritative run
outcome.

## Adding the connection

1. Provision a RunPod API key and export it where Mercator runs:
   ```sh
   export RUNPOD_API_KEY=rpa_...      # never commit this
   ```
2. Add the connection (UI **Connections → Add connection**, adapter type
   `runpod`), or via the API:
   ```sh
   curl -X POST "$MERCATOR/v1/connections" \
     -H "Authorization: Bearer $MERCATOR_API_TOKEN" \
     -H 'Idempotency-Key: conn-runpod-1' \
     -H 'Content-Type: application/json' \
     -d '{"workspace_id":"ws_1","connection_id":"conn_runpod_main",
          "adapter_type":"runpod",
          "credential":{"source":"env","ref":"RUNPOD_API_KEY"}}'
   ```
3. Authorize it (runs a cheap `GET /pods` to validate the key):
   ```sh
   curl -X POST "$MERCATOR/v1/connections/conn_runpod_main:authorize?workspace_id=ws_1" \
     -H "Authorization: Bearer $MERCATOR_API_TOKEN"
   ```

## Connection config (optional)

| Key | Default | Meaning |
|-----|---------|---------|
| `gpu_types` | `NVIDIA RTX A2000,NVIDIA RTX A4000` | Comma-separated allow-list of GPU type ids advertised as offers. |
| `cloud_type` | `COMMUNITY` | `COMMUNITY` or `SECURE`. |
| `container_disk_gb` | `20` | Pod container disk size. |

## How runs land on RunPod

The scheduler picks the lowest-cost **feasible** offer. The local docker offer
is priced at 0, so a run lands on RunPod only when it is **infeasible on
docker** — declare a GPU accelerator requirement in the workload:

```json
"resources": { "accelerators": [ { "vendor": "NVIDIA", "count": 1 } ] }
```

Docker advertises no accelerators (infeasible); the RunPod offer advertises one
NVIDIA accelerator (feasible) and is selected.

## Lifecycle & cleanup

- Pods are named `mercator-<launchKey>` and carry `MERCATOR_*` ownership env.
- RunPod offers are **provisionable** ⇒ disposition **terminate** ⇒ cleanup
  issues `DELETE /pods/{id}`.
- On the workload's **exit report**, Mercator records the authoritative outcome
  and terminates the pod promptly. If a pod shows `EXITED` with no report, the
  run is marked **failed** (indeterminate) and the pod is terminated.

## Live verification

See `examples/runpod/` for two ready-to-run workloads. Both require the GPU
accelerator (so they land on RunPod), use the cheapest community GPU, and
auto-terminate on their exit report (< $0.01 each). Rotate the API key after
testing.
````

- [ ] **Step 5: gofmt/vet + Commit**

```bash
gofmt -w cmd/mercator/
go vet ./cmd/mercator/
git add cmd/mercator/main.go docs/production/runpod.md
git commit -m "feat(runpod): register adapter in the broker factory + runbook"
```

---

## Task 7: Live example workloads

**Files:**
- Create: `examples/runpod/busybox-report/README.md`
- Create: `examples/runpod/python-sdk/run.py`
- Create: `examples/runpod/python-sdk/README.md`

**Interfaces:**
- Consumes: the injected reporting env (`MERCATOR_REPORT_URL`, `MERCATOR_RUN_ID`, `MERCATOR_RUN_TOKEN`, `MERCATOR_WORKSPACE_ID`); the Python SDK at `sdk/python` (`from mercator import run_reporter`).

These are documentation + a script (no Go tests). The "test" is the documented manual launch in the runbook; verify by reading the files for correctness.

- [ ] **Step 1: Write `examples/runpod/busybox-report/README.md`**

````markdown
# busybox — raw-HTTP reporting (no SDK)

Proves any minimal image can report to Mercator using only the injected env and
a plain HTTP POST. `busybox` ships `wget`, which we use to POST the `:report`
endpoint.

## Create the run

```sh
curl -X POST "$MERCATOR/v1/runs" \
  -H "Authorization: Bearer $MERCATOR_API_TOKEN" \
  -H 'Idempotency-Key: busybox-report-1' \
  -H 'Content-Type: application/json' \
  -d '{
    "workspace_id": "ws_1",
    "workload": {
      "workspace_id": "ws_1",
      "spec": {
        "containers": [{
          "image": "busybox",
          "args": ["sh","-c",
            "set -e; URL=\"$MERCATOR_REPORT_URL/v1/runs/$MERCATOR_RUN_ID:report?workspace_id=$MERCATOR_WORKSPACE_ID\"; AUTH=\"Authorization: Bearer $MERCATOR_RUN_TOKEN\"; wget -q -O- --header \"$AUTH\" --header \"Content-Type: application/json\" --post-data \"{\\\"type\\\":\\\"progress\\\",\\\"data\\\":{\\\"pct\\\":50}}\" \"$URL\"; sleep 5; wget -q -O- --header \"$AUTH\" --header \"Content-Type: application/json\" --post-data \"{\\\"type\\\":\\\"exit\\\",\\\"exit_code\\\":0}\" \"$URL\""
          ]
        }],
        "resources": { "accelerators": [ { "vendor": "NVIDIA", "count": 1 } ] }
      }
    }
  }'
```

## Expected

- The run lands on RunPod (GPU accelerator required ⇒ docker infeasible).
- The Events tab shows a `compute.run.reported.v1` progress event then an exit
  event ("Workload report").
- On the exit report, the run outcome becomes **succeeded** and Mercator issues
  `DELETE /pods/{id}` — confirm the pod disappears from RunPod.
````

- [ ] **Step 2: Write `examples/runpod/python-sdk/run.py`**

```python
"""Custom-event reporting on RunPod using the Mercator Python SDK.

Reads the injected MERCATOR_* env, emits two custom event types, and reports
exit automatically via the context manager. Run inside a python:3-slim pod.
"""
import time

from mercator import run_reporter


def main() -> int:
    with run_reporter() as reporter:
        reporter.report({"type": "model.loaded", "data": {"name": "demo-model"}})
        for pct in (25, 50, 75, 100):
            reporter.report({"type": "progress", "data": {"pct": pct}})
            time.sleep(1)
        # The context manager reports {"type":"exit","exit_code":0} on a clean
        # exit; a raised exception reports a non-zero exit.
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
```

Note: before writing this, open `sdk/python/src/mercator/reporter.py` and
confirm the exact public surface (`run_reporter` context manager and its
`report(...)` signature). If the SDK exposes a different method name (e.g.
`report_event`) or a different exit-event shape, match the SDK — the SDK is the
source of truth, adjust `run.py` to it.

- [ ] **Step 3: Write `examples/runpod/python-sdk/README.md`**

````markdown
# python — custom events via the Mercator Python SDK

Proves the SDK path: arbitrary event types plus automatic exit reporting.

## Create the run

The pod installs the SDK and runs `run.py`. Point `args` at a shell that
installs the SDK from your published package (or vendors it) and executes the
script:

```sh
curl -X POST "$MERCATOR/v1/runs" \
  -H "Authorization: Bearer $MERCATOR_API_TOKEN" \
  -H 'Idempotency-Key: python-sdk-1' \
  -H 'Content-Type: application/json' \
  -d '{
    "workspace_id": "ws_1",
    "workload": {
      "workspace_id": "ws_1",
      "spec": {
        "containers": [{
          "image": "python:3-slim",
          "args": ["sh","-c","pip install --quiet mercator-sdk && python /app/run.py"]
        }],
        "resources": { "accelerators": [ { "vendor": "NVIDIA", "count": 1 } ] }
      }
    }
  }'
```

`run.py` must be present in the image (bake it in, or fetch it in the start
command). The exact install line depends on how the Python SDK is distributed;
adapt `pip install ...` to the published package name or a vendored copy.

## Expected

- The Events tab shows `model.loaded`, several `progress` events, then the exit
  event.
- Outcome **succeeded**; the pod is terminated automatically.
````

- [ ] **Step 4: Commit**

```bash
git add examples/runpod/
git commit -m "docs(runpod): live example workloads (busybox raw-HTTP + python SDK)"
```

---

## Manual verification (operator-run, after the automated tasks)

Not an automated task — requires the user's RunPod account, a real
`RUNPOD_API_KEY` (env only), and the running `bucket.bot` tunnel.

1. Start Mercator with `MERCATOR_SECRET_KEY`, `MERCATOR_PUBLIC_URL=https://mercator.bucket.bot`, the docker bootstrap, and `RUNPOD_API_KEY` exported.
2. Add + authorize the `conn_runpod_main` connection (runbook).
3. Launch `examples/runpod/busybox-report` → confirm: run lands on RunPod, progress + exit events appear in the Events tab, outcome `succeeded`, pod auto-terminated (gone from RunPod).
4. Launch `examples/runpod/python-sdk` → confirm: custom events + exit appear, outcome `succeeded`, pod auto-terminated.
5. Rotate the RunPod API key.

---

## Self-Review notes

- **Spec coverage:** REST client (T1), GraphQL `gpuTypes` (T2), offer mapping with allow-list + accelerator inventory (T3), adapter lifecycle + ownership + pessimistic EXITED mapping + Verify (T4), reported-exit → authoritative outcome + prompt cleanup with Observe backstop (T5), factory registration + runbook (T6), two live workloads forced onto RunPod via the GPU accelerator requirement (T7), manual live verification (final section). Every spec section maps to a task.
- **Secrets:** the key flows only through `New(secret, ...)`; `httpError` never includes the `Authorization` header; the runbook exports `RUNPOD_API_KEY` and tells the operator to rotate it.
- **Type consistency:** `pod`, `gpuType`, `podCreateInput`, `flexEnv`, `restClient`, `graphqlClient`, `buildOffers`, `Adapter`, `New`, `finalizeReportedExit` are defined once and referenced with matching signatures across tasks. `roundTripFunc`/`jsonResponse`/`newFakeHTTPClient` are defined once in T1's `helpers_test.go` and reused by T2/T4.
- **Additive orchestrator change:** `recordObservation`/`outcomeForPhase` are untouched; only `reduceRun` gains a case and `RecordReport` gains a best-effort finalize call — no regression risk to docker/fake success paths.
