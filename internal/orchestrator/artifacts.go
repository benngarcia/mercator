package orchestrator

import (
	"context"
	"fmt"

	"github.com/benngarcia/mercator/internal/domain"
)

// ArtifactCatalog is the object store Mercator asks what an Artifact version is
// and whether its bytes are there. It is the whole observation path for
// durability, and it is deliberately not a question about capacity: an answer
// assembled from what machines report holding would make a Run admissible
// because some host has bytes and inadmissible the moment that host goes away,
// which is a distributed filesystem rather than an authority.
//
// A version the store has never heard of comes back zero, which is not durable.
// That is the same answer as a version whose upload has not landed, because
// from a consumer's side they are the same fact: the content is not readable
// yet.
type ArtifactCatalog interface {
	ArtifactVersion(ctx context.Context, workspaceID, artifactID string) (domain.ArtifactVersion, error)
}

// WithArtifactCatalog supplies the object store admission asks about a Run's
// declared inputs. Without one Mercator cannot establish that an Artifact
// exists, so a Run that reads one is refused rather than placed on the
// assumption that it does.
func WithArtifactCatalog(catalog ArtifactCatalog) Option {
	return func(o *Orchestrator) {
		o.artifacts = catalog
	}
}

// refuseUnknowableInputs rejects a Run whose declared inputs this Mercator
// cannot ask about at all. It is answered at intake, before the Run is
// recorded, because the answer never changes: a Run accepted here would sit in
// the projection forever with a caller told it was accepted, which is the shape
// of a wedged Run rather than of a Run waiting for a publication. The refusal
// is the deployment's, not the request's, and it says which Artifact made the
// question unanswerable.
func (o *Orchestrator) refuseUnknowableInputs(workload domain.WorkloadRevision) error {
	consumes := workload.Spec.Artifacts.Consumes
	if len(consumes) == 0 || o.artifacts != nil {
		return nil
	}
	return fmt.Errorf(
		"ARTIFACT_CATALOG_UNAVAILABLE: Run reads Artifact %q and this Mercator has no artifact catalog to establish that it exists",
		consumes[0],
	)
}

// consumedArtifacts is what the catalog says each version this Run reads is.
// One read answers both questions Placement has about an input: whether it
// exists, which decides admission, and how big it is, which decides what a host
// holding no copy of it would still have to fetch. Size is a property of the
// content, so it comes from the store rather than from any machine: a host that
// does not have something cannot be asked how large it is.
func (o *Orchestrator) consumedArtifacts(ctx context.Context, workspaceID string, workload domain.WorkloadRevision) ([]domain.ArtifactVersion, error) {
	// A catalog that went away after the Run was recorded leaves a Mercator
	// that can no longer answer a question it accepted, which is a failure to
	// propagate rather than a Run to place.
	if err := o.refuseUnknowableInputs(workload); err != nil {
		return nil, err
	}
	consumes := workload.Spec.Artifacts.Consumes
	versions := make([]domain.ArtifactVersion, 0, len(consumes))
	for _, artifactID := range consumes {
		version, err := o.artifacts.ArtifactVersion(ctx, workspaceID, artifactID)
		if err != nil {
			return nil, fmt.Errorf("orchestrator: read Artifact %q: %w", artifactID, err)
		}
		versions = append(versions, version)
	}
	return versions, nil
}

// inputsAreDurable answers whether every Artifact this Run declared reading is
// in the object store. A Run whose inputs are not all durable is not admitted:
// it waits for a publication, never for a machine. That is what makes a local
// copy an optimisation, so losing every copy costs a fetch and never costs
// availability.
func (o *Orchestrator) inputsAreDurable(ctx context.Context, workspaceID string, workload domain.WorkloadRevision) (bool, error) {
	versions, err := o.consumedArtifacts(ctx, workspaceID, workload)
	if err != nil {
		return false, err
	}
	for _, version := range versions {
		if !version.Durable() {
			return false, nil
		}
	}
	return true, nil
}
