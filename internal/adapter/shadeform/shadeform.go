// Package shadeform rents machines through Shadeform (api.shadeform.ai), a GPU
// marketplace aggregator that fronts ~21 provider clouds behind one API, and
// bootstraps a Mercator node agent onto each one.
//
// It is a CapacityProvider and nothing else. What it sells is a VM Mercator
// holds across Runs, so what executes on that VM is the enrolled agent's
// business: the create body carries a bootstrap script and never a workload
// image. The docker launch configuration this adapter used to send ran one
// container per machine and reported nothing about it, which is a one-shot
// execution product wearing a rented machine's clothes.
//
// Shadeform's lifecycle is VM-only: an instance reports creating,
// pending_provider, pending, active, deleting, deleted, with an error off-ramp,
// and there is no stop, no resume, and no suspend. ObserveCapacity therefore
// reports the machine and never the work on it; a node's own session is what
// makes anything on that machine observable.
package shadeform

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Ownership metadata lives in instance tags, the only mutable, list-visible,
// client-searchable field Shadeform offers. A machine is one Mercator holds iff
// it carries the Rental tag, and each tag here is a field the reconciler reads
// back out of the account listing.
const (
	tagRental         = "mercator:rental"
	tagGeneration     = "mercator:generation"
	tagWorkspace      = "mercator:workspace"
	tagOwnershipToken = "mercator:ownership-token"
)

const defaultMaxLifetimeHours = 24

// autoDeleteSlack is added on top of a lease's own bound when deriving the
// provider-side auto_delete backstop: generous enough never to race Mercator's
// own teardown, tight enough to bound a dead broker's spend.
const autoDeleteSlack = time.Hour

type Adapter struct {
	client *client
	// shadeCloud selects Shadeform's managed account (true) vs a linked
	// bring-your-own-cloud account (false).
	shadeCloud bool
	// allowedClouds, when non-nil, is the static allow-list of provider cloud
	// slugs: ListCapacity filters to it and ProvisionCapacity rejects anything
	// outside it. The API exposes no per-provider trust attributes, so this is
	// the whole secure-cloud story.
	allowedClouds map[string]bool
	osOverride    string
	// agentDownloadURL is where a provisioned machine fetches the node agent
	// build its bootstrap pinned. It has no default: see agentSource.
	agentDownloadURL string
	// maxLifetime is the auto_delete horizon used for a lease that carries no
	// bound of its own. It is the reclamation backstop for a dead broker, not
	// the lease.
	maxLifetime time.Duration
	now         func() time.Time
}

func New(secret string, config map[string]string) (*Adapter, error) {
	shadeCloud := true
	if raw := strings.TrimSpace(config["shade_cloud"]); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, fmt.Errorf("shadeform: invalid shade_cloud %q: %w", raw, err)
		}
		shadeCloud = value
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
		hours, err := strconv.Atoi(raw)
		if err != nil || hours <= 0 {
			return nil, fmt.Errorf("shadeform: invalid max_lifetime_hours %q: must be a positive integer", raw)
		}
		lifetimeHours = hours
	}
	agentDownloadURL, err := agentDownloadURL(config["agent_download_url"])
	if err != nil {
		return nil, err
	}
	return &Adapter{
		client:           newClient(secret, config["base_url"], &http.Client{Timeout: time.Minute}),
		shadeCloud:       shadeCloud,
		allowedClouds:    allowed,
		osOverride:       config["os"],
		agentDownloadURL: agentDownloadURL,
		maxLifetime:      time.Duration(lifetimeHours) * time.Hour,
		now:              time.Now,
	}, nil
}

// agentDownloadURL checks the connection's agent source at the moment an
// operator authorizes the connection rather than at the moment a machine is
// paid for. A connection that states none is still built: it can be verified and
// it can list capacity, and what it cannot do is provision, which is where the
// refusal lands.
func agentDownloadURL(configured string) (string, error) {
	source := strings.TrimSpace(configured)
	if source == "" {
		return "", nil
	}
	parsed, err := url.Parse(source)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return "", fmt.Errorf("shadeform: agent_download_url must be an absolute https URL; a rented machine fetches its agent over the open internet")
	}
	if !strings.Contains(source, agentVersionPlaceholder) {
		return "", fmt.Errorf(
			"shadeform: agent_download_url must contain %s, or every machine installs whatever build is behind that URL on the day it boots",
			agentVersionPlaceholder,
		)
	}
	return source, nil
}

// Verify is the cheap credential and reachability check. It allocates nothing.
func (a *Adapter) Verify(ctx context.Context) error {
	_, err := a.client.listInstances(ctx)
	return err
}

// lookupType fetches the live catalog record for one machine. It is required,
// not best-effort: without it neither the auto_delete backstop nor the OS image
// can be derived, and renting an uncapped machine is the worse failure.
func (a *Adapter) lookupType(ctx context.Context, cloud, shadeType string) (instanceType, error) {
	types, err := a.client.instanceTypes(ctx, url.Values{"cloud": {cloud}, "shade_instance_type": {shadeType}})
	if err != nil {
		return instanceType{}, err
	}
	for _, candidate := range types {
		if strings.EqualFold(candidate.Cloud, cloud) && candidate.ShadeInstanceType == shadeType {
			return candidate, nil
		}
	}
	return instanceType{}, fmt.Errorf("shadeform: instance type %s/%s not found in catalog; cannot derive auto_delete backstop", cloud, shadeType)
}

func instanceName(rentalID string) string { return "mercator-" + rentalID }

func parseNativeRef(ref string) (cloud, region, shadeType string, err error) {
	parts := strings.Split(ref, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", fmt.Errorf("shadeform: native ref %q is not a cloud/region/shade_instance_type triple", ref)
	}
	return parts[0], parts[1], parts[2], nil
}

// chooseOS picks the OS image for the machine. shade_os images bake in the GPU
// driver and the container runtime the node agent needs to run anything at all,
// so without an explicit override a catalog entry offering no shade_os image
// fails loudly: renting a plain host would burn a paid machine whose agent can
// start no container.
func chooseOS(override string, options []string) (string, error) {
	if override != "" {
		return override, nil
	}
	for _, option := range options {
		if strings.Contains(option, "shade_os") {
			return option, nil
		}
	}
	return "", fmt.Errorf("shadeform: no shade_os image among os options %v; set the connection's os config to rent this type", options)
}

func tagValue(tags []string, key string) (string, bool) {
	for _, tag := range tags {
		if value, found := strings.CutPrefix(tag, key+"="); found {
			return value, true
		}
	}
	return "", false
}

func matchingRental(instances []instance, rentalID string) []instance {
	var matched []instance
	for _, candidate := range instances {
		if value, tagged := tagValue(candidate.Tags, tagRental); tagged && value == rentalID {
			matched = append(matched, candidate)
		}
	}
	return matched
}

func live(held instance) bool {
	status := strings.ToLower(held.Status)
	return status != "deleting" && status != "deleted"
}

func liveMatches(instances []instance) []instance {
	var matched []instance
	for _, candidate := range instances {
		if live(candidate) {
			matched = append(matched, candidate)
		}
	}
	return matched
}

// oldest picks the deterministic winner among instances sharing a Rental:
// earliest created_at, instance id as the tie-break. Every path uses the same
// rule, so all of them converge on the same machine.
func oldest(instances []instance) (instance, bool) {
	if len(instances) == 0 {
		return instance{}, false
	}
	winner := instances[0]
	for _, candidate := range instances[1:] {
		if candidate.CreatedAt.Before(winner.CreatedAt) ||
			(candidate.CreatedAt.Equal(winner.CreatedAt) && candidate.ID < winner.ID) {
			winner = candidate
		}
	}
	return winner, true
}

// verifyOwnership conflicts only on a positive token mismatch, tag present and
// different, and never on a missing tag. A false conflict would refuse a
// teardown and orphan a paid machine, which is the worse failure.
//
// The refusal names the instance and never either token: one of them belongs to
// somebody else, and both are material.
func verifyOwnership(held instance, ownershipToken string) error {
	if ownershipToken == "" {
		return nil
	}
	if token, tagged := tagValue(held.Tags, tagOwnershipToken); tagged && token != ownershipToken {
		return fmt.Errorf("shadeform: instance %s is held under another ownership token; this Rental does not hold that machine", held.ID)
	}
	return nil
}

// verifyOwnershipOfAll is the ownership question asked of every machine carrying
// one Rental's tag, which is the only way to ask it that means anything. A tag
// is a label anybody with this account can write, and the ownership token is
// what says a machine wearing it is really this lease's; a path that checked
// only the machine it was about to act on would read a foreign machine as absent
// and go on to observe, adopt or destroy around it.
//
// It refuses on the first mismatch rather than reporting the set, because there
// is nothing safe to do with the rest: this account is holding a machine under
// this lease's name that this lease did not take out, and that is an answer for
// an operator rather than a state to converge from.
func verifyOwnershipOfAll(matches []instance, ownershipToken string) error {
	for _, match := range matches {
		if err := verifyOwnership(match, ownershipToken); err != nil {
			return err
		}
	}
	return nil
}
