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
