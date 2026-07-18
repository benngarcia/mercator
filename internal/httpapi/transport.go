package httpapi

import (
	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/connection"
	"github.com/benngarcia/mercator/internal/credential"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
	"github.com/benngarcia/mercator/internal/ociresolver"
)

type createRunBody struct {
	WorkspaceID        string                  `json:"workspace_id,omitempty"`
	RunID              string                  `json:"run_id,omitempty"`
	WorkloadID         string                  `json:"workload_id,omitempty"`
	WorkloadRevisionID string                  `json:"workload_revision_id,omitempty"`
	Workload           domain.WorkloadRevision `json:"workload"`
	// Top-level image shorthand. When no full workload (or revision id) is
	// supplied, the server synthesizes workload.spec.containers[0] from these.
	Image string                       `json:"image,omitempty"`
	Args  []string                     `json:"args,omitempty"`
	Env   map[string]domain.EnvBinding `json:"env,omitempty"`
}

// hasWorkloadSpec reports whether the body carries an explicit full workload
// spec (at least one container). The shorthand image form is only expanded when
// no explicit spec is present.
func (b createRunBody) hasWorkloadSpec() bool {
	return len(b.Workload.Spec.Containers) > 0
}

type createWorkloadBody struct {
	WorkspaceID string `json:"workspace_id"`
	WorkloadID  string `json:"workload_id"`
	Name        string `json:"name"`
}

type createRevisionBody struct {
	Revision domain.WorkloadRevision `json:"revision"`
}

type workloadRevisionResponse struct {
	Revision domain.WorkloadRevision `json:"revision"`
}

type workloadRevisionListResponse struct {
	Revisions []domain.WorkloadRevision `json:"revisions"`
}

type resolveImageBody struct {
	Image    string `json:"image"`
	Platform string `json:"platform"`
}

type resolveImageResponse struct {
	Image ociresolver.ResolvedImage `json:"image"`
}

type createConnectionBody struct {
	WorkspaceID  string                `json:"workspace_id"`
	ConnectionID string                `json:"connection_id"`
	AdapterType  string                `json:"adapter_type"`
	Config       map[string]string     `json:"config,omitempty"`
	Credential   credential.Credential `json:"credential"`
	// Secret is write-only: accepted on create, never echoed in any response.
	Secret string `json:"secret,omitempty"`
}

type connectionResponse struct {
	Connection connection.Record `json:"connection"`
}

type connectionListResponse struct {
	Connections []connection.Record `json:"connections"`
}

type adapterListResponse struct {
	Adapters []adapter.Manifest `json:"adapters"`
}

type offerListResponse struct {
	Offers []domain.OfferSnapshot `json:"offers"`
}

type replaySinkBody struct {
	FromExclusive eventlog.GlobalPosition `json:"from_exclusive"`
	Limit         int                     `json:"limit"`
	ReplayID      string                  `json:"replay_id"`
}

type eventListResponse struct {
	Events []eventlog.CloudEvent `json:"events"`
}

type placementPreviewBody struct {
	RunID       string                  `json:"run_id,omitempty"`
	WorkspaceID string                  `json:"workspace_id,omitempty"`
	Workload    domain.WorkloadRevision `json:"workload"`
}

type placementPreviewResponse struct {
	Decision domain.PlacementDecision `json:"decision"`
}

type runResponse struct {
	// RunID is the convenience top-level run identifier, returned alongside the
	// full run{} record on every run response for envelope consistency. Metadata
	// is reserved for a future per-response metadata object.
	RunID     string            `json:"run_id"`
	Run       domain.RunRecord  `json:"run"`
	Metadata  map[string]any    `json:"metadata,omitempty"`
	Links     map[string]string `json:"links,omitempty"`
	Duplicate bool              `json:"duplicate,omitempty"`
}

// newRunResponse builds a run response envelope with the top-level run_id
// derived from the record, keeping run_id and run.id consistent.
func newRunResponse(workspaceID string, record domain.RunRecord, duplicate bool) runResponse {
	return runResponse{
		RunID:     record.ID,
		Run:       record,
		Links:     runLinks(workspaceID, record.ID),
		Duplicate: duplicate,
	}
}

type runListResponse struct {
	Runs []domain.RunRecord `json:"runs"`
}

type placementDecisionResponse struct {
	Decision domain.PlacementDecision `json:"decision"`
}

type errorResponse struct {
	Code    string             `json:"code"`
	Message string             `json:"message"`
	Details []domain.Violation `json:"details,omitempty"`
}
