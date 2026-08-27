package janitor

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/adapter/fake"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
)

// TestJanitorTerminatesCapacityMercatorCannotAccountFor is the terminate half of
// the policy. The provider is holding an execution whose Run this control plane
// has no record of at all, so nothing can ever be bound to it and nothing will
// ever collect it. Releasing only its slot, which is what a sweep with no stated
// policy did, leaves a machine billing that nothing in the fleet can use.
func TestJanitorTerminatesCapacityMercatorCannotAccountFor(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ad := fake.New()
	_, err := ad.Launch(ctx, adapter.LaunchRequest{
		OperationKey: "launch_orphan",
		RequestHash:  "sha256:orphan",

		RunID:              "run_orphan",
		AttemptID:          "att_orphan",
		OwnershipToken:     "own_orphan",
		LaunchKey:          "launch_orphan",
		CleanupLocator:     "cleanup_orphan",
		WorkloadID:         "wl_1",
		WorkloadRevisionID: "wrev_1",
	})
	if err != nil {
		t.Fatalf("seed orphan: %v", err)
	}

	log := openJanitorTestLog(t)

	result, err := New(ad, WithEventLog(log)).Sweep(ctx)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if result.Found != 1 || result.Terminated != 1 || result.Adopted != 0 {
		t.Fatalf("sweep result = %+v, want the unaccounted-for execution terminated", result)
	}
	if ad.TerminateCount() != 1 || ad.ReleaseCount() != 0 {
		t.Fatalf(
			"capacity nothing can be bound to was reclaimed with release=%d terminate=%d, want it destroyed",
			ad.ReleaseCount(), ad.TerminateCount(),
		)
	}
	convergence := onlyConvergence(t, log, "ws_1")
	if convergence.Policy != OrphanPolicy || convergence.Outcome != OrphanTerminated {
		t.Fatalf("the record says %+v, want the stated policy naming a termination", convergence)
	}
	if convergence.Reason != reasonNoRecordedRun {
		t.Fatalf("the record gives reason %q, want the Run nobody recorded", convergence.Reason)
	}
	owned, err := ad.ListOwned(ctx, adapter.OwnershipQuery{})
	if err != nil {
		t.Fatalf("list owned: %v", err)
	}
	if len(owned) != 0 {
		t.Fatalf("expected owned resources released, got %+v", owned)
	}
}

func TestJanitorSkipsActiveRunResources(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ad := fake.New()
	_, err := ad.Launch(ctx, adapter.LaunchRequest{
		OperationKey: "launch_active",
		RequestHash:  "sha256:active",

		RunID:              "run_active",
		AttemptID:          "att_active",
		OwnershipToken:     "own_active",
		LaunchKey:          "launch_active",
		CleanupLocator:     "cleanup_active",
		WorkloadID:         "wl_1",
		WorkloadRevisionID: "wrev_1",
	})
	if err != nil {
		t.Fatalf("seed active object: %v", err)
	}
	log := openJanitorTestLog(t)
	appendRunEvent(t, log, "ws_1", "run_active", "compute.run.requested.v1")

	result, err := New(ad, WithEventLog(log)).Sweep(ctx)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if result.Found != 1 || result.Converged() != 0 {
		t.Fatalf("live work should be found and left alone: %+v", result)
	}
	owned, err := ad.ListOwned(ctx, adapter.OwnershipQuery{})
	if err != nil {
		t.Fatalf("list owned: %v", err)
	}
	if len(owned) != 1 {
		t.Fatalf("expected active resource to remain, got %+v", owned)
	}
}

// TestJanitorAdoptsCapacityItsOwnRecordSaysSurvives is the adopt half. Mercator
// holds the launch this execution came from, and that record says the capacity is
// handed back by releasing the slot: the machine outlives the workload. So the
// slot goes back and the machine stays in the fleet, and the record says which
// policy kept it.
func TestJanitorAdoptsCapacityItsOwnRecordSaysSurvives(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ad := fake.New()
	_, err := ad.Launch(ctx, adapter.LaunchRequest{
		OperationKey: "launch_adopt",
		RequestHash:  "sha256:adopt",

		RunID:              "run_adopt",
		AttemptID:          "att_adopt",
		OwnershipToken:     "own_adopt",
		LaunchKey:          "launch_adopt",
		CleanupLocator:     "cleanup_adopt",
		WorkloadID:         "wl_1",
		WorkloadRevisionID: "wrev_1",
		Disposition:        domain.DispositionRelease,
	})
	if err != nil {
		t.Fatalf("seed adoptable execution: %v", err)
	}
	log := openJanitorTestLog(t)
	appendLaunchIntent(t, log, "ws_1", "run_adopt", domain.DispositionRelease)

	result, err := New(ad, WithEventLog(log)).Sweep(ctx)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}

	if result.Adopted != 1 || result.Terminated != 0 {
		t.Fatalf("sweep result = %+v, want the machine adopted rather than destroyed", result)
	}
	if ad.ReleaseCount() != 1 || ad.TerminateCount() != 0 {
		t.Fatalf(
			"adopted capacity was reclaimed with release=%d terminate=%d, want its slot released and the machine kept",
			ad.ReleaseCount(), ad.TerminateCount(),
		)
	}
	convergence := onlyConvergence(t, log, "ws_1")
	if convergence.Policy != OrphanPolicy || convergence.Outcome != OrphanAdopted {
		t.Fatalf("the record says %+v, want the stated policy naming an adoption", convergence)
	}
	if convergence.Reason != reasonRecordedRelease || convergence.LaunchKey != "launch_adopt" {
		t.Fatalf("the record gives %q for %q, want the recorded disposition for this capacity", convergence.Reason, convergence.LaunchKey)
	}
}

// TestJanitorTerminatesCapacityLeftBehindByAClosedRun is the case a sweep keyed
// on the cleanup request alone could only skip. The Run is over and Mercator
// never asked for its capacity back, which is what a control plane that died
// between closing a Run and reclaiming it leaves behind, and nothing else in the
// tree would ever have come for it.
func TestJanitorTerminatesCapacityLeftBehindByAClosedRun(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ad := fake.New()
	_, err := ad.Launch(ctx, adapter.LaunchRequest{
		OperationKey: "launch_closed",
		RequestHash:  "sha256:closed",

		RunID:              "run_closed",
		AttemptID:          "att_closed",
		OwnershipToken:     "own_closed",
		LaunchKey:          "launch_closed",
		CleanupLocator:     "cleanup_closed",
		WorkloadID:         "wl_1",
		WorkloadRevisionID: "wrev_1",
	})
	if err != nil {
		t.Fatalf("seed stranded execution: %v", err)
	}
	log := openJanitorTestLog(t)
	appendRunEvent(t, log, "ws_1", "run_closed", "compute.run.closed.v1")

	result, err := New(ad, WithEventLog(log)).Sweep(ctx)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}

	if result.Terminated != 1 {
		t.Fatalf("sweep result = %+v, want the capacity of a finished Run destroyed", result)
	}
	convergence := onlyConvergence(t, log, "ws_1")
	if convergence.Reason != reasonClosedWithoutAsking {
		t.Fatalf("the record gives reason %q, want the Run that closed with nothing asked for", convergence.Reason)
	}
}

// TestJanitorAdoptsCapacityAClosedRunLeftOnAMachineMercatorDoesNotOwn is the
// combination the policy is stated over and the cleanup request says nothing
// about. The Run reached a launch on a machine in a pool Mercator does not own,
// so its own record says the machine outlives the workload, and then it ended
// without anybody asking for the capacity back, which is the ordinary end of a
// launch whose attempts ran out.
//
// Reading the cleanup request first destroyed the whole machine here, and other
// work already placed on it lost its host.
func TestJanitorAdoptsCapacityAClosedRunLeftOnAMachineMercatorDoesNotOwn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ad := fake.New()
	_, err := ad.Launch(ctx, adapter.LaunchRequest{
		OperationKey: "launch_stranded",
		RequestHash:  "sha256:stranded",

		RunID:              "run_stranded",
		AttemptID:          "att_stranded",
		OwnershipToken:     "own_stranded",
		LaunchKey:          "launch_stranded",
		CleanupLocator:     "cleanup_stranded",
		WorkloadID:         "wl_1",
		WorkloadRevisionID: "wrev_1",
		Disposition:        domain.DispositionRelease,
	})
	if err != nil {
		t.Fatalf("seed stranded execution: %v", err)
	}
	log := openJanitorTestLog(t)
	appendLaunchThenClose(t, log, "ws_1", "run_stranded", domain.DispositionRelease)

	result, err := New(ad, WithEventLog(log)).Sweep(ctx)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}

	if result.Adopted != 1 || result.Terminated != 0 {
		t.Fatalf("sweep result = %+v, want the machine kept because the launch said it outlives the workload", result)
	}
	if ad.ReleaseCount() != 1 || ad.TerminateCount() != 0 {
		t.Fatalf(
			"capacity whose launch recorded release was reclaimed with release=%d terminate=%d, want its slot released and the machine kept",
			ad.ReleaseCount(), ad.TerminateCount(),
		)
	}
	convergence := onlyConvergence(t, log, "ws_1")
	if convergence.Outcome != OrphanAdopted || convergence.Reason != reasonRecordedRelease {
		t.Fatalf("the record says %+v, want an adoption on the recorded disposition", convergence)
	}
}

// TestJanitorReleasesTheSlotOfCapacityItsProviderCannotDestroy is the policy
// meeting a provider that holds no machine of Mercator's. Local Docker is one:
// it is a standing pool, so there is nothing to destroy and the container is the
// whole of what Mercator has there.
//
// Stopping at the provider's refusal left that container standing and returned an
// error before anything later in the same listing was looked at, so one object
// nothing could account for stopped every sweep of the deployment from then on.
func TestJanitorReleasesTheSlotOfCapacityItsProviderCannotDestroy(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ad := &standingPool{Adapter: fake.New()}
	_, err := ad.Launch(ctx, adapter.LaunchRequest{
		OperationKey: "launch_forgotten",
		RequestHash:  "sha256:forgotten",

		RunID:              "run_forgotten",
		AttemptID:          "att_forgotten",
		OwnershipToken:     "own_forgotten",
		LaunchKey:          "launch_forgotten",
		CleanupLocator:     "cleanup_forgotten",
		WorkloadID:         "wl_1",
		WorkloadRevisionID: "wrev_1",
	})
	if err != nil {
		t.Fatalf("seed capacity nothing recorded: %v", err)
	}
	log := openJanitorTestLog(t)

	result, err := New(ad, WithEventLog(log)).Sweep(ctx)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}

	if result.Terminated != 1 {
		t.Fatalf("sweep result = %+v, want the capacity nothing recorded gone", result)
	}
	if ad.ReleaseCount() != 1 {
		t.Fatalf("a provider that cannot destroy this capacity was asked to release it %d times, want once", ad.ReleaseCount())
	}
	owned, err := ad.ListOwned(ctx, adapter.OwnershipQuery{})
	if err != nil {
		t.Fatalf("list owned: %v", err)
	}
	if len(owned) != 0 {
		t.Fatalf("the provider still holds %+v, want the slot given back", owned)
	}
}

// TestJanitorRecordsItsDecisionBeforeItActsOnIt is the ordering the whole policy
// rests on. Reclaiming is not reversible and a machine that stops existing is
// never listed again, so a sweep that destroyed one and then failed to write down
// why would leave capacity gone under no stated rule, and no later sweep could
// repair it: the object it would have to explain is not there to be found.
//
// The provider fails the first reclaim. The decision is in the record anyway, the
// capacity is still there to be finished, and the sweep that finishes it acts on
// the decision already taken rather than writing a second one.
func TestJanitorRecordsItsDecisionBeforeItActsOnIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ad := &refusesFirstReclaim{Adapter: fake.New()}
	_, err := ad.Launch(ctx, adapter.LaunchRequest{
		OperationKey: "launch_unrecorded",
		RequestHash:  "sha256:unrecorded",

		RunID:              "run_unrecorded",
		AttemptID:          "att_unrecorded",
		OwnershipToken:     "own_unrecorded",
		LaunchKey:          "launch_unrecorded",
		CleanupLocator:     "cleanup_unrecorded",
		WorkloadID:         "wl_1",
		WorkloadRevisionID: "wrev_1",
	})
	if err != nil {
		t.Fatalf("seed capacity nothing recorded: %v", err)
	}
	log := openJanitorTestLog(t)
	janitor := New(ad, WithEventLog(log))

	if _, err := janitor.Sweep(ctx); err == nil {
		t.Fatal("the sweep hid a provider that would not reclaim the capacity")
	}

	decided := onlyConvergence(t, log, "ws_1")
	if decided.Outcome != OrphanTerminated || decided.Reason != reasonNoRecordedRun {
		t.Fatalf("the record says %+v, want the decision the failed sweep took", decided)
	}
	result, err := janitor.Sweep(ctx)
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if result.Terminated != 1 {
		t.Fatalf("second sweep result = %+v, want the capacity decided about finished", result)
	}
	if again := onlyConvergence(t, log, "ws_1"); again != decided {
		t.Fatalf("the record now says %+v, want the one decision already taken", again)
	}
}

// TestJanitorConvergesCapacityByTheLaunchThatTookIt is the policy meeting a Run
// that was launched more than once. The first attempt took a machine Mercator
// provisioned for it and was left behind by an indeterminate launch; the
// replacement took a slot in a pool Mercator does not own, and the Run ended
// there.
//
// The machine is the claim. Deciding this capacity by the Run's last launch reads
// the slot's rule over the provisioned machine, hands the slot back, and leaves
// the machine standing and billing with no Run that could ever be placed on it.
func TestJanitorConvergesCapacityByTheLaunchThatTookIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ad := fake.New()
	provisioned := attemptLaunch("run_replaced", "att_one", domain.DispositionTerminate)
	if _, err := ad.Launch(ctx, provisioned); err != nil {
		t.Fatalf("seed the machine the first attempt provisioned: %v", err)
	}
	log := openJanitorTestLog(t)
	appendLaunchesThenClose(t, log, "ws_1", "run_replaced",
		provisioned,
		attemptLaunch("run_replaced", "att_two", domain.DispositionRelease),
	)

	result, err := New(ad, WithEventLog(log)).Sweep(ctx)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}

	if result.Terminated != 1 || result.Adopted != 0 {
		t.Fatalf("sweep result = %+v, want the provisioned machine destroyed by its own launch's rule", result)
	}
	if ad.TerminateCount() != 1 || ad.ReleaseCount() != 0 {
		t.Fatalf(
			"the machine the first attempt provisioned was reclaimed with release=%d terminate=%d, want it destroyed",
			ad.ReleaseCount(), ad.TerminateCount(),
		)
	}
	convergence := onlyConvergence(t, log, "ws_1")
	if convergence.Outcome != OrphanTerminated || convergence.Reason != reasonRecordedTerminate {
		t.Fatalf("the record says %+v, want the disposition the launch that took this capacity recorded", convergence)
	}
}

// TestJanitorKeepsAMachineTheReplacedLaunchDidNotProvision is the same Run with
// its attempts the other way round. The capacity left behind is a slot in a pool
// Mercator does not own, and the replacement went on to provision a machine, so
// deciding by the last launch would destroy a machine Mercator has no right to
// and take every other Booking on it down with it.
func TestJanitorKeepsAMachineTheReplacedLaunchDidNotProvision(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ad := fake.New()
	borrowed := attemptLaunch("run_moved", "att_one", domain.DispositionRelease)
	if _, err := ad.Launch(ctx, borrowed); err != nil {
		t.Fatalf("seed the slot the first attempt borrowed: %v", err)
	}
	log := openJanitorTestLog(t)
	appendLaunchesThenClose(t, log, "ws_1", "run_moved",
		borrowed,
		attemptLaunch("run_moved", "att_two", domain.DispositionTerminate),
	)

	result, err := New(ad, WithEventLog(log)).Sweep(ctx)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}

	if result.Adopted != 1 || result.Terminated != 0 {
		t.Fatalf("sweep result = %+v, want the borrowed slot given back and its machine kept", result)
	}
	if ad.ReleaseCount() != 1 || ad.TerminateCount() != 0 {
		t.Fatalf(
			"a slot in a pool Mercator does not own was reclaimed with release=%d terminate=%d, want the slot released",
			ad.ReleaseCount(), ad.TerminateCount(),
		)
	}
}

// TestJanitorDestroysCapacityNoRecordedLaunchAccountsFor is the capacity that
// carries a Run identity and none of the launch identities Mercator minted for
// it. The Run's launches took capacity on opposite terms, so the record cannot
// say which of them this machine came from, and a policy that guessed would
// either keep a machine that must be destroyed or destroy a slot Mercator only
// borrowed.
func TestJanitorDestroysCapacityNoRecordedLaunchAccountsFor(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ad := fake.New()
	_, err := ad.Launch(ctx, adapter.LaunchRequest{
		OperationKey: "launch_unnamed",
		RequestHash:  "sha256:unnamed",

		RunID:              "run_disagreeing",
		AttemptID:          "att_the_provider_minted",
		OwnershipToken:     "own_unnamed",
		LaunchKey:          "launch_the_provider_minted",
		CleanupLocator:     "cleanup_unnamed",
		WorkloadID:         "wl_1",
		WorkloadRevisionID: "wrev_1",
	})
	if err != nil {
		t.Fatalf("seed capacity naming no launch: %v", err)
	}
	log := openJanitorTestLog(t)
	appendLaunchesThenClose(t, log, "ws_1", "run_disagreeing",
		attemptLaunch("run_disagreeing", "att_one", domain.DispositionTerminate),
		attemptLaunch("run_disagreeing", "att_two", domain.DispositionRelease),
	)

	result, err := New(ad, WithEventLog(log)).Sweep(ctx)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}

	if result.Terminated != 1 {
		t.Fatalf("sweep result = %+v, want capacity no launch accounts for destroyed", result)
	}
	convergence := onlyConvergence(t, log, "ws_1")
	if convergence.Reason != reasonNoLaunchAccountsFor {
		t.Fatalf("the record gives reason %q, want the launches that cannot account for it", convergence.Reason)
	}
}

// attemptLaunch is one attempt of a Run as Mercator recorded launching it, with
// the identities Mercator minted for that attempt's capacity.
func attemptLaunch(runID, attemptID string, disposition domain.Disposition) adapter.LaunchRequest {
	return adapter.LaunchRequest{
		OperationKey: "launch_" + attemptID,
		RequestHash:  "sha256:" + attemptID,

		RunID:              runID,
		AttemptID:          attemptID,
		OwnershipToken:     "own_" + attemptID,
		LaunchKey:          "launch_" + attemptID,
		CleanupLocator:     "cleanup_" + attemptID,
		WorkloadID:         "wl_1",
		WorkloadRevisionID: "wrev_1",
		Disposition:        disposition,
	}
}

// appendLaunchesThenClose is a Run that was launched once per attempt and then
// ended, which is what a Run replaced onto other capacity leaves in the record.
func appendLaunchesThenClose(t *testing.T, log eventlog.EventLog, fixtureID, runID string, launches ...adapter.LaunchRequest) {
	t.Helper()
	events := make([]eventlog.NewEvent, 0, len(launches)+1)
	for _, launch := range launches {
		private, err := json.Marshal(launch)
		if err != nil {
			t.Fatalf("marshal intent: %v", err)
		}
		event := runEvent(fixtureID, runID, "intent_"+launch.AttemptID, "compute.run.launch_intent_recorded.v1")
		event.PrivateData = private
		events = append(events, event)
	}
	appendRunHistory(t, log, fixtureID, runID,
		append(events, runEvent(fixtureID, runID, "closed", "compute.run.closed.v1"))...,
	)
}

// standingPool is a provider holding no machine of Mercator's, which is what
// local Docker is: there is a slot to give back and nothing to destroy.
type standingPool struct {
	*fake.Adapter
}

func (standingPool) Terminate(context.Context, adapter.TerminateRequest) (adapter.TerminateReceipt, error) {
	return adapter.TerminateReceipt{}, adapter.ErrTerminateUnsupported
}

// refusesFirstReclaim is a provider that fails the first cleanup it is asked for,
// which is what a sweep interrupted between deciding and acting meets.
type refusesFirstReclaim struct {
	*fake.Adapter
	asked bool
}

func (r *refusesFirstReclaim) Terminate(ctx context.Context, request adapter.TerminateRequest) (adapter.TerminateReceipt, error) {
	if !r.asked {
		r.asked = true
		return adapter.TerminateReceipt{}, errors.New("provider is unreachable")
	}
	return r.Adapter.Terminate(ctx, request)
}

// onlyConvergence is the one orphan decision this deployment's record holds. It
// reads the public log rather than the sweep's return value, because the record is
// what an operator and every rule about the policy actually see.
func onlyConvergence(t *testing.T, log eventlog.EventLog, fixtureID string) OrphanConvergence {
	t.Helper()
	head, err := log.LatestPosition(context.Background(), eventlog.EventFilter{})
	if err != nil {
		t.Fatalf("read log head: %v", err)
	}
	var found []OrphanConvergence
	for event, err := range eventlog.ScanAll(context.Background(), log, head, eventlog.EventFilter{}) {
		if err != nil {
			t.Fatalf("scan log: %v", err)
		}
		if event.Type != EventOrphanConverged {
			continue
		}
		var convergence OrphanConvergence
		if err := json.Unmarshal(event.Data, &convergence); err != nil {
			t.Fatalf("decode orphan convergence: %v", err)
		}
		found = append(found, convergence)
	}
	if len(found) != 1 {
		t.Fatalf("the record holds %d orphan decisions, want exactly one: %+v", len(found), found)
	}
	return found[0]
}

func TestJanitorRequiresEventLog(t *testing.T) {
	t.Parallel()
	_, err := New(fake.New()).Sweep(context.Background())
	if err == nil {
		t.Fatalf("expected missing event log error")
	}
}

func openJanitorTestLog(t *testing.T) *eventlog.SQLiteEventLog {
	t.Helper()
	log, err := eventlog.OpenSQLite(context.Background(), "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open event log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	return log
}

func appendRunEvent(t *testing.T, log eventlog.EventLog, fixtureID, runID, eventType string) {
	t.Helper()
	_, err := log.Append(context.Background(), eventlog.AppendRequest{
		Stream:                eventlog.StreamKey{Type: "run", ID: runID},
		ExpectedStreamVersion: 0,
		CommandKey:            "seed:" + eventType,
		RequestHash:           "sha256:seed",
		CorrelationID:         runID,
		CausationID:           "seed",
		Events: []eventlog.NewEvent{{
			ID:            "evt_" + fixtureID + "_" + runID + "_seed",
			Type:          eventType,
			SchemaVersion: 1,
			OccurredAt:    time.Now().UTC(),
			Visibility:    eventlog.VisibilityPublic,
			Data:          []byte(`{}`),
		}},
	})
	if err != nil {
		t.Fatalf("append run event: %v", err)
	}
}

func TestJanitorReclaimsViaRecordedTerminateDisposition(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ad := fake.New()
	_, err := ad.Launch(ctx, adapter.LaunchRequest{
		OperationKey: "launch_term",
		RequestHash:  "sha256:term",

		RunID:              "run_term",
		AttemptID:          "att_term",
		OwnershipToken:     "own_term",
		LaunchKey:          "launch_term",
		CleanupLocator:     "cleanup_term",
		WorkloadID:         "wl_1",
		WorkloadRevisionID: "wrev_1",
		Disposition:        domain.DispositionTerminate,
	})
	if err != nil {
		t.Fatalf("seed terminate orphan: %v", err)
	}
	log := openJanitorTestLog(t)
	appendLaunchIntent(t, log, "ws_1", "run_term", domain.DispositionTerminate)

	result, err := New(ad, WithEventLog(log)).Sweep(ctx)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if result.Terminated != 1 {
		t.Fatalf("expected one reclaim, got %+v", result)
	}
	if ad.TerminateCount() != 1 {
		t.Fatalf("janitor must reclaim a provisioned run via Terminate, terminate count=%d", ad.TerminateCount())
	}
	if ad.ReleaseCount() != 0 {
		t.Fatalf("janitor must not Release a provisioned run, release count=%d", ad.ReleaseCount())
	}
}

func TestJanitorRejectsCleanupWithoutRecordedDisposition(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ad := fake.New()
	_, err := ad.Launch(ctx, adapter.LaunchRequest{
		OperationKey: "launch_missing_disposition",
		RequestHash:  "sha256:missing_disposition",

		RunID:              "run_missing_disposition",
		AttemptID:          "att_missing_disposition",
		OwnershipToken:     "own_missing_disposition",
		LaunchKey:          "launch_missing_disposition",
		CleanupLocator:     "cleanup_missing_disposition",
		WorkloadID:         "wl_1",
		WorkloadRevisionID: "wrev_1",
	})
	if err != nil {
		t.Fatalf("seed owned resource: %v", err)
	}
	log := openJanitorTestLog(t)
	appendLaunchIntent(t, log, "ws_1", "run_missing_disposition", "")

	if _, err := New(ad, WithEventLog(log)).Sweep(ctx); err == nil {
		t.Fatal("janitor accepted cleanup without a recorded disposition")
	}
	if ad.ReleaseCount() != 0 || ad.TerminateCount() != 0 {
		t.Fatalf("invalid disposition reached provider cleanup: release=%d terminate=%d", ad.ReleaseCount(), ad.TerminateCount())
	}
}

// appendLaunchIntent is a Run that launched and whose capacity Mercator then
// asked back, which is how work ordinarily ends.
func appendLaunchIntent(t *testing.T, log eventlog.EventLog, fixtureID, runID string, disposition domain.Disposition) {
	t.Helper()
	appendRunHistory(t, log, fixtureID, runID,
		launchIntentEvent(t, runID, disposition),
		runEvent(fixtureID, runID, "cleanup", "compute.run.cleanup_requested.v1"),
	)
}

// appendLaunchThenClose is a Run that launched and then ended with nobody asking
// for its capacity back, which is what a launch whose attempts ran out leaves in
// the record.
func appendLaunchThenClose(t *testing.T, log eventlog.EventLog, fixtureID, runID string, disposition domain.Disposition) {
	t.Helper()
	appendRunHistory(t, log, fixtureID, runID,
		launchIntentEvent(t, runID, disposition),
		runEvent(fixtureID, runID, "closed", "compute.run.closed.v1"),
	)
}

func appendRunHistory(t *testing.T, log eventlog.EventLog, fixtureID, runID string, events ...eventlog.NewEvent) {
	t.Helper()
	_, err := log.Append(context.Background(), eventlog.AppendRequest{
		Stream:                eventlog.StreamKey{Type: "run", ID: runID},
		ExpectedStreamVersion: 0,
		CommandKey:            "seed:intent:" + runID,
		RequestHash:           "sha256:seed_intent",
		CorrelationID:         runID,
		CausationID:           "seed",
		Events:                events,
	})
	if err != nil {
		t.Fatalf("append run history: %v", err)
	}
}

func launchIntentEvent(t *testing.T, runID string, disposition domain.Disposition) eventlog.NewEvent {
	t.Helper()
	private, err := json.Marshal(adapter.LaunchRequest{
		AttemptID:   "att_" + runID,
		LaunchKey:   "launch_" + runID,
		Disposition: disposition,
	})
	if err != nil {
		t.Fatalf("marshal intent: %v", err)
	}
	event := runEvent("ws_1", runID, "intent", "compute.run.launch_intent_recorded.v1")
	event.PrivateData = private
	return event
}

func runEvent(fixtureID, runID, name, eventType string) eventlog.NewEvent {
	return eventlog.NewEvent{
		ID:            "evt_" + fixtureID + "_" + runID + "_" + name,
		Type:          eventType,
		SchemaVersion: 1,
		OccurredAt:    time.Now().UTC(),
		Visibility:    eventlog.VisibilityPublic,
		Data:          []byte(`{}`),
	}
}
