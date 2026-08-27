package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/benngarcia/mercator/internal/connection"
	"github.com/benngarcia/mercator/internal/credential"
	"github.com/benngarcia/mercator/internal/eventlog"
)

type credentialSealer interface {
	Seal([]byte) ([]byte, bool)
}

type Storage struct {
	db          *sql.DB
	log         *eventlog.SQLiteEventLog
	credentials *credential.SQLiteStore
	schedules   *RentalScheduleStore
	rentals     *RentalStore
	runs        *RunStore
	nodes       *NodeStore
	preparation *PreparationClock
}

func Open(ctx context.Context, dsn string) (*Storage, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	return New(ctx, db)
}

// New takes ownership of db.
func New(ctx context.Context, db *sql.DB) (*Storage, error) {
	if err := flattenWorkspaceSchema(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	sqliteLog, err := eventlog.NewSQLite(ctx, db)
	if err != nil {
		return nil, err
	}
	log := sqliteLog
	if err := migrateLegacyRunEvents(ctx, db); err != nil {
		_ = log.Close()
		return nil, err
	}
	if err := migrateLegacyPlacementObjectives(ctx, db); err != nil {
		_ = log.Close()
		return nil, err
	}
	if err := migrateStoredRevisionSecrets(ctx, db); err != nil {
		_ = log.Close()
		return nil, err
	}
	if err := migrateRentalSchedules(ctx, db); err != nil {
		_ = log.Close()
		return nil, err
	}
	if err := migrateRuns(ctx, db); err != nil {
		_ = log.Close()
		return nil, err
	}
	if err := migratePreparationClock(ctx, db); err != nil {
		_ = log.Close()
		return nil, err
	}
	if err := migrateNodes(ctx, db); err != nil {
		_ = log.Close()
		return nil, err
	}
	if err := migrateRentals(ctx, db); err != nil {
		_ = log.Close()
		return nil, err
	}
	credentials, err := credential.NewSQLiteStore(ctx, db)
	if err != nil {
		_ = log.Close()
		return nil, err
	}
	storage := &Storage{db: db, log: log, credentials: credentials}
	storage.runs = &RunStore{db: db, log: log}
	storage.schedules = &RentalScheduleStore{db: db, log: log, runs: storage.runs}
	storage.rentals = &RentalStore{db: db}
	storage.nodes = &NodeStore{db: db}
	storage.preparation = &PreparationClock{db: db}
	if err := storage.purgeDeletedConnectionCredentials(ctx); err != nil {
		_ = log.Close()
		return nil, err
	}
	return storage, nil
}

func (s *Storage) EventLog() *eventlog.SQLiteEventLog {
	return s.log
}

func (s *Storage) CredentialStore() *credential.SQLiteStore {
	return s.credentials
}

func (s *Storage) RentalSchedules() *RentalScheduleStore {
	return s.schedules
}

func (s *Storage) Runs() *RunStore {
	return s.runs
}

// Rentals is the durable record of the capacity Mercator holds: the leases it
// has taken and the generations each of them has been through.
func (s *Storage) Rentals() *RentalStore {
	return s.rentals
}

func (s *Storage) Connections(sealer credentialSealer) (*ConnectionRepository, error) {
	if sealer == nil {
		return nil, fmt.Errorf("sqlite storage: connection credential sealer is required")
	}
	return &ConnectionRepository{SQLiteEventLog: s.log, sealer: sealer, credentials: s.credentials}, nil
}

// Nodes is the durable registry of enrolled node identities, the commands they
// were given, and what they reported back.
func (s *Storage) Nodes() *NodeStore {
	return s.nodes
}

// Preparation is when this control plane last began preparing capacity
// speculatively, which is the one part of that decision a restart may not
// forget.
func (s *Storage) Preparation() *PreparationClock {
	return s.preparation
}

func (s *Storage) Close() error {
	return s.log.Close()
}

func (s *Storage) purgeDeletedConnectionCredentials(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM connection_secret
		WHERE EXISTS (
			SELECT 1
			FROM events
			WHERE events.stream_type = 'connection'
			  AND events.stream_id = connection_secret.connection_id
			  AND events.event_type = ?
		)`, connection.EventConnectionDeleted); err != nil {
		return fmt.Errorf("purge deleted connection credentials: %w", err)
	}
	return tx.Commit()
}

type ConnectionRepository struct {
	*eventlog.SQLiteEventLog
	sealer      credentialSealer
	credentials *credential.SQLiteStore
}

func (r *ConnectionRepository) CreateCredential(ctx context.Context, request eventlog.AppendRequest, write connection.CredentialWrite) (eventlog.AppendResult, error) {
	return r.AppendAtomic(ctx, request, func(ctx context.Context, tx *sql.Tx) error {
		sealed, ok := r.sealer.Seal(write.Secret)
		if !ok {
			return fmt.Errorf("%w: configure MERCATOR_SECRET_KEY", connection.ErrSecretStoreDisabled)
		}
		if err := r.credentials.PutTx(ctx, tx, write.ConnectionID, sealed); err != nil {
			return fmt.Errorf("%w: %w", connection.ErrSecretStore, err)
		}
		return nil
	})
}

func (r *ConnectionRepository) DeleteCredential(ctx context.Context, request eventlog.AppendRequest, ref connection.CredentialRef) (eventlog.AppendResult, error) {
	return r.AppendAtomic(ctx, request, func(ctx context.Context, tx *sql.Tx) error {
		if err := r.credentials.DeleteTx(ctx, tx, ref.ConnectionID); err != nil {
			return fmt.Errorf("%w: %w", connection.ErrSecretStore, err)
		}
		return nil
	})
}
