package ociresolver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
)

// A registry answers three different questions with three different failures,
// and collapsing them loses the only information an operator can act on: an
// image nobody pushed, a manifest that exists but names no build for this
// platform, and a registry that will not talk to the credentials Mercator has.
// Placement prices all three as uncertainty, because a host may still hold the
// image; what changes is what a human is told to fix.
var (
	ErrImageUnknown         = errors.New("ociresolver: the registry does not have this image")
	ErrManifestUnresolvable = errors.New("ociresolver: the registry has this image but no manifest for this platform")
	ErrUnauthorized         = errors.New("ociresolver: the registry refused the credentials offered")
)

const (
	mediaTypeDockerManifest     = "application/vnd.docker.distribution.manifest.v2+json"
	mediaTypeDockerManifestList = "application/vnd.docker.distribution.manifest.list.v2+json"
	mediaTypeOCIManifest        = "application/vnd.oci.image.manifest.v1+json"
	mediaTypeOCIIndex           = "application/vnd.oci.image.index.v1+json"
)

// BasicAuth is one registry's credentials. Anonymous access carries neither.
type BasicAuth struct {
	Username string
	Password string
}

// CredentialFunc answers what Mercator may present to one registry host.
// Returning the zero value means anonymous, which is the correct answer for a
// public registry and the honest one for a private registry nobody configured.
type CredentialFunc func(registryHost string) BasicAuth

// RegistryResolver reads an image's exact content from the registry that serves
// it. It is the only source in the tree that can state a layer in both digest
// spaces at once: the manifest lists compressed blob digests and their sizes,
// and the config blob lists the uncompressed diff IDs a container daemon
// enumerates. Without both, a host that says what it holds and a manifest that
// says what the image needs are describing the same bytes in vocabularies that
// never meet.
//
// Registry v2 is token auth over HTTP plus JSON, so it is net/http and
// encoding/json. Nothing here needs a client library.
type RegistryResolver struct {
	client      *http.Client
	credentials CredentialFunc
}

type RegistryOption func(*RegistryResolver)

// WithHTTPClient replaces the transport, which is how a test points the
// resolver at a local registry and how an operator would bound its timeouts.
func WithHTTPClient(client *http.Client) RegistryOption {
	return func(r *RegistryResolver) { r.client = client }
}

// WithCredentials supplies per-host registry credentials.
func WithCredentials(credentials CredentialFunc) RegistryOption {
	return func(r *RegistryResolver) { r.credentials = credentials }
}

func NewRegistryResolver(options ...RegistryOption) *RegistryResolver {
	resolver := &RegistryResolver{
		client:      &http.Client{Timeout: 30 * time.Second},
		credentials: func(string) BasicAuth { return BasicAuth{} },
	}
	for _, option := range options {
		option(resolver)
	}
	return resolver
}

// ResolveManifest reads the platform manifest for a digest-pinned reference and
// states every layer in both digest spaces. A reference that is not
// digest-pinned is refused rather than resolved through a tag, because a tag is
// not image identity and Mercator has already pinned the reference by the time
// placement asks.
func (r *RegistryResolver) ResolveManifest(ctx context.Context, imageRef string, platform domain.Platform) (domain.ImageManifest, error) {
	ref, err := parseDigestRef(imageRef)
	if err != nil {
		return domain.ImageManifest{}, err
	}
	manifest, digest, err := r.platformManifest(ctx, ref, platform)
	if err != nil {
		return domain.ImageManifest{}, err
	}
	diffIDs, err := r.configDiffIDs(ctx, ref, manifest.Config.Digest)
	if err != nil {
		return domain.ImageManifest{}, err
	}
	return assembleManifest(digest, manifest.Layers, diffIDs)
}

// assembleManifest zips the two digest spaces together. The OCI image
// specification orders rootfs.diff_ids exactly as the manifest orders layers,
// so a count mismatch means the registry contradicted itself and the manifest
// is unusable rather than half-usable.
func assembleManifest(digest string, layers []registryLayer, diffIDs []string) (domain.ImageManifest, error) {
	if len(diffIDs) != len(layers) {
		return domain.ImageManifest{}, fmt.Errorf(
			"%w: the manifest names %d layers and its config names %d diff IDs",
			ErrManifestUnresolvable, len(layers), len(diffIDs),
		)
	}
	manifest := domain.ImageManifest{Known: true, Digest: digest, Layers: make([]domain.ImageLayer, 0, len(layers))}
	for index, layer := range layers {
		manifest.Layers = append(manifest.Layers, domain.ImageLayer{
			Digest:          layer.Digest,
			DiffID:          diffIDs[index],
			CompressedBytes: layer.Size,
		})
	}
	return manifest, nil
}

// platformManifest follows an index to the one manifest built for this
// platform. A registry that serves a single manifest is already the answer.
func (r *RegistryResolver) platformManifest(ctx context.Context, ref digestRef, platform domain.Platform) (registryManifest, string, error) {
	body, contentType, err := r.get(ctx, ref, "manifests/"+ref.Digest, manifestAccept)
	if err != nil {
		return registryManifest{}, "", err
	}
	var document registryManifest
	if err := json.Unmarshal(body, &document); err != nil {
		return registryManifest{}, "", fmt.Errorf("ociresolver: decode manifest for %s: %w", ref, err)
	}
	if !isIndex(contentType, document) {
		return document, ref.Digest, nil
	}
	selected, err := selectPlatform(document.Manifests, platform)
	if err != nil {
		return registryManifest{}, "", fmt.Errorf("%w: %s on %s", err, ref, platform)
	}
	body, _, err = r.get(ctx, ref, "manifests/"+selected, manifestAccept)
	if err != nil {
		return registryManifest{}, "", err
	}
	var child registryManifest
	if err := json.Unmarshal(body, &child); err != nil {
		return registryManifest{}, "", fmt.Errorf("ociresolver: decode manifest %s for %s: %w", selected, ref, err)
	}
	return child, selected, nil
}

func (r *RegistryResolver) configDiffIDs(ctx context.Context, ref digestRef, configDigest string) ([]string, error) {
	if configDigest == "" {
		return nil, fmt.Errorf("%w: %s names no config blob", ErrManifestUnresolvable, ref)
	}
	body, _, err := r.get(ctx, ref, "blobs/"+configDigest, "application/json")
	if err != nil {
		return nil, err
	}
	var config struct {
		RootFS struct {
			DiffIDs []string `json:"diff_ids"`
		} `json:"rootfs"`
	}
	if err := json.Unmarshal(body, &config); err != nil {
		return nil, fmt.Errorf("ociresolver: decode config %s for %s: %w", configDigest, ref, err)
	}
	return config.RootFS.DiffIDs, nil
}

var manifestAccept = strings.Join([]string{
	mediaTypeOCIIndex, mediaTypeOCIManifest, mediaTypeDockerManifestList, mediaTypeDockerManifest,
}, ", ")

// get performs one registry read, obtaining a bearer token first if the
// registry asks for one. The token exchange is the whole of registry v2 auth:
// the 401 names the realm, the service, and the scope, and the realm mints a
// token for exactly that scope.
func (r *RegistryResolver) get(ctx context.Context, ref digestRef, path, accept string) ([]byte, string, error) {
	response, err := r.do(ctx, ref, path, accept, "")
	if err != nil {
		return nil, "", err
	}
	if response.StatusCode == http.StatusUnauthorized {
		header := response.Header.Get("Www-Authenticate")
		challenge := parseChallenge(header)
		response.Body.Close()
		// A registry that authenticates with HTTP basic already had its chance:
		// the request carried whatever credentials Mercator has for this host,
		// so a second attempt would present the same ones.
		if challenge.Realm == "" {
			return nil, "", fmt.Errorf("%w: %s refused the read of %s and challenged with %q", ErrUnauthorized, ref.Registry, ref, header)
		}
		token, tokenErr := r.token(ctx, ref, challenge)
		if tokenErr != nil {
			return nil, "", tokenErr
		}
		if response, err = r.do(ctx, ref, path, accept, token); err != nil {
			return nil, "", err
		}
	}
	defer response.Body.Close()
	switch {
	case response.StatusCode == http.StatusOK:
		body, readErr := io.ReadAll(response.Body)
		if readErr != nil {
			return nil, "", fmt.Errorf("ociresolver: read %s from %s: %w", path, ref.Registry, readErr)
		}
		return body, response.Header.Get("Content-Type"), nil
	case response.StatusCode == http.StatusUnauthorized, response.StatusCode == http.StatusForbidden:
		return nil, "", fmt.Errorf("%w: %s rejected the read of %s", ErrUnauthorized, ref.Registry, ref)
	case response.StatusCode == http.StatusNotFound:
		return nil, "", fmt.Errorf("%w: %s has no %s", ErrImageUnknown, ref.Registry, ref)
	default:
		return nil, "", fmt.Errorf("ociresolver: %s answered %s for %s", ref.Registry, response.Status, path)
	}
}

func (r *RegistryResolver) do(ctx context.Context, ref digestRef, path, accept, token string) (*http.Response, error) {
	endpoint := ref.Scheme() + "://" + ref.Registry + "/v2/" + ref.Repository + "/" + path
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("ociresolver: build request for %s: %w", endpoint, err)
	}
	request.Header.Set("Accept", accept)
	switch credentials := r.credentials(ref.Registry); {
	case token != "":
		request.Header.Set("Authorization", "Bearer "+token)
	case credentials.Username != "":
		request.SetBasicAuth(credentials.Username, credentials.Password)
	}
	response, err := r.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("ociresolver: reach %s: %w", endpoint, err)
	}
	return response, nil
}

// token exchanges the registry's challenge for a bearer token, presenting
// credentials when Mercator has them for that host. A public registry mints an
// anonymous token for a pull scope, which is how docker.io works.
func (r *RegistryResolver) token(ctx context.Context, ref digestRef, challenge authChallenge) (string, error) {
	endpoint, err := url.Parse(challenge.Realm)
	if err != nil {
		return "", fmt.Errorf("%w: %s named an unusable token realm %q", ErrUnauthorized, ref.Registry, challenge.Realm)
	}
	query := endpoint.Query()
	if challenge.Service != "" {
		query.Set("service", challenge.Service)
	}
	scope := challenge.Scope
	if scope == "" {
		scope = "repository:" + ref.Repository + ":pull"
	}
	query.Set("scope", scope)
	endpoint.RawQuery = query.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return "", fmt.Errorf("ociresolver: build token request for %s: %w", ref.Registry, err)
	}
	if credentials := r.credentials(ref.Registry); credentials.Username != "" {
		request.SetBasicAuth(credentials.Username, credentials.Password)
	}
	response, err := r.client.Do(request)
	if err != nil {
		return "", fmt.Errorf("ociresolver: reach the token endpoint for %s: %w", ref.Registry, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: %s answered %s to the token request", ErrUnauthorized, ref.Registry, response.Status)
	}
	var minted struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&minted); err != nil {
		return "", fmt.Errorf("ociresolver: decode the token from %s: %w", ref.Registry, err)
	}
	if minted.Token != "" {
		return minted.Token, nil
	}
	if minted.AccessToken != "" {
		return minted.AccessToken, nil
	}
	return "", fmt.Errorf("%w: %s minted an empty token", ErrUnauthorized, ref.Registry)
}

type registryManifest struct {
	MediaType string `json:"mediaType"`
	Config    struct {
		Digest string `json:"digest"`
	} `json:"config"`
	Layers    []registryLayer      `json:"layers"`
	Manifests []registryDescriptor `json:"manifests"`
}

type registryLayer struct {
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

type registryDescriptor struct {
	Digest   string `json:"digest"`
	Platform struct {
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
		Variant      string `json:"variant"`
	} `json:"platform"`
}

// isIndex distinguishes a multi-platform index from a single manifest. The
// content type is authoritative when the registry sends one; a document that
// carries child manifests and no layers is the same thing said structurally,
// which is what a registry that omits the header leaves to read.
func isIndex(contentType string, document registryManifest) bool {
	mediaType := document.MediaType
	if declared, _, _ := strings.Cut(contentType, ";"); declared != "" {
		mediaType = strings.TrimSpace(declared)
	}
	switch mediaType {
	case mediaTypeOCIIndex, mediaTypeDockerManifestList:
		return true
	case mediaTypeOCIManifest, mediaTypeDockerManifest:
		return false
	default:
		return len(document.Manifests) > 0 && len(document.Layers) == 0
	}
}

// selectPlatform picks the one build this Run is pinned to. Attestation and
// unknown-platform entries are skipped rather than matched, because they are
// not images anything can run.
func selectPlatform(manifests []registryDescriptor, platform domain.Platform) (string, error) {
	for _, candidate := range manifests {
		if candidate.Platform.OS == platform.OS && candidate.Platform.Architecture == platform.Architecture {
			return candidate.Digest, nil
		}
	}
	return "", ErrManifestUnresolvable
}

type authChallenge struct {
	Realm   string
	Service string
	Scope   string
}

// parseChallenge reads the Www-Authenticate header a registry answers a 401
// with: `Bearer realm="https://auth.example/token",service="registry",scope="..."`.
func parseChallenge(header string) authChallenge {
	scheme, parameters, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return authChallenge{}
	}
	challenge := authChallenge{}
	for parameter := range strings.SplitSeq(parameters, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(parameter), "=")
		if !ok {
			continue
		}
		value = strings.Trim(value, `"`)
		switch key {
		case "realm":
			challenge.Realm = value
		case "service":
			challenge.Service = value
		case "scope":
			challenge.Scope = value
		}
	}
	return challenge
}

// digestRef is a digest-pinned reference split into the three things a registry
// read needs: which host serves it, which repository holds it, and which
// manifest is being asked for.
type digestRef struct {
	Registry   string
	Repository string
	Digest     string
}

func (ref digestRef) String() string {
	return ref.Registry + "/" + ref.Repository + "@" + ref.Digest
}

// Scheme is plaintext for a registry on this machine and TLS everywhere else,
// which is the same rule Docker applies to a loopback registry. A registry
// reached over the network is never read in the clear.
func (ref digestRef) Scheme() string {
	host, _, _ := strings.Cut(ref.Registry, ":")
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return "http"
	}
	return "https"
}

// parseDigestRef splits `[registry/]repository@sha256:...` the way the OCI
// distribution spec does, including Docker Hub's two unwritten rules: a
// bare name lives in `library`, and `docker.io` is served by
// `registry-1.docker.io`.
func parseDigestRef(image string) (digestRef, error) {
	if !digestRefPattern.MatchString(image) {
		return digestRef{}, fmt.Errorf("ociresolver: %q is not digest-pinned, and a tag is not image identity", image)
	}
	name, digest, _ := strings.Cut(image, "@")
	registry := "registry-1.docker.io"
	repository := name
	if head, rest, found := strings.Cut(name, "/"); found && isRegistryHost(head) {
		registry = head
		repository = rest
		if head == "docker.io" || head == "index.docker.io" {
			registry = "registry-1.docker.io"
		}
	}
	if registry == "registry-1.docker.io" && !strings.Contains(repository, "/") {
		repository = "library/" + repository
	}
	return digestRef{Registry: registry, Repository: repository, Digest: digest}, nil
}

// isRegistryHost separates `example.com/team/image` from `team/image`, which is
// the only ambiguity in a reference: a first component is a host when it looks
// like one.
func isRegistryHost(component string) bool {
	return strings.Contains(component, ".") || strings.Contains(component, ":") || component == "localhost"
}

// DockerConfigCredentials reads the registry credentials the operator already
// has, from the file `docker login` writes. Only the plaintext `auths` entries
// are read: a credential helper is an external program, and shelling out to one
// on the placement path is a decision to make deliberately rather than by
// accident. A registry with no entry is anonymous, which is the truth.
func DockerConfigCredentials(path string) (CredentialFunc, error) {
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return func(string) BasicAuth { return BasicAuth{} }, nil
	}
	if err != nil {
		return nil, fmt.Errorf("ociresolver: read %s: %w", path, err)
	}
	var config struct {
		Auths map[string]struct {
			Auth     string `json:"auth"`
			Username string `json:"username"`
			Password string `json:"password"`
		} `json:"auths"`
	}
	if err := json.Unmarshal(body, &config); err != nil {
		return nil, fmt.Errorf("ociresolver: decode %s: %w", path, err)
	}
	byHost := make(map[string]BasicAuth, len(config.Auths))
	for host, entry := range config.Auths {
		credentials := BasicAuth{Username: entry.Username, Password: entry.Password}
		if entry.Auth != "" {
			decoded, decodeErr := base64.StdEncoding.DecodeString(entry.Auth)
			if decodeErr != nil {
				return nil, fmt.Errorf("ociresolver: decode the credential for %s in %s: %w", host, path, decodeErr)
			}
			username, password, _ := strings.Cut(string(decoded), ":")
			credentials = BasicAuth{Username: username, Password: password}
		}
		if credentials.Username == "" {
			continue
		}
		byHost[dockerConfigHost(host)] = credentials
	}
	return func(registryHost string) BasicAuth { return byHost[registryHost] }, nil
}

// dockerConfigHost normalizes the key `docker login` writes into the host the
// resolver reads from. Docker Hub is stored under its v1 index URL and served
// from registry-1.docker.io.
func dockerConfigHost(key string) string {
	key = strings.TrimPrefix(strings.TrimPrefix(key, "https://"), "http://")
	key = strings.TrimSuffix(key, "/")
	if host, _, found := strings.Cut(key, "/"); found {
		key = host
	}
	if key == "index.docker.io" || key == "docker.io" {
		return "registry-1.docker.io"
	}
	return key
}

// DefaultDockerConfigPath is where `docker login` stores credentials, honouring
// the same DOCKER_CONFIG override the Docker CLI does.
func DefaultDockerConfigPath(getenv func(string) string) string {
	if directory := getenv("DOCKER_CONFIG"); directory != "" {
		return filepath.Join(directory, "config.json")
	}
	home := getenv("HOME")
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".docker", "config.json")
}
