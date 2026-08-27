package nodeagent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/capability"
	"github.com/benngarcia/mercator/internal/credential"
	"github.com/benngarcia/mercator/internal/domain"
)

// This file is the higher-fidelity half of the private pull: a real registry
// that refuses an anonymous read, a real image inside it, and this machine's own
// daemon fetching it with material the production control plane minted for that
// one operation.
//
// The registry is distribution in a container of this machine's own daemon, with
// htpasswd auth in front of it. Everything that matters about the claim needs a
// real one. Whether X-Registry-Auth is the header the daemon wants, whether the
// material survives the round trip in the shape the daemon expects, and above all
// what a refusal looks like: the daemon accepts the request and then discovers it
// may not have the content, so a node reading the status alone reports every
// private image it cannot read as content it holds.
//
// To exercise it by hand, an operator runs the same steps this case does:
//
//	docker run --rm --detach --publish 127.0.0.1::5000 \
//	  --mount type=bind,src=$PWD/internal/nodeagent/testdata,dst=/auth,readonly \
//	  --env REGISTRY_AUTH=htpasswd --env REGISTRY_AUTH_HTPASSWD_REALM=Mercator \
//	  --env REGISTRY_AUTH_HTPASSWD_PATH=/auth/private-registry.htpasswd \
//	  --name mercator-registry registry:3
//	docker port mercator-registry 5000
//	# tag and push an image, then pull it back by digest with and without a credential
//	docker rm --force mercator-registry

const (
	registryImage = "registry:3"
	// baseImage is what the content this case pushes is built on. Anything small
	// does: what is under test is the material, not the bytes.
	baseImage = "busybox:1.37"
	// privateRepository is where it goes. A repository nothing else on this host
	// pushes to, so a pull that succeeds fetched what this case put there.
	privateRepository = "mercator/analyst"
	// registryAccount and registryAccountSecret are the account this Mercator
	// holds at that registry, matching testdata/private-registry.htpasswd. It is
	// the credential the control plane keeps and never hands over: what reaches
	// the node is minted from it and expires.
	registryAccount       = "mercator"
	registryAccountSecret = "one-pull-and-no-more"
)

// TestANodePullsAPrivateImageWithACredentialMintedForThatPull is the conformance
// case. The registry serves nothing to an anonymous reader, the control plane
// mints one pull of one digest, and the machine ends up holding that content.
func TestANodePullsAPrivateImageWithACredentialMintedForThatPull(t *testing.T) {
	runtime := NewDockerRuntime("docker")
	reference := privateImageServedFromThisHost(t, runtime)
	command := preparePrivateImage(t, reference)
	command.RegistryCredential = mintedPull(t, command.OperationID, reference)

	err := runtime.PrepareImage(context.Background(), command)

	if err != nil {
		t.Fatalf("pull a private image with material minted for it: %v", err)
	}
	if !thisDaemonHolds(reference) {
		t.Fatalf("the pull reported success and this daemon does not hold %s", reference)
	}
}

// TestTheSameReferenceIsRefusedWithNothingMintedForIt is the other half, and the
// half that stops the case above from passing against a registry that never
// refused anybody. Same daemon, same reference, no credential: the registry says
// no, and the node reports the refusal rather than the success the daemon's own
// status line would have let it report.
func TestTheSameReferenceIsRefusedWithNothingMintedForIt(t *testing.T) {
	runtime := NewDockerRuntime("docker")
	reference := privateImageServedFromThisHost(t, runtime)

	err := runtime.PrepareImage(context.Background(), preparePrivateImage(t, reference))

	if err == nil {
		t.Fatal("a private image was pulled with no credential and the node reported it holds the content")
	}
	if thisDaemonHolds(reference) {
		t.Fatalf("the pull was refused and this daemon holds %s anyway", reference)
	}
}

// TestMaterialMintedForAnotherOperationNeverReachesTheRegistry is the node's own
// half. The credential is this deployment's and the registry would accept it, and
// the machine refuses to present it because it was minted for a different
// operation: the scope is something both ends enforce rather than a claim in a
// comment, and the refusal happens before anything crosses the network.
func TestMaterialMintedForAnotherOperationNeverReachesTheRegistry(t *testing.T) {
	runtime := NewDockerRuntime("docker")
	reference := privateImageServedFromThisHost(t, runtime)
	command := preparePrivateImage(t, reference)
	command.RegistryCredential = mintedPull(t, "prepare:image:another-machine:"+domain.ReferenceDigest(reference), reference)

	err := runtime.PrepareImage(context.Background(), command)

	if err == nil {
		t.Fatal("a machine presented material minted for another operation and pulled with it")
	}
	if want := "was minted for operation"; !strings.Contains(err.Error(), want) {
		t.Fatalf("refusal = %q, want it to say %q", err, want)
	}
}

// preparePrivateImage is the command a node really receives for one pull: the
// digest-pinned reference and the operation identity the desired set gave it. The
// identity comes from adapter.PrepareItem rather than being spelled here, because
// the string the control plane mints for and the string the machine checks against
// have to be the same string.
func preparePrivateImage(t *testing.T, reference string) capability.PrepareImageCommand {
	t.Helper()
	item := adapter.PrepareItem{
		Kind:            adapter.PrepareImage,
		OfferSnapshotID: "nod_live",
		Image:           reference,
	}
	command := capability.PrepareImageCommand{
		ManifestDigest: domain.ReferenceDigest(reference),
		Reference:      reference,
		Unpack:         true,
	}
	command.OperationID = item.Operation()
	return command
}

// mintedPull is production's own minter, holding the registry account the way a
// deployment holds it. That is the point of running it here: the node is handed
// exactly what production hands it, so a header the daemon will not take or a
// scope the node refuses is caught against a real daemon and a real registry.
func mintedPull(t *testing.T, operation, reference string) domain.RegistryPull {
	t.Helper()
	mint, err := credential.NewMint(credential.MintConfig{
		Registries: []credential.RegistryAccount{{
			Registry: domain.ReferenceRegistry(reference),
			Username: registryAccount,
			Secret:   registryAccountSecret,
		}},
	})
	if err != nil {
		t.Fatalf("hold the registry account: %v", err)
	}
	pull, err := mint.RegistryPull(context.Background(), operation, reference)
	if err != nil {
		t.Fatalf("mint a pull of %s: %v", reference, err)
	}
	if pull.Zero() {
		t.Fatalf("nothing was minted for %s, and this registry serves nothing to an anonymous reader", reference)
	}
	return pull
}

// privateImageServedFromThisHost stands the registry up, puts one image behind
// it, and takes this daemon's copy away so that pulling it is a fetch rather than
// a lookup. It answers with the digest-pinned reference, because a digest is what
// a preparation command names: a tag would have the machine fetch whatever the
// label points at now.
func privateImageServedFromThisHost(t *testing.T, runtime *DockerRuntime) string {
	t.Helper()
	requireDocker(t)
	pull(t, registryImage)
	pull(t, baseImage)
	endpoint := startPrivateRegistry(t)
	repository := endpoint + "/" + privateRepository
	tag := fmt.Sprintf("live-%d", time.Now().UnixNano())
	buildPrivateImage(t, repository+":"+tag)
	digest := pushToPrivateRegistry(t, runtime, repository, tag)
	reference := repository + "@" + digest
	forget(repository+":"+tag, reference)
	t.Cleanup(func() { forget(repository+":"+tag, reference) })
	return reference
}

// buildPrivateImage makes this case its own image rather than retagging one that
// is already here. Tagging a shared image into a local registry adds a repository
// digest to the object every other case on this daemon reads, and mercator#212's
// lock does not help: the image survives the lock, and a case that inspects
// busybox's first repository digest gets this registry's instead, pointing at a
// port nothing listens on any more. That was observed, not imagined.
func buildPrivateImage(t *testing.T, reference string) {
	t.Helper()
	build := exec.Command("docker", "build", "--quiet", "--tag", reference, "-")
	build.Stdin = strings.NewReader("FROM " + baseImage + "\nRUN echo mercator > /mercator-private\n")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the image this case serves: %v\n%s", err, output)
	}
}

// startPrivateRegistry runs distribution behind htpasswd on a port this host
// chose. The account file is a fixture rather than something generated here: a
// bcrypt hash computed per run would make the one thing this case depends on, that
// the registry really refuses everybody else, a property of whatever library
// generated it.
func startPrivateRegistry(t *testing.T) string {
	t.Helper()
	auth, err := filepath.Abs("testdata")
	if err != nil {
		t.Fatalf("find the registry account file: %v", err)
	}
	name := fmt.Sprintf("mercator-registry-%d", time.Now().UnixNano())
	output, err := exec.Command("docker", "run", "--rm", "--detach",
		"--name", name,
		"--publish", "127.0.0.1::5000",
		"--mount", "type=bind,src="+auth+",dst=/auth,readonly",
		"--env", "REGISTRY_AUTH=htpasswd",
		"--env", "REGISTRY_AUTH_HTPASSWD_REALM=Mercator",
		"--env", "REGISTRY_AUTH_HTPASSWD_PATH=/auth/private-registry.htpasswd",
		registryImage).CombinedOutput()
	if err != nil {
		t.Fatalf("start a private registry on this machine: %v\n%s", err, output)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "--force", name).Run() })
	mapped, err := exec.Command("docker", "port", name, "5000/tcp").Output()
	if err != nil {
		t.Fatalf("read the registry's port: %v", err)
	}
	endpoint := strings.TrimSpace(strings.Split(string(mapped), "\n")[0])
	awaitPrivateRegistry(t, endpoint)
	return endpoint
}

// awaitPrivateRegistry waits until the registry answers, and asserts on the way
// that it answers an anonymous caller with a refusal. A registry that came up
// without its account file would serve everybody, and every case in this file
// would pass while proving nothing.
func awaitPrivateRegistry(t *testing.T, endpoint string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get("http://" + endpoint + "/v2/")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusUnauthorized {
				return
			}
			t.Fatalf("the registry at %s answers an anonymous read %s, and this case needs one that refuses", endpoint, response.Status)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("the registry at %s never came up", endpoint)
}

// pushToPrivateRegistry puts the content there and answers with the digest the
// registry filed it under. It goes through the daemon API for the reason
// PrepareImage does: the CLI reads registry material out of a config file, and
// this case would then be arranging its content with the very thing the slice
// exists to remove.
//
// The digest comes off the daemon's own progress stream rather than from
// inspecting the image afterwards. A local copy pulled from elsewhere carries the
// digest of whatever index it came from, and what a pull from this registry has to
// name is the manifest this push actually wrote.
func pushToPrivateRegistry(t *testing.T, runtime *DockerRuntime, repository, tag string) string {
	t.Helper()
	endpoint, err := runtime.endpoint(context.Background())
	if err != nil {
		t.Fatalf("find the daemon this case drives: %v", err)
	}
	client, base, err := daemonClient(endpoint)
	if err != nil {
		t.Fatalf("open the daemon: %v", err)
	}
	request, err := http.NewRequest(http.MethodPost,
		base+"/"+contentStoreAPIVersion+"/images/"+repository+"/push?tag="+tag, nil)
	if err != nil {
		t.Fatalf("build the push: %v", err)
	}
	request.Header.Set("X-Registry-Auth", accountHeader(repository))
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("push to this host's registry: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		t.Fatalf("the daemon answered %s for the push: %s", response.Status, strings.TrimSpace(string(body)))
	}
	return pushedDigest(t, response.Body, tag)
}

// pushedDigest reads the daemon's progress stream to its end and takes the digest
// out of the line that reports one. The stream is drained whatever happens,
// because the daemon performs the push while it writes it.
func pushedDigest(t *testing.T, body io.Reader, tag string) string {
	t.Helper()
	decoder := json.NewDecoder(body)
	digest := ""
	for {
		var message struct {
			Status string `json:"status"`
			Error  string `json:"error"`
		}
		if err := decoder.Decode(&message); err != nil {
			break
		}
		if message.Error != "" {
			t.Fatalf("the push failed: %s", message.Error)
		}
		if _, rest, found := strings.Cut(message.Status, tag+": digest: "); found {
			digest, _, _ = strings.Cut(rest, " ")
		}
	}
	if digest == "" {
		t.Fatal("the push reported no digest, so this case has no content to name")
	}
	return digest
}

// accountHeader is the account this Mercator holds, in the shape the daemon takes
// it. It is the arrangement rather than the thing under test: the node's own half
// is registryAuthHeader, which builds the same shape out of minted material.
func accountHeader(repository string) string {
	encoded, _ := json.Marshal(map[string]string{
		"username":      registryAccount,
		"password":      registryAccountSecret,
		"serveraddress": domain.ReferenceRegistry(repository),
	})
	return base64.URLEncoding.EncodeToString(encoded)
}

// forget removes this daemon's copies so a pull has somewhere to go.
func forget(references ...string) {
	for _, reference := range references {
		_ = exec.Command("docker", "rmi", "--force", reference).Run()
	}
}

func thisDaemonHolds(reference string) bool {
	return exec.Command("docker", "image", "inspect", reference).Run() == nil
}
