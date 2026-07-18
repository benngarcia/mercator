package runpod

import (
	"context"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/scheduler"
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
	var body string
	a := newTestAdapter(t, func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Host, "gql.test") {
			raw, _ := io.ReadAll(r.Body)
			body = string(raw)
			return jsonResponse(200, `{"data":{"gpuTypes":[
				{"id":"NVIDIA RTX A2000","displayName":"A2000","memoryInGb":6,"securePrice":0.12,"secureStock":{"stockStatus":"High"}}
			]}}`), nil
		}
		return jsonResponse(200, `[]`), nil
	})
	offers, err := a.ListOffers(context.Background(), adapter.OfferRequest{WorkspaceID: "ws_1"})
	if err != nil {
		t.Fatalf("list offers: %v", err)
	}
	if len(offers) != 1 || offers[0].NativeRef != "NVIDIA RTX A2000|SECURE" {
		t.Fatalf("offers = %+v", offers)
	}
	if !strings.Contains(body, `"variables":{"gpuCount":1}`) {
		t.Fatalf("omitted resources must query the one-GPU default: %s", body)
	}
	if offers[0].Resources.Accelerators[0].Count != 1 || offers[0].Resources.EphemeralDiskBytes != 20*gib {
		t.Fatalf("omitted resources must retain adapter defaults: %+v", offers[0].Resources)
	}
}

func TestRequestedAllocationSchedulesAndReachesPodCreation(t *testing.T) {
	var graphqlBody, createBody string
	a := newTestAdapter(t, func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(r.URL.Host, "gql.test") {
			graphqlBody = string(body)
			return jsonResponse(200, `{"data":{"gpuTypes":[
				{"id":"NVIDIA RTX A2000","displayName":"A2000","memoryInGb":6,"securePrice":0.12,"secureStock":{"stockStatus":"High"}}
			]}}`), nil
		}
		createBody = string(body)
		return jsonResponse(201, `{"id":"pod_2","name":"mercator-launch_2","desiredStatus":"RUNNING"}`), nil
	})
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }
	resources := domain.ResourceRequirements{
		CPU:           domain.CPURequirement{MinMillis: 4000},
		Memory:        domain.MemoryRequirement{MinBytes: 8 * gib},
		Accelerators:  []domain.AcceleratorRequirement{{Vendor: "nvidia", Count: 2}},
		EphemeralDisk: domain.DiskRequirement{MinBytes: 75*gib + 1},
	}

	offers, err := a.ListOffers(context.Background(), adapter.OfferRequest{WorkspaceID: "ws_1", Resources: resources})
	if err != nil {
		t.Fatalf("list offers: %v", err)
	}
	if !strings.Contains(graphqlBody, `"variables":{"gpuCount":2}`) {
		t.Fatalf("availability query did not request two GPUs: %s", graphqlBody)
	}
	if len(offers) != 1 || offers[0].Resources.Accelerators[0].Count != 2 || offers[0].Resources.EphemeralDiskBytes != 76*gib {
		t.Fatalf("offer does not describe requested allocation: %+v", offers)
	}
	if offers[0].Pricing.RatePerSecondUSD < 6.6e-5 || offers[0].Pricing.RatePerSecondUSD > 6.7e-5 {
		t.Fatalf("offer price does not scale with GPU count: %+v", offers[0].Pricing)
	}

	workload := domain.WorkloadRevision{
		Digest: "sha256:revision",
		Spec: domain.WorkloadSpec{
			Containers: []domain.ContainerSpec{{
				Name:     "main",
				Image:    "ghcr.io/acme/trainer@sha256:0000000000000000000000000000000000000000000000000000000000000000",
				Platform: domain.Platform{OS: "linux", Architecture: "amd64"},
			}},
			Resources: resources,
			Placement: domain.PlacementPolicy{Objective: domain.ObjectiveCheapest, ExpectedRuntimeSeconds: 60},
		},
	}
	decision, err := scheduler.New().Evaluate(context.Background(), scheduler.SchedulingInput{
		RunID: "run_2", Workload: workload, Offers: offers, EvaluatedAt: now,
	})
	if err != nil {
		t.Fatalf("schedule: %v", err)
	}
	if decision.SelectedOfferSnapshotID != offers[0].ID {
		t.Fatalf("requested allocation was not schedulable: %+v", decision)
	}

	_, err = a.Launch(context.Background(), adapter.LaunchRequest{
		WorkspaceID:            "ws_1",
		RunID:                  "run_2",
		AttemptID:              "att_2",
		LaunchKey:              "launch_2",
		OwnershipToken:         "own_2",
		RequestHash:            "request_2",
		CleanupLocator:         "cleanup_2",
		Image:                  workload.Spec.Containers[0].Image,
		Resources:              resources,
		SelectedOfferNativeRef: offers[0].NativeRef,
	})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	// The offer's cloud tag must translate into an explicit cloudType, and the
	// gpuTypeIds must carry the bare GPU id, never the cloud-tagged native ref.
	for _, want := range []string{`"gpuCount":2`, `"containerDiskInGb":76`, `"cloudType":"SECURE"`, `"gpuTypeIds":["NVIDIA RTX A2000"`} {
		if !strings.Contains(createBody, want) {
			t.Errorf("pod create body missing %s: %s", want, createBody)
		}
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
		Resources: domain.ResourceRequirements{
			Accelerators:  []domain.AcceleratorRequirement{{Vendor: "nvidia", Count: 2}},
			EphemeralDisk: domain.DiskRequirement{MinBytes: 75*gib + 1},
		},
		Environment: []adapter.EnvironmentBinding{{Name: "FOO", Value: &val}},
	})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if receipt.ExternalID != "pod_1" || receipt.Phase != adapter.ExternalPhaseQueued {
		t.Fatalf("receipt = %+v", receipt)
	}
	// A cloudless native ref must default to the SECURE cloud.
	for _, want := range []string{`"name":"mercator-lk1"`, `"imageName":"busybox"`, `"gpuCount":2`, `"containerDiskInGb":76`, `"cloudType":"SECURE"`, `"MERCATOR_OWNERSHIP_TOKEN":"own1"`, `"MERCATOR_REQUEST_HASH":"rh1"`, `"FOO":"v"`, `"NVIDIA RTX A2000"`} {
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

func TestLaunchRefusesCommunityOfferWithoutOptIn(t *testing.T) {
	created := false
	a := newTestAdapter(t, func(r *http.Request) (*http.Response, error) {
		created = true
		return jsonResponse(201, `{"id":"pod_1","name":"mercator-lk1","desiredStatus":"RUNNING"}`), nil
	})
	_, err := a.Launch(context.Background(), adapter.LaunchRequest{
		LaunchKey:              "lk1",
		Image:                  "busybox",
		SelectedOfferNativeRef: "NVIDIA RTX A2000|COMMUNITY",
	})
	if err == nil || !strings.Contains(err.Error(), "community cloud") || !strings.Contains(err.Error(), "allow_community_cloud") {
		t.Fatalf("stale community offer must be refused loudly, got err=%v", err)
	}
	if created {
		t.Fatal("refused launch must never reach the pods API")
	}
}

func TestLaunchRefusesUnknownCloudTag(t *testing.T) {
	a := newTestAdapter(t, func(r *http.Request) (*http.Response, error) {
		t.Fatal("unknown cloud tag must never reach the pods API")
		return nil, nil
	})
	_, err := a.Launch(context.Background(), adapter.LaunchRequest{
		LaunchKey:              "lk1",
		Image:                  "busybox",
		SelectedOfferNativeRef: "NVIDIA RTX A2000|SPOT",
	})
	if err == nil || !strings.Contains(err.Error(), "unknown cloud") {
		t.Fatalf("unknown cloud tag must be refused, got err=%v", err)
	}
}

func TestLaunchSendsCommunityCloudWhenOptedIn(t *testing.T) {
	var body string
	a := newTestAdapter(t, func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		return jsonResponse(201, `{"id":"pod_1","name":"mercator-lk1","desiredStatus":"RUNNING"}`), nil
	})
	a.allowCommunity = true
	if _, err := a.Launch(context.Background(), adapter.LaunchRequest{
		LaunchKey:              "lk1",
		Image:                  "busybox",
		SelectedOfferNativeRef: "NVIDIA RTX A2000|COMMUNITY",
	}); err != nil {
		t.Fatalf("launch: %v", err)
	}
	if !strings.Contains(body, `"cloudType":"COMMUNITY"`) {
		t.Fatalf("opted-in community launch must request COMMUNITY explicitly: %s", body)
	}
}

func TestLaunchDestroysPodPlacedOnCommunityDespiteSecureRequest(t *testing.T) {
	var deleted string
	a := newTestAdapter(t, func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodDelete {
			deleted = strings.TrimPrefix(r.URL.Path, "/v1/pods/")
			return jsonResponse(204, ``), nil
		}
		// RunPod reports the backing machine as explicitly non-secure.
		return jsonResponse(201, `{"id":"pod_1","name":"mercator-lk1","desiredStatus":"RUNNING","machine":{"secureCloud":false}}`), nil
	})
	_, err := a.Launch(context.Background(), adapter.LaunchRequest{
		LaunchKey:              "lk1",
		Image:                  "busybox",
		SelectedOfferNativeRef: "NVIDIA RTX A2000|SECURE",
	})
	if err == nil || !strings.Contains(err.Error(), "community cloud despite SECURE") {
		t.Fatalf("community placement must fail the launch, got err=%v", err)
	}
	if deleted != "pod_1" {
		t.Fatalf("mis-placed pod must be destroyed, deleted=%q", deleted)
	}
}

func TestLaunchAcceptsPodWithoutMachineFacts(t *testing.T) {
	// The machine block may be absent while the pod awaits placement; that must
	// not be read as a community violation.
	a := newTestAdapter(t, func(r *http.Request) (*http.Response, error) {
		return jsonResponse(201, `{"id":"pod_1","name":"mercator-lk1","desiredStatus":"RUNNING"}`), nil
	})
	receipt, err := a.Launch(context.Background(), adapter.LaunchRequest{
		LaunchKey:              "lk1",
		Image:                  "busybox",
		SelectedOfferNativeRef: "NVIDIA RTX A2000|SECURE",
	})
	if err != nil || receipt.ExternalID != "pod_1" {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
}

func TestNewRejectsRemovedCloudTypeConfig(t *testing.T) {
	if _, err := New("k", map[string]string{"cloud_type": "SECURE"}); err == nil || !strings.Contains(err.Error(), "allow_community_cloud") {
		t.Fatalf("removed cloud_type key must fail loudly, got err=%v", err)
	}
}

func TestNewRejectsInvalidAllowCommunityCloud(t *testing.T) {
	if _, err := New("k", map[string]string{"allow_community_cloud": "yes please"}); err == nil {
		t.Fatal("invalid allow_community_cloud must fail loudly")
	}
}

func TestNewDefaultsToSecureOnly(t *testing.T) {
	a, err := New("k", nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a.allowCommunity {
		t.Fatal("community cloud must be opt-in, not the default")
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
