package scenario

import (
	"testing"

	"github.com/benngarcia/mercator/internal/domain"
)

// TestEveryArrivingRunStatesAClassMercatorKnows is the promise the console's own
// decoder was already making and nothing here was keeping. A ServiceClass
// replaced the placement objective outright, so every reader downstream of a Run
// was told it would be handed one of five words. `WorkloadForRun` cast the
// Blueprint's request straight into the domain, so a Blueprint that said nothing
// about the kind of work a Run is produced a revision carrying the empty class,
// which is a Run the operator API cannot accept: it normalises an omitted class
// to standard before validation ever sees it.
//
// Nothing caught that, because nothing in the corpus reads the class of a Run it
// did not itself state. The console does. Its event decoder requires one of the
// five literals, so the empty string failed to decode, the canvas rendered
// nothing, and the browser proof timed out waiting for a heading rather than
// reporting a class it could not read. That is why this is a test about every
// arriving Run rather than about the one demo Blueprint that exposed it.
func TestEveryArrivingRunStatesAClassMercatorKnows(t *testing.T) {
	catalog, err := OpenCatalog("scenarios")
	if err != nil {
		t.Fatalf("open the corpus: %v", err)
	}

	// The one Blueprint that states a class on purpose that Mercator does not
	// know, to prove the door refuses it. Normalisation deliberately leaves an
	// unrecognised word alone rather than defaulting it, because defaulting would
	// place work at rates its caller never asked for, so this fixture is the
	// counterpart of the rule rather than an exception to it.
	const refusesAnUnknownClass = "a-class-mercator-does-not-know-is-refused"

	for _, entry := range catalog.Entries() {
		blueprint := entry.Blueprint
		if blueprint.Arrivals == nil || blueprint.Name == refusesAnUnknownClass {
			continue
		}
		runs, err := blueprint.Arrivals.ExpandedRuns()
		if err != nil {
			t.Fatalf("%s: expand arrivals: %v", blueprint.Name, err)
		}
		for _, run := range runs {
			revision := WorkloadForRun("ws_test", run.Name, run.Request)
			if class := revision.Spec.Placement.Class; !class.Known() {
				t.Errorf(
					"%s: Run %q arrives as class %q, and every reader of a Run was promised one of %v",
					blueprint.Name, run.Name, class, domain.KnownServiceClasses,
				)
			}
		}
	}
}
