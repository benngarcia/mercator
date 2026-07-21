package scenario

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func loadFixtureText(t *testing.T, text string) (Scenario, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.json")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return Load(path)
}

const minimalGreenScenario = `{
  "summary": "One idle rental, one request, rental wins.",
  "status": "green",
  "world": {
    "rentals": [{"id": "rental-a", "state": "idle", "rate_per_hour_usd": 1.0}]
  },
  "request": {"image": "app:v1"},
  "expect": {"outcome": "place", "offer": "rental-a"}
}`

func TestLoadParsesHumanReadableUnits(t *testing.T) {
	sc, err := loadFixtureText(t, `{
      "summary": "Units parse.",
      "status": "target",
      "world": {
        "images": {"app:v1": {"layers": [{"name": "base", "size": "1.5GB"}]}},
        "rentals": [{
          "id": "rental-a",
          "state": "busy",
          "busy": {"remaining_max_runtime": "6m"},
          "named_caches": {"dataset-x": "40GB"},
          "rate_per_hour_usd": 2.5
        }]
      },
      "request": {"image": "app:v1"},
      "expect": {
        "outcome": "defer",
        "defer": {"reason": "BUSY_RENTAL_WORTH_WAITING", "deadline": "+6m"}
      }
    }`)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := sc.World.Images["app:v1"].Layers[0].Size; got != ByteSize(1_500_000_000) {
		t.Fatalf("layer size = %d, want 1.5GB in bytes", got)
	}
	if got := sc.World.Rentals[0].Busy.RemainingMaxRuntime.Duration(); got != 6*time.Minute {
		t.Fatalf("remaining max runtime = %v, want 6m", got)
	}
	deadline := sc.Expect.Defer.Deadline.Resolve(sc.World.Start())
	if want := sc.World.Start().Add(6 * time.Minute); !deadline.Equal(want) {
		t.Fatalf("deadline = %v, want %v", deadline, want)
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	_, err := loadFixtureText(t, strings.Replace(minimalGreenScenario,
		`"request"`, `"unexpected": true, "request"`, 1))
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown fields must be rejected, got %v", err)
	}
}

func TestLoadRejectsFixtureMistakes(t *testing.T) {
	cases := map[string]struct{ from, to, want string }{
		"unknown status": {`"status": "green"`, `"status": "someday"`, "status"},
		"idle rental with busy block": {
			`"state": "idle"`, `"state": "idle", "busy": {"remaining_max_runtime": "5m"}`, "busy"},
		"winning offer missing from world": {`"offer": "rental-a"`, `"offer": "rental-z"`, "not in the world"},
		"defer without deadline":           {`"outcome": "place", "offer": "rental-a"`, `"outcome": "defer"`, "deadline"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := loadFixtureText(t, strings.Replace(minimalGreenScenario, tc.from, tc.to, 1))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected an error mentioning %q, got %v", tc.want, err)
			}
		})
	}
}

func TestBoundChecksExactAndRangeExpectations(t *testing.T) {
	exact := float64(240)
	if problem := (Bound{Exactly: &exact}).Check(240); problem != "" {
		t.Fatalf("exact bound must accept its value: %s", problem)
	}
	if problem := (Bound{Exactly: &exact}).Check(241); problem == "" {
		t.Fatalf("exact bound must reject a different value")
	}
	atLeast := float64(200)
	if problem := (Bound{AtLeast: &atLeast}).Check(199); problem == "" {
		t.Fatalf("at_least bound must reject a smaller value")
	}
}
