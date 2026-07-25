package nodeagent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
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
	if facts.Images[index].State != domain.LocalityHot || !facts.Images[index].ContentPresent {
		t.Fatalf("an image this daemon can start a container on was reported %+v", facts.Images[index])
	}
}

// TestEveryImageThisDaemonHoldsIsAssembled is the other half of stating
// readiness: the rule has to fire on a machine with real images on it and not
// report a working host as half-built. Every image the daemon on this machine
// lists is one it can run, so every one of them must come back hot. A rule that
// called a working host partial would price local assembly nobody owes, and one
// that called it cold would send its own work to a machine that has to fetch
// what this one is sitting on.
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
		if image.State != domain.LocalityHot || !image.ContentPresent {
			t.Errorf("image %s was reported %q (content present %v), and this daemon can run every image it lists",
				image.ManifestDigest, image.State, image.ContentPresent)
		}
		if image.LastVerifiedAt.IsZero() {
			t.Errorf("image %s was reported without saying when it was last looked at", image.ManifestDigest)
		}
	}
}

// TestDockerRuntimeSeparatesWhatItUnpackedFromWhatItPulled is the claim this
// node used to make and could not support: every image it could list was
// reported hot and unpacked, whatever state its storage was in. Being listed
// says a manifest is here; on a daemon keeping images in the containerd content
// store, which is what Docker Engine 29 installs by default, content and
// snapshots are separate, so an image can be listed with its bytes missing and
// it can hold every byte with nothing to start from. Only that store says which,
// and only over the API: `docker image inspect` reports no storage chain at all
// for it.
func TestDockerRuntimeSeparatesWhatItUnpackedFromWhatItPulled(t *testing.T) {
	daemon := standInContentStore(t, `[
	  {"Id":"sha256:whole","Manifests":[
	    {"Kind":"attestation","Available":true},
	    {"Kind":"image","Available":true,
	     "ImageData":{"Platform":{"os":"linux","architecture":"amd64"},"Size":{"Unpacked":41000000}}}]},
	  {"Id":"sha256:fetched","Manifests":[{"Kind":"image","Available":true,
	    "ImageData":{"Platform":{"os":"linux","architecture":"amd64"},"Size":{"Unpacked":0}}}]},
	  {"Id":"sha256:absent","Manifests":[{"Kind":"image","Available":false,
	    "ImageData":{"Platform":{"os":"linux","architecture":"amd64"},"Size":{"Unpacked":0}}}]}
	]`)

	facts, err := NewDockerRuntime(daemon).Facts(context.Background())

	if err != nil {
		t.Fatalf("read node facts: %v", err)
	}
	if len(facts.Images) != 4 {
		t.Fatalf("reported %d images, want the unpacked, fetched, absent, and unaccounted ones: %+v", len(facts.Images), facts.Images)
	}
	whole, fetched, absent, vanished := facts.Images[0], facts.Images[1], facts.Images[2], facts.Images[3]
	if whole.State != domain.LocalityHot || !whole.ContentPresent || len(whole.LayerDiffIDs) != 2 {
		t.Errorf("an image the daemon has unpacked was reported %+v, want hot with the layers it can mount", whole)
	}
	if fetched.State != domain.LocalityPartial || !fetched.ContentPresent || len(fetched.LayerDiffIDs) != 0 {
		t.Errorf("an image whose bytes are here and whose chain is not built was reported %+v, want partial with no mountable layer", fetched)
	}
	if absent.State != domain.LocalityCold || absent.ContentPresent {
		t.Errorf("an image the daemon lists and does not hold was reported %+v, want cold", absent)
	}
	if vanished.State != domain.LocalityUnknown || vanished.ContentPresent {
		t.Errorf("an image the store did not account for was reported %+v, want unknown", vanished)
	}
}

// TestEveryImageAGraphDriverDaemonListsIsRunnable is the same question asked of
// the other image store, and the case that refutes reading readiness off
// GraphDriver.Data. A graph driver's layer store holds applied layers only, so
// listing an image is the evidence that a container can start on it; the chain
// it names for that image is decoration, and three of the four drivers name
// none. This daemon is btrfs, which returns no storage metadata whatsoever, and
// every image on it is startable.
func TestEveryImageAGraphDriverDaemonListsIsRunnable(t *testing.T) {
	daemon := standInDaemon(t, `#!/bin/sh
case "$1 $2 $3" in
  "info "*) echo '{"OperatingSystem":"linux","Architecture":"x86_64","ServerVersion":"29.4.0","NCPU":8,"MemTotal":1,"DriverStatus":[["Build Version","Btrfs v6.6"]]}' ;;
  "images "*) echo '{"ID":"sha256:whole","Digest":"sha256:1111111111111111111111111111111111111111111111111111111111111111"}' ;;
  "image inspect sha256:whole") echo '{"os":"linux","architecture":"amd64","diff_ids":["sha256:aaaa","sha256:bbbb"]}' ;;
esac
`)

	facts, err := NewDockerRuntime(daemon).Facts(context.Background())

	if err != nil {
		t.Fatalf("read node facts: %v", err)
	}
	if len(facts.Images) != 1 {
		t.Fatalf("reported %d images, want the one this daemon lists: %+v", len(facts.Images), facts.Images)
	}
	image := facts.Images[0]
	if image.State != domain.LocalityHot || !image.ContentPresent {
		t.Fatalf("an image on a driver that names no storage chain was reported %+v, want hot: it applied every layer before listing it", image)
	}
	if !slices.Equal(image.LayerDiffIDs, []string{"sha256:aaaa", "sha256:bbbb"}) {
		t.Fatalf("reported layers %v, want every layer of an assembled image", image.LayerDiffIDs)
	}
}

// TestDockerRuntimeRefusesToDescribeAContentStoreItCannotRead is the failure
// this node must not paper over. A daemon on the content store that will not
// answer for its images leaves nothing to say about the machine's warmth, and
// both available guesses are wrong in a way that costs a placement: calling it
// cold sends its own work to a host that has to fetch 18GB, and calling it hot
// promises a start it may not be able to make. The report fails and the operator
// hears about it.
func TestDockerRuntimeRefusesToDescribeAContentStoreItCannotRead(t *testing.T) {
	daemon := standInContentStore(t, "")
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:1")

	_, err := NewDockerRuntime(daemon).Facts(context.Background())

	if err == nil {
		t.Fatal("a daemon whose image store could not be read still produced facts about what it holds")
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

// standInContentStore is a scripted daemon keeping its images in the containerd
// content store. It answers the CLI the way moby does for that store, which is
// with no storage chain for any image whatsoever, and answers the API with the
// account of its content that only the API carries. It lists one image more
// than the store accounts for, which is what a prune between the two reads
// leaves behind.
func standInContentStore(t *testing.T, listed string) string {
	t.Helper()
	t.Setenv("DOCKER_HOST", "")
	store := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1.48/images/json" || request.URL.Query().Get("manifests") != "1" {
			http.Error(writer, "unexpected request "+request.URL.String(), http.StatusNotFound)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, listed)
	}))
	t.Cleanup(store.Close)
	return standInDaemon(t, `#!/bin/sh
case "$1 $2 $3" in
  "info "*) echo '{"OperatingSystem":"linux","Architecture":"x86_64","ServerVersion":"29.4.0","NCPU":8,"MemTotal":1,"DriverStatus":[["driver-type","io.containerd.snapshotter.v1"]]}' ;;
  "context inspect --format") echo '`+strings.Replace(store.URL, "http://", "tcp://", 1)+`' ;;
  "images "*) echo '{"ID":"sha256:whole","Digest":"sha256:1111111111111111111111111111111111111111111111111111111111111111"}'
              echo '{"ID":"sha256:fetched","Digest":"sha256:2222222222222222222222222222222222222222222222222222222222222222"}'
              echo '{"ID":"sha256:absent","Digest":"sha256:3333333333333333333333333333333333333333333333333333333333333333"}'
              echo '{"ID":"sha256:vanished","Digest":"sha256:4444444444444444444444444444444444444444444444444444444444444444"}' ;;
  "image inspect "*) echo '{"os":"linux","architecture":"amd64","diff_ids":["sha256:aaaa","sha256:bbbb"]}' ;;
esac
`)
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
