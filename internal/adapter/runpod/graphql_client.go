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

// gpuTypesQuery requests the GPU catalog with pricing and per-cloud
// availability for a concrete allocation size. Stock is a per-cloud fact:
// `lowestPrice(input: {gpuCount, secureCloud})` is queried once per cloud via
// aliases (GpuLowestPriceInput.secureCloud selects the cloud).
// RunPod's `lowestPrice` field REQUIRES an input argument — querying it bare
// returns an INTERNAL_SERVER_ERROR per gpuType.
const gpuTypesQuery = `query GPUTypes($gpuCount: Int!) { gpuTypes { id displayName memoryInGb communityPrice securePrice secureStock: lowestPrice(input: {gpuCount: $gpuCount, secureCloud: true}) { stockStatus } communityStock: lowestPrice(input: {gpuCount: $gpuCount, secureCloud: false}) { stockStatus } } }`

type gpuType struct {
	ID                   string
	DisplayName          string
	MemoryInGb           int
	CommunityPrice       *float64
	SecurePrice          *float64
	SecureStockStatus    string
	CommunityStockStatus string
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

func (c *graphqlClient) gpuTypes(ctx context.Context, gpuCount int) ([]gpuType, error) {
	if gpuCount <= 0 {
		gpuCount = 1
	}
	reqBody, err := json.Marshal(map[string]any{
		"query":     gpuTypesQuery,
		"variables": map[string]int{"gpuCount": gpuCount},
	})
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
		return nil, fmt.Errorf("runpod: gpuTypes read body: %w", err)
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
				SecureStock    struct {
					StockStatus string `json:"stockStatus"`
				} `json:"secureStock"`
				CommunityStock struct {
					StockStatus string `json:"stockStatus"`
				} `json:"communityStock"`
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
		msgs := make([]string, len(out.Errors))
		for i, e := range out.Errors {
			msgs[i] = e.Message
		}
		return nil, fmt.Errorf("runpod: gpuTypes graphql errors: %s", strings.Join(msgs, "; "))
	}
	gpus := make([]gpuType, 0, len(out.Data.GPUTypes))
	for _, g := range out.Data.GPUTypes {
		gpus = append(gpus, gpuType{
			ID:                   g.ID,
			DisplayName:          g.DisplayName,
			MemoryInGb:           g.MemoryInGb,
			CommunityPrice:       g.CommunityPrice,
			SecurePrice:          g.SecurePrice,
			SecureStockStatus:    g.SecureStock.StockStatus,
			CommunityStockStatus: g.CommunityStock.StockStatus,
		})
	}
	return gpus, nil
}
