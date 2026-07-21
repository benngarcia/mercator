package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

const missingWorkspaceConstraint = "MERCATOR_WORKSPACE_NOT_FOUND"

type SQLiteCatalog struct {
	db *sql.DB
}

func NewSQLiteCatalog(ctx context.Context, db *sql.DB) (*SQLiteCatalog, error) {
	if db == nil {
		return nil, fmt.Errorf("workspace: sqlite database is required")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("workspace: begin migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, statement := range workspaceMigration {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return nil, fmt.Errorf("workspace: migrate sqlite catalog: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("workspace: commit migration: %w", err)
	}
	return &SQLiteCatalog{db: db}, nil
}

var workspaceMigration = []string{
	`CREATE TABLE IF NOT EXISTS workspaces (
		workspace_id TEXT PRIMARY KEY,
		display_name TEXT NOT NULL CHECK (length(trim(display_name)) > 0),
		created_at TEXT NOT NULL,
		created_by TEXT NOT NULL CHECK (length(trim(created_by)) > 0),
		archived_at TEXT
	)`,
	`INSERT INTO workspaces (workspace_id, display_name, created_at, created_by)
	 SELECT workspace_id, workspace_id, MIN(occurred_at), '` + MigrationPrincipal + `'
	 FROM events
	 GROUP BY workspace_id
	 ON CONFLICT(workspace_id) DO NOTHING`,
	`CREATE TRIGGER IF NOT EXISTS events_require_workspace
	 BEFORE INSERT ON events
	 WHEN NOT EXISTS (
		SELECT 1 FROM workspaces WHERE workspace_id = NEW.workspace_id
	 )
	 BEGIN
		SELECT RAISE(ABORT, '` + missingWorkspaceConstraint + `');
	 END`,
}

func (c *SQLiteCatalog) Create(ctx context.Context, command Create) (Workspace, error) {
	if err := command.validate(); err != nil {
		return Workspace{}, err
	}
	_, err := c.db.ExecContext(ctx, `INSERT INTO workspaces (
		workspace_id, display_name, created_at, created_by
	) VALUES (?, ?, ?, ?)`, command.ID, command.DisplayName, formatTime(command.CreatedAt), command.CreatedBy)
	if err != nil {
		if isConstraintViolation(err) {
			return Workspace{}, fmt.Errorf("%w: %s", ErrAlreadyExists, command.ID)
		}
		return Workspace{}, fmt.Errorf("workspace: create %s: %w", command.ID, err)
	}
	return c.Find(ctx, command.ID)
}

func (c *SQLiteCatalog) List(ctx context.Context, options ListOptions) ([]Workspace, error) {
	query := `SELECT workspace_id, display_name, created_at, created_by, archived_at
		FROM workspaces`
	if !options.IncludeArchived {
		query += ` WHERE archived_at IS NULL`
	}
	query += ` ORDER BY archived_at IS NOT NULL, display_name COLLATE NOCASE, workspace_id`
	rows, err := c.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("workspace: list: %w", err)
	}
	defer rows.Close()
	var workspaces []Workspace
	for rows.Next() {
		item, err := scanWorkspace(rows)
		if err != nil {
			return nil, err
		}
		workspaces = append(workspaces, item)
	}
	return workspaces, rows.Err()
}

func (c *SQLiteCatalog) Find(ctx context.Context, id string) (Workspace, error) {
	return scanWorkspace(c.db.QueryRowContext(ctx, `SELECT workspace_id, display_name, created_at, created_by, archived_at
		FROM workspaces WHERE workspace_id = ?`, id))
}

func (c *SQLiteCatalog) RequireActive(ctx context.Context, id string) error {
	item, err := c.Find(ctx, id)
	if err != nil {
		return err
	}
	if item.ArchivedAt != nil {
		return fmt.Errorf("%w: %s", ErrArchived, id)
	}
	return nil
}

func (c *SQLiteCatalog) Archive(ctx context.Context, id string, at time.Time) (Workspace, error) {
	if strings.TrimSpace(id) == "" {
		return Workspace{}, fmt.Errorf("workspace: workspace_id is required")
	}
	if at.IsZero() {
		return Workspace{}, fmt.Errorf("workspace: archived_at is required")
	}
	result, err := c.db.ExecContext(ctx, `UPDATE workspaces
		SET archived_at = COALESCE(archived_at, ?)
		WHERE workspace_id = ?`, formatTime(at), id)
	if err != nil {
		return Workspace{}, fmt.Errorf("workspace: archive %s: %w", id, err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return Workspace{}, fmt.Errorf("workspace: archive %s result: %w", id, err)
	}
	if changed == 0 {
		return Workspace{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return c.Find(ctx, id)
}

type scanner interface {
	Scan(...any) error
}

func scanWorkspace(row scanner) (Workspace, error) {
	var item Workspace
	var createdAt string
	var archivedAt sql.NullString
	if err := row.Scan(&item.ID, &item.DisplayName, &createdAt, &item.CreatedBy, &archivedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Workspace{}, ErrNotFound
		}
		return Workspace{}, fmt.Errorf("workspace: scan: %w", err)
	}
	parsedCreatedAt, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return Workspace{}, fmt.Errorf("workspace: parse created_at for %s: %w", item.ID, err)
	}
	item.CreatedAt = parsedCreatedAt.UTC()
	if archivedAt.Valid {
		parsedArchivedAt, err := time.Parse(time.RFC3339Nano, archivedAt.String)
		if err != nil {
			return Workspace{}, fmt.Errorf("workspace: parse archived_at for %s: %w", item.ID, err)
		}
		parsedArchivedAt = parsedArchivedAt.UTC()
		item.ArchivedAt = &parsedArchivedAt
	}
	return item, nil
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func IsNotFoundConstraint(err error) bool {
	var sqliteError *sqlite.Error
	return errors.As(err, &sqliteError) &&
		sqliteError.Code() == sqlite3.SQLITE_CONSTRAINT_TRIGGER &&
		strings.Contains(err.Error(), missingWorkspaceConstraint)
}

func isConstraintViolation(err error) bool {
	var sqliteError *sqlite.Error
	return errors.As(err, &sqliteError) && sqliteError.Code()&0xff == sqlite3.SQLITE_CONSTRAINT
}
