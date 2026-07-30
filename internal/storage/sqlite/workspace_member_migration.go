package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/benngarcia/mercator/internal/workspace"
)

// A workspace used to be a partition key and nothing else: naming one in a
// request was the whole of the authority needed to read and write inside it.
// It is a tenancy boundary now, and membership is what the API checks, so a
// database whose workspaces have no members is a database whose humans can
// reach nothing.
//
// The backfill answers that from the one fact the old schema already recorded
// about authority: who created the workspace. They become its admin. A
// workspace that was itself backfilled from event history has
// workspace.MigrationPrincipal as its creator, so its admin is a machine
// principal and no human is a member of it. That is the honest answer rather
// than a convenient one: nothing in the log says which person owned a partition
// key, and inventing an answer would hand a tenant to whoever happened to
// appear first in the events.
var workspaceMemberMigration = []string{
	`CREATE TABLE IF NOT EXISTS workspace_members (
		workspace_id TEXT NOT NULL,
		subject TEXT NOT NULL CHECK (length(trim(subject)) > 0),
		role TEXT NOT NULL CHECK (role IN ('` + string(workspace.RoleAdmin) + `', '` + string(workspace.RoleMember) + `')),
		granted_at TEXT NOT NULL,
		PRIMARY KEY (workspace_id, subject)
	)`,
	`INSERT INTO workspace_members (workspace_id, subject, role, granted_at)
	 SELECT workspace_id, created_by, '` + string(workspace.RoleAdmin) + `', created_at
	 FROM workspaces
	 WHERE true
	 ON CONFLICT(workspace_id, subject) DO NOTHING`,
}

func migrateWorkspaceMembers(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite storage: begin workspace member migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, statement := range workspaceMemberMigration {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("sqlite storage: migrate workspace members: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite storage: commit workspace member migration: %w", err)
	}
	return nil
}
