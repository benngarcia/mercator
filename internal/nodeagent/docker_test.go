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
//
// The fourth machine state is the one an interrupted pull leaves. moby's
// Available is all-or-nothing over every blob a manifest references, so a host
// that fetched all but the last layer reports it false while holding almost
// every byte, and the bytes it holds have no names attached. Calling that cold
// would send the retry at a machine holding none of the image.
func TestDockerRuntimeSeparatesWhatItUnpackedFromWhatItPulled(t *testing.T) {
	daemon := standInContentStore(t, `[
	  {"Id":"sha256:whole","Descriptor":{"digest":"sha256:index-whole"},"Manifests":[
	    {"Kind":"attestation","Available":true},
	    {"Kind":"image","Available":true,"Size":{"Content":41000000},
	     "ImageData":{"Platform":{"os":"linux","architecture":"amd64"},"Size":{"Unpacked":41000000}}}]},
	  {"Id":"sha256:fetched","Descriptor":{"digest":"sha256:index-fetched"},"Manifests":[
	    {"Kind":"image","Available":true,"Size":{"Content":41000000},
	     "ImageData":{"Platform":{"os":"linux","architecture":"amd64"},"Size":{"Unpacked":0}}}]},
	  {"Id":"sha256:torn","Descriptor":{"digest":"sha256:index-torn"},"Manifests":[
	    {"Kind":"image","Available":false,"Size":{"Content":17000000000},
	     "ImageData":{"Platform":{"os":"linux","architecture":"amd64"},"Size":{"Unpacked":0}}}]},
	  {"Id":"sha256:absent","Descriptor":{"digest":"sha256:index-absent"},"Manifests":[
	    {"Kind":"image","Available":false,"Size":{"Content":0},
	     "ImageData":{"Platform":{"os":"linux","architecture":"amd64"},"Size":{"Unpacked":0}}}]}
	]`)

	facts, err := NewDockerRuntime(daemon).Facts(context.Background())

	if err != nil {
		t.Fatalf("read node facts: %v", err)
	}
	if len(facts.Images) != 4 {
		t.Fatalf("reported %d images, want the unpacked, fetched, torn, and absent ones: %+v", len(facts.Images), facts.Images)
	}
	whole, fetched, torn, absent := facts.Images[0], facts.Images[1], facts.Images[2], facts.Images[3]
	if whole.State != domain.LocalityHot || !whole.ContentPresent || len(whole.LayerDiffIDs) != 2 {
		t.Errorf("an image the daemon has unpacked was reported %+v, want hot with the layers it can mount", whole)
	}
	if fetched.State != domain.LocalityPartial || !fetched.ContentPresent || len(fetched.LayerDiffIDs) != 0 {
		t.Errorf("an image whose bytes are here and whose chain is not built was reported %+v, want partial with no mountable layer", fetched)
	}
	if torn.State != domain.LocalityUnknown || torn.ContentPresent {
		t.Errorf("an image this store holds 17GB of and cannot name a layer of was reported %+v, want unknown", torn)
	}
	if absent.State != domain.LocalityCold || absent.ContentPresent {
		t.Errorf("an image the daemon lists and holds no byte of was reported %+v, want cold", absent)
	}
}

// TestAContentStoreImageIsNamedByTheDigestARunIsPinnedTo is the name half of the
// same report, and the one a locality answer is worthless without. This store
// records a multi-platform image under the platform manifest it selected and
// builds RepoDigests from that same value, so `docker images --digests` prints,
// in both its ID and its Digest column, a manifest no Run is ever pinned to
// (moby, daemon/containerd/image_list.go singlePlatformImage over an
// ImageManifest whose Target NewImageManifest replaced with the platform
// descriptor). The control plane pins to the index above it, which is what the
// daemon's own image record targets and what it reports as the image's
// Descriptor. Filed under either printed name, a correct hot answer is filed
// where the scheduler's subtraction can never find it, and the host holding
// every byte is priced a full fetch.
//
// An image the store does not account for at all is not reported: this daemon
// has no name for it that the control plane could match, and inventing one
// would be filing an answer nothing can read.
func TestAContentStoreImageIsNamedByTheDigestARunIsPinnedTo(t *testing.T) {
	daemon := standInContentStore(t, `[
	  {"Id":"sha256:whole","Descriptor":{"digest":"sha256:index-whole"},"Manifests":[
	    {"Kind":"image","Available":true,"Size":{"Content":41000000},
	     "ImageData":{"Platform":{"os":"linux","architecture":"amd64"},"Size":{"Unpacked":41000000}}}]}
	]`)

	facts, err := NewDockerRuntime(daemon).Facts(context.Background())

	if err != nil {
		t.Fatalf("read node facts: %v", err)
	}
	if len(facts.Images) != 1 {
		t.Fatalf("reported %d images, want only the one this store accounts for: %+v", len(facts.Images), facts.Images)
	}
	if got := facts.Images[0].ManifestDigest; got != "sha256:index-whole" {
		t.Fatalf("the node named its image %q, and a Run on it is pinned to sha256:index-whole", got)
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

// TestTwoWorkspacesGetTwoVolumesForOneCacheName is the hard isolation claim
// against a real daemon. Two tenants declare a cache called compiler-cache and
// both containers run on this machine; what each is attached to is read back with
// `docker inspect` rather than asserted about the arguments this code built,
// because the promise is about the running container and not about a string.
//
// The third launch is the same tenant and name under a new compatibility key. The
// application has said that content belongs to another generation, so it gets its
// own volume too: a comparison Mercator makes and then mounts across anyway would
// be a comparison with no consequence.
func TestTwoWorkspacesGetTwoVolumesForOneCacheName(t *testing.T) {
	requireDocker(t)
	pull(t, "busybox:latest")
	runtime := NewDockerRuntime("")
	cache := domain.CacheMountRequirement{Name: "compiler-cache", CompatibilityKey: "cuda-12.4"}
	nextGeneration := domain.CacheMountRequirement{Name: "compiler-cache", CompatibilityKey: "cuda-13.0"}

	alpha := launchWithCache(t, runtime, "ws_alpha", "run-alpha", cache)
	beta := launchWithCache(t, runtime, "ws_beta", "run-beta", cache)
	rebuilt := launchWithCache(t, runtime, "ws_alpha", "run-rebuilt", nextGeneration)

	if alpha == beta {
		t.Fatalf("two workspaces naming one cache were attached to the same volume %q", alpha)
	}
	if alpha == rebuilt {
		t.Fatalf("a new compatibility key was attached to the previous generation's volume %q", alpha)
	}
	if want := domain.CacheVolumeName("ws_alpha", cache); alpha != want {
		t.Fatalf("the running container is attached to %q, and this cache's volume is %q", alpha, want)
	}
	if !strings.Contains(alpha, "ws_alpha") || !strings.Contains(beta, "ws_beta") {
		t.Fatalf("volumes %q and %q do not name the workspace that owns them", alpha, beta)
	}

	facts, err := runtime.Facts(context.Background())
	if err != nil {
		t.Fatalf("read node facts: %v", err)
	}
	if !facts.Caches.Known {
		t.Fatal("a node that enumerated its caches reported that it had not")
	}
	if !facts.Caches.Holds("ws_alpha", cache) || !facts.Caches.Holds("ws_beta", cache) {
		t.Fatalf("the node reports %+v, and both tenants' caches are on this disk", facts.Caches.Mounts)
	}
	if facts.Caches.Holds("ws_beta", nextGeneration) {
		t.Fatal("the node reports a generation for a tenant that never asked for one")
	}
}

// TestALaunchThatNeverRunsLeavesNoCacheBehind is what makes a reported cache
// mean something. Creating the container is what creates its storage, so a
// launch the daemon refuses before it gets there leaves nothing on the disk and
// nothing in the node's report. The agent used to open the volume itself before
// dispatching the run, which made every failed launch a machine advertising a
// cache no workload of that tenant and generation was ever attached to, and the
// next Run's decision recorded that empty directory as warmth.
func TestALaunchThatNeverRunsLeavesNoCacheBehind(t *testing.T) {
	requireDocker(t)
	runtime := NewDockerRuntime("")
	cache := domain.CacheMountRequirement{Name: "never-run-cache", CompatibilityKey: "cuda-12.4"}
	volume := domain.CacheVolumeName("ws_alpha", cache)
	t.Cleanup(func() { _ = exec.Command("docker", "volume", "rm", "--force", volume).Run() })
	command := capability.LaunchWorkloadCommand{
		RunID:       "run-never",
		AttemptID:   "1",
		BookingID:   "bkg-run-never",
		CacheMounts: []domain.CacheMountRequirement{cache},
		// A digest no registry can serve, which is the ordinary way a launch
		// dies before the daemon creates anything.
		Workload: domain.WorkloadSpec{Containers: []domain.ContainerSpec{{
			Name:  "main",
			Image: "busybox@sha256:0000000000000000000000000000000000000000000000000000000000000000",
		}}},
	}
	command.WorkspaceID = "ws_alpha"

	if err := runtime.LaunchWorkload(context.Background(), command); err == nil {
		t.Fatal("a launch of an image no registry can serve reported success")
	}

	if err := exec.Command("docker", "volume", "inspect", volume).Run(); err == nil {
		t.Fatalf("the failed launch left volume %q on this machine", volume)
	}
	facts, err := runtime.Facts(context.Background())
	if err != nil {
		t.Fatalf("read node facts: %v", err)
	}
	if facts.Caches.Holds("ws_alpha", cache) {
		t.Fatalf("the node reports holding a cache no workload ever ran against: %+v", facts.Caches.Mounts)
	}
}

// TestAContainerThatNeverStartsIsNotACacheThisNodeHolds is the other half of
// that promise, and the one creating the container cannot make. The daemon
// resolves the image, creates the container, and creates the labelled volume its
// mount point names before it asks the runtime for a process, so an entrypoint
// this image does not carry exits 127 with the volume already on the disk and no
// workload ever run against it. Left to the labels alone, the next heartbeat
// reports that empty directory as a cache and the next Run declaring the same
// generation is recorded hot on a machine that has never done the work.
//
// So the volume stays, because reclaiming storage is garbage collection, and the
// report leaves it out, because no container of this node's has run against it.
func TestAContainerThatNeverStartsIsNotACacheThisNodeHolds(t *testing.T) {
	requireDocker(t)
	pull(t, "busybox:latest")
	runtime := NewDockerRuntime("")
	cache := domain.CacheMountRequirement{Name: "never-started-cache", CompatibilityKey: "cuda-12.4"}
	volume := domain.CacheVolumeName("ws_alpha", cache)
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "--force", "mercator-run-never-started-1").Run()
		_ = exec.Command("docker", "volume", "rm", "--force", volume).Run()
	})
	command := capability.LaunchWorkloadCommand{
		RunID:       "run-never-started",
		AttemptID:   "1",
		BookingID:   "bkg-run-never-started",
		CacheMounts: []domain.CacheMountRequirement{cache},
		Workload: domain.WorkloadSpec{Containers: []domain.ContainerSpec{{
			Name:  "main",
			Image: "busybox:latest",
			// A program this image does not contain, which is the ordinary way
			// a container is created and then never runs.
			Entrypoint: &[]string{"/definitely-not-in-this-image"},
		}}},
	}
	command.WorkspaceID = "ws_alpha"

	if err := runtime.LaunchWorkload(context.Background(), command); err == nil {
		t.Fatal("a launch whose container could not start reported success")
	}

	if err := exec.Command("docker", "volume", "inspect", volume).Run(); err != nil {
		t.Skipf("this daemon did not create %q for a container it could not start, so the case under test did not happen", volume)
	}
	facts, err := runtime.Facts(context.Background())
	if err != nil {
		t.Fatalf("read node facts: %v", err)
	}
	if facts.Caches.Holds("ws_alpha", cache) {
		t.Fatalf("the node reports a cache whose container never started: %+v", facts.Caches.Mounts)
	}
}

// TestOneUnreadableCacheVolumeDoesNotCostTheNodeItsReport is the cache half of
// the lesson the image read already carries. The daemon prints the volumes it
// could describe and exits non-zero for the one that vanished between the
// listing and the read, which is what `docker volume prune` on a working machine
// looks like from here. Failing the whole report would end this agent's session
// and, on an agent with no session yet, block its enrollment, over mutable state
// that is best-effort by construction.
func TestOneUnreadableCacheVolumeDoesNotCostTheNodeItsReport(t *testing.T) {
	daemon := standInDaemon(t, `#!/bin/sh
case "$1 $2" in
  "info --format") echo '{"OperatingSystem":"linux","Architecture":"x86_64","ServerVersion":"29.4.0","NCPU":8,"MemTotal":1}' ;;
  "images --digests") ;;
  "ps --all") echo '{"Names":"mercator-run-alpha-1","State":"exited","Status":"Exited (0) 2m ago","Labels":"mercator.run=run-alpha","Mounts":"mercator-cache-ws_alpha-compiler-cache-aaaaaaaa,mercator-cache-ws_alpha-pruned-cache-bbbbbbbb"}' ;;
  "volume ls") echo 'mercator-cache-ws_alpha-compiler-cache-aaaaaaaa'
               echo 'mercator-cache-ws_alpha-pruned-cache-bbbbbbbb' ;;
  "volume inspect") echo '{"name":"mercator-cache-ws_alpha-compiler-cache-aaaaaaaa","created_at":"2030-01-01T00:00:00Z","labels":{"mercator.cache.workspace":"ws_alpha","mercator.cache.name":"compiler-cache","mercator.cache.key":"cuda-12.4"}}'
                    echo 'Error response from daemon: get mercator-cache-ws_alpha-pruned-cache-bbbbbbbb: no such volume' >&2
                    exit 1 ;;
esac
`)

	facts, err := NewDockerRuntime(daemon).Facts(context.Background())

	if err != nil {
		t.Fatalf("one pruned volume cost this node its whole facts report: %v", err)
	}
	if !facts.Caches.Known {
		t.Fatal("a node that described a cache reported that it had enumerated nothing")
	}
	if !facts.Caches.Holds("ws_alpha", domain.CacheMountRequirement{Name: "compiler-cache", CompatibilityKey: "cuda-12.4"}) {
		t.Fatalf("the node dropped the cache the daemon described: %+v", facts.Caches.Mounts)
	}
}

// TestANodeReportsOnlyTheCachesAWorkloadRanAgainst is the rule itself, on a
// machine holding one of each. Both volumes carry the labels that name a cache,
// because the daemon stamps them when it creates the mount point; only one of
// them belongs to a container that ever held a process. The other is what a
// launch that got as far as container creation and no further leaves behind, and
// the whole point of reporting a cache is to say a workload of this tenant and
// generation ran here.
func TestANodeReportsOnlyTheCachesAWorkloadRanAgainst(t *testing.T) {
	daemon := standInDaemon(t, `#!/bin/sh
case "$1 $2" in
  "info --format") echo '{"OperatingSystem":"linux","Architecture":"x86_64","ServerVersion":"29.4.0","NCPU":8,"MemTotal":1}' ;;
  "images --digests") ;;
  "ps --all") echo '{"Names":"mercator-run-ran-1","State":"exited","Status":"Exited (0) 2m ago","Labels":"mercator.run=run-ran","Mounts":"mercator-cache-ws_alpha-ran-cache-aaaaaaaa"}'
              echo '{"Names":"mercator-run-stillborn-1","State":"created","Status":"Created","Labels":"mercator.run=run-stillborn","Mounts":"mercator-cache-ws_alpha-stillborn-cache-bbbbbbbb"}' ;;
  "volume ls") echo 'mercator-cache-ws_alpha-ran-cache-aaaaaaaa'
               echo 'mercator-cache-ws_alpha-stillborn-cache-bbbbbbbb' ;;
  "volume inspect")
    for volume in "$@"; do
      case "$volume" in
        *ran-cache*) echo '{"name":"mercator-cache-ws_alpha-ran-cache-aaaaaaaa","created_at":"2030-01-01T00:00:00Z","labels":{"mercator.cache.workspace":"ws_alpha","mercator.cache.name":"ran-cache","mercator.cache.key":"cuda-12.4"}}' ;;
        *stillborn-cache*) echo '{"name":"mercator-cache-ws_alpha-stillborn-cache-bbbbbbbb","created_at":"2030-01-01T00:00:00Z","labels":{"mercator.cache.workspace":"ws_alpha","mercator.cache.name":"stillborn-cache","mercator.cache.key":"cuda-12.4"}}' ;;
      esac
    done ;;
esac
`)

	facts, err := NewDockerRuntime(daemon).Facts(context.Background())

	if err != nil {
		t.Fatalf("read node facts: %v", err)
	}
	if !facts.Caches.Holds("ws_alpha", domain.CacheMountRequirement{Name: "ran-cache", CompatibilityKey: "cuda-12.4"}) {
		t.Fatalf("the node dropped a cache a workload ran against: %+v", facts.Caches.Mounts)
	}
	if facts.Caches.Holds("ws_alpha", domain.CacheMountRequirement{Name: "stillborn-cache", CompatibilityKey: "cuda-12.4"}) {
		t.Fatalf("the node reports a cache whose container never started: %+v", facts.Caches.Mounts)
	}
}

// TestANodeThatCannotReadItsCachesSaysNothing is the other end of that rule. A
// daemon that will not answer at all leaves this node with nothing to claim, and
// silence is what it reports: an inventory marked enumerated and empty would be
// this machine asserting it holds no cache anywhere, which is a fact nobody
// established and which prices every candidate as having never done the work.
func TestANodeThatCannotReadItsCachesSaysNothing(t *testing.T) {
	daemon := standInDaemon(t, `#!/bin/sh
case "$1 $2" in
  "info --format") echo '{"OperatingSystem":"linux","Architecture":"x86_64","ServerVersion":"29.4.0","NCPU":8,"MemTotal":1}' ;;
  "images --digests") ;;
  "volume ls") echo 'Cannot connect to the Docker daemon' >&2; exit 1 ;;
esac
`)

	facts, err := NewDockerRuntime(daemon).Facts(context.Background())

	if err != nil {
		t.Fatalf("a cache read this node could not make cost it its whole facts report: %v", err)
	}
	if facts.Caches.Known {
		t.Fatalf("a node that could not read its caches claims it enumerated them: %+v", facts.Caches)
	}
}

// launchWithCache runs one throwaway container with one cache attached and
// answers which volume the daemon says it mounted.
func launchWithCache(t *testing.T, runtime *DockerRuntime, workspaceID, runID string, cache domain.CacheMountRequirement) string {
	t.Helper()
	command := capability.LaunchWorkloadCommand{
		RunID:       runID,
		AttemptID:   "1",
		BookingID:   "bkg-" + runID,
		CacheMounts: []domain.CacheMountRequirement{cache},
		Workload: domain.WorkloadSpec{Containers: []domain.ContainerSpec{{
			Name:  "main",
			Image: "busybox:latest",
			Args:  []string{"sleep", "30"},
		}}},
	}
	command.WorkspaceID = workspaceID
	container := "mercator-" + runID + "-1"
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "--force", container).Run()
		_ = exec.Command("docker", "volume", "rm", "--force", domain.CacheVolumeName(workspaceID, cache)).Run()
	})
	if err := runtime.LaunchWorkload(context.Background(), command); err != nil {
		t.Fatalf("launch %s: %v", runID, err)
	}
	output, err := exec.Command("docker", "inspect", container, "--format",
		`{{range .Mounts}}{{if eq .Destination "`+domain.CacheMountPath(cache.Name)+`"}}{{.Name}}{{end}}{{end}}`).CombinedOutput()
	if err != nil {
		t.Fatalf("docker inspect %s: %v\n%s", container, err, output)
	}
	mounted := strings.TrimSpace(string(output))
	if mounted == "" {
		t.Fatalf("the container for %s has nothing mounted at %s", runID, domain.CacheMountPath(cache.Name))
	}
	return mounted
}

// standInContentStore is a scripted daemon keeping its images in the containerd
// content store. It answers the CLI the way moby does for that store: no storage
// chain for any image whatsoever, and an ID and Digest column naming the
// platform manifest rather than the index a Run is pinned to. The API carries
// the account of its content, and the index digest, that only the API carries.
// The CLI lists one image more than the store accounts for, which is what a pull
// landing between the two reads leaves behind.
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
              echo '{"ID":"sha256:torn","Digest":"sha256:3333333333333333333333333333333333333333333333333333333333333333"}'
              echo '{"ID":"sha256:absent","Digest":"sha256:4444444444444444444444444444444444444444444444444444444444444444"}'
              echo '{"ID":"sha256:vanished","Digest":"sha256:5555555555555555555555555555555555555555555555555555555555555555"}' ;;
  "image inspect "*) echo '{"os":"linux","architecture":"amd64","diff_ids":["sha256:aaaa","sha256:bbbb"]}' ;;
esac
`)
}

// standInDaemon is a scripted `docker` that answers what this case needs and
// fails the read it is about, which is the only way to drive a daemon that is
// listing images and refusing to describe them.
//
// Every stand-in answers the disk probe, because a facts report starts with how
// much room the machine has and a daemon that cannot say makes no report at all.
// None of these cases are about the disk, so the answer is prepended here rather
// than written into each script.
func standInDaemon(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "docker")
	answered := strings.Replace(script, "#!/bin/sh\n", "#!/bin/sh\n"+diskProbeAnswer, 1)
	if err := os.WriteFile(path, []byte(answered), 0o755); err != nil {
		t.Fatalf("write the stand-in daemon: %v", err)
	}
	return path
}

// diskProbeAnswer is a 200GiB root filesystem with 180GiB free, in the columns
// POSIX df prints them.
const diskProbeAnswer = `if [ "$1" = "run" ]; then
  echo 'Filesystem     1024-blocks      Used Available Capacity Mounted on'
  echo 'overlay          209715200  20971520 188743680      10% /'
  exit 0
fi
`

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

// TestANodeReportsTheDiskItsDaemonHas is the disk half of a facts report. Every
// enrolled node advertised zero ephemeral disk before this, because
// HostFacts.DiskTotalBytes and DiskFreeBytes were declared and nothing wrote
// them, and the offer projection mapped that zero straight onto the resource a
// workload's disk minimum is compared against. So a Run asking for a gigabyte
// was refused on every node in the fleet.
func TestANodeReportsTheDiskItsDaemonHas(t *testing.T) {
	daemon := standInDaemon(t, `#!/bin/sh
case "$1 $2" in
  "info --format") echo '{"OperatingSystem":"linux","Architecture":"x86_64","ServerVersion":"29.4.0","NCPU":8,"MemTotal":1}' ;;
  "images --digests") ;;
esac
`)

	facts, err := NewDockerRuntime(daemon).Facts(context.Background())

	if err != nil {
		t.Fatalf("read node facts: %v", err)
	}
	if facts.Host.DiskTotalBytes != 200<<30 {
		t.Errorf("disk total = %d, want the 200GiB filesystem the daemon reported", facts.Host.DiskTotalBytes)
	}
	if facts.Host.DiskFreeBytes != 180<<30 {
		t.Errorf("disk free = %d, want the 180GiB available the daemon reported", facts.Host.DiskFreeBytes)
	}
}

// TestADaemonThatCannotSayHowMuchDiskItHasMakesNoReport is the loud half. There
// is no honest silence available here: HostFacts states disk as a number, a node
// advertising zero is refused every workload with a disk minimum, and a node
// advertising a guess sends work to a machine that may have nowhere to put it.
// This is deliberately unlike an image or a cache the daemon will not describe,
// which costs one entry: the disk is a fact about the whole machine.
func TestADaemonThatCannotSayHowMuchDiskItHasMakesNoReport(t *testing.T) {
	daemon := standInDaemon(t, `#!/bin/sh
case "$1 $2" in
  "info --format") echo '{"OperatingSystem":"linux","Architecture":"x86_64","ServerVersion":"29.4.0","NCPU":8,"MemTotal":1}' ;;
  "images --digests") ;;
esac
`)
	// The prepended probe answer is what every other stand-in uses; this daemon
	// refuses the probe instead.
	refusing := strings.Replace(readScript(t, daemon), `if [ "$1" = "run" ]; then`,
		`if [ "$1" = "run" ]; then echo 'no space left on device' >&2; exit 1;`, 1)
	writeScript(t, daemon, refusing)

	_, err := NewDockerRuntime(daemon).Facts(context.Background())

	if err == nil {
		t.Fatal("a daemon that could not be asked for its disk still produced a facts report")
	}
	if !strings.Contains(err.Error(), "measure this daemon's disk") {
		t.Fatalf("facts failed with %v, want the disk measurement named", err)
	}
}

// TestTheDiskANodeReportsIsTheDiskItsWorkloadsGet is the same claim against the
// daemon on this machine. A workload's writable layer, its image layers, and its
// volumes all land on the storage driver's filesystem, so what a probe container
// sees at / is what the next workload here can use.
func TestTheDiskANodeReportsIsTheDiskItsWorkloadsGet(t *testing.T) {
	requireDocker(t)
	pull(t, diskProbeImage)

	facts, err := NewDockerRuntime("").Facts(context.Background())

	if err != nil {
		t.Fatalf("read node facts: %v", err)
	}
	if facts.Host.DiskTotalBytes <= 0 || facts.Host.DiskFreeBytes <= 0 {
		t.Fatalf("this daemon reported %d total and %d free bytes of disk", facts.Host.DiskTotalBytes, facts.Host.DiskFreeBytes)
	}
	if facts.Host.DiskFreeBytes > facts.Host.DiskTotalBytes {
		t.Fatalf("free disk %d exceeds the whole filesystem %d", facts.Host.DiskFreeBytes, facts.Host.DiskTotalBytes)
	}
	total, free := containerRootFilesystem(t)
	if facts.Host.DiskTotalBytes != total {
		t.Errorf("reported %d bytes of disk, and a container of this daemon's sees %d", facts.Host.DiskTotalBytes, total)
	}
	// Free space moves between two probes on a working machine, so this asserts
	// the same filesystem rather than the same instant: a tenth of a percent of
	// a multi-terabyte disk is a lot of bytes and none of them are a different
	// mount.
	if drift := facts.Host.DiskFreeBytes - free; drift > total/1000 || drift < -total/1000 {
		t.Errorf("reported %d bytes free, and a container of this daemon's sees %d", facts.Host.DiskFreeBytes, free)
	}
}

// containerRootFilesystem reads the same measurement through the docker CLI
// directly, so the case checks the runtime against the daemon rather than
// against its own parser.
func containerRootFilesystem(t *testing.T) (total, free int64) {
	t.Helper()
	output, err := exec.Command("docker", "run", "--rm", "--network=none", diskProbeImage, "df", "-Pk", "/").CombinedOutput()
	if err != nil {
		t.Fatalf("df in a probe container: %v\n%s", err, output)
	}
	total, free, err = parseDiskFacts(string(output))
	if err != nil {
		t.Fatalf("parse df: %v", err)
	}
	return total, free
}

func readScript(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the stand-in daemon: %v", err)
	}
	return string(content)
}

func writeScript(t *testing.T, path, script string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write the stand-in daemon: %v", err)
	}
}
