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
	TrialID        string            `json:"trial_id"`
	WorkspaceID    string            `json:"workspace_id"`
	ConnectionID   string            `json:"connection_id"`
	AdapterType    string            `json:"adapter_type"`
	Mode           Mode              `json:"mode"`
	Verdict        Verdict           `json:"verdict"`
	StartedAt      time.Time         `json:"started_at"`
	Duration       time.Duration     `json:"-"`
	DurationSecs   float64           `json:"duration_seconds"`
	Offer          OfferEvidence     `json:"offer"`
	Run            RunEvidence       `json:"run"`
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
	// oldest first. It is the chain and not its last entry, because a decision is
	// appended and never rewritten: a trial where the first machine refused the
	// launch is a trial with two decisions, and evidence holding only the second
	// cannot show that the first ever happened.
	BookingDecisions []domain.BookingDecision `json:"booking_decisions,omitempty"`
}

type InventoryEvidence struct {
	Owned int `json:"owned"`
}

type TrialFailure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
