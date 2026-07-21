package workload

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
	"github.com/benngarcia/mercator/internal/workspace"
)

const (
	EventWorkloadCreated         = "compute.workload.created.v1"
	EventWorkloadRevisionCreated = "compute.workload.revision_created.v1"
)

type Service struct {
	log        eventlog.EventLog
	workspaces workspace.ActiveCatalog
	now        func() time.Time
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

type revisionCreatedData struct {
	Revision domain.WorkloadRevision `json:"revision"`
}

func New(log eventlog.EventLog, workspaces workspace.ActiveCatalog) *Service {
	if workspaces == nil {
		panic("workload: workspace catalog is required")
	}
	return &Service{log: log, workspaces: workspaces, now: time.Now}
}

func (s *Service) CreateWorkload(ctx context.Context, req CreateWorkloadRequest) error {
	if req.WorkspaceID == "" || req.WorkloadID == "" {
		return fmt.Errorf("workload: workspace_id and workload_id are required")
	}
	if err := s.workspaces.RequireActive(ctx, req.WorkspaceID); err != nil {
		return err
	}
	data, err := json.Marshal(workloadCreatedData{WorkloadID: req.WorkloadID, Name: req.Name})
	if err != nil {
		return err
	}
	hash, err := domain.CanonicalHash(req)
	if err != nil {
		return err
	}
	_, err = s.log.Append(ctx, eventlog.AppendRequest{
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
	if err := s.workspaces.RequireActive(ctx, req.WorkspaceID); err != nil {
		return domain.WorkloadRevision{}, err
	}
	revision := req.Revision
	revision.WorkspaceID = req.WorkspaceID
	revision.WorkloadID = req.WorkloadID
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
	for _, event := range history.Events {
		if event.Type != EventWorkloadRevisionCreated {
			continue
		}
		var data revisionCreatedData
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return domain.WorkloadRevision{}, err
		}
		if data.Revision.ID == revision.ID {
			return domain.WorkloadRevision{}, fmt.Errorf("workload: revision already exists")
		}
	}
	data, err := json.Marshal(revisionCreatedData{Revision: revision})
	if err != nil {
		return domain.WorkloadRevision{}, err
	}
	hash, err := domain.CanonicalHash(revision)
	if err != nil {
		return domain.WorkloadRevision{}, err
	}
	_, err = s.log.Append(ctx, eventlog.AppendRequest{
		Stream:                workloadStream(req.WorkspaceID, req.WorkloadID),
		ExpectedStreamVersion: history.LastVersion,
		CommandKey:            "workload:revision:create:" + revision.ID,
		RequestHash:           hash,
		CorrelationID:         req.WorkloadID,
		CausationID:           "workload:revision:create:" + revision.ID,
		Events: []eventlog.NewEvent{{
			ID:            "evt_workload_" + req.WorkloadID + "_revision_" + revision.ID + "_created",
			Type:          EventWorkloadRevisionCreated,
			SchemaVersion: 1,
			OccurredAt:    s.now().UTC(),
			Visibility:    eventlog.VisibilityPublic,
			Data:          data,
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
		var data revisionCreatedData
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return nil, err
		}
		revisions = append(revisions, data.Revision)
	}
	sort.Slice(revisions, func(i, j int) bool { return revisions[i].ID < revisions[j].ID })
	return revisions, nil
}

func workloadStream(workspaceID, workloadID string) eventlog.StreamKey {
	return eventlog.StreamKey{WorkspaceID: workspaceID, Type: "workload", ID: workloadID}
}
