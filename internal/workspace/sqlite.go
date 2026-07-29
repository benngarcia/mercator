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

type SQLiteCatalog struct {
	db *sql.DB
}

func NewSQLiteCatalog(db *sql.DB) *SQLiteCatalog {
	return &SQLiteCatalog{db: db}
}

// Create brings a workspace into existence and makes its creator an admin of
// it in the same transaction. Nobody creates a workspace they are then a
// stranger to, so the two facts are written together or neither is.
func (c *SQLiteCatalog) Create(ctx context.Context, command Create) (Workspace, error) {
	if err := command.validate(); err != nil {
		return Workspace{}, err
	}
	founder := Membership{
		WorkspaceID: command.ID,
		Subject:     command.CreatedBy,
		Role:        RoleAdmin,
		GrantedAt:   command.CreatedAt,
	}
	if err := founder.validate(); err != nil {
		return Workspace{}, err
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return Workspace{}, fmt.Errorf("workspace: create %s: %w", command.ID, err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT INTO workspaces (
		workspace_id, display_name, created_at, created_by
	) VALUES (?, ?, ?, ?)`, command.ID, command.DisplayName, formatTime(command.CreatedAt), command.CreatedBy); err != nil {
		if isConstraintViolation(err) {
			return Workspace{}, fmt.Errorf("%w: %s", ErrAlreadyExists, command.ID)
		}
		return Workspace{}, fmt.Errorf("workspace: create %s: %w", command.ID, err)
	}
	if _, err := tx.ExecContext(ctx, insertMembership, founder.WorkspaceID, founder.Subject, string(founder.Role), formatTime(founder.GrantedAt)); err != nil {
		return Workspace{}, fmt.Errorf("workspace: grant %s admin of %s: %w", founder.Subject, command.ID, err)
	}
	if err := tx.Commit(); err != nil {
		return Workspace{}, fmt.Errorf("workspace: create %s: %w", command.ID, err)
	}
	return c.Find(ctx, command.ID)
}

// insertMembership records a standing, moving the role when the pair already
// has one. A membership states what someone may do now, so re-granting is a
// correction rather than a conflict.
const insertMembership = `INSERT INTO workspace_members (workspace_id, subject, role, granted_at)
	VALUES (?, ?, ?, ?)
	ON CONFLICT(workspace_id, subject) DO UPDATE SET role = excluded.role`

// Grant records one subject's standing in one workspace.
func (c *SQLiteCatalog) Grant(ctx context.Context, membership Membership) error {
	if err := membership.validate(); err != nil {
		return err
	}
	if _, err := c.db.ExecContext(ctx, insertMembership,
		membership.WorkspaceID, membership.Subject, string(membership.Role), formatTime(membership.GrantedAt),
	); err != nil {
		return fmt.Errorf("workspace: grant %s in %s: %w", membership.Subject, membership.WorkspaceID, err)
	}
	return nil
}

// MembershipOf answers what a subject may do in a workspace, and answers
// ErrNotMember when the answer is nothing.
func (c *SQLiteCatalog) MembershipOf(ctx context.Context, workspaceID, subject string) (Membership, error) {
	var membership Membership
	var grantedAt string
	err := c.db.QueryRowContext(ctx, `SELECT workspace_id, subject, role, granted_at
		FROM workspace_members WHERE workspace_id = ? AND subject = ?`, workspaceID, subject,
	).Scan(&membership.WorkspaceID, &membership.Subject, &membership.Role, &grantedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Membership{}, fmt.Errorf("%w: %s in %s", ErrNotMember, subject, workspaceID)
		}
		return Membership{}, fmt.Errorf("workspace: read membership of %s in %s: %w", subject, workspaceID, err)
	}
	parsed, err := time.Parse(time.RFC3339Nano, grantedAt)
	if err != nil {
		return Membership{}, fmt.Errorf("workspace: parse granted_at for %s in %s: %w", subject, workspaceID, err)
	}
	membership.GrantedAt = parsed.UTC()
	return membership, nil
}

func (c *SQLiteCatalog) List(ctx context.Context, options ListOptions) ([]Workspace, error) {
	query := `SELECT workspaces.workspace_id, workspaces.display_name, workspaces.created_at,
		workspaces.created_by, workspaces.archived_at
		FROM workspaces`
	arguments := []any{}
	if options.Subject != "" {
		query += ` JOIN workspace_members
			ON workspace_members.workspace_id = workspaces.workspace_id
			AND workspace_members.subject = ?`
		arguments = append(arguments, options.Subject)
	}
	if !options.IncludeArchived {
		query += ` WHERE workspaces.archived_at IS NULL`
	}
	query += ` ORDER BY workspaces.archived_at IS NOT NULL, workspaces.display_name COLLATE NOCASE, workspaces.workspace_id`
	rows, err := c.db.QueryContext(ctx, query, arguments...)
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

func isConstraintViolation(err error) bool {
	var sqliteError *sqlite.Error
	return errors.As(err, &sqliteError) && sqliteError.Code()&0xff == sqlite3.SQLITE_CONSTRAINT
}
