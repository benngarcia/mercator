package janitor

import (
	"context"
	"encoding/json"
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

// Sweep converges every piece of capacity this workspace owns that Mercator no
// longer holds live work for. Each one is decided by the stated policy, acted on,
// and written down naming the policy that decided it.
func (j *Janitor) Sweep(ctx context.Context, workspaceID string) (Result, error) {
	if j.adapter == nil {
		return Result{}, fmt.Errorf("janitor: adapter is required")
	}
	if j.log == nil {
		return Result{}, fmt.Errorf("janitor: event log is required")
	}
	if workspaceID == "" {
		return Result{}, fmt.Errorf("janitor: workspace_id is required")
	}
	owned, err := j.adapter.ListOwned(ctx, adapter.OwnershipQuery{WorkspaceID: workspaceID})
	if err != nil {
		return Result{}, err
	}
	result := Result{Found: len(owned)}
	for _, object := range owned {
		if object.WorkspaceID == "" {
			// An orphan listed without workspace labels still belongs to the
			// swept workspace; requests need it to route through the broker.
			object.WorkspaceID = workspaceID
		}
		decision, err := j.decide(ctx, object)
		if err != nil {
			return result, err
		}
		if decision.outcome == "" {
			continue
		}
		if err := j.reclaim(ctx, object, decision.action); err != nil {
			return result, err
		}
		if err := j.record(ctx, object, decision); err != nil {
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

// orphanDecision is one application of the policy: what it decided, why, and
// which cleanup action carries it out. An empty outcome is not a decision at all,
// which is what live work gets: Mercator still holds the Run this capacity is
// executing, and that Run's own lifecycle converges it.
type orphanDecision struct {
	outcome OrphanOutcome
	reason  string
	action  domain.Disposition
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
	switch {
	case !recorded.exists:
		return terminate(reasonNoRecordedRun), nil
	case recorded.cleanupAsked:
		return convergeByRecord(recorded.disposition)
	case recorded.closed:
		// A Run that ended without Mercator ever asking for its capacity back is
		// the case a sweep keyed on the cleanup request alone could only skip, so
		// the machine ran on with nothing left that would ever reclaim it.
		return terminate(reasonClosedWithoutAsking), nil
	default:
		return orphanDecision{}, nil
	}
}

// convergeByRecord is the adopt half of the policy. The launch recorded how this
// capacity is handed back, and that is what says whether there is a machine left
// to keep once the workload on it is reclaimed.
func convergeByRecord(disposition domain.Disposition) (orphanDecision, error) {
	switch disposition {
	case domain.DispositionRelease:
		return orphanDecision{
			outcome: OrphanAdopted,
			reason:  reasonRecordedRelease,
			action:  domain.DispositionRelease,
		}, nil
	case domain.DispositionTerminate:
		return orphanDecision{
			outcome: OrphanTerminated,
			reason:  reasonRecordedTerminate,
			action:  domain.DispositionTerminate,
		}, nil
	default:
		return orphanDecision{}, fmt.Errorf("janitor: cleanup requires a valid recorded disposition, got %q", disposition)
	}
}

func terminate(reason string) orphanDecision {
	return orphanDecision{outcome: OrphanTerminated, reason: reason, action: domain.DispositionTerminate}
}

// recordedRun is what Mercator's own log says about the Run one owned object
// names: whether there is a record at all, whether it is over, whether anybody
// ever asked for its capacity back, and how the launch said to hand it back.
type recordedRun struct {
	exists       bool
	closed       bool
	cleanupAsked bool
	disposition  domain.Disposition
}

func (j *Janitor) recordedRun(ctx context.Context, object adapter.OwnedExternalObject) (recordedRun, error) {
	history, err := eventlog.ReadFullStream(ctx, j.log, eventlog.StreamKey{
		WorkspaceID: object.WorkspaceID,
		Type:        "run",
		ID:          object.RunID,
	})
	if err != nil {
		return recordedRun{}, err
	}
	recorded := recordedRun{exists: len(history.Events) > 0}
	for _, event := range history.Events {
		switch event.Type {
		case "compute.run.launch_intent_recorded.v1":
			disposition, err := recordedDisposition(event)
			if err != nil {
				return recordedRun{}, err
			}
			recorded.disposition = disposition
		case "compute.run.cleanup_requested.v1", "compute.run.cleanup_confirmed.v1":
			recorded.cleanupAsked = true
		case "compute.run.closed.v1":
			recorded.closed = true
		}
	}
	return recorded, nil
}

func recordedDisposition(event eventlog.StoredEvent) (domain.Disposition, error) {
	payload := event.PrivateData
	if len(payload) == 0 {
		payload = event.Data
	}
	var intent struct {
		Disposition domain.Disposition `json:"disposition"`
	}
	if err := json.Unmarshal(payload, &intent); err != nil {
		return "", fmt.Errorf("janitor: decode recorded launch intent: %w", err)
	}
	return intent.Disposition, nil
}

// reclaim carries out one decision against the provider. Release gives the slot
// back and keeps the machine; Terminate destroys it. Both are addressed by the
// launch key and the ownership token, so a reconciler never acts on a resource it
// merely resembles.
func (j *Janitor) reclaim(ctx context.Context, object adapter.OwnedExternalObject, action domain.Disposition) error {
	if action == domain.DispositionRelease {
		request := adapter.ReleaseRequest{
			WorkspaceID:       object.WorkspaceID,
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
	request := adapter.TerminateRequest{
		WorkspaceID:       object.WorkspaceID,
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
type OrphanConvergence struct {
	Policy       string             `json:"policy"`
	Outcome      OrphanOutcome      `json:"outcome"`
	Reason       string             `json:"reason"`
	Action       domain.Disposition `json:"action"`
	ExternalID   string             `json:"external_id,omitempty"`
	LaunchKey    string             `json:"launch_key,omitempty"`
	ConnectionID string             `json:"connection_id,omitempty"`
	RunID        string             `json:"run_id,omitempty"`
}

// record writes the decision down before the sweep moves on. It is its own
// stream keyed by the capacity rather than an entry on a Run, because the
// capacity is what the decision was about and the whole point of the
// unattributed case is that there is no Run to file it under.
func (j *Janitor) record(ctx context.Context, object adapter.OwnedExternalObject, decision orphanDecision) error {
	convergence := OrphanConvergence{
		Policy:       OrphanPolicy,
		Outcome:      decision.outcome,
		Reason:       decision.reason,
		Action:       decision.action,
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
		Stream:                eventlog.StreamKey{WorkspaceID: object.WorkspaceID, Type: "orphan", ID: identity},
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
