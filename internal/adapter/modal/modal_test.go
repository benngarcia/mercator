package modal

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	pb "github.com/modal-labs/modal-client/go/proto/modal_proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/scheduler"
)

func launchRequest() adapter.LaunchRequest {
	return adapter.LaunchRequest{
		WorkspaceID:            "ws_1",
		RunID:                  "run_1",
		AttemptID:              "att_1",
		LaunchKey:              "lk1",
		OwnershipToken:         "own1",
		RequestHash:            "rh1",
		CleanupLocator:         "cl1",
		Image:                  "busybox:1.36",
		Args:                   []string{"sh", "-c", "echo hi"},
		SelectedOfferNativeRef: "T4",
	}
}

// Real launch keys are uuidv7-derived and longer than Modal's 64-char object
// name limit; the sandbox name must stay short, deterministic, and unique.
func TestSandboxNameFitsModalLimits(t *testing.T) {
	longKey := "launch_att_ws_1_0198c1c9_7e5f_7c3a_b111_222233334444_abcdef123456"
	name := sandboxName(longKey)
	if len(name) >= 64 {
		t.Fatalf("sandbox name %q is %d chars; Modal requires < 64", name, len(name))
	}
	if name != sandboxName(longKey) {
		t.Fatalf("sandbox name must be deterministic")
	}
	if name == sandboxName(longKey+"x") {
		t.Fatalf("distinct launch keys must yield distinct names")
	}
}

func TestNewRejectsMalformedCredential(t *testing.T) {
	for _, secret := range []string{"", "ak-only", ":as-x", "ak-x:"} {
		if _, err := New(secret, nil); err == nil {
			t.Errorf("New(%q) should reject a credential that is not token_id:token_secret", secret)
		}
	}
}

func TestNewRejectsOutOfRangeTimeout(t *testing.T) {
	for _, raw := range []string{"0", "-5", "4294967296", "not-a-number"} {
		if _, err := New("ak-x:as-y", map[string]string{"timeout_seconds": raw}); err == nil {
			t.Errorf("New with timeout_seconds=%q must fail loudly", raw)
		}
	}
}

func TestNewWithGPUTypesDoesNotMutateDefault(t *testing.T) {
	snapshot := append([]string(nil), defaultGPUTypes...)
	if _, err := New("ak-x:as-y", map[string]string{"gpu_types": "H100"}); err != nil {
		t.Fatalf("New: %v", err)
	}
	if !reflect.DeepEqual(defaultGPUTypes, snapshot) {
		t.Fatalf("New mutated defaultGPUTypes: got %+v, want %+v", defaultGPUTypes, snapshot)
	}
}

func TestVerifyExchangesAuthToken(t *testing.T) {
	fake := &fakeModal{}
	a := newTestAdapter(t, fake, nil)
	if err := a.Verify(context.Background()); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if fake.authCalls != 1 {
		t.Fatalf("verify should call AuthTokenGet once, got %d", fake.authCalls)
	}

	fake.authErr = status.Error(codes.Unauthenticated, "bad token")
	if err := a.Verify(context.Background()); err == nil || !strings.Contains(err.Error(), "Unauthenticated") {
		t.Fatalf("verify with bad credentials should surface Unauthenticated, got %v", err)
	}
}

func TestRPCsCarryCredentialAndAuthHeaders(t *testing.T) {
	fake := &fakeModal{}
	a := newTestAdapter(t, fake, nil)
	if _, err := a.ListOwned(context.Background(), adapter.OwnershipQuery{}); err != nil {
		t.Fatalf("list owned: %v", err)
	}
	if got := outgoingHeader(fake.authCtx, "x-modal-token-id"); len(got) == 0 || got[0] != "ak-test" {
		t.Fatalf("AuthTokenGet must carry the token id header, got %v", got)
	}
	if got := outgoingHeader(fake.authCtx, "x-modal-auth-token"); len(got) != 0 {
		t.Fatalf("AuthTokenGet must not carry an auth token, got %v", got)
	}
	if got := outgoingHeader(fake.listCtx, "x-modal-token-secret"); len(got) == 0 || got[0] != "as-test" {
		t.Fatalf("SandboxList must carry the token secret header, got %v", got)
	}
	if got := outgoingHeader(fake.listCtx, "x-modal-auth-token"); len(got) == 0 || got[0] == "" {
		t.Fatalf("SandboxList must carry the exchanged auth token, got %v", got)
	}
}

func TestAuthTokenIsCachedAcrossRPCs(t *testing.T) {
	fake := &fakeModal{}
	a := newTestAdapter(t, fake, nil)
	if _, err := a.Launch(context.Background(), launchRequest()); err != nil {
		t.Fatalf("launch: %v", err)
	}
	if fake.authCalls != 1 {
		t.Fatalf("the launch RPC chain should reuse one cached auth token, got %d AuthTokenGet calls", fake.authCalls)
	}
}

func TestListOffersSynthesizesCatalogFromAllowlist(t *testing.T) {
	a := newTestAdapter(t, &fakeModal{}, map[string]string{"gpu_types": "cpu, T4, FutureGPU"})
	offers, err := a.ListOffers(context.Background(), adapter.OfferRequest{WorkspaceID: "ws_1"})
	if err != nil {
		t.Fatalf("list offers: %v", err)
	}
	if len(offers) != 3 {
		t.Fatalf("want 3 offers, got %+v", offers)
	}

	cpu, t4, future := offers[0], offers[1], offers[2]
	if len(cpu.Resources.Accelerators) != 0 || !cpu.Pricing.Known || cpu.Pricing.RatePerSecondUSD <= 0 {
		t.Fatalf("cpu offer should be accelerator-free with known nonzero pricing: %+v", cpu)
	}
	if t4.Resources.Accelerators[0].CanonicalModel != "nvidia-t4" || !t4.Pricing.Known {
		t.Fatalf("t4 offer = %+v", t4)
	}
	if t4.Pricing.RatePerSecondUSD <= cpu.Pricing.RatePerSecondUSD {
		t.Fatalf("t4 must price above cpu baseline: t4=%v cpu=%v", t4.Pricing.RatePerSecondUSD, cpu.Pricing.RatePerSecondUSD)
	}
	if future.Pricing.Known {
		t.Fatalf("unknown GPU type must advertise unknown pricing, got %+v", future.Pricing)
	}
	for _, o := range offers {
		if o.Kind != domain.OfferKindProvisionable {
			t.Fatalf("modal offers are provisionable, got %+v", o)
		}
	}
}

func TestRequestedAllocationSchedulesAndReachesSandboxCreation(t *testing.T) {
	fake := &fakeModal{}
	a := newTestAdapter(t, fake, map[string]string{"gpu_types": "A100-80GB"})
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
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
	if len(offers) != 1 || offers[0].Resources.Accelerators[0].Count != 2 || offers[0].Resources.EphemeralDiskBytes != 75*gib+1 {
		t.Fatalf("offer does not describe requested allocation: %+v", offers)
	}
	perGPU := gpuRatePerSecondUSD["a100-80gb"]
	if offers[0].Pricing.RatePerSecondUSD < 2*perGPU {
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

	req := launchRequest()
	req.Resources = resources
	req.SelectedOfferNativeRef = offers[0].NativeRef
	if _, err := a.Launch(context.Background(), req); err != nil {
		t.Fatalf("launch: %v", err)
	}
	def := fake.createReqs[0].GetDefinition()
	if def.GetResources().GetGpuConfig().GetGpuType() != "A100-80GB" || def.GetResources().GetGpuConfig().GetCount() != 2 {
		t.Fatalf("sandbox gpu config = %+v", def.GetResources().GetGpuConfig())
	}
	if def.GetResources().GetMilliCpu() != 4000 || def.GetResources().GetMemoryMb() != 8*1024 {
		t.Fatalf("sandbox resources = %+v", def.GetResources())
	}
	if def.GetResources().GetEphemeralDiskMb() != uint32((75*gib+1)/mib) {
		t.Fatalf("sandbox disk = %d", def.GetResources().GetEphemeralDiskMb())
	}
}

func TestLaunchCreatesNamedTaggedSandbox(t *testing.T) {
	fake := &fakeModal{builderVersion: "2025.06"}
	a := newTestAdapter(t, fake, nil)
	val := "v"
	req := launchRequest()
	req.Entrypoint = &[]string{"/bin/run"}
	req.Environment = []adapter.EnvironmentBinding{{Name: "FOO", Value: &val}, {Name: "unset", Value: nil}}

	receipt, err := a.Launch(context.Background(), req)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if receipt.ExternalID != "sb-1" || receipt.Phase != adapter.ExternalPhaseQueued || receipt.Duplicate {
		t.Fatalf("receipt = %+v", receipt)
	}

	create := fake.createReqs[0]
	def := create.GetDefinition()
	if def.GetName() != sandboxName("lk1") {
		t.Fatalf("sandbox name = %q", def.GetName())
	}
	if !reflect.DeepEqual(def.GetEntrypointArgs(), []string{"/bin/run", "sh", "-c", "echo hi"}) {
		t.Fatalf("entrypoint args = %v", def.GetEntrypointArgs())
	}
	if def.GetTimeoutSecs() != defaultTimeoutSecs {
		t.Fatalf("timeout = %d", def.GetTimeoutSecs())
	}
	if def.GetNetworkAccess().GetNetworkAccessType() != pb.NetworkAccess_OPEN {
		t.Fatalf("network access = %v", def.GetNetworkAccess())
	}
	tags := tagMap(create.GetTags())
	want := map[string]string{
		tagWorkspaceID: "ws_1", tagRunID: "run_1", tagAttemptID: "att_1",
		tagLaunchKey: "lk1", tagOwnershipToken: "own1", tagRequestHash: "rh1", tagCleanupLocator: "cl1",
	}
	if !reflect.DeepEqual(tags, want) {
		t.Fatalf("tags = %+v", tags)
	}

	if got := fake.imageReqs[0].GetImage().GetDockerfileCommands(); !reflect.DeepEqual(got, []string{"FROM busybox:1.36"}) {
		t.Fatalf("image dockerfile = %v", got)
	}
	if fake.imageReqs[0].GetBuilderVersion() != "2025.06" {
		t.Fatalf("builder version should come from the environment, got %q", fake.imageReqs[0].GetBuilderVersion())
	}

	env := fake.secretReqs[0].GetEnvDict()
	if env["FOO"] != "v" || env["MERCATOR_OWNERSHIP_TOKEN"] != "own1" || env["MERCATOR_LAUNCH_KEY"] != "lk1" {
		t.Fatalf("sandbox env = %+v", env)
	}
	if _, present := env["unset"]; present {
		t.Fatalf("nil-valued env bindings must be skipped, got %+v", env)
	}
	if fake.secretReqs[0].GetObjectCreationType() != pb.ObjectCreationType_OBJECT_CREATION_TYPE_EPHEMERAL {
		t.Fatalf("env secret must be ephemeral, got %v", fake.secretReqs[0].GetObjectCreationType())
	}
}

func TestLaunchCPUOfferRequestsNoGPU(t *testing.T) {
	fake := &fakeModal{}
	a := newTestAdapter(t, fake, map[string]string{"gpu_types": "cpu"})
	req := launchRequest()
	req.SelectedOfferNativeRef = "cpu"
	if _, err := a.Launch(context.Background(), req); err != nil {
		t.Fatalf("launch: %v", err)
	}
	if gpu := fake.createReqs[0].GetDefinition().GetResources().GetGpuConfig(); gpu != nil {
		t.Fatalf("cpu launch must not request a GPU, got %+v", gpu)
	}
}

func TestLaunchWithoutCommandFailsLoudly(t *testing.T) {
	a := newTestAdapter(t, &fakeModal{}, nil)
	req := launchRequest()
	req.Entrypoint = nil
	req.Args = nil
	if _, err := a.Launch(context.Background(), req); err == nil || !strings.Contains(err.Error(), "entrypoint or args") {
		t.Fatalf("empty command must fail (Modal never runs the image CMD), got %v", err)
	}
}

func TestLaunchRetryReturnsDuplicateReceipt(t *testing.T) {
	fake := &fakeModal{}
	a := newTestAdapter(t, fake, nil)
	first, err := a.Launch(context.Background(), launchRequest())
	if err != nil {
		t.Fatalf("first launch: %v", err)
	}
	second, err := a.Launch(context.Background(), launchRequest())
	if err != nil {
		t.Fatalf("retried launch: %v", err)
	}
	if !second.Duplicate || second.ExternalID != first.ExternalID {
		t.Fatalf("retry must dedupe on sandbox name: first=%+v second=%+v", first, second)
	}
	if len(fake.sandboxes) != 1 {
		t.Fatalf("retry must not create a second sandbox, got %d", len(fake.sandboxes))
	}
}

// Modal frees a sandbox name once the sandbox exits, so create-time
// AlreadyExists cannot catch a retry that arrives after the first attempt ran
// to completion — the pre-create ownership lookup must dedupe it, or the
// workload would run twice.
func TestLaunchRetryAfterExitReturnsDuplicate(t *testing.T) {
	fake := &fakeModal{}
	finished := fake.addSandbox(&fakeSandbox{
		name:      sandboxName("lk1"),
		tags:      ownershipTagsFor("ws_1", "run_1", "att_1", "lk1", "own1"),
		startedAt: 42,
		result:    resultWithStatus(pb.GenericResult_GENERIC_STATUS_SUCCESS, 0),
	})
	a := newTestAdapter(t, fake, nil)
	receipt, err := a.Launch(context.Background(), launchRequest())
	if err != nil {
		t.Fatalf("launch retry: %v", err)
	}
	if !receipt.Duplicate || receipt.ExternalID != finished.id || receipt.Phase != adapter.ExternalPhaseSucceeded {
		t.Fatalf("retry after exit must dedupe to the finished sandbox: %+v", receipt)
	}
	if len(fake.createReqs) != 0 || len(fake.sandboxes) != 1 {
		t.Fatalf("retry after exit must not create a second sandbox (creates=%d sandboxes=%d)", len(fake.createReqs), len(fake.sandboxes))
	}
}

// An ambiguous transport failure on SandboxCreate may have committed the
// sandbox server-side; the orchestrator must reconcile, not conclude that
// nothing external exists.
func TestAmbiguousCreateFailureIsIndeterminate(t *testing.T) {
	fake := &fakeModal{createErr: status.Error(codes.Unavailable, "connection reset")}
	a := newTestAdapter(t, fake, nil)
	_, err := a.Launch(context.Background(), launchRequest())
	if !errors.Is(err, adapter.ErrLaunchIndeterminate) {
		t.Fatalf("ambiguous SandboxCreate failure must map to ErrLaunchIndeterminate, got %v", err)
	}

	fake = &fakeModal{createErr: status.Error(codes.InvalidArgument, "bad definition")}
	a = newTestAdapter(t, fake, nil)
	_, err = a.Launch(context.Background(), launchRequest())
	if err == nil || errors.Is(err, adapter.ErrLaunchIndeterminate) {
		t.Fatalf("a definitive rejection must stay a plain error, got %v", err)
	}
}

func TestLaunchNameCollisionWithForeignTokenConflicts(t *testing.T) {
	fake := &fakeModal{}
	fake.addSandbox(&fakeSandbox{
		name: sandboxName("lk1"),
		tags: ownershipTagsFor("ws_1", "run_0", "att_0", "lk1", "someone-else"),
	})
	a := newTestAdapter(t, fake, nil)
	if _, err := a.Launch(context.Background(), launchRequest()); !errors.Is(err, adapter.ErrIdempotencyConflict) {
		t.Fatalf("expected ErrIdempotencyConflict, got %v", err)
	}
}

func TestLaunchWaitsForPendingImageBuild(t *testing.T) {
	fake := &fakeModal{
		imagePending: true,
		imageStream: []*pb.ImageJoinStreamingResponse{
			pb.ImageJoinStreamingResponse_builder{EntryId: "e1"}.Build(),
			pb.ImageJoinStreamingResponse_builder{
				Result: resultWithStatus(pb.GenericResult_GENERIC_STATUS_SUCCESS, 0),
			}.Build(),
		},
	}
	a := newTestAdapter(t, fake, nil)
	if _, err := a.Launch(context.Background(), launchRequest()); err != nil {
		t.Fatalf("launch should wait out a pending image build: %v", err)
	}
	if len(fake.createReqs) != 1 {
		t.Fatalf("sandbox create should happen after the build completes")
	}
}

func TestLaunchSurfacesImageBuildFailure(t *testing.T) {
	fake := &fakeModal{
		imageResult: pb.GenericResult_builder{
			Status:    pb.GenericResult_GENERIC_STATUS_FAILURE,
			Exception: "manifest for busybox:1.36 not found",
		}.Build(),
	}
	a := newTestAdapter(t, fake, nil)
	_, err := a.Launch(context.Background(), launchRequest())
	if err == nil || !strings.Contains(err.Error(), "manifest for busybox:1.36 not found") {
		t.Fatalf("image build failure must surface the builder exception, got %v", err)
	}
	if len(fake.createReqs) != 0 {
		t.Fatalf("no sandbox may be created on a failed image build")
	}
}

func TestObserveQueuedThenRunning(t *testing.T) {
	fake := &fakeModal{}
	sandbox := fake.addSandbox(&fakeSandbox{
		name: sandboxName("lk1"),
		tags: ownershipTagsFor("ws_1", "run_1", "att_1", "lk1", "own1"),
	})
	a := newTestAdapter(t, fake, nil)

	obs, err := a.Observe(context.Background(), adapter.ObserveRequest{LaunchKey: "lk1", OwnershipToken: "own1"})
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if obs.Phase != adapter.ExternalPhaseQueued || obs.ExitCode != nil {
		t.Fatalf("unstarted sandbox should observe queued, got %+v", obs)
	}

	sandbox.startedAt = 42
	obs, err = a.Observe(context.Background(), adapter.ObserveRequest{LaunchKey: "lk1", OwnershipToken: "own1"})
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if obs.Phase != adapter.ExternalPhaseRunning || obs.ExitCode != nil || obs.ExternalID != sandbox.id {
		t.Fatalf("started sandbox should observe running, got %+v", obs)
	}
}

// Modal reports real exit codes; Observe is authoritative on success/failure,
// unlike RunPod's pessimistic EXITED→failed mapping.
func TestObserveExitCodesAreAuthoritative(t *testing.T) {
	cases := []struct {
		name     string
		result   *pb.GenericResult
		phase    adapter.ExternalPhase
		exitCode *int
	}{
		{"success", resultWithStatus(pb.GenericResult_GENERIC_STATUS_SUCCESS, 0), adapter.ExternalPhaseSucceeded, intPtr(0)},
		{"failure keeps real code", resultWithStatus(pb.GenericResult_GENERIC_STATUS_FAILURE, 3), adapter.ExternalPhaseFailed, intPtr(3)},
		{"timeout", resultWithStatus(pb.GenericResult_GENERIC_STATUS_TIMEOUT, 0), adapter.ExternalPhaseFailed, intPtr(124)},
		{"external kill", resultWithStatus(pb.GenericResult_GENERIC_STATUS_TERMINATED, 0), adapter.ExternalPhaseFailed, intPtr(137)},
		{"internal failure", resultWithStatus(pb.GenericResult_GENERIC_STATUS_INTERNAL_FAILURE, 0), adapter.ExternalPhaseFailed, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeModal{}
			fake.addSandbox(&fakeSandbox{
				name: sandboxName("lk1"),
				tags:      ownershipTagsFor("ws_1", "run_1", "att_1", "lk1", "own1"),
				startedAt: 42,
				result:    tc.result,
			})
			a := newTestAdapter(t, fake, nil)
			obs, err := a.Observe(context.Background(), adapter.ObserveRequest{LaunchKey: "lk1", OwnershipToken: "own1"})
			if err != nil {
				t.Fatalf("observe: %v", err)
			}
			if obs.Phase != tc.phase {
				t.Fatalf("phase = %q, want %q", obs.Phase, tc.phase)
			}
			if (obs.ExitCode == nil) != (tc.exitCode == nil) || (obs.ExitCode != nil && *obs.ExitCode != *tc.exitCode) {
				t.Fatalf("exit code = %v, want %v", obs.ExitCode, tc.exitCode)
			}
			if obs.ExitCode != nil && !obs.Phase.Exited() {
				t.Fatalf("exit code set on non-exited phase %q", obs.Phase)
			}
		})
	}
}

// The high-level Modal SDKs hide finished sandboxes from listings; the adapter
// must request them explicitly or every exited run would be observed as
// released and lose its exit code.
func TestObserveFindsFinishedSandboxes(t *testing.T) {
	fake := &fakeModal{}
	fake.addSandbox(&fakeSandbox{
		name: sandboxName("lk1"),
		tags:      ownershipTagsFor("ws_1", "run_1", "att_1", "lk1", "own1"),
		startedAt: 42,
		result:    resultWithStatus(pb.GenericResult_GENERIC_STATUS_SUCCESS, 0),
	})
	a := newTestAdapter(t, fake, nil)
	obs, err := a.Observe(context.Background(), adapter.ObserveRequest{LaunchKey: "lk1", OwnershipToken: "own1"})
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if obs.Phase != adapter.ExternalPhaseSucceeded {
		t.Fatalf("finished sandbox must be observable, got %+v", obs)
	}
	if !fake.listReqs[0].GetIncludeFinished() {
		t.Fatalf("observe must list with include_finished=true")
	}
}

// The tag listing can lag a just-created sandbox; the by-name index is
// consistent with create, so Observe must fall back to it before concluding
// the sandbox is gone (a false "gone" reads as Released and abandons a live,
// billing sandbox).
func TestObserveFallsBackToNameLookupWhenListLags(t *testing.T) {
	fake := &fakeModal{}
	sandbox := fake.addSandbox(&fakeSandbox{
		name:           sandboxName("lk1"),
		tags:           ownershipTagsFor("ws_1", "run_1", "att_1", "lk1", "own1"),
		hiddenFromList: true,
	})
	a := newTestAdapter(t, fake, nil)
	obs, err := a.Observe(context.Background(), adapter.ObserveRequest{LaunchKey: "lk1", OwnershipToken: "own1"})
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if obs.Phase == adapter.ExternalPhaseReleased {
		t.Fatalf("a sandbox resolvable by name must not be observed as released")
	}
	if obs.ExternalID != sandbox.id {
		t.Fatalf("observation should carry the resolved sandbox id, got %+v", obs)
	}
}

func TestObserveMissingSandboxIsReleased(t *testing.T) {
	a := newTestAdapter(t, &fakeModal{}, nil)
	obs, err := a.Observe(context.Background(), adapter.ObserveRequest{LaunchKey: "lk1", OwnershipToken: "own1"})
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if obs.Phase != adapter.ExternalPhaseReleased {
		t.Fatalf("missing sandbox should be released, got %q", obs.Phase)
	}
}

func TestObserveOwnershipMismatchIsConflict(t *testing.T) {
	fake := &fakeModal{}
	fake.addSandbox(&fakeSandbox{
		name: sandboxName("lk1"),
		tags: ownershipTagsFor("ws_1", "run_1", "att_1", "lk1", "someone-else"),
	})
	a := newTestAdapter(t, fake, nil)
	if _, err := a.Observe(context.Background(), adapter.ObserveRequest{LaunchKey: "lk1", OwnershipToken: "own1"}); !errors.Is(err, adapter.ErrIdempotencyConflict) {
		t.Fatalf("expected ErrIdempotencyConflict, got %v", err)
	}
}

func TestCancelTerminatesRegardlessOfOwnershipToken(t *testing.T) {
	fake := &fakeModal{}
	sandbox := fake.addSandbox(&fakeSandbox{
		name: sandboxName("lk1"),
		tags: ownershipTagsFor("ws_1", "run_1", "att_1", "lk1", "own1"),
	})
	a := newTestAdapter(t, fake, nil)
	rec, err := a.Cancel(context.Background(), adapter.CancelRequest{LaunchKey: "lk1"})
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if !rec.Cancelled || len(fake.terminated) != 1 || fake.terminated[0] != sandbox.id {
		t.Fatalf("cancel rec=%+v terminated=%v", rec, fake.terminated)
	}
}

func TestReleaseAndTerminateResolveByTagAndTerminate(t *testing.T) {
	fake := &fakeModal{}
	sandbox := fake.addSandbox(&fakeSandbox{
		name: sandboxName("lk1"),
		tags: ownershipTagsFor("ws_1", "run_1", "att_1", "lk1", "own1"),
	})
	a := newTestAdapter(t, fake, nil)

	rel, err := a.Release(context.Background(), adapter.ReleaseRequest{LaunchKey: "lk1", OwnershipToken: "own1"})
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if !rel.Released || fake.terminated[0] != sandbox.id {
		t.Fatalf("release rec=%+v terminated=%v", rel, fake.terminated)
	}

	// Terminate after the sandbox is already finished still succeeds (idempotent cleanup).
	term, err := a.Terminate(context.Background(), adapter.TerminateRequest{LaunchKey: "lk1", OwnershipToken: "own1"})
	if err != nil {
		t.Fatalf("terminate: %v", err)
	}
	if !term.Terminated {
		t.Fatalf("terminate rec=%+v", term)
	}
}

func TestTerminateMissingSandboxIsIdempotent(t *testing.T) {
	a := newTestAdapter(t, &fakeModal{}, nil)
	rec, err := a.Terminate(context.Background(), adapter.TerminateRequest{LaunchKey: "lk1", OwnershipToken: "own1"})
	if err != nil {
		t.Fatalf("terminate: %v", err)
	}
	if !rec.Terminated {
		t.Fatalf("terminating an already-gone sandbox must report terminated, got %+v", rec)
	}
}

func TestListOwnedMapsTagsToFields(t *testing.T) {
	fake := &fakeModal{}
	fake.addSandbox(&fakeSandbox{
		name: sandboxName("lk1"),
		tags:      ownershipTagsFor("ws_1", "run_1", "att_1", "lk1", "own1"),
		startedAt: 42,
	})
	fake.addSandbox(&fakeSandbox{name: "someone-elses-sandbox", tags: map[string]string{"team": "other"}})
	fake.addSandbox(&fakeSandbox{
		name: sandboxName("lk2"),
		tags: ownershipTagsFor("ws_2", "run_2", "att_2", "lk2", "own2"),
	})
	a := newTestAdapter(t, fake, nil)

	owned, err := a.ListOwned(context.Background(), adapter.OwnershipQuery{WorkspaceID: "ws_1"})
	if err != nil {
		t.Fatalf("list owned: %v", err)
	}
	if len(owned) != 1 {
		t.Fatalf("workspace filter failed: %+v", owned)
	}
	got := owned[0]
	if got.RunID != "run_1" || got.AttemptID != "att_1" || got.OwnershipToken != "own1" ||
		got.LaunchKey != "lk1" || got.RequestHash != "rh1" || got.CleanupLocator != "cl1" ||
		got.WorkspaceID != "ws_1" || got.Phase != adapter.ExternalPhaseRunning {
		t.Fatalf("owned = %+v", got)
	}

	all, err := a.ListOwned(context.Background(), adapter.OwnershipQuery{})
	if err != nil {
		t.Fatalf("list owned: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("unfiltered ListOwned must return every Mercator sandbox and skip foreign ones: %+v", all)
	}
}

// Reclamation lists only live sandboxes: exited ones hold no capacity and
// Modal keeps their records indefinitely.
func TestListOwnedExcludesFinishedSandboxes(t *testing.T) {
	fake := &fakeModal{}
	fake.addSandbox(&fakeSandbox{
		name: sandboxName("lk1"),
		tags:   ownershipTagsFor("ws_1", "run_1", "att_1", "lk1", "own1"),
		result: resultWithStatus(pb.GenericResult_GENERIC_STATUS_SUCCESS, 0),
	})
	a := newTestAdapter(t, fake, nil)
	owned, err := a.ListOwned(context.Background(), adapter.OwnershipQuery{WorkspaceID: "ws_1"})
	if err != nil {
		t.Fatalf("list owned: %v", err)
	}
	if len(owned) != 0 {
		t.Fatalf("finished sandboxes must not appear in reclamation listing: %+v", owned)
	}
	if fake.listReqs[0].GetIncludeFinished() {
		t.Fatalf("ListOwned must list with include_finished=false")
	}
}

// Some Modal deployments omit tags from SandboxList responses; the adapter
// falls back to SandboxTagsGet so ownership never depends on the listing's
// completeness.
func TestFindOwnedFetchesTagsWhenListOmitsThem(t *testing.T) {
	fake := &fakeModal{omitTagsInList: true}
	sandbox := fake.addSandbox(&fakeSandbox{
		name: sandboxName("lk1"),
		tags:      ownershipTagsFor("ws_1", "run_1", "att_1", "lk1", "own1"),
		startedAt: 42,
	})
	a := newTestAdapter(t, fake, nil)
	obs, err := a.Observe(context.Background(), adapter.ObserveRequest{LaunchKey: "lk1", OwnershipToken: "own1"})
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if obs.Phase != adapter.ExternalPhaseRunning {
		t.Fatalf("observe with tag fallback = %+v", obs)
	}
	if len(fake.tagsGetCalls) == 0 || fake.tagsGetCalls[0] != sandbox.id {
		t.Fatalf("expected SandboxTagsGet fallback, calls=%v", fake.tagsGetCalls)
	}
}

func TestRetryInterceptorRetriesTransientUnderOneIdempotencyKey(t *testing.T) {
	calls := 0
	var keys, attempts []string
	inv := func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, opts ...grpc.CallOption) error {
		md, _ := metadata.FromOutgoingContext(ctx)
		keys = append(keys, md.Get("x-idempotency-key")[0])
		attempts = append(attempts, md.Get("x-retry-attempt")[0])
		calls++
		if calls == 1 {
			return status.Error(codes.Unavailable, "blip")
		}
		return nil
	}
	if err := retryUnaryInterceptor(context.Background(), "/modal.client.ModalClient/SandboxList", nil, nil, nil, inv); err != nil {
		t.Fatalf("transient failure should be retried away: %v", err)
	}
	if calls != 2 || keys[0] != keys[1] {
		t.Fatalf("retry must reuse one idempotency key: calls=%d keys=%v", calls, keys)
	}
	if attempts[0] != "0" || attempts[1] != "1" {
		t.Fatalf("x-retry-attempt must count attempts, got %v", attempts)
	}
}

func TestRetryInterceptorDoesNotRetryDefinitiveErrors(t *testing.T) {
	calls := 0
	inv := func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, opts ...grpc.CallOption) error {
		calls++
		return status.Error(codes.InvalidArgument, "bad request")
	}
	if err := retryUnaryInterceptor(context.Background(), "/m", nil, nil, nil, inv); err == nil {
		t.Fatalf("definitive error must propagate")
	}
	if calls != 1 {
		t.Fatalf("definitive error must not be retried, got %d calls", calls)
	}
}

// A refresh failure while the cached token is still valid must serve the
// cached token instead of failing the caller's RPC.
func TestAuthTokenCacheServesStaleOnRefreshFailure(t *testing.T) {
	cache := &authTokenCache{token: "still-valid", exp: time.Now().Add(2 * time.Minute)} // inside refresh window
	failing := func(context.Context) (string, time.Time, error) {
		return "", time.Time{}, errors.New("control plane down")
	}
	got, err := cache.get(context.Background(), failing)
	if err != nil || got != "still-valid" {
		t.Fatalf("expected stale-if-error fallback, got %q err=%v", got, err)
	}

	cache = &authTokenCache{token: "expired", exp: time.Now().Add(-time.Minute)}
	if _, err := cache.get(context.Background(), failing); err == nil {
		t.Fatalf("an expired token with a failing refresh must error")
	}
}

func TestListSandboxesPaginates(t *testing.T) {
	fake := &fakeModal{}
	for i := 0; i < 5; i++ {
		fake.addSandbox(&fakeSandbox{
			name: "mercator-lk" + itoa(int64(i)),
			tags: ownershipTagsFor("ws_1", "run", "att", "lk"+itoa(int64(i)), "own"),
		})
	}
	a := newTestAdapter(t, fake, nil)
	owned, err := a.ListOwned(context.Background(), adapter.OwnershipQuery{WorkspaceID: "ws_1"})
	if err != nil {
		t.Fatalf("list owned: %v", err)
	}
	if len(owned) != 5 {
		t.Fatalf("pagination lost sandboxes: got %d, want 5", len(owned))
	}
	if len(fake.listReqs) < 3 {
		t.Fatalf("expected multiple list pages, got %d requests", len(fake.listReqs))
	}
}
