package nodeagent

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/capability"
	"github.com/benngarcia/mercator/internal/domain"
)

// This file is the higher-fidelity half of Artifact replication: the node
// reading real content out of a real S3-compatible object store, over a URL the
// control plane minted, and reporting afterwards what those bytes hashed to.
//
// The store is MinIO in a container of this machine's own daemon. Everything
// that matters about the claim needs a real one: the redirect and header
// behaviour of a presigned GET, the fact that the node holds no credential of
// any kind, and the digest the bytes on this disk actually produce.
//
// To exercise it by hand, an operator runs the same three steps this case does:
//
//	docker run --rm --detach --publish 127.0.0.1::9000 \
//	  --env MINIO_ROOT_USER=mercator --env MINIO_ROOT_PASSWORD=mercator-secret \
//	  --name mercator-objectstore minio/minio server /data
//	docker port mercator-objectstore 9000
//	# PUT the content with a presigned URL, then point a node at a presigned GET
//	docker rm --force mercator-objectstore

const (
	objectStoreImage  = "minio/minio:latest"
	objectStoreUser   = "mercator"
	objectStoreSecret = "mercator-secret"
	objectStoreRegion = "us-east-1"
)

// TestANodeReplicatesAnArtifactFromARealObjectStore is the conformance case.
// The store holds one version, the control plane mints a read of it, and the
// node ends up holding a copy it can prove is that content: the digest it
// reports is recomputed from the stream rather than repeated back from the
// command it was given.
func TestANodeReplicatesAnArtifactFromARealObjectStore(t *testing.T) {
	requireDocker(t)
	endpoint := startObjectStore(t)
	content := []byte(strings.Repeat("mercator artifact conformance\n", 4096))
	digest := sha256.Sum256(content)
	putObject(t, endpoint, "datasets", "corpus-v7", content)

	runtime := NewDockerRuntime("", WithArtifactRoot(t.TempDir()))
	command := capability.PrepareArtifactCommand{
		ArtifactID:    "artifact:corpus:v7",
		ContentDigest: "sha256:" + hex.EncodeToString(digest[:]),
		// The control plane mints the read. Nothing of the object store's
		// material reaches this node: the signature in the URL is scoped to one
		// object and expires.
		Source:    presign(t, http.MethodGet, endpoint, "datasets", "corpus-v7", time.Hour),
		SizeBytes: int64(len(content)),
	}
	command.WorkspaceID = "ws_alpha"

	if err := runtime.PrepareArtifact(context.Background(), command); err != nil {
		t.Fatalf("replicate the Artifact: %v", err)
	}

	inventory := runtime.artifacts()
	if !inventory.Known || len(inventory.Replicas) != 1 {
		t.Fatalf("the node reports %+v, want one copy it enumerated", inventory)
	}
	replica := inventory.Replicas[0]
	if replica.ArtifactID != command.ArtifactID || replica.ContentDigest != command.ContentDigest {
		t.Fatalf("the node holds %+v, want the version and digest it was asked for", replica)
	}
	if replica.State != domain.ArtifactReplicaVerified {
		t.Fatalf("the copy is %q, and its bytes hash to exactly what the catalog names", replica.State)
	}
	if replica.SizeBytes != int64(len(content)) {
		t.Fatalf("the copy is %d bytes, want %d", replica.SizeBytes, len(content))
	}
	if strings.Contains(command.Source, objectStoreSecret) {
		t.Fatal("the minted URL carries the object store's secret, which is the one thing a node must never hold")
	}
}

// TestACopyThatIsNotTheContentItWasAskedForIsNotWarmth is the other half of what
// verification is for. The store serves different bytes under the name the
// catalog gave, which is the machine an operator restored an older snapshot
// onto, and the node files what it actually received rather than what it was
// promised: a copy reported verified here is a Run reading the wrong data fast.
func TestACopyThatIsNotTheContentItWasAskedForIsNotWarmth(t *testing.T) {
	requireDocker(t)
	endpoint := startObjectStore(t)
	putObject(t, endpoint, "datasets", "corpus-v8", []byte("the previous version of this content"))

	runtime := NewDockerRuntime("", WithArtifactRoot(t.TempDir()))
	command := capability.PrepareArtifactCommand{
		ArtifactID:    "artifact:corpus:v8",
		ContentDigest: "sha256:" + strings.Repeat("ab", 32),
		Source:        presign(t, http.MethodGet, endpoint, "datasets", "corpus-v8", time.Hour),
	}
	command.WorkspaceID = "ws_alpha"

	if err := runtime.PrepareArtifact(context.Background(), command); err != nil {
		t.Fatalf("replicate the Artifact: %v", err)
	}

	replicas := runtime.artifacts().Replicas
	if len(replicas) != 1 {
		t.Fatalf("the node reports %+v, want the copy it fetched", replicas)
	}
	if replicas[0].State != domain.ArtifactReplicaUnverified {
		t.Fatalf("a copy whose bytes are another version's is reported %q", replicas[0].State)
	}
	if replicas[0].ContentDigest == command.ContentDigest {
		t.Fatal("the node repeated back the digest it was asked for instead of the one its bytes produced")
	}
}

// TestANodeReportsNoCopyOfWhatItsOwnWorkloadWrote is the premise producer
// affinity exists for, checked against a real daemon rather than assumed. A
// workload publishes an Artifact itself, which is what the authority model says
// it does, so the bytes are written inside its own container and Mercator is told
// nothing about where. This node then reports no copy of that version: the
// content is on this machine, the machine holds every byte of it, and its
// enumeration answers about the replica store alone.
//
// That silence is why the producing host has to be recorded on the catalog entry
// at publication. Nothing else in the system can say that this machine is the one
// most likely to still be holding a 40GB dataset, and a consumer that could not
// be told would price this machine exactly like a host that has never seen the
// content.
//
// The same content then arrives through PrepareArtifact, from the same store the
// workload uploaded it to, and now the node does report it. The two halves are
// the difference between content on a disk and content Mercator can be asked
// about, which is the difference the affinity record fills in.
func TestANodeReportsNoCopyOfWhatItsOwnWorkloadWrote(t *testing.T) {
	requireDocker(t)
	pull(t, "busybox:latest")
	endpoint := startObjectStore(t)
	content := []byte(strings.Repeat("mercator producer affinity\n", 4096))
	digest := "sha256:" + hex.EncodeToString(sliceDigest(content))
	putObject(t, endpoint, "datasets", "checkpoint-v1", content)

	runtime := NewDockerRuntime("", WithArtifactRoot(t.TempDir()))
	writeArtifactInsideAContainer(t, runtime, "run-producer", content)

	produced := runtime.artifacts()
	if !produced.Known {
		t.Fatalf("the node cannot enumerate its replica store at all, so this case cannot say what its silence means: %+v", produced)
	}
	if len(produced.Replicas) != 0 {
		t.Fatalf("the node reports %+v of content its workload wrote into its own container", produced.Replicas)
	}

	command := capability.PrepareArtifactCommand{
		ArtifactID:    "artifact:checkpoint:v1",
		ContentDigest: digest,
		Source:        presign(t, http.MethodGet, endpoint, "datasets", "checkpoint-v1", time.Hour),
		SizeBytes:     int64(len(content)),
	}
	command.WorkspaceID = "ws_alpha"
	if err := runtime.PrepareArtifact(context.Background(), command); err != nil {
		t.Fatalf("replicate the Artifact this workload produced: %v", err)
	}

	replicated := runtime.artifacts()
	if len(replicated.Replicas) != 1 || replicated.Replicas[0].State != domain.ArtifactReplicaVerified {
		t.Fatalf("the node reports %+v after fetching the content itself", replicated.Replicas)
	}
	if replicated.Replicas[0].ContentDigest != digest {
		t.Fatalf("the copy hashes to %q and the workload's own bytes hash to %q",
			replicated.Replicas[0].ContentDigest, digest)
	}
}

// writeArtifactInsideAContainer runs one real workload that publishes its output
// the way a workload does: into its own filesystem, and then to the object store.
// Nothing tells this node where those bytes went, which is the whole point.
func writeArtifactInsideAContainer(t *testing.T, runtime *DockerRuntime, runID string, content []byte) {
	t.Helper()
	container := "mercator-" + runID + "-1"
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "--force", container).Run() })
	command := capability.LaunchWorkloadCommand{
		RunID:     runID,
		AttemptID: "1",
		BookingID: "bkg-" + runID,
		Workload: domain.WorkloadSpec{Containers: []domain.ContainerSpec{{
			Name:  "main",
			Image: "busybox:latest",
			Args:  []string{"sh", "-c", fmt.Sprintf("printf '%%s' '%s' > /checkpoint && sleep 30", string(content[:32]))},
		}}},
	}
	command.WorkspaceID = "ws_alpha"
	if err := runtime.LaunchWorkload(context.Background(), command); err != nil {
		t.Fatalf("launch the producer: %v", err)
	}
}

func sliceDigest(content []byte) []byte {
	sum := sha256.Sum256(content)
	return sum[:]
}

// startObjectStore runs MinIO on this machine's own daemon and answers where it
// listens. It is a container rather than an in-process stand-in because the
// claim is about a real S3-compatible endpoint.
func startObjectStore(t *testing.T) string {
	t.Helper()
	pull(t, objectStoreImage)
	name := fmt.Sprintf("mercator-objectstore-%d", time.Now().UnixNano())
	output, err := exec.Command("docker", "run", "--rm", "--detach",
		"--name", name,
		"--publish", "127.0.0.1::9000",
		"--env", "MINIO_ROOT_USER="+objectStoreUser,
		"--env", "MINIO_ROOT_PASSWORD="+objectStoreSecret,
		objectStoreImage, "server", "/data").CombinedOutput()
	if err != nil {
		t.Skipf("cannot start an object store on this machine: %v\n%s", err, output)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "--force", name).Run() })
	mapped, err := exec.Command("docker", "port", name, "9000/tcp").Output()
	if err != nil {
		t.Fatalf("read the object store's port: %v", err)
	}
	endpoint := "http://" + strings.TrimSpace(strings.Split(string(mapped), "\n")[0])
	awaitObjectStore(t, endpoint)
	return endpoint
}

func awaitObjectStore(t *testing.T, endpoint string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get(endpoint + "/minio/health/live")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("the object store at %s never became ready", endpoint)
}

// putObject creates the bucket and writes one object, both over presigned URLs,
// so the test holds credentials exactly where the control plane would and the
// node holds none.
func putObject(t *testing.T, endpoint, bucket, key string, content []byte) {
	t.Helper()
	send(t, http.MethodPut, presign(t, http.MethodPut, endpoint, bucket, "", time.Minute), nil, http.StatusOK, http.StatusConflict)
	send(t, http.MethodPut, presign(t, http.MethodPut, endpoint, bucket, key, time.Minute), content, http.StatusOK)
}

func send(t *testing.T, method, target string, body []byte, accept ...int) {
	t.Helper()
	request, err := http.NewRequest(method, target, strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("build %s %s: %v", method, target, err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("call the object store: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	raw, _ := io.ReadAll(response.Body)
	for _, status := range accept {
		if response.StatusCode == status {
			return
		}
	}
	t.Fatalf("%s %s = %s: %s", method, target, response.Status, raw)
}

// presign mints one scoped, expiring read or write, which is what the control
// plane hands a node instead of a credential. It is SigV4 over the standard
// library: an object store client is its own slice, and this case needs exactly
// the one URL.
func presign(t *testing.T, method, endpoint, bucket, key string, expires time.Duration) string {
	t.Helper()
	parsed, err := url.Parse(endpoint)
	if err != nil {
		t.Fatalf("parse the object store endpoint: %v", err)
	}
	now := time.Now().UTC()
	stamp := now.Format("20060102T150405Z")
	day := now.Format("20060102")
	scope := day + "/" + objectStoreRegion + "/s3/aws4_request"
	path := "/" + bucket
	if key != "" {
		path += "/" + key
	}
	query := url.Values{
		"X-Amz-Algorithm":     {"AWS4-HMAC-SHA256"},
		"X-Amz-Credential":    {objectStoreUser + "/" + scope},
		"X-Amz-Date":          {stamp},
		"X-Amz-Expires":       {fmt.Sprintf("%d", int(expires.Seconds()))},
		"X-Amz-SignedHeaders": {"host"},
	}
	canonical := strings.Join([]string{
		method,
		path,
		strings.ReplaceAll(query.Encode(), "+", "%20"),
		"host:" + parsed.Host + "\n",
		"host",
		"UNSIGNED-PAYLOAD",
	}, "\n")
	hashed := sha256.Sum256([]byte(canonical))
	toSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		stamp,
		scope,
		hex.EncodeToString(hashed[:]),
	}, "\n")
	signing := sign(sign(sign(sign([]byte("AWS4"+objectStoreSecret), day), objectStoreRegion), "s3"), "aws4_request")
	query.Set("X-Amz-Signature", hex.EncodeToString(sign(signing, toSign)))
	return endpoint + path + "?" + strings.ReplaceAll(query.Encode(), "+", "%20")
}

func sign(key []byte, message string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(message))
	return mac.Sum(nil)
}
