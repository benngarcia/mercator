package shadeform

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/benngarcia/mercator/internal/capability"
	"github.com/benngarcia/mercator/internal/domain"
)

// This file is Shadeform's half of the capacity lease: renting a machine for one
// Rental, reporting what the provider can see of it, destroying it, and
// answering what this connection is still holding.
//
// Nothing here knows what a workload is. What runs on a machine Mercator rents
// is the enrolled agent's business, and the only thing this file ever puts on a
// machine is the bootstrap that starts that agent.

var _ capability.CapacityProvider = (*Adapter)(nil)

// CapacitySupport states what Shadeform's four endpoints can actually do, which
// is less than most of this contract allows for.
//
// There is no stop and no resume: /instances/{id}/delete destroys the machine
// and nothing suspends one, so shedding cost here means giving the machine back.
// A disk that survives a stop is therefore a claim about a provider that can
// stop, and this one cannot.
//
// Create honours no idempotency key, so a repeated provision is reconciled by
// scanning this account for the Rental's own tag. That is only safe because the
// full-account listing is available, which is exactly the pair
// CapacitySupport.Validate refuses a provider for breaking.
//
// A destroyed instance stays in the listing while it is deleting and then
// disappears, so a terminate is observable for a window rather than instantly
// unanswerable.
func (a *Adapter) CapacitySupport() capability.CapacitySupport {
	return capability.CapacitySupport{
		Stop:                  false,
		Resume:                false,
		PersistentDisk:        false,
		Spot:                  false,
		ExactPricing:          true,
		IdempotentProvision:   capability.IdempotentProvisionNone,
		ListOwned:             true,
		ObserveAfterTerminate: true,
	}
}

// ListCapacity is the capacity this connection sells: every (cloud, region,
// shade_instance_type) triple the catalog lists, whether or not it is in stock
// right now. A listing filtered on availability makes a sold-out region look
// like inventory nobody sells, and admission reads the difference: capacity to
// wait for against capacity that has to be added.
func (a *Adapter) ListCapacity(ctx context.Context, query capability.CapacityQuery) ([]domain.OfferSnapshot, error) {
	types, err := a.client.instanceTypes(ctx, url.Values{"sort": {"price"}})
	if err != nil {
		return nil, err
	}
	offers, excludedNonVM := buildOffers(types, a.allowedClouds, a.now().UTC())
	if excludedNonVM > 0 {
		log.Printf("shadeform: excluded %d non-vm instance types (deployment_type container/baremetal is undocumented; open question with Shadeform support)", excludedNonVM)
	}
	return offers, nil
}

// ProvisionCapacity rents the machine behind one listing and hands it the node
// bootstrap through a script launch configuration.
//
// It is idempotent without any server-side key, because Shadeform's create has
// none: the Rental's own tag is the identity. Before creating, this scans for a
// live instance already carrying it; after creating it scans again and, if a
// concurrent duplicate slipped through, keeps the oldest and destroys the rest.
// A create whose outcome is unknown is reconciled the same way rather than by a
// second create, which is the whole point of the Rental identity travelling to
// the provider. The residual race is two provisioners that both pass the
// pre-scan and both die before reconciling; auto_delete bounds what that costs.
func (a *Adapter) ProvisionCapacity(ctx context.Context, command capability.ProvisionCommand) (capability.CapacityReceipt, error) {
	cloud, region, shadeType, err := parseNativeRef(command.NativeRef)
	if err != nil {
		return capability.CapacityReceipt{}, err
	}
	if a.allowedClouds != nil && !a.allowedClouds[strings.ToLower(cloud)] {
		return capability.CapacityReceipt{}, fmt.Errorf("shadeform: cloud %q is not in this connection's allowed_clouds", cloud)
	}
	if held, exists, err := a.findLiveRental(ctx, command.RentalID, command.OwnershipToken); err != nil {
		return capability.CapacityReceipt{}, err
	} else if exists {
		return a.receipt(held, true), nil
	}

	create, err := a.createRequestFor(ctx, command, cloud, region, shadeType)
	if err != nil {
		return capability.CapacityReceipt{}, err
	}
	created, err := a.client.createInstance(ctx, create)
	if err != nil {
		// The create may have landed anyway. Adopting what the scan finds is
		// what keeps an indeterminate answer from costing a second machine.
		if held, exists, _ := a.findLiveRental(ctx, command.RentalID, command.OwnershipToken); exists {
			return a.receipt(held, true), nil
		}
		return capability.CapacityReceipt{}, err
	}
	return a.reconcileDuplicates(ctx, command, created)
}

// createRequestFor is the create body: the machine, the bootstrap, the
// reclamation backstop, and the tags that make this machine findable as this
// Rental's. It carries no workload, no environment, and no registry account.
func (a *Adapter) createRequestFor(
	ctx context.Context,
	command capability.ProvisionCommand,
	cloud, region, shadeType string,
) (createRequest, error) {
	script, err := encodedBootstrapScript(command.Bootstrap, a.agentDownloadURL)
	if err != nil {
		return createRequest{}, err
	}
	instanceType, err := a.lookupType(ctx, cloud, shadeType)
	if err != nil {
		return createRequest{}, err
	}
	operatingSystem, err := chooseOS(a.osOverride, instanceType.Configuration.OSOptions)
	if err != nil {
		return createRequest{}, err
	}
	return createRequest{
		Cloud:             cloud,
		Region:            region,
		ShadeInstanceType: shadeType,
		ShadeCloud:        a.shadeCloud,
		Name:              instanceName(command.RentalID),
		OS:                operatingSystem,
		LaunchConfiguration: &launchConfiguration{
			Type:                "script",
			ScriptConfiguration: &scriptConfiguration{Base64Script: script},
		},
		AutoDelete: a.autoDeleteFor(command.MaxLifetimeSeconds, instanceType),
		Tags:       ownershipTags(command),
	}, nil
}

// reconcileDuplicates resolves the client-side idempotency race after a
// successful create: the oldest live instance carrying this Rental's tag wins
// and the rest are destroyed. A created instance may lag the listing, so the
// scan retries briefly; one that never surfaces is reported as indeterminate
// rather than as success, because a receipt for an instance nothing can find
// would have the next observation read the machine as already gone.
func (a *Adapter) reconcileDuplicates(
	ctx context.Context,
	command capability.ProvisionCommand,
	createdID string,
) (capability.CapacityReceipt, error) {
	const visibilityAttempts = 4
	for attempt := range visibilityAttempts {
		instances, err := a.client.listInstances(ctx)
		if err != nil {
			break
		}
		live := liveMatches(matchingRental(instances, command.RentalID))
		if winner, found := oldest(live); found {
			// Every machine wearing this lease's tag, and not merely the one about to
			// be kept. The rest of this loop destroys them, and a machine held under
			// somebody else's token is not this reconciler's to destroy.
			if err := verifyOwnershipOfAll(live, command.OwnershipToken); err != nil {
				return capability.CapacityReceipt{}, err
			}
			for _, duplicate := range live {
				if duplicate.ID == winner.ID {
					continue
				}
				if err := a.client.deleteInstance(ctx, duplicate.ID); err != nil {
					log.Printf("shadeform: failed to delete duplicate instance %s for Rental %s (auto_delete will reclaim): %v", duplicate.ID, command.RentalID, err)
				}
			}
			return a.receipt(winner, winner.ID != createdID), nil
		}
		if attempt < visibilityAttempts-1 {
			if err := a.client.wait(ctx, attempt); err != nil {
				break
			}
		}
	}
	return capability.CapacityReceipt{}, fmt.Errorf(
		"%w: shadeform created instance %s and it is not yet visible in the instance list; the next owned-capacity sweep resolves it",
		capability.ErrCapacityIndeterminate, createdID,
	)
}

// ObserveCapacity is what the provider can see of one machine, which is
// allocation and boot and nothing past them. Whether an agent opened a session
// is the node registry's answer and never this one.
//
// An instance that has left the listing entirely is reported terminated rather
// than unknown. Nothing else can have happened to a machine this account
// allocated: it was destroyed, or the auto_delete backstop reclaimed it, and a
// caller told "unknown" would go on waiting for an agent that has no machine to
// arrive from.
//
// Ownership is asked of every machine wearing the lease's tag rather than of the
// one about to be reported. A second machine tagged for this lease under another
// token is another deployment or a stale script on this account, and reporting
// the one this Rental recognises would leave the account quietly billing for the
// other for as long as nobody looked.
func (a *Adapter) ObserveCapacity(ctx context.Context, ref capability.CapacityRef) (capability.CapacityObservation, error) {
	instances, err := a.client.listInstances(ctx)
	if err != nil {
		return capability.CapacityObservation{}, err
	}
	matches := matchingRental(instances, ref.RentalID)
	if err := verifyOwnershipOfAll(matches, ref.OwnershipToken); err != nil {
		return capability.CapacityObservation{}, err
	}
	observed, found := oldest(liveMatches(matches))
	if !found {
		observed, found = oldest(matches)
	}
	if !found {
		return capability.CapacityObservation{
			NativeRef:  ref.NativeRef,
			State:      capability.CapacityStateTerminated,
			ObservedAt: a.now().UTC(),
		}, nil
	}
	// The observation carries no StateSince, and that absence is deliberate. The
	// only moment a Shadeform instance record holds is created_at, which is when
	// the machine was asked for rather than when it reached the state being
	// reported. A caller that read it as the transition would price the whole
	// acquisition into whichever stage it happened to be watching.
	return capability.CapacityObservation{
		NativeRef:  observed.ID,
		State:      capacityState(observed.Status),
		ObservedAt: a.now().UTC(),
	}, nil
}

// StopCapacity and StartCapacity are the two acts this provider cannot perform.
// Shadeform destroys a machine or leaves it running; there is no suspend, so
// there is nothing to resume. They refuse rather than quietly succeeding,
// because a caller that believed a machine was stopped would stop expecting the
// bill.
func (a *Adapter) StopCapacity(_ context.Context, _ capability.CapacityCommand) (capability.CapacityReceipt, error) {
	return capability.CapacityReceipt{}, fmt.Errorf("%w: shadeform can only destroy a machine, not suspend one", capability.ErrCapabilityUnsupported)
}

func (a *Adapter) StartCapacity(_ context.Context, _ capability.CapacityCommand) (capability.CapacityReceipt, error) {
	return capability.CapacityReceipt{}, fmt.Errorf("%w: shadeform suspends no machine, so it has none to resume", capability.ErrCapabilityUnsupported)
}

// TerminateCapacity destroys every live instance carrying this Rental's tag,
// not merely the one the caller named. A reconciliation that failed halfway
// leaves duplicates behind, and this is the path that converges back to zero.
//
// A terminate that finds nothing live reports Duplicate: the machine is already
// gone, which is the same effect this command asks for, and counting it as a
// second destruction would tell a reader two machines ended.
func (a *Adapter) TerminateCapacity(ctx context.Context, command capability.CapacityCommand) (capability.CapacityReceipt, error) {
	instances, err := a.client.listInstances(ctx)
	if err != nil {
		return capability.CapacityReceipt{}, err
	}
	targets := liveMatches(matchingRental(instances, command.RentalID))
	if err := verifyOwnershipOfAll(targets, command.OwnershipToken); err != nil {
		return capability.CapacityReceipt{}, err
	}
	for _, target := range targets {
		if err := a.client.deleteInstance(ctx, target.ID); err != nil {
			return capability.CapacityReceipt{}, err
		}
	}
	return capability.CapacityReceipt{
		NativeRef:  command.NativeRef,
		State:      capability.CapacityStateTerminated,
		AcceptedAt: a.now().UTC(),
		Duplicate:  len(targets) == 0,
	}, nil
}

// ListOwnedCapacity is every machine this connection is still holding, which is
// the answer a lost provision response is reconciled against. Shadeform's
// listing takes no query parameters, so the full account is filtered here.
//
// The filter is the Rental tag rather than the Mercator prefix. A machine
// carrying no Rental is not capacity Mercator holds, and naming one here would
// have the reconciler adopt it as a lease nothing ever took out. Instances
// already deleting are excluded: Shadeform stops billing when deleting starts,
// and re-destroying them is noise.
func (a *Adapter) ListOwnedCapacity(ctx context.Context, query capability.OwnershipQuery) ([]capability.OwnedCapacity, error) {
	instances, err := a.client.listInstances(ctx)
	if err != nil {
		return nil, err
	}
	owned := make([]capability.OwnedCapacity, 0, len(instances))
	for _, instance := range instances {
		rentalID, tagged := tagValue(instance.Tags, tagRental)
		if !tagged || !live(instance) {
			continue
		}
		ownershipToken, _ := tagValue(instance.Tags, tagOwnershipToken)
		owned = append(owned, capability.OwnedCapacity{
			NativeRef: instance.ID,

			RentalID:       rentalID,
			Generation:     generationTag(instance.Tags),
			OwnershipToken: ownershipToken,
			State:          capacityState(instance.Status),
			CreatedAt:      instance.CreatedAt.UTC(),
		})
	}
	return owned, nil
}

// findLiveRental is the single definition of "this connection's live machine for
// this Rental": every path resolves the same winner, so a pre-scan, a scan after
// a lost answer, and an observation all converge on one instance.
func (a *Adapter) findLiveRental(ctx context.Context, rentalID, ownershipToken string) (instance, bool, error) {
	instances, err := a.client.listInstances(ctx)
	if err != nil {
		return instance{}, false, err
	}
	live := liveMatches(matchingRental(instances, rentalID))
	if err := verifyOwnershipOfAll(live, ownershipToken); err != nil {
		return instance{}, false, err
	}
	held, found := oldest(live)
	if !found {
		return instance{}, false, nil
	}
	return held, true, nil
}

// receipt reports the machine as the provider's own record has it, which is why
// the moment on it is the instance's rather than this adapter's clock. A
// duplicate is a machine allocated before this command was sent, and dating it
// now would put the whole of somebody else's acquisition inside whichever stage
// the caller happens to be timing. A record with no moment falls back to this
// one, because a receipt with a zero time is one no stage can be measured from.
func (a *Adapter) receipt(held instance, duplicate bool) capability.CapacityReceipt {
	acceptedAt := held.CreatedAt.UTC()
	if acceptedAt.IsZero() {
		acceptedAt = a.now().UTC()
	}
	return capability.CapacityReceipt{
		NativeRef:  held.ID,
		State:      capacityState(held.Status),
		AcceptedAt: acceptedAt,
		Duplicate:  duplicate,
	}
}

// autoDeleteFor derives the provider-side reclamation backstop. The horizon is
// the lease's own bound where the command carries one and the connection's
// max_lifetime_hours otherwise; the spend cap is the catalog price over that
// horizon. A zero catalog price (bring-your-own-cloud inventory bills through
// the provider rather than Shadeform) gets the date threshold only: Shadeform
// leaves "0.00" spend semantics undefined, and a cap on zero spend caps nothing.
//
// It is a backstop for a broker that died, never the lease itself. Mercator
// gives a machine back when the Rental ends.
func (a *Adapter) autoDeleteFor(maxLifetimeSeconds int64, held instanceType) *autoDelete {
	lifetime := a.maxLifetime
	if maxLifetimeSeconds > 0 {
		lifetime = time.Duration(maxLifetimeSeconds)*time.Second + autoDeleteSlack
	}
	backstop := &autoDelete{DateThreshold: a.now().UTC().Add(lifetime).Format(time.RFC3339)}
	if held.HourlyPrice > 0 {
		spendUSD := float64(held.HourlyPrice) / 100.0 * lifetime.Hours()
		backstop.SpendThreshold = strconv.FormatFloat(spendUSD, 'f', 2, 64)
	}
	return backstop
}

// ownershipTags is what makes an instance findable as one Rental's machine.
// Every tag here is a field capability.OwnedCapacity carries, because the
// listing is the only place a reconciler can read them back from.
func ownershipTags(command capability.ProvisionCommand) []string {
	return []string{
		tagRental + "=" + command.RentalID,
		tagGeneration + "=" + strconv.FormatUint(command.Generation, 10),
		tagOwnershipToken + "=" + command.OwnershipToken,
	}
}

func generationTag(tags []string) uint64 {
	raw, tagged := tagValue(tags, tagGeneration)
	if !tagged {
		return 0
	}
	generation, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0
	}
	return generation
}

// capacityState maps Shadeform's VM lifecycle onto the states the contract
// negotiates. "active" means the machine is up and says nothing about whether an
// agent enrolled on it, which is the node registry's answer.
func capacityState(status string) capability.CapacityState {
	switch strings.ToLower(status) {
	case "active":
		return capability.CapacityStateActive
	case "creating", "pending_provider", "pending":
		return capability.CapacityStateStarting
	case "deleting", "deleted":
		return capability.CapacityStateTerminated
	default: // "error", or a status this adapter has not seen
		return capability.CapacityStateUnknown
	}
}
