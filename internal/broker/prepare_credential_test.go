package broker

import (
	"context"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/capability"
	"github.com/benngarcia/mercator/internal/domain"
)

// TestTheBrokerDeliversTheMaterialTheControlPlaneMintedForEachFetch is the hop
// the Lab cannot see. The Lab replaces the world at the desired-set seam, which
// is above this: a Broker that dropped the credential on its way to the node
// command would leave every rule in the corpus green and every private pull on a
// real machine refused.
//
// What is asserted is delivery and nothing else. The Broker holds no account, so
// it must not narrow, re-scope, or invent material: the pull that reaches the
// runtime is the pull the control plane minted, and the read is the read.
func TestTheBrokerDeliversTheMaterialTheControlPlaneMintedForEachFetch(t *testing.T) {
	expiry := time.Date(2026, 7, 28, 12, 15, 0, 0, time.UTC)
	runtime := &recordingRuntime{}
	broker := brokerServing(t, runtime, map[string]capability.Backend{})
	image := adapter.PrepareItem{
		Kind:            adapter.PrepareImage,
		OfferSnapshotID: "builder",
		ConnectionID:    nodeConnectionID,
		NativeRef:       "nod_builder",
		Image:           "registry.mercator.test/analyst@sha256:7a1c4e9b2d6f8a0c3e5b7d9f1a3c5e7b9d1f3a5c7e9b1d3f5a7c9e1b3d5f7a9c",
	}
	image.RegistryCredential = domain.RegistryPull{
		ContentCredentialScope: domain.ContentCredentialScope{
			Operation: image.Operation(),

			Content:   image.Content(),
			ExpiresAt: expiry,
		},
		Registry: "registry.mercator.test",
		Username: "mercator",
		Secret:   "one-pull-and-no-more",
	}
	artifact := adapter.PrepareItem{
		Kind:            adapter.PrepareArtifact,
		OfferSnapshotID: "builder",
		ConnectionID:    nodeConnectionID,
		NativeRef:       "nod_builder",
		ArtifactID:      "artifact:corpus:v3",
		ContentDigest:   "sha256:cc33dd44ee55ff66aa77bb88cc99dd00ee11ff22aa33bb44cc55dd66aa77bb88",
		Source:          "mercator://ws_1/artifacts/artifact:corpus:v3",
	}
	artifact.SourceCredential = domain.ArtifactRead{
		ContentCredentialScope: domain.ContentCredentialScope{
			Operation: artifact.Operation(),

			Content:   artifact.Content(),
			ExpiresAt: expiry,
		},
		Location: "https://objects.test/mercator/ws_1/artifacts/corpus?X-Amz-Signature=one-read",
	}

	if _, err := broker.Prepare(context.Background(), adapter.PrepareRequest{

		OperationKey: "prepare/ws_1",
		Wanted:       []adapter.PrepareItem{image, artifact},
	}); err != nil {
		t.Fatalf("prepare the queued Run's content on the node: %v", err)
	}

	if got := runtime.pull.RegistryCredential; got != image.RegistryCredential {
		t.Fatalf("the node was told to pull with %+v, want the material minted for that pull", got)
	}
	if got := runtime.read.SourceCredential; got != artifact.SourceCredential {
		t.Fatalf("the node was told to read with %+v, want the read minted for that fetch", got)
	}
	if runtime.read.Source != artifact.Source {
		t.Fatalf("the node was told the durable location is %q, want the catalog's own name for the content", runtime.read.Source)
	}
}

// TestAnImageAnyoneCanReadReachesTheNodeWithNoMaterial is the other answer, and
// the one that stops the Broker from becoming a place where a credential is
// invented. Content the control plane minted nothing for reaches the machine with
// nothing attached, and the node pulls it the way any anonymous reader would.
func TestAnImageAnyoneCanReadReachesTheNodeWithNoMaterial(t *testing.T) {
	runtime := &recordingRuntime{}
	broker := brokerServing(t, runtime, map[string]capability.Backend{})

	if _, err := broker.Prepare(context.Background(), adapter.PrepareRequest{

		OperationKey: "prepare/ws_1",
		Wanted: []adapter.PrepareItem{{
			Kind:            adapter.PrepareImage,
			OfferSnapshotID: "builder",
			ConnectionID:    nodeConnectionID,
			NativeRef:       "nod_builder",
			Image:           "runner@sha256:1b3d5f7a9c1e3b5d7f9a1c3e5b7d9f1a3c5e7b9d1f3a5c7e9b1d3f5a7c9e1b3d",
		}},
	}); err != nil {
		t.Fatalf("prepare a public image on the node: %v", err)
	}

	if !runtime.pull.RegistryCredential.Zero() {
		t.Fatalf("a machine was handed %+v to read an image any anonymous reader can have", runtime.pull.RegistryCredential)
	}
}

// recordingRuntime is the enrolled fleet as far as preparation is concerned: it
// resolves one node and remembers the two commands it was sent.
type recordingRuntime struct {
	Nodes
	pull capability.PrepareImageCommand
	read capability.PrepareArtifactCommand
}

func (runtime *recordingRuntime) Ref(_ context.Context, nodeID string) (capability.NodeRef, error) {
	return capability.NodeRef{NodeID: nodeID, Generation: 1}, nil
}

func (runtime *recordingRuntime) PrepareImage(_ context.Context, command capability.PrepareImageCommand) (capability.OperationReceipt, error) {
	runtime.pull = command
	return capability.OperationReceipt{OperationID: command.OperationID}, nil
}

func (runtime *recordingRuntime) PrepareArtifact(_ context.Context, command capability.PrepareArtifactCommand) (capability.OperationReceipt, error) {
	runtime.read = command
	return capability.OperationReceipt{OperationID: command.OperationID}, nil
}
