package daemon_test

import (
	"context"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/capability"
	"github.com/benngarcia/mercator/internal/daemon"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/nodeagent"
)

// TestAMachineItsProviderBootstrappedIsWarmCapacityForTheNextRun is the L1
// counterpart of enrolled-node-survives-its-first-run, and it holds the half of
// that Blueprint a simulated world cannot hold on its own: that a machine handed a
// bootstrap by a CapacityProvider, and nothing else, becomes standing capacity
// Placement will choose, and that running a workload there really leaves the image
// on it.
//
// Everything here is the production article. A real capacity connection is
// authorized through the API and holds a real capability.CapacityProvider. The
// bootstrap is minted by the real node registry and handed to that provider, and
// the agent is started from the provider's own copy of it and no other input, so
// an identity the provider was never given could not enrol. The agent is the
// production nodeagent.Agent driving this workstation's own Docker daemon, over
// the real node protocol, and both Runs are submitted and read over the public
// API.
//
// What it deliberately does not do is let Placement choose to provision. A
// capacity connection publishes no candidate today, which is mercator#200, and the
// launch that follows one cannot be addressed at the machine that was built, which
// is mercator#207. So the act a placement would perform is performed here by the
// case, and everything after it, the enrolment, the publication, the execution and
// the warmth, is production's own.
func TestAMachineItsProviderBootstrappedIsWarmCapacityForTheNextRun(t *testing.T) {
	docker := requireDockerBinary(t)
	image := coldImage(t, docker, "alpine:3.20")
	provider := &bootstrappingProvider{}
	fleet := startProvisionedFleet(t, provider, docker, image)

	first := fleet.submitRunRunning(t, image)
	fleet.awaitRealOutcome(t, first, "succeeded")
	fleet.awaitHolding(t, image)
	second := fleet.submitRunRunning(t, image)
	fleet.awaitRealOutcome(t, second, "succeeded")

	cold := fleet.decision(t, first)
	if cold.SelectedOfferSnapshotID != fleet.nodeID {
		t.Fatalf("the first Run landed on %q, and the machine its provider bootstrapped is %q",
			cold.SelectedOfferSnapshotID, fleet.nodeID)
	}
	if pull := cold.pullEstimate(); pull.Expected <= 0 {
		t.Fatalf("the first Run was charged %.2fs to fetch an image this machine did not have, so the second Run proves nothing", pull.Expected)
	}
	warm := fleet.decision(t, second)
	if warm.SelectedOfferSnapshotID != fleet.nodeID {
		t.Fatalf("the second Run landed on %q rather than back on the machine that is already there", warm.SelectedOfferSnapshotID)
	}
	if boot := warm.stageEstimate(domain.StageBoot); boot.Expected != 0 {
		t.Errorf("the second Run is charged %.2fs of boot on a machine that is already up: %+v", boot.Expected, boot)
	}
	if pull := warm.pullEstimate(); pull.Expected != 0 || pull.Source != "image_inventory" {
		t.Errorf("the second Run is charged %.2fs of image fetch from %q, and the first Run left the image on this machine",
			pull.Expected, pull.Source)
	}
}

// startProvisionedFleet stands up a daemon holding one capacity connection,
// provisions a machine through it, and starts the agent that machine boots with.
//
// The bootstrap makes one round trip on purpose. The registry mints it, the
// provision command carries it to the provider, and the agent is started from what
// the provider kept: an agent started from the invitation directly would enrol
// just as well against a provider that was handed nothing.
func startProvisionedFleet(t *testing.T, provider *bootstrappingProvider, docker, image string) *fleet {
	t.Helper()
	// Docker is resolved above and then taken off PATH, so the daemon seeds no
	// local connection of its own. This host would otherwise publish itself twice,
	// once as this node and once as an ephemeral Docker endpoint, which is
	// mercator#201 and not what this case is about.
	t.Setenv("PATH", t.TempDir())
	harness, _ := startDaemonServing(t, map[string]capability.Backend{"machines": provider})
	harness.image = image
	harness.heartbeat = 250 * time.Millisecond
	harness.authorize(t, "conn_machines", "machines")

	bootstrap := harness.invite(t, 1.25)
	if _, err := provider.ProvisionCapacity(t.Context(), capability.ProvisionCommand{
		WorkspaceID:     daemon.DefaultWorkspaceID,
		ConnectionID:    "conn_machines",
		OperationKey:    "provision_" + bootstrap.RentalID,
		RentalID:        bootstrap.RentalID,
		Generation:      bootstrap.Generation,
		OfferSnapshotID: "i-held",
		Bootstrap:       bootstrap,
	}); err != nil {
		t.Fatalf("provision capacity: %v", err)
	}
	harness.nodeID = bootstrap.NodeID
	harness.stop = harness.startAgent(t, provider.delivered(t), nodeagent.NewDockerRuntime(docker))
	harness.awaitOffer(t, harness.nodeID)
	return harness
}

// bootstrappingProvider is a CapacityProvider that allocates a machine and keeps
// the material it was handed for it, verbatim. It executes nothing, which is the
// only shape a provider adapter has: what runs a workload on the machine it
// allocated is the agent that enrols on it.
type bootstrappingProvider struct {
	machineProvider

	mu        sync.Mutex
	bootstrap capability.NodeBootstrap
	allocated bool
}

func (provider *bootstrappingProvider) ProvisionCapacity(_ context.Context, command capability.ProvisionCommand) (capability.CapacityReceipt, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.bootstrap = command.Bootstrap
	provider.allocated = true
	return capability.CapacityReceipt{
		NativeRef:  command.OfferSnapshotID,
		State:      capability.CapacityStateRequested,
		AcceptedAt: time.Now().UTC(),
	}, nil
}

// delivered is what this provider would put on the machine it booted. A provider
// that was never asked for a machine has nothing to deliver, and a case that
// started an agent anyway would be asserting nothing about a bootstrap.
func (provider *bootstrappingProvider) delivered(t *testing.T) capability.NodeBootstrap {
	t.Helper()
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if !provider.allocated {
		t.Fatal("nothing asked this provider for a machine, so no agent could have been bootstrapped onto one")
	}
	return provider.bootstrap
}

// submitRunRunning submits a Run of an image that really exists, whose container
// exits on its own. Every other case in this package scripts the machine and can
// name a command that does not exist; this one is run by a container runtime.
func (f *fleet) submitRunRunning(t *testing.T, image string) string {
	t.Helper()
	return f.submitWorkload(t, func(name string) map[string]any {
		revision := workloadRevision(name, image)
		revision["spec"].(map[string]any)["containers"].([]map[string]any)[0]["args"] = []string{"true"}
		return revision
	})
}

// awaitHolding waits until the machine has told the control plane it holds the
// image. What a node holds is its own fact and travels by heartbeat, so a case
// that needs a warm candidate waits for the machine to have said so rather than
// for the workload that put it there to have ended.
func (f *fleet) awaitHolding(t *testing.T, image string) {
	t.Helper()
	waitFor(t, func() bool {
		return f.nodeOffer(t).Images.Holds(domain.ReferenceDigest(image))
	}, "the machine never reported holding "+image+" after running it")
}

// awaitRealOutcome drives the Run forward the way the reconcile sweep does until
// the machine's own report of the container's exit has reached an outcome. It
// refreshes rather than waiting, because nothing here scripts the exit: the
// container decides when it is over and the sweep's own cadence is a minute.
func (f *fleet) awaitRealOutcome(t *testing.T, runID, want string) {
	t.Helper()
	outcome := ""
	waitFor(t, func() bool {
		var refreshed struct {
			Run struct {
				Outcome string `json:"outcome"`
			} `json:"run"`
		}
		f.call(t, http.MethodPost, "/v1/runs/"+runID+"/refresh?workspace_id="+daemon.DefaultWorkspaceID, nil, &refreshed, http.StatusOK)
		outcome = refreshed.Run.Outcome
		return outcome == want
	}, "Run "+runID+" never reached outcome "+want+" (last outcome "+outcome+")")
}

// coldImage is the reference this case places, and the machine it places it on
// really not holding it yet: the image identity a real registry served, read off
// the daemon that holds it, and then taken back off that daemon.
//
// Starting cold is what makes the warm half falsifiable. This workstation's Docker
// holds whatever earlier work left on it, so a case that placed an image already
// here would charge the first Run nothing either, and would go green against a
// control plane that never learned anything from the execution at all. The tag is
// one no other case in this tree uses, because two suites running at once must not
// take each other's content away.
func coldImage(t *testing.T, docker, tag string) string {
	t.Helper()
	if output, err := exec.Command(docker, "pull", "--quiet", tag).CombinedOutput(); err != nil {
		t.Skipf("no reachable registry to serve %s: %v\n%s", tag, err, output)
	}
	output, err := exec.Command(docker, "image", "inspect", tag, "--format", "{{index .RepoDigests 0}}").Output()
	if err != nil {
		t.Fatalf("read the digest %s was pulled by: %v", tag, err)
	}
	reference := strings.TrimSpace(string(output))
	if output, err := exec.Command(docker, "image", "rm", "--force", tag, reference).CombinedOutput(); err != nil {
		t.Fatalf("take %s back off this machine: %v\n%s", tag, err, output)
	}
	return reference
}
