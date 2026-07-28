package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/benngarcia/mercator/internal/capability"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
	"github.com/benngarcia/mercator/internal/node"
)

// Capacity is the control plane's reach into the capacity lease: allocating a
// machine, reading what the provider says about it, destroying it, and asking a
// connection what it holds. It is a seam of its own rather than four more
// methods on Adapter, because Adapter is an execution: a one-shot product
// Mercator starts and cannot hold, whose release ends a workload. ADR 0005 is
// why a terminate that destroys a machine may never be filed under it.
type Capacity interface {
	ProvisionCapacity(ctx context.Context, command capability.ProvisionCommand) (capability.CapacityReceipt, error)
	ObserveCapacity(ctx context.Context, ref capability.CapacityRef) (capability.CapacityObservation, error)
	TerminateCapacity(ctx context.Context, command capability.CapacityCommand) (capability.CapacityReceipt, error)
	ListOwnedCapacity(ctx context.Context, query capability.OwnershipQuery) ([]capability.OwnedCapacity, error)
}

// Inviter is the node registry as provisioning needs it: the identity a fresh
// machine will enrol under, the short-lived material it redeems, and the answer
// to whether its agent has opened a session.
//
// Invite and Reinvite are separate because they answer different questions
// about one identity. The first reserves a node nothing has filled yet; the
// second mints a fresh token for a node that already exists, which is what an
// attempt Mercator lost the answer to comes back through. Nothing here ever
// hands back a credential Mercator wrote down: the token is minted at the moment
// it is given to a provider and is never in the Run's record.
type Inviter interface {
	Invite(ctx context.Context, invitation node.Invitation) (capability.NodeBootstrap, error)
	Reinvite(ctx context.Context, workspaceID, nodeID string) (capability.NodeBootstrap, error)
	// EnrolledAt is when the agent opened its session, and the zero time while
	// none has. The moment is asked for rather than a yes because the registry is
	// the only holder of it: the agent calls in, so the arrival is dated where it
	// lands, and a control plane that dated it from its own next look would record
	// its polling cadence as the machine's own spend.
	EnrolledAt(ctx context.Context, ref capability.NodeRef) (time.Time, error)
}

// WithCapacity supplies the capacity lease seam, and WithInviter the node
// registry provisioning invites machines through. A deployment with neither
// cannot act on a placement that chose to provision, and says so where it would
// have had to allocate a machine rather than launching into the ephemeral seam
// as though a Rental were a one-shot product.
func WithCapacity(capacity Capacity) Option {
	return func(o *Orchestrator) { o.capacity = capacity }
}

func WithInviter(inviter Inviter) Option {
	return func(o *Orchestrator) { o.inviter = inviter }
}

// enrolmentPatience is how long Mercator goes on expecting a node agent on a
// machine whose publisher named no patience of its own. It is Mercator's policy
// rather than a provider fact, and there is deliberately no value meaning "wait
// for ever": a machine nobody gives up on bills until somebody notices.
const enrolmentPatience = 15 * time.Minute

const (
	// EventCapacityRequested is the machine a placement promised to allocate,
	// written down with the decision that promised it and before any provider is
	// asked for anything. It is what makes a machine allocated by a command whose
	// response never came back still a machine this Run can be reconciled against.
	EventCapacityRequested = "compute.run.capacity_requested.v1"
	// EventCapacityAccepted is a provider having allocated the machine, or an
	// owned-capacity sweep having found the machine an earlier command allocated.
	EventCapacityAccepted = "compute.run.capacity_accepted.v1"
	// EventCapacityStageObserved is one provisioning stage completing, with the
	// seconds between the moment the stage before it finished and the moment the
	// authority that owns this one says it did. Where an authority will not date
	// its own transition the record says so and the seconds are an upper bound.
	// The three are recorded separately because they are three different
	// failures: a provider that cannot allocate, a machine that cannot boot, and
	// an agent that never arrives.
	EventCapacityStageObserved = "compute.run.capacity_stage_observed.v1"
	// EventCapacityReclaimed is Mercator having stopped waiting for the agent and
	// destroyed the machine. It is not a cleanup: a cleanup ends a Run, and this
	// is a Run that never started, whose capacity was handed back so the fleet
	// could be asked again.
	EventCapacityReclaimed = "compute.run.capacity_reclaimed.v1"
)

// capacityReclaimEnrolmentDeadline is the policy that decided a reclamation,
// named in the record so a reader is told which rule fired rather than left to
// infer it from two timestamps.
const capacityReclaimEnrolmentDeadline = "ENROLMENT_DEADLINE_EXCEEDED"

// capacityRequestedData is the machine this Run's placement committed to
// allocating: which lease it will be, which node will enrol on it, what the
// listing charges, and when Mercator stops waiting for the agent.
//
// It is built where the offer is in hand, which is the placement itself, so
// nothing downstream has to reconstruct a listing from a decision record that
// was never meant to carry one. Neither identity is minted here: the Rental is
// the one the Booking was reserved on, and the node is derived from it, so both
// survive a control plane that stops between deciding and allocating.
type capacityRequestedData struct {
	RentalID        string `json:"rental_id"`
	NodeID          string `json:"node_id"`
	Generation      uint64 `json:"generation"`
	ConnectionID    string `json:"connection_id"`
	OfferSnapshotID string `json:"offer_snapshot_id"`
	NativeRef       string `json:"native_ref"`
	// RatePerHourUSD is what the listing said holding this machine costs, carried
	// into the node's invitation so the machine is not born unpriced: placement
	// weighs an enrolled node against fresh capacity by its price, and a node
	// invited without one is refused rather than treated as free.
	RatePerHourUSD float64 `json:"rate_per_hour_usd"`
	// EnrolmentDeadlineAt is the moment Mercator stops expecting the agent, from
	// the listing's own patience where it stated one and Mercator's where it did
	// not.
	EnrolmentDeadlineAt time.Time `json:"enrolment_deadline_at"`
	RequestedAt         time.Time `json:"requested_at"`
}

// capacityPlan is what a placement that chose to provision commits to, or
// nothing at all for a placement that chose a machine that already exists.
func capacityPlan(decision domain.BookingDecision, offer domain.OfferSnapshot, now time.Time) *capacityRequestedData {
	if decision.Booking == nil || !provisionsFreshCapacity(decision) {
		return nil
	}
	return &capacityRequestedData{
		RentalID:            decision.Booking.RentalID,
		NodeID:              nodeIdentityFor(decision.Booking.RentalID),
		Generation:          1,
		ConnectionID:        offer.ConnectionID,
		OfferSnapshotID:     offer.ID,
		NativeRef:           offer.NativeRef,
		RatePerHourUSD:      offer.Pricing.RatePerSecondUSD * 3600,
		EnrolmentDeadlineAt: offer.Bootstrap.EnrolmentDeadline(now, enrolmentPatience),
		RequestedAt:         now,
	}
}

// provisionsFreshCapacity reads what the decision committed to about the offer
// it chose. The disposition is the commitment, and it is what the corpus states
// and an operator reads, so it is what the act is dispatched on rather than the
// offer's kind: an ephemeral one-shot execution is also a machine that does not
// exist yet, and it gets no Rental and no agent.
func provisionsFreshCapacity(decision domain.BookingDecision) bool {
	for _, candidate := range decision.Candidates {
		if candidate.OfferSnapshotID != decision.SelectedOfferSnapshotID {
			continue
		}
		return candidate.Disposition == domain.CandidateDispositionProvision
	}
	return false
}

// nodeIdentityFor is the node a machine allocated for one Rental generation will
// enrol under. It is derived from the lease rather than minted, because the
// derivation is what lets an allocation Mercator lost the answer to be asked
// about again under the identity it already had. A second generation of the same
// lease invites a second machine and gets its own identity from its own Rental.
func nodeIdentityFor(rentalID string) string {
	return "nod_" + strings.TrimPrefix(rentalID, "rnt_")
}

func (data capacityRequestedData) ref(workspaceID, ownershipToken string) capability.CapacityRef {
	return capability.CapacityRef{
		WorkspaceID:    workspaceID,
		ConnectionID:   data.ConnectionID,
		RentalID:       data.RentalID,
		NativeRef:      data.NativeRef,
		OwnershipToken: ownershipToken,
	}
}

func (data capacityRequestedData) nodeRef(workspaceID string) capability.NodeRef {
	return capability.NodeRef{
		WorkspaceID: workspaceID,
		NodeID:      data.NodeID,
		RentalID:    data.RentalID,
		Generation:  data.Generation,
	}
}

// capacityAcceptedData is what the provider answered, and how Mercator came to
// believe it. Adopted marks a machine found through the owned-capacity sweep
// rather than returned by the command that allocated it, which is the only way
// a lost response ends without a second machine billing beside the first.
type capacityAcceptedData struct {
	NativeRef  string                   `json:"native_ref"`
	State      capability.CapacityState `json:"state"`
	AcceptedAt time.Time                `json:"accepted_at"`
	Duplicate  bool                     `json:"duplicate,omitempty"`
	Adopted    bool                     `json:"adopted,omitempty"`
}

// capacityStageObservedData is one provisioning stage with its duration: when it
// finished, and the seconds between that and the stage before it. Nothing here is
// read from the estimate the offer published, which is the claim these
// measurements exist to be judged against.
//
// FinishedAt is the authority's own moment where it dates its transitions, and
// the moment Mercator looked where it does not. Bounded is which of the two this
// record holds, and it is written rather than left to be inferred because the
// difference decides what the number may be used for: a calibration that trained
// on bounded records would be learning the reconcile cadence.
type capacityStageObservedData struct {
	Stage      domain.LaunchStage `json:"stage"`
	FinishedAt time.Time          `json:"finished_at"`
	Seconds    float64            `json:"seconds"`
	Bounded    bool               `json:"bounded,omitempty"`
}

// capacityReclaimedData is Mercator having given a machine back, and the rule
// that decided it. The policy is named rather than left to be derived, because
// an operator reading a destroyed machine is asking which bound fired.
type capacityReclaimedData struct {
	RentalID        string             `json:"rental_id"`
	NodeID          string             `json:"node_id"`
	OfferSnapshotID string             `json:"offer_snapshot_id"`
	Policy          string             `json:"policy"`
	DeadlineSeconds float64            `json:"deadline_seconds"`
	WaitedSeconds   float64            `json:"waited_seconds"`
	Disposition     domain.Disposition `json:"disposition"`
}

// stepBuildCapacity is one turn of building the machine a placement promised.
// Each turn is one external act and one append, in the order a machine comes
// into existence: the allocation, then what the provider and the registry say
// about it, then giving it back if neither ever says the agent came.
func (o *Orchestrator) stepBuildCapacity(ctx context.Context, workspaceID, runID string, version uint64, state runState) (bool, error) {
	if o.capacity == nil || o.inviter == nil {
		return false, fmt.Errorf("orchestrator: Run %q was placed on capacity to provision and this deployment has no capacity seam", runID)
	}
	if state.capacityAccepted == nil {
		return o.allocateCapacity(ctx, workspaceID, runID, version, state)
	}
	return o.watchCapacityArrive(ctx, workspaceID, runID, version, state)
}

// allocateCapacity asks the provider for the machine, and asks first whether
// this connection is already holding one for this Rental.
//
// The sweep comes first on every attempt rather than only after a failure,
// because a command whose response was lost and a command that was never sent
// leave Mercator's own record saying exactly the same thing. The Rental identity
// travels to the provider precisely so this question has an answer, and asking
// it is what makes a lost response cost one read rather than a second machine
// nobody will ever come for.
func (o *Orchestrator) allocateCapacity(ctx context.Context, workspaceID, runID string, version uint64, state runState) (bool, error) {
	requested := *state.capacity
	owned, held, err := o.capacityAlreadyHeld(ctx, workspaceID, requested.RentalID)
	if err != nil {
		return false, err
	}
	if held {
		return true, o.recordCapacityAccepted(ctx, workspaceID, runID, version, capacityAcceptedData{
			NativeRef:  owned.NativeRef,
			State:      owned.State,
			AcceptedAt: owned.CreatedAt.UTC(),
			Adopted:    true,
		})
	}
	bootstrap, err := o.bootstrapFor(ctx, workspaceID, requested)
	if err != nil {
		return false, err
	}
	command := capability.ProvisionCommand{
		WorkspaceID:     workspaceID,
		ConnectionID:    requested.ConnectionID,
		OperationKey:    "provision_" + requested.RentalID,
		RentalID:        requested.RentalID,
		Generation:      requested.Generation,
		OwnershipToken:  state.launchIntent.OwnershipToken,
		OfferSnapshotID: requested.OfferSnapshotID,
		NativeRef:       requested.NativeRef,
		Resources:       state.requested.Workload.Spec.Resources,
		// Handed over exactly as the registry minted it. A CapacityProvider
		// delivers it through whatever mechanism it has and never interprets it.
		Bootstrap: bootstrap,
	}
	hash, hashErr := domain.CanonicalHash(command)
	if hashErr != nil {
		return false, hashErr
	}
	command.RequestHash = hash
	receipt, err := o.capacity.ProvisionCapacity(ctx, command)
	if err != nil {
		// Nothing is written down. The next advance sweeps this connection's owned
		// capacity and either finds the machine this command allocated or asks
		// again under the same operation key.
		return false, fmt.Errorf("orchestrator: provision capacity for Rental %q: %w", requested.RentalID, err)
	}
	return true, o.recordCapacityAccepted(ctx, workspaceID, runID, version, capacityAcceptedData{
		NativeRef:  receipt.NativeRef,
		State:      receipt.State,
		AcceptedAt: receipt.AcceptedAt.UTC(),
		Duplicate:  receipt.Duplicate,
	})
}

// bootstrapFor mints the material this machine redeems for a session. The
// identity is asked for by name, so an attempt whose answer Mercator lost comes
// back to the node it was always going to be: an identity that already exists is
// reinvited rather than duplicated, which is what keeps one Rental generation to
// one node.
func (o *Orchestrator) bootstrapFor(ctx context.Context, workspaceID string, requested capacityRequestedData) (capability.NodeBootstrap, error) {
	bootstrap, err := o.inviter.Invite(ctx, node.Invitation{
		WorkspaceID: workspaceID,
		NodeID:      requested.NodeID,
		RentalID:    requested.RentalID,
		Generation:  requested.Generation,
		// What the listing said holding this machine costs. A node invited without
		// it is refused, because placement weighs an enrolled node against fresh
		// capacity by its price and has no reading for silence.
		ShadowPriceUSDPerHour: requested.RatePerHourUSD,
	})
	if err == nil {
		return bootstrap, nil
	}
	if !errors.Is(err, node.ErrIdentityExists) {
		return capability.NodeBootstrap{}, fmt.Errorf("orchestrator: invite node %q: %w", requested.NodeID, err)
	}
	bootstrap, err = o.inviter.Reinvite(ctx, workspaceID, requested.NodeID)
	if err != nil {
		return capability.NodeBootstrap{}, fmt.Errorf("orchestrator: reinvite node %q: %w", requested.NodeID, err)
	}
	return bootstrap, nil
}

func (o *Orchestrator) capacityAlreadyHeld(ctx context.Context, workspaceID, rentalID string) (capability.OwnedCapacity, bool, error) {
	owned, err := o.capacity.ListOwnedCapacity(ctx, capability.OwnershipQuery{WorkspaceID: workspaceID})
	if err != nil {
		return capability.OwnedCapacity{}, false, fmt.Errorf("orchestrator: list owned capacity: %w", err)
	}
	for _, held := range owned {
		if held.RentalID == rentalID && !held.State.Terminal() {
			return held, true, nil
		}
	}
	return capability.OwnedCapacity{}, false, nil
}

func (o *Orchestrator) recordCapacityAccepted(ctx context.Context, workspaceID, runID string, version uint64, accepted capacityAcceptedData) error {
	return o.appendEvents(ctx, workspaceID, runID, version, "advance:capacity_accepted:"+accepted.NativeRef, []eventlog.NewEvent{
		mustEvent(runID, "capacity_accepted_"+accepted.NativeRef, EventCapacityAccepted, accepted, o.now()),
	})
}

// watchCapacityArrive reads the two authorities on a machine being built and
// records what each established since the last look. The provider owns
// allocation and boot; the registry owns whether an agent opened a session, and
// nothing else can answer that. A machine that has reached neither by the
// deadline is handed back.
func (o *Orchestrator) watchCapacityArrive(ctx context.Context, workspaceID, runID string, version uint64, state runState) (bool, error) {
	requested := *state.capacity
	observation, err := o.capacity.ObserveCapacity(ctx, requested.ref(workspaceID, state.launchIntent.OwnershipToken))
	if err != nil {
		return false, fmt.Errorf("orchestrator: observe capacity for Rental %q: %w", requested.RentalID, err)
	}
	enrolledAt, err := o.inviter.EnrolledAt(ctx, requested.nodeRef(workspaceID))
	if err != nil {
		return false, fmt.Errorf("orchestrator: read whether node %q enrolled: %w", requested.NodeID, err)
	}
	now := o.now().UTC()
	events := capacityStageEvents(runID, state, observation, enrolledAt, now)
	switch {
	case len(events) > 0:
		return true, o.appendEvents(ctx, workspaceID, runID, version,
			fmt.Sprintf("advance:capacity_stages:%s:%d", requested.RentalID, len(state.capacityStages)), events)
	case !now.Before(requested.EnrolmentDeadlineAt):
		return o.reclaimCapacity(ctx, workspaceID, runID, version, state, now)
	default:
		return false, nil
	}
}

// capacityStageEvents is what the two answers above establish that the record
// does not hold yet, in the order a machine goes through them. Each stage is
// measured from the moment the stage before it finished, so the three add up to
// the whole wait rather than each restating it from the beginning.
//
// A stage finished when the authority that owns it says it did. Only where an
// authority will not date its own transition does the moment Mercator looked
// stand in, and such a record is marked as the bound it is: the machine finished
// somewhere between this look and the last one, and writing the whole interval
// down as a duration would publish the reconcile cadence as a property of the
// machine for a calibration to learn.
func capacityStageEvents(runID string, state runState, observation capability.CapacityObservation, enrolledAt, now time.Time) []eventlog.NewEvent {
	finished := provisioningStagesFinished(observation, enrolledAt)
	since := state.capacity.RequestedAt
	if !state.lastCapacityStageAt.IsZero() {
		since = state.lastCapacityStageAt
	}
	var events []eventlog.NewEvent
	for _, stage := range domain.ProvisioningStages {
		at, reached := finished[stage]
		if state.capacityStages[stage] || !reached {
			continue
		}
		// A moment outside the interval this look is about dates nothing usable:
		// before the last stage finished it would measure a negative duration, and
		// after now it is a transition this look cannot have established. Two stages
		// found complete in one look leave the second here, sharing the first's
		// moment and measuring zero, because splitting an interval nothing observed
		// would be the control plane inventing a boundary.
		dated := !at.IsZero() && !at.Before(since) && !at.After(now)
		if !dated {
			at = now
		}
		// Identified by the machine as well as the stage, because a Run whose first
		// machine was reclaimed goes through all three again on the next one.
		events = append(events, mustEvent(runID, "capacity_stage_"+state.capacity.RentalID+"_"+string(stage), EventCapacityStageObserved, capacityStageObservedData{
			Stage:      stage,
			FinishedAt: at,
			Seconds:    at.Sub(since).Seconds(),
			Bounded:    !dated,
		}, now))
		since = at
	}
	return events
}

// provisioningStagesFinished is when each stage a machine has got through
// finished, according to the authority that owns it: the provider for the
// allocation and the boot, the node registry for the agent's session. A stage
// the machine has not reached is absent, and a stage its authority reports
// without dating carries the zero time.
func provisioningStagesFinished(observation capability.CapacityObservation, enrolledAt time.Time) map[domain.LaunchStage]time.Time {
	finished := map[domain.LaunchStage]time.Time{}
	switch observation.State {
	case capability.CapacityStateStarting:
		// Building has begun, so the allocation is over and this is when it ended.
		finished[domain.StageAcquisition] = observation.StateSince
	case capability.CapacityStateActive:
		// The machine is up, which dates the boot. The acquisition ended somewhere
		// before that and the provider is no longer saying where, so it is left
		// undated for the caller to record as a bound.
		finished[domain.StageAcquisition] = time.Time{}
		finished[domain.StageBoot] = observation.StateSince
	}
	if !enrolledAt.IsZero() {
		finished[domain.StageAgentReady] = enrolledAt
	}
	return finished
}

// reclaimCapacity destroys a machine whose agent never came and releases the
// Booking that was waiting for it, in that order and in one commit. Handing the
// capacity back before the work moves is the whole of the rule: a control plane
// that runs the work elsewhere and leaves the machine to the provider's own
// backstop is one an operator pays twice.
func (o *Orchestrator) reclaimCapacity(ctx context.Context, workspaceID, runID string, version uint64, state runState, now time.Time) (bool, error) {
	requested := *state.capacity
	command := capability.CapacityCommand{
		CapacityRef:  requested.ref(workspaceID, state.launchIntent.OwnershipToken),
		OperationKey: "reclaim_" + requested.RentalID,
		Generation:   requested.Generation,
	}
	hash, err := domain.CanonicalHash(command)
	if err != nil {
		return false, err
	}
	command.RequestHash = hash
	if _, err := o.capacity.TerminateCapacity(ctx, command); err != nil {
		// The machine is still billing, so this is not a state to move past. The
		// next advance asks again under the same operation key.
		return false, fmt.Errorf("orchestrator: reclaim capacity for Rental %q: %w", requested.RentalID, err)
	}
	reclaimed := capacityReclaimedData{
		RentalID:        requested.RentalID,
		NodeID:          requested.NodeID,
		OfferSnapshotID: requested.OfferSnapshotID,
		Policy:          capacityReclaimEnrolmentDeadline,
		DeadlineSeconds: requested.EnrolmentDeadlineAt.Sub(requested.RequestedAt).Seconds(),
		WaitedSeconds:   now.Sub(requested.RequestedAt).Seconds(),
		Disposition:     domain.DispositionTerminate,
	}
	return true, o.completeBookingAndAppend(ctx, workspaceID, runID, version, state, "advance:capacity_reclaimed:"+requested.RentalID, []eventlog.NewEvent{
		mustEvent(runID, "capacity_reclaimed_"+requested.RentalID, EventCapacityReclaimed, reclaimed, o.now()),
	})
}
