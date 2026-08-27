package conformance

import (
	"time"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
)

type Verdict string

const (
	VerdictPassed  Verdict = "passed"
	VerdictFailed  Verdict = "failed"
	VerdictBlocked Verdict = "blocked"
)

// Evidence is the complete sanitized record returned by one Conformance Trial.
type Evidence struct {
	TrialID string `json:"trial_id"`

	ConnectionID   string            `json:"connection_id"`
	AdapterType    string            `json:"adapter_type"`
	Mode           Mode              `json:"mode"`
	Verdict        Verdict           `json:"verdict"`
	StartedAt      time.Time         `json:"started_at"`
	Duration       time.Duration     `json:"-"`
	DurationSecs   float64           `json:"duration_seconds"`
	Offer          OfferEvidence     `json:"offer"`
	Run            RunEvidence       `json:"run"`
	Promises       []PromiseEvidence `json:"promises,omitempty"`
	Inventory      InventoryEvidence `json:"inventory"`
	Failure        *TrialFailure     `json:"failure,omitempty"`
	CleanupFailure *TrialFailure     `json:"cleanup_failure,omitempty"`
}

type OfferEvidence struct {
	ID               string  `json:"id"`
	ConnectionID     string  `json:"connection_id"`
	RatePerSecondUSD float64 `json:"rate_per_second_usd"`
	MaximumCostUSD   float64 `json:"maximum_cost_usd"`
}

type RunEvidence struct {
	ID        string    `json:"id"`
	StartedAt time.Time `json:"started_at,omitempty"`
	// ApplicationReadyAt is when the workload itself said it could do work, as the
	// workload stated the moment. It is the actual of the last stage of a launch,
	// and it is the one stage no provider, node, or container runtime can report:
	// each of them can see a process running and none of them can see whether it is
	// serving. A probe that ran and never said so is a trial with that stage
	// unproven, which is why the verdict reads it.
	ApplicationReadyAt *time.Time            `json:"application_ready_at,omitempty"`
	DurationSecs       float64               `json:"duration_seconds,omitempty"`
	Outcome            string                `json:"outcome,omitempty"`
	ExitCode           *int                  `json:"exit_code,omitempty"`
	Cleanup            string                `json:"cleanup,omitempty"`
	Closed             bool                  `json:"closed"`
	EventTypes         []string              `json:"event_types,omitempty"`
	Events             []eventlog.CloudEvent `json:"events,omitempty"`
	// BookingDecisions is every decision the control plane recorded about this Run,
	// oldest first, exactly as the decision route answered with them. It is the whole
	// chain rather than its last entry because a decision is appended and never
	// rewritten, so evidence holding one entry cannot say whether it is the only
	// answer or the last of several.
	//
	// A trial of its own produces one, because a trial asks for one launch on one
	// machine and states MaxPreStartAttempts of 1. So the length is not a claim this
	// package can hold, and it is held where a Run really is answered twice: the
	// decision route is read whole through the real daemon in internal/daemon, and
	// safety.decisions_are_never_rewritten adjudicates the chain in the Lab.
	BookingDecisions []domain.BookingDecision `json:"booking_decisions,omitempty"`
}

// PromiseEvidence is what one promise of the CapacityProvider contract found.
// It names the Lab rule it is the higher-fidelity half of, so evidence from a
// live provider can be read against the rule the Lab holds over the same fact.
type PromiseEvidence struct {
	Name    string  `json:"name"`
	Rule    string  `json:"rule"`
	Outcome Promise `json:"outcome"`
	Detail  string  `json:"detail,omitempty"`
}

// Promise is what became of one promise. Out of reach is neither kept nor
// broken: a provider that enumerates nothing it owns is not breaking the
// contract by having no listing, and reporting the case green would claim to
// have read one.
type Promise string

const (
	PromiseKept       Promise = "kept"
	PromiseBroken     Promise = "broken"
	PromiseOutOfReach Promise = "out_of_reach"
)

type InventoryEvidence struct {
	Owned int `json:"owned"`
}

type TrialFailure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
