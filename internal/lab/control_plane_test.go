package lab

import (
	"context"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/eventlog"
	"github.com/benngarcia/mercator/internal/orchestrator"
	"github.com/benngarcia/mercator/internal/runprojection"
	"github.com/benngarcia/mercator/internal/scenario"
)

func TestExecutionRunsTheRealControlPlaneAndReconcilesLostLaunchResponse(t *testing.T) {
	execution := openDemoExecution(t)
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	if _, err := execution.Drive(context.Background(), Quiesce()); err != nil {
		t.Fatalf("drive arrivals: %v", err)
	}
	if _, err := execution.Drive(context.Background(), Advance(time.Hour)); err != nil {
		t.Fatalf("advance through first actual runtime: %v", err)
	}

	events, err := execution.runtime.mercatorEvents(context.Background())
	if err != nil {
		t.Fatalf("read Mercator events: %v", err)
	}
	assertEventType(t, events, orchestrator.EventRunRequested)
	assertEventType(t, events, orchestrator.EventBookingDecided)
	assertEventType(t, events, orchestrator.EventLaunchIndeterminate)
	assertEventType(t, events, orchestrator.EventExternalStateObserved)

	effects := execution.runtime.world.effectRecords()
	assertEffect(t, effects, OperationProviderLaunch, "run-producer", EffectCommandAccepted, EffectResponseLost)
	assertEffect(t, effects, OperationProviderObserve, "run-producer", EffectCommandAccepted, EffectResponseDelivered)
	if countEffects(effects, OperationProviderLaunch, "run-producer") != 1 {
		t.Fatalf("producer launch effects = %d, want 1", countEffects(effects, OperationProviderLaunch, "run-producer"))
	}

	page, err := execution.runtime.orchestrator.ListRuns(
		context.Background(),
		labWorkspace,
		runprojection.PageRequest{Limit: 50},
	)
	if err != nil {
		t.Fatalf("list projected Runs: %v", err)
	}
	if len(page.Records) != 2 {
		t.Fatalf("projected Runs = %d, want 2", len(page.Records))
	}
}

func TestRestartPreservesExternalResourcesWithoutRepeatingLaunch(t *testing.T) {
	execution := openDemoExecution(t)
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()
	if _, err := execution.Drive(context.Background(), Quiesce()); err != nil {
		t.Fatalf("drive arrivals: %v", err)
	}
	before := execution.runtime.world.truthSnapshot()
	launches := countEffects(execution.runtime.world.effectRecords(), OperationProviderLaunch, "")

	if err := execution.Restart(context.Background()); err != nil {
		t.Fatalf("restart control plane: %v", err)
	}

	after := execution.runtime.world.truthSnapshot()
	if len(after.ActiveExecutions) != len(before.ActiveExecutions) {
		t.Fatalf(
			"active executions changed across restart: %d vs %d",
			len(before.ActiveExecutions),
			len(after.ActiveExecutions),
		)
	}
	if got := countEffects(execution.runtime.world.effectRecords(), OperationProviderLaunch, ""); got != launches {
		t.Fatalf("restart repeated launch: %d effects before, %d after", launches, got)
	}

	if _, err := execution.Drive(context.Background(), Advance(time.Hour)); err != nil {
		t.Fatalf("advance through first actual runtime: %v", err)
	}
	if _, err := execution.Drive(context.Background(), Advance(time.Hour)); err != nil {
		t.Fatalf("advance through second actual runtime: %v", err)
	}
	page, err := execution.runtime.orchestrator.ListRuns(
		context.Background(),
		labWorkspace,
		runprojection.PageRequest{Limit: 50},
	)
	if err != nil {
		t.Fatalf("list projected Runs: %v", err)
	}
	for _, run := range page.Records {
		if !run.Closed {
			t.Fatalf("Run %q remained open after actual runtime", run.ID)
		}
	}
	if got := len(execution.runtime.world.truthSnapshot().ActiveExecutions); got != 0 {
		t.Fatalf("active executions after cleanup = %d, want 0", got)
	}
}

func TestEventFaultRestartsTheControlPlaneDeterministically(t *testing.T) {
	blueprint, err := scenario.LoadBlueprint("../scenario/scenarios/demos/artifact-warmth-restart.json")
	if err != nil {
		t.Fatalf("load Blueprint: %v", err)
	}
	tape, samples, err := Compile(blueprint, CompileOptions{})
	if err != nil {
		t.Fatalf("compile Blueprint: %v", err)
	}
	tape.Faults = []scenario.FaultSpec{{
		ID: "restart-after-launch",
		Trigger: scenario.FaultTriggerSpec{
			Event: orchestrator.EventLaunchAccepted,
		},
		Action: scenario.FaultRestartControlPlane,
	}}
	execution, err := Open(context.Background(), Config{
		Blueprint:        blueprint,
		Tape:             tape,
		Samples:          samples,
		Limits:           testLimits(),
		Policy:           "policy:test",
		MercatorRevision: "revision:test",
	})
	if err != nil {
		t.Fatalf("open execution: %v", err)
	}
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	if _, err := execution.Drive(context.Background(), Step()); err != nil {
		t.Fatalf("drive first arrival: %v", err)
	}

	if execution.runtime.restarts != 1 {
		t.Fatalf("control-plane restarts = %d, want 1", execution.runtime.restarts)
	}
	assertEffect(
		t,
		execution.runtime.world.effectRecords(),
		OperationControlPlaneRestart,
		labWorkspace,
		EffectCommandAccepted,
		EffectResponseDelivered,
	)
}

func openDemoExecution(t *testing.T) *Execution {
	t.Helper()
	blueprint, err := scenario.LoadBlueprint("../scenario/scenarios/demos/artifact-warmth-restart.json")
	if err != nil {
		t.Fatalf("load Blueprint: %v", err)
	}
	tape, samples, err := Compile(blueprint, CompileOptions{})
	if err != nil {
		t.Fatalf("compile Blueprint: %v", err)
	}
	execution, err := Open(context.Background(), Config{
		Blueprint:        blueprint,
		Tape:             tape,
		Samples:          samples,
		Limits:           testLimits(),
		Policy:           "policy:test",
		MercatorRevision: "revision:test",
	})
	if err != nil {
		t.Fatalf("open execution: %v", err)
	}
	return execution
}

func assertEventType(t *testing.T, events []eventlog.StoredEvent, eventType string) {
	t.Helper()
	for _, event := range events {
		if event.Type == eventType {
			return
		}
	}
	t.Fatalf("Mercator events have no %q", eventType)
}

func assertEffect(
	t *testing.T,
	effects []EffectRecord,
	operation string,
	correlationID string,
	command EffectCommand,
	response EffectResponse,
) {
	t.Helper()
	for _, effect := range effects {
		if effect.Operation == operation &&
			effect.CorrelationID == correlationID &&
			effect.Command == command &&
			effect.Response == response {
			return
		}
	}
	t.Fatalf(
		"effects have no operation=%q correlation=%q command=%q response=%q: %+v",
		operation,
		correlationID,
		command,
		response,
		effects,
	)
}

func countEffects(effects []EffectRecord, operation, correlationID string) int {
	count := 0
	for _, effect := range effects {
		if effect.Operation == operation && (correlationID == "" || effect.CorrelationID == correlationID) {
			count++
		}
	}
	return count
}
