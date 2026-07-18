package shadeform

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
)

func launchRequest() adapter.LaunchRequest {
	val := "v"
	return adapter.LaunchRequest{
		WorkspaceID:            "ws_1",
		RunID:                  "run_1",
		AttemptID:              "att_1",
		LaunchKey:              "lk1",
		OwnershipToken:         "own1",
		RequestHash:            "rh_1",
		CleanupLocator:         "cl_1",
		Image:                  "ghcr.io/acme/trainer@sha256:0000000000000000000000000000000000000000000000000000000000000000",
		Args:                   []string{"python", "train.py", "--epochs", "10"},
		Environment:            []adapter.EnvironmentBinding{{Name: "FOO", Value: &val}},
		SelectedOfferNativeRef: "hyperstack/canada-1/A6000",
	}
}

func TestVerifyListsInstances(t *testing.T) {
	a := newTestAdapter(t, newFakeShadeform(), nil)
	if err := a.Verify(context.Background()); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestLaunchCreatesInstanceWithDockerConfigTagsAndAutoDelete(t *testing.T) {
	fake := newFakeShadeform()
	fake.types = []instanceType{vmType()}
	a := newTestAdapter(t, fake, nil)

	receipt, err := a.Launch(context.Background(), launchRequest())
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if receipt.ExternalID != "inst_1" || receipt.Duplicate || receipt.Phase != adapter.ExternalPhaseQueued {
		t.Fatalf("receipt = %+v", receipt)
	}
	if len(fake.creates) != 1 {
		t.Fatalf("want exactly one create, got %d", len(fake.creates))
	}
	c := fake.creates[0]
	if c.Cloud != "hyperstack" || c.Region != "canada-1" || c.ShadeInstanceType != "A6000" {
		t.Errorf("placement triple = %s/%s/%s", c.Cloud, c.Region, c.ShadeInstanceType)
	}
	if !c.ShadeCloud {
		t.Error("shade_cloud must default to true")
	}
	if c.Name != "mercator-lk1" {
		t.Errorf("name = %q", c.Name)
	}
	if c.OS != "ubuntu22.04_cuda12.2_shade_os" {
		t.Errorf("os = %q, want the shade_os image (driver + container runtime baked in)", c.OS)
	}
	lc := c.LaunchConfiguration
	if lc == nil || lc.Type != "docker" || lc.DockerConfiguration == nil {
		t.Fatalf("launch configuration = %+v", lc)
	}
	if lc.DockerConfiguration.Image != launchRequest().Image {
		t.Errorf("image = %q", lc.DockerConfiguration.Image)
	}
	if lc.DockerConfiguration.Args != "python train.py --epochs 10" {
		t.Errorf("args = %q", lc.DockerConfiguration.Args)
	}
	if lc.DockerConfiguration.RegistryCredentials != nil {
		t.Error("no registry credentials configured; none must be sent")
	}
	envs := map[string]string{}
	for _, e := range lc.DockerConfiguration.Envs {
		envs[e.Name] = e.Value
	}
	for key, want := range map[string]string{"FOO": "v", "MERCATOR_OWNERSHIP_TOKEN": "own1", "MERCATOR_LAUNCH_KEY": "lk1", "MERCATOR_REQUEST_HASH": "rh_1"} {
		if envs[key] != want {
			t.Errorf("env %s = %q, want %q", key, envs[key], want)
		}
	}
	wantTags := map[string]bool{}
	for _, tag := range ownershipTags(launchRequest()) {
		wantTags[tag] = true
	}
	for _, tag := range c.Tags {
		delete(wantTags, tag)
	}
	if len(wantTags) > 0 {
		t.Errorf("create missing ownership tags: %v (got %v)", wantTags, c.Tags)
	}
	if c.AutoDelete == nil {
		t.Fatal("auto_delete backstop must be set on every create")
	}
	// now (2026-07-17T12:00Z) + default 24h lifetime
	if c.AutoDelete.DateThreshold != "2026-07-18T12:00:00Z" {
		t.Errorf("auto_delete date threshold = %q", c.AutoDelete.DateThreshold)
	}
	// 210 cents/hour × 24h = $50.40
	if c.AutoDelete.SpendThreshold != "50.40" {
		t.Errorf("auto_delete spend threshold = %q", c.AutoDelete.SpendThreshold)
	}
}

func TestLaunchShellQuotesArgsIntoOneString(t *testing.T) {
	fake := newFakeShadeform()
	fake.types = []instanceType{vmType()}
	a := newTestAdapter(t, fake, nil)
	req := launchRequest()
	req.Args = []string{"sh", "-c", `echo "hello world"; exit $?`, "it's"}

	if _, err := a.Launch(context.Background(), req); err != nil {
		t.Fatalf("launch: %v", err)
	}
	got := fake.creates[0].LaunchConfiguration.DockerConfiguration.Args
	want := `sh -c 'echo "hello world"; exit $?' 'it'\''s'`
	if got != want {
		t.Fatalf("args = %q, want %q", got, want)
	}
}

func TestLaunchPassesRegistryCredentialsFromConfig(t *testing.T) {
	fake := newFakeShadeform()
	fake.types = []instanceType{vmType()}
	a := newTestAdapter(t, fake, map[string]string{"registry_username": "bot", "registry_password": "ghp_pat"})

	if _, err := a.Launch(context.Background(), launchRequest()); err != nil {
		t.Fatalf("launch: %v", err)
	}
	creds := fake.creates[0].LaunchConfiguration.DockerConfiguration.RegistryCredentials
	if creds == nil || creds.Username != "bot" || creds.Password != "ghp_pat" {
		t.Fatalf("registry credentials = %+v", creds)
	}
}

func TestLaunchRejectsEntrypointOverride(t *testing.T) {
	a := newTestAdapter(t, newFakeShadeform(), nil)
	req := launchRequest()
	entrypoint := []string{"/custom"}
	req.Entrypoint = &entrypoint

	if _, err := a.Launch(context.Background(), req); err == nil || !strings.Contains(err.Error(), "entrypoint") {
		t.Fatalf("want loud entrypoint rejection, got %v", err)
	}
}

func TestLaunchRejectsCloudOutsideAllowedClouds(t *testing.T) {
	fake := newFakeShadeform()
	fake.types = []instanceType{vmType()}
	a := newTestAdapter(t, fake, map[string]string{"allowed_clouds": "lambdalabs"})

	_, err := a.Launch(context.Background(), launchRequest())
	if err == nil || !strings.Contains(err.Error(), "allowed_clouds") {
		t.Fatalf("want allowed_clouds rejection, got %v", err)
	}
	if len(fake.creates) != 0 {
		t.Fatal("rejected launch must not create anything")
	}
}

func TestLaunchFailsWhenCatalogLacksTheType(t *testing.T) {
	fake := newFakeShadeform() // empty catalog
	a := newTestAdapter(t, fake, nil)

	_, err := a.Launch(context.Background(), launchRequest())
	if err == nil || !strings.Contains(err.Error(), "auto_delete") {
		t.Fatalf("launch without a priced catalog record must fail loudly, got %v", err)
	}
	if len(fake.creates) != 0 {
		t.Fatal("must not create an instance whose spend cannot be capped")
	}
}

func TestLaunchIsIdempotentAcrossRetries(t *testing.T) {
	fake := newFakeShadeform()
	fake.types = []instanceType{vmType()}
	fake.addInstance(ownedInstance("inst_9", "lk1", "ws_1", "own1", "active", fake.base))
	a := newTestAdapter(t, fake, nil)

	receipt, err := a.Launch(context.Background(), launchRequest())
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if !receipt.Duplicate || receipt.ExternalID != "inst_9" || receipt.Phase != adapter.ExternalPhaseRunning {
		t.Fatalf("receipt = %+v, want duplicate of the live instance", receipt)
	}
	if len(fake.creates) != 0 {
		t.Fatal("pre-scan hit must not create a second instance")
	}
}

func TestLaunchIgnoresDeletedInstancesInPreScan(t *testing.T) {
	fake := newFakeShadeform()
	fake.types = []instanceType{vmType()}
	fake.addInstance(ownedInstance("inst_9", "lk1", "ws_1", "own1", "deleting", fake.base))
	a := newTestAdapter(t, fake, nil)

	receipt, err := a.Launch(context.Background(), launchRequest())
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if receipt.Duplicate || len(fake.creates) != 1 {
		t.Fatalf("a deleting instance must not satisfy idempotency: receipt=%+v creates=%d", receipt, len(fake.creates))
	}
}

func TestLaunchOwnershipMismatchIsConflict(t *testing.T) {
	fake := newFakeShadeform()
	fake.types = []instanceType{vmType()}
	fake.addInstance(ownedInstance("inst_9", "lk1", "ws_1", "someone-else", "active", fake.base))
	a := newTestAdapter(t, fake, nil)

	if _, err := a.Launch(context.Background(), launchRequest()); err != adapter.ErrIdempotencyConflict {
		t.Fatalf("want ErrIdempotencyConflict, got %v", err)
	}
}

func TestLaunchReconcilesConcurrentDuplicateKeepingOldest(t *testing.T) {
	fake := newFakeShadeform()
	fake.types = []instanceType{vmType()}
	// A concurrent launcher's instance appears between our pre-scan and our
	// create landing; it is OLDER than ours so it must win.
	fake.beforeCreateReturns = func(f *fakeShadeform) {
		f.instances = append(f.instances, ownedInstance("inst_0", "lk1", "ws_1", "own1", "creating", f.base.Add(-time.Hour)))
	}
	a := newTestAdapter(t, fake, nil)

	receipt, err := a.Launch(context.Background(), launchRequest())
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if !receipt.Duplicate || receipt.ExternalID != "inst_0" {
		t.Fatalf("receipt = %+v, want the older concurrent instance to win", receipt)
	}
	if len(fake.deletes) != 1 || fake.deletes[0] != "inst_1" {
		t.Fatalf("our younger duplicate must be deleted, got deletes=%v", fake.deletes)
	}
}

func TestLaunchAdoptsInstanceAfterIndeterminateCreateFailure(t *testing.T) {
	fake := newFakeShadeform()
	fake.types = []instanceType{vmType()}
	fake.createStatus = 500
	fake.createLandsAnyway = true
	a := newTestAdapter(t, fake, nil)

	receipt, err := a.Launch(context.Background(), launchRequest())
	if err != nil {
		t.Fatalf("launch should adopt the landed instance, got %v", err)
	}
	if !receipt.Duplicate || receipt.ExternalID != "inst_1" {
		t.Fatalf("receipt = %+v", receipt)
	}
	if len(fake.creates) != 1 {
		t.Fatalf("a 5xx create must NOT be retried blindly, got %d create calls", len(fake.creates))
	}
}

func TestLaunchSurfacesCreateFailureWhenNothingLanded(t *testing.T) {
	fake := newFakeShadeform()
	fake.types = []instanceType{vmType()}
	fake.createStatus = 500
	a := newTestAdapter(t, fake, nil)

	if _, err := a.Launch(context.Background(), launchRequest()); err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("want surfaced create failure, got %v", err)
	}
}

func TestObserveMapsVMStatusToPhase(t *testing.T) {
	cases := map[string]adapter.ExternalPhase{
		"creating":         adapter.ExternalPhaseQueued,
		"pending_provider": adapter.ExternalPhaseQueued,
		"pending":          adapter.ExternalPhaseQueued,
		"active":           adapter.ExternalPhaseRunning,
		"error":            adapter.ExternalPhaseFailed,
		"deleting":         adapter.ExternalPhaseReleased,
	}
	for status, want := range cases {
		fake := newFakeShadeform()
		fake.addInstance(ownedInstance("inst_1", "lk1", "ws_1", "own1", status, fake.base))
		a := newTestAdapter(t, fake, nil)
		obs, err := a.Observe(context.Background(), adapter.ObserveRequest{LaunchKey: "lk1", OwnershipToken: "own1"})
		if err != nil {
			t.Fatalf("observe %s: %v", status, err)
		}
		if obs.Phase != want {
			t.Errorf("status %q → phase %q, want %q", status, obs.Phase, want)
		}
		if obs.ExitCode != nil {
			t.Errorf("shadeform exposes no exit code; got %v for %s", *obs.ExitCode, status)
		}
	}
}

func TestObserveMissingInstanceIsReleased(t *testing.T) {
	a := newTestAdapter(t, newFakeShadeform(), nil)
	obs, err := a.Observe(context.Background(), adapter.ObserveRequest{LaunchKey: "lk1", OwnershipToken: "own1"})
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if obs.Phase != adapter.ExternalPhaseReleased {
		t.Fatalf("missing instance must observe as released, got %q", obs.Phase)
	}
}

func TestObserveOwnershipMismatchIsConflict(t *testing.T) {
	fake := newFakeShadeform()
	fake.addInstance(ownedInstance("inst_1", "lk1", "ws_1", "someone-else", "active", fake.base))
	a := newTestAdapter(t, fake, nil)
	if _, err := a.Observe(context.Background(), adapter.ObserveRequest{LaunchKey: "lk1", OwnershipToken: "own1"}); err != adapter.ErrIdempotencyConflict {
		t.Fatalf("want ErrIdempotencyConflict, got %v", err)
	}
}

func TestTerminateDeletesEveryLiveMatch(t *testing.T) {
	fake := newFakeShadeform()
	fake.addInstance(ownedInstance("inst_1", "lk1", "ws_1", "own1", "active", fake.base))
	// A stray duplicate from a failed reconciliation: cleanup converges on zero.
	fake.addInstance(ownedInstance("inst_2", "lk1", "ws_1", "own1", "creating", fake.base.Add(time.Minute)))
	// Already deleting: never re-deleted.
	fake.addInstance(ownedInstance("inst_3", "lk1", "ws_1", "own1", "deleting", fake.base))
	a := newTestAdapter(t, fake, nil)

	rec, err := a.Terminate(context.Background(), adapter.TerminateRequest{LaunchKey: "lk1", OwnershipToken: "own1"})
	if err != nil {
		t.Fatalf("terminate: %v", err)
	}
	if !rec.Terminated {
		t.Fatalf("rec = %+v", rec)
	}
	if len(fake.deletes) != 2 || fake.deletes[0] != "inst_1" || fake.deletes[1] != "inst_2" {
		t.Fatalf("deletes = %v, want both live matches and not the deleting one", fake.deletes)
	}
}

func TestTerminateMissingInstanceIsIdempotent(t *testing.T) {
	a := newTestAdapter(t, newFakeShadeform(), nil)
	rec, err := a.Terminate(context.Background(), adapter.TerminateRequest{LaunchKey: "lk1", OwnershipToken: "own1"})
	if err != nil || !rec.Terminated {
		t.Fatalf("already-gone terminate must succeed, rec=%+v err=%v", rec, err)
	}
}

func TestReleaseDeletesLikeTerminate(t *testing.T) {
	fake := newFakeShadeform()
	fake.addInstance(ownedInstance("inst_1", "lk1", "ws_1", "own1", "active", fake.base))
	a := newTestAdapter(t, fake, nil)

	rec, err := a.Release(context.Background(), adapter.ReleaseRequest{LaunchKey: "lk1", OwnershipToken: "own1"})
	if err != nil || !rec.Released || len(fake.deletes) != 1 {
		t.Fatalf("release rec=%+v err=%v deletes=%v", rec, err, fake.deletes)
	}
}

func TestCancelDeletesRegardlessOfOwnershipToken(t *testing.T) {
	fake := newFakeShadeform()
	fake.addInstance(ownedInstance("inst_1", "lk1", "ws_1", "own1", "pending", fake.base))
	a := newTestAdapter(t, fake, nil)

	rec, err := a.Cancel(context.Background(), adapter.CancelRequest{LaunchKey: "lk1"})
	if err != nil || !rec.Cancelled || len(fake.deletes) != 1 {
		t.Fatalf("cancel rec=%+v err=%v deletes=%v", rec, err, fake.deletes)
	}
}

func TestListOwnedFiltersTagsWorkspaceAndDeleting(t *testing.T) {
	fake := newFakeShadeform()
	fake.addInstance(ownedInstance("inst_1", "lk1", "ws_1", "own1", "active", fake.base))
	fake.addInstance(ownedInstance("inst_2", "lk2", "ws_1", "own2", "deleting", fake.base))
	fake.addInstance(ownedInstance("inst_3", "lk3", "ws_2", "own3", "active", fake.base))
	fake.addInstance(instance{ID: "inst_4", Name: "someone-elses-vm", Status: "active", CreatedAt: fake.base})
	a := newTestAdapter(t, fake, nil)

	owned, err := a.ListOwned(context.Background(), adapter.OwnershipQuery{WorkspaceID: "ws_1"})
	if err != nil {
		t.Fatalf("list owned: %v", err)
	}
	if len(owned) != 1 {
		t.Fatalf("owned = %+v, want only the live ws_1 instance", owned)
	}
	o := owned[0]
	if o.ExternalID != "inst_1" || o.LaunchKey != "lk1" || o.OwnershipToken != "own1" ||
		o.RunID != "run_1" || o.AttemptID != "att_1" || o.RequestHash != "rh_1" ||
		o.CleanupLocator != "cl_1" || o.Phase != adapter.ExternalPhaseRunning {
		t.Fatalf("owned[0] = %+v", o)
	}
}

func TestListOwnedWithoutWorkspaceFilterReturnsAllOurs(t *testing.T) {
	fake := newFakeShadeform()
	fake.addInstance(ownedInstance("inst_1", "lk1", "ws_1", "own1", "active", fake.base))
	fake.addInstance(ownedInstance("inst_3", "lk3", "ws_2", "own3", "error", fake.base))
	fake.addInstance(instance{ID: "inst_4", Name: "someone-elses-vm", Status: "active", CreatedAt: fake.base})
	a := newTestAdapter(t, fake, nil)

	owned, err := a.ListOwned(context.Background(), adapter.OwnershipQuery{})
	if err != nil {
		t.Fatalf("list owned: %v", err)
	}
	if len(owned) != 2 {
		t.Fatalf("owned = %+v, want both tagged instances and never the untagged one", owned)
	}
}

func TestNewValidatesConfig(t *testing.T) {
	if _, err := New("k", map[string]string{"shade_cloud": "sometimes"}); err == nil {
		t.Error("invalid shade_cloud must fail loudly")
	}
	if _, err := New("k", map[string]string{"max_lifetime_hours": "0"}); err == nil {
		t.Error("non-positive max_lifetime_hours must fail loudly")
	}
	if _, err := New("k", map[string]string{"max_lifetime_hours": "abc"}); err == nil {
		t.Error("non-numeric max_lifetime_hours must fail loudly")
	}
}

func TestLaunchHonorsShadeCloudFalse(t *testing.T) {
	fake := newFakeShadeform()
	fake.types = []instanceType{vmType()}
	a := newTestAdapter(t, fake, map[string]string{"shade_cloud": "false"})

	if _, err := a.Launch(context.Background(), launchRequest()); err != nil {
		t.Fatalf("launch: %v", err)
	}
	if fake.creates[0].ShadeCloud {
		t.Fatal("shade_cloud=false must reach the create payload")
	}
}
