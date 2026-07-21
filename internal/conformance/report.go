package conformance

import "time"

type Verdict string

const (
	VerdictPassed  Verdict = "passed"
	VerdictFailed  Verdict = "failed"
	VerdictBlocked Verdict = "blocked"
)

// Evidence is the complete sanitized record returned by one Conformance Trial.
type Evidence struct {
	TrialID      string            `json:"trial_id"`
	WorkspaceID  string            `json:"workspace_id"`
	ConnectionID string            `json:"connection_id"`
	AdapterType  string            `json:"adapter_type"`
	Verdict      Verdict           `json:"verdict"`
	StartedAt    time.Time         `json:"started_at"`
	Duration     time.Duration     `json:"-"`
	DurationSecs float64           `json:"duration_seconds"`
	Offer        OfferEvidence     `json:"offer"`
	Run          RunEvidence       `json:"run"`
	Inventory    InventoryEvidence `json:"inventory"`
	Failure      *TrialFailure     `json:"failure,omitempty"`
}

type OfferEvidence struct {
	ID               string  `json:"id"`
	ConnectionID     string  `json:"connection_id"`
	RatePerSecondUSD float64 `json:"rate_per_second_usd"`
	MaximumCostUSD   float64 `json:"maximum_cost_usd"`
}

type RunEvidence struct {
	ID         string   `json:"id"`
	Outcome    string   `json:"outcome,omitempty"`
	ExitCode   *int     `json:"exit_code,omitempty"`
	Cleanup    string   `json:"cleanup,omitempty"`
	Closed     bool     `json:"closed"`
	EventTypes []string `json:"event_types,omitempty"`
}

type InventoryEvidence struct {
	Owned int `json:"owned"`
}

type TrialFailure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
