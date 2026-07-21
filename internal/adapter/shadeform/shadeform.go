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
// MERCATOR_* env contract; an instance is ours iff it carries the launch-key
// tag.
const (
	tagLaunchKey      = "mercator:launch-key"
	tagWorkspace      = "mercator:workspace"
	tagRun            = "mercator:run"
	tagAttempt        = "mercator:attempt"
	tagOwnershipToken = "mercator:ownership-token"
	tagRequestHash    = "mercator:request-hash"
	tagCleanupLocator = "mercator:cleanup-locator"
)

const defaultMaxLifetimeHours = 24

// autoDeleteSlack is added on top of the run's MaxRuntimeSeconds when deriving
// the provider-side auto_delete backstop: generous enough to never race a
// healthy run's own cleanup, tight enough to bound a dead broker's spend.
const autoDeleteSlack = time.Hour

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
	// maxLifetime is the auto_delete horizon used when a launch carries no
	// MaxRuntimeSeconds. It is the reclamation backstop for a dead broker,
	// not the run timeout — the janitor remains the primary cleanup.
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
//
// Failure semantics: a create whose outcome is unknown (5xx/transport, or a
// created instance that never surfaces in the list) returns
// adapter.ErrLaunchIndeterminate so the orchestrator records the launch as
// indeterminate and reconciles through Observe/ListOwned instead of closing
// the run with no cleanup.
func (a *Adapter) Launch(ctx context.Context, req adapter.LaunchRequest) (adapter.LaunchReceipt, error) {
	// Defense in depth: the scheduler already rejects entrypoint-overriding
	// workloads via Capabilities.Container.SupportsEntrypointOverride=false.
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

	if inst, ok, err := a.findLiveOwned(ctx, req.LaunchKey, req.OwnershipToken); err != nil {
		return adapter.LaunchReceipt{}, err
	} else if ok {
		return a.receipt(inst.ID, inst.Status, req, true), nil
	}

	create, err := a.createRequestFor(ctx, req, cloud, region, shadeType)
	if err != nil {
		return adapter.LaunchReceipt{}, err
	}
	id, err := a.client.createInstance(ctx, create)
	if err != nil {
		// An indeterminate failure may still have landed the instance:
		// re-list and adopt it; otherwise let the orchestrator reconcile.
		if inst, ok, _ := a.findLiveOwned(ctx, req.LaunchKey, req.OwnershipToken); ok {
			return a.receipt(inst.ID, inst.Status, req, true), nil
		}
		return adapter.LaunchReceipt{}, err
	}

	return a.reconcileDuplicates(ctx, req, id)
}

func (a *Adapter) createRequestFor(ctx context.Context, req adapter.LaunchRequest, cloud, region, shadeType string) (createRequest, error) {
	it, err := a.lookupType(ctx, cloud, shadeType)
	if err != nil {
		return createRequest{}, err
	}
	os, err := chooseOS(a.osOverride, it.Configuration.OSOptions)
	if err != nil {
		return createRequest{}, err
	}
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
		OS:                os,
		LaunchConfiguration: &launchConfiguration{
			Type:                "docker",
			DockerConfiguration: docker,
		},
		AutoDelete: a.autoDeleteFor(req, it),
		Tags:       ownershipTags(req),
	}, nil
}

// autoDeleteFor derives the provider-side reclamation backstop. The horizon is
// the run's own execution bound plus slack when the launch carries one, else
// the connection's max_lifetime_hours; the spend cap is the catalog price over
// that horizon. A zero catalog price (bring-your-own-cloud inventory bills
// through the provider, not Shadeform) omits the spend threshold: Shadeform
// leaves "0.00" semantics undefined and a spend cap on zero spend caps
// nothing — the date threshold still bounds the instance in time.
func (a *Adapter) autoDeleteFor(req adapter.LaunchRequest, it instanceType) *autoDelete {
	lifetime := a.maxLifetime
	if req.MaxRuntimeSeconds > 0 {
		lifetime = time.Duration(req.MaxRuntimeSeconds)*time.Second + autoDeleteSlack
	}
	out := &autoDelete{DateThreshold: a.now().UTC().Add(lifetime).Format(time.RFC3339)}
	if it.HourlyPrice > 0 {
		spendUSD := float64(it.HourlyPrice) / 100.0 * lifetime.Hours()
		out.SpendThreshold = strconv.FormatFloat(spendUSD, 'f', 2, 64)
	}
	return out
}

// lookupType fetches the live catalog record for the launch triple. It is
// required, not best-effort: without the catalog record the auto_delete
// backstop and OS image cannot be derived, and launching an uncapped paid
// instance is the worse failure.
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

// findLiveOwned is the single definition of "our live instance for this launch
// key": every path (pre-scan, post-failure adoption, Observe) resolves the
// same winner, so all participants converge on the same instance.
func (a *Adapter) findLiveOwned(ctx context.Context, launchKey, ownershipToken string) (instance, bool, error) {
	instances, err := a.client.listInstances(ctx)
	if err != nil {
		return instance{}, false, err
	}
	inst, ok := oldest(liveMatches(matching(instances, launchKey)))
	if !ok {
		return instance{}, false, nil
	}
	if err := verifyOwnership(inst, ownershipToken); err != nil {
		return instance{}, false, err
	}
	return inst, true, nil
}

// reconcileDuplicates resolves the client-side idempotency race after a
// successful create: keep the oldest live tagged instance, delete the rest.
// The created instance may lag the list (create is async), so the scan retries
// briefly; a create that never surfaces is reported as indeterminate rather
// than as success, because a success receipt for an unlisted instance would
// make the next Observe read "released" and fail a healthy run.
func (a *Adapter) reconcileDuplicates(ctx context.Context, req adapter.LaunchRequest, createdID string) (adapter.LaunchReceipt, error) {
	const visibilityAttempts = 4
	for i := range visibilityAttempts {
		instances, err := a.client.listInstances(ctx)
		if err != nil {
			break
		}
		live := liveMatches(matching(instances, req.LaunchKey))
		if winner, ok := oldest(live); ok {
			if err := verifyOwnership(winner, req.OwnershipToken); err != nil {
				return adapter.LaunchReceipt{}, err
			}
			for _, inst := range live {
				if inst.ID == winner.ID {
					continue
				}
				if err := a.client.deleteInstance(ctx, inst.ID); err != nil {
					log.Printf("shadeform: failed to delete duplicate instance %s for launch key %s (janitor/auto_delete will reclaim): %v", inst.ID, req.LaunchKey, err)
				}
			}
			return a.receipt(winner.ID, winner.Status, req, winner.ID != createdID), nil
		}
		if i < visibilityAttempts-1 {
			if err := a.client.wait(ctx, i); err != nil {
				break
			}
		}
	}
	return adapter.LaunchReceipt{}, fmt.Errorf("%w: shadeform: created instance %s not yet visible in the instance list", adapter.ErrLaunchIndeterminate, createdID)
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
	inst, ok := oldest(liveMatches(matches))
	if !ok {
		// No live instance: deleted, deleting, or never surfaced — released
		// either way. Keep a deterministic ID when terminal records are still
		// listed so successive polls agree.
		obs := adapter.ExternalObservation{LaunchKey: req.LaunchKey, Phase: adapter.ExternalPhaseReleased, ObservedAt: a.now().UTC()}
		if terminal, ok := oldest(matches); ok {
			obs.ExternalID = terminal.ID
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
// client-side (GET /instances has no query parameters) down to instances
// carrying our launch-key tag. Instances already in deleting/deleted are
// excluded — Shadeform stops billing at deleting and re-deleting them is
// noise.
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

// chooseOS picks the OS image for the instance. shade_os images bake in GPU
// drivers and the container runtime the docker launch configuration depends
// on, so without an explicit override a catalog entry offering no shade_os
// image fails loudly: silently launching a plain OS would burn a paid VM whose
// container never starts.
func chooseOS(override string, options []string) (string, error) {
	if override != "" {
		return override, nil
	}
	for _, o := range options {
		if strings.Contains(o, "shade_os") {
			return o, nil
		}
	}
	return "", fmt.Errorf("shadeform: no shade_os image among os options %v; set the connection's os config to launch on this type", options)
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
	values := make(map[string]string, len(req.Environment)+7)
	for _, b := range req.Environment {
		if b.Value != nil {
			values[b.Name] = *b.Value
		}
	}
	values["MERCATOR_WORKSPACE_ID"] = req.WorkspaceID
	values["MERCATOR_RUN_ID"] = req.RunID
	values["MERCATOR_ATTEMPT_ID"] = req.AttemptID
	values["MERCATOR_LAUNCH_KEY"] = req.LaunchKey
	values["MERCATOR_OWNERSHIP_TOKEN"] = req.OwnershipToken
	values["MERCATOR_REQUEST_HASH"] = req.RequestHash
	values["MERCATOR_CLEANUP_LOCATOR"] = req.CleanupLocator
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	envs := make([]envVar, 0, len(names))
	for _, name := range names {
		envs = append(envs, envVar{Name: name, Value: values[name]})
	}
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

// oldest picks the deterministic winner among instances sharing a launch key:
// earliest created_at, instance id as tie-break. Every code path uses the same
// rule so all participants converge on the same instance.
func oldest(instances []instance) (instance, bool) {
	if len(instances) == 0 {
		return instance{}, false
	}
	winner := instances[0]
	for _, inst := range instances[1:] {
		if inst.CreatedAt.Before(winner.CreatedAt) ||
			(inst.CreatedAt.Equal(winner.CreatedAt) && inst.ID < winner.ID) {
			winner = inst
		}
	}
	return winner, true
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

var _ adapter.Provider = (*Adapter)(nil)
