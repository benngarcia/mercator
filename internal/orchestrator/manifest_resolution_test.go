package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter/fake"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/ociresolver"
	"github.com/benngarcia/mercator/internal/scheduler"
)

// recordingResolver answers a placement's manifest read and remembers what it
// was given to answer it with.
type recordingResolver struct {
	answer   domain.ImageManifest
	failure  error
	deadline time.Time
	bounded  bool
}

func (resolver *recordingResolver) ResolveManifest(ctx context.Context, _ string, _ domain.Platform) (domain.ImageManifest, error) {
	resolver.deadline, resolver.bounded = ctx.Deadline()
	return resolver.answer, resolver.failure
}

// TestPlacementBoundsWhatItWaitsForAManifest keeps a registry from holding a
// run-create open. Resolution is an external read on a synchronous request
// path, and a registry that accepts a connection and then answers nothing would
// otherwise keep the handler running long after the caller has given up.
func TestPlacementBoundsWhatItWaitsForAManifest(t *testing.T) {
	resolver := &recordingResolver{answer: domain.ImageManifest{Known: true}}
	orch := New(openOrchestratorLog(t), scheduler.New(), fake.New(), WithImageManifests(resolver), withTestCapacity())

	if _, err := orch.PreviewPlacement(context.Background(), "run_preview", orchRevision()); err != nil {
		t.Fatalf("preview placement: %v", err)
	}

	if !resolver.bounded {
		t.Fatal("placement handed the registry a context that never expires")
	}
	if budget := time.Until(resolver.deadline); budget > manifestBudget {
		t.Fatalf("the registry was given %s to answer, want at most %s", budget, manifestBudget)
	}
}

// TestPlacementRecordsWhyAManifestCouldNotBeRead is what keeps a registry
// failure from reading as every candidate being equally warm. The transfer
// answer is unknown either way; what the decision has to carry is which silence
// it was, because an image nobody pushed, credentials a registry refused, a
// registry rate limiting Mercator, and a registry that answered nothing at all
// are fixed by different people doing different things. The classification is
// the resolver's, made where the response is: a failure it never named stays
// unreadable rather than being guessed at from its text.
func TestPlacementRecordsWhyAManifestCouldNotBeRead(t *testing.T) {
	log := openOrchestratorLog(t)
	for _, testCase := range []struct {
		name    string
		failure error
		want    string
	}{
		{name: "nobody pushed it", failure: fmt.Errorf("read manifest: %w", ociresolver.ErrImageUnknown), want: "registry_image_unknown"},
		{name: "no build for this platform", failure: fmt.Errorf("read manifest: %w", ociresolver.ErrManifestUnresolvable), want: "registry_manifest_unresolvable"},
		{name: "credentials refused", failure: fmt.Errorf("read manifest: %w", ociresolver.ErrUnauthorized), want: "registry_unauthorized"},
		{name: "rate limiting this client", failure: fmt.Errorf("read manifest: %w", ociresolver.ErrThrottled), want: "registry_throttled"},
		{name: "answered nothing in time", failure: fmt.Errorf("%w: %w", ociresolver.ErrUnreachable, context.DeadlineExceeded), want: "registry_unreachable"},
		{name: "a refusal nothing has classified", failure: errors.New("the registry answered 503 Service Unavailable"), want: "registry_unreadable"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			resolver := &recordingResolver{failure: testCase.failure}
			offer := orchOffer("off_1", time.Now().UTC())
			offer.Images = domain.ImageInventory{Known: true}
			orch := New(
				log,
				scheduler.New(),
				fake.New(fake.WithOffers([]domain.OfferSnapshot{offer})),
				WithImageManifests(resolver),

				withTestCapacity(),
			)

			decision, err := orch.PreviewPlacement(context.Background(), "run_preview", orchRevision())

			if err != nil {
				t.Fatalf("preview placement: %v", err)
			}
			if len(decision.Candidates) == 0 {
				t.Fatal("placement evaluated no candidates")
			}
			pull := decision.Candidates[0].Estimates.Stages.ImageFetch
			if pull.Source != testCase.want || pull.Confidence != 0 {
				t.Fatalf("pull estimate = %+v, want source %q carrying no confidence", pull, testCase.want)
			}
		})
	}
}
