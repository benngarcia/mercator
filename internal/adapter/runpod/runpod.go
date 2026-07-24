package runpod

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/capability"
	"github.com/benngarcia/mercator/internal/domain"
)

var defaultAllowlist = []string{"NVIDIA RTX A2000", "NVIDIA RTX A4000"}

type Adapter struct {
	rest           *restClient
	graphql        *graphqlClient
	allowlist      []string
	allowCommunity bool
	diskGB         int
	registryAuthID string
	now            func() time.Time
}

func New(secret string, config map[string]string) (*Adapter, error) {
	allow := defaultAllowlist
	if raw := strings.TrimSpace(config["gpu_types"]); raw != "" {
		parts := strings.Split(raw, ",")
		allow = make([]string, 0, len(parts))
		for _, p := range parts {
			if t := strings.TrimSpace(p); t != "" {
				allow = append(allow, t)
			}
		}
	}
	if _, ok := config["cloud_type"]; ok {
		return nil, fmt.Errorf("runpod: config key \"cloud_type\" was removed; the adapter is secure-cloud only unless \"allow_community_cloud\" is \"true\"")
	}
	allowCommunity := false
	if raw := strings.TrimSpace(config["allow_community_cloud"]); raw != "" {
		v, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, fmt.Errorf("runpod: allow_community_cloud must be \"true\" or \"false\", got %q", raw)
		}
		allowCommunity = v
	}
	disk := 20
	if d := strings.TrimSpace(config["container_disk_gb"]); d != "" {
		n, err := strconv.Atoi(d)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("runpod: container_disk_gb must be a positive integer, got %q", d)
		}
		disk = n
	}
	providerClient := &http.Client{Timeout: time.Minute}
	return &Adapter{
		rest:           newRESTClient(secret, config["rest_base_url"], providerClient),
		graphql:        newGraphQLClient(secret, config["graphql_base_url"], providerClient),
		allowlist:      allow,
		allowCommunity: allowCommunity,
		diskGB:         disk,
		registryAuthID: strings.TrimSpace(config["container_registry_auth_id"]),
		now:            time.Now,
	}, nil
}

func (a *Adapter) Verify(ctx context.Context) error { return a.rest.ping(ctx) }

func (a *Adapter) ListOffers(ctx context.Context, req adapter.OfferRequest) ([]domain.OfferSnapshot, error) {
	gpuCount := requestedGPUCount(req.Resources)
	gpus, err := a.graphql.gpuTypes(ctx, gpuCount)
	if err != nil {
		return nil, err
	}
	diskGB := requestedDiskGB(req.Resources, a.diskGB)
	return buildOffers(gpus, a.allowlist, gpuCount, diskGB, a.allowCommunity, a.now().UTC()), nil
}

func (a *Adapter) Launch(ctx context.Context, req adapter.LaunchRequest) (adapter.LaunchReceipt, error) {
	gpuID, cloud, err := a.launchCloud(req.SelectedOfferNativeRef)
	if err != nil {
		return adapter.LaunchReceipt{}, err
	}
	name := podName(req.LaunchKey)
	in := podCreateInput{
		Name:                    name,
		ImageName:               req.Image,
		GPUTypeIDs:              a.gpuTypeIDs(gpuID),
		GPUCount:                requestedGPUCount(req.Resources),
		ContainerDiskGB:         requestedDiskGB(req.Resources, a.diskGB),
		ContainerRegistryAuthID: a.registryAuthID,
		CloudType:               cloud,
		Env:                     a.launchEnv(req),
		DockerStartCmd:          append([]string(nil), req.Args...),
	}
	if req.Entrypoint != nil {
		in.DockerEntrypoint = append([]string(nil), (*req.Entrypoint)...)
	}
	p, err := a.rest.createPod(ctx, in)
	if err != nil {
		return adapter.LaunchReceipt{}, err
	}
	if cloud == cloudSecure {
		if err := a.assertSecurePlacement(ctx, p); err != nil {
			return adapter.LaunchReceipt{}, err
		}
	}
	return adapter.LaunchReceipt{
		ExternalID:     p.ID,
		LaunchKey:      req.LaunchKey,
		OwnershipToken: req.OwnershipToken,
		CleanupLocator: req.CleanupLocator,
		Phase:          phaseFromPod(p),
		AcceptedAt:     a.now().UTC(),
	}, nil
}

func requestedGPUCount(resources domain.ResourceRequirements) int {
	for _, accelerator := range resources.Accelerators {
		if accelerator.Count > 0 {
			return accelerator.Count
		}
	}
	return 1
}

func requestedDiskGB(resources domain.ResourceRequirements, defaultGB int) int {
	if resources.EphemeralDisk.MinBytes <= 0 {
		return defaultGB
	}
	return int((resources.EphemeralDisk.MinBytes + gib - 1) / gib)
}

func (a *Adapter) Observe(ctx context.Context, req adapter.ObserveRequest) (adapter.ExternalObservation, error) {
	p, found, err := a.findOwned(ctx, req.LaunchKey, req.OwnershipToken)
	if err != nil {
		return adapter.ExternalObservation{}, err
	}
	if !found {
		return adapter.ExternalObservation{LaunchKey: req.LaunchKey, Phase: adapter.ExternalPhaseReleased, ObservedAt: a.now().UTC()}, nil
	}
	return adapter.ExternalObservation{ExternalID: p.ID, LaunchKey: req.LaunchKey, Phase: phaseFromPod(p), ObservedAt: a.now().UTC()}, nil
}

func (a *Adapter) Release(ctx context.Context, req adapter.ReleaseRequest) (adapter.ReleaseReceipt, error) {
	deleted, err := a.deleteOwned(ctx, req.LaunchKey, req.OwnershipToken)
	if err != nil {
		return adapter.ReleaseReceipt{}, err
	}
	return adapter.ReleaseReceipt{Released: deleted}, nil
}

func (a *Adapter) Terminate(ctx context.Context, req adapter.TerminateRequest) (adapter.TerminateReceipt, error) {
	deleted, err := a.deleteOwned(ctx, req.LaunchKey, req.OwnershipToken)
	if err != nil {
		return adapter.TerminateReceipt{}, err
	}
	return adapter.TerminateReceipt{Terminated: deleted}, nil
}

func (a *Adapter) ListOwned(ctx context.Context, req adapter.OwnershipQuery) ([]adapter.OwnedExternalObject, error) {
	pods, err := a.rest.listPodsByName(ctx, "mercator-")
	if err != nil {
		return nil, err
	}
	owned := make([]adapter.OwnedExternalObject, 0, len(pods))
	for _, p := range pods {
		if req.WorkspaceID != "" && p.Env["MERCATOR_WORKSPACE_ID"] != req.WorkspaceID {
			continue
		}
		owned = append(owned, adapter.OwnedExternalObject{
			ExternalID:     p.ID,
			WorkspaceID:    p.Env["MERCATOR_WORKSPACE_ID"],
			RunID:          p.Env["MERCATOR_RUN_ID"],
			AttemptID:      p.Env["MERCATOR_ATTEMPT_ID"],
			OwnershipToken: p.Env["MERCATOR_OWNERSHIP_TOKEN"],
			LaunchKey:      p.Env["MERCATOR_LAUNCH_KEY"],
			CleanupLocator: p.Env["MERCATOR_CLEANUP_LOCATOR"],
			RequestHash:    p.Env["MERCATOR_REQUEST_HASH"],
			Phase:          phaseFromPod(p),
		})
	}
	return owned, nil
}

// --- helpers ---

func podName(launchKey string) string { return "mercator-" + launchKey }

// launchCloud resolves the selected offer to the GPU type and cloud the pod
// must be created on, and enforces the connection's cloud posture: community
// capacity is refused unless allow_community_cloud opted it in. Enforcing here
// (not just in offer filtering) means a stale offer or misconfigured caller
// cannot leak a workload onto community hardware.
func (a *Adapter) launchCloud(nativeRef string) (gpuID, cloud string, err error) {
	gpuID, cloud = splitNativeRef(nativeRef)
	if cloud != cloudSecure && cloud != cloudCommunity {
		return "", "", fmt.Errorf("runpod: offer %q names unknown cloud %q (want %s or %s)", nativeRef, cloud, cloudSecure, cloudCommunity)
	}
	if cloud == cloudCommunity && !a.allowCommunity {
		return "", "", fmt.Errorf("runpod: refusing to launch on community cloud: offer %q targets community capacity but this connection is secure-cloud only (set connection config allow_community_cloud=true to opt in)", nativeRef)
	}
	return gpuID, cloud, nil
}

// assertSecurePlacement guards against the provider scheduling a pod onto
// community hardware despite the explicit SECURE request. Only an explicit
// machine.secureCloud=false counts as a violation — the machine facts may be
// absent while the pod awaits placement, and a false positive here would
// destroy a legitimate secure pod. On violation the pod is destroyed before
// erroring so the workload never runs on community hardware.
func (a *Adapter) assertSecurePlacement(ctx context.Context, p pod) error {
	if p.Machine == nil || p.Machine.SecureCloud == nil || *p.Machine.SecureCloud {
		return nil
	}
	if err := a.rest.deletePod(ctx, p.ID); err != nil {
		return fmt.Errorf("runpod: pod %s landed on community cloud despite SECURE request and could not be destroyed (delete it manually): %w", p.ID, err)
	}
	return fmt.Errorf("runpod: pod %s landed on community cloud despite SECURE request; destroyed it and refusing the launch", p.ID)
}

func (a *Adapter) gpuTypeIDs(selected string) []string {
	ids := []string{}
	if selected != "" {
		ids = append(ids, selected)
	}
	for _, g := range a.allowlist {
		if g != selected {
			ids = append(ids, g)
		}
	}
	return ids
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

// findOwned locates our pod by name and verifies ownership. The boolean is
// false when no such pod exists (treated as released by callers).
func (a *Adapter) findOwned(ctx context.Context, launchKey, ownershipToken string) (pod, bool, error) {
	name := podName(launchKey)
	pods, err := a.rest.listPodsByName(ctx, name)
	if err != nil {
		return pod{}, false, err
	}
	for _, p := range pods {
		if p.Name != name {
			continue
		}
		// Ownership: the exact unique launch-key pod name is the authoritative
		// ownership signal; the token guards against the (near-impossible) reuse of
		// that name. We conflict only on a POSITIVE token mismatch (token present and
		// different), never on a missing/empty token — a false conflict here would
		// fail cleanup and orphan a paid pod, the worse failure.
		if ownershipToken != "" && p.Env != nil {
			if tok, ok := p.Env["MERCATOR_OWNERSHIP_TOKEN"]; ok && tok != ownershipToken {
				return pod{}, false, adapter.ErrIdempotencyConflict
			}
		}
		return p, true, nil
	}
	return pod{}, false, nil
}

func (a *Adapter) deleteOwned(ctx context.Context, launchKey, ownershipToken string) (bool, error) {
	p, found, err := a.findOwned(ctx, launchKey, ownershipToken)
	if err != nil {
		return false, err
	}
	if !found {
		return true, nil // already gone — idempotent
	}
	if err := a.rest.deletePod(ctx, p.ID); err != nil {
		return false, err
	}
	return true, nil
}

func phaseFromPod(p pod) adapter.ExternalPhase {
	switch strings.ToUpper(p.DesiredStatus) {
	case "RUNNING":
		if strings.TrimSpace(p.PublicIP) != "" {
			return adapter.ExternalPhaseRunning
		}
		return adapter.ExternalPhaseQueued
	case "EXITED":
		// Pod stopped, but RunPod never tells us success vs failure. Map to
		// failed (pessimistic); the workload's exit report is authoritative and
		// overrides this via the orchestrator's report finalize path.
		return adapter.ExternalPhaseFailed
	case "TERMINATED":
		return adapter.ExternalPhaseReleased
	default:
		return adapter.ExternalPhaseQueued
	}
}

var _ capability.EphemeralExecutor = (*Adapter)(nil)

// EphemeralSupport states what a RunPod connection can do today. Each launch
// creates a pod for one workload and terminates it afterwards, so RunPod stays
// in the ephemeral lane until a node agent is proven to bootstrap on a pod.
func (a *Adapter) EphemeralSupport() capability.EphemeralSupport {
	return capability.EphemeralSupport{
		ReusableBetweenRuns: false,
		// A fresh pod reports nothing about what it already holds, so its
		// locality is unknown rather than cold.
		ObservableLocality: false,
		CancelQueued:       false,
		ProviderTTL:        false,
		IdempotentLaunch:   "launch_key",
		ListOwned:          true,
		ExactPricing:       true,
	}
}
