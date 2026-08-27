package eventlog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/benngarcia/mercator/internal/sqliteutil"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

type SQLiteEventLog struct {
	db        *sql.DB
	mu        sync.Mutex
	subs      map[*subscription]struct{}
	done      chan struct{}
	closeOnce sync.Once
}

type subscription struct {
	id     string
	after  GlobalPosition
	filter EventFilter
	ch     chan Delivery
	wake   chan struct{}
}

const createEvents = `CREATE TABLE IF NOT EXISTS events (
	global_position INTEGER PRIMARY KEY AUTOINCREMENT,
	event_id TEXT NOT NULL UNIQUE,
	stream_type TEXT NOT NULL,
	stream_id TEXT NOT NULL,
	stream_version INTEGER NOT NULL,
	event_type TEXT NOT NULL,
	schema_version INTEGER NOT NULL,
	occurred_at TEXT NOT NULL,
	actor_json BLOB,
	correlation_id TEXT,
	causation_id TEXT,
	command_key TEXT NOT NULL,
	request_hash TEXT NOT NULL,
	visibility TEXT NOT NULL,
	data_json BLOB,
	private_data BLOB,
	UNIQUE(stream_type, stream_id, stream_version)
)`

const createCommandAppends = `CREATE TABLE IF NOT EXISTS command_appends (
	command_key TEXT PRIMARY KEY,
	request_hash TEXT NOT NULL,
	first_position INTEGER NOT NULL,
	last_position INTEGER NOT NULL
)`

func OpenSQLite(ctx context.Context, dsn string) (*SQLiteEventLog, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	return NewSQLite(ctx, db)
}

// NewSQLite takes ownership of db. Closing the event log closes the database.
func NewSQLite(ctx context.Context, db *sql.DB) (*SQLiteEventLog, error) {
	db.SetMaxOpenConns(1)
	log := &SQLiteEventLog{db: db, subs: map[*subscription]struct{}{}, done: make(chan struct{})}
	if err := log.init(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return log, nil
}

func (l *SQLiteEventLog) Close() error {
	// Stop subscription goroutines before closing the DB so none of them park
	// on a wake signal that will never come.
	l.closeOnce.Do(func() { close(l.done) })
	return l.db.Close()
}

func (l *SQLiteEventLog) init(ctx context.Context) error {
	for _, stmt := range []string{
		`PRAGMA journal_mode=WAL`,
		// Wait for competing writers instead of failing instantly with SQLITE_BUSY.
		`PRAGMA busy_timeout=5000`,
	} {
		if _, err := l.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	if err := flattenWorkspacePartitions(ctx, l.db); err != nil {
		return err
	}
	stmts := []string{
		createEvents,
		createCommandAppends,
		`CREATE TABLE IF NOT EXISTS subscription_offsets (
			subscription_id TEXT PRIMARY KEY,
			global_position INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_events_stream ON events(stream_type, stream_id, stream_version)`,
		`CREATE INDEX IF NOT EXISTS idx_events_global ON events(global_position)`,
		`CREATE INDEX IF NOT EXISTS idx_events_types ON events(stream_type, event_type)`,
	}
	for _, stmt := range stmts {
		if _, err := l.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func flattenWorkspacePartitions(ctx context.Context, db *sql.DB) error {
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

// FlattenWorkspacePartitions removes the legacy event-log partition inside the
// caller's transaction. Storage startup composes this with every other legacy
// partition so a collision anywhere rolls the whole deployment back.
func FlattenWorkspacePartitions(ctx context.Context, tx *sql.Tx) error {
	eventsPartitioned, err := tableHasColumn(ctx, tx, "events", "workspace_id")
	if err != nil {
		return err
	}
	commandsPartitioned, err := tableHasColumn(ctx, tx, "command_appends", "workspace_id")
	if err != nil {
		return err
	}
	commandsExist, err := tableExists(ctx, tx, "command_appends")
	if err != nil {
		return err
	}
	if !eventsPartitioned && !commandsPartitioned {
		return nil
	}

	if eventsPartitioned {
		if err := refuseFlattenedStreamCollisions(ctx, tx); err != nil {
			return err
		}
	}
	if commandsPartitioned {
		if err := refuseFlattenedCommandCollisions(ctx, tx, eventsPartitioned); err != nil {
			return err
		}
	}
	if eventsPartitioned {
		if err := migratePartitionedEvents(ctx, tx); err != nil {
			return err
		}
	}
	if commandsPartitioned {
		if err := migratePartitionedCommands(ctx, tx); err != nil {
			return err
		}
	} else if eventsPartitioned && commandsExist {
		if err := pruneCommandsForRemovedStreams(ctx, tx); err != nil {
			return err
		}
	}
	return nil
}

func refuseFlattenedStreamCollisions(ctx context.Context, tx *sql.Tx) error {
	var streamType, streamID string
	var streamVersion int64
	err := tx.QueryRowContext(ctx, `SELECT stream_type, stream_id, stream_version
		FROM events
		WHERE stream_type <> 'workspace'
		GROUP BY stream_type, stream_id, stream_version
		HAVING COUNT(*) > 1
		LIMIT 1`).Scan(&streamType, &streamID, &streamVersion)
	if err == nil {
		return fmt.Errorf("eventlog: workspace migration cannot flatten duplicate stream %s/%s at version %d", streamType, streamID, streamVersion)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return nil
}

func refuseFlattenedCommandCollisions(ctx context.Context, tx *sql.Tx, eventsPartitioned bool) error {
	query := `SELECT command_appends.command_key
		FROM command_appends
		WHERE EXISTS (
			SELECT 1 FROM events
			WHERE events.command_key = command_appends.command_key
			  AND events.global_position BETWEEN command_appends.first_position
			      AND command_appends.last_position
		)
		GROUP BY command_appends.command_key
		HAVING COUNT(*) > 1
		LIMIT 1`
	if eventsPartitioned {
		query = `SELECT command_appends.command_key
			FROM command_appends
			WHERE EXISTS (
				SELECT 1 FROM events
				WHERE events.command_key = command_appends.command_key
				  AND events.workspace_id = command_appends.workspace_id
				  AND events.stream_type <> 'workspace'
			)
			GROUP BY command_appends.command_key
			HAVING COUNT(*) > 1
			LIMIT 1`
	}
	var commandKey string
	err := tx.QueryRowContext(ctx, query).Scan(&commandKey)
	if err == nil {
		return fmt.Errorf("eventlog: workspace migration cannot flatten duplicate command key %q", commandKey)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return nil
}

func migratePartitionedEvents(ctx context.Context, tx *sql.Tx) error {
	for _, statement := range []string{
		`ALTER TABLE events RENAME TO events_workspace_legacy`,
		createEvents,
		`INSERT INTO events (
			global_position, event_id, stream_type, stream_id, stream_version,
			event_type, schema_version, occurred_at, actor_json, correlation_id,
			causation_id, command_key, request_hash, visibility, data_json, private_data
		) SELECT
			global_position, event_id, stream_type, stream_id, stream_version,
			event_type, schema_version, occurred_at, actor_json, correlation_id,
			causation_id, command_key, request_hash, visibility, data_json, private_data
		FROM events_workspace_legacy
		WHERE stream_type <> 'workspace'
		ORDER BY global_position`,
		`DROP TABLE events_workspace_legacy`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("eventlog: flatten workspace events: %w", err)
		}
	}
	return nil
}

func migratePartitionedCommands(ctx context.Context, tx *sql.Tx) error {
	for _, statement := range []string{
		`ALTER TABLE command_appends RENAME TO command_appends_workspace_legacy`,
		createCommandAppends,
		`INSERT INTO command_appends (command_key, request_hash, first_position, last_position)
		SELECT command_key, request_hash, first_position, last_position
		FROM command_appends_workspace_legacy
		WHERE EXISTS (
			SELECT 1 FROM events
			WHERE events.command_key = command_appends_workspace_legacy.command_key
			  AND events.global_position BETWEEN command_appends_workspace_legacy.first_position
			      AND command_appends_workspace_legacy.last_position
		)`,
		`DROP TABLE command_appends_workspace_legacy`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("eventlog: flatten workspace commands: %w", err)
		}
	}
	return nil
}

func pruneCommandsForRemovedStreams(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM command_appends
		WHERE NOT EXISTS (
			SELECT 1 FROM events
			WHERE events.command_key = command_appends.command_key
			  AND events.global_position BETWEEN command_appends.first_position
			      AND command_appends.last_position
		)`)
	if err != nil {
		return fmt.Errorf("eventlog: prune commands for removed workspace streams: %w", err)
	}
	return nil
}

type schemaQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func tableExists(ctx context.Context, db schemaQueryer, table string) (bool, error) {
	var exists bool
	err := db.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?
	)`, table).Scan(&exists)
	return exists, err
}

func tableHasColumn(ctx context.Context, db schemaQueryer, table, column string) (bool, error) {
	return sqliteutil.HasColumn(ctx, db, table, column)
}

func (l *SQLiteEventLog) Append(ctx context.Context, req AppendRequest) (AppendResult, error) {
	return l.append(ctx, req, nil)
}

// AppendAtomic commits an event append and mutation in one SQLite transaction.
// The mutation runs only for a new command, after event inserts succeed and
// before the command append is recorded. Conflicts and replays never invoke it.
func (l *SQLiteEventLog) AppendAtomic(ctx context.Context, req AppendRequest, mutation func(context.Context, *sql.Tx) error) (AppendResult, error) {
	if mutation == nil {
		return AppendResult{}, fmt.Errorf("eventlog: atomic mutation is required")
	}
	return l.append(ctx, req, mutation)
}

func (l *SQLiteEventLog) append(ctx context.Context, req AppendRequest, mutation func(context.Context, *sql.Tx) error) (AppendResult, error) {
	if err := req.Stream.validate(); err != nil {
		return AppendResult{}, err
	}
	if req.CommandKey == "" || req.RequestHash == "" {
		return AppendResult{}, fmt.Errorf("eventlog: command_key and request_hash are required")
	}
	if len(req.Events) == 0 {
		return AppendResult{}, fmt.Errorf("eventlog: at least one event is required")
	}
	for _, event := range req.Events {
		if event.ID == "" || event.Type == "" || event.SchemaVersion == 0 {
			return AppendResult{}, fmt.Errorf("eventlog: event id, type, and schema_version are required")
		}
	}

	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return AppendResult{}, err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	existing, err := readCommand(ctx, tx, req.CommandKey)
	if err != nil {
		return AppendResult{}, err
	}
	if len(existing) > 0 {
		if existing[0].RequestHash != req.RequestHash {
			return AppendResult{}, ErrIdempotencyConflict
		}
		return appendResult(existing, true), nil
	}

	version, err := currentStreamVersion(ctx, tx, req.Stream)
	if err != nil {
		return AppendResult{}, err
	}
	if version != req.ExpectedStreamVersion {
		return AppendResult{}, ErrConcurrencyConflict
	}

	stored := make([]StoredEvent, 0, len(req.Events))
	for i, event := range req.Events {
		occurredAt := event.OccurredAt
		if occurredAt.IsZero() {
			occurredAt = time.Now().UTC()
		}
		visibility := event.Visibility
		if visibility == "" {
			visibility = VisibilityPublic
		}
		streamVersion := req.ExpectedStreamVersion + uint64(i) + 1
		result, err := tx.ExecContext(ctx, `INSERT INTO events (
			event_id, stream_type, stream_id, stream_version,
			event_type, schema_version, occurred_at, actor_json, correlation_id,
			causation_id, command_key, request_hash, visibility, data_json, private_data
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			event.ID, req.Stream.Type, req.Stream.ID, streamVersion,
			event.Type, event.SchemaVersion, occurredAt.UTC().Format(time.RFC3339Nano), []byte(req.Actor),
			req.CorrelationID, req.CausationID, req.CommandKey, req.RequestHash, string(visibility),
			[]byte(event.Data), event.PrivateData,
		)
		if err != nil {
			if isConstraintViolation(err) {
				return AppendResult{}, ErrIdempotencyConflict
			}
			return AppendResult{}, err
		}
		pos, err := result.LastInsertId()
		if err != nil {
			return AppendResult{}, err
		}
		stored = append(stored, StoredEvent{
			GlobalPosition: GlobalPosition(pos),
			ID:             event.ID,
			StreamType:     req.Stream.Type,
			StreamID:       req.Stream.ID,
			StreamVersion:  streamVersion,
			Type:           event.Type,
			SchemaVersion:  event.SchemaVersion,
			OccurredAt:     occurredAt.UTC(),
			Actor:          cloneJSON(req.Actor),
			CorrelationID:  req.CorrelationID,
			CausationID:    req.CausationID,
			CommandKey:     req.CommandKey,
			RequestHash:    req.RequestHash,
			Visibility:     visibility,
			Data:           cloneJSON(event.Data),
			PrivateData:    cloneBytes(event.PrivateData),
		})
	}
	if mutation != nil {
		if err := mutation(ctx, tx); err != nil {
			return AppendResult{}, err
		}
	}
	if len(stored) > 0 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO command_appends (
			command_key, request_hash, first_position, last_position
		) VALUES (?, ?, ?, ?)`,
			req.CommandKey, req.RequestHash,
			stored[0].GlobalPosition, stored[len(stored)-1].GlobalPosition); err != nil {
			if isConstraintViolation(err) {
				return AppendResult{}, ErrIdempotencyConflict
			}
			return AppendResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return AppendResult{}, err
	}
	l.wakeSubscribers()
	return appendResult(stored, false), nil
}

func (l *SQLiteEventLog) ReadStream(ctx context.Context, stream StreamKey, afterVersion uint64, limit int) ([]StoredEvent, error) {
	if err := stream.validate(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	rows, err := l.db.QueryContext(ctx, `SELECT global_position, event_id, stream_type, stream_id,
		stream_version, event_type, schema_version, occurred_at, actor_json, correlation_id, causation_id,
		command_key, request_hash, visibility, data_json, private_data
		FROM events
		WHERE stream_type = ? AND stream_id = ? AND stream_version > ?
		ORDER BY stream_version ASC
		LIMIT ?`, stream.Type, stream.ID, afterVersion, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEvents(rows)
}

func (l *SQLiteEventLog) ReadAll(ctx context.Context, after GlobalPosition, limit int, filter EventFilter) ([]StoredEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	where := []string{"global_position > ?"}
	args := []any{after}
	filterWhere, filterArgs := eventFilterSQL(filter)
	where = append(where, filterWhere...)
	args = append(args, filterArgs...)
	args = append(args, limit)
	rows, err := l.db.QueryContext(ctx, `SELECT global_position, event_id, stream_type, stream_id,
		stream_version, event_type, schema_version, occurred_at, actor_json, correlation_id, causation_id,
		command_key, request_hash, visibility, data_json, private_data
		FROM events
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY global_position ASC
		LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEvents(rows)
}

func (l *SQLiteEventLog) LatestPosition(ctx context.Context, filter EventFilter) (GlobalPosition, error) {
	where, args := eventFilterSQL(filter)
	query := "SELECT COALESCE(MAX(global_position), 0) FROM events"
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	var position GlobalPosition
	if err := l.db.QueryRowContext(ctx, query, args...).Scan(&position); err != nil {
		return 0, err
	}
	return position, nil
}

func eventFilterSQL(filter EventFilter) ([]string, []any) {
	where := make([]string, 0, 3)
	args := make([]any, 0, 1+len(filter.StreamTypes)+len(filter.EventTypes))
	if len(filter.StreamTypes) > 0 {
		where = append(where, "stream_type IN ("+placeholders(len(filter.StreamTypes))+")")
		for _, value := range filter.StreamTypes {
			args = append(args, value)
		}
	}
	if len(filter.EventTypes) > 0 {
		where = append(where, "event_type IN ("+placeholders(len(filter.EventTypes))+")")
		for _, value := range filter.EventTypes {
			args = append(args, value)
		}
	}
	if filter.Visibility != "" {
		where = append(where, "visibility = ?")
		args = append(args, filter.Visibility)
	}
	return where, args
}

func (l *SQLiteEventLog) Subscribe(ctx context.Context, req SubscriptionRequest) (<-chan Delivery, error) {
	if req.SubscriptionID == "" {
		return nil, fmt.Errorf("eventlog: subscription_id is required")
	}
	after := req.After
	stored, ok, err := l.Offset(ctx, req.SubscriptionID)
	if err != nil {
		return nil, err
	}
	if ok && stored > after {
		after = stored
	}
	sub := &subscription{
		id:     req.SubscriptionID,
		after:  after,
		filter: req.Filter,
		ch:     make(chan Delivery, 64),
		wake:   make(chan struct{}, 1),
	}
	l.mu.Lock()
	l.subs[sub] = struct{}{}
	l.mu.Unlock()

	go l.runSubscription(ctx, sub)
	sub.signal()
	return sub.ch, nil
}

func (l *SQLiteEventLog) Offset(ctx context.Context, subscriptionID string) (GlobalPosition, bool, error) {
	if subscriptionID == "" {
		return 0, false, fmt.Errorf("eventlog: subscription_id is required")
	}
	var position uint64
	err := l.db.QueryRowContext(ctx, `SELECT global_position FROM subscription_offsets WHERE subscription_id = ?`, subscriptionID).Scan(&position)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return GlobalPosition(position), true, nil
}

func (l *SQLiteEventLog) Ack(ctx context.Context, subscriptionID string, position GlobalPosition) error {
	_, err := l.db.ExecContext(ctx, `INSERT INTO subscription_offsets(subscription_id, global_position)
		VALUES (?, ?)
		ON CONFLICT(subscription_id) DO UPDATE SET global_position =
			MAX(subscription_offsets.global_position, excluded.global_position)`,
		subscriptionID, position)
	return err
}

func (l *SQLiteEventLog) runSubscription(ctx context.Context, sub *subscription) {
	defer func() {
		l.mu.Lock()
		delete(l.subs, sub)
		l.mu.Unlock()
		close(sub.ch)
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-l.done:
			return
		case <-sub.wake:
			for {
				events, err := l.ReadAll(ctx, sub.after, 100, sub.filter)
				if err != nil {
					// Transient read failure: retry shortly instead of
					// stalling this subscription until the next append.
					time.AfterFunc(time.Second, sub.signal)
					break
				}
				if len(events) == 0 {
					break
				}
				for _, event := range events {
					select {
					case <-ctx.Done():
						return
					case <-l.done:
						return
					case sub.ch <- Delivery{SubscriptionID: sub.id, Event: event}:
						sub.after = event.GlobalPosition
					}
				}
			}
		}
	}
}

func (l *SQLiteEventLog) wakeSubscribers() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for sub := range l.subs {
		sub.signal()
	}
}

func (s *subscription) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// isConstraintViolation reports whether err is any SQLite constraint failure
// (unique, primary key, …), matched by error code rather than message text.
func isConstraintViolation(err error) bool {
	var serr *sqlite.Error
	if errors.As(err, &serr) {
		return serr.Code()&0xff == sqlite3.SQLITE_CONSTRAINT
	}
	return false
}

func readCommand(ctx context.Context, tx *sql.Tx, commandKey string) ([]StoredEvent, error) {
	var requestHash string
	var firstPosition, lastPosition int64
	err := tx.QueryRowContext(ctx, `SELECT request_hash, first_position, last_position
		FROM command_appends
		WHERE command_key = ?`, commandKey).Scan(&requestHash, &firstPosition, &lastPosition)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT global_position, event_id, stream_type, stream_id,
		stream_version, event_type, schema_version, occurred_at, actor_json, correlation_id, causation_id,
		command_key, request_hash, visibility, data_json, private_data
		FROM events
		WHERE command_key = ? AND global_position BETWEEN ? AND ?
		ORDER BY global_position ASC`, commandKey, firstPosition, lastPosition)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEvents(rows)
}

func currentStreamVersion(ctx context.Context, tx *sql.Tx, stream StreamKey) (uint64, error) {
	var version sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(stream_version) FROM events
		WHERE stream_type = ? AND stream_id = ?`,
		stream.Type, stream.ID).Scan(&version); err != nil {
		return 0, err
	}
	if !version.Valid {
		return 0, nil
	}
	return uint64(version.Int64), nil
}

func scanEvents(rows *sql.Rows) ([]StoredEvent, error) {
	var events []StoredEvent
	for rows.Next() {
		var event StoredEvent
		var occurredAt string
		var visibility string
		var actor, data []byte
		var streamVersion int64
		var position int64
		if err := rows.Scan(&position, &event.ID, &event.StreamType, &event.StreamID,
			&streamVersion, &event.Type, &event.SchemaVersion, &occurredAt, &actor, &event.CorrelationID,
			&event.CausationID, &event.CommandKey, &event.RequestHash, &visibility, &data, &event.PrivateData); err != nil {
			return nil, err
		}
		parsedAt, err := time.Parse(time.RFC3339Nano, occurredAt)
		if err != nil {
			return nil, err
		}
		event.GlobalPosition = GlobalPosition(position)
		event.StreamVersion = uint64(streamVersion)
		event.OccurredAt = parsedAt.UTC()
		event.Visibility = Visibility(visibility)
		event.Actor = cloneJSON(json.RawMessage(actor))
		event.Data = cloneJSON(json.RawMessage(data))
		events = append(events, event)
	}
	return events, rows.Err()
}

func appendResult(events []StoredEvent, duplicate bool) AppendResult {
	if len(events) == 0 {
		return AppendResult{Duplicate: duplicate}
	}
	return AppendResult{
		FirstPosition:     events[0].GlobalPosition,
		LastPosition:      events[len(events)-1].GlobalPosition,
		NextStreamVersion: events[len(events)-1].StreamVersion,
		Duplicate:         duplicate,
		Events:            events,
	}
}

func placeholders(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = "?"
	}
	return strings.Join(parts, ",")
}

func cloneJSON(v json.RawMessage) json.RawMessage {
	if v == nil {
		return nil
	}
	out := make([]byte, len(v))
	copy(out, v)
	return json.RawMessage(out)
}

func cloneBytes(v []byte) []byte {
	if v == nil {
		return nil
	}
	out := make([]byte, len(v))
	copy(out, v)
	return out
}
