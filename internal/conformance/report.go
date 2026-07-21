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

type TrialReport struct {
	TrialID      string             `json:"trial_id"`
	WorkspaceID  string             `json:"workspace_id"`
	ConnectionID string             `json:"connection_id"`
	AdapterType  string             `json:"adapter_type"`
	Mode         Mode               `json:"mode"`
	Verdict      Verdict            `json:"verdict"`
	StartedAt    time.Time          `json:"started_at,omitempty"`
	DurationMS   int64              `json:"duration_ms,omitempty"`
	Scenarios    []ScenarioEvidence `json:"scenarios,omitempty"`
	Inventory    InventoryEvidence  `json:"inventory"`
	Failure      *TrialFailure      `json:"failure,omitempty"`
}

type ScenarioEvidence struct {
	Name       string                   `json:"name"`
	StartedAt  time.Time                `json:"started_at"`
	DurationMS int64                    `json:"duration_ms"`
	Run        domain.RunRecord         `json:"run"`
	Placement  domain.PlacementDecision `json:"placement"`
	Events     []eventlog.CloudEvent    `json:"events"`
}

type InventoryEvidence struct {
	Owned int `json:"owned"`
}

type TrialFailure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
