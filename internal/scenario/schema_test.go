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
    "rentals": [{"id": "rental-a", "rate_per_hour_usd": 1.0}]
  },
  "request": {"image": "app:v1"},
  "expect": {"outcome": "place", "offer": "rental-a"}
}`

func TestLoadParsesHumanReadableUnits(t *testing.T) {
	sc, err := loadFixtureText(t, `{
      "summary": "Units parse.",
      "status": "target",
      "missing_capabilities": ["rental_schedule"],
      "world": {
        "images": {"app:v1": {"layers": [{"name": "base", "size": "1.5GB"}]}},
        "rentals": [{
          "id": "rental-a",
          "named_caches": {"dataset-x": "40GB"},
          "rate_per_hour_usd": 2.5
        }],
        "rental_schedules": [{
          "rental": "rental-a",
          "version": 2,
          "running": {
            "placement": "placement-active",
            "run": "run-active",
            "remaining_max_runtime": "6m"
          },
          "scheduled": [{
            "placement": "placement-ahead",
            "run": "run-ahead",
            "max_runtime": "4m"
          }]
        }]
      },
      "request": {"image": "app:v1", "max_runtime": "2m"},
      "expect": {
        "outcome": "place",
        "offer": "rental-a",
        "placement": {
          "id": "placement-run",
          "rental": "rental-a",
          "state": "scheduled",
          "after": "placement-ahead",
          "projected_start_in": "10m",
          "latest_start": "+12m",
          "schedule_version": 3
        }
      }
    }`)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := sc.World.Images["app:v1"].Layers[0].Size; got != ByteSize(1_500_000_000) {
		t.Fatalf("layer size = %d, want 1.5GB in bytes", got)
	}
	if got := sc.World.RentalSchedules[0].Running.RemainingMaxRuntime.Duration(); got != 6*time.Minute {
		t.Fatalf("remaining max runtime = %v, want 6m", got)
	}
	if got := sc.World.RentalSchedules[0].Scheduled[0].MaxRuntime.Duration(); got != 4*time.Minute {
		t.Fatalf("scheduled max runtime = %v, want 4m", got)
	}
	latestStart := sc.Expect.Placement.LatestStart.Resolve(sc.World.Start())
	if want := sc.World.Start().Add(12 * time.Minute); !latestStart.Equal(want) {
		t.Fatalf("latest start = %v, want %v", latestStart, want)
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
		"scheduled Placement without running Placement": {
			`"rentals": [{"id": "rental-a", "rate_per_hour_usd": 1.0}]`, `"rentals": [{"id": "rental-a", "rate_per_hour_usd": 1.0}], "rental_schedules": [{"rental": "rental-a", "version": 1, "scheduled": [{"placement": "p1", "run": "r1", "max_runtime": "5m"}]}]`, "require a RunningPlacement"},
		"winning offer missing from world": {`"offer": "rental-a"`, `"offer": "rental-z"`, "not in the world"},
		"unknown outcome":                  {`"outcome": "place"`, `"outcome": "defer"`, "outcome must"},
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

func TestLoadRejectsIncoherentScheduledPlacement(t *testing.T) {
	const coherent = `{
      "summary": "A scheduled placement follows the current tail.",
      "status": "target",
      "missing_capabilities": ["rental_schedule"],
      "world": {
        "rentals": [{
          "id": "rental-a",
          "rate_per_hour_usd": 1.0
        }],
        "rental_schedules": [{
          "rental": "rental-a",
          "version": 1,
          "running": {
            "placement": "placement-active",
            "run": "run-active",
            "remaining_max_runtime": "5m"
          }
        }]
      },
      "request": {"image": "app:v1", "max_runtime": "2m"},
      "expect": {
        "outcome": "place",
        "offer": "rental-a",
        "placement": {
          "id": "placement-run",
          "rental": "rental-a",
          "state": "scheduled",
          "after": "placement-active",
          "projected_start_in": "5m",
          "schedule_version": 2
        }
      }
    }`

	for name, replacement := range map[string]string{
		"predecessor":      `"after": "placement-wrong"`,
		"projected start":  `"projected_start_in": "6m"`,
		"schedule version": `"schedule_version": 3`,
	} {
		t.Run(name, func(t *testing.T) {
			from := map[string]string{
				"predecessor":      `"after": "placement-active"`,
				"projected start":  `"projected_start_in": "5m"`,
				"schedule version": `"schedule_version": 2`,
			}[name]
			if _, err := loadFixtureText(t, strings.Replace(coherent, from, replacement, 1)); err == nil {
				t.Fatalf("incoherent %s must be rejected", name)
			}
		})
	}
}

func TestLoadEnforcesCapabilityDeclarations(t *testing.T) {
	if _, err := loadFixtureText(t, strings.Replace(minimalGreenScenario,
		`"status": "green"`, `"status": "target"`, 1)); err == nil || !strings.Contains(err.Error(), "missing_capabilities") {
		t.Fatalf("target scenarios must declare missing capabilities, got %v", err)
	}
	if _, err := loadFixtureText(t, strings.Replace(minimalGreenScenario,
		`"status": "green"`, `"status": "green", "missing_capabilities": ["rental_schedule"]`, 1)); err == nil || !strings.Contains(err.Error(), "green scenarios") {
		t.Fatalf("green scenarios must not declare missing capabilities, got %v", err)
	}
	if _, err := loadFixtureText(t, strings.Replace(minimalGreenScenario,
		`"status": "green"`, `"status": "target", "missing_capabilities": ["telepathy"]`, 1)); err == nil || !strings.Contains(err.Error(), "unknown capability") {
		t.Fatalf("unknown capabilities must be rejected, got %v", err)
	}
}

func TestLoadEnforcesScheduleBounds(t *testing.T) {
	const overfull = `{
      "summary": "Five ScheduledPlacements exceed the schedule cap.",
      "status": "target",
      "missing_capabilities": ["rental_schedule"],
      "world": {
        "rentals": [{"id": "rental-a", "rate_per_hour_usd": 1.0}],
        "rental_schedules": [{
          "rental": "rental-a",
          "version": 1,
          "running": {"placement": "p0", "run": "r0", "remaining_max_runtime": "5m"},
          "scheduled": [
            {"placement": "p1", "run": "r1", "max_runtime": "5m"},
            {"placement": "p2", "run": "r2", "max_runtime": "5m"},
            {"placement": "p3", "run": "r3", "max_runtime": "5m"},
            {"placement": "p4", "run": "r4", "max_runtime": "5m"},
            {"placement": "p5", "run": "r5", "max_runtime": "5m"}
          ]
        }]
      },
      "request": {"image": "app:v1"},
      "expect": {"outcome": "fail"}
    }`
	if _, err := loadFixtureText(t, overfull); err == nil || !strings.Contains(err.Error(), "at most 4") {
		t.Fatalf("a fifth ScheduledPlacement must be rejected, got %v", err)
	}
}

func TestLoadRejectsExpectedRuntimeBeyondMaxBound(t *testing.T) {
	const optimist = `{
      "summary": "An expected runtime cannot exceed its enforced bound.",
      "status": "target",
      "missing_capabilities": ["rental_schedule"],
      "world": {
        "rentals": [{"id": "rental-a", "rate_per_hour_usd": 1.0}],
        "rental_schedules": [{
          "rental": "rental-a",
          "version": 1,
          "running": {
            "placement": "p0",
            "run": "r0",
            "remaining_max_runtime": "5m",
            "remaining_expected_runtime": "6m"
          }
        }]
      },
      "request": {"image": "app:v1"},
      "expect": {"outcome": "fail"}
    }`
	if _, err := loadFixtureText(t, optimist); err == nil || !strings.Contains(err.Error(), "within the max bound") {
		t.Fatalf("an expected runtime beyond the max bound must be rejected, got %v", err)
	}
}

func TestProjectedStartsWorkOffExpectedRuntimes(t *testing.T) {
	const p50 = `{
      "summary": "Projected starts derive from expected runtimes, not max bounds.",
      "status": "target",
      "missing_capabilities": ["rental_schedule"],
      "world": {
        "rentals": [{"id": "rental-a", "rate_per_hour_usd": 1.0}],
        "rental_schedules": [{
          "rental": "rental-a",
          "version": 1,
          "running": {
            "placement": "p0",
            "run": "r0",
            "remaining_max_runtime": "10m",
            "remaining_expected_runtime": "3m"
          },
          "scheduled": [
            {"placement": "p1", "run": "r1", "max_runtime": "10m", "expected_runtime": "2m"}
          ]
        }]
      },
      "request": {"image": "app:v1", "max_runtime": "1h"},
      "expect": {
        "outcome": "place",
        "offer": "rental-a",
        "placement": {
          "id": "p-new",
          "rental": "rental-a",
          "state": "scheduled",
          "after": "p1",
          "projected_start_in": "5m",
          "schedule_version": 2
        }
      }
    }`
	if _, err := loadFixtureText(t, p50); err != nil {
		t.Fatalf("p50-based projected start must validate: %v", err)
	}
	if _, err := loadFixtureText(t, strings.Replace(p50,
		`"projected_start_in": "5m"`, `"projected_start_in": "20m"`, 1)); err == nil || !strings.Contains(err.Error(), "expected runtimes") {
		t.Fatalf("a max-bound projected start must be rejected under p50 projection, got %v", err)
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
