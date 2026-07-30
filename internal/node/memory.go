package node

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/benngarcia/mercator/internal/capability"
)

// NewMemoryStore returns an in-memory Store for focused tests and local
// compositions. Production uses the SQLite store, because a node registry that
// forgets across a restart cannot promise not to launch twice.
func NewMemoryStore() Store {
	return &memoryStore{
		records:    map[string]Record{},
		operations: map[string][]Operation{},
		events:     map[string]bool{},
		workloads:  map[string]capability.WorkloadObservation{},
	}
}

type memoryStore struct {
	mu         sync.Mutex
	records    map[string]Record
	operations map[string][]Operation
	events     map[string]bool
	workloads  map[string]capability.WorkloadObservation
}

func nodeKey(workspaceID, nodeID string) string { return workspaceID + "/" + nodeID }

func workloadKey(workspaceID, nodeID, runID, attemptID string) string {
	return workspaceID + "/" + nodeID + "/" + runID + "/" + attemptID
}

func (store *memoryStore) Invite(_ context.Context, record Record) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	key := nodeKey(record.WorkspaceID, record.ID)
	if _, exists := store.records[key]; exists {
		return fmt.Errorf("node: %q is already invited", record.ID)
	}
	store.records[key] = record
	return nil
}

func (store *memoryStore) Get(_ context.Context, workspaceID, nodeID string) (Record, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	record, ok := store.records[nodeKey(workspaceID, nodeID)]
	if !ok {
		return Record{}, fmt.Errorf("%w: %s", ErrNotFound, nodeID)
	}
	return record, nil
}

func (store *memoryStore) Find(_ context.Context, nodeID string) (Record, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, record := range store.records {
		if record.ID == nodeID {
			return record, nil
		}
	}
	return Record{}, fmt.Errorf("%w: %s", ErrNotFound, nodeID)
}

func (store *memoryStore) List(_ context.Context, workspaceID string) ([]Record, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	var records []Record
	for _, record := range store.records {
		if record.WorkspaceID == workspaceID {
			records = append(records, record)
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].ID < records[j].ID })
	return records, nil
}

func (store *memoryStore) Enroll(_ context.Context, workspaceID, nodeID string, enrollment Enrollment) (Record, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	key := nodeKey(workspaceID, nodeID)
	record, ok := store.records[key]
	if !ok {
		return Record{}, fmt.Errorf("%w: %s", ErrNotFound, nodeID)
	}
	if record.State == StateRetired {
		return Record{}, fmt.Errorf("node: %q is retired and cannot enroll again", nodeID)
	}
	if record.EnrollmentTokenID != enrollment.EnrollmentTokenID {
		return Record{}, fmt.Errorf("%w: %s", ErrEnrollmentSpent, nodeID)
	}
	record.State = StateReady
	record.FencingToken++
	record.EnrollmentTokenID = ""
	record.AgentVersion = enrollment.AgentVersion
	record.Facts = enrollment.Facts
	record.EnrolledAt = enrollment.EnrolledAt
	record.LastHeartbeatAt = enrollment.EnrolledAt
	record.LeaseExpires = enrollment.LeaseExpires
	store.records[key] = record
	return record, nil
}

func (store *memoryStore) Reinvite(_ context.Context, workspaceID, nodeID, enrollmentTokenID string, expires time.Time) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	key := nodeKey(workspaceID, nodeID)
	record, ok := store.records[key]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, nodeID)
	}
	record.EnrollmentTokenID = enrollmentTokenID
	record.EnrollmentExpires = expires
	store.records[key] = record
	return nil
}

func (store *memoryStore) Heartbeat(
	_ context.Context,
	workspaceID, nodeID string,
	facts capability.NodeFacts,
	leaseExpires time.Time,
) (Record, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	key := nodeKey(workspaceID, nodeID)
	record, ok := store.records[key]
	if !ok {
		return Record{}, fmt.Errorf("%w: %s", ErrNotFound, nodeID)
	}
	record.State = StateReady
	record.Facts = facts
	record.LastHeartbeatAt = facts.ObservedAt
	record.LeaseExpires = leaseExpires
	store.records[key] = record
	return record, nil
}

func (store *memoryStore) RecordEvent(_ context.Context, event Event) (bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	key := nodeKey(event.WorkspaceID, event.NodeID) + "/" + event.ID
	if store.events[key] {
		return false, nil
	}
	store.events[key] = true
	if event.Kind == EventWorkload {
		observation := *event.Workload
		key := workloadKey(event.WorkspaceID, event.NodeID, observation.RunID, observation.AttemptID)
		// A spool replayed after a reconnection can deliver an earlier
		// observation late. Keeping the newest one is what stops a stale
		// "running" from erasing a recorded exit.
		if held, ok := store.workloads[key]; !ok || !observation.ObservedAt.Before(held.ObservedAt) {
			store.workloads[key] = observation
		}
	}
	return true, nil
}

func (store *memoryStore) LatestWorkload(
	_ context.Context,
	workspaceID, nodeID, runID, attemptID string,
) (capability.WorkloadObservation, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	observation, ok := store.workloads[workloadKey(workspaceID, nodeID, runID, attemptID)]
	return observation, ok, nil
}

func (store *memoryStore) Workloads(_ context.Context, workspaceID, nodeID string) ([]capability.WorkloadObservation, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	prefix := workspaceID + "/" + nodeID + "/"
	var observations []capability.WorkloadObservation
	for key, observation := range store.workloads {
		if len(key) > len(prefix) && key[:len(prefix)] == prefix {
			observations = append(observations, observation)
		}
	}
	sort.Slice(observations, func(i, j int) bool {
		if observations[i].RunID != observations[j].RunID {
			return observations[i].RunID < observations[j].RunID
		}
		return observations[i].AttemptID < observations[j].AttemptID
	})
	return observations, nil
}

func (store *memoryStore) AppendOperation(_ context.Context, operation Operation) (Operation, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	key := nodeKey(operation.WorkspaceID, operation.NodeID)
	for _, existing := range store.operations[key] {
		if existing.OperationID == operation.OperationID {
			return existing, true, nil
		}
	}
	store.operations[key] = append(store.operations[key], operation)
	return operation, false, nil
}

func (store *memoryStore) SettleOperation(_ context.Context, workspaceID, nodeID string, result Result) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	key := nodeKey(workspaceID, nodeID)
	operations := store.operations[key]
	for index := range operations {
		if operations[index].OperationID != result.OperationID {
			continue
		}
		if operations[index].State != OperationPending {
			return nil
		}
		operations[index].State = OperationApplied
		if !result.Applied {
			operations[index].State = OperationRefused
		}
		operations[index].Failure = result.Failure
		operations[index].SettledAt = result.ReportedAt
		return nil
	}
	return fmt.Errorf("node: %q has no operation %q to settle", nodeID, result.OperationID)
}

func (store *memoryStore) PendingOperations(_ context.Context, workspaceID, nodeID string) ([]Operation, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	var pending []Operation
	for _, operation := range store.operations[nodeKey(workspaceID, nodeID)] {
		if operation.State == OperationPending {
			pending = append(pending, operation)
		}
	}
	return pending, nil
}

func (store *memoryStore) AppliedOperationIDs(_ context.Context, workspaceID, nodeID string) ([]string, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	var applied []string
	for _, operation := range store.operations[nodeKey(workspaceID, nodeID)] {
		if operation.State == OperationApplied {
			applied = append(applied, operation.OperationID)
		}
	}
	slices.Sort(applied)
	return applied, nil
}

func (store *memoryStore) ExpireLeases(_ context.Context, now time.Time) ([]Record, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	var expired []Record
	for key, record := range store.records {
		if record.State != StateReady || now.Before(record.LeaseExpires) {
			continue
		}
		record.State = StateLost
		store.records[key] = record
		expired = append(expired, record)
	}
	sort.Slice(expired, func(i, j int) bool { return expired[i].ID < expired[j].ID })
	return expired, nil
}
