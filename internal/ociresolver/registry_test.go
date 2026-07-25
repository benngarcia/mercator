package ociresolver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/benngarcia/mercator/internal/domain"
)

const (
	indexDigest    = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	amd64Digest    = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	arm64Digest    = "sha256:3333333333333333333333333333333333333333333333333333333333333333"
	configDigest   = "sha256:4444444444444444444444444444444444444444444444444444444444444444"
	baseBlobDigest = "sha256:5555555555555555555555555555555555555555555555555555555555555555"
	baseDiffID     = "sha256:6666666666666666666666666666666666666666666666666666666666666666"
	topBlobDigest  = "sha256:7777777777777777777777777777777777777777777777777777777777777777"
	topDiffID      = "sha256:8888888888888888888888888888888888888888888888888888888888888888"
)

var linuxAMD64 = domain.Platform{OS: "linux", Architecture: "amd64"}

// fakeRegistry is a registry v2 endpoint that behaves the way a real one does:
// it refuses an unauthenticated read with a token challenge, mints a token for
// the scope the challenge named, and serves manifests and blobs by digest.
type fakeRegistry struct {
	documents map[string]string
	// requiredAuth, when set, is the credential the token endpoint accepts.
	requiredAuth *BasicAuth
	// tokenRequests records the scopes the resolver asked tokens for.
	tokenRequests []string
	// authorizedReads counts reads that presented the minted bearer token.
	authorizedReads int
	// realm is the absolute token endpoint a challenge names, filled in once
	// the test server has an address.
	realm string
}

func (registry *fakeRegistry) handler(t *testing.T) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		registry.tokenRequests = append(registry.tokenRequests, r.URL.Query().Get("scope"))
		if registry.requiredAuth != nil {
			username, password, ok := r.BasicAuth()
			if !ok || username != registry.requiredAuth.Username || password != registry.requiredAuth.Password {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "minted-for-" + r.URL.Query().Get("scope")})
	})
	mux.HandleFunc("/v2/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			w.Header().Set("Www-Authenticate", `Bearer realm="`+registry.realm+`",service="fake",scope="repository:library/trainer:pull"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		registry.authorizedReads++
		document, ok := registry.documents[digestOf(r.URL.Path)]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", contentTypeOf(document))
		_, _ = w.Write([]byte(document))
	})
	return mux
}

func digestOf(path string) string {
	_, digest, _ := strings.Cut(path, "sha256:")
	return "sha256:" + digest
}

func contentTypeOf(document string) string {
	if strings.Contains(document, `"manifests"`) {
		return mediaTypeOCIIndex
	}
	if strings.Contains(document, `"rootfs"`) {
		return "application/json"
	}
	return mediaTypeOCIManifest
}

func indexDocument() string {
	return `{"mediaType":"` + mediaTypeOCIIndex + `","manifests":[
		{"digest":"` + arm64Digest + `","platform":{"os":"linux","architecture":"arm64"}},
		{"digest":"` + amd64Digest + `","platform":{"os":"linux","architecture":"amd64"}}
	]}`
}

func manifestDocument() string {
	return `{"mediaType":"` + mediaTypeOCIManifest + `","config":{"digest":"` + configDigest + `"},"layers":[
		{"digest":"` + baseBlobDigest + `","size":24000000000},
		{"digest":"` + topBlobDigest + `","size":300000000}
	]}`
}

func configDocument() string {
	return `{"rootfs":{"type":"layers","diff_ids":["` + baseDiffID + `","` + topDiffID + `"]}}`
}

func startRegistry(t *testing.T, registry *fakeRegistry) (*RegistryResolver, string) {
	t.Helper()
	server := httptest.NewServer(registry.handler(t))
	t.Cleanup(server.Close)
	registry.realm = server.URL + "/token"
	resolver := NewRegistryResolver(WithHTTPClient(server.Client()))
	return resolver, strings.TrimPrefix(server.URL, "http://")
}

func fullCatalog() map[string]string {
	return map[string]string{
		indexDigest:  indexDocument(),
		amd64Digest:  manifestDocument(),
		configDigest: configDocument(),
	}
}

func TestResolverStatesEveryLayerInBothDigestSpaces(t *testing.T) {
	registry := &fakeRegistry{documents: fullCatalog()}
	resolver, host := startRegistry(t, registry)

	manifest, err := resolver.ResolveManifest(context.Background(), host+"/library/trainer@"+indexDigest, linuxAMD64)

	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	// The layers are the amd64 build's, and the image's name is the digest the
	// reference pinned. That is the only name a host ever says back: a container
	// daemon records the digest it pulled by and has no word for the platform
	// manifest the registry selected underneath it.
	if !manifest.Known || manifest.Digest != indexDigest {
		t.Fatalf("manifest = %+v, want the amd64 layers named by the pinned digest %s", manifest, indexDigest)
	}
	want := []domain.ImageLayer{
		{Digest: baseBlobDigest, DiffID: baseDiffID, CompressedBytes: 24_000_000_000},
		{Digest: topBlobDigest, DiffID: topDiffID, CompressedBytes: 300_000_000},
	}
	if len(manifest.Layers) != len(want) {
		t.Fatalf("layers = %+v, want %+v", manifest.Layers, want)
	}
	for index, layer := range manifest.Layers {
		if layer != want[index] {
			t.Errorf("layer %d = %+v, want %+v", index, layer, want[index])
		}
	}
}

// A manifest that states both spaces is what makes a Docker host comparable to
// a registry at all, which is the whole reason this resolver exists.
func TestResolvedManifestRecognisesAHostThatOnlyKnowsDiffIDs(t *testing.T) {
	registry := &fakeRegistry{documents: fullCatalog()}
	resolver, host := startRegistry(t, registry)
	manifest, err := resolver.ResolveManifest(context.Background(), host+"/library/trainer@"+indexDigest, linuxAMD64)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	dockerHost := domain.ImageInventory{Known: true, LayerDiffIDs: []string{baseDiffID, topDiffID}}
	bytes, known := manifest.TransferBytes(dockerHost)

	if !known || bytes != 0 {
		t.Fatalf("a host holding every diff ID transfers %d bytes (known=%v), want 0", bytes, known)
	}
}

func TestResolverObtainsATokenForTheScopeTheRegistryAsked(t *testing.T) {
	registry := &fakeRegistry{documents: fullCatalog()}
	resolver, host := startRegistry(t, registry)

	if _, err := resolver.ResolveManifest(context.Background(), host+"/library/trainer@"+indexDigest, linuxAMD64); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	// One token for the three reads. A registry counts every mint against the
	// same limit it counts reads against, and the three reads are one scope on
	// one repository, so minting per read spends a rate limit on nothing.
	if len(registry.tokenRequests) != 1 {
		t.Fatalf("token requests = %v, want one token reused across the reads it authorizes", registry.tokenRequests)
	}
	if scope := registry.tokenRequests[0]; scope != "repository:library/trainer:pull" {
		t.Errorf("token scope = %q, want the scope the challenge named", scope)
	}
	if registry.authorizedReads != 3 {
		t.Errorf("authorized reads = %d, want the index, the child manifest, and the config", registry.authorizedReads)
	}
}

// TestResolverReadsOneImageOnce is what keeps placement off a registry's rate
// limit. A digest names one document forever, so the second placement of the
// same image has nothing to learn from asking again, and a registry that
// answers 429 to the thirty-fourth placement would silently take every
// candidate back to looking equally warm.
func TestResolverReadsOneImageOnce(t *testing.T) {
	registry := &fakeRegistry{documents: fullCatalog()}
	resolver, host := startRegistry(t, registry)
	reference := host + "/library/trainer@" + indexDigest

	first, err := resolver.ResolveManifest(context.Background(), reference, linuxAMD64)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	second, err := resolver.ResolveManifest(context.Background(), reference, linuxAMD64)
	if err != nil {
		t.Fatalf("resolve again: %v", err)
	}

	if registry.authorizedReads != 3 || len(registry.tokenRequests) != 1 {
		t.Errorf(
			"the second resolution cost %d further reads and %d further tokens, want none",
			registry.authorizedReads-3, len(registry.tokenRequests)-1,
		)
	}
	if second.Digest != first.Digest || len(second.Layers) != len(first.Layers) {
		t.Errorf("remembered manifest = %+v, want what the registry answered: %+v", second, first)
	}
}

// TestResolverRetriesAnImageItCouldNotRead is the other half of remembering: a
// refusal is a fact about a moment, not about the image, so a registry that
// recovers is asked again rather than being permanently believed.
func TestResolverRetriesAnImageItCouldNotRead(t *testing.T) {
	registry := &fakeRegistry{documents: map[string]string{}}
	resolver, host := startRegistry(t, registry)
	reference := host + "/library/trainer@" + indexDigest

	if _, err := resolver.ResolveManifest(context.Background(), reference, linuxAMD64); !errors.Is(err, ErrImageUnknown) {
		t.Fatalf("resolve error = %v, want %v", err, ErrImageUnknown)
	}
	registry.documents = fullCatalog()

	manifest, err := resolver.ResolveManifest(context.Background(), reference, linuxAMD64)

	if err != nil {
		t.Fatalf("resolve after the registry recovered: %v", err)
	}
	if !manifest.Known {
		t.Fatal("the resolver kept answering with the failure instead of reading the image the registry now serves")
	}
}

func TestResolverPresentsConfiguredCredentialsToAPrivateRegistry(t *testing.T) {
	registry := &fakeRegistry{documents: fullCatalog(), requiredAuth: &BasicAuth{Username: "mercator", Password: "s3cret"}}
	server := httptest.NewServer(registry.handler(t))
	t.Cleanup(server.Close)
	registry.realm = server.URL + "/token"
	host := strings.TrimPrefix(server.URL, "http://")
	resolver := NewRegistryResolver(
		WithHTTPClient(server.Client()),
		WithCredentials(func(string) BasicAuth { return BasicAuth{Username: "mercator", Password: "s3cret"} }),
	)

	manifest, err := resolver.ResolveManifest(context.Background(), host+"/library/trainer@"+indexDigest, linuxAMD64)

	if err != nil {
		t.Fatalf("resolve with credentials: %v", err)
	}
	if len(manifest.Layers) != 2 {
		t.Fatalf("manifest = %+v, want two layers", manifest)
	}
}

func TestRegistryRefusalsStayDistinguishable(t *testing.T) {
	catalog := fullCatalog()
	indexWithoutAMD64 := `{"mediaType":"` + mediaTypeOCIIndex + `","manifests":[
		{"digest":"` + arm64Digest + `","platform":{"os":"linux","architecture":"arm64"}}
	]}`

	testCases := []struct {
		name      string
		documents map[string]string
		auth      *BasicAuth
		reference string
		want      error
	}{
		{
			name:      "an image nobody pushed",
			documents: catalog,
			reference: "/library/trainer@sha256:9999999999999999999999999999999999999999999999999999999999999999",
			want:      ErrImageUnknown,
		},
		{
			name:      "an index with no build for this platform",
			documents: map[string]string{indexDigest: indexWithoutAMD64},
			reference: "/library/trainer@" + indexDigest,
			want:      ErrManifestUnresolvable,
		},
		{
			name:      "a registry that refuses the credentials offered",
			documents: catalog,
			auth:      &BasicAuth{Username: "someone", Password: "else"},
			reference: "/library/trainer@" + indexDigest,
			want:      ErrUnauthorized,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			registry := &fakeRegistry{documents: testCase.documents, requiredAuth: testCase.auth}
			resolver, host := startRegistry(t, registry)

			_, err := resolver.ResolveManifest(context.Background(), host+testCase.reference, linuxAMD64)

			if !errors.Is(err, testCase.want) {
				t.Fatalf("resolve error = %v, want %v", err, testCase.want)
			}
		})
	}
}

func TestResolverRefusesAReferenceThatIsNotDigestPinned(t *testing.T) {
	resolver := NewRegistryResolver()

	_, err := resolver.ResolveManifest(context.Background(), "docker.io/library/busybox:latest", linuxAMD64)

	if err == nil || !strings.Contains(err.Error(), "not digest-pinned") {
		t.Fatalf("resolve error = %v, want a refusal naming the missing pin", err)
	}
}

func TestReferencesResolveToTheHostThatServesThem(t *testing.T) {
	testCases := []struct {
		reference      string
		wantRegistry   string
		wantRepository string
		wantScheme     string
	}{
		{"busybox@" + indexDigest, "registry-1.docker.io", "library/busybox", "https"},
		{"docker.io/library/busybox@" + indexDigest, "registry-1.docker.io", "library/busybox", "https"},
		{"team/trainer@" + indexDigest, "registry-1.docker.io", "team/trainer", "https"},
		{"ghcr.io/team/trainer@" + indexDigest, "ghcr.io", "team/trainer", "https"},
		{"localhost:5000/trainer@" + indexDigest, "localhost:5000", "trainer", "http"},
		{"127.0.0.1:5000/team/trainer@" + indexDigest, "127.0.0.1:5000", "team/trainer", "http"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.reference, func(t *testing.T) {
			ref, err := parseDigestRef(testCase.reference)

			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if ref.Registry != testCase.wantRegistry || ref.Repository != testCase.wantRepository {
				t.Errorf("ref = %s/%s, want %s/%s", ref.Registry, ref.Repository, testCase.wantRegistry, testCase.wantRepository)
			}
			if ref.Scheme() != testCase.wantScheme {
				t.Errorf("scheme = %q, want %q", ref.Scheme(), testCase.wantScheme)
			}
		})
	}
}

func TestDockerConfigSuppliesTheCredentialsAnOperatorAlreadyHas(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.json")
	encoded := base64.StdEncoding.EncodeToString([]byte("mercator:s3cret"))
	config := `{"auths":{"https://index.docker.io/v1/":{"auth":"` + encoded + `"},"ghcr.io":{"username":"bot","password":"token"}}}`
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatalf("write docker config: %v", err)
	}

	credentials, err := DockerConfigCredentials(path)

	if err != nil {
		t.Fatalf("read docker config: %v", err)
	}
	if got := credentials("registry-1.docker.io"); got != (BasicAuth{Username: "mercator", Password: "s3cret"}) {
		t.Errorf("docker hub credential = %+v", got)
	}
	if got := credentials("ghcr.io"); got != (BasicAuth{Username: "bot", Password: "token"}) {
		t.Errorf("ghcr credential = %+v", got)
	}
	if got := credentials("registry.example.com"); got != (BasicAuth{}) {
		t.Errorf("an unconfigured registry is anonymous, got %+v", got)
	}
}

func TestMissingDockerConfigIsAnonymousRatherThanAnError(t *testing.T) {
	credentials, err := DockerConfigCredentials(filepath.Join(t.TempDir(), "absent.json"))

	if err != nil {
		t.Fatalf("a machine that never ran docker login is not an error: %v", err)
	}
	if got := credentials("registry-1.docker.io"); got != (BasicAuth{}) {
		t.Errorf("credential = %+v, want anonymous", got)
	}
}
