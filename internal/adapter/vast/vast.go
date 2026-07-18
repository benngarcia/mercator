// Package vast launches Mercator runs on Vast.ai, a marketplace of GPU hosts.
// The adapter is SECURE-TIER ONLY: it advertises and rents exclusively
// certified-datacenter machines (Vast's "Secure Cloud": datacenter offers on
// verified machines). Community/peer-host capacity is excluded with no
// configuration to relax that — the tier is enforced in both ListOffers and
// Launch. Vast's API never exposes a container exit code, so like RunPod the
// workload's self-report is the authoritative run outcome and Observe maps a
// stopped container pessimistically to failed.
package vast

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/domain"
)

const defaultOfferLimit = 20

type Adapter struct {
	api        *apiClient
	gpuNames   []string
	offerLimit int
	diskGB     int
	now        func() time.Time
}

// New builds the adapter from a connection's config and credential (a single
// Vast API key).
func New(secret string, config map[string]string) (*Adapter, error) {
	var gpuNames []string
	if raw := strings.TrimSpace(config["gpu_names"]); raw != "" {
		for _, p := range strings.Split(raw, ",") {
			if t := strings.TrimSpace(p); t != "" {
				// Vast stores GPU names with spaces ("RTX 4090"); its own CLI
				// accepts underscores and rewrites them, so we do the same.
				gpuNames = append(gpuNames, strings.ReplaceAll(t, "_", " "))
			}
		}
	}
	disk := 20
	if d := strings.TrimSpace(config["container_disk_gb"]); d != "" {
		if n, err := strconv.Atoi(d); err == nil && n > 0 {
			disk = n
		}
	}
	limit := defaultOfferLimit
	if l := strings.TrimSpace(config["offer_limit"]); l != "" {
		n, err := strconv.Atoi(l)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("vast: offer_limit must be a positive integer, got %q", l)
		}
		limit = n
	}
	return &Adapter{
		api:        newAPIClient(secret, config["base_url"], http.DefaultClient),
		gpuNames:   gpuNames,
		offerLimit: limit,
		diskGB:     disk,
		now:        time.Now,
	}, nil
}

// Verify validates the API key via the cheapest authenticated read
// (GET /users/current/). It does not launch anything.
func (a *Adapter) Verify(ctx context.Context) error { return a.api.ping(ctx) }

func (a *Adapter) ListOffers(ctx context.Context, req adapter.OfferRequest) ([]domain.OfferSnapshot, error) {
	gpuCount := requestedGPUCount(req.Resources)
	diskGB := requestedDiskGB(req.Resources, a.diskGB)
	offers, err := a.api.searchOffers(ctx, secureOfferQuery(a.gpuNames, gpuCount, diskGB, a.offerLimit))
	if err != nil {
		return nil, err
	}
	return buildOffers(offers, gpuCount, diskGB, a.now().UTC()), nil
}

func (a *Adapter) Launch(ctx context.Context, req adapter.LaunchRequest) (adapter.LaunchReceipt, error) {
	if req.Entrypoint != nil && len(*req.Entrypoint) > 0 {
		// Vast's create API has no exec-form entrypoint override (only a
		// shell-parsed onstart string); silently re-joining argv would corrupt
		// arguments. Bake the entrypoint into the image or use args.
		return adapter.LaunchReceipt{}, fmt.Errorf("vast: entrypoint override is not supported; bake the entrypoint into the image and pass args")
	}
	// Find-before-create: the label is the deterministic launch-key marker, so
	// a retried launch resolves to the instance the first attempt created.
	if existing, found, err := a.findOwned(ctx, req.LaunchKey, req.OwnershipToken); err != nil {
		return adapter.LaunchReceipt{}, err
	} else if found {
		return a.receipt(req, existing, true), nil
	}
	askID, err := a.secureAskID(ctx, req.SelectedOfferNativeRef)
	if err != nil {
		return adapter.LaunchReceipt{}, err
	}
	contractID, err := a.api.createInstance(ctx, askID, createInstanceInput{
		ClientID: "me",
		Image:    req.Image,
		Env:      a.launchEnv(req),
		Disk:     float64(requestedDiskGB(req.Resources, a.diskGB)),
		Label:    instanceLabel(req.LaunchKey),
		Runtype:  "args",
		Args:     append([]string(nil), req.Args...),
		// Fail loudly if the ask can't start now instead of parking a stopped
		// (but billed-for-storage) instance.
		TargetState:   "running",
		CancelUnavail: true,
	})
	if err != nil {
		return adapter.LaunchReceipt{}, err
	}
	return a.reconcileCreated(ctx, req, contractID)
}

// reconcileCreated resolves the just-created contract against the label
// listing: it enforces the secure tier on the realized machine and collapses
// concurrent duplicate creates deterministically (lowest contract id wins,
// the loser destroys its own instance).
func (a *Adapter) reconcileCreated(ctx context.Context, req adapter.LaunchRequest, contractID int64) (adapter.LaunchReceipt, error) {
	rows, err := a.api.listInstances(ctx, instanceLabel(req.LaunchKey))
	if err != nil {
		return adapter.LaunchReceipt{}, err
	}
	created := instance{ID: contractID}
	winner := instance{ID: contractID}
	for _, row := range rows {
		if row.Label != instanceLabel(req.LaunchKey) || conflictingToken(row, req.OwnershipToken) {
			continue
		}
		if row.ID == contractID {
			created = row
		}
		if row.ID < winner.ID {
			winner = row
		}
	}
	// Belt-and-braces tier check on the realized machine: the ask was verified
	// pre-create, but only an explicit non-verified status here is a violation
	// (a listing that lags the create must not destroy a legitimate instance).
	if created.Verification != "" && created.Verification != secureVerification {
		if err := a.api.destroyInstance(ctx, contractID); err != nil {
			return adapter.LaunchReceipt{}, fmt.Errorf("vast: instance %d landed on a %s machine despite the secure-tier ask check and could not be destroyed (destroy it manually): %w", contractID, created.Verification, err)
		}
		return adapter.LaunchReceipt{}, fmt.Errorf("vast: instance %d landed on a %s machine despite the secure-tier ask check; destroyed it and refusing the launch", contractID, created.Verification)
	}
	if winner.ID != contractID {
		// A concurrent launch with the same key won the race; destroying our
		// contract (not theirs) keeps exactly one instance without deadlock.
		if err := a.api.destroyInstance(ctx, contractID); err != nil {
			return adapter.LaunchReceipt{}, fmt.Errorf("vast: destroy duplicate instance %d: %w", contractID, err)
		}
		return a.receipt(req, winner, true), nil
	}
	return a.receipt(req, created, false), nil
}

func (a *Adapter) receipt(req adapter.LaunchRequest, i instance, duplicate bool) adapter.LaunchReceipt {
	return adapter.LaunchReceipt{
		ExternalID:     strconv.FormatInt(i.ID, 10),
		LaunchKey:      req.LaunchKey,
		OwnershipToken: req.OwnershipToken,
		CleanupLocator: req.CleanupLocator,
		Phase:          phaseFromInstance(i),
		AcceptedAt:     a.now().UTC(),
		Duplicate:      duplicate,
	}
}

// secureAskID validates the selected offer against the live marketplace under
// the SAME secure-tier filters ListOffers uses. Enforcing here (not just in
// offer filtering) means a stale offer or misconfigured caller cannot leak a
// workload onto community/peer hardware: a non-secure ask id simply does not
// resolve, and the launch is refused before any money moves.
func (a *Adapter) secureAskID(ctx context.Context, nativeRef string) (int64, error) {
	askID, err := strconv.ParseInt(strings.TrimSpace(nativeRef), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("vast: selected offer native ref %q is not an ask id", nativeRef)
	}
	offers, err := a.api.searchOffers(ctx, secureAskQuery(askID))
	if err != nil {
		return 0, err
	}
	for _, o := range offers {
		if o.ID == askID && o.Verification == secureVerification {
			return askID, nil
		}
	}
	return 0, fmt.Errorf("vast: refusing launch: ask %d is not (or no longer) a secure-tier offer (verified machine in a certified datacenter); this adapter never launches on community capacity", askID)
}

func (a *Adapter) Observe(ctx context.Context, req adapter.ObserveRequest) (adapter.ExternalObservation, error) {
	i, found, err := a.findOwned(ctx, req.LaunchKey, req.OwnershipToken)
	if err != nil {
		return adapter.ExternalObservation{}, err
	}
	if !found {
		return adapter.ExternalObservation{LaunchKey: req.LaunchKey, Phase: adapter.ExternalPhaseReleased, ObservedAt: a.now().UTC()}, nil
	}
	return adapter.ExternalObservation{
		ExternalID: strconv.FormatInt(i.ID, 10),
		LaunchKey:  req.LaunchKey,
		Phase:      phaseFromInstance(i),
		ObservedAt: a.now().UTC(),
	}, nil
}

func (a *Adapter) Cancel(ctx context.Context, req adapter.CancelRequest) (adapter.CancelReceipt, error) {
	// Best-effort: destroying a not-yet-started instance is the same
	// resolve+destroy path.
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
	rows, err := a.api.listInstances(ctx, "")
	if err != nil {
		return nil, err
	}
	owned := make([]adapter.OwnedExternalObject, 0, len(rows))
	for _, i := range rows {
		if !strings.HasPrefix(i.Label, labelPrefix) {
			continue
		}
		env := i.env()
		if req.WorkspaceID != "" && env["MERCATOR_WORKSPACE_ID"] != req.WorkspaceID {
			continue
		}
		owned = append(owned, adapter.OwnedExternalObject{
			ExternalID:     strconv.FormatInt(i.ID, 10),
			WorkspaceID:    env["MERCATOR_WORKSPACE_ID"],
			RunID:          env["MERCATOR_RUN_ID"],
			AttemptID:      env["MERCATOR_ATTEMPT_ID"],
			OwnershipToken: env["MERCATOR_OWNERSHIP_TOKEN"],
			LaunchKey:      env["MERCATOR_LAUNCH_KEY"],
			CleanupLocator: env["MERCATOR_CLEANUP_LOCATOR"],
			RequestHash:    env["MERCATOR_REQUEST_HASH"],
			Phase:          phaseFromInstance(i),
		})
	}
	return owned, nil
}

// --- helpers ---

const labelPrefix = "mercator-"

func instanceLabel(launchKey string) string { return labelPrefix + launchKey }

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

// conflictingToken reports a POSITIVE ownership-token mismatch (token present
// and different). A missing/empty token never conflicts — a false conflict
// would fail cleanup and orphan a paid instance, the worse failure.
func conflictingToken(i instance, ownershipToken string) bool {
	if ownershipToken == "" {
		return false
	}
	tok, ok := i.env()["MERCATOR_OWNERSHIP_TOKEN"]
	return ok && tok != ownershipToken
}

// findOwned locates our instance by its launch-key label and verifies
// ownership. The boolean is false when no such instance exists (treated as
// released by callers).
func (a *Adapter) findOwned(ctx context.Context, launchKey, ownershipToken string) (instance, bool, error) {
	label := instanceLabel(launchKey)
	rows, err := a.api.listInstances(ctx, label)
	if err != nil {
		return instance{}, false, err
	}
	for _, i := range rows {
		if i.Label != label {
			continue
		}
		if conflictingToken(i, ownershipToken) {
			return instance{}, false, adapter.ErrIdempotencyConflict
		}
		return i, true, nil
	}
	return instance{}, false, nil
}

func (a *Adapter) deleteOwned(ctx context.Context, launchKey, ownershipToken string) (bool, error) {
	i, found, err := a.findOwned(ctx, launchKey, ownershipToken)
	if err != nil {
		return false, err
	}
	if !found {
		return true, nil // already gone — idempotent
	}
	if err := a.api.destroyInstance(ctx, i.ID); err != nil {
		return false, err
	}
	return true, nil
}

// phaseFromInstance maps Vast's container status onto the adapter phase
// model. Vast never reports an exit code, so a stopped container maps to
// failed (pessimistic); the workload's exit report is authoritative and
// overrides this via the orchestrator's report finalize path. "offline" (host
// dropped out) stays queued — non-terminal — and the orchestrator's timeouts
// decide when to give up on it.
func phaseFromInstance(i instance) adapter.ExternalPhase {
	switch strings.ToLower(strings.TrimSpace(i.ActualStatus)) {
	case "running":
		return adapter.ExternalPhaseRunning
	case "exited", "stopped":
		return adapter.ExternalPhaseFailed
	default: // "", "created", "loading", "offline"
		return adapter.ExternalPhaseQueued
	}
}

var _ adapter.Adapter = (*Adapter)(nil)
