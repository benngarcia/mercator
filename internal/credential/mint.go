package credential

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
)

// This file is the other half of what this package does. The rest of it keeps a
// connection's own secret at rest, sealed, so Mercator can present it to a
// provider. This is what Mercator hands a machine instead of ever presenting one
// of those: a credential minted for one fetch, narrower than the account behind
// it in every dimension the far side can be told to check.
//
// The two accounts here are the ones a node would otherwise need. A registry
// account can read every private image the workspace has, and an object-store
// key can read every Artifact ever published; both stay in the control plane,
// and what crosses to the machine is a pull of one image or a read of one
// object, expiring.

// DefaultMintWindow is how long a minted fetch stays presentable. It is long
// enough for a cold machine to finish a large pull over a slow link, and short
// enough that material captured off a host is worth little by the time anybody
// reads it. It is deliberately not a Run's runtime: a credential that lived as
// long as the workload it was fetched for would be readable for the whole of an
// execution rather than for the fetch.
const DefaultMintWindow = 15 * time.Minute

// RegistryAccount is one registry Mercator can read from, and the material it
// reads with. It never leaves this process.
type RegistryAccount struct {
	// Registry is the host as an image reference names it, so that matching a
	// reference against an account is a comparison rather than a guess.
	Registry string
	Username string
	Secret   string
}

// ObjectStoreAccount is the durable authority for Artifacts, as the control
// plane holds it. A machine never holds any of this: what it gets is one
// presigned read of one object.
type ObjectStoreAccount struct {
	// Endpoint is the S3-compatible service, with its scheme.
	Endpoint  string
	Bucket    string
	Region    string
	AccessKey string
	Secret    string
}

func (account ObjectStoreAccount) complete() error {
	if account.Endpoint == "" || account.Bucket == "" || account.Region == "" ||
		account.AccessKey == "" || account.Secret == "" {
		return fmt.Errorf(
			"credential: an object store account needs an endpoint, a bucket, a region and a key, and this one names %q/%q in %q",
			account.Endpoint, account.Bucket, account.Region,
		)
	}
	return nil
}

// Mint is the control plane's authority to hand a machine one fetch. It holds
// the accounts and answers with material that is not them.
type Mint struct {
	registries  map[string]RegistryAccount
	objectStore *ObjectStoreAccount
	window      time.Duration
	now         func() time.Time
}

// MintConfig is what an operator states about the accounts this Mercator holds.
type MintConfig struct {
	Registries []RegistryAccount
	// ObjectStore is the durable Artifact authority. Nil is a Mercator that has
	// none, which is a real deployment: nothing publishes a durable version, so
	// nothing can ask for a read of one, and asking anyway is refused rather
	// than answered with a location no node could fetch.
	ObjectStore *ObjectStoreAccount
	// Window overrides how long minted material stays presentable. Zero takes
	// DefaultMintWindow.
	Window time.Duration
	Now    func() time.Time
}

func NewMint(config MintConfig) (*Mint, error) {
	mint := &Mint{
		registries:  make(map[string]RegistryAccount, len(config.Registries)),
		objectStore: config.ObjectStore,
		window:      config.Window,
		now:         config.Now,
	}
	for _, account := range config.Registries {
		if account.Registry == "" || account.Username == "" || account.Secret == "" {
			return nil, fmt.Errorf("credential: a registry account needs a host, a username and a secret, and %q states %q", account.Registry, account.Username)
		}
		mint.registries[account.Registry] = account
	}
	if mint.objectStore != nil {
		if err := mint.objectStore.complete(); err != nil {
			return nil, err
		}
	}
	if mint.window == 0 {
		mint.window = DefaultMintWindow
	}
	if mint.now == nil {
		mint.now = time.Now
	}
	return mint, nil
}

// RegistryPull is the material one machine presents to pull one image, and
// nothing else.
//
// An image at a registry Mercator holds no account for is minted nothing, and
// that is the answer rather than a failure: a public image is read anonymously
// by anyone, so a machine presenting no credential for it is behaving exactly
// correctly. What would be a failure is minting material for it anyway, which
// would put an account on a machine for content that never needed one.
func (mint *Mint) RegistryPull(_ context.Context, operation, workspaceID, reference string) (domain.RegistryPull, error) {
	digest := domain.ReferenceDigest(reference)
	if operation == "" || workspaceID == "" || digest == "" {
		return domain.RegistryPull{}, fmt.Errorf(
			"credential: a pull is minted for one operation, one workspace and one digest, and this names %q, %q and %q",
			operation, workspaceID, reference,
		)
	}
	account, held := mint.registries[domain.ReferenceRegistry(reference)]
	if !held {
		return domain.RegistryPull{}, nil
	}
	return domain.RegistryPull{
		ContentCredentialScope: mint.scope(operation, workspaceID, digest),
		Registry:               account.Registry,
		Username:               account.Username,
		Secret:                 account.Secret,
	}, nil
}

// ArtifactRead is one presigned GET of one object, expiring with the scope that
// names it. The signature is over the object's own path, so the material cannot
// be turned into a read of anything else in the bucket.
//
// A Mercator with no object store refuses rather than answering. The durable
// location a catalog states is a name for content, so a node handed one and no
// read would go to the network with a URI nothing serves; saying so here names
// the missing configuration instead of leaving an operator to read it off a
// machine's fetch failure.
func (mint *Mint) ArtifactRead(_ context.Context, operation, workspaceID, artifactID, location string) (domain.ArtifactRead, error) {
	if operation == "" || workspaceID == "" || artifactID == "" || location == "" {
		return domain.ArtifactRead{}, fmt.Errorf(
			"credential: a read is minted for one operation, one workspace and one version, and this names %q, %q, %q at %q",
			operation, workspaceID, artifactID, location,
		)
	}
	if mint.objectStore == nil {
		return domain.ArtifactRead{}, fmt.Errorf(
			"credential: this Mercator holds no object store, so it cannot mint a read of %q, and the durable location %q is a name rather than a way in",
			artifactID, location,
		)
	}
	scope := mint.scope(operation, workspaceID, artifactID)
	signed, err := mint.objectStore.presign(objectKey(location), scope.ExpiresAt.Sub(mint.now().UTC()), mint.now().UTC())
	if err != nil {
		return domain.ArtifactRead{}, err
	}
	return domain.ArtifactRead{ContentCredentialScope: scope, Location: signed}, nil
}

func (mint *Mint) scope(operation, workspaceID, content string) domain.ContentCredentialScope {
	return domain.ContentCredentialScope{
		Operation:   operation,
		WorkspaceID: workspaceID,
		Content:     content,
		ExpiresAt:   mint.now().UTC().Add(mint.window),
	}
}

// objectKey is where one version's bytes live in the bucket, taken from the
// durable location the catalog states. Identity determines the address, so this
// is a translation rather than a lookup.
func objectKey(location string) string {
	_, path, found := strings.Cut(location, "://")
	if !found {
		return strings.TrimPrefix(location, "/")
	}
	return path
}

// presign signs one GET with SigV4 query parameters, which is how an
// S3-compatible store lets a holder read one object without holding a key. The
// standard library is all it takes: an object-store client would be a dependency
// for the one request this makes.
func (account ObjectStoreAccount) presign(key string, window time.Duration, at time.Time) (string, error) {
	endpoint, err := url.Parse(account.Endpoint)
	if err != nil {
		return "", fmt.Errorf("credential: the object store endpoint %q is not a URL: %w", account.Endpoint, err)
	}
	if window <= 0 {
		return "", fmt.Errorf("credential: a read minted for %s is expired before it is handed over", window)
	}
	stamp := at.Format("20060102T150405Z")
	day := at.Format("20060102")
	scope := day + "/" + account.Region + "/s3/aws4_request"
	path := "/" + account.Bucket + "/" + key
	query := url.Values{
		"X-Amz-Algorithm":     {"AWS4-HMAC-SHA256"},
		"X-Amz-Credential":    {account.AccessKey + "/" + scope},
		"X-Amz-Date":          {stamp},
		"X-Amz-Expires":       {strconv.Itoa(int(window.Seconds()))},
		"X-Amz-SignedHeaders": {"host"},
	}
	canonical := strings.Join([]string{
		"GET",
		escapePath(path),
		encodeQuery(query),
		"host:" + endpoint.Host + "\n",
		"host",
		"UNSIGNED-PAYLOAD",
	}, "\n")
	hashed := sha256.Sum256([]byte(canonical))
	toSign := strings.Join([]string{"AWS4-HMAC-SHA256", stamp, scope, hex.EncodeToString(hashed[:])}, "\n")
	signing := hmacAll([]byte("AWS4"+account.Secret), day, account.Region, "s3", "aws4_request")
	query.Set("X-Amz-Signature", hex.EncodeToString(hmacAll(signing, toSign)))
	return account.Endpoint + escapePath(path) + "?" + encodeQuery(query), nil
}

// escapePath encodes each path segment the way SigV4 canonicalises one, which
// leaves the separators alone. A version ID carries characters a URL escapes, so
// a store asked for the raw string would answer about a different object than
// the one that was signed.
func escapePath(path string) string {
	segments := strings.Split(path, "/")
	for index, segment := range segments {
		segments[index] = strings.ReplaceAll(url.QueryEscape(segment), "+", "%20")
	}
	return strings.Join(segments, "/")
}

// encodeQuery is the canonical query string SigV4 signs: sorted, and with spaces
// percent-encoded rather than written as plus signs.
func encodeQuery(query url.Values) string {
	return strings.ReplaceAll(query.Encode(), "+", "%20")
}

func hmacAll(key []byte, messages ...string) []byte {
	for _, message := range messages {
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(message))
		key = mac.Sum(nil)
	}
	return key
}
