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

// inputsAreDurable answers whether every Artifact this Run declared reading is
// in the object store. A Run whose inputs are not all durable is not admitted:
// it waits for a publication, never for a machine. That is what makes a local
// copy an optimisation, so losing every copy costs a fetch and never costs
// availability.
func (o *Orchestrator) inputsAreDurable(ctx context.Context, workspaceID string, workload domain.WorkloadRevision) (bool, error) {
	consumes := workload.Spec.Artifacts.Consumes
	if len(consumes) == 0 {
		return true, nil
	}
	if o.artifacts == nil {
		return false, fmt.Errorf(
			"orchestrator: Run reads Artifact %q and this Mercator has no artifact catalog to establish that it exists",
			consumes[0],
		)
	}
	for _, artifactID := range consumes {
		version, err := o.artifacts.ArtifactVersion(ctx, workspaceID, artifactID)
		if err != nil {
			return false, fmt.Errorf("orchestrator: read Artifact %q: %w", artifactID, err)
		}
		if !version.Durable() {
			return false, nil
		}
	}
	return true, nil
}
