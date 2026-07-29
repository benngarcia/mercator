package shadeform

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/capability"
)

func provisionCommand() capability.ProvisionCommand {
	return capability.ProvisionCommand{
		WorkspaceID:     "ws_1",
		ConnectionID:    "conn_1",
		OperationKey:    "provision_rent_1",
		RentalID:        "rent_1",
		Generation:      1,
		OwnershipToken:  "own1",
		OfferSnapshotID: "off_shadeform_hyperstack_canada_1_a6000",
		NativeRef:       "hyperstack/canada-1/A6000",
		Bootstrap:       bootstrap(),
	}
}

func bootstrap() capability.NodeBootstrap {
	return capability.NodeBootstrap{
		ControlPlaneURL: "https://mercator.test",
		NodeID:          "node_1",
		RentalID:        "rent_1",
		Generation:      1,
		EnrollmentToken: "enrol-token-1",
		AgentVersion:    "v0.7.1",
	}
}

func TestShadeformNegotiatesOnlyWhatItsEndpointsCanDo(t *testing.T) {
	support := newTestAdapter(t, newFakeShadeform(), nil).CapacitySupport()

	if support.Stop || support.Resume || support.PersistentDisk {
		t.Errorf("shadeform destroys machines and suspends none: %+v", support)
	}
	if support.IdempotentProvision != capability.IdempotentProvisionNone {
		t.Errorf("create honours no idempotency key, got %q", support.IdempotentProvision)
	}
	if !support.ListOwned {
		t.Error("a provider that deduplicates no provision must list what it owns")
	}
	if !support.ObserveAfterTerminate {
		t.Error("a destroyed instance stays listed while it is deleting")
	}
	if err := support.Validate(); err != nil {
		t.Fatalf("negotiated set is one no provider could keep: %v", err)
	}
}

func TestProvisionCreatesOneMachineCarryingTheBootstrapScript(t *testing.T) {
	fake := newFakeShadeform()
	fake.types = []instanceType{vmType()}
	adapter := newTestAdapter(t, fake, nil)

	receipt, err := adapter.ProvisionCapacity(context.Background(), provisionCommand())

	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if receipt.NativeRef != "inst_1" || receipt.Duplicate || receipt.State != capability.CapacityStateStarting {
		t.Fatalf("receipt = %+v", receipt)
	}
	if len(fake.creates) != 1 {
		t.Fatalf("want exactly one create, got %d", len(fake.creates))
	}
	create := fake.creates[0]
	if create.Cloud != "hyperstack" || create.Region != "canada-1" || create.ShadeInstanceType != "A6000" {
		t.Errorf("placement triple = %s/%s/%s", create.Cloud, create.Region, create.ShadeInstanceType)
	}
	if !create.ShadeCloud {
		t.Error("shade_cloud must default to true")
	}
	if create.Name != "mercator-rent_1" {
		t.Errorf("name = %q, want the machine named for the lease that bought it", create.Name)
	}
	if create.OS != "ubuntu22.04_cuda12.2_shade_os" {
		t.Errorf("os = %q, want the shade_os image (driver + container runtime baked in)", create.OS)
	}
	launch := create.LaunchConfiguration
	if launch == nil || launch.Type != "script" || launch.ScriptConfiguration == nil {
		t.Fatalf("launch configuration = %+v, want a script that starts the agent", launch)
	}
	script := decodeScript(t, launch.ScriptConfiguration.Base64Script)
	for _, want := range []string{
		"MERCATOR_CONTROL_PLANE_URL=https://mercator.test",
		"MERCATOR_NODE_ID=node_1",
		"MERCATOR_RENTAL_ID=rent_1",
		"MERCATOR_NODE_GENERATION=1",
		"MERCATOR_NODE_ENROLLMENT_TOKEN=enrol-token-1",
		"https://downloads.mercator.test/mercator-node/v0.7.1/linux-amd64",
		"systemctl enable --now mercator-node.service",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("bootstrap script does not %q:\n%s", want, script)
		}
	}
	// Literal tag expectations: the tag wire format is the reconciler's whole
	// ownership contract on real instances, so it is pinned independently of the
	// helper that writes it.
	tags := map[string]bool{}
	for _, tag := range create.Tags {
		tags[tag] = true
	}
	for _, want := range []string{
		"mercator:rental=rent_1",
		"mercator:generation=1",
		"mercator:workspace=ws_1",
		"mercator:ownership-token=own1",
	} {
		if !tags[want] {
			t.Errorf("create missing ownership tag %q (got %v)", want, create.Tags)
		}
	}
	if create.AutoDelete == nil {
		t.Fatal("auto_delete backstop must be set on every create")
	}
	// now (2026-07-17T12:00Z) + default 24h lifetime (no MaxLifetimeSeconds)
	if create.AutoDelete.DateThreshold != "2026-07-18T12:00:00Z" {
		t.Errorf("auto_delete date threshold = %q", create.AutoDelete.DateThreshold)
	}
	// 210 cents/hour × 24h = $50.40
	if create.AutoDelete.SpendThreshold != "50.40" {
		t.Errorf("auto_delete spend threshold = %q", create.AutoDelete.SpendThreshold)
	}
}

// TestAProvisionedMachineIsHandedNoWorkloadAndNoRegistryAccount is the rule that
// separates the two lanes on the wire. A reusable Rental's machine runs whatever
// its enrolled agent is told to run, so a create body naming an image, an
// environment or a registry account would be this adapter deciding what executes
// on capacity it only rents, and Shadeform would write that account onto the
// machine where it outlives every pull it was needed for.
func TestAProvisionedMachineIsHandedNoWorkloadAndNoRegistryAccount(t *testing.T) {
	fake := newFakeShadeform()
	fake.types = []instanceType{vmType()}
	adapter := newTestAdapter(t, fake, map[string]string{"registry_username": "bot", "registry_password": "ghp_pat"})

	if _, err := adapter.ProvisionCapacity(context.Background(), provisionCommand()); err != nil {
		t.Fatalf("provision: %v", err)
	}

	body, err := json.Marshal(fake.creates[0])
	if err != nil {
		t.Fatalf("read back the create body: %v", err)
	}
	for _, absent := range []string{"bot", "ghp_pat", "registry_credentials", "docker_configuration", "envs"} {
		if strings.Contains(string(body), absent) {
			t.Fatalf("the create body carries %q: %s", absent, body)
		}
	}
}

func TestProvisionRefusesAConnectionThatStatesNoAgentSource(t *testing.T) {
	fake := newFakeShadeform()
	fake.types = []instanceType{vmType()}
	adapter := newTestAdapter(t, fake, map[string]string{"agent_download_url": ""})

	_, err := adapter.ProvisionCapacity(context.Background(), provisionCommand())

	if err == nil || !strings.Contains(err.Error(), "agent_download_url") {
		t.Fatalf("want a refusal naming the missing agent source, got %v", err)
	}
	if len(fake.creates) != 0 {
		t.Fatal("a machine no agent can be installed on must not be paid for")
	}
}

func TestProvisionDerivesAutoDeleteFromTheLeaseBound(t *testing.T) {
	fake := newFakeShadeform()
	fake.types = []instanceType{vmType()}
	adapter := newTestAdapter(t, fake, nil)
	command := provisionCommand()
	command.MaxLifetimeSeconds = 3600

	if _, err := adapter.ProvisionCapacity(context.Background(), command); err != nil {
		t.Fatalf("provision: %v", err)
	}

	backstop := fake.creates[0].AutoDelete
	// provision (2026-07-17T12:00Z) + 1h bound + 1h slack
	if backstop.DateThreshold != "2026-07-17T14:00:00Z" {
		t.Errorf("date threshold = %q, want provision+bound+slack", backstop.DateThreshold)
	}
	// 210 cents/hour × 2h = $4.20
	if backstop.SpendThreshold != "4.20" {
		t.Errorf("spend threshold = %q", backstop.SpendThreshold)
	}
}

func TestProvisionOmitsSpendThresholdForZeroPricedInventory(t *testing.T) {
	free := vmType()
	free.HourlyPrice = 0
	fake := newFakeShadeform()
	fake.types = []instanceType{free}
	adapter := newTestAdapter(t, fake, nil)

	if _, err := adapter.ProvisionCapacity(context.Background(), provisionCommand()); err != nil {
		t.Fatalf("provision: %v", err)
	}

	backstop := fake.creates[0].AutoDelete
	if backstop == nil || backstop.DateThreshold == "" {
		t.Fatalf("date threshold must remain the time backstop, got %+v", backstop)
	}
	if backstop.SpendThreshold != "" {
		t.Fatalf("a zero catalog price must omit the spend threshold (\"0.00\" semantics are undefined), got %q", backstop.SpendThreshold)
	}
}

func TestProvisionRejectsCloudOutsideAllowedClouds(t *testing.T) {
	fake := newFakeShadeform()
	fake.types = []instanceType{vmType()}
	adapter := newTestAdapter(t, fake, map[string]string{"allowed_clouds": "lambdalabs"})

	_, err := adapter.ProvisionCapacity(context.Background(), provisionCommand())

	if err == nil || !strings.Contains(err.Error(), "allowed_clouds") {
		t.Fatalf("want allowed_clouds rejection, got %v", err)
	}
	if len(fake.creates) != 0 {
		t.Fatal("a rejected provision must not create anything")
	}
}

func TestProvisionFailsWhenTheCatalogLacksTheType(t *testing.T) {
	fake := newFakeShadeform() // empty catalog
	adapter := newTestAdapter(t, fake, nil)

	_, err := adapter.ProvisionCapacity(context.Background(), provisionCommand())

	if err == nil || !strings.Contains(err.Error(), "auto_delete") {
		t.Fatalf("a machine whose spend cannot be capped must fail loudly, got %v", err)
	}
	if len(fake.creates) != 0 {
		t.Fatal("must not rent a machine whose spend cannot be capped")
	}
}

func TestProvisionFailsLoudlyWithoutAShadeOSImage(t *testing.T) {
	plain := vmType()
	plain.Configuration.OSOptions = []string{"ubuntu22.04", "ubuntu20.04"}
	fake := newFakeShadeform()
	fake.types = []instanceType{plain}
	adapter := newTestAdapter(t, fake, nil)

	_, err := adapter.ProvisionCapacity(context.Background(), provisionCommand())

	if err == nil || !strings.Contains(err.Error(), "shade_os") {
		t.Fatalf("no shade_os image and no override must fail loudly (a plain image may carry no container runtime), got %v", err)
	}
	if len(fake.creates) != 0 {
		t.Fatal("must not rent a machine whose agent could start no container")
	}
}

func TestProvisionOSOverrideBypassesTheShadeOSRequirement(t *testing.T) {
	plain := vmType()
	plain.Configuration.OSOptions = []string{"ubuntu22.04"}
	fake := newFakeShadeform()
	fake.types = []instanceType{plain}
	adapter := newTestAdapter(t, fake, map[string]string{"os": "ubuntu22.04"})

	if _, err := adapter.ProvisionCapacity(context.Background(), provisionCommand()); err != nil {
		t.Fatalf("provision: %v", err)
	}

	if fake.creates[0].OS != "ubuntu22.04" {
		t.Fatalf("os = %q", fake.creates[0].OS)
	}
}

func TestProvisionHonoursShadeCloudFalse(t *testing.T) {
	fake := newFakeShadeform()
	fake.types = []instanceType{vmType()}
	adapter := newTestAdapter(t, fake, map[string]string{"shade_cloud": "false"})

	if _, err := adapter.ProvisionCapacity(context.Background(), provisionCommand()); err != nil {
		t.Fatalf("provision: %v", err)
	}

	if fake.creates[0].ShadeCloud {
		t.Fatal("shade_cloud=false must reach the create payload")
	}
}

func TestProvisionIsIdempotentAcrossRetries(t *testing.T) {
	fake := newFakeShadeform()
	fake.types = []instanceType{vmType()}
	fake.addInstance(rentedInstance("inst_9", "rent_1", "ws_1", "own1", "active", fake.base))
	adapter := newTestAdapter(t, fake, nil)

	receipt, err := adapter.ProvisionCapacity(context.Background(), provisionCommand())

	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if !receipt.Duplicate || receipt.NativeRef != "inst_9" || receipt.State != capability.CapacityStateActive {
		t.Fatalf("receipt = %+v, want the machine this Rental already holds", receipt)
	}
	if len(fake.creates) != 0 {
		t.Fatal("a pre-scan hit must not rent a second machine")
	}
}

func TestProvisionIgnoresDeletingMachinesInThePreScan(t *testing.T) {
	fake := newFakeShadeform()
	fake.types = []instanceType{vmType()}
	fake.addInstance(rentedInstance("inst_9", "rent_1", "ws_1", "own1", "deleting", fake.base))
	adapter := newTestAdapter(t, fake, nil)

	receipt, err := adapter.ProvisionCapacity(context.Background(), provisionCommand())

	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if receipt.Duplicate || len(fake.creates) != 1 {
		t.Fatalf("a machine being destroyed cannot satisfy this lease: receipt=%+v creates=%d", receipt, len(fake.creates))
	}
}

func TestProvisionRefusesAMachineHeldUnderAnotherOwnershipToken(t *testing.T) {
	fake := newFakeShadeform()
	fake.types = []instanceType{vmType()}
	fake.addInstance(rentedInstance("inst_9", "rent_1", "ws_1", "someone-else", "active", fake.base))
	adapter := newTestAdapter(t, fake, nil)

	_, err := adapter.ProvisionCapacity(context.Background(), provisionCommand())

	if err == nil || !strings.Contains(err.Error(), "ownership token") {
		t.Fatalf("want a refusal naming the ownership conflict, got %v", err)
	}
	if len(fake.creates) != 0 {
		t.Fatal("a conflicted lease must not rent a machine")
	}
}

func TestProvisionReconcilesAConcurrentDuplicateKeepingTheOldest(t *testing.T) {
	fake := newFakeShadeform()
	fake.types = []instanceType{vmType()}
	// A concurrent provisioner's machine appears between our pre-scan and our
	// create landing. It is older than ours, so it wins.
	fake.beforeCreateReturns = func(f *fakeShadeform) {
		f.instances = append(f.instances, rentedInstance("inst_0", "rent_1", "ws_1", "own1", "creating", f.base.Add(-time.Hour)))
	}
	adapter := newTestAdapter(t, fake, nil)

	receipt, err := adapter.ProvisionCapacity(context.Background(), provisionCommand())

	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if !receipt.Duplicate || receipt.NativeRef != "inst_0" {
		t.Fatalf("receipt = %+v, want the older concurrent machine to win", receipt)
	}
	if len(fake.deletes) != 1 || fake.deletes[0] != "inst_1" {
		t.Fatalf("our younger duplicate must be destroyed, got deletes=%v", fake.deletes)
	}
}

// TestAnIndeterminateProvisionIsReconciledByTagScanRatherThanASecondCreate is the
// whole reason the Rental identity travels to the provider. Shadeform's create
// carries no operation key, so a lost answer is indistinguishable from a command
// nobody sent, and asking again would rent a second machine nobody will come for.
func TestAnIndeterminateProvisionIsReconciledByTagScanRatherThanASecondCreate(t *testing.T) {
	fake := newFakeShadeform()
	fake.types = []instanceType{vmType()}
	fake.createStatus = 500
	fake.createLandsAnyway = true
	adapter := newTestAdapter(t, fake, nil)

	receipt, err := adapter.ProvisionCapacity(context.Background(), provisionCommand())

	if err != nil {
		t.Fatalf("provision should adopt the machine the failed create landed, got %v", err)
	}
	if !receipt.Duplicate || receipt.NativeRef != "inst_1" {
		t.Fatalf("receipt = %+v", receipt)
	}
	if len(fake.creates) != 1 {
		t.Fatalf("a 5xx create must never be retried blindly, got %d create calls", len(fake.creates))
	}
}

func TestAProvisionWhoseMachineNeverSurfacesIsIndeterminate(t *testing.T) {
	fake := newFakeShadeform()
	fake.types = []instanceType{vmType()}
	fake.hideCreated = true
	adapter := newTestAdapter(t, fake, nil)

	_, err := adapter.ProvisionCapacity(context.Background(), provisionCommand())

	if err == nil || !strings.Contains(err.Error(), "indeterminate") {
		t.Fatalf("a machine that never appears in the listing must be indeterminate rather than a receipt the next observation reads as destroyed, got %v", err)
	}
}

func TestObserveReportsTheProviderLifecycleAndNothingAboutWork(t *testing.T) {
	states := map[string]capability.CapacityState{
		"creating":         capability.CapacityStateStarting,
		"pending_provider": capability.CapacityStateStarting,
		"pending":          capability.CapacityStateStarting,
		"active":           capability.CapacityStateActive,
		"deleting":         capability.CapacityStateTerminated,
		"error":            capability.CapacityStateUnknown,
	}
	for status, want := range states {
		fake := newFakeShadeform()
		fake.addInstance(rentedInstance("inst_1", "rent_1", "ws_1", "own1", status, fake.base))
		adapter := newTestAdapter(t, fake, nil)

		observation, err := adapter.ObserveCapacity(context.Background(), capability.CapacityRef{
			WorkspaceID: "ws_1", RentalID: "rent_1", NativeRef: "inst_1", OwnershipToken: "own1",
		})

		if err != nil {
			t.Fatalf("observe %s: %v", status, err)
		}
		if observation.State != want {
			t.Errorf("status %q → state %q, want %q", status, observation.State, want)
		}
		if !observation.StateSince.IsZero() {
			t.Errorf("shadeform dates no transition; reporting one would publish the poll as the machine's own spend, got %s", observation.StateSince)
		}
	}
}

func TestObserveReportsAMachineThatLeftTheListingAsTerminated(t *testing.T) {
	adapter := newTestAdapter(t, newFakeShadeform(), nil)

	observation, err := adapter.ObserveCapacity(context.Background(), capability.CapacityRef{
		WorkspaceID: "ws_1", RentalID: "rent_1", NativeRef: "inst_1", OwnershipToken: "own1",
	})

	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if observation.State != capability.CapacityStateTerminated || observation.NativeRef != "inst_1" {
		t.Fatalf("observation = %+v, want the machine reported gone under the ref the caller named", observation)
	}
}

func TestObserveRefusesAMachineHeldUnderAnotherOwnershipToken(t *testing.T) {
	fake := newFakeShadeform()
	fake.addInstance(rentedInstance("inst_1", "rent_1", "ws_1", "someone-else", "active", fake.base))
	adapter := newTestAdapter(t, fake, nil)

	_, err := adapter.ObserveCapacity(context.Background(), capability.CapacityRef{
		WorkspaceID: "ws_1", RentalID: "rent_1", NativeRef: "inst_1", OwnershipToken: "own1",
	})

	if err == nil || !strings.Contains(err.Error(), "ownership token") {
		t.Fatalf("want a refusal naming the ownership conflict, got %v", err)
	}
}

func TestProvisionReportsTheMomentTheProviderAllocatedTheMachine(t *testing.T) {
	fake := newFakeShadeform()
	fake.types = []instanceType{vmType()}
	allocatedAnHourAgo := fake.base.Add(-time.Hour)
	fake.addInstance(rentedInstance("inst_9", "rent_1", "ws_1", "own1", "active", allocatedAnHourAgo))
	adapter := newTestAdapter(t, fake, nil)

	receipt, err := adapter.ProvisionCapacity(context.Background(), provisionCommand())

	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if !receipt.AcceptedAt.Equal(allocatedAnHourAgo) {
		t.Fatalf("accepted at = %s, want the moment the machine was allocated rather than the moment this command asked", receipt.AcceptedAt)
	}
}

func TestStopAndResumeAreRefusedRatherThanQuietlySucceeding(t *testing.T) {
	adapter := newTestAdapter(t, newFakeShadeform(), nil)
	command := capability.CapacityCommand{
		CapacityRef: capability.CapacityRef{WorkspaceID: "ws_1", RentalID: "rent_1", NativeRef: "inst_1"},
	}

	if _, err := adapter.StopCapacity(context.Background(), command); !errors.Is(err, capability.ErrCapabilityUnsupported) {
		t.Errorf("stop = %v, want ErrCapabilityUnsupported", err)
	}
	if _, err := adapter.StartCapacity(context.Background(), command); !errors.Is(err, capability.ErrCapabilityUnsupported) {
		t.Errorf("resume = %v, want ErrCapabilityUnsupported", err)
	}
}

func TestTerminateDestroysEveryLiveMachineHeldUnderTheLease(t *testing.T) {
	fake := newFakeShadeform()
	fake.addInstance(rentedInstance("inst_1", "rent_1", "ws_1", "own1", "active", fake.base))
	// A stray duplicate from a reconciliation that failed halfway: teardown is
	// the path that converges back to zero.
	fake.addInstance(rentedInstance("inst_2", "rent_1", "ws_1", "own1", "creating", fake.base.Add(time.Minute)))
	// Already deleting: never destroyed twice.
	fake.addInstance(rentedInstance("inst_3", "rent_1", "ws_1", "own1", "deleting", fake.base))
	adapter := newTestAdapter(t, fake, nil)

	receipt, err := adapter.TerminateCapacity(context.Background(), capability.CapacityCommand{
		CapacityRef: capability.CapacityRef{WorkspaceID: "ws_1", RentalID: "rent_1", NativeRef: "inst_1", OwnershipToken: "own1"},
	})

	if err != nil {
		t.Fatalf("terminate: %v", err)
	}
	if receipt.State != capability.CapacityStateTerminated || receipt.Duplicate {
		t.Fatalf("receipt = %+v", receipt)
	}
	if len(fake.deletes) != 2 || fake.deletes[0] != "inst_1" || fake.deletes[1] != "inst_2" {
		t.Fatalf("deletes = %v, want both live machines and never the one already deleting", fake.deletes)
	}
}

func TestTerminatingAMachineAlreadyGoneIsADuplicateRatherThanASecondEnding(t *testing.T) {
	adapter := newTestAdapter(t, newFakeShadeform(), nil)

	receipt, err := adapter.TerminateCapacity(context.Background(), capability.CapacityCommand{
		CapacityRef: capability.CapacityRef{WorkspaceID: "ws_1", RentalID: "rent_1", NativeRef: "inst_1", OwnershipToken: "own1"},
	})

	if err != nil {
		t.Fatalf("terminate: %v", err)
	}
	if receipt.State != capability.CapacityStateTerminated || !receipt.Duplicate {
		t.Fatalf("receipt = %+v, want a terminate that found nothing to destroy reported as a duplicate", receipt)
	}
}

func TestListOwnedCapacityFiltersTheAccountByLeaseWorkspaceAndDeletion(t *testing.T) {
	fake := newFakeShadeform()
	fake.addInstance(rentedInstance("inst_1", "rent_1", "ws_1", "own1", "active", fake.base))
	fake.addInstance(rentedInstance("inst_2", "rent_2", "ws_1", "own2", "deleting", fake.base))
	fake.addInstance(rentedInstance("inst_3", "rent_3", "ws_2", "own3", "active", fake.base))
	fake.addInstance(instance{ID: "inst_4", Name: "someone-elses-vm", Status: "active", CreatedAt: fake.base})
	// A one-shot execution somebody else's tooling left in this account. It
	// carries no lease, so it is not capacity Mercator holds.
	fake.addInstance(instance{ID: "inst_5", Status: "active", CreatedAt: fake.base, Tags: []string{"mercator:launch-key=lk1"}})
	adapter := newTestAdapter(t, fake, nil)

	owned, err := adapter.ListOwnedCapacity(context.Background(), capability.OwnershipQuery{WorkspaceID: "ws_1"})

	if err != nil {
		t.Fatalf("list owned capacity: %v", err)
	}
	if len(owned) != 1 {
		t.Fatalf("owned = %+v, want only the live ws_1 machine", owned)
	}
	held := owned[0]
	if held.NativeRef != "inst_1" || held.RentalID != "rent_1" || held.WorkspaceID != "ws_1" ||
		held.Generation != 1 || held.OwnershipToken != "own1" || held.State != capability.CapacityStateActive {
		t.Fatalf("owned[0] = %+v", held)
	}
	if !held.CreatedAt.Equal(fake.base) {
		t.Fatalf("created at = %s, want the provider's own moment", held.CreatedAt)
	}
}

func TestListOwnedCapacityWithoutAWorkspaceReturnsEveryLeaseThisAccountHolds(t *testing.T) {
	fake := newFakeShadeform()
	fake.addInstance(rentedInstance("inst_1", "rent_1", "ws_1", "own1", "active", fake.base))
	fake.addInstance(rentedInstance("inst_3", "rent_3", "ws_2", "own3", "error", fake.base))
	fake.addInstance(instance{ID: "inst_4", Name: "someone-elses-vm", Status: "active", CreatedAt: fake.base})
	adapter := newTestAdapter(t, fake, nil)

	owned, err := adapter.ListOwnedCapacity(context.Background(), capability.OwnershipQuery{})

	if err != nil {
		t.Fatalf("list owned capacity: %v", err)
	}
	if len(owned) != 2 {
		t.Fatalf("owned = %+v, want both leases and never the untagged machine", owned)
	}
}

func TestListCapacityAsksTheCatalogForEverythingItSells(t *testing.T) {
	fake := newFakeShadeform()
	fake.types = []instanceType{vmType()}
	adapter := newTestAdapter(t, fake, nil)

	offers, err := adapter.ListCapacity(context.Background(), capability.CapacityQuery{WorkspaceID: "ws_1"})

	if err != nil {
		t.Fatalf("list capacity: %v", err)
	}
	if len(offers) != 2 {
		t.Fatalf("offers = %d, want one per listed region including the sold-out one", len(offers))
	}
}

func decodeScript(t *testing.T, encoded string) string {
	t.Helper()
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("the create body's script must be base64: %v", err)
	}
	return string(decoded)
}
