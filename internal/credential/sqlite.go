package credential

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/benngarcia/mercator/internal/sqliteutil"
)

type SQLiteStore struct{ db *sql.DB }

const createConnectionSecrets = `CREATE TABLE IF NOT EXISTS connection_secret (
	connection_id TEXT PRIMARY KEY,
	blob          BLOB NOT NULL
)`

type executor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func NewSQLiteStore(ctx context.Context, db *sql.DB) (*SQLiteStore, error) {
	if err := flattenConnectionSecrets(ctx, db); err != nil {
		return nil, err
	}
	_, err := db.ExecContext(ctx, createConnectionSecrets)
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

// sealedRow is one row of connection_secret carrying the blob it is about to
// be written back with.
type sealedRow struct {
	id   string
	blob []byte
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
		if err := put(ctx, tx, row.id, row.blob); err != nil {
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
	rows, err := tx.QueryContext(ctx, `SELECT connection_id, blob FROM connection_secret`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pending []sealedRow
	var unopenable []error
	for rows.Next() {
		var row sealedRow
		if err := rows.Scan(&row.id, &row.blob); err != nil {
			return nil, err
		}
		if _, err := Open(next, row.blob); err == nil {
			continue
		}
		plain, err := Open(retired, row.blob)
		if err != nil {
			unopenable = append(unopenable,
				fmt.Errorf("credential for %s opens under neither MERCATOR_SECRET_KEY_PREVIOUS nor MERCATOR_SECRET_KEY", row.id))
			continue
		}
		resealed, err := Seal(next, plain)
		if err != nil {
			return nil, fmt.Errorf("re-seal credential for %s: %w", row.id, err)
		}
		pending = append(pending, sealedRow{id: row.id, blob: resealed})
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
		`SELECT connection_id, blob FROM connection_secret`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	var pending []sealedRow
	var undecryptable []error
	for rows.Next() {
		var r sealedRow
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
		pending = append(pending, sealedRow{id: r.id, blob: resealed})
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

func flattenConnectionSecrets(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := FlattenWorkspacePartitions(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

// FlattenWorkspacePartitions removes the legacy credential partition inside
// the caller's transaction. It is exported only for the SQLite storage owner,
// which combines every partition removal into one atomic startup migration.
func FlattenWorkspacePartitions(ctx context.Context, tx *sql.Tx) error {
	partitioned, err := sqliteutil.HasColumn(ctx, tx, "connection_secret", "workspace_id")
	if err != nil {
		return err
	}
	if !partitioned {
		return nil
	}

	var collision string
	err = tx.QueryRowContext(ctx, `SELECT connection_id FROM connection_secret GROUP BY connection_id HAVING COUNT(*) > 1 LIMIT 1`).Scan(&collision)
	if err == nil {
		return fmt.Errorf("credential: workspace migration cannot flatten duplicate connection %q", collision)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	for _, statement := range []string{
		`ALTER TABLE connection_secret RENAME TO connection_secret_workspace_legacy`,
		createConnectionSecrets,
		`INSERT INTO connection_secret (connection_id, blob) SELECT connection_id, blob FROM connection_secret_workspace_legacy`,
		`DROP TABLE connection_secret_workspace_legacy`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("credential: flatten workspace partitions: %w", err)
		}
	}
	return nil
}
