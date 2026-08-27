package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/adapter/fake"
	"github.com/benngarcia/mercator/internal/capability"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
	"github.com/benngarcia/mercator/internal/node"
	"github.com/benngarcia/mercator/internal/scheduler"
)

// TestTheInvitationCarriesWhatTheListingChargesForTheMachine is the price half of
// provisioning. Placement weighs an enrolled node against fresh capacity by what
// holding it costs, and a node invited without a price is refused rather than
// treated as free, so a machine born unpriced is a machine that can never be
// weighed against anything again.
//
// The rate is read from the listing rather than from anywhere else, because the
// listing is the only thing that has quoted this machine at the moment Mercator
// commits to allocating it.
func TestTheInvitationCarriesWhatTheListingChargesForTheMachine(t *testing.T) {
	ctx := context.Background()
	seam := newRecordingCapacity()
	orch := newProvisioningOrchestrator(t, seam, provisionableOfferAt(2.5))
	createRun(t, ctx, orch)

	if err := orch.AdvanceRun(ctx, "run_1"); err != nil {
		t.Fatalf("advance: %v", err)
	}

	invitations := seam.invitations()
	if len(invitations) != 1 {
		t.Fatalf("the machine was invited %d times, and one placement builds one machine", len(invitations))
	}
	if invitations[0].ShadowPriceUSDPerHour != 2.5 {
		t.Fatalf("the node was invited at %v an hour, and its listing charges 2.5",
			invitations[0].ShadowPriceUSDPerHour)
	}
	if invitations[0].RentalID == "" || invitations[0].NodeID == "" {
		t.Fatalf("the invitation names Rental %q and node %q, and a machine is invited as both",
			invitations[0].RentalID, invitations[0].NodeID)
	}
}

// TestTheProviderIsHandedTheBootstrapVerbatim is the other half of the same act.
// A CapacityProvider delivers the bootstrap through whatever mechanism it has and
// never interprets it, so what reaches it has to be exactly what the registry
// minted: the identity the machine will enrol as, the generation it is bound to,
// and the material that identity is redeemed with.
func TestTheProviderIsHandedTheBootstrapVerbatim(t *testing.T) {
	ctx := context.Background()
	seam := newRecordingCapacity()
	orch := newProvisioningOrchestrator(t, seam, provisionableOfferAt(1))
	createRun(t, ctx, orch)

	if err := orch.AdvanceRun(ctx, "run_1"); err != nil {
		t.Fatalf("advance: %v", err)
	}

	commands := seam.provisions()
	if len(commands) != 1 {
		t.Fatalf("the provider was asked for %d machines, and one placement asks for one", len(commands))
	}
	minted := seam.invitations()[0]
	bootstrap := commands[0].Bootstrap
	if bootstrap.NodeID != minted.NodeID || bootstrap.RentalID != minted.RentalID {
		t.Fatalf("the provider was handed node %q of Rental %q, and the registry minted %q of %q",
			bootstrap.NodeID, bootstrap.RentalID, minted.NodeID, minted.RentalID)
	}
	if bootstrap.EnrollmentToken == "" {
		t.Fatal("the provider was handed no enrollment token, and a machine with nothing to redeem never enrols")
	}
	// The token reaches the provider and stops there. Every event this Run records
	// is read back and none of them may carry it, because a Run Bundle is exported
	// from exactly this stream.
	assertTokenAbsentFromTheRecord(t, ctx, orch, bootstrap.EnrollmentToken)
}

// TestAProvisionWhoseAnswerWasLostAllocatesNoSecondMachine is the reconciliation
// the capacity contract exists for. A provider that allocated a machine and could
// not tell Mercator so leaves Mercator's own record saying exactly what it says
// when nothing was ever sent, so the record cannot be the thing that tells them
// apart. The Rental identity travels to the provider so this question has an
// answer, and the owned-capacity sweep is where it is asked.
func TestAProvisionWhoseAnswerWasLostAllocatesNoSecondMachine(t *testing.T) {
	ctx := context.Background()
	seam := newRecordingCapacity()
	// The machine is allocated and the answer never comes back, exactly once.
	seam.loseTheNextAnswer()
	orch := newProvisioningOrchestrator(t, seam, provisionableOfferAt(1))
	createRun(t, ctx, orch)

	if err := orch.AdvanceRun(ctx, "run_1"); err == nil {
		t.Fatal("the lost provision was reported as a success, and a control plane that cannot tell has to say so")
	}
	if err := orch.AdvanceRun(ctx, "run_1"); err != nil {
		t.Fatalf("reconcile the lost provision: %v", err)
	}

	if allocated := seam.allocatedMachines(); allocated != 1 {
		t.Fatalf("the provider is holding %d machines for one Run, and a lost answer is not a second machine", allocated)
	}
	accepted := recordedCapacityAcceptance(t, ctx, orch)
	if !accepted.Adopted {
		t.Fatalf("the machine was recorded as %+v, and this one was found by asking what the connection owns", accepted)
	}
}

// TestTheThreeProvisioningStagesAreMeasuredRatherThanDeclared is what separates a
// machine Mercator watched from one a fixture described. Each stage is recorded
// when the authority that owns it says it finished: the provider answers for the
// allocation and the boot, and only the node registry can answer for the agent.
// The seconds are the difference between two moments this Run's own stream
// carries, so the estimate the listing published is measured against something
// rather than against itself.
//
// Every look here lands deliberately late. The machine is allocated at thirty
// seconds and nobody looks until thirty seven, it is up at four and a half
// minutes and nobody looks until four fifty, its agent arrives at five fifteen
// and nobody looks until six. A control plane that dated a stage from its own
// look would record 37, 253 and 70, which is this case's whole point: those three
// numbers are a property of when Mercator asked, and a calibration trained on
// them would learn the reconcile cadence.
func TestTheThreeProvisioningStagesAreMeasuredRatherThanDeclared(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	clock := &steppingClock{now: start}
	seam := newRecordingCapacity().runsOn(clock.Now)
	seam.holdAt(capability.CapacityStateRequested)
	orch := New(openOrchestratorLog(t), scheduler.New(), fake.New(
		fake.WithOffers([]domain.OfferSnapshot{provisionableOfferAt(1)}),
		fake.WithLaunchOutcome(adapter.ExternalPhaseRunning),
	), WithCapacity(seam), WithInviter(seam), WithClock(clock.Now))
	createRun(t, ctx, orch)

	if err := orch.AdvanceRun(ctx, "run_1"); err != nil {
		t.Fatalf("allocate the machine: %v", err)
	}
	reach(t, ctx, orch, clock, seam.acquiredAt(start.Add(30*time.Second)), start.Add(37*time.Second))
	reach(t, ctx, orch, clock, seam.bootedAt(start.Add(4*time.Minute+30*time.Second)), start.Add(4*time.Minute+50*time.Second))
	reach(t, ctx, orch, clock, seam.agentArrivedAt(start.Add(5*time.Minute+15*time.Second)), start.Add(6*time.Minute))

	measured := recordedStages(t, ctx, orch)
	want := map[domain.LaunchStage]float64{
		domain.StageAcquisition: 30,
		domain.StageBoot:        240,
		domain.StageAgentReady:  45,
	}
	for stage, seconds := range want {
		if measured[stage].Seconds != seconds {
			t.Errorf("the %s stage was recorded as %vs, and this machine spent %vs on it",
				stage, measured[stage].Seconds, seconds)
		}
		if measured[stage].Bounded {
			t.Errorf("the %s stage is recorded as a bound, and its authority dated the transition", stage)
		}
	}
	if len(measured) != len(want) {
		t.Fatalf("the record holds %d provisioning actuals, and a machine goes through three", len(measured))
	}
}

// TestAStageNoAuthorityDatesIsRecordedAsABound is the other half, and the reason
// the record carries the distinction at all. A provider that reports what a
// machine is doing without saying since when leaves Mercator with the interval
// between two looks, and that interval is an upper bound on the stage rather than
// its duration. It is written down as one, so a reader can tell a machine that
// took thirty seconds from a machine that was found finished by a look thirty
// seconds after the last.
func TestAStageNoAuthorityDatesIsRecordedAsABound(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	clock := &steppingClock{now: start}
	seam := newRecordingCapacity().runsOn(clock.Now).datesNothing()
	seam.holdAt(capability.CapacityStateRequested)
	orch := New(openOrchestratorLog(t), scheduler.New(), fake.New(
		fake.WithOffers([]domain.OfferSnapshot{provisionableOfferAt(1)}),
		fake.WithLaunchOutcome(adapter.ExternalPhaseRunning),
	), WithCapacity(seam), WithInviter(seam), WithClock(clock.Now))
	createRun(t, ctx, orch)

	if err := orch.AdvanceRun(ctx, "run_1"); err != nil {
		t.Fatalf("allocate the machine: %v", err)
	}
	reach(t, ctx, orch, clock, seam.acquiredAt(start.Add(30*time.Second)), start.Add(37*time.Second))

	acquisition := recordedStages(t, ctx, orch)[domain.StageAcquisition]
	if !acquisition.Bounded {
		t.Fatal("a stage Mercator only knows had finished by the time it looked is recorded as though the machine had been timed")
	}
	if acquisition.Seconds != 37 {
		t.Fatalf("the bound is recorded at %vs, and the whole interval this look established is 37s", acquisition.Seconds)
	}
}

// reach is one thing happening to the machine at its own moment and Mercator
// looking at a later one, which is the arrangement every case above is about.
func reach(t *testing.T, ctx context.Context, orch *Orchestrator, clock *steppingClock, world func(), looked time.Time) {
	t.Helper()
	world()
	clock.stopAt(looked)
	if err := orch.AdvanceRun(ctx, "run_1"); err != nil {
		t.Fatalf("look at the machine: %v", err)
	}
}

func newProvisioningOrchestrator(t *testing.T, seam *recordingCapacity, offer domain.OfferSnapshot) *Orchestrator {
	t.Helper()
	return New(openOrchestratorLog(t), scheduler.New(), fake.New(
		fake.WithOffers([]domain.OfferSnapshot{offer}),
		fake.WithLaunchOutcome(adapter.ExternalPhaseRunning),
	), WithCapacity(seam), WithInviter(seam))
}

func provisionableOfferAt(ratePerHourUSD float64) domain.OfferSnapshot {
	offer := orchProvisionableOffer("off_prov", time.Now().UTC())
	offer.Pricing = domain.PriceModel{Currency: "USD", RatePerSecondUSD: ratePerHourUSD / 3600, Known: true}
	return offer
}

// steppingClock is the scripted clock these cases measure against, so a stage's
// duration is stated rather than waited for.
type steppingClock struct {
	mu  sync.Mutex
	now time.Time
}

func (clock *steppingClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *steppingClock) step(by time.Duration) {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.now = clock.now.Add(by)
}

// stopAt puts the clock at one named moment, which is how a case says that the
// world reached a state at one time and Mercator looked at another.
func (clock *steppingClock) stopAt(moment time.Time) {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.now = moment
}

// recordingCapacity is a provider these cases can hold still. It answers the
// capacity contract honestly, remembers what it was asked, and lets a case decide
// how far the machine has got and whether its answer came back.
type recordingCapacity struct {
	mu sync.Mutex
	// now is this provider's own clock, so the moments it dates its transitions
	// with are the world's rather than the caller's. A double that dated them
	// from the look would answer whatever the look wanted to hear.
	now              func() time.Time
	state            capability.CapacityState
	stateSince       time.Time
	dates            bool
	enrolledAt       time.Time
	enrolled         bool
	loseNext         bool
	terminateRefused bool
	machines         map[string]string
	nodes            map[string]node.Invitation
	invited          []node.Invitation
	provisioned      []capability.ProvisionCommand
	terminations     []capability.CapacityCommand
}

func newRecordingCapacity() *recordingCapacity {
	return &recordingCapacity{
		now:      func() time.Time { return time.Now().UTC() },
		state:    capability.CapacityStateActive,
		dates:    true,
		enrolled: true,
		machines: map[string]string{},
		nodes:    map[string]node.Invitation{},
	}
}

// runsOn puts this provider on the case's own clock, so a machine that reaches a
// state at one moment and is looked at another is a world a case can arrange.
func (c *recordingCapacity) runsOn(clock func() time.Time) *recordingCapacity {
	c.now = clock
	c.stateSince = clock()
	return c
}

// datesNothing is the provider that reports what a machine is doing and never
// when it started doing it, which is a real product and the reason a record has
// to be able to say that its seconds are a bound.
func (c *recordingCapacity) datesNothing() *recordingCapacity {
	c.dates = false
	return c
}

func (c *recordingCapacity) holdAt(state capability.CapacityState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state = state
	c.stateSince = c.now()
	c.enrolled = false
	c.enrolledAt = time.Time{}
}

// acquiredAt, bootedAt and agentArrivedAt are the three things that happen to a
// machine, each at the moment it really happened. The moment is stated rather
// than read off the clock, because these cases are about the gap between when a
// machine reached a state and when Mercator looked, and a double that dated its
// own transitions from the look could not hold one open.
func (c *recordingCapacity) acquiredAt(moment time.Time) func() {
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.state = capability.CapacityStateStarting
		c.stateSince = moment
	}
}

func (c *recordingCapacity) bootedAt(moment time.Time) func() {
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.state = capability.CapacityStateActive
		c.stateSince = moment
	}
}

func (c *recordingCapacity) agentArrivedAt(moment time.Time) func() {
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.enrolled = true
		c.enrolledAt = moment
	}
}

func (c *recordingCapacity) refuseTerminate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.terminateRefused = true
}

func (c *recordingCapacity) enrolTheAgent() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.enrolled = true
	c.enrolledAt = c.now()
}

func (c *recordingCapacity) loseTheNextAnswer() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loseNext = true
}

func (c *recordingCapacity) invitations() []node.Invitation {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]node.Invitation(nil), c.invited...)
}

func (c *recordingCapacity) provisions() []capability.ProvisionCommand {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]capability.ProvisionCommand(nil), c.provisioned...)
}

func (c *recordingCapacity) allocatedMachines() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.machines)
}

func (c *recordingCapacity) ProvisionCapacity(_ context.Context, command capability.ProvisionCommand) (capability.CapacityReceipt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.provisioned = append(c.provisioned, command)
	c.machines[command.RentalID] = "machine-" + command.RentalID
	if c.loseNext {
		// The machine exists and the answer does not come back, which is the only
		// world in which the sweep below is the difference between one machine and
		// two.
		c.loseNext = false
		return capability.CapacityReceipt{}, fmt.Errorf("the provider took the allocation and the answer was lost")
	}
	return capability.CapacityReceipt{
		NativeRef:  c.machines[command.RentalID],
		State:      c.state,
		AcceptedAt: time.Now().UTC(),
	}, nil
}

func (c *recordingCapacity) ObserveCapacity(_ context.Context, ref capability.CapacityRef) (capability.CapacityObservation, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	native, exists := c.machines[ref.RentalID]
	if !exists {
		return capability.CapacityObservation{}, fmt.Errorf("nothing allocated for Rental %q", ref.RentalID)
	}
	observation := capability.CapacityObservation{NativeRef: native, State: c.state, ObservedAt: c.now()}
	if c.dates {
		observation.StateSince = c.stateSince
	}
	return observation, nil
}

func (c *recordingCapacity) TerminateCapacity(_ context.Context, command capability.CapacityCommand) (capability.CapacityReceipt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.terminations = append(c.terminations, command)
	if c.terminateRefused {
		return capability.CapacityReceipt{}, fmt.Errorf("the provider refused to destroy %q", command.RentalID)
	}
	delete(c.machines, command.RentalID)
	return capability.CapacityReceipt{State: capability.CapacityStateTerminated}, nil
}

func (c *recordingCapacity) ListOwnedCapacity(_ context.Context, query capability.OwnershipQuery) ([]capability.OwnedCapacity, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var owned []capability.OwnedCapacity
	for rentalID, native := range c.machines {
		owned = append(owned, capability.OwnedCapacity{
			NativeRef: native,

			RentalID:  rentalID,
			State:     c.state,
			CreatedAt: time.Now().UTC(),
		})
	}
	return owned, nil
}

func (c *recordingCapacity) Invite(_ context.Context, invitation node.Invitation) (capability.NodeBootstrap, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.nodes[invitation.NodeID]; exists {
		return capability.NodeBootstrap{}, fmt.Errorf("%w: %s", node.ErrIdentityExists, invitation.NodeID)
	}
	c.nodes[invitation.NodeID] = invitation
	c.invited = append(c.invited, invitation)
	return c.bootstrapFor(invitation.NodeID), nil
}

func (c *recordingCapacity) Reinvite(_ context.Context, nodeID string, _ time.Time) (capability.NodeBootstrap, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.nodes[nodeID]; !exists {
		return capability.NodeBootstrap{}, fmt.Errorf("%w: %s", node.ErrNotFound, nodeID)
	}
	return c.bootstrapFor(nodeID), nil
}

func (c *recordingCapacity) bootstrapFor(nodeID string) capability.NodeBootstrap {
	invitation := c.nodes[nodeID]
	return capability.NodeBootstrap{
		NodeID:          nodeID,
		RentalID:        invitation.RentalID,
		Generation:      invitation.Generation,
		EnrollmentToken: "enrolment-secret-for-" + nodeID,
	}
}

func (c *recordingCapacity) EnrolledAt(_ context.Context, _ capability.NodeRef) (time.Time, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.enrolled {
		return time.Time{}, nil
	}
	if c.enrolledAt.IsZero() {
		return c.now(), nil
	}
	return c.enrolledAt, nil
}

func recordedCapacityAcceptance(t *testing.T, ctx context.Context, orch *Orchestrator) capacityAcceptedData {
	t.Helper()
	var accepted capacityAcceptedData
	found := false
	for _, event := range runEventsFor(t, ctx, orch) {
		if event.Type != EventCapacityAccepted {
			continue
		}
		if err := json.Unmarshal(event.Data, &accepted); err != nil {
			t.Fatalf("decode accepted capacity: %v", err)
		}
		found = true
	}
	if !found {
		t.Fatal("the Run records no accepted capacity, and it was placed on a machine to allocate")
	}
	return accepted
}

func recordedStages(t *testing.T, ctx context.Context, orch *Orchestrator) map[domain.LaunchStage]capacityStageObservedData {
	t.Helper()
	measured := map[domain.LaunchStage]capacityStageObservedData{}
	for _, event := range runEventsFor(t, ctx, orch) {
		if event.Type != EventCapacityStageObserved {
			continue
		}
		var stage capacityStageObservedData
		if err := json.Unmarshal(event.Data, &stage); err != nil {
			t.Fatalf("decode observed stage: %v", err)
		}
		measured[stage.Stage] = stage
	}
	return measured
}

func assertTokenAbsentFromTheRecord(t *testing.T, ctx context.Context, orch *Orchestrator, token string) {
	t.Helper()
	for _, event := range runEventsFor(t, ctx, orch) {
		if bytesContain(event.Data, token) || bytesContain(event.PrivateData, token) {
			t.Fatalf("event %q carries the enrollment token, and a short-lived credential belongs nowhere in the record", event.Type)
		}
	}
}

func bytesContain(data []byte, needle string) bool {
	return len(needle) > 0 && len(data) > 0 && stringContains(string(data), needle)
}

func stringContains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

func runEventsFor(t *testing.T, ctx context.Context, orch *Orchestrator) []eventlog.StoredEvent {
	t.Helper()
	events, err := orch.GetRunEvents(ctx, "run_1")
	if err != nil {
		t.Fatalf("read run events: %v", err)
	}
	return events
}

// TestTheWorkDoesNotMoveUntilTheBillEnds is the clause that makes a recorded
// reclamation worth reading. The record of giving a machine back is written after
// the provider confirms the terminate and never before, so a provider that
// refuses leaves the Run exactly where it was: still holding the machine, still
// answered by the decision that chose it, and still costing what it costs.
//
// Recording the reclamation first and retrying the terminate afterwards would
// produce the same Run stream in both worlds, and the difference between them is
// a machine an operator is paying for.
func TestTheWorkDoesNotMoveUntilTheBillEnds(t *testing.T) {
	ctx := context.Background()
	clock := &steppingClock{now: time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)}
	seam := newRecordingCapacity()
	seam.holdAt(capability.CapacityStateActive)
	seam.refuseTerminate()
	offer := provisionableOfferAt(1)
	offer.Bootstrap = &domain.CapacityBootstrap{EnrolmentDeadlineSeconds: 600}
	orch := New(openOrchestratorLog(t), scheduler.New(), fake.New(
		fake.WithOffers([]domain.OfferSnapshot{offer}),
		fake.WithLaunchOutcome(adapter.ExternalPhaseRunning),
	), WithCapacity(seam), WithInviter(seam), WithClock(clock.Now))
	createRun(t, ctx, orch)

	if err := orch.AdvanceRun(ctx, "run_1"); err != nil {
		t.Fatalf("allocate the machine: %v", err)
	}
	clock.step(11 * time.Minute)
	if err := orch.AdvanceRun(ctx, "run_1"); err == nil {
		t.Fatal("the reclamation was reported as done, and this provider refused to destroy the machine")
	}

	for _, event := range runEventsFor(t, ctx, orch) {
		if event.Type == EventCapacityReclaimed {
			t.Fatal("the Run records capacity handed back, and the machine is still allocated")
		}
	}
	if allocated := seam.allocatedMachines(); allocated != 1 {
		t.Fatalf("the provider holds %d machines, and the one it refused to destroy is still there", allocated)
	}
	if decisions := countEvents(runEventsFor(t, ctx, orch), EventBookingDecided); decisions != 1 {
		t.Fatalf("the Run holds %d decisions, and the work must not move off a machine still being paid for", decisions)
	}
}
