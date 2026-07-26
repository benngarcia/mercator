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
	"github.com/benngarcia/mercator/internal/scheduler"
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

// TestANodeMeasuresTheObjectStorePathItJustCrossed is the live half of the
// transfer model. Everything else about how long content takes to reach a
// machine is Mercator's own stated assumption, and this is the one place a real
// number enters the system: the node streams sixteen megabytes out of a real
// S3-compatible store over a real presigned GET, times the bytes it actually
// moved, and publishes what it found as a fact about that path.
//
// Then Placement prices the next read off the reported number. That is the whole
// point of measuring: a rate a machine published and nothing reads is a rate that
// changes no decision, which is what the field this fills has been since phase 2.
func TestANodeMeasuresTheObjectStorePathItJustCrossed(t *testing.T) {
	requireDocker(t)
	endpoint := startObjectStore(t)
	// Large enough to be a measurement of throughput rather than of the round trip
	// to the store, which is the distinction minimumMeasuredBytes draws.
	content := []byte(strings.Repeat("mercator throughput conformance 0123456789abcdef\n", 340_000))
	digest := sha256.Sum256(content)
	putObject(t, endpoint, "datasets", "corpus-v9", content)

	runtime := NewDockerRuntime("", WithArtifactRoot(t.TempDir()))
	if err := runtime.PrepareArtifact(context.Background(), capability.PrepareArtifactCommand{
		ArtifactID:    "artifact:corpus:v9",
		ContentDigest: "sha256:" + hex.EncodeToString(digest[:]),
		Source:        presign(t, http.MethodGet, endpoint, "datasets", "corpus-v9", time.Hour),
		SizeBytes:     int64(len(content)),
	}); err != nil {
		t.Fatalf("replicate the Artifact: %v", err)
	}

	facts, err := runtime.Facts(context.Background())
	if err != nil {
		t.Fatalf("read the node's facts: %v", err)
	}
	measured := objectStorePath(t, facts.Host.Network)
	if measured.ValueMbps <= 0 || measured.SampleCount != 1 {
		t.Fatalf("the node reports %+v, want the one transfer it just timed", measured)
	}
	if measured.Source != ArtifactCopySource || measured.Confidence != MeasuredLinkConfidence {
		t.Fatalf("the node reports %+v, want a reading it names as its own", measured)
	}
	if !measured.Answers(facts.ObservedAt) {
		t.Fatalf("the node published %+v, which Mercator may not act on at the moment it was reported", measured)
	}

	// The next Run that reads out of this store is priced off that number rather
	// than off the fleet-wide assumption. The offer is the node's own facts as the
	// registry projects them, which is where a measurement either reaches a
	// decision or stops being worth making.
	fetch := pricedArtifactRead(t, facts, 40_000_000_000)
	if fetch.Measurement != ArtifactCopySource || fetch.Mbps != measured.ValueMbps {
		t.Fatalf("Placement priced the next read at %+v, and this machine measured %.2f Mbps itself",
			fetch, measured.ValueMbps)
	}
	if fetch.Assumption != "" {
		t.Fatalf("Placement priced the next read from the assumption %q on a machine that measured the path", fetch.Assumption)
	}
}

func objectStorePath(t *testing.T, facts []domain.NetworkFact) domain.NetworkFact {
	t.Helper()
	for _, fact := range facts {
		if fact.Scope == domain.NetworkScopeObjectStore {
			return fact
		}
	}
	t.Fatalf("the node published %+v, and nothing there describes its path to the object store", facts)
	return domain.NetworkFact{}
}

// pricedArtifactRead runs the production scheduler over this node's own facts and
// answers what it charged the Artifact read at. It reads the rate the decision
// recorded rather than the seconds, because the seconds are bytes over a rate and
// only the rate says which of the two halves this case is about.
func pricedArtifactRead(t *testing.T, facts capability.NodeFacts, bytes int64) domain.TransferRate {
	t.Helper()
	candidate := placeAReadOfTheCorpus(t, facts, bytes, runAsking{})
	for _, rate := range candidate.TransferRates {
		if rate.Stage == domain.StageArtifactFetch {
			return rate
		}
	}
	t.Fatalf("the decision priced no Artifact read: %+v", candidate)
	return domain.TransferRate{}
}

// runAsking is what the Run in these cases refuses to do without: a floor on how
// fast this machine reaches the object store, and a bound on how long it may take
// to start. They are the two hard readers of one measurement, stated together
// because what a case turns on is which of them was allowed to act on the number
// this node published about itself.
type runAsking struct {
	download        *domain.NetworkDownloadRequirement
	maxStartSeconds float64
}

// placeAReadOfTheCorpus runs the production scheduler over this node's own facts
// and answers what it made of the machine. The Run may state a floor on how fast
// it reaches the object store, which is the other reader of the same measurement:
// one asks how long the read takes and the other asks whether this machine may
// serve the Run at all, and both are asked of the number this node published
// about itself.
func placeAReadOfTheCorpus(
	t *testing.T,
	facts capability.NodeFacts,
	bytes int64,
	asks runAsking,
) domain.CandidateDecision {
	t.Helper()
	now := facts.ObservedAt
	decision, err := scheduler.New().Evaluate(context.Background(), scheduler.SchedulingInput{
		RunID: "run-reader",
		Workload: domain.WorkloadRevision{
			WorkspaceID: "ws_alpha",
			Spec: domain.WorkloadSpec{
				Containers: []domain.ContainerSpec{{
					Name:     "reader",
					Image:    "trainer@sha256:" + strings.Repeat("cd", 32),
					Platform: domain.Platform{OS: "linux", Architecture: "amd64"},
				}},
				Placement: domain.PlacementPolicy{
					Class:                  domain.ClassStandard,
					ExpectedRuntimeSeconds: 600,
					MaxP90StartSeconds:     asks.maxStartSeconds,
				},
				Execution: domain.ExecutionPolicy{MaxRuntimeSeconds: 3600},
				Artifacts: domain.ArtifactRequirements{Consumes: []string{"artifact:corpus:v9"}},
				Network:   domain.NetworkRequirements{Download: asks.download},
			},
		},
		Offers: []domain.OfferSnapshot{{
			ID:           "nod_live",
			RentalID:     "rnt_live",
			ConnectionID: "conn_nodes",
			Kind:         domain.OfferKindStanding,
			Lane:         domain.LaneReusable,
			ObservedAt:   now,
			ExpiresAt:    now.Add(time.Minute),
			Platform:     domain.Platform{OS: "linux", Architecture: "amd64"},
			Resources:    domain.ResourceInventory{CPUMillis: 8000, MemoryBytes: 32 << 30, EphemeralDiskBytes: 1 << 40},
			Capabilities: domain.CapabilityProfile{
				Container: domain.ContainerCapabilities{MaxContainers: 4, SupportsDigestRefs: true},
			},
			// The one field this case is about, carried the way node.Registry
			// projects it onto an offer.
			Network:   domain.NetworkFacts{Download: facts.Host.Network},
			Pricing:   domain.PriceModel{Currency: "USD", RatePerSecondUSD: 0.0005, Known: true},
			Capacity:  domain.CapacityEvidence{Available: true, Confidence: 1},
			Artifacts: facts.Artifacts,
		}},
		Artifacts: []domain.ArtifactVersion{{
			ID:            "artifact:corpus:v9",
			ContentDigest: "sha256:" + strings.Repeat("ef", 32),
			SizeBytes:     bytes,
		}},
		ModelVersion: "latency-v1",
		EvaluatedAt:  now,
	})
	if err != nil {
		t.Fatalf("evaluate placement over this node's facts: %v", err)
	}
	return decision.Candidates[0]
}

// TestAFloorOnReadingTheDataIsAskedOfWhatThisNodeDelivers is the conformance half
// of a Run's hard floor on how fast it reaches its dataset. The corpus states that
// floor against declared paths; here it is asked of a number this machine really
// produced, reading real content out of a real object store onto a real disk.
//
// Two copies rather than one, because what a node publishes is a quantile over
// the transfers it still stands behind and never one of them. The date on it says
// when this machine last measured the path, so it cannot precede the second copy,
// and a Run that will act on nothing older than ten minutes is served by a machine
// that has just been reading.
//
// The floor is stated on either side of what this host delivered, which is the
// only way to state one against a rate nobody can predict: no host is refused for
// its link alone, so a machine whose Artifact disk is slower than its path is
// refused a floor above what it delivers and served one below it.
func TestAFloorOnReadingTheDataIsAskedOfWhatThisNodeDelivers(t *testing.T) {
	requireDocker(t)
	endpoint := startObjectStore(t)
	runtime := NewDockerRuntime("", WithArtifactRoot(t.TempDir()))
	replicate(t, runtime, endpoint, "corpus-v10", 8_000_000)
	secondCopyBegan := time.Now().UTC()
	replicate(t, runtime, endpoint, "corpus-v11", 160_000_000)

	facts, err := runtime.Facts(context.Background())
	if err != nil {
		t.Fatalf("read the node's facts: %v", err)
	}
	measured := objectStorePath(t, facts.Host.Network)

	if measured.SampleCount != 2 {
		t.Fatalf("the node published %+v after timing two copies, and a p10 is a quantile over the transfers it stands behind", measured)
	}
	if measured.ObservedAt.Before(secondCopyBegan) {
		t.Fatalf("the node dated its p10 %s, and it was still reading this path at %s",
			measured.ObservedAt.Format(time.RFC3339Nano), secondCopyBegan.Format(time.RFC3339Nano))
	}
	refused := placeAReadOfTheCorpus(t, facts, 40_000_000_000, runAsking{download: &domain.NetworkDownloadRequirement{
		Scope:                    domain.NetworkScopeObjectStore,
		MinP10Mbps:               measured.ValueMbps * 2,
		MaxMeasurementAgeSeconds: 600,
	}})
	if refused.Feasible {
		t.Fatalf("this node delivered %.2f Mbps and was admitted to a Run that states a floor of %.2f", measured.ValueMbps, measured.ValueMbps*2)
	}
	if !rejectedFor(refused, "NETWORK_FACT_UNSATISFIED", "network.download") {
		t.Fatalf("the decision refused this node as %+v, and what it published was measured too slow rather than absent", refused.Rejections)
	}
	served := placeAReadOfTheCorpus(t, facts, 40_000_000_000, runAsking{download: &domain.NetworkDownloadRequirement{
		Scope:                    domain.NetworkScopeObjectStore,
		MinP10Mbps:               measured.ValueMbps / 2,
		MaxMeasurementAgeSeconds: 600,
	}})
	if !served.Feasible {
		t.Fatalf("this node delivered %.2f Mbps a moment ago and was refused a Run asking for half of it: %+v", measured.ValueMbps, served.Rejections)
	}
}

// TestAStartBoundRefusesOnlyThePathThisNodeMeasured is the conformance half of
// what a hard start bound is allowed to act on. The corpus states it against
// declared paths; here one machine has really read content out of a real object
// store onto a real disk and published what it delivered, and the other is that
// same machine with nothing to say about the path.
//
// Both owe the same forty gigabytes and both are predicted to be late. Only one
// of them is known to be, and the Run's bound may refuse only that one. What
// prices the silent machine is Mercator's fleet-wide prior, a number nothing on
// that host answered for, so a refusal resting on it would refuse capacity for
// this model's own opinion and turn silence about a path into infeasibility. It
// stays a price: the prediction still carries every one of those seconds.
//
// The bound is stated from what this machine measured rather than as a constant,
// because no case can predict what a loopback object store delivers. Half of its
// own reading is a bound this host provably misses and the silent host is priced
// well past.
func TestAStartBoundRefusesOnlyThePathThisNodeMeasured(t *testing.T) {
	requireDocker(t)
	endpoint := startObjectStore(t)
	runtime := NewDockerRuntime("", WithArtifactRoot(t.TempDir()))
	replicate(t, runtime, endpoint, "corpus-v12", 160_000_000)

	facts, err := runtime.Facts(context.Background())
	if err != nil {
		t.Fatalf("read the node's facts: %v", err)
	}
	measured := objectStorePath(t, facts.Host.Network)
	const dataset = 40_000_000_000
	bound := float64(dataset*8) / 1_000_000 / measured.ValueMbps / 2

	refused := placeAReadOfTheCorpus(t, facts, dataset, runAsking{maxStartSeconds: bound})
	if refused.Feasible {
		t.Fatalf("this node measured %.2f Mbps itself, which is twice the %.2fs bound away from the data, and it was admitted",
			measured.ValueMbps, bound)
	}
	if !rejectedFor(refused, "LATENCY_SLO_EXCEEDED", "placement.max_p90_start_seconds") {
		t.Fatalf("the decision refused this node as %+v, and what it published was a path measured too slow", refused.Rejections)
	}
	if refused.Estimates.EstablishedStartSeconds.P90 <= bound {
		t.Fatalf("this node was refused against a %.2fs bound with %.2fs of its start established, so the refusal rests on something nobody measured",
			bound, refused.Estimates.EstablishedStartSeconds.P90)
	}

	silent := facts
	silent.Host.Network = nil
	admitted := placeAReadOfTheCorpus(t, silent, dataset, runAsking{maxStartSeconds: bound})
	if admitted.Estimates.StartSeconds.P90 <= bound {
		t.Fatalf("the unmeasured machine is predicted to start in %.2fs against a bound of %.2fs, so this case proves nothing",
			admitted.Estimates.StartSeconds.P90, bound)
	}
	if !admitted.Feasible {
		t.Fatalf("nothing measured this machine's path and a bound struck it out anyway: %+v", admitted.Rejections)
	}
	if admitted.Estimates.EstablishedStartSeconds.P90 > bound {
		t.Fatalf("the unmeasured machine established %.2fs of its start, and the only thing known about it is the byte count",
			admitted.Estimates.EstablishedStartSeconds.P90)
	}
}

// replicate is one Artifact put into the store and read back out by the node,
// which is one timed transfer over the path this case is about.
func replicate(t *testing.T, runtime *DockerRuntime, endpoint, key string, size int) {
	t.Helper()
	content := []byte(strings.Repeat("mercator delivery conformance 0123456789abcdef\n", size/47+1))
	digest := sha256.Sum256(content)
	putObject(t, endpoint, "datasets", key, content)
	if err := runtime.PrepareArtifact(context.Background(), capability.PrepareArtifactCommand{
		ArtifactID:    "artifact:" + key,
		ContentDigest: "sha256:" + hex.EncodeToString(digest[:]),
		Source:        presign(t, http.MethodGet, endpoint, "datasets", key, time.Hour),
		SizeBytes:     int64(len(content)),
	}); err != nil {
		t.Fatalf("replicate %s: %v", key, err)
	}
}

func rejectedFor(candidate domain.CandidateDecision, code, path string) bool {
	for _, rejection := range candidate.Rejections {
		if rejection.Code == code && rejection.Path == path {
			return true
		}
	}
	return false
}
