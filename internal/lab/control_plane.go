package lab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
	"github.com/benngarcia/mercator/internal/janitor"
	"github.com/benngarcia/mercator/internal/orchestrator"
	"github.com/benngarcia/mercator/internal/runprojection"
	"github.com/benngarcia/mercator/internal/scenario"
	"github.com/benngarcia/mercator/internal/scheduler"
	sqlitestore "github.com/benngarcia/mercator/internal/storage/sqlite"
	"github.com/benngarcia/mercator/internal/workspace"
)

type controlPlane struct {
	storage *sqlitestore.Storage
	world   *simulatedWorld
	// workspaces is every tenant this Blueprint runs work for. One Mercator
	// serves all of them, which is the point: the machines are shared and the
	// caches, Artifacts, and Runs on them are not.
	workspaces   []string
	orchestrator *orchestrator.Orchestrator
	// janitor is what converges capacity the control plane does not recognise. It
	// is a controller beside the Runs rather than a step in any of them, exactly as
	// it is in production: nothing waits on it, and what it decides about a machine
	// is decided by a stated policy and written down.
	janitor *janitor.Janitor
	// prewarm is what this world's Blueprint allows the control plane to have in
	// flight for work it has not admitted. A Blueprint that states none turns
	// preparation off, which is every fixture written before it existed.
	prewarm       orchestrator.PrewarmPolicy
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
	for _, workspace := range runtime.workspaces {
		if err := runtime.orchestrator.RebuildRunProjection(ctx, workspace); err != nil {
			return InvariantObservation{}, err
		}
	}
	rebuiltRuns, err := runtime.allRuns(ctx)
	if err != nil {
		return InvariantObservation{}, err
	}
	schedules, err := runtime.allSchedules(ctx)
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
		SeededReplicas:              facts.SeededReplicas,
		Prewarm:                     facts.Prewarm,
		SeededOrphans:               facts.SeededOrphans,
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

// holdsOpenRun answers whether Mercator still owes an answer for a Run it
// accepted. That is what a Run parked by admission looks like from outside: in
// the projection, not closed, and waiting on something the world may never do.
func (runtime *controlPlane) holdsOpenRun(ctx context.Context) (bool, error) {
	runs, err := runtime.allRuns(ctx)
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(runs, func(run domain.RunRecord) bool { return !run.Closed }), nil
}

// allRuns is every Run Mercator holds, across every tenant this Blueprint runs
// work for. A rule stated over one workspace would be blind to the other's Runs,
// which is exactly the blindness a cross-workspace claim has to be checked
// against.
func (runtime *controlPlane) allRuns(ctx context.Context) ([]domain.RunRecord, error) {
	var records []domain.RunRecord
	for _, workspace := range runtime.workspaces {
		request := runprojection.PageRequest{Limit: runprojection.MaxPageSize}
		for {
			page, err := runtime.orchestrator.ListRuns(ctx, workspace, request)
			if err != nil {
				return nil, err
			}
			records = append(records, page.Records...)
			if page.NextCursor == "" {
				break
			}
			request.After = page.NextCursor
		}
	}
	return records, nil
}

// allSchedules is every Rental Schedule Mercator owns. A Rental is one machine
// whichever tenant booked it, so the schedules of every workspace are read
// together: a rule about one machine carrying one running Booking is a rule about
// the machine and not about a tenant's view of it.
func (runtime *controlPlane) allSchedules(ctx context.Context) (map[string]domain.RentalSchedule, error) {
	schedules := map[string]domain.RentalSchedule{}
	for _, workspace := range runtime.workspaces {
		owned, err := runtime.storage.RentalSchedules().List(ctx, workspace)
		if err != nil {
			return nil, err
		}
		for rentalID, schedule := range owned {
			schedules[rentalID] = schedule
		}
	}
	return schedules, nil
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
	workspaces := tape.Workspaces()
	for _, id := range workspaces {
		if _, err := storage.Workspaces().Create(ctx, workspace.Create{
			ID:          id,
			DisplayName: "Mercator Lab " + id,
			CreatedAt:   tape.Start,
			CreatedBy:   "system:lab",
		}); err != nil {
			return closeWith(fmt.Errorf("create Lab workspace %s: %w", id, err))
		}
	}
	if err := storage.Runs().MarkRebuilt(ctx); err != nil {
		return closeWith(fmt.Errorf("initialize Lab Run projection: %w", err))
	}
	world, err := newSimulatedWorld(tape)
	if err != nil {
		return closeWith(err)
	}
	runtime := &controlPlane{
		storage:    storage,
		world:      world,
		workspaces: workspaces,
		prewarm:    prewarmPolicy(tape.InitialWorld.Prewarm),
		janitor:    janitor.New(world, janitor.WithEventLog(storage.EventLog())),
	}
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
	case EventRunCancelled:
		if err := runtime.handleRunCancellation(ctx, event); err != nil {
			return err
		}
		return runtime.applyEventFaults(ctx)
	case EventCapacityPreempted:
		if err := runtime.handleCapacityPreemption(ctx, event); err != nil {
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
	if err := runtime.admitRun(ctx, arrival); err != nil {
		return err
	}
	_, err := runtime.orchestrator.Prewarm(ctx)
	return err
}

// prewarmPolicy is the Blueprint's bounds as the control plane's own restraint.
func prewarmPolicy(spec *scenario.PrewarmSpec) orchestrator.PrewarmPolicy {
	if spec == nil {
		return orchestrator.PrewarmPolicy{}
	}
	return orchestrator.PrewarmPolicy{
		MaxConcurrent: spec.MaxConcurrent,
		MinInterval:   spec.MinInterval.Duration(),
	}
}

// admitRun submits the arrival to Mercator. Every Run a Blueprint declares
// enters the control plane when it arrives, whatever its inputs are worth:
// whether it may be placed is Mercator's own decision, made against the object
// store, and a harness that withheld the Run would be answering that question on
// Mercator's behalf and hiding the Run from every rule that watches admitted
// work make progress.
// handleRunCancellation is the caller withdrawing work it asked for. Mercator
// answers it the way the public API does, because that is the path an operator
// takes: the Run is cancelled, its queued Booking is released, and the next
// reconciliation of the desired preparation set no longer names its content.
func (runtime *controlPlane) handleRunCancellation(ctx context.Context, event WorldEvent) error {
	var cancellation RunCancellation
	if err := json.Unmarshal(event.Data, &cancellation); err != nil {
		return fmt.Errorf("decode Run cancellation event %q: %w", event.ID, err)
	}
	workspace := workspaceID(cancellation.Workspace)
	if _, err := runtime.orchestrator.CancelRun(ctx, workspace, "run-"+cancellation.Name, nil); err != nil {
		return fmt.Errorf("cancel Lab Run %q: %w", cancellation.Name, err)
	}
	return runtime.advanceWorkspace(ctx, workspace)
}

// handleCapacityPreemption is the provider taking a machine back. Nothing is asked
// of Mercator and nothing is told to it: the world removes the capacity and the
// executions on it, and then every tenant's open Runs are advanced, which is the
// sweep that finds the launch missing. That is how a control plane learns of a
// reclamation it was never notified of, and it is the only way it can learn of one
// here, because a provider that has taken its machine back answers no differently
// from a provider whose machine finished the work.
func (runtime *controlPlane) handleCapacityPreemption(ctx context.Context, event WorldEvent) error {
	var preemption CapacityPreemption
	if err := json.Unmarshal(event.Data, &preemption); err != nil {
		return fmt.Errorf("decode capacity preemption event %q: %w", event.ID, err)
	}
	if err := runtime.world.preemptCapacity(preemption.Rental); err != nil {
		return err
	}
	for _, workspace := range runtime.workspaces {
		if err := runtime.advanceWorkspace(ctx, workspace); err != nil {
			return err
		}
	}
	return nil
}

func (runtime *controlPlane) admitRun(ctx context.Context, arrival RunArrival) error {
	runID := "run-" + arrival.Name
	if err := runtime.world.prepareRun(runID, arrival); err != nil {
		return err
	}
	workspace := workspaceID(arrival.Workspace)
	if _, err := runtime.orchestrator.CreateRun(ctx, orchestrator.CreateRunRequest{
		WorkspaceID:    workspace,
		RunID:          runID,
		IdempotencyKey: "create:" + runID,
		Workload:       scenario.WorkloadForRun(workspace, runID, arrival.Request),
	}); err != nil {
		return fmt.Errorf("create Lab Run %q: %w", arrival.Name, err)
	}
	if err := runtime.orchestrator.AdvanceRun(ctx, workspace, runID); err != nil {
		if !errors.Is(err, adapter.ErrLaunchIndeterminate) {
			return fmt.Errorf("advance Lab Run %q: %w", arrival.Name, err)
		}
		if err := runtime.orchestrator.AdvanceRun(ctx, workspace, runID); err != nil {
			return fmt.Errorf("reconcile ambiguous Lab Run %q: %w", arrival.Name, err)
		}
	}
	return nil
}

func (runtime *controlPlane) advance(ctx context.Context, now time.Time) error {
	runtime.world.setNow(now)
	if err := runtime.deliverReadiness(ctx); err != nil {
		return err
	}
	for _, workspace := range runtime.workspaces {
		if err := runtime.advanceWorkspace(ctx, workspace); err != nil {
			return err
		}
	}
	// Preparation is reconciled after every tenant's Runs have moved, because
	// what Mercator wants prepared is derived from where they ended up: a Booking
	// that was just dispatched is no longer speculative, and a Run that was just
	// cancelled is no longer worth a byte. It is one pass over the fleet because
	// the bounds it stays inside are the fleet's.
	if _, err := runtime.orchestrator.Prewarm(ctx); err != nil {
		return err
	}
	return runtime.applyEventFaults(ctx)
}

// deliverReadiness is the applications in this world calling Mercator to say they
// can do work. It runs before the Runs are advanced, because a readiness that has
// arrived is a fact about a Run the same sweep then reasons over.
//
// It is an inbound call rather than something read off an observation, because
// that is what application readiness is: the workload is the only authority, and
// routing it through the provider seam would make a running process and a serving
// one the same fact again.
func (runtime *controlPlane) deliverReadiness(ctx context.Context) error {
	for _, report := range runtime.world.dueReadinessReports() {
		ready, err := orchestrator.NewApplicationReadyReport(report.ReadyAt)
		if err != nil {
			return err
		}
		if err := runtime.orchestrator.RecordReport(ctx, report.WorkspaceID, report.RunID, ready); err != nil {
			return fmt.Errorf("report Lab readiness for Run %q: %w", report.RunID, err)
		}
	}
	return nil
}

// advanceWorkspace drives one tenant's open Runs. An ambiguous launch is
// reconciled by advancing again, which is what a control plane does with a
// response it never got.
func (runtime *controlPlane) advanceWorkspace(ctx context.Context, workspace string) error {
	_, err := runtime.orchestrator.AdvanceOpenRuns(ctx, workspace)
	if errors.Is(err, adapter.ErrLaunchIndeterminate) {
		_, err = runtime.orchestrator.AdvanceOpenRuns(ctx, workspace)
	}
	if err != nil {
		return err
	}
	// The orphan sweep is last because what capacity Mercator holds live work for
	// is whatever the Runs just settled into, so a sweep that ran first would find
	// a machine orphaned that a Run was about to be launched on.
	_, err = runtime.janitor.Sweep(ctx, workspace)
	return err
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
		orchestrator.WithArtifactCatalog(runtime.world),
		orchestrator.WithPrewarm(runtime.world, runtime.prewarm, runtime.storage.Preparation()),
		orchestrator.WithRentalSchedules(runtime.storage.RentalSchedules()),
		orchestrator.WithRunProjection(runtime.storage.Runs()),
	)
}

// mercatorEvents is Mercator's whole public record for this execution, read one
// tenant at a time and merged on the log's own global order. Reading with no
// workspace filter would be one query and would also pick up whatever else ever
// lands in this log, so the workspaces this execution created are named
// explicitly.
func (runtime *controlPlane) mercatorEvents(ctx context.Context) ([]eventlog.StoredEvent, error) {
	var events []eventlog.StoredEvent
	for _, workspace := range runtime.workspaces {
		filter := eventlog.EventFilter{WorkspaceID: workspace}
		head, err := runtime.storage.EventLog().LatestPosition(ctx, filter)
		if err != nil {
			return nil, err
		}
		for event, err := range eventlog.ScanAll(ctx, runtime.storage.EventLog(), head, filter) {
			if err != nil {
				return nil, err
			}
			events = append(events, event)
		}
	}
	slices.SortStableFunc(events, func(left, right eventlog.StoredEvent) int {
		return int(left.GlobalPosition) - int(right.GlobalPosition)
	})
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
