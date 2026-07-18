package credential

import (
	"context"
	"database/sql"
)

type SQLiteStore struct{ db *sql.DB }

func NewSQLiteStore(ctx context.Context, db *sql.DB) (*SQLiteStore, error) {
	_, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS connection_secret (
		workspace_id  TEXT NOT NULL,
		connection_id TEXT NOT NULL,
		blob          BLOB NOT NULL,
		PRIMARY KEY (workspace_id, connection_id)
	)`)
	if err != nil {
		return nil, err
	}
	return &SQLiteStore{db: db}, nil
}

func (s *SQLiteStore) Put(ctx context.Context, ws, id string, blob []byte) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO connection_secret (workspace_id, connection_id, blob) VALUES (?, ?, ?)
		 ON CONFLICT(workspace_id, connection_id) DO UPDATE SET blob = excluded.blob`,
		ws, id, blob)
	return err
}

func (s *SQLiteStore) Get(ctx context.Context, ws, id string) ([]byte, error) {
	var blob []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT blob FROM connection_secret WHERE workspace_id = ? AND connection_id = ?`, ws, id).Scan(&blob)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	return blob, err
}

func (s *SQLiteStore) Delete(ctx context.Context, ws, id string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM connection_secret WHERE workspace_id = ? AND connection_id = ?`, ws, id)
	return err
}
