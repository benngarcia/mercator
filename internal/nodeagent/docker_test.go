package nodeagent

import (
	"context"
	"encoding/json"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/benngarcia/mercator/internal/capability"
)

// TestDockerRuntimeReportsTheLayersItUnpacked is the node half of the digest
// bridge, against a real daemon. A container runtime knows its layers only as
// uncompressed diff IDs, so what it reports has to be exactly that: reporting
// nothing would make every node look equally cold, and reporting the manifest's
// compressed blob digests would be inventing an answer it cannot have.
func TestDockerRuntimeReportsTheLayersItUnpacked(t *testing.T) {
	requireDocker(t)
	pull(t, "busybox:latest")
	var wantDiffIDs []string
	if err := json.Unmarshal([]byte(inspect(t, "busybox:latest", "{{json .RootFS.Layers}}")), &wantDiffIDs); err != nil {
		t.Fatalf("decode the daemon's layers: %v", err)
	}
	wantDigest := strings.TrimSpace(inspect(t, "busybox:latest", "{{index .RepoDigests 0}}"))
	_, digest, _ := strings.Cut(wantDigest, "@")

	facts, err := NewDockerRuntime("").Facts(context.Background())

	if err != nil {
		t.Fatalf("read node facts: %v", err)
	}
	index := slices.IndexFunc(facts.Images, func(image capability.ImageLocality) bool { return image.ManifestDigest == digest })
	if index < 0 {
		t.Fatalf("the node reports no image with manifest digest %s", digest)
	}
	if !slices.Equal(facts.Images[index].LayerDiffIDs, wantDiffIDs) {
		t.Fatalf("reported diff IDs = %v, the daemon holds %v", facts.Images[index].LayerDiffIDs, wantDiffIDs)
	}
}

func requireDocker(t *testing.T) {
	t.Helper()
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skipf("no reachable Docker daemon to read node facts from: %v", err)
	}
}

func pull(t *testing.T, reference string) {
	t.Helper()
	if output, err := exec.Command("docker", "pull", "--quiet", reference).CombinedOutput(); err != nil {
		t.Skipf("cannot pull %s on this machine: %v\n%s", reference, err, output)
	}
}

func inspect(t *testing.T, reference, format string) string {
	t.Helper()
	output, err := exec.Command("docker", "image", "inspect", reference, "--format", format).CombinedOutput()
	if err != nil {
		t.Fatalf("docker image inspect %s: %v\n%s", reference, err, output)
	}
	return strings.TrimSpace(string(output))
}
