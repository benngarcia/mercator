package lab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
	"github.com/benngarcia/mercator/internal/orchestrator"
	"github.com/benngarcia/mercator/internal/runprojection"
	"github.com/benngarcia/mercator/internal/scenario"
	"github.com/benngarcia/mercator/internal/scheduler"
	sqlitestore "github.com/benngarcia/mercator/internal/storage/sqlite"
	"github.com/benngarcia/mercator/internal/workspace"
)

type controlPlane struct {
	storage       *sqlitestore.Storage
	world         *simulatedWorld
	orchestrator  *orchestrator.Orchestrator
	pending       []RunArrival
	restarts      uint64
	faultPosition eventlog.GlobalPosition
}

func (runtime *controlPlane) invariantObservation(ctx context.Context, tape WorldTape, transition uint64) (InvariantObservation, error) {
	stored, err := runtime.mercatorEvents(ctx)
	if err != nil {
		return InvariantObservation{}, err
	}
	events := make([]eventlog.CloudEvent, len(stored))
	for index, event := range stored {
		events[index] = event.CloudEvent()
	}
	runs, err := runtime.allRuns(ctx)
	if err != nil {
		return InvariantObservation{}, err
	}
	if err := runtime.orchestrator.RebuildRunProjection(ctx, labWorkspace); err != nil {
		return InvariantObservation{}, err
	}
	rebuiltRuns, err := runtime.allRuns(ctx)
	if err != nil {
		return InvariantObservation{}, err
	}
	schedules, err := runtime.storage.RentalSchedules().List(ctx, labWorkspace)
	if err != nil {
		return InvariantObservation{}, err
	}
	facts := runtime.world.invariantFacts()
	workloads, err := recordedWorkloads(stored)
	if err != nil {
		return InvariantObservation{}, err
	}
	return InvariantObservation{
		StartedAt:                   tape.Start,
		Now:                         runtime.world.nowTime(),
		Transition:                  transition,
		Blueprint:                   tape.BlueprintName,
		World:                       runtime.world.truthSnapshot(),
		MercatorEvents:              events,
		Effects:                     runtime.world.effectRecords(),
		Runs:                        runs,
		Workloads:                   workloads,
		RentalSchedules:             schedules,
		RunRequirements:             facts.Runs,
		ArtifactCatalog:             facts.ArtifactCatalog,
		SeededLocality:              facts.SeededLocality,
		ProjectionRebuildEquivalent: reflect.DeepEqual(runs, rebuiltRuns),
	}, nil
}

// recordedWorkloads is the workload the control plane holds for each Run, read
// back out of the public event log. It is what Mercator itself was told to run,
// which is the only thing an invariant about Mercator's own admission decisions
// may read: the world knows what a process actually does, and checking a Run's
// dependencies against that would check the world against itself.
func recordedWorkloads(events []eventlog.StoredEvent) (map[string]domain.WorkloadRevision, error) {
	workloads := map[string]domain.WorkloadRevision{}
	for _, event := range events {
		if event.Type != orchestrator.EventRunRequested {
			continue
		}
		var payload struct {
			RunID    string                  `json:"run_id"`
			Workload domain.WorkloadRevision `json:"workload_revision"`
		}
		if err := json.Unmarshal(event.CloudEvent().Data, &payload); err != nil {
			return nil, fmt.Errorf("decode recorded workload for %s: %w", event.StreamID, err)
		}
		workloads[payload.RunID] = payload.Workload
	}
	return workloads, nil
}

func (runtime *controlPlane) allRuns(ctx context.Context) ([]domain.RunRecord, error) {
	var records []domain.RunRecord
	request := runprojection.PageRequest{Limit: runprojection.MaxPageSize}
	for {
		page, err := runtime.orchestrator.ListRuns(ctx, labWorkspace, request)
		if err != nil {
			return nil, err
		}
		records = append(records, page.Records...)
		if page.NextCursor == "" {
			return records, nil
		}
		request.After = page.NextCursor
	}
}

func newControlPlane(ctx context.Context, tape WorldTape) (*controlPlane, error) {
	storage, err := sqlitestore.Open(ctx, ":memory:")
	if err != nil {
		return nil, fmt.Errorf("open Lab SQLite control plane: %w", err)
	}
	closeWith := func(err error) (*controlPlane, error) {
		_ = storage.Close()
		return nil, err
	}
	if _, err := storage.Workspaces().Create(ctx, workspace.Create{
		ID:          labWorkspace,
		DisplayName: "Mercator Lab",
		CreatedAt:   tape.Start,
		CreatedBy:   "system:lab",
	}); err != nil {
		return closeWith(fmt.Errorf("create Lab workspace: %w", err))
	}
	if err := storage.Runs().MarkRebuilt(ctx); err != nil {
		return closeWith(fmt.Errorf("initialize Lab Run projection: %w", err))
	}
	world, err := newSimulatedWorld(tape)
	if err != nil {
		return closeWith(err)
	}
	runtime := &controlPlane{storage: storage, world: world}
	runtime.restartOrchestrator()
	return runtime, nil
}

func (runtime *controlPlane) handle(ctx context.Context, event WorldEvent) error {
	runtime.world.setNow(event.At)
	switch event.Kind {
	case EventRunArrived:
		if err := runtime.handleRunArrival(ctx, event); err != nil {
			return err
		}
		return runtime.applyEventFaults(ctx)
	default:
		return fmt.Errorf("Lab control plane does not handle World event kind %q", event.Kind)
	}
}

func (runtime *controlPlane) handleRunArrival(ctx context.Context, event WorldEvent) error {
	var arrival RunArrival
	if err := json.Unmarshal(event.Data, &arrival); err != nil {
		return fmt.Errorf("decode Run arrival event %q: %w", event.ID, err)
	}
	if !runtime.admissible(arrival) {
		runtime.pending = append(runtime.pending, arrival)
		return nil
	}
	return runtime.admitRun(ctx, arrival)
}

// admissible answers whether this workload's declared inputs are durable. The
// question is asked of the workload Mercator would record rather than of the
// arrival, because admission is a control-plane decision about a declaration and
// not about anything the world happens to know.
func (runtime *controlPlane) admissible(arrival RunArrival) bool {
	workload := scenario.WorkloadForRun(labWorkspace, "run-"+arrival.Name, arrival.Request)
	return runtime.world.artifactsAreDurable(workload.Spec.Artifacts.Consumes)
}

func (runtime *controlPlane) admitRun(ctx context.Context, arrival RunArrival) error {
	runID := "run-" + arrival.Name
	runtime.world.prepareRun(runID, arrival)
	if _, err := runtime.orchestrator.CreateRun(ctx, orchestrator.CreateRunRequest{
		WorkspaceID:    labWorkspace,
		RunID:          runID,
		IdempotencyKey: "create:" + runID,
		Workload:       scenario.WorkloadForRun(labWorkspace, runID, arrival.Request),
	}); err != nil {
		return fmt.Errorf("create Lab Run %q: %w", arrival.Name, err)
	}
	if err := runtime.orchestrator.AdvanceRun(ctx, labWorkspace, runID); err != nil {
		if !errors.Is(err, adapter.ErrLaunchIndeterminate) {
			return fmt.Errorf("advance Lab Run %q: %w", arrival.Name, err)
		}
		if err := runtime.orchestrator.AdvanceRun(ctx, labWorkspace, runID); err != nil {
			return fmt.Errorf("reconcile ambiguous Lab Run %q: %w", arrival.Name, err)
		}
	}
	return nil
}

func (runtime *controlPlane) advance(ctx context.Context, now time.Time) error {
	runtime.world.setNow(now)
	_, err := runtime.orchestrator.AdvanceOpenRuns(ctx, labWorkspace)
	if !errors.Is(err, adapter.ErrLaunchIndeterminate) {
		if err != nil {
			return err
		}
		if err := runtime.admitPendingRuns(ctx); err != nil {
			return err
		}
		return runtime.applyEventFaults(ctx)
	}
	_, reconciliationErr := runtime.orchestrator.AdvanceOpenRuns(ctx, labWorkspace)
	if reconciliationErr != nil {
		return reconciliationErr
	}
	if err := runtime.admitPendingRuns(ctx); err != nil {
		return err
	}
	return runtime.applyEventFaults(ctx)
}

func (runtime *controlPlane) admitPendingRuns(ctx context.Context) error {
	pending := runtime.pending[:0]
	for _, arrival := range runtime.pending {
		if !runtime.admissible(arrival) {
			pending = append(pending, arrival)
			continue
		}
		if err := runtime.admitRun(ctx, arrival); err != nil {
			return err
		}
	}
	runtime.pending = pending
	return nil
}

func (runtime *controlPlane) restart(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	runtime.restarts++
	runtime.world.recordControlPlaneRestart(runtime.restarts)
	runtime.restartOrchestrator()
	return nil
}

func (runtime *controlPlane) restartOrchestrator() {
	runtime.orchestrator = orchestrator.New(
		runtime.storage.EventLog(),
		scheduler.New(),
		runtime.world,
		orchestrator.WithClock(runtime.world.nowTime),
		orchestrator.WithImageManifests(runtime.world),
		orchestrator.WithRentalSchedules(runtime.storage.RentalSchedules()),
		orchestrator.WithRunProjection(runtime.storage.Runs()),
	)
}

func (runtime *controlPlane) mercatorEvents(ctx context.Context) ([]eventlog.StoredEvent, error) {
	filter := eventlog.EventFilter{WorkspaceID: labWorkspace}
	head, err := runtime.storage.EventLog().LatestPosition(ctx, filter)
	if err != nil {
		return nil, err
	}
	var events []eventlog.StoredEvent
	for event, err := range eventlog.ScanAll(ctx, runtime.storage.EventLog(), head, filter) {
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, nil
}

func (runtime *controlPlane) applyEventFaults(ctx context.Context) error {
	events, err := runtime.mercatorEvents(ctx)
	if err != nil {
		return err
	}
	for _, event := range events {
		if event.GlobalPosition <= runtime.faultPosition {
			continue
		}
		runtime.faultPosition = event.GlobalPosition
		fault := runtime.world.matchEventFault(event.Type, event.StreamID)
		if fault == nil || fault.Action != scenario.FaultRestartControlPlane {
			continue
		}
		if err := runtime.restart(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (runtime *controlPlane) close() error {
	return runtime.storage.Close()
}
