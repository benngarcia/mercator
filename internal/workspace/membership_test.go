package workspace_test

import (
	"context"
	"errors"
	"testing"

	sqlitestore "github.com/benngarcia/mercator/internal/storage/sqlite"
	"github.com/benngarcia/mercator/internal/workspace"
)

func TestOpenBackfillsOneAdminPerWorkspaceFromItsCreator(t *testing.T) {
	// Arrange
	ctx := context.Background()
	db := openFixtureDatabase(t, "workspaces_without_members.sql")

	// Act
	store, err := sqlitestore.New(ctx, db)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	catalog := store.Workspaces()

	// Assert
	want := map[string]string{
		"ws_research": "ana@example.com",
		"ws_platform": "brij@example.com",
		"ws_retired":  "ana@example.com",
		"staging":     workspace.MigrationPrincipal,
	}
	for workspaceID, subject := range want {
		membership, err := catalog.MembershipOf(ctx, workspaceID, subject)
		if err != nil {
			t.Fatalf("membership of %s in %s: %v", subject, workspaceID, err)
		}
		if membership.Role != workspace.RoleAdmin {
			t.Errorf("%s in %s has role %q, want %q", subject, workspaceID, membership.Role, workspace.RoleAdmin)
		}
		if membership.GrantedAt.IsZero() {
			t.Errorf("%s in %s was granted at no time", subject, workspaceID)
		}
	}
	if _, err := catalog.MembershipOf(ctx, "ws_research", "brij@example.com"); !errors.Is(err, workspace.ErrNotMember) {
		t.Fatalf("a stranger's membership error = %v, want ErrNotMember", err)
	}
}

func TestTheMemberBackfillIsIdempotentAndKeepsLaterGrants(t *testing.T) {
	// Arrange
	ctx := context.Background()
	db := openFixtureDatabase(t, "workspaces_without_members.sql")
	store, err := sqlitestore.New(ctx, db)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	// The later grant that constrains this is one on the row the backfill would
	// write again: ws_research was created by Ana, so a re-run inserts her as its
	// admin. Standing her down to member is the change a second backfill must
	// leave alone. Granting a stranger instead proves nothing, because the
	// backfill never touches that row.
	for _, later := range []workspace.Membership{
		{WorkspaceID: "ws_research", Subject: "ana@example.com", Role: workspace.RoleMember, GrantedAt: mustTime(t, "2026-07-01T09:00:00Z")},
		{WorkspaceID: "ws_research", Subject: "brij@example.com", Role: workspace.RoleMember, GrantedAt: mustTime(t, "2026-07-01T09:00:00Z")},
	} {
		if err := store.Workspaces().Grant(ctx, later); err != nil {
			t.Fatalf("grant membership: %v", err)
		}
	}

	// Act
	reopened, err := sqlitestore.New(ctx, db)
	if err != nil {
		t.Fatalf("reopen storage: %v", err)
	}

	// Assert
	for _, subject := range []string{"ana@example.com", "brij@example.com"} {
		membership, err := reopened.Workspaces().MembershipOf(ctx, "ws_research", subject)
		if err != nil {
			t.Fatalf("membership of %s after reopen: %v", subject, err)
		}
		if membership.Role != workspace.RoleMember {
			t.Fatalf("role of %s after reopen = %q, want %q", subject, membership.Role, workspace.RoleMember)
		}
	}
}

func TestListingForASubjectAnswersOnlyTheirWorkspaces(t *testing.T) {
	// Arrange
	ctx := context.Background()
	db := openFixtureDatabase(t, "workspaces_without_members.sql")
	store, err := sqlitestore.New(ctx, db)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	catalog := store.Workspaces()

	// Act
	anas, err := catalog.List(ctx, workspace.ListOptions{Subject: "ana@example.com"})
	if err != nil {
		t.Fatalf("list for subject: %v", err)
	}
	everything, err := catalog.List(ctx, workspace.ListOptions{})
	if err != nil {
		t.Fatalf("list everything: %v", err)
	}

	// Assert
	if len(anas) != 1 || anas[0].ID != "ws_research" {
		t.Fatalf("Ana's active workspaces = %+v, want only ws_research", anas)
	}
	if len(everything) != 3 {
		t.Fatalf("the unscoped listing returned %d workspaces, want 3", len(everything))
	}
}

func TestCreatingAWorkspaceMakesTheCreatorItsAdmin(t *testing.T) {
	// Arrange
	ctx := context.Background()
	db := openFixtureDatabase(t, "workspaces_without_members.sql")
	store, err := sqlitestore.New(ctx, db)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	catalog := store.Workspaces()

	// Act
	created, err := catalog.Create(ctx, workspace.Create{
		ID:          "ws_new",
		DisplayName: "New",
		CreatedAt:   mustTime(t, "2026-07-05T09:00:00Z"),
		CreatedBy:   "cleo@example.com",
	})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}

	// Assert
	membership, err := catalog.MembershipOf(ctx, created.ID, "cleo@example.com")
	if err != nil {
		t.Fatalf("the creator is not a member of what they created: %v", err)
	}
	if membership.Role != workspace.RoleAdmin {
		t.Fatalf("creator role = %q, want %q", membership.Role, workspace.RoleAdmin)
	}
}
