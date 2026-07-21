package workspace_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/workspace"
	_ "modernc.org/sqlite"
)

func TestOpenMigratesLegacyWorkspacePartitions(t *testing.T) {
	ctx := context.Background()
	db := openFixtureDatabase(t, "legacy_partitions.sql")

	catalog, err := workspace.NewSQLiteCatalog(ctx, db)
	if err != nil {
		t.Fatalf("open workspace catalog: %v", err)
	}

	workspaces, err := catalog.List(ctx, workspace.ListOptions{IncludeArchived: true})
	if err != nil {
		t.Fatalf("list workspaces: %v", err)
	}
	want := []workspace.Workspace{
		{ID: "staging", DisplayName: "staging", CreatedAt: mustTime(t, "2026-07-16T08:15:00Z"), CreatedBy: workspace.MigrationPrincipal},
		{ID: "staging-experiments", DisplayName: "staging-experiments", CreatedAt: mustTime(t, "2026-07-17T09:30:00Z"), CreatedBy: workspace.MigrationPrincipal},
	}
	assertWorkspaces(t, workspaces, want)
}

func TestOpenKeepsWorkspaceMigrationIdempotent(t *testing.T) {
	ctx := context.Background()
	db := openFixtureDatabase(t, "legacy_partitions.sql")
	catalog, err := workspace.NewSQLiteCatalog(ctx, db)
	if err != nil {
		t.Fatalf("open workspace catalog: %v", err)
	}
	archivedAt := mustTime(t, "2026-07-20T12:00:00Z")
	archived, err := catalog.Archive(ctx, "staging", archivedAt)
	if err != nil {
		t.Fatalf("archive workspace: %v", err)
	}

	reopened, err := workspace.NewSQLiteCatalog(ctx, db)
	if err != nil {
		t.Fatalf("reopen workspace catalog: %v", err)
	}
	workspaces, err := reopened.List(ctx, workspace.ListOptions{IncludeArchived: true})
	if err != nil {
		t.Fatalf("list workspaces: %v", err)
	}
	if len(workspaces) != 2 {
		t.Fatalf("workspace count = %d, want 2", len(workspaces))
	}
	if workspaces[1].ID != archived.ID || workspaces[1].ArchivedAt == nil || !workspaces[1].ArchivedAt.Equal(archivedAt) {
		t.Fatalf("reopened archived workspace = %+v, want archive at %s", workspaces[1], archivedAt)
	}
}

func TestWorkspaceCatalogCreatesListsAndArchives(t *testing.T) {
	ctx := context.Background()
	db := openFixtureDatabase(t, "legacy_partitions.sql")
	catalog, err := workspace.NewSQLiteCatalog(ctx, db)
	if err != nil {
		t.Fatalf("open workspace catalog: %v", err)
	}
	createdAt := mustTime(t, "2026-07-20T10:00:00Z")

	created, err := catalog.Create(ctx, workspace.Create{
		ID: "production", DisplayName: "Production", CreatedAt: createdAt, CreatedBy: "operator@example.com",
	})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	archivedAt := mustTime(t, "2026-07-20T11:00:00Z")
	archived, err := catalog.Archive(ctx, created.ID, archivedAt)
	if err != nil {
		t.Fatalf("archive workspace: %v", err)
	}
	repeated, err := catalog.Archive(ctx, created.ID, archivedAt.Add(time.Hour))
	if err != nil {
		t.Fatalf("repeat archive: %v", err)
	}

	active, err := catalog.List(ctx, workspace.ListOptions{})
	if err != nil {
		t.Fatalf("list active workspaces: %v", err)
	}
	for _, item := range active {
		if item.ID == created.ID {
			t.Fatalf("archived workspace appeared in active list: %+v", item)
		}
	}
	if archived.ArchivedAt == nil || repeated.ArchivedAt == nil || !repeated.ArchivedAt.Equal(*archived.ArchivedAt) {
		t.Fatalf("repeat archive changed timestamp: first=%+v repeat=%+v", archived, repeated)
	}
	if err := catalog.RequireActive(ctx, created.ID); !errors.Is(err, workspace.ErrArchived) {
		t.Fatalf("require archived workspace error = %v, want workspace.ErrArchived", err)
	}
	if err := catalog.RequireActive(ctx, "missing"); !errors.Is(err, workspace.ErrNotFound) {
		t.Fatalf("require missing workspace error = %v, want workspace.ErrNotFound", err)
	}
}

func openFixtureDatabase(t *testing.T, fixture string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "mercator.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	schema, err := os.ReadFile(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatalf("read fixture %s: %v", fixture, err)
	}
	if _, err := db.ExecContext(context.Background(), string(schema)); err != nil {
		t.Fatalf("load fixture %s: %v", fixture, err)
	}
	return db
}

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatalf("parse time %q: %v", value, err)
	}
	return parsed
}

func assertWorkspaces(t *testing.T, got, want []workspace.Workspace) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("workspace count = %d, want %d: %+v", len(got), len(want), got)
	}
	for index := range want {
		if got[index].ID != want[index].ID || got[index].DisplayName != want[index].DisplayName ||
			!got[index].CreatedAt.Equal(want[index].CreatedAt) || got[index].CreatedBy != want[index].CreatedBy ||
			got[index].ArchivedAt != nil {
			t.Errorf("workspace[%d] = %+v, want %+v", index, got[index], want[index])
		}
	}
}
