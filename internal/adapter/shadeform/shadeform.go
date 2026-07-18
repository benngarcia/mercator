// Package shadeform launches Mercator workloads on Shadeform
// (api.shadeform.ai), a GPU marketplace aggregator that fronts ~21 provider
// clouds behind one API. Each run provisions a VM whose docker launch
// configuration auto-runs exactly one container with --network=host.
//
// Shadeform's lifecycle is VM-only: an instance stays "active" forever no
// matter what the container does — there is no container status, exit code, or
// logs endpoint. Observe therefore reports only the VM phase, and the run's own
// signed exit report is the authoritative workload outcome (the same pessimism
// as the RunPod adapter).
package shadeform

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/domain"
)

// Ownership metadata lives in instance tags (the only mutable, list-visible,
// client-searchable field Shadeform offers). The tag set mirrors RunPod's
// MERCATOR_* env contract.
const (
	tagPrefix         = "mercator:"
	tagLaunchKey      = "mercator:launch-key"
	tagWorkspace      = "mercator:workspace"
	tagRun            = "mercator:run"
	tagAttempt        = "mercator:attempt"
	tagOwnershipToken = "mercator:ownership-token"
	tagRequestHash    = "mercator:request-hash"
	tagCleanupLocator = "mercator:cleanup-locator"
)

const defaultMaxLifetimeHours = 24

type Adapter struct {
	client *client
	// shadeCloud selects Shadeform's managed account (true) vs a linked
	// bring-your-own-cloud account (false).
	shadeCloud bool
	// allowedClouds, when non-nil, is the static allow-list of provider cloud
	// slugs: ListOffers filters to it and Launch rejects anything outside it.
	// The API exposes no per-provider trust attributes, so this is the whole
	// secure-cloud story.
	allowedClouds map[string]bool
	osOverride    string
	// maxLifetime bounds every instance via Shadeform's auto_delete: a date
	// threshold at launch+maxLifetime and a spend threshold of the offer's
	// hourly price over that window. It is the reclamation backstop for a dead
	// broker, not the run timeout — the janitor remains the primary cleanup.
	maxLifetime time.Duration
	regUser     string
	regPass     string
	now         func() time.Time
}

func New(secret string, config map[string]string) (*Adapter, error) {
	shadeCloud := true
	if raw := strings.TrimSpace(config["shade_cloud"]); raw != "" {
		v, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, fmt.Errorf("shadeform: invalid shade_cloud %q: %w", raw, err)
		}
		shadeCloud = v
	}
	var allowed map[string]bool
	if raw := strings.TrimSpace(config["allowed_clouds"]); raw != "" {
		allowed = map[string]bool{}
		for _, part := range strings.Split(raw, ",") {
			if cloud := strings.ToLower(strings.TrimSpace(part)); cloud != "" {
				allowed[cloud] = true
			}
		}
	}
	lifetimeHours := defaultMaxLifetimeHours
	if raw := strings.TrimSpace(config["max_lifetime_hours"]); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("shadeform: invalid max_lifetime_hours %q: must be a positive integer", raw)
		}
		lifetimeHours = n
	}
	return &Adapter{
		client:        newClient(secret, config["base_url"], http.DefaultClient),
		shadeCloud:    shadeCloud,
		allowedClouds: allowed,
		osOverride:    config["os"],
		maxLifetime:   time.Duration(lifetimeHours) * time.Hour,
		regUser:       config["registry_username"],
		regPass:       config["registry_password"],
		now:           time.Now,
	}, nil
}

func (a *Adapter) Verify(ctx context.Context) error {
	_, err := a.client.listInstances(ctx)
	return err
}

func (a *Adapter) ListOffers(ctx context.Context, req adapter.OfferRequest) ([]domain.OfferSnapshot, error) {
	types, err := a.client.instanceTypes(ctx, url.Values{"available": {"true"}, "sort": {"price"}})
	if err != nil {
		return nil, err
	}
	offers, excludedNonVM := buildOffers(types, a.allowedClouds, a.now().UTC())
	if excludedNonVM > 0 {
		log.Printf("shadeform: excluded %d non-vm instance types (deployment_type container/baremetal is undocumented; open question with Shadeform support)", excludedNonVM)
	}
	return offers, nil
}

// Launch is idempotent without any server-side key: Shadeform's create has no
// idempotency token, so identity is the deterministic mercator:launch-key tag.
// Before creating we scan for a live tagged instance; after creating we scan
// again and, if a concurrent duplicate slipped through, keep the oldest and
// delete the rest. Residual race: two launchers can each pass the pre-scan and
// create, and if BOTH then fail to reach the reconciliation scan (crash,
// network partition), two instances survive until Observe/cleanup/janitor —
// all of which resolve every tagged match — converge on one. auto_delete
// bounds the worst case in money and time.
func (a *Adapter) Launch(ctx context.Context, req adapter.LaunchRequest) (adapter.LaunchReceipt, error) {
	if req.Entrypoint != nil {
		return adapter.LaunchReceipt{}, fmt.Errorf("shadeform: docker launch configuration cannot override the image entrypoint; bake the entrypoint into the image or express it as args")
	}
	cloud, region, shadeType, err := parseNativeRef(req.SelectedOfferNativeRef)
	if err != nil {
		return adapter.LaunchReceipt{}, err
	}
	if a.allowedClouds != nil && !a.allowedClouds[strings.ToLower(cloud)] {
		return adapter.LaunchReceipt{}, fmt.Errorf("shadeform: cloud %q is not in this connection's allowed_clouds", cloud)
	}

	existing, err := a.client.listInstances(ctx)
	if err != nil {
		return adapter.LaunchReceipt{}, err
	}
	if inst, ok := oldestLive(matching(existing, req.LaunchKey)); ok {
		if err := verifyOwnership(inst, req.OwnershipToken); err != nil {
			return adapter.LaunchReceipt{}, err
		}
		return a.receipt(inst.ID, inst.Status, req, true), nil
	}

	it, err := a.lookupType(ctx, cloud, shadeType)
	if err != nil {
		return adapter.LaunchReceipt{}, err
	}
	id, err := a.client.createInstance(ctx, a.createRequestFor(req, cloud, region, shadeType, it))
	if err != nil {
		// A 5xx/transport failure leaves the create indeterminate. Re-list and
		// adopt the instance if it actually landed; otherwise surface the error.
		if inst, ok := a.adoptExisting(ctx, req); ok {
			return a.receipt(inst.ID, inst.Status, req, true), nil
		}
		return adapter.LaunchReceipt{}, err
	}

	return a.reconcileDuplicates(ctx, req, id), nil
}

func (a *Adapter) createRequestFor(req adapter.LaunchRequest, cloud, region, shadeType string, it instanceType) createRequest {
	deadline := a.now().UTC().Add(a.maxLifetime)
	spendUSD := float64(it.HourlyPrice) / 100.0 * a.maxLifetime.Hours()
	docker := &dockerConfiguration{
		Image: req.Image,
		Args:  shellJoin(req.Args),
		Envs:  launchEnvs(req),
	}
	if a.regUser != "" || a.regPass != "" {
		docker.RegistryCredentials = &registryCredentials{Username: a.regUser, Password: a.regPass}
	}
	return createRequest{
		Cloud:             cloud,
		Region:            region,
		ShadeInstanceType: shadeType,
		ShadeCloud:        a.shadeCloud,
		Name:              instanceName(req.LaunchKey),
		OS:                chooseOS(a.osOverride, it.Configuration.OSOptions),
		LaunchConfiguration: &launchConfiguration{
			Type:                "docker",
			DockerConfiguration: docker,
		},
		AutoDelete: &autoDelete{
			DateThreshold:  deadline.Format(time.RFC3339),
			SpendThreshold: strconv.FormatFloat(spendUSD, 'f', 2, 64),
		},
		Tags: ownershipTags(req),
	}
}

// lookupType fetches the live catalog record for the launch triple. It is
// required, not best-effort: without the hourly price the auto_delete spend
// backstop cannot be derived, and launching an uncapped paid instance is the
// worse failure.
func (a *Adapter) lookupType(ctx context.Context, cloud, shadeType string) (instanceType, error) {
	types, err := a.client.instanceTypes(ctx, url.Values{"cloud": {cloud}, "shade_instance_type": {shadeType}})
	if err != nil {
		return instanceType{}, err
	}
	for _, t := range types {
		if strings.EqualFold(t.Cloud, cloud) && t.ShadeInstanceType == shadeType {
			return t, nil
		}
	}
	return instanceType{}, fmt.Errorf("shadeform: instance type %s/%s not found in catalog; cannot derive auto_delete backstop", cloud, shadeType)
}

func (a *Adapter) adoptExisting(ctx context.Context, req adapter.LaunchRequest) (instance, bool) {
	instances, err := a.client.listInstances(ctx)
	if err != nil {
		return instance{}, false
	}
	inst, ok := oldestLive(matching(instances, req.LaunchKey))
	if !ok || verifyOwnership(inst, req.OwnershipToken) != nil {
		return instance{}, false
	}
	return inst, true
}

func (a *Adapter) reconcileDuplicates(ctx context.Context, req adapter.LaunchRequest, createdID string) adapter.LaunchReceipt {
	instances, err := a.client.listInstances(ctx)
	if err != nil {
		// Created but cannot re-list: return our receipt. Any concurrent
		// duplicate is resolved later — Observe and cleanup act on every
		// tagged match, and auto_delete bounds a stray.
		return a.receipt(createdID, "creating", req, false)
	}
	live := liveMatches(matching(instances, req.LaunchKey))
	winner, ok := oldestLive(live)
	if !ok {
		// The async create hasn't surfaced in the list yet.
		return a.receipt(createdID, "creating", req, false)
	}
	for _, inst := range live {
		if inst.ID == winner.ID {
			continue
		}
		if err := a.client.deleteInstance(ctx, inst.ID); err != nil {
			log.Printf("shadeform: failed to delete duplicate instance %s for launch key %s (janitor/auto_delete will reclaim): %v", inst.ID, req.LaunchKey, err)
		}
	}
	return a.receipt(winner.ID, winner.Status, req, winner.ID != createdID)
}

func (a *Adapter) receipt(externalID, status string, req adapter.LaunchRequest, duplicate bool) adapter.LaunchReceipt {
	return adapter.LaunchReceipt{
		ExternalID:     externalID,
		LaunchKey:      req.LaunchKey,
		OwnershipToken: req.OwnershipToken,
		CleanupLocator: req.CleanupLocator,
		Phase:          phaseFromStatus(status),
		AcceptedAt:     a.now().UTC(),
		Duplicate:      duplicate,
	}
}

func (a *Adapter) Observe(ctx context.Context, req adapter.ObserveRequest) (adapter.ExternalObservation, error) {
	instances, err := a.client.listInstances(ctx)
	if err != nil {
		return adapter.ExternalObservation{}, err
	}
	matches := matching(instances, req.LaunchKey)
	inst, ok := oldestLive(matches)
	if !ok {
		// No live instance: deleted, deleting, or never surfaced — released
		// either way. Keep the ID when a terminal record is still listed.
		obs := adapter.ExternalObservation{LaunchKey: req.LaunchKey, Phase: adapter.ExternalPhaseReleased, ObservedAt: a.now().UTC()}
		if len(matches) > 0 {
			obs.ExternalID = matches[0].ID
		}
		return obs, nil
	}
	if err := verifyOwnership(inst, req.OwnershipToken); err != nil {
		return adapter.ExternalObservation{}, err
	}
	return adapter.ExternalObservation{
		ExternalID: inst.ID,
		LaunchKey:  req.LaunchKey,
		Phase:      phaseFromStatus(inst.Status),
		ObservedAt: a.now().UTC(),
	}, nil
}

func (a *Adapter) Cancel(ctx context.Context, req adapter.CancelRequest) (adapter.CancelReceipt, error) {
	// Best-effort: deleting a not-yet-active instance is the same delete path.
	if _, err := a.deleteOwned(ctx, req.LaunchKey, ""); err != nil {
		return adapter.CancelReceipt{}, err
	}
	return adapter.CancelReceipt{Cancelled: true}, nil
}

// Release behaves exactly like Terminate: the adapter is provisionable-only, so
// "our slot" and "the host we own" are the same instance, and /instances/{id}/delete
// is the only teardown Shadeform offers. (/restart is never used — whether it
// re-runs the launch configuration is undocumented.)
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

// ListOwned is the janitor's view: the full-account instance list filtered
// client-side (GET /instances has no query parameters) down to our tag
// namespace. Instances already in deleting/deleted are excluded — Shadeform
// stops billing at deleting and re-deleting them is noise.
func (a *Adapter) ListOwned(ctx context.Context, req adapter.OwnershipQuery) ([]adapter.OwnedExternalObject, error) {
	instances, err := a.client.listInstances(ctx)
	if err != nil {
		return nil, err
	}
	owned := make([]adapter.OwnedExternalObject, 0, len(instances))
	for _, inst := range instances {
		launchKey, ok := tagValue(inst.Tags, tagLaunchKey)
		if !ok || !live(inst) {
			continue
		}
		workspace, _ := tagValue(inst.Tags, tagWorkspace)
		if req.WorkspaceID != "" && workspace != req.WorkspaceID {
			continue
		}
		runID, _ := tagValue(inst.Tags, tagRun)
		attemptID, _ := tagValue(inst.Tags, tagAttempt)
		token, _ := tagValue(inst.Tags, tagOwnershipToken)
		locator, _ := tagValue(inst.Tags, tagCleanupLocator)
		requestHash, _ := tagValue(inst.Tags, tagRequestHash)
		owned = append(owned, adapter.OwnedExternalObject{
			ExternalID:     inst.ID,
			WorkspaceID:    workspace,
			RunID:          runID,
			AttemptID:      attemptID,
			OwnershipToken: token,
			LaunchKey:      launchKey,
			CleanupLocator: locator,
			RequestHash:    requestHash,
			Phase:          phaseFromStatus(inst.Status),
		})
	}
	return owned, nil
}

// --- helpers ---

func instanceName(launchKey string) string { return "mercator-" + launchKey }

func parseNativeRef(ref string) (cloud, region, shadeType string, err error) {
	parts := strings.Split(ref, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", fmt.Errorf("shadeform: native ref %q is not a cloud/region/shade_instance_type triple", ref)
	}
	return parts[0], parts[1], parts[2], nil
}

func chooseOS(override string, options []string) string {
	if override != "" {
		return override
	}
	// shade_os images bake in GPU drivers and the container runtime the docker
	// launch configuration depends on; prefer them over plain OS images.
	for _, o := range options {
		if strings.Contains(o, "shade_os") {
			return o
		}
	}
	if len(options) > 0 {
		return options[0]
	}
	return ""
}

func ownershipTags(req adapter.LaunchRequest) []string {
	return []string{
		tagLaunchKey + "=" + req.LaunchKey,
		tagWorkspace + "=" + req.WorkspaceID,
		tagRun + "=" + req.RunID,
		tagAttempt + "=" + req.AttemptID,
		tagOwnershipToken + "=" + req.OwnershipToken,
		tagRequestHash + "=" + req.RequestHash,
		tagCleanupLocator + "=" + req.CleanupLocator,
	}
}

// launchEnvs passes the workload environment into the container plus the
// MERCATOR_* ownership/identity keys; these intentionally match the
// orchestrator-injected reporting vars of the same name (identical values).
func launchEnvs(req adapter.LaunchRequest) []envVar {
	envs := make([]envVar, 0, len(req.Environment)+7)
	for _, b := range req.Environment {
		if b.Value != nil {
			envs = append(envs, envVar{Name: b.Name, Value: *b.Value})
		}
	}
	envs = append(envs,
		envVar{Name: "MERCATOR_WORKSPACE_ID", Value: req.WorkspaceID},
		envVar{Name: "MERCATOR_RUN_ID", Value: req.RunID},
		envVar{Name: "MERCATOR_ATTEMPT_ID", Value: req.AttemptID},
		envVar{Name: "MERCATOR_LAUNCH_KEY", Value: req.LaunchKey},
		envVar{Name: "MERCATOR_OWNERSHIP_TOKEN", Value: req.OwnershipToken},
		envVar{Name: "MERCATOR_REQUEST_HASH", Value: req.RequestHash},
		envVar{Name: "MERCATOR_CLEANUP_LOCATOR", Value: req.CleanupLocator},
	)
	return envs
}

func tagValue(tags []string, key string) (string, bool) {
	for _, tag := range tags {
		if value, ok := strings.CutPrefix(tag, key+"="); ok {
			return value, true
		}
	}
	return "", false
}

func matching(instances []instance, launchKey string) []instance {
	var out []instance
	for _, inst := range instances {
		if v, ok := tagValue(inst.Tags, tagLaunchKey); ok && v == launchKey {
			out = append(out, inst)
		}
	}
	return out
}

func live(inst instance) bool {
	s := strings.ToLower(inst.Status)
	return s != "deleting" && s != "deleted"
}

func liveMatches(instances []instance) []instance {
	var out []instance
	for _, inst := range instances {
		if live(inst) {
			out = append(out, inst)
		}
	}
	return out
}

// oldestLive picks the deterministic winner among live instances sharing a
// launch key: oldest created_at, instance id as tie-break. Every code path
// (launch pre-scan, reconciliation, observe, delete) uses the same rule so all
// participants converge on the same instance.
func oldestLive(instances []instance) (instance, bool) {
	liveOnly := liveMatches(instances)
	if len(liveOnly) == 0 {
		return instance{}, false
	}
	sort.Slice(liveOnly, func(i, j int) bool {
		if !liveOnly[i].CreatedAt.Equal(liveOnly[j].CreatedAt) {
			return liveOnly[i].CreatedAt.Before(liveOnly[j].CreatedAt)
		}
		return liveOnly[i].ID < liveOnly[j].ID
	})
	return liveOnly[0], true
}

// verifyOwnership conflicts only on a POSITIVE token mismatch (tag present and
// different), never on a missing tag — a false conflict would fail cleanup and
// orphan a paid instance, the worse failure.
func verifyOwnership(inst instance, ownershipToken string) error {
	if ownershipToken == "" {
		return nil
	}
	if tok, ok := tagValue(inst.Tags, tagOwnershipToken); ok && tok != ownershipToken {
		return adapter.ErrIdempotencyConflict
	}
	return nil
}

func (a *Adapter) deleteOwned(ctx context.Context, launchKey, ownershipToken string) (bool, error) {
	instances, err := a.client.listInstances(ctx)
	if err != nil {
		return false, err
	}
	// Delete EVERY live match, not just the winner: if a past reconciliation
	// failed mid-way, cleanup is the path that converges back to zero.
	targets := liveMatches(matching(instances, launchKey))
	for _, inst := range targets {
		if err := verifyOwnership(inst, ownershipToken); err != nil {
			return false, err
		}
	}
	for _, inst := range targets {
		if err := a.client.deleteInstance(ctx, inst.ID); err != nil {
			return false, err
		}
	}
	return true, nil
}

// phaseFromStatus maps the VM-only status lifecycle onto external phases.
// "active" means only that the VM is up — Shadeform reports nothing about the
// container, so a workload can be long finished while the phase stays running.
// The run's signed exit report is the authoritative outcome.
func phaseFromStatus(status string) adapter.ExternalPhase {
	switch strings.ToLower(status) {
	case "active":
		return adapter.ExternalPhaseRunning
	case "error":
		return adapter.ExternalPhaseFailed
	case "deleting", "deleted":
		return adapter.ExternalPhaseReleased
	default: // creating, pending_provider, pending, or anything new
		return adapter.ExternalPhaseQueued
	}
}

var _ adapter.Adapter = (*Adapter)(nil)
