package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
	"github.com/benngarcia/mercator/internal/runprojection"
)

const createRuns = `CREATE TABLE IF NOT EXISTS runs (
	workspace_id TEXT NOT NULL,
	run_id TEXT NOT NULL,
	closed INTEGER NOT NULL,
	record_json BLOB NOT NULL,
	PRIMARY KEY(workspace_id, run_id)
)`

const runProjectionSchemaVersion = 1

func migrateRuns(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, createRuns); err != nil {
		return fmt.Errorf("migrate Run projection: %w", err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_runs_open
		ON runs(workspace_id, closed, run_id)
	`); err != nil {
		return fmt.Errorf("index Run projection: %w", err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS run_projection_metadata (
			singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
			schema_version INTEGER NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("migrate Run projection metadata: %w", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO run_projection_metadata (singleton, schema_version)
		VALUES (1, 0)
		ON CONFLICT(singleton) DO NOTHING
	`); err != nil {
		return fmt.Errorf("initialize Run projection metadata: %w", err)
	}
	return nil
}

type RunStore struct {
	db  *sql.DB
	log *WorkspaceEventLog
}

func (store *RunStore) RequiresRebuild(ctx context.Context) (bool, error) {
	var version int
	if err := store.db.QueryRowContext(ctx, `
		SELECT schema_version
		FROM run_projection_metadata
		WHERE singleton = 1
	`).Scan(&version); err != nil {
		return false, fmt.Errorf("read Run projection schema version: %w", err)
	}
	return version != runProjectionSchemaVersion, nil
}

func (store *RunStore) MarkRebuilt(ctx context.Context) error {
	if _, err := store.db.ExecContext(ctx, `
		UPDATE run_projection_metadata
		SET schema_version = ?
		WHERE singleton = 1
	`, runProjectionSchemaVersion); err != nil {
		return fmt.Errorf("record Run projection rebuild: %w", err)
	}
	return nil
}

func (store *RunStore) Append(
	ctx context.Context,
	request eventlog.AppendRequest,
	next domain.RunRecord,
) (eventlog.AppendResult, error) {
	return store.append(ctx, request, next, store.log.AppendAtomic)
}

func (store *RunStore) AppendIfWorkspaceActive(
	ctx context.Context,
	request eventlog.AppendRequest,
	next domain.RunRecord,
) (eventlog.AppendResult, error) {
	return store.append(ctx, request, next, store.log.appendIfWorkspaceActiveAtomic)
}

type atomicAppender func(
	context.Context,
	eventlog.AppendRequest,
	func(context.Context, *sql.Tx) error,
) (eventlog.AppendResult, error)

func (store *RunStore) append(
	ctx context.Context,
	request eventlog.AppendRequest,
	next domain.RunRecord,
	appendAtomic atomicAppender,
) (eventlog.AppendResult, error) {
	if request.Stream.Type != "run" ||
		request.Stream.WorkspaceID == "" ||
		request.Stream.ID == "" ||
		next.WorkspaceID != request.Stream.WorkspaceID ||
		next.ID != request.Stream.ID {
		return eventlog.AppendResult{}, fmt.Errorf("Run projection identity must match its run stream")
	}
	encoded, err := json.Marshal(next)
	if err != nil {
		return eventlog.AppendResult{}, fmt.Errorf("encode Run projection: %w", err)
	}
	return appendAtomic(ctx, request, func(ctx context.Context, tx *sql.Tx) error {
		return store.putEncodedTx(ctx, tx, next, encoded)
	})
}

func (store *RunStore) putTx(ctx context.Context, tx *sql.Tx, next domain.RunRecord) error {
	if next.WorkspaceID == "" || next.ID == "" {
		return fmt.Errorf("Run projection requires Workspace and Run identity")
	}
	encoded, err := json.Marshal(next)
	if err != nil {
		return fmt.Errorf("encode Run projection: %w", err)
	}
	return store.putEncodedTx(ctx, tx, next, encoded)
}

func (*RunStore) putEncodedTx(ctx context.Context, tx *sql.Tx, next domain.RunRecord, encoded []byte) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO runs (workspace_id, run_id, closed, record_json)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(workspace_id, run_id) DO UPDATE SET
			closed = excluded.closed,
			record_json = excluded.record_json
	`, next.WorkspaceID, next.ID, next.Closed, encoded)
	if err != nil {
		return fmt.Errorf("store Run projection: %w", err)
	}
	return nil
}

func (store *RunStore) List(
	ctx context.Context,
	workspaceID string,
	request runprojection.PageRequest,
) (runprojection.Page, error) {
	request, err := request.Validated()
	if err != nil {
		return runprojection.Page{}, err
	}
	rows, err := store.db.QueryContext(ctx, `
		SELECT record_json
		FROM runs
		WHERE workspace_id = ? AND run_id > ?
		ORDER BY run_id
		LIMIT ?
	`, workspaceID, request.After, request.Limit+1)
	if err != nil {
		return runprojection.Page{}, fmt.Errorf("list Run projection: %w", err)
	}
	defer func() { _ = rows.Close() }()

	records := make([]domain.RunRecord, 0, request.Limit+1)
	for rows.Next() {
		var encoded []byte
		if err := rows.Scan(&encoded); err != nil {
			return runprojection.Page{}, fmt.Errorf("scan Run projection: %w", err)
		}
		var record domain.RunRecord
		if err := json.Unmarshal(encoded, &record); err != nil {
			return runprojection.Page{}, fmt.Errorf("decode Run projection: %w", err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return runprojection.Page{}, fmt.Errorf("list Run projection: %w", err)
	}
	page := runprojection.Page{Records: records}
	if len(records) > request.Limit {
		page.Records = records[:request.Limit]
		page.NextCursor = page.Records[len(page.Records)-1].ID
	}
	return page, nil
}

func (store *RunStore) ListOpenIDs(ctx context.Context, workspaceID string) ([]string, error) {
	rows, err := store.db.QueryContext(ctx, `
		SELECT run_id
		FROM runs
		WHERE workspace_id = ? AND closed = 0
		ORDER BY run_id
	`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list open Run projection: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var runIDs []string
	for rows.Next() {
		var runID string
		if err := rows.Scan(&runID); err != nil {
			return nil, fmt.Errorf("scan open Run projection: %w", err)
		}
		runIDs = append(runIDs, runID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list open Run projection: %w", err)
	}
	return runIDs, nil
}

func (store *RunStore) Replace(ctx context.Context, workspaceID string, records []domain.RunRecord) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("replace Run projection: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM runs WHERE workspace_id = ?`, workspaceID); err != nil {
		return fmt.Errorf("clear Run projection: %w", err)
	}
	for _, record := range records {
		if record.WorkspaceID != workspaceID || record.ID == "" {
			return fmt.Errorf("replacement Run projection identity is invalid")
		}
		encoded, err := json.Marshal(record)
		if err != nil {
			return fmt.Errorf("encode replacement Run projection: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO runs (workspace_id, run_id, closed, record_json)
			VALUES (?, ?, ?, ?)
		`, workspaceID, record.ID, record.Closed, encoded); err != nil {
			return fmt.Errorf("replace Run projection row: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("replace Run projection: %w", err)
	}
	return nil
}
