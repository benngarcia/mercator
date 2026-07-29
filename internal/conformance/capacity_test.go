package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter/fake"
	shadeformadapter "github.com/benngarcia/mercator/internal/adapter/shadeform"
	"github.com/benngarcia/mercator/internal/broker"
	"github.com/benngarcia/mercator/internal/capability"
	"github.com/benngarcia/mercator/internal/domain"
)

// TestACapacityTrialKeepsEveryPromiseAndGivesTheMachinesBack is the capacity
// mode end to end: a connection in the reusable lane, the shared suite run
// against it through the command's own runner, and evidence naming every promise
// and the Lab rule behind it.
func TestACapacityTrialKeepsEveryPromiseAndGivesTheMachinesBack(t *testing.T) {
	world := sellingWorld(t)
	runner := capacityRunner(t, world)

	evidence, err := runner.Verify(t.Context(), capacityTrial())

	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if evidence.Verdict != VerdictPassed {
		t.Fatalf("verdict = %q, evidence = %+v", evidence.Verdict, evidence)
	}
	if len(evidence.Promises) == 0 {
		t.Fatal("a capacity trial reported no promises at all")
	}
	for _, promise := range evidence.Promises {
		if promise.Outcome != PromiseKept {
			t.Errorf("%s = %s: %s", promise.Name, promise.Outcome, promise.Detail)
		}
		if promise.Rule == "" {
			t.Errorf("%s cites no Lab rule, so nothing tells a reader what it is the other half of", promise.Name)
		}
	}
	if evidence.Inventory.Owned != 0 {
		t.Errorf("the trial left %d machines owned", evidence.Inventory.Owned)
	}
	if evidence.Offer.ID == "" || evidence.Offer.MaximumCostUSD <= 0 {
		t.Errorf("the trial recorded no listing it rented from: %+v", evidence.Offer)
	}
}

// TestACapacityTrialReportsTheProviderThatBreaksAPromise is the same path with
// one clause of the contract broken, because evidence that only ever says
// "passed" is evidence of nothing.
func TestACapacityTrialReportsTheProviderThatBreaksAPromise(t *testing.T) {
	runner := capacityRunner(t, &neverConfirmsATerminate{World: sellingWorld(t)})

	evidence, err := runner.Verify(t.Context(), capacityTrial())

	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if evidence.Verdict != VerdictFailed || evidence.Failure == nil || evidence.Failure.Code != "CAPACITY_PROMISE_BROKEN" {
		t.Fatalf("evidence = %+v, want a broken capacity promise", evidence)
	}
	broken := map[string]PromiseEvidence{}
	for _, promise := range evidence.Promises {
		if promise.Outcome == PromiseBroken {
			broken[promise.Name] = promise
		}
	}
	if _, found := broken["terminate_is_confirmed_and_stays_confirmed"]; !found {
		t.Fatalf("broken promises = %v, want the terminate promise among them", broken)
	}
}

// TestTheSweepDestroysEveryMachineWearingOneLeasesTag is the sweep's own reason
// for existing, made to happen: an account holding two machines tagged for one
// Rental, which is what a create whose answer was lost leaves on a provider
// whose listing lags. Nothing in this trial rented them and no promise holds a
// ref to either, so the sweep is the only thing that can end them.
//
// The provider honours the operation key on a terminate, which is what
// CapacityCommand says a key does. A sweep keying its destructions on the lease
// alone destroys the first machine and is answered "duplicate" for the second,
// which goes on billing while the evidence reads clean.
func TestTheSweepDestroysEveryMachineWearingOneLeasesTag(t *testing.T) {
	provider := twoOrphansOfOneLease(sellingWorld(t))
	runner := capacityRunner(t, provider)

	evidence, err := runner.Verify(t.Context(), capacityTrial())

	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if running := provider.stillRunning(); len(running) != 0 {
		t.Errorf("machines wearing Rental %q's tag are still running: %v", orphanedRental, running)
	}
	if evidence.CleanupFailure != nil || evidence.Inventory.Owned != 0 {
		t.Errorf("the trial ended owning %d machines: %+v", evidence.Inventory.Owned, evidence.CleanupFailure)
	}
}

// TestACapacityTrialRefusesUnreachableCallbackTopologyBeforeRentingAnything is
// the acceptance the launch modes already hold, kept for the mode that rents
// machines. Every machine a capacity trial rents is handed a bootstrap naming
// the public origin, so a trial with nowhere to name is refused before it
// contacts the provider.
func TestACapacityTrialRefusesUnreachableCallbackTopologyBeforeRentingAnything(t *testing.T) {
	world := sellingWorld(t)
	tests := []struct {
		name   string
		config RunnerConfig
		want   string
	}{
		{name: "listener", config: RunnerConfig{}, want: "MERCATOR_CONFORMANCE_LISTEN_ADDR is required"},
		{name: "fixed port", config: RunnerConfig{ListenAddress: "0.0.0.0:0", PublicURL: "https://reports.example.com"}, want: "must use a fixed port"},
		{name: "public url", config: RunnerConfig{ListenAddress: "0.0.0.0:8082"}, want: "MERCATOR_CONFORMANCE_PUBLIC_URL is required"},
		{name: "origin", config: RunnerConfig{ListenAddress: "0.0.0.0:8082", PublicURL: "https://reports.example.com/callback"}, want: "must be an origin"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.config.Environment = map[string]string{credentialEnv: "shadeform-key"}
			runner := newRunner(test.config, withProviderFactory(capacityFactory(world)), withTempRoot(t.TempDir()))

			_, err := runner.Verify(t.Context(), capacityTrial())

			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Verify() error = %v, want containing %q", err, test.want)
			}
			if held := ownedBy(t, world); held != 0 {
				t.Fatalf("a refused trial rented %d machines", held)
			}
		})
	}
}

// TestTheShippedCapacityTrialIsBlockedByNameWithoutItsCredential is the live
// half of the suite when nothing can run it, read as an operator reads it: the
// shipped trial document, an environment holding no key, and evidence naming the
// variable that is missing rather than a provider contacted with an empty one.
//
// It runs against the production provider catalog on purpose and reaches no
// network, because the credential is refused before any adapter is built.
func TestTheShippedCapacityTrialIsBlockedByNameWithoutItsCredential(t *testing.T) {
	environment := map[string]string{
		"MERCATOR_CONFORMANCE_LISTEN_ADDR": "0.0.0.0:8082",
		"MERCATOR_CONFORMANCE_PUBLIC_URL":  "https://reports.example.com",
	}
	var stdout, stderr bytes.Buffer

	exitCode := RunCommand(t.Context(), []string{"--spec", "testdata/shadeform_capacity_trial.json"}, environment, &stdout, &stderr)

	if exitCode != 1 {
		t.Fatalf("RunCommand() = %d, stderr = %s", exitCode, stderr.String())
	}
	var evidence Evidence
	if err := json.Unmarshal(stdout.Bytes(), &evidence); err != nil {
		t.Fatalf("stdout = %q: %v", stdout.String(), err)
	}
	if evidence.Verdict != VerdictBlocked || evidence.Failure == nil {
		t.Fatalf("evidence = %+v, want a blocked verdict", evidence)
	}
	if !strings.Contains(evidence.Failure.Message, credentialEnv) {
		t.Fatalf("failure = %+v, want the missing credential named", evidence.Failure)
	}
	if len(evidence.Promises) != 0 {
		t.Fatalf("a trial that never ran reported %d promises", len(evidence.Promises))
	}
}

// TestACapacityTrialRefusesAConnectionThatSellsNoCapacity keeps the mode inside
// its lane. A one-shot executor has no machines to rent, and the trial says so
// rather than launching one workload and calling it a capacity result.
func TestACapacityTrialRefusesAConnectionThatSellsNoCapacity(t *testing.T) {
	factory := broker.NewFactory()
	factory.Register(shadeformadapter.Manifest(), func(map[string]string, string) (capability.Backend, error) {
		return fake.New(), nil
	})
	runner := newRunner(
		RunnerConfig{
			Environment:   map[string]string{credentialEnv: "shadeform-key"},
			ListenAddress: "0.0.0.0:8082",
			PublicURL:     "https://reports.example.com",
		},
		withProviderFactory(factory),
		withTempRoot(t.TempDir()),
	)

	evidence, err := runner.Verify(t.Context(), capacityTrial())

	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if evidence.Verdict != VerdictBlocked || evidence.Failure == nil || evidence.Failure.Code != "CONNECTION_SELLS_NO_CAPACITY" {
		t.Fatalf("evidence = %+v, want a blocked verdict naming the lane", evidence)
	}
}

// TestTheCapacityTrialDocumentNamesNoImage is the shipped example read the way
// an operator's copy of it will be. A capacity trial launches no workload, so an
// image on one is a promise nothing keeps.
func TestTheCapacityTrialDocumentNamesNoImage(t *testing.T) {
	trial, err := readTrial([]string{"--spec", "testdata/shadeform_capacity_trial.json"})

	if err != nil {
		t.Fatalf("read the shipped capacity trial: %v", err)
	}
	if trial.Mode != ModeCapacity || trial.Image != "" || trial.CredentialEnv != credentialEnv {
		t.Fatalf("trial = %+v", trial)
	}
	if err := ValidateTrial(trial, func(string) (string, bool) { return "shadeform-key", true }); err != nil {
		t.Fatalf("the shipped capacity trial does not validate: %v", err)
	}
	withImage := trial
	withImage.Image = "ghcr.io/benngarcia/mercator-conformance-probe@sha256:" + strings.Repeat("0", 64)
	if err := ValidateTrial(withImage, func(string) (string, bool) { return "shadeform-key", true }); err == nil {
		t.Fatal("a capacity trial naming an image was accepted, and nothing here launches one")
	}
}

// TestCapacityEvidenceSerializesEveryPromiseAndItsRule keeps the operator's copy
// of the result readable: the JSON names each promise, what became of it, and
// the Lab rule it is the higher-fidelity half of.
func TestCapacityEvidenceSerializesEveryPromiseAndItsRule(t *testing.T) {
	encoded, err := json.Marshal(Evidence{Promises: []PromiseEvidence{{
		Name:    "one_provision_command_produces_one_machine",
		Rule:    "safety.idempotent_external_commands",
		Outcome: PromiseKept,
	}}})

	if err != nil {
		t.Fatalf("marshal capacity evidence: %v", err)
	}
	for _, want := range []string{`"promises"`, `"one_provision_command_produces_one_machine"`, `"safety.idempotent_external_commands"`, `"kept"`} {
		if !bytes.Contains(encoded, []byte(want)) {
			t.Errorf("evidence does not carry %s: %s", want, encoded)
		}
	}
}

const credentialEnv = "SHADEFORM_API_KEY"

func capacityTrial() Trial {
	return Trial{
		AdapterType:        "shadeform",
		CredentialEnv:      credentialEnv,
		Mode:               ModeCapacity,
		MaxExpectedCostUSD: 2.00,
		Timeout:            20 * time.Minute,
	}
}

func capacityRunner(t *testing.T, provider capability.CapacityProvider) *Runner {
	t.Helper()
	return newRunner(
		RunnerConfig{
			Environment:   map[string]string{credentialEnv: "shadeform-key"},
			ListenAddress: "0.0.0.0:8082",
			PublicURL:     "https://reports.example.com",
		},
		withProviderFactory(capacityFactory(provider)),
		withTempRoot(t.TempDir()),
	)
}

// capacityFactory registers one connection that sells capacity and nothing else.
// The simulated world also executes one-shot workloads, and a single connection
// cannot sell both, so what this trial talks to is the capacity half of it.
func capacityFactory(provider capability.CapacityProvider) *broker.Factory {
	factory := broker.NewFactory()
	factory.Register(shadeformadapter.Manifest(), func(map[string]string, string) (capability.Backend, error) {
		return capacityOnly{provider}, nil
	})
	return factory
}

type capacityOnly struct{ capability.CapacityProvider }

// neverConfirmsATerminate is a provider that destroys a machine and reports
// every destruction as the first one, so a reader counts two machines ending.
type neverConfirmsATerminate struct{ *fake.World }

func (provider *neverConfirmsATerminate) TerminateCapacity(
	ctx context.Context,
	command capability.CapacityCommand,
) (capability.CapacityReceipt, error) {
	receipt, err := provider.World.TerminateCapacity(ctx, command)
	receipt.Duplicate = false
	return receipt, err
}

// holdsTwoMachinesUnderOneLease is an account already carrying a pair of
// machines tagged for one Rental: what a provision whose answer was lost leaves
// behind on a provider whose listing lags, reconciled by scanning for the
// lease's tag, which is how both adapters in this tree find one. Nothing in this
// trial rented them, and nothing but the sweep can find them.
//
// Its terminate honours the operation key, exactly as CapacityCommand defines
// one: the same key performs the destruction once and answers a repeat with the
// receipt it already gave.
type holdsTwoMachinesUnderOneLease struct {
	*fake.World
	// orphans is each machine of the pair and whether it is still running, so a
	// destruction is a fact about one machine rather than a count of calls.
	orphans      map[string]bool
	destructions map[string]capability.CapacityReceipt
}

func twoOrphansOfOneLease(world *fake.World) *holdsTwoMachinesUnderOneLease {
	return &holdsTwoMachinesUnderOneLease{
		World:        world,
		orphans:      map[string]bool{"sim-machine-orphan-a": true, "sim-machine-orphan-b": true},
		destructions: map[string]capability.CapacityReceipt{},
	}
}

const orphanedRental = "rnt_an_earlier_trial"

func (provider *holdsTwoMachinesUnderOneLease) ListOwnedCapacity(
	ctx context.Context,
	query capability.OwnershipQuery,
) ([]capability.OwnedCapacity, error) {
	owned, err := provider.World.ListOwnedCapacity(ctx, query)
	if err != nil {
		return nil, err
	}
	for _, nativeRef := range slices.Sorted(maps.Keys(provider.orphans)) {
		if !provider.orphans[nativeRef] {
			continue
		}
		owned = append(owned, capability.OwnedCapacity{
			NativeRef:   nativeRef,
			WorkspaceID: query.WorkspaceID,
			RentalID:    orphanedRental,
			State:       capability.CapacityStateActive,
		})
	}
	return owned, nil
}

func (provider *holdsTwoMachinesUnderOneLease) TerminateCapacity(
	ctx context.Context,
	command capability.CapacityCommand,
) (capability.CapacityReceipt, error) {
	if performed, replayed := provider.destructions[command.OperationKey]; replayed {
		performed.Duplicate = true
		return performed, nil
	}
	receipt, err := provider.destroy(ctx, command)
	if err == nil {
		provider.destructions[command.OperationKey] = receipt
	}
	return receipt, err
}

func (provider *holdsTwoMachinesUnderOneLease) destroy(
	ctx context.Context,
	command capability.CapacityCommand,
) (capability.CapacityReceipt, error) {
	if _, orphan := provider.orphans[command.NativeRef]; !orphan {
		return provider.World.TerminateCapacity(ctx, command)
	}
	provider.orphans[command.NativeRef] = false
	return capability.CapacityReceipt{
		NativeRef:  command.NativeRef,
		State:      capability.CapacityStateTerminated,
		AcceptedAt: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC),
	}, nil
}

// stillRunning is every orphan this account is still billing for, whatever the
// sweep believes it destroyed.
func (provider *holdsTwoMachinesUnderOneLease) stillRunning() []string {
	var live []string
	for _, nativeRef := range slices.Sorted(maps.Keys(provider.orphans)) {
		if provider.orphans[nativeRef] {
			live = append(live, nativeRef)
		}
	}
	return live
}

// sellingWorld is a marketplace selling one machine type. It sells a product
// rather than a named machine, because a listing that names one machine can be
// bought once and the suite rents several.
func sellingWorld(t *testing.T) *fake.World {
	t.Helper()
	world := fake.NewWorld(fake.NewClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)))
	listing := &fake.Machine{
		Offer: domain.OfferSnapshot{
			ID:        "sim-a6000",
			NativeRef: "simcloud/eu-1/A6000",
			Kind:      domain.OfferKindProvisionable,
			Lane:      domain.LaneReusable,
			Pricing:   domain.PriceModel{Currency: "USD", RatePerSecondUSD: 0.0005, Known: true},
			Capacity:  domain.CapacityEvidence{Available: true},
			Resources: domain.ResourceInventory{EphemeralDiskBytes: 200 << 30, EphemeralDiskKnown: true},
		},
		AcquisitionSpend: time.Minute,
		BootSpend:        time.Minute,
		AgentReadySpend:  time.Minute,
	}
	if err := world.AddMachine(listing); err != nil {
		t.Fatalf("add listing: %v", err)
	}
	return world
}

func ownedBy(t *testing.T, world *fake.World) int {
	t.Helper()
	owned, err := world.ListOwnedCapacity(context.Background(), capability.OwnershipQuery{})
	if err != nil {
		t.Fatalf("list owned capacity: %v", err)
	}
	return len(owned)
}
