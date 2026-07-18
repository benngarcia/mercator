package modal

import (
	"context"
	"encoding/base64"
	"io"
	"sort"
	"sync"
	"testing"
	"time"

	pb "github.com/modal-labs/modal-client/go/proto/modal_proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// fakeModal is an in-memory Modal control plane implementing the RPCs the
// adapter uses. Unimplemented pb.ModalClientClient methods panic via the nil
// embedded interface, so an unexpected RPC fails the test loudly.
type fakeModal struct {
	pb.ModalClientClient
	mu sync.Mutex

	authErr        error
	authCalls      int
	authCtx        context.Context
	listCtx        context.Context
	builderVersion string

	imagePending bool // ImageGetOrCreate returns no result; the join stream supplies it
	imageStream  []*pb.ImageJoinStreamingResponse
	imageResult  *pb.GenericResult
	imageReqs    []*pb.ImageGetOrCreateRequest

	secretReqs []*pb.SecretGetOrCreateRequest

	sandboxes      []*fakeSandbox
	nextID         int
	omitTagsInList bool

	createReqs   []*pb.SandboxCreateRequest
	listReqs     []*pb.SandboxListRequest
	tagsGetCalls []string
	terminated   []string
}

type fakeSandbox struct {
	id        string
	name      string
	tags      map[string]string
	startedAt float64
	createdAt float64
	result    *pb.GenericResult
}

func testJWT(exp int64) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"exp":` + itoa(exp) + `}`))
	return "header." + payload + ".sig"
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	var digits []byte
	for v > 0 {
		digits = append([]byte{byte('0' + v%10)}, digits...)
		v /= 10
	}
	return string(digits)
}

func (f *fakeModal) AuthTokenGet(ctx context.Context, _ *pb.AuthTokenGetRequest, _ ...grpc.CallOption) (*pb.AuthTokenGetResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.authCalls++
	f.authCtx = ctx
	if f.authErr != nil {
		return nil, f.authErr
	}
	return pb.AuthTokenGetResponse_builder{Token: testJWT(time.Now().Add(time.Hour).Unix())}.Build(), nil
}

func (f *fakeModal) EnvironmentGetOrCreate(ctx context.Context, req *pb.EnvironmentGetOrCreateRequest, _ ...grpc.CallOption) (*pb.EnvironmentGetOrCreateResponse, error) {
	version := f.builderVersion
	if version == "" {
		version = "2024.10"
	}
	return pb.EnvironmentGetOrCreateResponse_builder{
		EnvironmentId: "en-1",
		Metadata: pb.EnvironmentMetadata_builder{
			Name:     req.GetDeploymentName(),
			Settings: pb.EnvironmentSettings_builder{ImageBuilderVersion: version}.Build(),
		}.Build(),
	}.Build(), nil
}

func (f *fakeModal) AppGetOrCreate(ctx context.Context, req *pb.AppGetOrCreateRequest, _ ...grpc.CallOption) (*pb.AppGetOrCreateResponse, error) {
	return pb.AppGetOrCreateResponse_builder{AppId: "ap-" + req.GetAppName()}.Build(), nil
}

func (f *fakeModal) ImageGetOrCreate(ctx context.Context, req *pb.ImageGetOrCreateRequest, _ ...grpc.CallOption) (*pb.ImageGetOrCreateResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.imageReqs = append(f.imageReqs, req)
	result := f.imageResult
	if result == nil && !f.imagePending {
		result = pb.GenericResult_builder{Status: pb.GenericResult_GENERIC_STATUS_SUCCESS}.Build()
	}
	return pb.ImageGetOrCreateResponse_builder{ImageId: "im-1", Result: result}.Build(), nil
}

type fakeImageStream struct {
	grpc.ClientStream
	items []*pb.ImageJoinStreamingResponse
	i     int
}

func (s *fakeImageStream) Recv() (*pb.ImageJoinStreamingResponse, error) {
	if s.i >= len(s.items) {
		return nil, io.EOF
	}
	item := s.items[s.i]
	s.i++
	return item, nil
}

func (f *fakeModal) ImageJoinStreaming(ctx context.Context, req *pb.ImageJoinStreamingRequest, _ ...grpc.CallOption) (grpc.ServerStreamingClient[pb.ImageJoinStreamingResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &fakeImageStream{items: f.imageStream}, nil
}

func (f *fakeModal) SecretGetOrCreate(ctx context.Context, req *pb.SecretGetOrCreateRequest, _ ...grpc.CallOption) (*pb.SecretGetOrCreateResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.secretReqs = append(f.secretReqs, req)
	return pb.SecretGetOrCreateResponse_builder{SecretId: "st-1"}.Build(), nil
}

func (f *fakeModal) SandboxCreate(ctx context.Context, req *pb.SandboxCreateRequest, _ ...grpc.CallOption) (*pb.SandboxCreateResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createReqs = append(f.createReqs, req)
	name := req.GetDefinition().GetName()
	for _, s := range f.sandboxes {
		if s.name == name {
			return nil, status.Error(codes.AlreadyExists, "sandbox name already in use")
		}
	}
	f.nextID++
	sandbox := &fakeSandbox{
		id:        "sb-" + itoa(int64(f.nextID)),
		name:      name,
		tags:      tagMap(req.GetTags()),
		createdAt: float64(f.nextID),
	}
	f.sandboxes = append(f.sandboxes, sandbox)
	return pb.SandboxCreateResponse_builder{SandboxId: sandbox.id}.Build(), nil
}

// SandboxList pages two items at a time (descending created_at) to exercise
// the adapter's pagination loop.
func (f *fakeModal) SandboxList(ctx context.Context, req *pb.SandboxListRequest, _ ...grpc.CallOption) (*pb.SandboxListResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listReqs = append(f.listReqs, req)
	f.listCtx = ctx
	filter := tagMap(req.GetTags())
	matches := make([]*fakeSandbox, 0, len(f.sandboxes))
	for _, s := range f.sandboxes {
		if s.result != nil && !req.GetIncludeFinished() {
			continue
		}
		if req.GetBeforeTimestamp() > 0 && s.createdAt >= req.GetBeforeTimestamp() {
			continue
		}
		if !hasAllTags(s.tags, filter) {
			continue
		}
		matches = append(matches, s)
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].createdAt > matches[j].createdAt })
	if len(matches) > 2 {
		matches = matches[:2]
	}
	infos := make([]*pb.SandboxInfo, 0, len(matches))
	for _, s := range matches {
		var tags []*pb.SandboxTag
		if !f.omitTagsInList {
			tags = tagList(s.tags)
		}
		infos = append(infos, pb.SandboxInfo_builder{
			Id:        s.id,
			Name:      s.name,
			CreatedAt: s.createdAt,
			Tags:      tags,
			TaskInfo:  pb.TaskInfo_builder{StartedAt: s.startedAt, Result: s.result}.Build(),
		}.Build())
	}
	return pb.SandboxListResponse_builder{Sandboxes: infos}.Build(), nil
}

func (f *fakeModal) SandboxTagsGet(ctx context.Context, req *pb.SandboxTagsGetRequest, _ ...grpc.CallOption) (*pb.SandboxTagsGetResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tagsGetCalls = append(f.tagsGetCalls, req.GetSandboxId())
	for _, s := range f.sandboxes {
		if s.id == req.GetSandboxId() {
			return pb.SandboxTagsGetResponse_builder{Tags: tagList(s.tags)}.Build(), nil
		}
	}
	return nil, status.Error(codes.NotFound, "no such sandbox")
}

func (f *fakeModal) SandboxWait(ctx context.Context, req *pb.SandboxWaitRequest, _ ...grpc.CallOption) (*pb.SandboxWaitResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.sandboxes {
		if s.id == req.GetSandboxId() {
			return pb.SandboxWaitResponse_builder{Result: s.result}.Build(), nil
		}
	}
	return nil, status.Error(codes.NotFound, "no such sandbox")
}

func (f *fakeModal) SandboxTerminate(ctx context.Context, req *pb.SandboxTerminateRequest, _ ...grpc.CallOption) (*pb.SandboxTerminateResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.terminated = append(f.terminated, req.GetSandboxId())
	for _, s := range f.sandboxes {
		if s.id == req.GetSandboxId() {
			if s.result == nil {
				s.result = pb.GenericResult_builder{Status: pb.GenericResult_GENERIC_STATUS_TERMINATED}.Build()
			}
			return pb.SandboxTerminateResponse_builder{}.Build(), nil
		}
	}
	return nil, status.Error(codes.NotFound, "no such sandbox")
}

func hasAllTags(tags, filter map[string]string) bool {
	for k, v := range filter {
		if tags[k] != v {
			return false
		}
	}
	return true
}

func (f *fakeModal) addSandbox(s *fakeSandbox) *fakeSandbox {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	if s.id == "" {
		s.id = "sb-" + itoa(int64(f.nextID))
	}
	if s.createdAt == 0 {
		s.createdAt = float64(f.nextID)
	}
	f.sandboxes = append(f.sandboxes, s)
	return s
}

func resultWithStatus(st pb.GenericResult_GenericStatus, exitCode int32) *pb.GenericResult {
	return pb.GenericResult_builder{Status: st, Exitcode: exitCode}.Build()
}

func ownershipTagsFor(workspace, run, attempt, launchKey, token string) map[string]string {
	return map[string]string{
		tagWorkspaceID:    workspace,
		tagRunID:          run,
		tagAttemptID:      attempt,
		tagLaunchKey:      launchKey,
		tagOwnershipToken: token,
		tagRequestHash:    "rh1",
		tagCleanupLocator: "cl1",
	}
}

// newTestAdapter builds an Adapter whose gRPC client is replaced by the fake.
func newTestAdapter(t *testing.T, fake *fakeModal, config map[string]string) *Adapter {
	t.Helper()
	if config == nil {
		config = map[string]string{}
	}
	a, err := New("ak-test:as-test", config)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a.api.pb = fake
	return a
}

// outgoingHeader reads a header the client attached to the RPC context.
func outgoingHeader(ctx context.Context, key string) []string {
	md, _ := metadata.FromOutgoingContext(ctx)
	return md.Get(key)
}
