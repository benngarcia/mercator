package modal

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	pb "github.com/modal-labs/modal-client/go/proto/modal_proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const defaultServerURL = "https://api.modal.com:443"

// stubVersion is the modal-client Go module release whose generated stubs this
// client speaks. Sent as x-modal-libmodal-version so the server applies the
// same protocol gating it would to that SDK version.
const stubVersion = "0.9.0"

var errAlreadyExists = errors.New("modal: sandbox name already exists")

// apiClient speaks Modal's gRPC control plane directly through the generated
// proto stubs. Mercator needs SandboxList(include_finished=true) to resolve a
// launch key to an exited sandbox — the high-level SDK hardcodes
// include_finished=false and its FromName only resolves running sandboxes, so
// the wrapper cannot express the adapter's Observe contract.
type apiClient struct {
	pb          pb.ModalClientClient
	conn        *grpc.ClientConn
	tokenID     string
	tokenSecret string
	environment string
	now         func() time.Time

	mu        sync.Mutex
	authToken string
	authExp   time.Time
}

func newAPIClient(tokenID, tokenSecret, environment, serverURL string) (*apiClient, error) {
	if serverURL == "" {
		serverURL = defaultServerURL
	}
	parsed, err := url.Parse(serverURL)
	if err != nil {
		return nil, fmt.Errorf("modal: parse server_url: %w", err)
	}
	target := parsed.Host
	if target == "" {
		target = parsed.Path // bare "host:port" parses as Path
	}
	if !strings.Contains(target, ":") {
		target += ":443"
	}
	// grpc.NewClient connects lazily; construction never touches the network.
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{})))
	if err != nil {
		return nil, fmt.Errorf("modal: dial %s: %w", target, err)
	}
	return &apiClient{
		pb:          pb.NewModalClientClient(conn),
		conn:        conn,
		tokenID:     tokenID,
		tokenSecret: tokenSecret,
		environment: environment,
		now:         time.Now,
	}, nil
}

// baseCtx attaches the credential headers every Modal RPC requires.
func (c *apiClient) baseCtx(ctx context.Context) context.Context {
	return metadata.AppendToOutgoingContext(ctx,
		"x-modal-client-type", strconv.Itoa(int(pb.ClientType_CLIENT_TYPE_LIBMODAL_GO)),
		"x-modal-client-version", "1.0.0",
		"x-modal-libmodal-version", "modal-go/"+stubVersion,
		"x-modal-token-id", c.tokenID,
		"x-modal-token-secret", c.tokenSecret,
	)
}

// authedCtx attaches credential headers plus the short-lived auth token the
// server requires on every RPC except AuthTokenGet itself.
func (c *apiClient) authedCtx(ctx context.Context) (context.Context, error) {
	token, err := c.getAuthToken(ctx)
	if err != nil {
		return nil, err
	}
	return metadata.AppendToOutgoingContext(c.baseCtx(ctx), "x-modal-auth-token", token), nil
}

const authRefreshWindow = 5 * time.Minute

func (c *apiClient) getAuthToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.authToken != "" && c.now().Before(c.authExp.Add(-authRefreshWindow)) {
		return c.authToken, nil
	}
	resp, err := c.pb.AuthTokenGet(c.baseCtx(ctx), pb.AuthTokenGetRequest_builder{}.Build())
	if err != nil {
		return "", rpcError("AuthTokenGet", err)
	}
	c.authToken = resp.GetToken()
	c.authExp = jwtExpiry(c.authToken, c.now())
	return c.authToken, nil
}

// jwtExpiry reads the exp claim from a JWT without verifying it (the server
// signed it; we only need the refresh deadline). Unparseable tokens get a
// short fallback lifetime so they are refreshed rather than kept forever.
func jwtExpiry(token string, now time.Time) time.Time {
	fallback := now.Add(20 * time.Minute)
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return fallback
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return fallback
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp == 0 {
		return fallback
	}
	return time.Unix(claims.Exp, 0)
}

// rpcError formats a failed RPC without ever including credential material.
func rpcError(rpc string, err error) error {
	if s, ok := status.FromError(err); ok {
		return fmt.Errorf("modal: %s -> %s: %s", rpc, s.Code(), s.Message())
	}
	return fmt.Errorf("modal: %s: %w", rpc, err)
}

func (c *apiClient) verify(ctx context.Context) error {
	_, err := c.pb.AuthTokenGet(c.baseCtx(ctx), pb.AuthTokenGetRequest_builder{}.Build())
	if err != nil {
		return rpcError("AuthTokenGet", err)
	}
	return nil
}

func (c *apiClient) appID(ctx context.Context, appName string) (string, error) {
	ctx, err := c.authedCtx(ctx)
	if err != nil {
		return "", err
	}
	resp, err := c.pb.AppGetOrCreate(ctx, pb.AppGetOrCreateRequest_builder{
		AppName:            appName,
		EnvironmentName:    c.environment,
		ObjectCreationType: pb.ObjectCreationType_OBJECT_CREATION_TYPE_CREATE_IF_MISSING,
	}.Build())
	if err != nil {
		return "", rpcError("AppGetOrCreate", err)
	}
	return resp.GetAppId(), nil
}

func (c *apiClient) imageBuilderVersion(ctx context.Context) (string, error) {
	ctx, err := c.authedCtx(ctx)
	if err != nil {
		return "", err
	}
	resp, err := c.pb.EnvironmentGetOrCreate(ctx, pb.EnvironmentGetOrCreateRequest_builder{
		DeploymentName: c.environment,
	}.Build())
	if err != nil {
		return "", rpcError("EnvironmentGetOrCreate", err)
	}
	return resp.GetMetadata().GetSettings().GetImageBuilderVersion(), nil
}

// buildImage registers the registry image with Modal and waits until the
// backend has finished building (pulling) it; SandboxCreate requires a built
// image id.
func (c *apiClient) buildImage(ctx context.Context, appID, imageRef, builderVersion string) (string, error) {
	authed, err := c.authedCtx(ctx)
	if err != nil {
		return "", err
	}
	resp, err := c.pb.ImageGetOrCreate(authed, pb.ImageGetOrCreateRequest_builder{
		AppId: appID,
		Image: pb.Image_builder{
			DockerfileCommands: []string{"FROM " + imageRef},
		}.Build(),
		BuilderVersion: builderVersion,
	}.Build())
	if err != nil {
		return "", rpcError("ImageGetOrCreate", err)
	}
	result := resp.GetResult()
	lastEntryID := ""
	for result.GetStatus() == pb.GenericResult_GENERIC_STATUS_UNSPECIFIED {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		result, lastEntryID, err = c.waitBuildIteration(ctx, resp.GetImageId(), lastEntryID)
		if err != nil {
			return "", err
		}
	}
	if result.GetStatus() != pb.GenericResult_GENERIC_STATUS_SUCCESS {
		return "", fmt.Errorf("modal: image build for %q failed (%s): %s", imageRef, result.GetStatus(), result.GetException())
	}
	return resp.GetImageId(), nil
}

func (c *apiClient) waitBuildIteration(ctx context.Context, imageID, lastEntryID string) (*pb.GenericResult, string, error) {
	authed, err := c.authedCtx(ctx)
	if err != nil {
		return nil, lastEntryID, err
	}
	stream, err := c.pb.ImageJoinStreaming(authed, pb.ImageJoinStreamingRequest_builder{
		ImageId:     imageID,
		Timeout:     55,
		LastEntryId: lastEntryID,
	}.Build())
	if err != nil {
		return nil, lastEntryID, rpcError("ImageJoinStreaming", err)
	}
	for {
		item, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil, lastEntryID, nil // stream window elapsed; caller re-joins
		}
		if err != nil {
			return nil, lastEntryID, rpcError("ImageJoinStreaming", err)
		}
		if item.GetEntryId() != "" {
			lastEntryID = item.GetEntryId()
		}
		if item.GetResult().GetStatus() != pb.GenericResult_GENERIC_STATUS_UNSPECIFIED {
			return item.GetResult(), lastEntryID, nil
		}
	}
}

// createEnvSecret stores the sandbox environment as an ephemeral Modal secret;
// SandboxCreate only accepts environment variables by secret id.
func (c *apiClient) createEnvSecret(ctx context.Context, env map[string]string) (string, error) {
	ctx, err := c.authedCtx(ctx)
	if err != nil {
		return "", err
	}
	resp, err := c.pb.SecretGetOrCreate(ctx, pb.SecretGetOrCreateRequest_builder{
		ObjectCreationType: pb.ObjectCreationType_OBJECT_CREATION_TYPE_EPHEMERAL,
		EnvDict:            env,
		EnvironmentName:    c.environment,
	}.Build())
	if err != nil {
		return "", rpcError("SecretGetOrCreate", err)
	}
	return resp.GetSecretId(), nil
}

type sandboxCreateInput struct {
	appID       string
	name        string
	imageID     string
	command     []string
	secretIDs   []string
	timeoutSecs uint32
	gpuType     string // Modal GPU type, e.g. "T4"; empty for CPU-only
	gpuCount    uint32
	milliCPU    uint32
	memoryMB    uint32
	diskMB      uint32
	tags        map[string]string
}

func (c *apiClient) createSandbox(ctx context.Context, in sandboxCreateInput) (string, error) {
	ctx, err := c.authedCtx(ctx)
	if err != nil {
		return "", err
	}
	var gpuConfig *pb.GPUConfig
	if in.gpuType != "" {
		gpuConfig = pb.GPUConfig_builder{GpuType: strings.ToUpper(in.gpuType), Count: in.gpuCount}.Build()
	}
	resp, err := c.pb.SandboxCreate(ctx, pb.SandboxCreateRequest_builder{
		AppId: in.appID,
		Tags:  tagList(in.tags),
		Definition: pb.Sandbox_builder{
			EntrypointArgs: in.command,
			ImageId:        in.imageID,
			SecretIds:      in.secretIDs,
			TimeoutSecs:    in.timeoutSecs,
			Name:           &in.name,
			NetworkAccess:  pb.NetworkAccess_builder{NetworkAccessType: pb.NetworkAccess_OPEN}.Build(),
			Resources: pb.Resources_builder{
				MilliCpu:        in.milliCPU,
				MemoryMb:        in.memoryMB,
				EphemeralDiskMb: in.diskMB,
				GpuConfig:       gpuConfig,
			}.Build(),
		}.Build(),
	}.Build())
	if err != nil {
		if s, ok := status.FromError(err); ok && s.Code() == codes.AlreadyExists {
			return "", errAlreadyExists
		}
		return "", rpcError("SandboxCreate", err)
	}
	return resp.GetSandboxId(), nil
}

// sandboxInfo is the subset of Modal's SandboxInfo the adapter acts on.
type sandboxInfo struct {
	id        string
	name      string
	tags      map[string]string
	startedAt float64
	result    *pb.GenericResult
}

// listSandboxes returns sandboxes in the connection's environment carrying all
// of the given tags. includeFinished extends the listing to exited sandboxes,
// which Observe needs to read authoritative exit codes.
func (c *apiClient) listSandboxes(ctx context.Context, tags map[string]string, includeFinished bool) ([]sandboxInfo, error) {
	authed, err := c.authedCtx(ctx)
	if err != nil {
		return nil, err
	}
	var infos []sandboxInfo
	before := float64(0)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		resp, err := c.pb.SandboxList(authed, pb.SandboxListRequest_builder{
			EnvironmentName: c.environment,
			BeforeTimestamp: before,
			IncludeFinished: includeFinished,
			Tags:            tagList(tags),
		}.Build())
		if err != nil {
			return nil, rpcError("SandboxList", err)
		}
		page := resp.GetSandboxes()
		if len(page) == 0 {
			return infos, nil
		}
		for _, s := range page {
			infos = append(infos, sandboxInfo{
				id:        s.GetId(),
				name:      s.GetName(),
				tags:      tagMap(s.GetTags()),
				startedAt: s.GetTaskInfo().GetStartedAt(),
				result:    s.GetTaskInfo().GetResult(),
			})
		}
		before = page[len(page)-1].GetCreatedAt()
	}
}

func (c *apiClient) sandboxTags(ctx context.Context, sandboxID string) (map[string]string, error) {
	ctx, err := c.authedCtx(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := c.pb.SandboxTagsGet(ctx, pb.SandboxTagsGetRequest_builder{SandboxId: sandboxID}.Build())
	if err != nil {
		return nil, rpcError("SandboxTagsGet", err)
	}
	return tagMap(resp.GetTags()), nil
}

// sandboxResult polls the sandbox without blocking. A nil-status result means
// the sandbox has not exited yet.
func (c *apiClient) sandboxResult(ctx context.Context, sandboxID string) (*pb.GenericResult, error) {
	ctx, err := c.authedCtx(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := c.pb.SandboxWait(ctx, pb.SandboxWaitRequest_builder{SandboxId: sandboxID, Timeout: 0}.Build())
	if err != nil {
		return nil, rpcError("SandboxWait", err)
	}
	return resp.GetResult(), nil
}

func (c *apiClient) terminateSandbox(ctx context.Context, sandboxID string) error {
	ctx, err := c.authedCtx(ctx)
	if err != nil {
		return err
	}
	_, err = c.pb.SandboxTerminate(ctx, pb.SandboxTerminateRequest_builder{SandboxId: sandboxID}.Build())
	if err != nil {
		if s, ok := status.FromError(err); ok && s.Code() == codes.NotFound {
			return nil // already gone — idempotent
		}
		return rpcError("SandboxTerminate", err)
	}
	return nil
}

func tagList(tags map[string]string) []*pb.SandboxTag {
	out := make([]*pb.SandboxTag, 0, len(tags))
	for k, v := range tags {
		out = append(out, pb.SandboxTag_builder{TagName: k, TagValue: v}.Build())
	}
	return out
}

func tagMap(tags []*pb.SandboxTag) map[string]string {
	out := make(map[string]string, len(tags))
	for _, t := range tags {
		out[t.GetTagName()] = t.GetTagValue()
	}
	return out
}
