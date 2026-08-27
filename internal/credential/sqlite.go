package credential

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type SQLiteStore struct{ db *sql.DB }

type executor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func NewSQLiteStore(ctx context.Context, db *sql.DB) (*SQLiteStore, error) {
	_, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS connection_secret (
		connection_id TEXT PRIMARY KEY,
		blob          BLOB NOT NULL,
		CHECK (length(connection_id) > 0)
	)`)
	if err != nil {
		return nil, err
	}
	return &SQLiteStore{db: db}, nil
}

func (s *SQLiteStore) Put(ctx context.Context, id string, blob []byte) error {
	return put(ctx, s.db, id, blob)
}

func (s *SQLiteStore) PutTx(ctx context.Context, tx *sql.Tx, id string, blob []byte) error {
	return put(ctx, tx, id, blob)
}

func put(ctx context.Context, db executor, id string, blob []byte) error {
	_, err := db.ExecContext(ctx,
		`INSERT INTO connection_secret (connection_id, blob) VALUES (?, ?)
		 ON CONFLICT(connection_id) DO UPDATE SET blob = excluded.blob`,
		id, blob)
	return err
}

func (s *SQLiteStore) Get(ctx context.Context, id string) ([]byte, error) {
	var blob []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT blob FROM connection_secret WHERE connection_id = ?`, id).Scan(&blob)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	return blob, err
}

func (s *SQLiteStore) Delete(ctx context.Context, id string) error {
	return deleteSecret(ctx, s.db, id)
}

func (s *SQLiteStore) DeleteTx(ctx context.Context, tx *sql.Tx, id string) error {
	return deleteSecret(ctx, tx, id)
}

func deleteSecret(ctx context.Context, db executor, id string) error {
	_, err := db.ExecContext(ctx,
		`DELETE FROM connection_secret WHERE connection_id = ?`, id)
	return err
}

// MigrateSealKey re-seals every stored blob under the key derived from
// masterKey. Rows already sealed under the derived key are left alone; rows
// sealed under the raw master key (the pre-HKDF format) are re-sealed. A row
// neither key can open means the configured MERCATOR_SECRET_KEY is not the key
// the store was written with — that is a startup-fatal condition for the
// caller, reported per row so the operator sees exactly which connections are
// affected. Returns how many rows were re-sealed.
func (s *SQLiteStore) MigrateSealKey(ctx context.Context, masterKey []byte) (int, error) {
	if len(masterKey) == 0 {
		return 0, nil
	}
	sealKey := DeriveSealKey(masterKey)
	rows, err := s.db.QueryContext(ctx,
		`SELECT connection_id, blob FROM connection_secret`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	type reseal struct {
		id   string
		blob []byte
	}
	var pending []reseal
	var undecryptable []error
	for rows.Next() {
		var r reseal
		if err := rows.Scan(&r.id, &r.blob); err != nil {
			return 0, err
		}
		if _, err := Open(sealKey, r.blob); err == nil {
			continue
		}
		plain, err := Open(masterKey, r.blob)
		if err != nil {
			undecryptable = append(undecryptable,
				fmt.Errorf("credential for %s cannot be decrypted with the configured MERCATOR_SECRET_KEY", r.id))
			continue
		}
		resealed, err := Seal(sealKey, plain)
		if err != nil {
			return 0, fmt.Errorf("re-seal credential for %s: %w", r.id, err)
		}
		pending = append(pending, reseal{id: r.id, blob: resealed})
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(undecryptable) > 0 {
		return 0, errors.Join(undecryptable...)
	}
	for _, r := range pending {
		if err := s.Put(ctx, r.id, r.blob); err != nil {
			return 0, err
		}
	}
	return len(pending), nil
}
