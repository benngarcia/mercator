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

	"github.com/google/uuid"
	pb "github.com/modal-labs/modal-client/go/proto/modal_proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
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
	tokenID     string
	tokenSecret string
	environment string
	tokens      *authTokenCache
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
	conn, err := sharedConn(target)
	if err != nil {
		return nil, err
	}
	return &apiClient{
		pb:          pb.NewModalClientClient(conn),
		tokenID:     tokenID,
		tokenSecret: tokenSecret,
		environment: environment,
		tokens:      sharedAuthTokens(target + "\x00" + tokenID + "\x00" + tokenSecret),
	}, nil
}

// --- connection pool ---

// Connections are shared per dial target for the life of the process, the
// gRPC analogue of the http.DefaultClient transport the RunPod adapter rides:
// the broker builds a fresh adapter per request, and a per-adapter conn would
// leak a TLS session (plus its goroutines) on every Observe poll. Credentials
// ride per-RPC metadata, never the conn, so one conn serves every connection.
var connPool = struct {
	sync.Mutex
	conns map[string]*grpc.ClientConn
}{conns: map[string]*grpc.ClientConn{}}

// maxMessageSize mirrors the official SDK: ImageJoinStreaming responses can
// far exceed gRPC's 4 MB default receive limit.
const maxMessageSize = 100 * 1024 * 1024

func sharedConn(target string) (*grpc.ClientConn, error) {
	connPool.Lock()
	defer connPool.Unlock()
	if conn, ok := connPool.conns[target]; ok {
		return conn, nil
	}
	// grpc.NewClient connects lazily; construction never touches the network.
	conn, err := grpc.NewClient(target,
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{})),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(maxMessageSize),
			grpc.MaxCallSendMsgSize(maxMessageSize),
		),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                30 * time.Second,
			Timeout:             10 * time.Second,
			PermitWithoutStream: true,
		}),
		grpc.WithUnaryInterceptor(retryUnaryInterceptor),
	)
	if err != nil {
		return nil, fmt.Errorf("modal: dial %s: %w", target, err)
	}
	connPool.conns[target] = conn
	return conn, nil
}

// --- retries ---

// Retry behavior mirrors the official SDK: transient codes retry with backoff
// under a stable x-idempotency-key (so the server dedupes replayed mutations
// like SandboxCreate), and a server-directed RPCRetryPolicy (throttling) is
// honored up to a total wait budget.
var (
	retryBaseDelay   = 100 * time.Millisecond
	retryMaxDelay    = 1 * time.Second
	maxThrottleWait  = 60 * time.Second
	retryAttempts    = 3
	retryableCodeSet = map[codes.Code]bool{
		codes.DeadlineExceeded: true,
		codes.Unavailable:      true,
		codes.Canceled:         true,
		codes.Internal:         true,
		codes.Unknown:          true,
	}
)

func retryUnaryInterceptor(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, inv grpc.UnaryInvoker, opts ...grpc.CallOption) error {
	idempotencyKey := uuid.NewString()
	start := time.Now()
	delay := retryBaseDelay
	attempt, throttleRetries := 0, 0
	for {
		attemptCtx := metadata.AppendToOutgoingContext(ctx,
			"x-idempotency-key", idempotencyKey,
			"x-retry-attempt", strconv.Itoa(attempt),
			"x-throttle-retry-attempt", strconv.Itoa(throttleRetries),
		)
		err := inv(attemptCtx, method, req, reply, cc, opts...)
		if err == nil {
			return nil
		}
		if wait, ok := serverRetryDelay(err); ok {
			if time.Since(start)+wait >= maxThrottleWait {
				return err
			}
			throttleRetries++
			if sleepCtx(ctx, wait) != nil {
				return err
			}
			continue
		}
		if attempt >= retryAttempts || ctx.Err() != nil {
			return err
		}
		if s, ok := status.FromError(err); !ok || !retryableCodeSet[s.Code()] {
			return err
		}
		attempt++
		if sleepCtx(ctx, delay) != nil {
			return err
		}
		delay = min(2*delay, retryMaxDelay)
	}
}

// serverRetryDelay extracts the retry-after delay from a server-directed
// RPCRetryPolicy error detail (Modal's throttling channel).
func serverRetryDelay(err error) (time.Duration, bool) {
	s, ok := status.FromError(err)
	if !ok {
		return 0, false
	}
	for _, detail := range s.Details() {
		if policy, ok := detail.(*pb.RPCRetryPolicy); ok {
			return max(time.Duration(float64(policy.GetRetryAfterSecs())*float64(time.Second)), retryBaseDelay), true
		}
	}
	return 0, false
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// --- auth ---

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
	token, err := c.tokens.get(ctx, c.fetchAuthToken)
	if err != nil {
		return nil, err
	}
	return metadata.AppendToOutgoingContext(c.baseCtx(ctx), "x-modal-auth-token", token), nil
}

func (c *apiClient) fetchAuthToken(ctx context.Context) (string, time.Time, error) {
	resp, err := c.pb.AuthTokenGet(c.baseCtx(ctx), pb.AuthTokenGetRequest_builder{}.Build())
	if err != nil {
		return "", time.Time{}, rpcError("AuthTokenGet", err)
	}
	return resp.GetToken(), jwtExpiry(resp.GetToken(), time.Now()), nil
}

const authRefreshWindow = 5 * time.Minute

// authTokenCache holds one credential's exchanged auth token. Cached tokens
// are shared process-wide (keyed by target+credential) so every broker-built
// adapter instance reuses them instead of re-exchanging per request.
type authTokenCache struct {
	mu    sync.Mutex
	token string
	exp   time.Time
}

var authTokens = struct {
	sync.Mutex
	m map[string]*authTokenCache
}{m: map[string]*authTokenCache{}}

func sharedAuthTokens(key string) *authTokenCache {
	authTokens.Lock()
	defer authTokens.Unlock()
	if t, ok := authTokens.m[key]; ok {
		return t
	}
	t := &authTokenCache{}
	authTokens.m[key] = t
	return t
}

// get returns a valid token, refreshing as needed. The lock is never held
// across the network fetch. A refresh failure inside the refresh window falls
// back to the cached still-valid token rather than failing the caller.
func (t *authTokenCache) get(ctx context.Context, fetch func(context.Context) (string, time.Time, error)) (string, error) {
	t.mu.Lock()
	token, exp := t.token, t.exp
	t.mu.Unlock()
	now := time.Now()
	if token != "" && now.Before(exp.Add(-authRefreshWindow)) {
		return token, nil
	}
	fresh, freshExp, err := fetch(ctx)
	if err != nil {
		if token != "" && now.Before(exp) {
			return token, nil // stale-if-error: still valid, use it
		}
		return "", err
	}
	t.set(fresh, freshExp)
	return fresh, nil
}

func (t *authTokenCache) set(token string, exp time.Time) {
	t.mu.Lock()
	t.token, t.exp = token, exp
	t.mu.Unlock()
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

// ambiguousOutcome reports whether the error leaves the RPC's server-side
// effect unknown (the request may have been applied despite the failure).
func ambiguousOutcome(err error) bool {
	s, ok := status.FromError(err)
	if !ok {
		return true // transport-level failure without a status: unknown effect
	}
	return retryableCodeSet[s.Code()]
}

// verify performs a fresh credential exchange, bypassing the token cache: the
// authorize flow must check the credentials as they are now.
func (c *apiClient) verify(ctx context.Context) error {
	token, exp, err := c.fetchAuthToken(ctx)
	if err != nil {
		return err
	}
	c.tokens.set(token, exp)
	return nil
}

// --- RPC wrappers ---

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
		return "", err // raw: Launch maps ambiguous outcomes to the contract's sentinels
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

// sandboxIDByName resolves a live sandbox by its unique in-app name. The name
// index is consistent with create (it enforces uniqueness), so this covers the
// window where a just-created sandbox has not reached the tag listing yet.
func (c *apiClient) sandboxIDByName(ctx context.Context, appName, name string) (string, bool, error) {
	ctx, err := c.authedCtx(ctx)
	if err != nil {
		return "", false, err
	}
	resp, err := c.pb.SandboxGetFromName(ctx, pb.SandboxGetFromNameRequest_builder{
		SandboxName:     name,
		AppName:         appName,
		EnvironmentName: c.environment,
	}.Build())
	if err != nil {
		if s, ok := status.FromError(err); ok && s.Code() == codes.NotFound {
			return "", false, nil
		}
		return "", false, rpcError("SandboxGetFromName", err)
	}
	return resp.GetSandboxId(), true, nil
}

// sandboxTags fetches a sandbox's tags. found=false means the sandbox is gone.
func (c *apiClient) sandboxTags(ctx context.Context, sandboxID string) (map[string]string, bool, error) {
	ctx, err := c.authedCtx(ctx)
	if err != nil {
		return nil, false, err
	}
	resp, err := c.pb.SandboxTagsGet(ctx, pb.SandboxTagsGetRequest_builder{SandboxId: sandboxID}.Build())
	if err != nil {
		if s, ok := status.FromError(err); ok && s.Code() == codes.NotFound {
			return nil, false, nil
		}
		return nil, false, rpcError("SandboxTagsGet", err)
	}
	return tagMap(resp.GetTags()), true, nil
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
