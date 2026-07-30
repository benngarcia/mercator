package nodeagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/benngarcia/mercator/internal/capability"
	"github.com/benngarcia/mercator/internal/domain"
)

// This file is the node's half of Artifact locality: fetching one immutable
// version out of the object store and being able to say afterwards that it is
// here and that it is the right bytes.
//
// Two things make it safe to place work against. The node reads from a location
// the control plane minted, so no object-store credential of Mercator's is ever
// on a machine an operator rents by the hour. And the copy is hashed on arrival
// and recorded under the digest it actually produced, never under the digest it
// was asked for: a replica is worth exactly what checking it says it is worth,
// and a node that filed a mismatch under the version's name would be telling
// Placement a Run can read bytes that are not that Artifact.

// artifactReplicaFile is what the node writes beside each copy: the version it
// is, the digest the bytes on this disk actually hashed to, and when that was
// established. The digest is recomputed from the stream rather than copied from
// the command, which is the whole of what verification is here.
type artifactReplicaFile struct {
	ArtifactID    string    `json:"artifact_id"`
	WorkspaceID   string    `json:"workspace_id"`
	ContentDigest string    `json:"content_digest"`
	SizeBytes     int64     `json:"size_bytes"`
	VerifiedAt    time.Time `json:"verified_at"`
	// Matches is whether the bytes here are the bytes the catalog named. A copy
	// that does not match is kept and reported unverified rather than deleted:
	// what it is worth is a control-plane decision, and an agent that quietly
	// removed it would leave the next report unable to say anything happened.
	Matches bool `json:"matches"`
}

func (file artifactReplicaFile) replica() domain.ArtifactReplica {
	state := domain.ArtifactReplicaUnverified
	if file.Matches {
		state = domain.ArtifactReplicaVerified
	}
	return domain.ArtifactReplica{
		ArtifactID:    file.ArtifactID,
		ContentDigest: file.ContentDigest,
		SizeBytes:     file.SizeBytes,
		State:         state,
		VerifiedAt:    file.VerifiedAt,
	}
}

// PrepareArtifact replicates one immutable version onto this machine. It reads
// from the location the command names and nowhere else: the control plane owns
// the object store and mints the read, so a node holds no credential for it and
// a compromised machine can fetch exactly the content it was told to.
func (docker *DockerRuntime) PrepareArtifact(ctx context.Context, command capability.PrepareArtifactCommand) error {
	if docker.artifactRoot == "" {
		return fmt.Errorf("%w: this node has nowhere to keep Artifact copies", capability.ErrCapabilityUnsupported)
	}
	if command.ArtifactID == "" || command.Source == "" || command.ContentDigest == "" {
		return fmt.Errorf("prepare Artifact: a replica needs a version, a source, and the digest to check it against")
	}
	directory := docker.artifactDirectory(command.WorkspaceID)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("prepare Artifact %s: %w", command.ArtifactID, err)
	}
	digest, size, err := docker.fetchArtifact(ctx, command, filepath.Join(directory, artifactFileName(command.ArtifactID)))
	if err != nil {
		return err
	}
	return writeArtifactRecord(filepath.Join(directory, artifactFileName(command.ArtifactID)+".json"), artifactReplicaFile{
		ArtifactID:    command.ArtifactID,
		WorkspaceID:   command.WorkspaceID,
		ContentDigest: digest,
		SizeBytes:     size,
		VerifiedAt:    docker.now().UTC(),
		Matches:       digest == command.ContentDigest,
	})
}

// fetchArtifact streams the content to a temporary file and hashes it as it
// goes, then puts it in place. Hashing the stream is what makes one read do both
// jobs; landing it under its final name only once complete is what stops an
// interrupted fetch from being reported as a copy.
func (docker *DockerRuntime) fetchArtifact(ctx context.Context, command capability.PrepareArtifactCommand, path string) (string, int64, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, command.Source, nil)
	if err != nil {
		return "", 0, fmt.Errorf("prepare Artifact %s: %w", command.ArtifactID, err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", 0, fmt.Errorf("read Artifact %s from the object store: %w", command.ArtifactID, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf(
			"the object store answered %s for Artifact %s, so this node holds no copy of it",
			response.Status, command.ArtifactID,
		)
	}
	partial, err := os.CreateTemp(filepath.Dir(path), ".partial-*")
	if err != nil {
		return "", 0, fmt.Errorf("prepare Artifact %s: %w", command.ArtifactID, err)
	}
	defer func() { _ = os.Remove(partial.Name()) }()
	sum := sha256.New()
	size, err := io.Copy(io.MultiWriter(partial, sum), response.Body)
	if err != nil {
		_ = partial.Close()
		return "", 0, fmt.Errorf("read Artifact %s from the object store: %w", command.ArtifactID, err)
	}
	if err := partial.Close(); err != nil {
		return "", 0, fmt.Errorf("prepare Artifact %s: %w", command.ArtifactID, err)
	}
	if err := os.Rename(partial.Name(), path); err != nil {
		return "", 0, fmt.Errorf("prepare Artifact %s: %w", command.ArtifactID, err)
	}
	return "sha256:" + hex.EncodeToString(sum.Sum(nil)), size, nil
}

func writeArtifactRecord(path string, record artifactReplicaFile) error {
	encoded, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("record Artifact %s: %w", record.ArtifactID, err)
	}
	return os.WriteFile(path, encoded, 0o600)
}

// artifacts is every Artifact copy this machine holds, read back out of the
// records the fetches left. A node with no place to keep copies says nothing
// rather than claiming it enumerated and found none: it has not looked, and
// "I hold no copy" is a fact that would strike this machine out of every
// placement for content it may well be sitting on.
//
// One unreadable record costs one copy rather than the whole report, for the
// reason every other inventory here says the same: an operator tidying a
// directory on a working machine must not take it out of the fleet.
func (docker *DockerRuntime) artifacts() domain.ArtifactInventory {
	if docker.artifactRoot == "" {
		return domain.ArtifactInventory{}
	}
	inventory := domain.ArtifactInventory{Known: true, ObservedAt: docker.now().UTC()}
	workspaces, err := os.ReadDir(docker.artifactRoot)
	if err != nil && !os.IsNotExist(err) {
		return domain.ArtifactInventory{}
	}
	for _, workspace := range workspaces {
		if !workspace.IsDir() {
			continue
		}
		inventory.Replicas = append(inventory.Replicas, docker.workspaceReplicas(filepath.Join(docker.artifactRoot, workspace.Name()))...)
	}
	slices.SortFunc(inventory.Replicas, func(left, right domain.ArtifactReplica) int {
		return strings.Compare(left.ArtifactID, right.ArtifactID)
	})
	return inventory
}

func (docker *DockerRuntime) workspaceReplicas(directory string) []domain.ArtifactReplica {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil
	}
	var replicas []domain.ArtifactReplica
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		encoded, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			continue
		}
		var record artifactReplicaFile
		if err := json.Unmarshal(encoded, &record); err != nil || record.ArtifactID == "" {
			continue
		}
		// A record whose content is gone is a record and not a copy. The bytes
		// are what a Run reads, so a node that reported this would be offering
		// warmth it cannot deliver.
		if _, err := os.Stat(filepath.Join(directory, strings.TrimSuffix(entry.Name(), ".json"))); err != nil {
			continue
		}
		replicas = append(replicas, record.replica())
	}
	return replicas
}

// artifactDirectory keeps one workspace's copies apart from another's on the
// disk as well as in the record. An Artifact never crosses a workspace, and a
// flat directory keyed by version ID would make two tenants naming one version
// one file.
func (docker *DockerRuntime) artifactDirectory(workspaceID string) string {
	return filepath.Join(docker.artifactRoot, artifactFileName(workspaceID))
}

// artifactFileName makes one identity into one path component. Version IDs
// carry colons and slashes, which are a directory traversal rather than a name,
// so the identity is written as its own digest and the record inside states what
// it is.
func artifactFileName(identity string) string {
	sum := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(sum[:])
}
