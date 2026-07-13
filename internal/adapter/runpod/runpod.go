package runpod

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/domain"
)

var defaultAllowlist = []string{"NVIDIA RTX A2000", "NVIDIA RTX A4000"}

type Adapter struct {
	rest      *restClient
	graphql   *graphqlClient
	allowlist []string
	cloudType string
	diskGB    int
	now       func() time.Time
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
	cloud := config["cloud_type"]
	if cloud == "" {
		cloud = "COMMUNITY"
	}
	disk := 20
	if d := strings.TrimSpace(config["container_disk_gb"]); d != "" {
		if n, err := strconv.Atoi(d); err == nil && n > 0 {
			disk = n
		}
	}
	return &Adapter{
		rest:      newRESTClient(secret, config["rest_base_url"], http.DefaultClient),
		graphql:   newGraphQLClient(secret, config["graphql_base_url"], http.DefaultClient),
		allowlist: allow,
		cloudType: cloud,
		diskGB:    disk,
		now:       time.Now,
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
	return buildOffers(gpus, a.allowlist, gpuCount, diskGB, a.now().UTC()), nil
}

func (a *Adapter) Launch(ctx context.Context, req adapter.LaunchRequest) (adapter.LaunchReceipt, error) {
	name := podName(req.LaunchKey)
	in := podCreateInput{
		Name:            name,
		ImageName:       req.Image,
		GPUTypeIDs:      a.gpuTypeIDs(req.SelectedOfferNativeRef),
		GPUCount:        requestedGPUCount(req.Resources),
		ContainerDiskGB: requestedDiskGB(req.Resources, a.diskGB),
		CloudType:       a.cloudType,
		Env:             a.launchEnv(req),
		DockerStartCmd:  append([]string(nil), req.Args...),
	}
	if req.Entrypoint != nil {
		in.DockerEntrypoint = append([]string(nil), (*req.Entrypoint)...)
	}
	p, err := a.rest.createPod(ctx, in)
	if err != nil {
		return adapter.LaunchReceipt{}, err
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

func (a *Adapter) Cancel(ctx context.Context, req adapter.CancelRequest) (adapter.CancelReceipt, error) {
	// Best-effort: deleting a not-yet-started pod is the same resolve+delete path.
	if _, err := a.deleteOwned(ctx, req.LaunchKey, ""); err != nil {
		return adapter.CancelReceipt{}, err
	}
	return adapter.CancelReceipt{Cancelled: true}, nil
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

var _ adapter.Adapter = (*Adapter)(nil)
