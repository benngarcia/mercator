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
		msgs := make([]string, len(out.Errors))
		for i, e := range out.Errors {
			msgs[i] = e.Message
		}
		return nil, fmt.Errorf("runpod: gpuTypes graphql errors: %s", strings.Join(msgs, "; "))
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
