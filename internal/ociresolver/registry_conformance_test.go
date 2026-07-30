package ociresolver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
)

// This file is the higher-fidelity half of manifest resolution: the resolver
// read against registries that are real rather than simulated, with Docker
// holding the answer it is checked against. Both cases skip when the machine
// cannot reach a registry or has no Docker daemon, because an environment
// without them proves nothing either way.

const (
	conformanceUser     = "mercator"
	conformancePassword = "conformance-only"
)

// TestRegistryResolverAgreesWithDockerAboutAPublicImage checks the resolver
// against a registry that is real, on the one question production actually
// asks: does the name the resolver gives an image match the name the machine
// running it gives the same image? busybox is multi-platform, so its reference
// pins an index and the layers below it belong to one child manifest. Naming
// the image after that child would produce a digest no Docker daemon has ever
// heard of, and the whole-image fast path would silently never fire.
func TestRegistryResolverAgreesWithDockerAboutAPublicImage(t *testing.T) {
	requireDocker(t)
	requireImage(t, "busybox:latest")
	pinned := docker(t, "image", "inspect", "busybox:latest", "--format", "{{index .RepoDigests 0}}")
	platform := dockerPlatform(t, "busybox:latest")

	manifest, err := NewRegistryResolver().ResolveManifest(context.Background(), pinned, platform)

	if errors.Is(err, ErrThrottled) {
		t.Skipf("docker.io is rate limiting this address: %v", err)
	}
	if err != nil {
		t.Fatalf("resolve %s: %v", pinned, err)
	}
	_, reported, _ := strings.Cut(pinned, "@")
	if manifest.Digest != reported {
		t.Fatalf(
			"resolved digest = %s, the daemon holds this image under %s: no host could ever be recognised as holding it whole",
			manifest.Digest, reported,
		)
	}
	assertMatchesDockerManifest(t, manifest, platformManifestReference(t, pinned, platform))
	assertMatchesDockerDiffIDs(t, manifest, "busybox:latest")
}

// platformManifestReference is the child manifest under an index, which is
// where `docker manifest inspect` keeps the layers. Reading it through the
// index rather than through the resolver's own answer is what makes this a
// check and not an echo.
func platformManifestReference(t *testing.T, pinned string, platform domain.Platform) string {
	t.Helper()
	var index struct {
		Manifests []struct {
			Digest   string `json:"digest"`
			Platform struct {
				OS           string `json:"os"`
				Architecture string `json:"architecture"`
			} `json:"platform"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal([]byte(dockerHubRead(t, "manifest", "inspect", pinned)), &index); err != nil {
		t.Fatalf("decode docker manifest inspect %s: %v", pinned, err)
	}
	repository, _, _ := strings.Cut(pinned, "@")
	for _, entry := range index.Manifests {
		if entry.Platform.OS == platform.OS && entry.Platform.Architecture == platform.Architecture {
			return repository + "@" + entry.Digest
		}
	}
	// A single-platform image is already the manifest it was pinned to.
	return pinned
}

// TestRegistryResolverAuthenticatesAgainstAPrivateRegistry proves the
// authenticated read against a registry that really demands credentials, run on
// this machine so no credential this environment lacks is needed. The
// unauthenticated attempt is the control: without it, the credentials could be
// doing nothing.
func TestRegistryResolverAuthenticatesAgainstAPrivateRegistry(t *testing.T) {
	requireDocker(t)
	requireImage(t, "busybox:latest")
	host := startPrivateRegistry(t)
	reference := host + "/private/trainer:v1"
	docker(t, "tag", "busybox:latest", reference)
	t.Cleanup(func() { _ = exec.Command("docker", "image", "rm", "-f", reference).Run() })
	config := t.TempDir()
	docker(t, "--config", config, "login", "--username", conformanceUser, "--password", conformancePassword, host)
	docker(t, "--config", config, "push", "--quiet", reference)
	pinned := privateRepoDigest(t, reference, host)
	platform := dockerPlatform(t, "busybox:latest")

	authenticated := NewRegistryResolver(WithCredentials(func(string) BasicAuth {
		return BasicAuth{Username: conformanceUser, Password: conformancePassword}
	}))
	manifest, err := authenticated.ResolveManifest(context.Background(), pinned, platform)

	if err != nil {
		t.Fatalf("resolve %s with credentials: %v", pinned, err)
	}
	assertMatchesDockerDiffIDs(t, manifest, "busybox:latest")
	if len(manifest.Layers) == 0 || manifest.Layers[0].CompressedBytes <= 0 {
		t.Fatalf("manifest = %+v, want layers with compressed sizes", manifest.Layers)
	}

	_, err = NewRegistryResolver().ResolveManifest(context.Background(), pinned, platform)

	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("anonymous resolve error = %v, want %v: the credentials above proved nothing otherwise", err, ErrUnauthorized)
	}
}

// startPrivateRegistry runs registry:2 behind the committed htpasswd fixture and
// returns the loopback host it answers on.
func startPrivateRegistry(t *testing.T) string {
	t.Helper()
	requireImage(t, "registry:2")
	container := docker(t,
		"run", "--detach",
		"--publish", "127.0.0.1::5000",
		"--volume", mustAbs(t, "testdata")+":/auth:ro",
		"--env", "REGISTRY_AUTH=htpasswd",
		"--env", "REGISTRY_AUTH_HTPASSWD_REALM=Mercator Conformance",
		"--env", "REGISTRY_AUTH_HTPASSWD_PATH=/auth/htpasswd",
		"registry:2",
	)
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "--force", container).Run() })
	host := strings.TrimSpace(docker(t, "port", container, "5000/tcp"))
	if line, _, found := strings.Cut(host, "\n"); found {
		host = line
	}
	awaitRegistry(t, host)
	return host
}

func awaitRegistry(t *testing.T, host string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get("http://" + host + "/v2/")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusUnauthorized {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("the private registry on %s never started demanding credentials", host)
}

// privateRepoDigest is the digest the push produced, read back from the local
// image's repository digests for that host.
// privateRepoDigest asks the registry which digest it stored, because that is
// the only digest it can be asked to serve. The daemon's RepoDigests reports the
// digest of the multi-platform index the local image was tagged from, and a push
// sends only the one platform the daemon holds, which the registry stores as a
// single-platform OCI manifest under a different digest. Docker says so on the
// push itself: "only the available single-platform image was pushed", followed
// by the index digest, an arrow, and the digest it actually wrote. Trusting
// RepoDigests asks the registry for content it never received.
func privateRepoDigest(t *testing.T, reference, host string) string {
	t.Helper()
	repository, tag, ok := strings.Cut(strings.TrimPrefix(reference, host+"/"), ":")
	if !ok {
		t.Fatalf("the conformance reference %q names no tag", reference)
	}
	request, err := http.NewRequest(http.MethodGet, "http://"+host+"/v2/"+repository+"/manifests/"+tag, nil)
	if err != nil {
		t.Fatalf("build the manifest request for %s: %v", reference, err)
	}
	request.SetBasicAuth(conformanceUser, conformancePassword)
	request.Header.Set("Accept", strings.Join([]string{
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.docker.distribution.manifest.v2+json",
	}, ", "))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("read the stored manifest for %s: %v", reference, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("the registry answered %s for %s, so the push left nothing to resolve", response.Status, reference)
	}
	digest := response.Header.Get("Docker-Content-Digest")
	if digest == "" {
		t.Fatalf("the registry served %s without naming the digest it stored", reference)
	}
	return host + "/" + repository + "@" + digest
}

func assertMatchesDockerManifest(t *testing.T, manifest domain.ImageManifest, reference string) {
	t.Helper()
	var reported struct {
		Layers []struct {
			Digest string `json:"digest"`
			Size   int64  `json:"size"`
		} `json:"layers"`
	}
	if err := json.Unmarshal([]byte(dockerHubRead(t, "manifest", "inspect", reference)), &reported); err != nil {
		t.Fatalf("decode docker manifest inspect %s: %v", reference, err)
	}
	if len(reported.Layers) != len(manifest.Layers) {
		t.Fatalf("resolved %d layers, docker reports %d", len(manifest.Layers), len(reported.Layers))
	}
	for index, layer := range reported.Layers {
		if manifest.Layers[index].Digest != layer.Digest {
			t.Errorf("layer %d blob digest = %s, docker reports %s", index, manifest.Layers[index].Digest, layer.Digest)
		}
		if manifest.Layers[index].CompressedBytes != layer.Size {
			t.Errorf("layer %d compressed size = %d, docker reports %d", index, manifest.Layers[index].CompressedBytes, layer.Size)
		}
	}
}

func assertMatchesDockerDiffIDs(t *testing.T, manifest domain.ImageManifest, reference string) {
	t.Helper()
	var reported []string
	if err := json.Unmarshal([]byte(docker(t, "image", "inspect", reference, "--format", "{{json .RootFS.Layers}}")), &reported); err != nil {
		t.Fatalf("decode docker image inspect %s: %v", reference, err)
	}
	if len(reported) != len(manifest.Layers) {
		t.Fatalf("resolved %d layers, the daemon holds %d", len(manifest.Layers), len(reported))
	}
	for index, diffID := range reported {
		if manifest.Layers[index].DiffID != diffID {
			t.Errorf("layer %d diff ID = %s, the daemon reports %s", index, manifest.Layers[index].DiffID, diffID)
		}
	}
}

func dockerPlatform(t *testing.T, reference string) domain.Platform {
	t.Helper()
	value := docker(t, "image", "inspect", reference, "--format", "{{.Os}}/{{.Architecture}}")
	platform, ok := domain.ParsePlatform(value)
	if !ok {
		t.Fatalf("the daemon reported an unusable platform %q for %s", value, reference)
	}
	return platform
}

// docker returns the command's stdout alone. Merging stderr in would corrupt
// every caller that parses this: `docker run --detach` writes pull progress to
// stderr on a cache miss, so a merged read returns the whole pull transcript
// with the container ID buried at the end, and only a host that already holds
// the image passes. The production CLI client separates the two streams for the
// same reason, in runSplit. Failures still report stderr, which is where docker
// puts the explanation.
func docker(t *testing.T, args ...string) string {
	t.Helper()
	var stdout, stderr strings.Builder
	command := exec.Command("docker", args...)
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return strings.TrimSpace(stdout.String())
}

func mustAbs(t *testing.T, path string) string {
	t.Helper()
	absolute, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("resolve %s: %v", path, err)
	}
	return absolute
}

func requireDocker(t *testing.T) {
	t.Helper()
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skipf("no reachable Docker daemon to check the resolver against: %v", err)
	}
}

// requireImage puts the content a case needs on this machine without insisting
// on a fresh copy of it. The private-registry case pushes these bytes into a
// registry it starts itself and checks the resolver against what that registry
// stored, so a copy this daemon already holds is the same evidence as one just
// fetched, and an address Docker Hub is throttling still runs the case.
func requireImage(t *testing.T, reference string) {
	t.Helper()
	if err := exec.Command("docker", "pull", "--quiet", reference).Run(); err == nil {
		return
	}
	if err := exec.Command("docker", "image", "inspect", reference).Run(); err != nil {
		t.Skipf("this machine can neither pull %s nor already hold it: %v", reference, err)
	}
}

// dockerHubRead is a docker command that reads a public registry, which answers
// an address that has spent its anonymous quota with 429. Everything the
// public-image case compares comes from two readers of the same registry, this
// resolver and this daemon, and they cross that quota four times between them, so
// a gate at the top can prove the first read is allowed and can promise nothing
// about the fourth. The throttle is answered where it appears instead. A skip is
// the honest answer to it for the same reason it is the honest answer to being
// offline: a registry that will not serve the manifest says nothing about whether
// the resolver reads it correctly.
func dockerHubRead(t *testing.T, args ...string) string {
	t.Helper()
	var stdout, stderr strings.Builder
	command := exec.Command("docker", args...)
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if strings.Contains(stderr.String(), "toomanyrequests") {
			t.Skipf("docker.io is rate limiting this address: docker %s\n%s", strings.Join(args, " "), stderr.String())
		}
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return strings.TrimSpace(stdout.String())
}
