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
	if !strings.Contains(gotBody, "gpuTypes") || !strings.Contains(gotBody, "lowestPrice") {
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
	_, err := client.gpuTypes(context.Background())
	if err == nil {
		t.Fatal("graphql errors must surface as an error")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error should carry the graphql message, got %q", err.Error())
	}
}
