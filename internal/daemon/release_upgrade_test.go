package daemon_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/broker"
	"github.com/benngarcia/mercator/internal/daemon"
	"github.com/benngarcia/mercator/internal/domain"
)

const releaseUpgradeToken = "release-upgrade-test-token"

type releaseUpgradeManifest struct {
	WorkspaceID        string                  `json:"workspace_id"`
	RunID              string                  `json:"run_id"`
	Sanitized          bool                    `json:"sanitized"`
	ReleaseGateVersion string                  `json:"release_gate_version"`
	Lineage            []releaseUpgradeFixture `json:"lineage"`
}

type releaseUpgradeFixture struct {
	Version           string `json:"version"`
	Fixture           string `json:"fixture"`
	DecisionEventType string `json:"decision_event_type"`
	BookingJSONType   string `json:"booking_json_type"`
}

type migrationSnapshot struct {
	EventType string
	DataJSON  string
}

func TestPreviousReleaseStateReplaysThroughProductionDaemon(t *testing.T) {
	manifest := readReleaseUpgradeManifest(t)
	t.Setenv("PATH", t.TempDir())

	for fixtureCount, fixture := range manifest.Lineage {
		fixtureCount := fixtureCount + 1
		t.Run(fixture.Version, func(t *testing.T) {
			dsn := arrangeReleaseUpgradeState(t, manifest, fixtureCount)
			assertArrangedReleaseState(t, dsn, fixture)

			bootAndReplayReleaseState(t, dsn, manifest)
			first := readMigrationSnapshot(t, dsn)

			bootAndReplayReleaseState(t, dsn, manifest)
			second := readMigrationSnapshot(t, dsn)

			if first != second {
				t.Fatalf("second startup changed migrated decision:\nfirst:  %+v\nsecond: %+v", first, second)
			}
		})
	}
}

func readReleaseUpgradeManifest(t *testing.T) releaseUpgradeManifest {
	t.Helper()
	contents, err := os.ReadFile(releaseUpgradePath("manifest.json"))
	if err != nil {
		t.Fatalf("read release upgrade manifest: %v", err)
	}
	var manifest releaseUpgradeManifest
	if err := json.Unmarshal(contents, &manifest); err != nil {
		t.Fatalf("decode release upgrade manifest: %v", err)
	}
	if manifest.WorkspaceID == "" || manifest.RunID == "" || !manifest.Sanitized || manifest.ReleaseGateVersion == "" || len(manifest.Lineage) == 0 {
		t.Fatalf("incomplete release upgrade manifest: %+v", manifest)
	}
	if last := manifest.Lineage[len(manifest.Lineage)-1].Version; last != manifest.ReleaseGateVersion {
		t.Fatalf("release gate version = %s, want final fixture %s", manifest.ReleaseGateVersion, last)
	}
	return manifest
}

func arrangeReleaseUpgradeState(t *testing.T, manifest releaseUpgradeManifest, fixtureCount int) string {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "mercator.db")
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open fixture database: %v", err)
	}
	for _, fixture := range manifest.Lineage[:fixtureCount] {
		contents, err := os.ReadFile(releaseUpgradePath(fixture.Fixture))
		if err != nil {
			_ = db.Close()
			t.Fatalf("read %s fixture: %v", fixture.Version, err)
		}
		if _, err := db.ExecContext(t.Context(), string(contents)); err != nil {
			_ = db.Close()
			t.Fatalf("apply %s fixture: %v", fixture.Version, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture database: %v", err)
	}
	return dsn
}

func assertArrangedReleaseState(t *testing.T, dsn string, fixture releaseUpgradeFixture) {
	t.Helper()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open arranged database: %v", err)
	}
	defer db.Close()
	var eventType string
	var bookingJSONType sql.NullString
	err = db.QueryRowContext(t.Context(), `
		SELECT event_type, json_type(data_json, '$.decision.booking')
		FROM events
		WHERE stream_type = 'run'
		  AND stream_id = 'run_release_upgrade_fixture'
		  AND event_type LIKE 'compute.run.%_decided.v1'
	`).Scan(&eventType, &bookingJSONType)
	if err != nil {
		t.Fatalf("read %s arranged decision: %v", fixture.Version, err)
	}
	if eventType != fixture.DecisionEventType {
		t.Fatalf("%s decision event = %q, want %q", fixture.Version, eventType, fixture.DecisionEventType)
	}
	bookingType := "absent"
	if bookingJSONType.Valid {
		bookingType = bookingJSONType.String
	}
	if bookingType != fixture.BookingJSONType {
		t.Fatalf("%s Booking JSON type = %q, want %q", fixture.Version, bookingType, fixture.BookingJSONType)
	}
}

func bootAndReplayReleaseState(t *testing.T, dsn string, manifest releaseUpgradeManifest) {
	t.Helper()
	runtime, err := daemon.New(t.Context(), daemon.Config{
		SQLiteDSN:       dsn,
		OperatorToken:   releaseUpgradeToken,
		ProviderFactory: broker.NewFactory(),
	})
	if err != nil {
		t.Fatalf("boot production daemon: %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = runtime.Shutdown(t.Context())
		t.Fatalf("listen: %v", err)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- runtime.Serve(listener) }()

	baseURL := "http://" + listener.Addr().String()
	assertReleaseReadiness(t, baseURL)
	assertAuthenticatedRunReplay(t, baseURL, manifest)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runtime.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown production daemon: %v", err)
	}
	if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("serve returned: %v", err)
	}
}

func assertReleaseReadiness(t *testing.T, baseURL string) {
	t.Helper()
	response, err := http.Get(baseURL + "/health/ready")
	if err != nil {
		t.Fatalf("get readiness: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("readiness status = %d, want 200", response.StatusCode)
	}
}

func assertAuthenticatedRunReplay(t *testing.T, baseURL string, manifest releaseUpgradeManifest) {
	t.Helper()
	runsURL := baseURL + "/v1/runs?workspace_id=" + url.QueryEscape(manifest.WorkspaceID)
	unauthorized, err := http.Get(runsURL)
	if err != nil {
		t.Fatalf("get runs without token: %v", err)
	}
	_ = unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated run list status = %d, want 401", unauthorized.StatusCode)
	}

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, runsURL, nil)
	if err != nil {
		t.Fatalf("build authenticated run list request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+releaseUpgradeToken)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("get authenticated run list: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("authenticated run list status = %d, want 200", response.StatusCode)
	}
	var payload struct {
		Runs []domain.RunRecord `json:"runs"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode authenticated run list: %v", err)
	}
	if len(payload.Runs) != 1 {
		t.Fatalf("runs = %+v, want one replayed run", payload.Runs)
	}
	run := payload.Runs[0]
	if run.ID != manifest.RunID || !run.Closed || run.Outcome != domain.RunOutcomeSucceeded {
		t.Fatalf("replayed run = %+v, want closed succeeded %s", run, manifest.RunID)
	}
}

func readMigrationSnapshot(t *testing.T, dsn string) migrationSnapshot {
	t.Helper()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open migrated database: %v", err)
	}
	defer db.Close()
	var count int
	var snapshot migrationSnapshot
	err = db.QueryRowContext(t.Context(), `
		SELECT COUNT(*), MIN(event_type), MIN(CAST(data_json AS TEXT))
		FROM events
		WHERE stream_type = 'run'
		  AND stream_id = 'run_release_upgrade_fixture'
		  AND event_type IN (
		    'compute.run.placement_decided.v1',
		    'compute.run.booking_decided.v1'
		  )
	`).Scan(&count, &snapshot.EventType, &snapshot.DataJSON)
	if err != nil {
		t.Fatalf("read migrated decision: %v", err)
	}
	if count != 1 || snapshot.EventType != "compute.run.booking_decided.v1" {
		t.Fatalf("migrated decisions = %d %q, want one booking decision", count, snapshot.EventType)
	}
	var payload struct {
		Decision struct {
			Booking *domain.Booking `json:"booking"`
		} `json:"decision"`
	}
	if err := json.Unmarshal([]byte(snapshot.DataJSON), &payload); err != nil {
		t.Fatalf("decode migrated decision: %v", err)
	}
	if payload.Decision.Booking == nil {
		t.Fatal("migrated decision has no Booking")
	}
	if payload.Decision.Booking.ID != "booking_legacy_dec_7f5082a93b5c0d540" {
		t.Fatalf("migrated Booking = %+v", payload.Decision.Booking)
	}
	return snapshot
}

func releaseUpgradePath(name string) string {
	return filepath.Join("testdata", "release-upgrades", name)
}
