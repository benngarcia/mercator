package vast

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/benngarcia/mercator/internal/adapter"
)

func TestVerifyPingsCurrentUser(t *testing.T) {
	var path, auth string
	a := newTestAdapter(t, func(r *http.Request) (*http.Response, error) {
		path = r.URL.Path
		auth = r.Header.Get("Authorization")
		return jsonResponse(200, `{"id":1,"username":"u"}`), nil
	})
	if err := a.Verify(context.Background()); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if path != "/api/v0/users/current/" || auth != "Bearer secret" {
		t.Fatalf("path=%q auth=%q", path, auth)
	}
}

func TestListOffersQueriesSecureTierAndMapsOffers(t *testing.T) {
	var body string
	a := newTestAdapter(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/v0/bundles/" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		return jsonResponse(200, `{"offers":[
			{"id":9001,"gpu_name":"RTX 4090","gpu_arch":"nvidia","num_gpus":1,"gpu_ram":24576,
			 "cpu_cores_effective":16,"cpu_ram":65536,"disk_space":500,"dph_total":0.31,
			 "reliability2":0.99,"verification":"verified"},
			{"id":9002,"gpu_name":"RTX 4090","gpu_arch":"nvidia","num_gpus":1,"gpu_ram":24576,
			 "cpu_cores_effective":16,"cpu_ram":65536,"disk_space":500,"dph_total":0.11,
			 "reliability2":0.99,"verification":"unverified"}
		]}`), nil
	})
	offers, err := a.ListOffers(context.Background(), offerRequest())
	if err != nil {
		t.Fatalf("list offers: %v", err)
	}
	if len(offers) != 1 || offers[0].NativeRef != "9001" {
		t.Fatalf("only the verified offer must survive, got %+v", offers)
	}
	for _, want := range []string{`"verified":{"eq":true}`, `"datacenter":{"eq":true}`, `"external":{"eq":false}`, `"rentable":{"eq":true}`, `"type":"ondemand"`, `"num_gpus":{"eq":1}`} {
		if !strings.Contains(body, want) {
			t.Errorf("secure-tier search body missing %s: %s", want, body)
		}
	}
}

func TestLaunchCreatesInstanceWithLabelEnvAndArgs(t *testing.T) {
	var createPath, createBody, secureLookupBody string
	listCalls := 0
	a := newTestAdapter(t, func(r *http.Request) (*http.Response, error) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/instances/":
			listCalls++
			if listCalls == 1 { // find-before-create: nothing yet
				return jsonResponse(200, `{"instances":[]}`), nil
			}
			return jsonResponse(200, `{"instances":[
				{"id":777,"label":"mercator-lk1","actual_status":"loading","verification":"verified",
				 "extra_env":[["MERCATOR_OWNERSHIP_TOKEN","own1"]]}
			]}`), nil
		case r.Method == http.MethodPost && r.URL.Path == "/api/v0/bundles/":
			raw, _ := io.ReadAll(r.Body)
			secureLookupBody = string(raw)
			return jsonResponse(200, `{"offers":[{"id":9001,"num_gpus":1,"dph_total":0.31,"verification":"verified"}]}`), nil
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/api/v0/asks/"):
			createPath = r.URL.Path
			raw, _ := io.ReadAll(r.Body)
			createBody = string(raw)
			return jsonResponse(200, `{"success":true,"new_contract":777}`), nil
		}
		t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		return nil, nil
	})
	val := "v"
	req := launchRequest()
	req.Args = []string{"sh", "-c", "echo hi"}
	req.Environment = []adapter.EnvironmentBinding{{Name: "FOO", Value: &val}}
	receipt, err := a.Launch(context.Background(), req)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if receipt.ExternalID != "777" || receipt.Duplicate {
		t.Fatalf("receipt = %+v", receipt)
	}
	if createPath != "/api/v0/asks/9001/" {
		t.Errorf("create path = %q", createPath)
	}
	for _, want := range []string{`"verified":{"eq":true}`, `"datacenter":{"eq":true}`, `"rentable":{"eq":true}`, `"rented":{"eq":false}`, `"num_gpus":{"eq":1}`, `"disk_space":{"gte":20}`} {
		if !strings.Contains(secureLookupBody, want) {
			t.Errorf("launch secure-tier lookup missing %s: %s", want, secureLookupBody)
		}
	}
	if strings.Contains(secureLookupBody, `"id"`) {
		t.Errorf("launch secure-tier lookup uses unsupported id filter: %s", secureLookupBody)
	}
	for _, want := range []string{`"label":"mercator-lk1"`, `"runtype":"args"`, `"args":["sh","-c","echo hi"]`, `"image":"busybox"`, `"cancel_unavail":true`, `"target_state":"running"`, `"MERCATOR_OWNERSHIP_TOKEN":"own1"`, `"MERCATOR_REQUEST_HASH":"rh1"`, `"FOO":"v"`} {
		if !strings.Contains(createBody, want) {
			t.Errorf("create body missing %s\nbody=%s", want, createBody)
		}
	}
}

func TestLaunchRefusesAskThatIsNotSecureTier(t *testing.T) {
	created := false
	a := newTestAdapter(t, func(r *http.Request) (*http.Response, error) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/instances/":
			return jsonResponse(200, `{"instances":[]}`), nil
		case r.Method == http.MethodPost && r.URL.Path == "/api/v0/bundles/":
			// The secure-filtered lookup does not resolve the ask.
			return jsonResponse(200, `{"offers":[]}`), nil
		case r.Method == http.MethodPut:
			created = true
		}
		return jsonResponse(200, `{}`), nil
	})
	_, err := a.Launch(context.Background(), launchRequest())
	if err == nil || !strings.Contains(err.Error(), "secure-tier") {
		t.Fatalf("non-secure ask must be refused loudly, got err=%v", err)
	}
	if created {
		t.Fatal("refused launch must never rent the ask")
	}
}

func TestLaunchFindsExistingInstanceBeforeCreate(t *testing.T) {
	a := newTestAdapter(t, func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/instances/" {
			return jsonResponse(200, `{"instances":[
				{"id":555,"label":"mercator-lk1","actual_status":"running",
				 "extra_env":[["MERCATOR_OWNERSHIP_TOKEN","own1"]]}
			]}`), nil
		}
		t.Fatalf("existing instance must short-circuit create, got %s %s", r.Method, r.URL.Path)
		return nil, nil
	})
	receipt, err := a.Launch(context.Background(), launchRequest())
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if !receipt.Duplicate || receipt.ExternalID != "555" {
		t.Fatalf("receipt = %+v", receipt)
	}
}

func TestLaunchReconciliationKeepsLowestContractAndDestroysOurs(t *testing.T) {
	var destroyed string
	listCalls := 0
	a := newTestAdapter(t, func(r *http.Request) (*http.Response, error) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/instances/":
			listCalls++
			if listCalls == 1 {
				return jsonResponse(200, `{"instances":[]}`), nil
			}
			// A concurrent launch created 555 before our 777 became visible.
			return jsonResponse(200, `{"instances":[
				{"id":555,"label":"mercator-lk1","actual_status":"loading","verification":"verified","extra_env":[["MERCATOR_OWNERSHIP_TOKEN","own1"]]},
				{"id":777,"label":"mercator-lk1","actual_status":"loading","verification":"verified","extra_env":[["MERCATOR_OWNERSHIP_TOKEN","own1"]]}
			]}`), nil
		case r.Method == http.MethodPost:
			return jsonResponse(200, `{"offers":[{"id":9001,"num_gpus":1,"dph_total":0.31,"verification":"verified"}]}`), nil
		case r.Method == http.MethodPut:
			return jsonResponse(200, `{"success":true,"new_contract":777}`), nil
		case r.Method == http.MethodDelete:
			destroyed = r.URL.Path
			return jsonResponse(200, `{"success":true}`), nil
		}
		return nil, nil
	})
	receipt, err := a.Launch(context.Background(), launchRequest())
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if !receipt.Duplicate || receipt.ExternalID != "555" {
		t.Fatalf("lowest contract must win, receipt = %+v", receipt)
	}
	if destroyed != "/api/v0/instances/777/" {
		t.Fatalf("our losing contract must be destroyed, destroyed=%q", destroyed)
	}
}

func TestLaunchDestroysInstanceOnExplicitlyUnverifiedMachine(t *testing.T) {
	var destroyed string
	listCalls := 0
	a := newTestAdapter(t, func(r *http.Request) (*http.Response, error) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/instances/":
			listCalls++
			if listCalls == 1 {
				return jsonResponse(200, `{"instances":[]}`), nil
			}
			return jsonResponse(200, `{"instances":[
				{"id":777,"label":"mercator-lk1","actual_status":"loading","verification":"deverified","extra_env":[["MERCATOR_OWNERSHIP_TOKEN","own1"]]}
			]}`), nil
		case r.Method == http.MethodPost:
			return jsonResponse(200, `{"offers":[{"id":9001,"num_gpus":1,"dph_total":0.31,"verification":"verified"}]}`), nil
		case r.Method == http.MethodPut:
			return jsonResponse(200, `{"success":true,"new_contract":777}`), nil
		case r.Method == http.MethodDelete:
			destroyed = r.URL.Path
			return jsonResponse(200, `{"success":true}`), nil
		}
		return nil, nil
	})
	_, err := a.Launch(context.Background(), launchRequest())
	if err == nil || !strings.Contains(err.Error(), "deverified machine") {
		t.Fatalf("unverified machine must fail the launch, got err=%v", err)
	}
	if destroyed != "/api/v0/instances/777/" {
		t.Fatalf("instance on unverified machine must be destroyed, destroyed=%q", destroyed)
	}
}

func TestLaunchRejectsEntrypointOverride(t *testing.T) {
	a := newTestAdapter(t, func(r *http.Request) (*http.Response, error) {
		t.Fatal("entrypoint rejection must not reach the API")
		return nil, nil
	})
	entry := []string{"/bin/init"}
	req := launchRequest()
	req.Entrypoint = &entry
	_, err := a.Launch(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "entrypoint") {
		t.Fatalf("entrypoint override must be rejected loudly, got err=%v", err)
	}
}

func TestLaunchRejectsNonNumericNativeRef(t *testing.T) {
	a := newTestAdapter(t, func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodGet {
			return jsonResponse(200, `{"instances":[]}`), nil
		}
		t.Fatalf("bad native ref must not reach search/create, got %s %s", r.Method, r.URL.Path)
		return nil, nil
	})
	req := launchRequest()
	req.SelectedOfferNativeRef = "NVIDIA RTX A2000"
	_, err := a.Launch(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "ask id") {
		t.Fatalf("non-numeric native ref must be rejected, got err=%v", err)
	}
}

func TestObserveMapsRunningStatus(t *testing.T) {
	a := newTestAdapter(t, func(r *http.Request) (*http.Response, error) {
		if q := r.URL.Query().Get("select_filters"); !strings.Contains(q, `"mercator-lk1"`) {
			t.Fatalf("observe must filter by label server-side, got %q", q)
		}
		return jsonResponse(200, `{"instances":[
			{"id":777,"label":"mercator-lk1","actual_status":"running","extra_env":[["MERCATOR_OWNERSHIP_TOKEN","own1"]]}
		]}`), nil
	})
	obs, err := a.Observe(context.Background(), observeRequest())
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if obs.Phase != "running" || obs.ExternalID != "777" {
		t.Fatalf("obs = %+v", obs)
	}
}

func TestObserveExitedMapsToFailedWithoutExitCode(t *testing.T) {
	a := newTestAdapter(t, func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, `{"instances":[
			{"id":777,"label":"mercator-lk1","actual_status":"exited","extra_env":[["MERCATOR_OWNERSHIP_TOKEN","own1"]]}
		]}`), nil
	})
	obs, err := a.Observe(context.Background(), observeRequest())
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if obs.Phase != "failed" {
		t.Fatalf("exited must map to failed (report is authoritative), got %q", obs.Phase)
	}
	if obs.ExitCode != nil {
		t.Fatalf("vast exposes no exit code; want nil, got %v", *obs.ExitCode)
	}
}

func TestObserveOfflineHostStaysQueued(t *testing.T) {
	a := newTestAdapter(t, func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, `{"instances":[
			{"id":777,"label":"mercator-lk1","actual_status":"offline","extra_env":[["MERCATOR_OWNERSHIP_TOKEN","own1"]]}
		]}`), nil
	})
	obs, err := a.Observe(context.Background(), observeRequest())
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if obs.Phase != "queued" {
		t.Fatalf("offline host is non-terminal, got %q", obs.Phase)
	}
}

func TestObserveMissingInstanceIsReleased(t *testing.T) {
	a := newTestAdapter(t, func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, `{"instances":[]}`), nil
	})
	obs, err := a.Observe(context.Background(), observeRequest())
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if obs.Phase != "released" {
		t.Fatalf("missing instance should be released, got %q", obs.Phase)
	}
}

func TestObserveOwnershipMismatchIsConflict(t *testing.T) {
	a := newTestAdapter(t, func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, `{"instances":[
			{"id":777,"label":"mercator-lk1","actual_status":"running","extra_env":[["MERCATOR_OWNERSHIP_TOKEN","someone-else"]]}
		]}`), nil
	})
	if _, err := a.Observe(context.Background(), observeRequest()); err != adapter.ErrIdempotencyConflict {
		t.Fatalf("expected ErrIdempotencyConflict, got %v", err)
	}
}

func TestTerminateResolvesByLabelAndDestroys(t *testing.T) {
	var destroyed string
	a := newTestAdapter(t, func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodDelete {
			destroyed = r.URL.Path
			return jsonResponse(200, `{"success":true}`), nil
		}
		return jsonResponse(200, `{"instances":[
			{"id":777,"label":"mercator-lk1","actual_status":"running","extra_env":[["MERCATOR_OWNERSHIP_TOKEN","own1"]]}
		]}`), nil
	})
	rec, err := a.Terminate(context.Background(), terminateRequest())
	if err != nil {
		t.Fatalf("terminate: %v", err)
	}
	if !rec.Terminated || destroyed != "/api/v0/instances/777/" {
		t.Fatalf("terminate rec=%+v destroyed=%q", rec, destroyed)
	}
}

func TestTerminateMissingInstanceIsIdempotent(t *testing.T) {
	a := newTestAdapter(t, func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodDelete {
			t.Fatal("nothing to destroy")
		}
		return jsonResponse(200, `{"instances":[]}`), nil
	})
	rec, err := a.Terminate(context.Background(), terminateRequest())
	if err != nil || !rec.Terminated {
		t.Fatalf("already-gone terminate must succeed, rec=%+v err=%v", rec, err)
	}
}

func TestDestroyTolerates404(t *testing.T) {
	a := newTestAdapter(t, func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodDelete {
			return jsonResponse(404, `{"success":false,"msg":"no such instance"}`), nil
		}
		return jsonResponse(200, `{"instances":[
			{"id":777,"label":"mercator-lk1","actual_status":"running","extra_env":[["MERCATOR_OWNERSHIP_TOKEN","own1"]]}
		]}`), nil
	})
	rec, err := a.Terminate(context.Background(), terminateRequest())
	if err != nil || !rec.Terminated {
		t.Fatalf("destroy racing a vanished instance must succeed, rec=%+v err=%v", rec, err)
	}
}

func TestListOwnedFiltersByLabelPrefixAndWorkspace(t *testing.T) {
	a := newTestAdapter(t, func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, `{"instances":[
			{"id":1,"label":"mercator-lk1","actual_status":"running","extra_env":[["MERCATOR_WORKSPACE_ID","ws_1"],["MERCATOR_RUN_ID","run_1"],["MERCATOR_OWNERSHIP_TOKEN","own1"],["MERCATOR_LAUNCH_KEY","lk1"],["MERCATOR_REQUEST_HASH","rh1"]]},
			{"id":2,"label":"mercator-lk2","actual_status":"running","extra_env":[["MERCATOR_WORKSPACE_ID","ws_2"]]},
			{"id":3,"label":"someone-elses-instance","actual_status":"running","extra_env":[]}
		]}`), nil
	})
	owned, err := a.ListOwned(context.Background(), ownershipQuery("ws_1"))
	if err != nil {
		t.Fatalf("list owned: %v", err)
	}
	if len(owned) != 1 || owned[0].RunID != "run_1" || owned[0].OwnershipToken != "own1" || owned[0].LaunchKey != "lk1" {
		t.Fatalf("owned = %+v", owned)
	}
}

func TestListInstancesFollowsPagination(t *testing.T) {
	calls := 0
	a := newTestAdapter(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Query().Get("after_token") == "" {
			return jsonResponse(200, `{"instances":[{"id":1,"label":"mercator-a"}],"next_token":"t2"}`), nil
		}
		return jsonResponse(200, `{"instances":[{"id":2,"label":"mercator-b"}]}`), nil
	})
	rows, err := a.api.listInstances(context.Background(), "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if calls != 2 || len(rows) != 2 || rows[1].ID != 2 {
		t.Fatalf("calls=%d rows=%+v", calls, rows)
	}
}

func TestNewRejectsInvalidOfferLimit(t *testing.T) {
	if _, err := New("k", map[string]string{"offer_limit": "lots"}); err == nil {
		t.Fatal("invalid offer_limit must fail loudly")
	}
}

func TestNewNormalizesGPUNameUnderscores(t *testing.T) {
	a, err := New("k", map[string]string{"gpu_names": "RTX_4090, H100 SXM"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if len(a.gpuNames) != 2 || a.gpuNames[0] != "RTX 4090" || a.gpuNames[1] != "H100 SXM" {
		t.Fatalf("gpuNames = %+v", a.gpuNames)
	}
}
