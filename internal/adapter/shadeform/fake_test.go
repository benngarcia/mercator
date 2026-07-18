package shadeform

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeShadeform is an in-memory Shadeform API served through an
// http.RoundTripper: enough state for idempotent-launch, reconciliation, and
// janitor tests without sockets. It mirrors the documented quirks — /instances
// takes no query parameters and still lists instances in "deleting".
type fakeShadeform struct {
	mu        sync.Mutex
	types     []instanceType
	instances []instance
	creates   []createRequest
	deletes   []string
	nextID    int
	base      time.Time

	// createStatus, when non-zero, is returned for the NEXT create call (then
	// cleared). createLandsAnyway makes that failed create still register the
	// instance, modeling an indeterminate 5xx.
	createStatus      int
	createLandsAnyway bool
	// beforeCreateReturns injects state right before create responds, e.g. a
	// concurrent duplicate that the pre-scan could not have seen.
	beforeCreateReturns func(f *fakeShadeform)
}

func newFakeShadeform() *fakeShadeform {
	return &fakeShadeform{base: time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)}
}

func (f *fakeShadeform) addInstance(inst instance) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.instances = append(f.instances, inst)
}

func (f *fakeShadeform) instanceByID(id string) *instance {
	for i := range f.instances {
		if f.instances[i].ID == id {
			return &f.instances[i]
		}
	}
	return nil
}

func (f *fakeShadeform) RoundTrip(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/v1")
	switch {
	case r.Method == http.MethodGet && path == "/instances":
		return marshalResponse(map[string]any{"instances": f.instances})
	case r.Method == http.MethodGet && path == "/instances/types":
		q := r.URL.Query()
		var out []instanceType
		for _, t := range f.types {
			if c := q.Get("cloud"); c != "" && !strings.EqualFold(t.Cloud, c) {
				continue
			}
			if s := q.Get("shade_instance_type"); s != "" && t.ShadeInstanceType != s {
				continue
			}
			out = append(out, t)
		}
		return marshalResponse(map[string]any{"instance_types": out})
	case r.Method == http.MethodPost && path == "/instances/create":
		var req createRequest
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &req); err != nil {
			return jsonResponse(400, `{"error":"bad request"}`), nil
		}
		f.creates = append(f.creates, req)
		if f.beforeCreateReturns != nil {
			hook := f.beforeCreateReturns
			f.beforeCreateReturns = nil
			hook(f)
		}
		status := f.createStatus
		lands := status == 0 || f.createLandsAnyway
		f.createStatus = 0
		var id string
		if lands {
			f.nextID++
			id = fmt.Sprintf("inst_%d", f.nextID)
			f.instances = append(f.instances, instance{
				ID:                id,
				Cloud:             req.Cloud,
				Region:            req.Region,
				ShadeInstanceType: req.ShadeInstanceType,
				ShadeCloud:        req.ShadeCloud,
				Name:              req.Name,
				Status:            "creating",
				Tags:              req.Tags,
				CreatedAt:         f.base.Add(time.Duration(f.nextID) * time.Minute),
			})
		}
		if status != 0 {
			return jsonResponse(status, `{"error":"provider"}`), nil
		}
		return marshalResponse(map[string]any{"id": id})
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/delete"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/instances/"), "/delete")
		f.deletes = append(f.deletes, id)
		if inst := f.instanceByID(id); inst != nil {
			inst.Status = "deleting"
			return jsonResponse(200, `{}`), nil
		}
		return jsonResponse(404, `{"error":"not found"}`), nil
	}
	return jsonResponse(404, fmt.Sprintf(`{"error":"no route %s %s"}`, r.Method, path)), nil
}

func marshalResponse(v any) (*http.Response, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return jsonResponse(200, string(body)), nil
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

// vmType returns a plausible vm-typed catalog record tests can tweak.
func vmType() instanceType {
	return instanceType{
		Cloud:             "hyperstack",
		ShadeInstanceType: "A6000",
		CloudInstanceType: "gpu_1x_a6000",
		Configuration: typeConfiguration{
			MemoryInGB:      48,
			StorageInGB:     256,
			VCPUs:           12,
			NumGPUs:         1,
			GPUType:         "A6000",
			VRAMPerGPUInGB:  48,
			GPUManufacturer: "nvidia",
			OSOptions:       []string{"ubuntu22.04", "ubuntu22.04_cuda12.2_shade_os"},
		},
		HourlyPrice:    210,
		DeploymentType: "vm",
		Availability: []availability{
			{Region: "canada-1", Available: true, DisplayName: "Toronto, Canada"},
			{Region: "us-east-1", Available: false, DisplayName: "Virginia, USA"},
		},
		BootTime: &bootTime{MinBootInSec: 180, MaxBootInSec: 300},
	}
}

func newTestAdapter(t *testing.T, fake *fakeShadeform, config map[string]string) *Adapter {
	t.Helper()
	if config == nil {
		config = map[string]string{}
	}
	config["base_url"] = "https://shadeform.test/v1"
	a, err := New("secret-key", config)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a.client.http = &http.Client{Transport: fake}
	a.client.backoff = 0
	a.now = func() time.Time { return time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC) }
	return a
}

func ownedInstance(id, launchKey, workspace, token, status string, createdAt time.Time) instance {
	return instance{
		ID:        id,
		Cloud:     "hyperstack",
		Region:    "canada-1",
		Name:      "mercator-" + launchKey,
		Status:    status,
		CreatedAt: createdAt,
		Tags: []string{
			tagLaunchKey + "=" + launchKey,
			tagWorkspace + "=" + workspace,
			tagRun + "=run_1",
			tagAttempt + "=att_1",
			tagOwnershipToken + "=" + token,
			tagRequestHash + "=rh_1",
			tagCleanupLocator + "=cl_1",
		},
	}
}
