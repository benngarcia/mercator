package runpod

import (
	"context"
	"io"
	"net/http"
	"reflect"
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
	if receipt.ExternalID != "pod_1" || receipt.Phase != adapter.ExternalPhaseQueued {
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

func TestNewWithGPUTypesDoesNotMutateDefaultAllowlist(t *testing.T) {
	snapshot := append([]string(nil), defaultAllowlist...)
	if _, err := New("k", map[string]string{"gpu_types": "NVIDIA H100"}); err != nil {
		t.Fatalf("New: %v", err)
	}
	if !reflect.DeepEqual(defaultAllowlist, snapshot) {
		t.Fatalf("New mutated defaultAllowlist: got %+v, want %+v", defaultAllowlist, snapshot)
	}
}

func TestReleaseResolvesByNameAndDeletes(t *testing.T) {
	var deleted string
	a := newTestAdapter(t, func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodDelete {
			deleted = strings.TrimPrefix(r.URL.Path, "/v1/pods/")
			return jsonResponse(204, ``), nil
		}
		return jsonResponse(200, `[{"id":"pod_1","name":"mercator-lk1","desiredStatus":"RUNNING","env":{"MERCATOR_OWNERSHIP_TOKEN":"own1"}}]`), nil
	})
	rec, err := a.Release(context.Background(), adapter.ReleaseRequest{LaunchKey: "lk1", OwnershipToken: "own1", LaunchRequestHash: "rh1"})
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if !rec.Released || deleted != "pod_1" {
		t.Fatalf("release rec=%+v deleted=%q", rec, deleted)
	}
}

func TestCancelDeletesRegardlessOfOwnershipToken(t *testing.T) {
	var deleted string
	a := newTestAdapter(t, func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodDelete {
			deleted = strings.TrimPrefix(r.URL.Path, "/v1/pods/")
			return jsonResponse(204, ``), nil
		}
		return jsonResponse(200, `[{"id":"pod_1","name":"mercator-lk1","desiredStatus":"RUNNING","env":{"MERCATOR_OWNERSHIP_TOKEN":"own1"}}]`), nil
	})
	rec, err := a.Cancel(context.Background(), adapter.CancelRequest{LaunchKey: "lk1"})
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if !rec.Cancelled || deleted != "pod_1" {
		t.Fatalf("cancel rec=%+v deleted=%q", rec, deleted)
	}
}
