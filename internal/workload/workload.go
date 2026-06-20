package workload

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/bengarcia/mercator/internal/domain"
	"github.com/bengarcia/mercator/internal/eventlog"
)

const (
	EventWorkloadCreated         = "compute.workload.created.v1"
	EventWorkloadRevisionCreated = "compute.workload.revision_created.v1"
)

type Service struct {
	log eventlog.EventLog
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

type revisionCreatedData struct {
	Revision domain.WorkloadRevision `json:"revision"`
}

func New(log eventlog.EventLog) *Service {
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
	revision := req.Revision
	revision.WorkspaceID = req.WorkspaceID
	revision.WorkloadID = req.WorkloadID
	if violations := domain.ValidateWorkloadRevision(revision); len(violations) > 0 {
		return domain.WorkloadRevision{}, fmt.Errorf("%s: %s", violations[0].Code, violations[0].Message)
	}
	existing, err := s.log.ReadStream(ctx, workloadStream(req.WorkspaceID, req.WorkloadID), 0, 1000)
	if err != nil {
		return domain.WorkloadRevision{}, err
	}
	if len(existing) == 0 {
		return domain.WorkloadRevision{}, fmt.Errorf("workload: workload not found")
	}
	for _, event := range existing {
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
		ExpectedStreamVersion: uint64(len(existing)),
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
	events, err := s.log.ReadStream(ctx, workloadStream(workspaceID, workloadID), 0, 1000)
	if err != nil {
		return nil, err
	}
	revisions := make([]domain.WorkloadRevision, 0)
	for _, event := range events {
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
