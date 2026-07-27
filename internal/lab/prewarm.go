package lab

import (
	"context"
	"strings"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/scenario"
)

// prefetchKeyPrefix marks the transfers this world is moving speculatively.
// They share the pending-transfer machinery with the pulls a launch dispatches,
// because they are the same act against the same disk and the same link: the
// only difference is who is waiting, and that is what the key says.
const prefetchKeyPrefix = "prewarm/"

// prefetchKey names one piece of content on one machine. Two Runs wanting the
// same image on the same host want one transfer, so the Run is deliberately not
// part of it: a prefetch is content arriving somewhere, not a Run's private
// copy.
func prefetchKey(offerID, content string) string {
	return prefetchKeyPrefix + offerID + "/" + content
}

// Prepare is the machine being told what Mercator wants it holding for work it
// has not admitted. It is a desired set rather than an order, and everything
// that follows from that is deliberate: content absent from Wanted is content
// Mercator has stopped asking for, and this world stops moving it rather than
// spending a machine's disk and bandwidth on a Run that will never run.
//
// The withdrawal happens before anything new starts. A control plane that
// started the next prefetch first would have both in flight for an instant,
// which is exactly the moment a real launch would find the machine busy.
func (world *simulatedWorld) Prepare(_ context.Context, request adapter.PrepareRequest) (adapter.PrepareReceipt, error) {
	world.mu.Lock()
	defer world.mu.Unlock()
	receipt := adapter.PrepareReceipt{OperationKey: request.OperationKey, AcceptedAt: world.now}
	wanted := make(map[string]bool, len(request.Wanted))
	for _, item := range request.Wanted {
		wanted[prefetchKey(item.OfferSnapshotID, item.Content())] = true
	}
	receipt.Abandoned = world.abandonUnwantedPrefetches(wanted)
	for _, item := range request.Wanted {
		outcome, started := world.startPrefetch(item)
		switch outcome {
		case prefetchStarted:
			receipt.Started = append(receipt.Started, started)
		case prefetchUnsupported:
			receipt.Unsupported = append(receipt.Unsupported, started)
		case prefetchRefused:
			receipt.Refused = append(receipt.Refused, item.Content())
		case prefetchDuplicate:
			receipt.Duplicate = true
		}
	}
	world.publishObservations()
	return receipt, nil
}

type prefetchOutcome uint8

const (
	prefetchStarted prefetchOutcome = iota + 1
	prefetchDuplicate
	prefetchUnsupported
	prefetchRefused
)

// startPrefetch begins moving one piece of content onto one machine, or says
// why it did not. A machine that keeps nothing is refused rather than served:
// preparing a one-shot product or a machine that does not exist yet would be
// warmth with nowhere to survive, which is the same rule every other content
// path in this world holds.
//
// A machine that turns the fetch away leaves the identity askable. That is the
// whole difference between a refusal and either of the other two answers: nothing
// is moving, nothing was kept, and the same content asked for again is a first
// ask rather than a redelivery. So the identity is remembered only once a fetch
// this world really took on, which is the same rule the operation store holds.
func (world *simulatedWorld) startPrefetch(item adapter.PrepareItem) (prefetchOutcome, string) {
	operation := prefetchOperationID(item)
	if world.prepared[operation] {
		world.recordPrefetchEffect(item, operation, EffectCommandDuplicate, map[string]any{"already_requested": true})
		return prefetchDuplicate, operation
	}
	state, exists := world.truth[item.OfferSnapshotID]
	if !exists || !state.offer.KeepsWhatItRuns() {
		world.recordPrefetchEffect(item, operation, EffectCommandRejected, map[string]any{
			"reason": "this machine keeps nothing, so content prepared on it could not outlive one workload",
		})
		return prefetchUnsupported, operation
	}
	if fault := world.matchOperationFault(prefetchOperation(item), item.RunID, 0); fault != nil && fault.Action == scenario.FaultRejectCommand {
		world.recordPrefetchEffectWithFault(item, operation, EffectCommandRejected, map[string]any{
			"reason": "this machine could not fetch the content it was asked for",
		}, fault.ID)
		return prefetchRefused, operation
	}
	world.prepared[operation] = true
	if item.Kind == adapter.PrepareImage {
		return prefetchStarted, world.prefetchImage(item, operation, state)
	}
	return prefetchStarted, world.prefetchArtifact(item, operation)
}

// prefetchImage moves the layers this machine is missing. A machine already
// holding the image whole is ready with nothing to do, which is recorded as
// such rather than as a transfer of no bytes: a prefetch that never converges
// and a prefetch with nothing to move are different answers.
func (world *simulatedWorld) prefetchImage(item adapter.PrepareItem, operation string, state hostState) string {
	layers := world.images[item.Image].Layers
	fetchedLayers, bytes := state.missing(item.Image, layers)
	fetched := fetchedNames(item.Image, state.heldImages[domain.ReferenceDigest(item.Image)], fetchedLayers)
	if len(fetched) == 0 {
		world.recordPrefetchEffect(item, operation, EffectCommandAccepted, map[string]any{"ready": true, "fetched_bytes": 0})
		return operation
	}
	key := prefetchKey(item.OfferSnapshotID, item.Content())
	completesAt := world.now.Add(transferDuration(bytes, world.linkMbps(item.OfferSnapshotID, domain.NetworkScopeRegistry)))
	world.pulls = append(world.pulls, pendingPull{
		offerID:     item.OfferSnapshotID,
		runID:       item.RunID,
		launchKey:   key,
		source:      "prewarm",
		image:       item.Image,
		layers:      layers,
		fetched:     fetched,
		bytes:       bytes,
		completesAt: completesAt,
	})
	world.recordPrefetchEffect(item, operation, EffectCommandAccepted, map[string]any{
		"ready":           false,
		"fetched_bytes":   bytes,
		"fetched_digests": fetched,
		"completes_at":    completesAt,
	})
	world.settlePulls()
	return operation
}

// prefetchArtifact reads one immutable version out of the object store onto the
// machine. The copy that lands is checked against the catalog on arrival, which
// is what makes it worth reading later: an unchecked copy costs a consumer
// exactly what no copy costs.
func (world *simulatedWorld) prefetchArtifact(item adapter.PrepareItem, operation string) string {
	if replica, held := world.replicas[item.ArtifactID][item.OfferSnapshotID]; held && replica.State.Usable() {
		world.recordPrefetchEffect(item, operation, EffectCommandAccepted, map[string]any{"ready": true, "fetched_bytes": 0})
		return operation
	}
	version, _ := world.store.entry(item.ArtifactID)
	key := prefetchKey(item.OfferSnapshotID, item.Content())
	completesAt := world.now.Add(world.store.transferDuration(item.ArtifactID, world.linkMbps(item.OfferSnapshotID, domain.NetworkScopeObjectStore)))
	world.replicating = append(world.replicating, pendingReplica{
		offerID:     item.OfferSnapshotID,
		runID:       item.RunID,
		launchKey:   key,
		source:      "prewarm",
		artifactID:  item.ArtifactID,
		bytes:       version.SizeBytes,
		completesAt: completesAt,
	})
	world.recordPrefetchEffect(item, operation, EffectCommandAccepted, map[string]any{
		"ready":         false,
		"fetched_bytes": version.SizeBytes,
		"completes_at":  completesAt,
	})
	world.settleReplicas()
	return operation
}

// abandonUnwantedPrefetches stops every speculative transfer this desired set no
// longer names. The room goes back to the machine at once, which is the whole
// point: a queued Run that was cancelled must stop costing the host it was
// queued on, and nothing else in this world can give that room back.
func (world *simulatedWorld) abandonUnwantedPrefetches(wanted map[string]bool) []string {
	var abandoned []string
	for _, pull := range world.pulls {
		if !strings.HasPrefix(pull.launchKey, prefetchKeyPrefix) || wanted[pull.launchKey] {
			continue
		}
		abandoned = append(abandoned, pull.launchKey)
		world.recordAbandonedPrefetch(pull.launchKey, pull.offerID, pull.runID, pull.bytes)
	}
	for _, fetch := range world.replicating {
		if !strings.HasPrefix(fetch.launchKey, prefetchKeyPrefix) || wanted[fetch.launchKey] {
			continue
		}
		abandoned = append(abandoned, fetch.launchKey)
		world.recordAbandonedPrefetch(fetch.launchKey, fetch.offerID, fetch.runID, fetch.bytes)
	}
	for _, key := range abandoned {
		world.cancelTransfers(key)
	}
	return abandoned
}

// recordAbandonedPrefetch names the content by the same string the request for
// it did, taken back out of the key both were built from. A withdrawal filed
// under another name for the same bytes would leave the transfer looking like it
// is still running.
func (world *simulatedWorld) recordAbandonedPrefetch(key, offerID, runID string, bytes int64) {
	content := strings.TrimPrefix(key, prefetchKeyPrefix+offerID+"/")
	world.recordEffect(
		OperationNodePrepareAbandoned,
		strings.Replace(key, prefetchKeyPrefix, "prepare-abandoned/", 1),
		EffectCommandAccepted,
		EffectResponseDelivered,
		runID,
		key,
		"",
		map[string]any{"offer_id": offerID, "content": content, "prefetch": key},
		map[string]any{"released_bytes": bytes},
		"",
	)
}

// prefetchOperationID is the identity one preparation answers to. It carries the
// machine and the content and never the Run, because two Runs wanting one image
// on one host is one preparation: naming the Run would have the second request
// start a second transfer of bytes already on their way.
func prefetchOperationID(item adapter.PrepareItem) string {
	if item.Kind == adapter.PrepareImage {
		return "prepare-image/" + item.OfferSnapshotID + "/" + item.Content()
	}
	return "prepare-artifact/" + item.OfferSnapshotID + "/" + item.Content()
}

func (world *simulatedWorld) recordPrefetchEffect(item adapter.PrepareItem, operation string, command EffectCommand, consequence map[string]any) {
	world.recordPrefetchEffectWithFault(item, operation, command, consequence, "")
}

func (world *simulatedWorld) recordPrefetchEffectWithFault(
	item adapter.PrepareItem,
	operation string,
	command EffectCommand,
	consequence map[string]any,
	faultID string,
) {
	world.recordEffect(
		prefetchOperation(item),
		operation,
		command,
		EffectResponseDelivered,
		item.RunID,
		prefetchKey(item.OfferSnapshotID, item.Content()),
		"",
		map[string]any{
			"offer_id": item.OfferSnapshotID,
			"content":  item.Content(),
			"run_id":   item.RunID,
			// Where the bytes come from, which for an Artifact is the durable
			// location the control plane minted a read of. No material of
			// Mercator's is in it, and nothing here ever carries one.
			"source": item.Source,
		},
		consequence,
		faultID,
	)
}

func prefetchOperation(item adapter.PrepareItem) string {
	if item.Kind == adapter.PrepareImage {
		return OperationNodePrepareImage
	}
	return OperationNodePrepareArtifact
}
