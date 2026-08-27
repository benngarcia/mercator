package janitor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
)

// OrphanPolicy names the rule this control plane converges capacity it does not
// recognise by. It is recorded with every decision rather than left to be
// inferred from the action taken: an operator reading that a machine was
// destroyed has to be able to read the rule that destroyed it, and a
// reconciliation whose only outcome is a failure cannot be that rule.
//
// The rule is what Mercator's own record says about the work the capacity was
// allocated for. A launch records how the capacity is given back, so capacity
// whose record says the machine survives being reclaimed is adopted, and
// everything else stops existing. Nothing is left running silently, which is the
// state a sweep that only knew how to skip left behind.
const OrphanPolicy = "adopt_capacity_mercator_recorded_terminate_the_rest"

// OrphanOutcome is what the policy decided about one piece of capacity. There
// are two, and they are the two an operator cares about: the machine goes back
// into the fleet, or it stops existing.
type OrphanOutcome string

const (
	// OrphanAdopted is capacity taken back under management. Its slot is released
	// and the machine stays in the fleet, which is only possible because
	// Mercator's own record of the launch says this capacity outlives the work on
	// it.
	OrphanAdopted OrphanOutcome = "adopted"
	// OrphanTerminated is capacity that stops existing. Either Mercator's record
	// says this capacity was never meant to outlive its workload, or there is no
	// record of it at all and nothing can be bound to it, and a machine nothing
	// can ever use is a machine that would otherwise bill forever.
	OrphanTerminated OrphanOutcome = "terminated"
)

// The reasons one outcome or the other applied. They are stated apart from the
// outcome because two very different findings destroy a machine, and an operator
// reading the record needs to know which: capacity Mercator promised to destroy,
// and capacity Mercator cannot account for at all.
const (
	reasonRecordedRelease     = "recorded_disposition_release"
	reasonRecordedTerminate   = "recorded_disposition_terminate"
	reasonUnattributed        = "unattributed"
	reasonNoRecordedRun       = "no_recorded_run"
	reasonNoLaunchAccountsFor = "no_recorded_launch_accounts_for_it"
	reasonClosedWithoutAsking = "closed_without_a_cleanup_request"
)

// EventOrphanConverged is the record of one policy decision about one piece of
// capacity Mercator found and did not recognise.
const EventOrphanConverged = "compute.capacity.orphan_converged.v1"

type Adapter interface {
	ListOwned(ctx context.Context, req adapter.OwnershipQuery) ([]adapter.OwnedExternalObject, error)
	Release(ctx context.Context, req adapter.ReleaseRequest) (adapter.ReleaseReceipt, error)
	Terminate(ctx context.Context, req adapter.TerminateRequest) (adapter.TerminateReceipt, error)
}

type Janitor struct {
	adapter Adapter
	log     eventlog.EventLog
}

// Result is what one sweep found and what the policy did with it. Adopted and
// Terminated are counted apart because they are opposite consequences, and a
// single total would let a sweep that destroyed a whole fleet read like a sweep
// that kept it.
type Result struct {
	Found      int `json:"found"`
	Adopted    int `json:"adopted"`
	Terminated int `json:"terminated"`
}

// Converged is how many owned objects this sweep decided about.
func (result Result) Converged() int { return result.Adopted + result.Terminated }

type Option func(*Janitor)

func WithEventLog(log eventlog.EventLog) Option {
	return func(j *Janitor) {
		j.log = log
	}
}

func New(adapter Adapter, options ...Option) *Janitor {
	j := &Janitor{adapter: adapter}
	for _, option := range options {
		option(j)
	}
	return j
}

// Sweep converges every piece of capacity this broker owns that Mercator no
// longer holds live work for. Each one is decided by the stated policy, acted on,
// and written down naming the policy that decided it.
func (j *Janitor) Sweep(ctx context.Context) (Result, error) {
	if j.adapter == nil {
		return Result{}, fmt.Errorf("janitor: adapter is required")
	}
	if j.log == nil {
		return Result{}, fmt.Errorf("janitor: event log is required")
	}
	owned, err := j.adapter.ListOwned(ctx, adapter.OwnershipQuery{})
	if err != nil {
		return Result{}, err
	}
	result := Result{Found: len(owned)}
	for _, object := range owned {
		decision, err := j.converge(ctx, object)
		if err != nil {
			return result, err
		}
		if decision.outcome == "" {
			continue
		}
		if err := j.reclaim(ctx, object, decision.outcome); err != nil {
			return result, err
		}
		if decision.outcome == OrphanAdopted {
			result.Adopted++
			continue
		}
		result.Terminated++
	}
	return result, nil
}

// orphanDecision is one application of the policy: what it decided and why. An
// empty outcome is not a decision at all, which is what live work gets: Mercator
// still holds the Run this capacity is executing, and that Run's own lifecycle
// converges it.
//
// How the provider is asked is deliberately not part of it. Adopting releases a
// slot and terminating destroys a machine, so the outcome already says which
// command carries it, and a decision that also carried the command could state
// one the provider cannot perform.
type orphanDecision struct {
	outcome OrphanOutcome
	reason  string
}

// converge is the decision this sweep will carry out, written down before
// anything acts on it. A decision the record already holds is read back rather
// than made again.
//
// The order is the whole point. Reclaiming is not reversible and a terminated
// machine never appears in a listing again, so a sweep that destroyed one and
// then failed to say why would leave capacity gone with no rule naming what took
// it, and no later sweep could ever repair that: the object it would have to
// explain is no longer there to be found. Deciding first leaves the opposite
// failure, which is a machine still standing under a decision that has not been
// carried out yet, and the next sweep finishes it.
func (j *Janitor) converge(ctx context.Context, object adapter.OwnedExternalObject) (orphanDecision, error) {
	decided, err := j.recordedDecision(ctx, object)
	if err != nil || decided.outcome != "" {
		return decided, err
	}
	decision, err := j.decide(ctx, object)
	if err != nil || decision.outcome == "" {
		return decision, err
	}
	return decision, j.record(ctx, object, decision)
}

// decide applies the policy to one owned object. Nothing here reaches the
// provider: every input is Mercator's own record of the Run the object names.
func (j *Janitor) decide(ctx context.Context, object adapter.OwnedExternalObject) (orphanDecision, error) {
	if object.RunID == "" {
		return terminate(reasonUnattributed), nil
	}
	recorded, err := j.recordedRun(ctx, object)
	if err != nil {
		return orphanDecision{}, err
	}
	return recorded.converge(object)
}

// terminate is capacity that stops existing, for a reason that is not a recorded
// disposition.
func terminate(reason string) orphanDecision {
	return orphanDecision{outcome: OrphanTerminated, reason: reason}
}

// recordedRun is what Mercator's own log says about the Run one owned object
// names: whether there is a record at all, whether it is over, whether anybody
// ever asked for its capacity back, and every launch it made.
//
// The launches are kept as the several they are. A Run that was placed again
// after a launch failed has one recorded launch per attempt, each with the
// disposition its own offer decided, so a Run that ran out of fresh capacity and
// was replaced onto a pool Mercator does not own holds both dispositions at once.
// Keeping only the last of them converged one attempt's capacity by another
// attempt's rule, which either destroys a machine in a pool Mercator does not own
// or leaves a machine Mercator provisioned billing forever.
type recordedRun struct {
	exists       bool
	closed       bool
	cleanupAsked bool
	launches     []recordedLaunch
}

// recordedLaunch is one launch as Mercator wrote it down: the identities it
// minted for the capacity, and how that capacity is handed back.
type recordedLaunch struct {
	attemptID   string
	launchKey   string
	disposition domain.Disposition
}

// took reports whether this launch is the one that took the capacity in hand.
// Capacity carries the identities Mercator minted for the launch that created
// it, and a provider hands back whichever of them it was able to keep, so either
// one naming this launch is this launch.
func (launch recordedLaunch) took(object adapter.OwnedExternalObject) bool {
	if object.AttemptID != "" && object.AttemptID == launch.attemptID {
		return true
	}
	return object.LaunchKey != "" && object.LaunchKey == launch.launchKey
}

// launchTaking is the launch that decides one piece of capacity: the one whose
// identities the capacity carries.
//
// Capacity carrying none of them is still decided by the record when every launch
// this Run made handed capacity back the same way, because then there is nothing
// for the several launches to disagree about. When they disagree, nothing in the
// record says which of them this machine came from, and picking one is picking
// between keeping a machine that must be destroyed and destroying a slot in a
// pool Mercator does not own.
func (recorded recordedRun) launchTaking(object adapter.OwnedExternalObject) (recordedLaunch, bool) {
	for _, launch := range recorded.launches {
		if launch.took(object) {
			return launch, true
		}
	}
	return recorded.agreedLaunch()
}

func (recorded recordedRun) agreedLaunch() (recordedLaunch, bool) {
	if len(recorded.launches) == 0 {
		return recordedLaunch{}, false
	}
	agreed := recorded.launches[0]
	for _, launch := range recorded.launches {
		if launch.disposition != agreed.disposition {
			return recordedLaunch{}, false
		}
	}
	return agreed, true
}

// over reports whether Mercator still holds live work on this capacity. A Run
// that closed and a Run whose capacity was asked back are both over: the
// difference between them is who noticed, and the policy does not turn on that.
func (recorded recordedRun) over() bool { return recorded.cleanupAsked || recorded.closed }

// converge is what this record says to do with one piece of the capacity the Run
// was given. The launch that took that capacity is what decides: it recorded how
// this capacity is handed back, and that is what says whether there is a machine
// left to keep once the workload on it is reclaimed. Reading the cleanup request
// instead would destroy a rented machine every time a Run ended without one,
// which is the ordinary end of a launch that failed after its attempts ran out.
func (recorded recordedRun) converge(object adapter.OwnedExternalObject) (orphanDecision, error) {
	launch, taken := recorded.launchTaking(object)
	switch {
	case !recorded.exists:
		return terminate(reasonNoRecordedRun), nil
	case !recorded.over():
		return orphanDecision{}, nil
	case taken:
		return byRecordedDisposition(launch.disposition)
	case len(recorded.launches) > 0:
		// Capacity of a Run whose launches disagree, carrying the identity of none
		// of them. The record cannot say which launch left this machine standing, so
		// nothing here says it survives being reclaimed.
		return terminate(reasonNoLaunchAccountsFor), nil
	case recorded.cleanupAsked:
		return orphanDecision{}, fmt.Errorf("janitor: cleanup requires a recorded launch, and Run %q recorded none", object.RunID)
	default:
		// A Run that ended with no launch of its own recorded is the case a sweep
		// keyed on the cleanup request alone could only skip, so the machine ran on
		// with nothing left that would ever reclaim it. Nothing says this capacity
		// survives being reclaimed, so nothing can be bound to what is left of it.
		return terminate(reasonClosedWithoutAsking), nil
	}
}

// byRecordedDisposition is the launch's own statement about the capacity it
// took: capacity the record says outlives its workload is adopted, and capacity
// the record says does not stops existing.
//
// In production every object that reaches this rule was launched by a one-shot
// executor, so release is a standing slot going back to a pool Mercator does not
// own and terminate is an allocated product ceasing to exist. A machine held under a
// lease reaches nothing here: Backend.ListOwned answers no workloads for a
// capacity connection, and a reusable launch is placed on a node, which is not a
// connection this sweep enumerates. Machines are converged against Rental
// records by mercator#199 in the Rental's own vocabulary, and until it lands the
// phase 5 change to OfferSnapshot.CleanupDisposition, which sends a provisioned
// reusable placement to release, changes no answer this rule gives.
//
// It changes what the answers have to mean when it does, and adoption is not
// yet the keeping its name promises. Adopting acts by calling adapter.Release,
// and only the local Docker adapter's Release leaves a host standing. Every VM
// adapter implements it as the same instance delete its Terminate performs, so
// adopting a leased machine would record that the machine was kept and then
// destroy it. That is mercator#208, and it is answered before a capacity
// provider joins the reusable lane rather than after.
func byRecordedDisposition(disposition domain.Disposition) (orphanDecision, error) {
	switch disposition {
	case domain.DispositionRelease:
		return orphanDecision{outcome: OrphanAdopted, reason: reasonRecordedRelease}, nil
	case domain.DispositionTerminate:
		return orphanDecision{outcome: OrphanTerminated, reason: reasonRecordedTerminate}, nil
	default:
		return orphanDecision{}, fmt.Errorf("janitor: cleanup requires a valid recorded disposition, got %q", disposition)
	}
}

func (j *Janitor) recordedRun(ctx context.Context, object adapter.OwnedExternalObject) (recordedRun, error) {
	history, err := eventlog.ReadFullStream(ctx, j.log, eventlog.StreamKey{

		Type: "run",
		ID:   object.RunID,
	})
	if err != nil {
		return recordedRun{}, err
	}
	recorded := recordedRun{exists: len(history.Events) > 0}
	for _, event := range history.Events {
		switch event.Type {
		case "compute.run.launch_intent_recorded.v1":
			launch, err := recordedLaunchOf(event)
			if err != nil {
				return recordedRun{}, err
			}
			recorded.launches = append(recorded.launches, launch)
		case "compute.run.cleanup_requested.v1", "compute.run.cleanup_confirmed.v1":
			recorded.cleanupAsked = true
		case "compute.run.closed.v1":
			recorded.closed = true
		}
	}
	return recorded, nil
}

func recordedLaunchOf(event eventlog.StoredEvent) (recordedLaunch, error) {
	payload := event.PrivateData
	if len(payload) == 0 {
		payload = event.Data
	}
	var intent struct {
		AttemptID   string             `json:"attempt_id"`
		LaunchKey   string             `json:"launch_key"`
		Disposition domain.Disposition `json:"disposition"`
	}
	if err := json.Unmarshal(payload, &intent); err != nil {
		return recordedLaunch{}, fmt.Errorf("janitor: decode recorded launch intent: %w", err)
	}
	return recordedLaunch{attemptID: intent.AttemptID, launchKey: intent.LaunchKey, disposition: intent.Disposition}, nil
}

// reclaim carries out one decision against the provider. Adopting gives the slot
// back and keeps the machine; terminating destroys it. Both are addressed by the
// launch key and the ownership token, so a reconciler never acts on a resource it
// merely resembles.
//
// A provider that answers that this capacity cannot be destroyed is stating that
// there is no machine of Mercator's here to destroy: what Mercator holds in a
// pool it does not own is a slot, and giving the slot back is the whole of that
// capacity ceasing to exist. Stopping at the refusal instead is what left one
// container standing in front of every later object in the same listing, on the
// local Docker connection every developer machine seeds.
func (j *Janitor) reclaim(ctx context.Context, object adapter.OwnedExternalObject, outcome OrphanOutcome) error {
	if outcome == OrphanAdopted {
		return j.release(ctx, object)
	}
	err := j.terminate(ctx, object)
	if errors.Is(err, adapter.ErrTerminateUnsupported) {
		return j.release(ctx, object)
	}
	return err
}

func (j *Janitor) release(ctx context.Context, object adapter.OwnedExternalObject) error {
	request := adapter.ReleaseRequest{

		ConnectionID:      object.ConnectionID,
		OperationKey:      "janitor:release:" + object.LaunchKey,
		LaunchKey:         object.LaunchKey,
		OwnershipToken:    object.OwnershipToken,
		LaunchRequestHash: object.RequestHash,
	}
	hash, err := domain.CanonicalHash(request)
	if err != nil {
		return err
	}
	request.RequestHash = hash
	_, err = j.adapter.Release(ctx, request)
	return err
}

func (j *Janitor) terminate(ctx context.Context, object adapter.OwnedExternalObject) error {
	request := adapter.TerminateRequest{

		ConnectionID:      object.ConnectionID,
		OperationKey:      "janitor:terminate:" + object.LaunchKey,
		LaunchKey:         object.LaunchKey,
		OwnershipToken:    object.OwnershipToken,
		LaunchRequestHash: object.RequestHash,
	}
	hash, err := domain.CanonicalHash(request)
	if err != nil {
		return err
	}
	request.RequestHash = hash
	_, err = j.adapter.Terminate(ctx, request)
	return err
}

// OrphanConvergence is the recorded decision about one piece of capacity. It is
// the whole of what an operator or a rule reads back: which policy applied, what
// it decided, why, and which capacity it decided about.
//
// It states no provider command. The outcome is what happened to the capacity,
// and how the provider was asked for it is the provider's own vocabulary: a pool
// that holds no machine of Mercator's gives a slot back where a provisioned
// machine is destroyed, and both are that capacity ceasing to exist.
type OrphanConvergence struct {
	Policy       string        `json:"policy"`
	Outcome      OrphanOutcome `json:"outcome"`
	Reason       string        `json:"reason"`
	ExternalID   string        `json:"external_id,omitempty"`
	LaunchKey    string        `json:"launch_key,omitempty"`
	ConnectionID string        `json:"connection_id,omitempty"`
	RunID        string        `json:"run_id,omitempty"`
}

// recordedDecision is the decision this control plane already wrote down about
// one piece of capacity, and an empty outcome is none. It is read before the
// policy is applied again, so capacity decided by a sweep that then died is
// finished under the rule that decided it rather than judged a second time
// against a record that has moved on.
func (j *Janitor) recordedDecision(ctx context.Context, object adapter.OwnedExternalObject) (orphanDecision, error) {
	history, err := eventlog.ReadFullStream(ctx, j.log, eventlog.StreamKey{

		Type: "orphan",
		ID:   orphanIdentity(object),
	})
	if err != nil {
		return orphanDecision{}, err
	}
	for _, event := range history.Events {
		if event.Type != EventOrphanConverged {
			continue
		}
		var convergence OrphanConvergence
		if err := json.Unmarshal(event.Data, &convergence); err != nil {
			return orphanDecision{}, fmt.Errorf("janitor: decode recorded orphan convergence: %w", err)
		}
		return orphanDecision{outcome: convergence.Outcome, reason: convergence.Reason}, nil
	}
	return orphanDecision{}, nil
}

// record writes the decision down before anything acts on it. It is its own
// stream keyed by the capacity rather than an entry on a Run, because the
// capacity is what the decision was about and the whole point of the
// unattributed case is that there is no Run to file it under.
func (j *Janitor) record(ctx context.Context, object adapter.OwnedExternalObject, decision orphanDecision) error {
	convergence := OrphanConvergence{
		Policy:       OrphanPolicy,
		Outcome:      decision.outcome,
		Reason:       decision.reason,
		ExternalID:   object.ExternalID,
		LaunchKey:    object.LaunchKey,
		ConnectionID: object.ConnectionID,
		RunID:        object.RunID,
	}
	data, err := json.Marshal(convergence)
	if err != nil {
		return fmt.Errorf("janitor: encode orphan convergence: %w", err)
	}
	hash, err := domain.CanonicalHash(convergence)
	if err != nil {
		return err
	}
	identity := orphanIdentity(object)
	_, err = j.log.Append(ctx, eventlog.AppendRequest{
		Stream:                eventlog.StreamKey{Type: "orphan", ID: identity},
		ExpectedStreamVersion: 0,
		CommandKey:            "janitor:converge:" + identity,
		RequestHash:           hash,
		CorrelationID:         object.RunID,
		CausationID:           "janitor",
		Events: []eventlog.NewEvent{{
			ID:            "evt_orphan_" + identity,
			Type:          EventOrphanConverged,
			SchemaVersion: 1,
			Visibility:    eventlog.VisibilityPublic,
			Data:          data,
		}},
	})
	if err != nil {
		return fmt.Errorf("janitor: record orphan convergence: %w", err)
	}
	return nil
}

// orphanIdentity is what this capacity is filed under. The launch key is the
// identity Mercator minted for it, and a provider that reports none leaves only
// its own handle, which is still stable enough to name one decision once.
func orphanIdentity(object adapter.OwnedExternalObject) string {
	if object.LaunchKey != "" {
		return object.LaunchKey
	}
	return object.ExternalID
}
