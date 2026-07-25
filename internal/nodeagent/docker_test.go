package nodeagent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
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

// TestDockerRuntimeReportsNothingWhenItCannotReadWhatItHolds is the rule that
// silence stays uncertainty, at the one place a machine could invent an answer
// about itself. A daemon that lists an image and then refuses to describe it has
// told the agent nothing, and an image reported hot with no layers is
// indistinguishable downstream from a host that holds no part of it: Placement
// would price a full cold pull, at full confidence, for content this machine is
// sitting on. Losing a heartbeat is how a node says it does not currently know
// what it holds, and the next one has the answer.
func TestDockerRuntimeReportsNothingWhenItCannotReadWhatItHolds(t *testing.T) {
	daemon := standInDaemon(t, `#!/bin/sh
case "$1 $2" in
  "info "*|"info") echo '{"OperatingSystem":"linux","Architecture":"x86_64","ServerVersion":"27.0.0","NCPU":8,"MemTotal":1}' ;;
  "images "*) echo '{"ID":"sha256:abc","Digest":"sha256:1111111111111111111111111111111111111111111111111111111111111111"}' ;;
  "image inspect") echo 'Error response from daemon: request timed out' >&2; exit 1 ;;
esac
`)

	facts, err := NewDockerRuntime(daemon).Facts(context.Background())

	if err == nil {
		t.Fatalf("a daemon that would not say what it holds produced %+v, want a failed report", facts.Images)
	}
}

// standInDaemon is a scripted `docker` that answers what this case needs and
// fails the read it is about, which is the only way to drive a daemon that is
// listing images and refusing to describe them.
func standInDaemon(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write the stand-in daemon: %v", err)
	}
	return path
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
