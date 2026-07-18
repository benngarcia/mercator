// Package modal launches Mercator runs as Modal Sandboxes
// (https://modal.com/docs/guide/sandboxes). Modal is serverless: offers are
// catalog entries from a configured GPU-type list, not live capacity, and
// every sandbox we create is capacity we own (provisionable ⇒ terminate).
// Unlike RunPod, Modal reports real exit codes, so Observe is authoritative
// on success versus failure.
package modal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	pb "github.com/modal-labs/modal-client/go/proto/modal_proto"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/domain"
)

var defaultGPUTypes = []string{"T4"}

const (
	defaultAppName     = "mercator"
	defaultTimeoutSecs = 24 * 60 * 60 // Modal kills a sandbox at its TTL; cap a lost run at one day.
)

// Ownership tags stamped onto every sandbox at create time. Tags are the
// queryable ownership channel (SandboxList filters on them); the same values
// are also injected as MERCATOR_* environment for the workload report path,
// matching the RunPod adapter's env scheme.
const (
	tagWorkspaceID    = "mercator_workspace_id"
	tagRunID          = "mercator_run_id"
	tagAttemptID      = "mercator_attempt_id"
	tagLaunchKey      = "mercator_launch_key"
	tagOwnershipToken = "mercator_ownership_token"
	tagRequestHash    = "mercator_request_hash"
	tagCleanupLocator = "mercator_cleanup_locator"
)

type Adapter struct {
	api         *apiClient
	gpuTypes    []string
	appName     string
	timeoutSecs uint32
	now         func() time.Time
}

// New builds the adapter from a connection's config and credential. The
// credential packs Modal's token pair into one secret as
// "<token_id>:<token_secret>" (e.g. "ak-...:as-...").
func New(secret string, config map[string]string) (*Adapter, error) {
	tokenID, tokenSecret, ok := strings.Cut(strings.TrimSpace(secret), ":")
	if !ok || tokenID == "" || tokenSecret == "" {
		return nil, fmt.Errorf("modal: credential must be \"<token_id>:<token_secret>\" (e.g. \"ak-xxx:as-xxx\")")
	}
	gpuTypes := defaultGPUTypes
	if raw := strings.TrimSpace(config["gpu_types"]); raw != "" {
		parts := strings.Split(raw, ",")
		gpuTypes = make([]string, 0, len(parts))
		for _, p := range parts {
			if t := strings.TrimSpace(p); t != "" {
				gpuTypes = append(gpuTypes, t)
			}
		}
	}
	timeoutSecs := uint32(defaultTimeoutSecs)
	if raw := strings.TrimSpace(config["timeout_seconds"]); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 || n > math.MaxUint32 {
			return nil, fmt.Errorf("modal: timeout_seconds must be a positive integer that fits in 32 bits, got %q", raw)
		}
		timeoutSecs = uint32(n)
	}
	appName := config["app_name"]
	if appName == "" {
		appName = defaultAppName
	}
	api, err := newAPIClient(tokenID, tokenSecret, config["environment"], config["server_url"])
	if err != nil {
		return nil, err
	}
	return &Adapter{
		api:         api,
		gpuTypes:    gpuTypes,
		appName:     appName,
		timeoutSecs: timeoutSecs,
		now:         time.Now,
	}, nil
}

// Verify exchanges the token pair for an auth token — the cheapest full
// credential check Modal offers. It does not launch anything.
func (a *Adapter) Verify(ctx context.Context) error { return a.api.verify(ctx) }

func (a *Adapter) ListOffers(_ context.Context, req adapter.OfferRequest) ([]domain.OfferSnapshot, error) {
	return buildOffers(a.gpuTypes, req.Resources, a.now().UTC()), nil
}

func (a *Adapter) Launch(ctx context.Context, req adapter.LaunchRequest) (adapter.LaunchReceipt, error) {
	command := launchCommand(req)
	if len(command) == 0 {
		// Modal sandboxes run their entrypoint_args, never the image CMD; an
		// empty command would silently idle until the TTL instead of running
		// the workload.
		return adapter.LaunchReceipt{}, fmt.Errorf("modal: workload must set an entrypoint or args; Modal sandboxes do not run the image's default command")
	}
	// Modal frees a sandbox name once the sandbox exits, so the create-time
	// AlreadyExists guard only covers live duplicates. A retried launch whose
	// sandbox already ran to completion must dedupe here or it would run the
	// workload a second time.
	if info, found, err := a.findOwned(ctx, req.LaunchKey, req.OwnershipToken); err != nil {
		return adapter.LaunchReceipt{}, err
	} else if found {
		phase, _ := phaseFromResult(info.result, info.startedAt > 0)
		return adapter.LaunchReceipt{
			ExternalID:     info.id,
			LaunchKey:      req.LaunchKey,
			OwnershipToken: req.OwnershipToken,
			CleanupLocator: req.CleanupLocator,
			Phase:          phase,
			AcceptedAt:     a.now().UTC(),
			Duplicate:      true,
		}, nil
	}
	builderVersion, err := a.api.imageBuilderVersion(ctx)
	if err != nil {
		return adapter.LaunchReceipt{}, err
	}
	appID, err := a.api.appID(ctx, a.appName)
	if err != nil {
		return adapter.LaunchReceipt{}, err
	}
	imageID, err := a.api.buildImage(ctx, appID, req.Image, builderVersion)
	if err != nil {
		return adapter.LaunchReceipt{}, err
	}
	secretID, err := a.api.createEnvSecret(ctx, a.launchEnv(req))
	if err != nil {
		return adapter.LaunchReceipt{}, err
	}
	gpuType, gpuCount := a.launchGPU(req)
	id, err := a.api.createSandbox(ctx, sandboxCreateInput{
		appID:       appID,
		name:        sandboxName(req.LaunchKey),
		imageID:     imageID,
		command:     command,
		secretIDs:   []string{secretID},
		timeoutSecs: a.timeoutSecs,
		gpuType:     gpuType,
		gpuCount:    gpuCount,
		milliCPU:    uint32(requestedCPUMillis(req.Resources)),
		memoryMB:    uint32(requestedMemoryBytes(req.Resources) / mib),
		diskMB:      uint32(requestedDiskBytes(req.Resources) / mib),
		tags:        a.ownershipTags(req),
	})
	if err == errAlreadyExists {
		return a.duplicateLaunchReceipt(ctx, req)
	}
	if err != nil {
		if ambiguousOutcome(err) {
			// The create may have been applied server-side; the orchestrator
			// must reconcile instead of concluding nothing external exists.
			return adapter.LaunchReceipt{}, fmt.Errorf("%w: %v", adapter.ErrLaunchIndeterminate, rpcError("SandboxCreate", err))
		}
		return adapter.LaunchReceipt{}, rpcError("SandboxCreate", err)
	}
	return adapter.LaunchReceipt{
		ExternalID:     id,
		LaunchKey:      req.LaunchKey,
		OwnershipToken: req.OwnershipToken,
		CleanupLocator: req.CleanupLocator,
		Phase:          adapter.ExternalPhaseQueued,
		AcceptedAt:     a.now().UTC(),
	}, nil
}

// duplicateLaunchReceipt resolves a name collision on create: a concurrent
// attempt with this launch key already created the sandbox. The unique
// sandbox name is the idempotency guard; the ownership token distinguishes a
// retried launch (duplicate) from a foreign squatter (conflict). A collision
// we cannot resolve back to a sandbox (listing/tag lag) is indeterminate, not
// a conflict: a false conflict would close the run and orphan a paid sandbox.
func (a *Adapter) duplicateLaunchReceipt(ctx context.Context, req adapter.LaunchRequest) (adapter.LaunchReceipt, error) {
	info, found, err := a.findOwned(ctx, req.LaunchKey, req.OwnershipToken)
	if err != nil {
		return adapter.LaunchReceipt{}, err
	}
	if !found {
		return adapter.LaunchReceipt{}, fmt.Errorf("%w: sandbox name %s exists but is not yet resolvable", adapter.ErrLaunchIndeterminate, sandboxName(req.LaunchKey))
	}
	phase, _ := phaseFromResult(info.result, info.startedAt > 0)
	return adapter.LaunchReceipt{
		ExternalID:     info.id,
		LaunchKey:      req.LaunchKey,
		OwnershipToken: req.OwnershipToken,
		CleanupLocator: req.CleanupLocator,
		Phase:          phase,
		AcceptedAt:     a.now().UTC(),
		Duplicate:      true,
	}, nil
}

func (a *Adapter) Observe(ctx context.Context, req adapter.ObserveRequest) (adapter.ExternalObservation, error) {
	info, found, err := a.findOwned(ctx, req.LaunchKey, req.OwnershipToken)
	if err != nil {
		return adapter.ExternalObservation{}, err
	}
	if !found {
		return adapter.ExternalObservation{LaunchKey: req.LaunchKey, Phase: adapter.ExternalPhaseReleased, ObservedAt: a.now().UTC()}, nil
	}
	// SandboxWait(timeout=0) is the authoritative poll; the listing's embedded
	// result may lag it.
	result, err := a.api.sandboxResult(ctx, info.id)
	if err != nil {
		return adapter.ExternalObservation{}, err
	}
	phase, exitCode := phaseFromResult(result, info.startedAt > 0)
	return adapter.ExternalObservation{
		ExternalID: info.id,
		LaunchKey:  req.LaunchKey,
		Phase:      phase,
		ObservedAt: a.now().UTC(),
		ExitCode:   exitCode,
	}, nil
}

func (a *Adapter) Cancel(ctx context.Context, req adapter.CancelRequest) (adapter.CancelReceipt, error) {
	// Best-effort: terminating a not-yet-started sandbox is the same resolve+terminate path.
	if _, err := a.deleteOwned(ctx, req.LaunchKey, ""); err != nil {
		return adapter.CancelReceipt{}, err
	}
	return adapter.CancelReceipt{Cancelled: true}, nil
}

func (a *Adapter) Release(ctx context.Context, req adapter.ReleaseRequest) (adapter.ReleaseReceipt, error) {
	released, err := a.deleteOwned(ctx, req.LaunchKey, req.OwnershipToken)
	if err != nil {
		return adapter.ReleaseReceipt{}, err
	}
	return adapter.ReleaseReceipt{Released: released}, nil
}

func (a *Adapter) Terminate(ctx context.Context, req adapter.TerminateRequest) (adapter.TerminateReceipt, error) {
	terminated, err := a.deleteOwned(ctx, req.LaunchKey, req.OwnershipToken)
	if err != nil {
		return adapter.TerminateReceipt{}, err
	}
	return adapter.TerminateReceipt{Terminated: terminated}, nil
}

// ListOwned reports live (queued or running) sandboxes carrying Mercator
// ownership tags. Exited sandboxes are excluded deliberately: they hold no
// billable capacity, cannot be reclaimed, and Modal keeps their records
// indefinitely — including them would grow the janitor's working set forever.
func (a *Adapter) ListOwned(ctx context.Context, req adapter.OwnershipQuery) ([]adapter.OwnedExternalObject, error) {
	filter := map[string]string{}
	if req.WorkspaceID != "" {
		filter[tagWorkspaceID] = req.WorkspaceID
	}
	infos, err := a.api.listSandboxes(ctx, filter, false)
	if err != nil {
		return nil, err
	}
	owned := make([]adapter.OwnedExternalObject, 0, len(infos))
	for _, info := range infos {
		tags, found, err := a.tagsOf(ctx, info)
		if err != nil {
			return nil, err
		}
		if !found || tags[tagLaunchKey] == "" {
			continue // vanished since listing, or not a Mercator sandbox
		}
		phase, _ := phaseFromResult(info.result, info.startedAt > 0)
		owned = append(owned, adapter.OwnedExternalObject{
			ExternalID:     info.id,
			WorkspaceID:    tags[tagWorkspaceID],
			RunID:          tags[tagRunID],
			AttemptID:      tags[tagAttemptID],
			OwnershipToken: tags[tagOwnershipToken],
			LaunchKey:      tags[tagLaunchKey],
			CleanupLocator: tags[tagCleanupLocator],
			RequestHash:    tags[tagRequestHash],
			Phase:          phase,
		})
	}
	return owned, nil
}

// --- helpers ---

// sandboxName derives the sandbox's unique in-app name from the launch key.
// Modal object names must be shorter than 64 characters and real launch keys
// (uuidv7-derived) exceed that, so the name carries a hash; the full launch
// key rides on the mercator_launch_key tag.
func sandboxName(launchKey string) string {
	sum := sha256.Sum256([]byte(launchKey))
	return "mercator-" + hex.EncodeToString(sum[:12])
}

func launchCommand(req adapter.LaunchRequest) []string {
	var command []string
	if req.Entrypoint != nil {
		command = append(command, (*req.Entrypoint)...)
	}
	return append(command, req.Args...)
}

// launchGPU maps the selected offer to Modal's GPU request. An empty native
// ref (no offer context) falls back to the first configured GPU type when the
// workload asks for accelerators.
func (a *Adapter) launchGPU(req adapter.LaunchRequest) (string, uint32) {
	ref := req.SelectedOfferNativeRef
	if ref == "" && wantsAccelerator(req.Resources) && len(a.gpuTypes) > 0 {
		ref = a.gpuTypes[0]
	}
	if ref == "" || isCPURef(ref) {
		return "", 0
	}
	return ref, uint32(requestedGPUCount(req.Resources))
}

func (a *Adapter) ownershipTags(req adapter.LaunchRequest) map[string]string {
	return map[string]string{
		tagWorkspaceID:    req.WorkspaceID,
		tagRunID:          req.RunID,
		tagAttemptID:      req.AttemptID,
		tagLaunchKey:      req.LaunchKey,
		tagOwnershipToken: req.OwnershipToken,
		tagRequestHash:    req.RequestHash,
		tagCleanupLocator: req.CleanupLocator,
	}
}

func (a *Adapter) launchEnv(req adapter.LaunchRequest) map[string]string {
	env := map[string]string{}
	for _, b := range req.Environment {
		if b.Value != nil {
			env[b.Name] = *b.Value
		}
	}
	// Ownership/identity keys are authoritative here; they intentionally match the orchestrator-injected reporting vars of the same name (identical values).
	env["MERCATOR_WORKSPACE_ID"] = req.WorkspaceID
	env["MERCATOR_RUN_ID"] = req.RunID
	env["MERCATOR_ATTEMPT_ID"] = req.AttemptID
	env["MERCATOR_LAUNCH_KEY"] = req.LaunchKey
	env["MERCATOR_OWNERSHIP_TOKEN"] = req.OwnershipToken
	env["MERCATOR_REQUEST_HASH"] = req.RequestHash
	env["MERCATOR_CLEANUP_LOCATOR"] = req.CleanupLocator
	return env
}

// tagsOf returns the sandbox's tags, fetching them individually when the
// listing omitted them (the list response's tag field is newer than some
// server deployments). found=false means the sandbox vanished since listing.
func (a *Adapter) tagsOf(ctx context.Context, info sandboxInfo) (map[string]string, bool, error) {
	if len(info.tags) > 0 {
		return info.tags, true, nil
	}
	return a.api.sandboxTags(ctx, info.id)
}

// findOwned locates our sandbox by launch-key tag and verifies ownership. The
// boolean is false when no such sandbox exists (treated as released by
// callers). Finished sandboxes are included: Modal keeps their records, and
// Observe needs them to read authoritative exit codes. When the tag listing
// misses (it can lag a just-created sandbox), the consistent by-name index is
// consulted before concluding the sandbox is gone — a false "gone" here makes
// Observe report Released for a live, billing sandbox.
func (a *Adapter) findOwned(ctx context.Context, launchKey, ownershipToken string) (sandboxInfo, bool, error) {
	infos, err := a.api.listSandboxes(ctx, map[string]string{tagLaunchKey: launchKey}, true)
	if err != nil {
		return sandboxInfo{}, false, err
	}
	for _, info := range infos {
		tags, found, err := a.tagsOf(ctx, info)
		if err != nil {
			return sandboxInfo{}, false, err
		}
		if !found || tags[tagLaunchKey] != launchKey {
			continue
		}
		if err := checkOwnershipToken(tags, ownershipToken); err != nil {
			return sandboxInfo{}, false, err
		}
		info.tags = tags
		return info, true, nil
	}
	id, found, err := a.api.sandboxIDByName(ctx, a.appName, sandboxName(launchKey))
	if err != nil || !found {
		return sandboxInfo{}, false, err
	}
	tags, found, err := a.api.sandboxTags(ctx, id)
	if err != nil || !found {
		return sandboxInfo{}, false, err
	}
	if err := checkOwnershipToken(tags, ownershipToken); err != nil {
		return sandboxInfo{}, false, err
	}
	// The by-name index only resolves live sandboxes, so a hit here is a
	// just-created sandbox the tag listing has not caught up to.
	return sandboxInfo{id: id, name: sandboxName(launchKey), tags: tags}, true, nil
}

// checkOwnershipToken conflicts only on a POSITIVE token mismatch (token
// present and different), never on a missing/empty token — a false conflict
// would fail cleanup and orphan a paid sandbox, the worse failure.
func checkOwnershipToken(tags map[string]string, ownershipToken string) error {
	if ownershipToken == "" {
		return nil
	}
	if tok, ok := tags[tagOwnershipToken]; ok && tok != ownershipToken {
		return adapter.ErrIdempotencyConflict
	}
	return nil
}

func (a *Adapter) deleteOwned(ctx context.Context, launchKey, ownershipToken string) (bool, error) {
	info, found, err := a.findOwned(ctx, launchKey, ownershipToken)
	if err != nil {
		return false, err
	}
	if !found {
		return true, nil // already gone — idempotent
	}
	if err := a.api.terminateSandbox(ctx, info.id); err != nil {
		return false, err
	}
	return true, nil
}

// phaseFromResult maps Modal's task result onto the adapter phase model. A
// nil/unspecified result means the sandbox has not exited. Exit codes are only
// reported for exited phases, following the SDK's subprocess convention
// (timeout ⇒ 124, external kill ⇒ 137).
func phaseFromResult(result *pb.GenericResult, started bool) (adapter.ExternalPhase, *int) {
	switch result.GetStatus() {
	case pb.GenericResult_GENERIC_STATUS_SUCCESS:
		return adapter.ExternalPhaseSucceeded, intPtr(0)
	case pb.GenericResult_GENERIC_STATUS_FAILURE:
		return adapter.ExternalPhaseFailed, intPtr(int(result.GetExitcode()))
	case pb.GenericResult_GENERIC_STATUS_TIMEOUT:
		return adapter.ExternalPhaseFailed, intPtr(124)
	case pb.GenericResult_GENERIC_STATUS_TERMINATED:
		// Killed by an external signal: our own cancel/terminate (in which case
		// this observation is no longer consulted) or a provider-side kill such
		// as OOM — a failure, not a cancellation Mercator initiated.
		return adapter.ExternalPhaseFailed, intPtr(137)
	case pb.GenericResult_GENERIC_STATUS_UNSPECIFIED:
		if started {
			return adapter.ExternalPhaseRunning, nil
		}
		return adapter.ExternalPhaseQueued, nil
	default: // INIT_FAILURE, INTERNAL_FAILURE, IDLE_TIMEOUT
		return adapter.ExternalPhaseFailed, nil
	}
}

func intPtr(v int) *int { return &v }

var _ adapter.Adapter = (*Adapter)(nil)
