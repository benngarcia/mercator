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
	requireDockerHubReachable(t)
	requireDocker(t)
	holdImage(t, "busybox:latest")
	pinned := docker(t, "image", "inspect", "busybox:latest", "--format", "{{index .RepoDigests 0}}")
	platform := dockerPlatform(t, "busybox:latest")

	manifest, err := NewRegistryResolver().ResolveManifest(context.Background(), pinned, platform)

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
	if err := json.Unmarshal([]byte(docker(t, "manifest", "inspect", pinned)), &index); err != nil {
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
	// The registry this case reads from runs here, so Docker Hub is needed only to
	// obtain the content once. A machine that already holds busybox needs nothing
	// off this host at all.
	holdImage(t, "busybox:latest")
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
	if err := json.Unmarshal([]byte(docker(t, "manifest", "inspect", reference)), &reported); err != nil {
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

// requireDockerHubReachable proves the network is there before a test that
// needs it, so an offline machine skips rather than reporting a failure it
// cannot have caused.
// requireDockerHubReachable skips the case unless Docker Hub will serve this
// machine a manifest. A 200 from /v2/ was never that question: an anonymous
// client over the registry's rate limit is issued a token and then answered 429
// for every manifest it asks for, which surfaces as this case reporting that
// Mercator and Docker disagree about a digest when the truth is that neither of
// them was served. A registry refusing an environment proves nothing either way,
// so the case says so and skips.
func requireDockerHubReachable(t *testing.T) {
	t.Helper()
	client := &http.Client{Timeout: 10 * time.Second}
	request, err := http.NewRequest(http.MethodHead, "https://registry-1.docker.io/v2/library/busybox/manifests/latest", nil)
	if err != nil {
		t.Fatalf("build the reachability request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+dockerHubPullToken(t, client, "library/busybox"))
	request.Header.Set("Accept", "application/vnd.oci.image.index.v1+json")
	response, err := client.Do(request)
	if err != nil {
		t.Skipf("docker.io is unreachable from this machine: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Skipf("docker.io answers %s to an anonymous manifest read, so it will not serve this machine", response.Status)
	}
}

// dockerHubPullToken is the anonymous pull token the daemon would fetch for the
// same read. The rate limit is enforced on the manifest and not on the token, so
// the token succeeding is not evidence the registry will serve anything.
func dockerHubPullToken(t *testing.T, client *http.Client, repository string) string {
	t.Helper()
	response, err := client.Get("https://auth.docker.io/token?service=registry.docker.io&scope=repository:" + repository + ":pull")
	if err != nil {
		t.Skipf("docker.io is unreachable from this machine: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	var issued struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&issued); err != nil || issued.Token == "" {
		t.Skipf("docker.io issued no anonymous pull token for %s: %v", repository, err)
	}
	return issued.Token
}

// holdImage makes sure this machine holds the image a case reads. A machine that
// already holds it needs no registry: the content is identified by digest, so a
// copy on this disk is the same content the registry would serve. A registry that
// refuses to serve an image this machine does not hold skips the case rather than
// failing it, for the same reason the guard above does.
func holdImage(t *testing.T, reference string) {
	t.Helper()
	if exec.Command("docker", "image", "inspect", reference).Run() == nil {
		return
	}
	output, err := exec.Command("docker", "pull", "--quiet", reference).CombinedOutput()
	if err == nil {
		return
	}
	t.Skipf("this machine holds no %s and the registry will not serve one: %s", reference, strings.TrimSpace(string(output)))
}
