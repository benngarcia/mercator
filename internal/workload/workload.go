package workload

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
)

const (
	EventWorkloadCreated         = "compute.workload.created.v1"
	EventWorkloadRevisionCreated = "compute.workload.revision_created.v1"
)

type Service struct {
	log eventlog.WorkspaceEventLog
	now func() time.Time
}

type CreateWorkloadRequest struct {
	WorkspaceID string
	WorkloadID  string
	Name        string
}

type CreateRevisionRequest struct {
	WorkspaceID string
	WorkloadID  string
	Revision    domain.WorkloadRevision
}

type workloadCreatedData struct {
	WorkloadID string `json:"workload_id"`
	Name       string `json:"name"`
}

// revisionCreatedData is the whole revision, environment values included, and it
// is the private payload of the event. It is what GetRevision and ListRevisions
// read, because a revision a Run is created from has to be the revision the
// caller stored.
type revisionCreatedData struct {
	Revision domain.WorkloadRevision `json:"revision"`
}

// publicRevisionCreatedData is what a reader of the public log sees of the same
// revision: every environment value replaced by its kind. This door wrote the
// full revision into a public event, so a token a caller put in a container's
// environment reached every console reader of the workspace over the event
// stream, while the run door has redacted exactly these values since it had a
// public payload at all.
type publicRevisionCreatedData struct {
	Revision domain.PublicWorkloadRevision `json:"revision"`
}

func New(log eventlog.WorkspaceEventLog) *Service {
	return &Service{log: log, now: time.Now}
}

func (s *Service) CreateWorkload(ctx context.Context, req CreateWorkloadRequest) error {
	if req.WorkspaceID == "" || req.WorkloadID == "" {
		return fmt.Errorf("workload: workspace_id and workload_id are required")
	}
	data, err := json.Marshal(workloadCreatedData{WorkloadID: req.WorkloadID, Name: req.Name})
	if err != nil {
		return err
	}
	hash, err := domain.CanonicalHash(req)
	if err != nil {
		return err
	}
	_, err = s.log.AppendIfWorkspaceActive(ctx, eventlog.AppendRequest{
		Stream:                workloadStream(req.WorkspaceID, req.WorkloadID),
		ExpectedStreamVersion: 0,
		CommandKey:            "workload:create:" + req.WorkloadID,
		RequestHash:           hash,
		CorrelationID:         req.WorkloadID,
		CausationID:           "workload:create:" + req.WorkloadID,
		Events: []eventlog.NewEvent{{
			ID:            "evt_workload_" + req.WorkloadID + "_created",
			Type:          EventWorkloadCreated,
			SchemaVersion: 1,
			OccurredAt:    s.now().UTC(),
			Visibility:    eventlog.VisibilityPublic,
			Data:          data,
		}},
	})
	return err
}

func (s *Service) CreateRevision(ctx context.Context, req CreateRevisionRequest) (domain.WorkloadRevision, error) {
	if req.WorkspaceID == "" || req.WorkloadID == "" || req.Revision.ID == "" {
		return domain.WorkloadRevision{}, fmt.Errorf("workload: workspace_id, workload_id, and revision id are required")
	}
	revision := req.Revision
	revision.WorkspaceID = req.WorkspaceID
	revision.WorkloadID = req.WorkloadID
	// Fill the omissions a minimal create body leaves before validating, which is
	// the order NormalizeWorkloadRevision documents and the order run intake
	// already uses. This door stored what it validated, so validating raw input
	// made the two doors disagree about one body: a revision omitting its service
	// class was refused here and filled with standard by POST /v1/runs, and a
	// revision that did get stored raw was served back with no class at all,
	// which is not a PlacementPolicy the wire contract allows.
	revision = domain.NormalizeWorkloadRevision(revision)
	if violations := domain.ValidateWorkloadRevision(revision); len(violations) > 0 {
		return domain.WorkloadRevision{}, fmt.Errorf("%s: %s", violations[0].Code, violations[0].Message)
	}
	history, err := eventlog.ReadFullStream(ctx, s.log, workloadStream(req.WorkspaceID, req.WorkloadID))
	if err != nil {
		return domain.WorkloadRevision{}, err
	}
	if len(history.Events) == 0 {
		return domain.WorkloadRevision{}, fmt.Errorf("workload: workload not found")
	}
	commandKey := "workload:revision:create:" + revision.ID
	for _, event := range history.Events {
		if event.Type != EventWorkloadRevisionCreated {
			continue
		}
		existing, err := storedRevision(event)
		if err != nil {
			return domain.WorkloadRevision{}, err
		}
		if existing.ID == revision.ID && event.CommandKey != commandKey {
			return domain.WorkloadRevision{}, fmt.Errorf("workload: revision already exists")
		}
	}
	private, err := json.Marshal(revisionCreatedData{Revision: revision})
	if err != nil {
		return domain.WorkloadRevision{}, err
	}
	data, err := json.Marshal(publicRevisionCreatedData{Revision: revision.Public()})
	if err != nil {
		return domain.WorkloadRevision{}, err
	}
	hash, err := domain.CanonicalHash(revision)
	if err != nil {
		return domain.WorkloadRevision{}, err
	}
	_, err = s.log.AppendIfWorkspaceActive(ctx, eventlog.AppendRequest{
		Stream:                workloadStream(req.WorkspaceID, req.WorkloadID),
		ExpectedStreamVersion: history.LastVersion,
		CommandKey:            commandKey,
		RequestHash:           hash,
		CorrelationID:         req.WorkloadID,
		CausationID:           commandKey,
		Events: []eventlog.NewEvent{{
			ID:            "evt_workload_" + req.WorkloadID + "_revision_" + revision.ID + "_created",
			Type:          EventWorkloadRevisionCreated,
			SchemaVersion: 1,
			OccurredAt:    s.now().UTC(),
			Visibility:    eventlog.VisibilityPublic,
			Data:          data,
			PrivateData:   private,
		}},
	})
	if err != nil {
		return domain.WorkloadRevision{}, err
	}
	return revision, nil
}

func (s *Service) GetRevision(ctx context.Context, workspaceID, workloadID, revisionID string) (domain.WorkloadRevision, error) {
	revisions, err := s.ListRevisions(ctx, workspaceID, workloadID)
	if err != nil {
		return domain.WorkloadRevision{}, err
	}
	for _, revision := range revisions {
		if revision.ID == revisionID {
			return revision, nil
		}
	}
	return domain.WorkloadRevision{}, fmt.Errorf("workload: revision not found")
}

func (s *Service) ListRevisions(ctx context.Context, workspaceID, workloadID string) ([]domain.WorkloadRevision, error) {
	history, err := eventlog.ReadFullStream(ctx, s.log, workloadStream(workspaceID, workloadID))
	if err != nil {
		return nil, err
	}
	revisions := make([]domain.WorkloadRevision, 0)
	for _, event := range history.Events {
		if event.Type != EventWorkloadRevisionCreated {
			continue
		}
		stored, err := storedRevision(event)
		if err != nil {
			return nil, err
		}
		revisions = append(revisions, stored)
	}
	sort.Slice(revisions, func(i, j int) bool { return revisions[i].ID < revisions[j].ID })
	return revisions, nil
}

// storedRevision is the revision one event recorded. It reads the private
// payload, which is the only copy that carries what a container's environment is
// set to: the public one states each value's kind, and a Run created from that
// would run with every literal replaced by the word "literal".
func storedRevision(event eventlog.StoredEvent) (domain.WorkloadRevision, error) {
	var data revisionCreatedData
	if err := json.Unmarshal(event.PrivateData, &data); err != nil {
		return domain.WorkloadRevision{}, fmt.Errorf("workload: revision event %q carries no readable revision: %w", event.ID, err)
	}
	return data.Revision, nil
}

func workloadStream(workspaceID, workloadID string) eventlog.StreamKey {
	return eventlog.StreamKey{WorkspaceID: workspaceID, Type: "workload", ID: workloadID}
}
