package lab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/eventlog"
	"github.com/benngarcia/mercator/internal/orchestrator"
	"github.com/benngarcia/mercator/internal/scenario"
	"github.com/benngarcia/mercator/internal/scheduler"
	sqlitestore "github.com/benngarcia/mercator/internal/storage/sqlite"
	"github.com/benngarcia/mercator/internal/workspace"
)

type controlPlane struct {
	storage       *sqlitestore.Storage
	world         *simulatedWorld
	orchestrator  *orchestrator.Orchestrator
	restarts      uint64
	faultPosition eventlog.GlobalPosition
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
		return runtime.applyEventFaults(ctx)
	}
	_, reconciliationErr := runtime.orchestrator.AdvanceOpenRuns(ctx, labWorkspace)
	if reconciliationErr != nil {
		return reconciliationErr
	}
	return runtime.applyEventFaults(ctx)
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
