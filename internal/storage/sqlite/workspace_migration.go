package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/benngarcia/mercator/internal/workspace"
)

var workspaceMigration = []string{
	`CREATE TABLE IF NOT EXISTS workspaces (
		workspace_id TEXT PRIMARY KEY,
		display_name TEXT NOT NULL CHECK (length(trim(display_name)) > 0),
		created_at TEXT NOT NULL,
		created_by TEXT NOT NULL CHECK (length(trim(created_by)) > 0),
		archived_at TEXT
	)`,
	`INSERT INTO workspaces (workspace_id, display_name, created_at, created_by)
	 SELECT workspace_id, workspace_id, MIN(occurred_at), '` + workspace.MigrationPrincipal + `'
	 FROM events
	 GROUP BY workspace_id
	 ON CONFLICT(workspace_id) DO NOTHING`,
	`DROP TRIGGER IF EXISTS events_require_workspace`,
}

func migrateWorkspaces(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite storage: begin workspace migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, statement := range workspaceMigration {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("sqlite storage: migrate workspaces: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite storage: commit workspace migration: %w", err)
	}
	return nil
}
