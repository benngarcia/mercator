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
	return put(ctx, s.db, ws, id, blob)
}

func (s *SQLiteStore) PutTx(ctx context.Context, tx *sql.Tx, ws, id string, blob []byte) error {
	return put(ctx, tx, ws, id, blob)
}

func put(ctx context.Context, db executor, ws, id string, blob []byte) error {
	_, err := db.ExecContext(ctx,
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
	return deleteSecret(ctx, s.db, ws, id)
}

func (s *SQLiteStore) DeleteTx(ctx context.Context, tx *sql.Tx, ws, id string) error {
	return deleteSecret(ctx, tx, ws, id)
}

func deleteSecret(ctx context.Context, db executor, ws, id string) error {
	_, err := db.ExecContext(ctx,
		`DELETE FROM connection_secret WHERE workspace_id = ? AND connection_id = ?`, ws, id)
	return err
}

// sealedRow is one row of connection_secret carrying the blob it is about to
// be written back with.
type sealedRow struct {
	ws, id string
	blob   []byte
}

// Rekey re-seals every stored credential from the key derived from the retired
// master key to the key derived from the new one, in one transaction: a
// rotation that fails part way leaves every row readable under the key it was
// written with, so the running deployment is never half-rotated. Rows that
// already open under the new key are counted as rotated and left alone, so an
// operator who interrupted a rotation runs the command again rather than
// working out which half moved. A row neither key opens names the connection it
// belongs to and aborts, because that means the retired key supplied is not the
// key the store was written with.
//
// This is deliberately an operator action rather than a boot action. Rotating
// at startup would require the retired key to stay in the deployment's
// environment forever, which is the opposite of retiring it.
func (s *SQLiteStore) Rekey(ctx context.Context, retiredMaster, newMaster []byte) (int, error) {
	if len(retiredMaster) == 0 || len(newMaster) == 0 {
		return 0, errors.New("credential: rekey needs both the retired and the new master key")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	pending, err := rowsAwaitingKey(ctx, tx, DeriveSealKey(retiredMaster), DeriveSealKey(newMaster))
	if err != nil {
		return 0, err
	}
	for _, row := range pending {
		if err := put(ctx, tx, row.ws, row.id, row.blob); err != nil {
			return 0, err
		}
	}
	return len(pending), tx.Commit()
}

// rowsAwaitingKey reads every stored blob and answers with those that still
// need writing back, already re-sealed under next. Reading completes before any
// write begins because the store holds one SQLite connection and cannot serve a
// scan and an update at the same time.
func rowsAwaitingKey(ctx context.Context, tx *sql.Tx, retired, next []byte) ([]sealedRow, error) {
	rows, err := tx.QueryContext(ctx, `SELECT workspace_id, connection_id, blob FROM connection_secret`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pending []sealedRow
	var unopenable []error
	for rows.Next() {
		var row sealedRow
		if err := rows.Scan(&row.ws, &row.id, &row.blob); err != nil {
			return nil, err
		}
		if _, err := Open(next, row.blob); err == nil {
			continue
		}
		plain, err := Open(retired, row.blob)
		if err != nil {
			unopenable = append(unopenable,
				fmt.Errorf("credential for %s/%s opens under neither MERCATOR_SECRET_KEY_PREVIOUS nor MERCATOR_SECRET_KEY", row.ws, row.id))
			continue
		}
		resealed, err := Seal(next, plain)
		if err != nil {
			return nil, fmt.Errorf("re-seal credential for %s/%s: %w", row.ws, row.id, err)
		}
		pending = append(pending, sealedRow{ws: row.ws, id: row.id, blob: resealed})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(unopenable) > 0 {
		return nil, errors.Join(unopenable...)
	}
	return pending, nil
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
		`SELECT workspace_id, connection_id, blob FROM connection_secret`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	var pending []sealedRow
	var undecryptable []error
	for rows.Next() {
		var r sealedRow
		if err := rows.Scan(&r.ws, &r.id, &r.blob); err != nil {
			return 0, err
		}
		if _, err := Open(sealKey, r.blob); err == nil {
			continue
		}
		plain, err := Open(masterKey, r.blob)
		if err != nil {
			undecryptable = append(undecryptable,
				fmt.Errorf("credential for %s/%s cannot be decrypted with the configured MERCATOR_SECRET_KEY", r.ws, r.id))
			continue
		}
		resealed, err := Seal(sealKey, plain)
		if err != nil {
			return 0, fmt.Errorf("re-seal credential for %s/%s: %w", r.ws, r.id, err)
		}
		pending = append(pending, sealedRow{ws: r.ws, id: r.id, blob: resealed})
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(undecryptable) > 0 {
		return 0, errors.Join(undecryptable...)
	}
	for _, r := range pending {
		if err := s.Put(ctx, r.ws, r.id, r.blob); err != nil {
			return 0, err
		}
	}
	return len(pending), nil
}
