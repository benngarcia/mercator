package nodeagent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/benngarcia/mercator/internal/capability"
	"github.com/benngarcia/mercator/internal/domain"
)

// TestDockerRuntimeReportsTheLayersItUnpacked is the node half of the digest
// bridge, against a real daemon. A container runtime knows its layers only as
// uncompressed diff IDs, so what it reports has to be exactly that: reporting
// nothing would make every node look equally cold, and reporting the manifest's
// compressed blob digests would be inventing an answer it cannot have. It also
// reports which build of that digest it holds, because a multi-platform image
// is listed under one index digest whichever platform was fetched.
func TestDockerRuntimeReportsTheLayersItUnpacked(t *testing.T) {
	requireDocker(t)
	pull(t, "busybox:latest")
	var wantDiffIDs []string
	if err := json.Unmarshal([]byte(inspect(t, "busybox:latest", "{{json .RootFS.Layers}}")), &wantDiffIDs); err != nil {
		t.Fatalf("decode the daemon's layers: %v", err)
	}
	wantDigest := strings.TrimSpace(inspect(t, "busybox:latest", "{{index .RepoDigests 0}}"))
	_, digest, _ := strings.Cut(wantDigest, "@")
	wantPlatform := domain.Platform{
		OS:           inspect(t, "busybox:latest", "{{.Os}}"),
		Architecture: inspect(t, "busybox:latest", "{{.Architecture}}"),
	}

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
	if facts.Images[index].Platform != wantPlatform {
		t.Fatalf("reported platform = %+v, the daemon holds the %+v build", facts.Images[index].Platform, wantPlatform)
	}
	if !facts.Images[index].Unpacked || facts.Images[index].State != domain.LocalityHot {
		t.Fatalf("an image this daemon can start a container on was reported %+v", facts.Images[index])
	}
}

// TestEveryImageThisDaemonHoldsIsAssembled is the other half of reading
// readiness off the storage driver: the rule has to fire on a machine with real
// images on it and not report a working host as half-built. Every image this
// daemon lists is one it can run, so every one of them must come back hot with a
// mount chain as deep as the layers its config declares. A rule that called a
// working host partial would price local assembly nobody owes, which is the
// defect it replaced pointing the other way.
func TestEveryImageThisDaemonHoldsIsAssembled(t *testing.T) {
	requireDocker(t)
	pull(t, "busybox:latest")

	facts, err := NewDockerRuntime("").Facts(context.Background())

	if err != nil {
		t.Fatalf("read node facts: %v", err)
	}
	if len(facts.Images) == 0 {
		t.Skip("this daemon holds no digest-named image to read")
	}
	for _, image := range facts.Images {
		if image.State != domain.LocalityHot || !image.Unpacked {
			t.Errorf("image %s was reported %q (unpacked %v), and this daemon can run every image it lists",
				image.ManifestDigest, image.State, image.Unpacked)
		}
		if image.LastVerifiedAt.IsZero() {
			t.Errorf("image %s was reported without saying when it was last looked at", image.ManifestDigest)
		}
	}
}

// TestDockerRuntimeSeparatesWhatItUnpackedFromWhatItPulled is the claim this
// node used to make and could not support: every image it could list was
// reported hot and unpacked, whatever state its storage was in. Being listed
// says the content arrived; only the storage driver's own layer chain says a
// container can be started on it. An image missing part of that chain is here
// and not runnable, and the layers it does report are exactly the ones it
// assembled, because that chain is ordered from the base up just as the config
// orders its diff IDs.
func TestDockerRuntimeSeparatesWhatItUnpackedFromWhatItPulled(t *testing.T) {
	daemon := standInDaemon(t, `#!/bin/sh
case "$1 $2 $3" in
  "info "*) echo '{"OperatingSystem":"linux","Architecture":"x86_64","ServerVersion":"29.4.0","NCPU":8,"MemTotal":1}' ;;
  "images "*) echo '{"ID":"sha256:whole","Digest":"sha256:1111111111111111111111111111111111111111111111111111111111111111"}'
              echo '{"ID":"sha256:half","Digest":"sha256:2222222222222222222222222222222222222222222222222222222222222222"}'
              echo '{"ID":"sha256:none","Digest":"sha256:3333333333333333333333333333333333333333333333333333333333333333"}' ;;
  "image inspect sha256:whole") echo '{"os":"linux","architecture":"amd64","diff_ids":["sha256:aaaa","sha256:bbbb"],"mount_chain":{"LowerDir":"/var/lib/docker/overlay2/base/diff","UpperDir":"/var/lib/docker/overlay2/top/diff"}}' ;;
  "image inspect sha256:half") echo '{"os":"linux","architecture":"amd64","diff_ids":["sha256:aaaa","sha256:bbbb"],"mount_chain":{"UpperDir":"/var/lib/docker/overlay2/base/diff"}}' ;;
  "image inspect sha256:none") echo '{"os":"linux","architecture":"amd64","diff_ids":["sha256:aaaa","sha256:bbbb"],"mount_chain":null}' ;;
esac
`)

	facts, err := NewDockerRuntime(daemon).Facts(context.Background())

	if err != nil {
		t.Fatalf("read node facts: %v", err)
	}
	if len(facts.Images) != 3 {
		t.Fatalf("reported %d images, want the assembled, half-assembled, and unassembled ones: %+v", len(facts.Images), facts.Images)
	}
	whole, half, none := facts.Images[0], facts.Images[1], facts.Images[2]
	if whole.State != domain.LocalityHot || !whole.Unpacked || len(whole.LayerDiffIDs) != 2 {
		t.Errorf("an image whose chain covers every layer was reported %+v, want hot and unpacked", whole)
	}
	if half.State != domain.LocalityPartial || half.Unpacked {
		t.Errorf("an image missing part of its chain was reported %+v, want partial", half)
	}
	if !slices.Equal(half.LayerDiffIDs, []string{"sha256:aaaa"}) {
		t.Errorf("a half-assembled image reported %v, want only the base layer it can mount", half.LayerDiffIDs)
	}
	if none.State != domain.LocalityCold || none.Unpacked || len(none.LayerDiffIDs) != 0 {
		t.Errorf("an image whose content is here and unassembled was reported %+v, want cold with no layers", none)
	}
}

// TestDockerRuntimeSaysWhichImageItCannotDescribe is the rule that silence
// stays uncertainty, at the one place a machine could invent an answer about
// itself, and that one such silence is about one image. An image reported hot
// with no layers is indistinguishable downstream from a host holding no part of
// it, so Placement would price a full cold pull, at full confidence, for content
// this machine is sitting on. The image the daemon would not describe is
// reported unknown; the image beside it, which the daemon described perfectly
// well, is still reported. Failing the whole report would have cost this node
// its session, and a node with no session yet its enrollment, because one image
// was pruned between the listing and the read.
func TestDockerRuntimeSaysWhichImageItCannotDescribe(t *testing.T) {
	daemon := standInDaemon(t, `#!/bin/sh
case "$1 $2 $3" in
  "info "*) echo '{"OperatingSystem":"linux","Architecture":"x86_64","ServerVersion":"27.0.0","NCPU":8,"MemTotal":1}' ;;
  "images "*) echo '{"ID":"sha256:gone","Digest":"sha256:1111111111111111111111111111111111111111111111111111111111111111"}'
              echo '{"ID":"sha256:here","Digest":"sha256:2222222222222222222222222222222222222222222222222222222222222222"}' ;;
  "image inspect sha256:gone") echo 'Error response from daemon: no such image' >&2; exit 1 ;;
  "image inspect sha256:here") echo '{"os":"linux","architecture":"amd64","diff_ids":["sha256:3333333333333333333333333333333333333333333333333333333333333333"],"mount_chain":{"UpperDir":"/var/lib/docker/overlay2/only/diff"}}' ;;
esac
`)

	facts, err := NewDockerRuntime(daemon).Facts(context.Background())

	if err != nil {
		t.Fatalf("read node facts: %v", err)
	}
	if len(facts.Images) != 2 {
		t.Fatalf("reported %d images, want the one it could describe and the one it could not: %+v", len(facts.Images), facts.Images)
	}
	undescribed, described := facts.Images[0], facts.Images[1]
	if undescribed.State != domain.LocalityUnknown || len(undescribed.LayerDiffIDs) > 0 {
		t.Errorf("an image the daemon would not describe was reported %+v, want unknown content", undescribed)
	}
	if described.State != domain.LocalityHot || len(described.LayerDiffIDs) != 1 {
		t.Errorf("an image the daemon described was reported %+v, want the layer it named", described)
	}
	if described.Platform != (domain.Platform{OS: "linux", Architecture: "amd64"}) {
		t.Errorf("reported platform = %+v, want the build the daemon says it holds", described.Platform)
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

// TestTheDockerRuntimeRefusesTheWorkItDoesNotDo is the machine half of the same
// promise: what the node declares and what the runtime answers have to be the
// same answer, and this is the one place the runtime says no out loud.
func TestTheDockerRuntimeRefusesTheWorkItDoesNotDo(t *testing.T) {
	runtime := NewDockerRuntime("")

	err := runtime.PrepareArtifact(context.Background(), capability.PrepareArtifactCommand{})

	if !errors.Is(err, capability.ErrCapabilityUnsupported) {
		t.Fatalf("preparing an Artifact returned %v, want an unsupported-capability refusal", err)
	}
}
