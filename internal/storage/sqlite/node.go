package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	modernsqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/benngarcia/mercator/internal/capability"
	"github.com/benngarcia/mercator/internal/node"
)

const createNodes = `CREATE TABLE IF NOT EXISTS nodes (
	node_id TEXT PRIMARY KEY,
	rental_id TEXT NOT NULL,
	generation INTEGER NOT NULL,
	state TEXT NOT NULL,
	fencing_token INTEGER NOT NULL,
	enrollment_token_id TEXT NOT NULL,
	enrollment_expires TEXT NOT NULL,
	enrolled_at TEXT NOT NULL,
	lease_expires TEXT NOT NULL,
	last_heartbeat_at TEXT NOT NULL,
	agent_version TEXT NOT NULL,
	facts_json BLOB NOT NULL,
	shadow_price_usd_per_hour REAL NOT NULL DEFAULT 0,
	purchase_json BLOB NOT NULL DEFAULT '{}'
)`

// addNodePurchase carries the terms a machine is bought on into a table that
// existed before an operator could state them. SQLite has no "add column if not
// exists", so a duplicate is how it says the column is already there, and a
// registry that could not read the column would refuse every node it holds.
const addNodePurchase = `ALTER TABLE nodes ADD COLUMN purchase_json BLOB NOT NULL DEFAULT '{}'`

const createNodeOperations = `CREATE TABLE IF NOT EXISTS node_operations (
	node_id TEXT NOT NULL,
	operation_id TEXT NOT NULL,
	kind TEXT NOT NULL,
	fencing_token INTEGER NOT NULL,
	state TEXT NOT NULL,
	issued_at TEXT NOT NULL,
	settled_at TEXT NOT NULL,
	failure TEXT NOT NULL,
	payload BLOB NOT NULL,
	sequence INTEGER NOT NULL,
	PRIMARY KEY(node_id, operation_id)
)`

const createNodeEvents = `CREATE TABLE IF NOT EXISTS node_events (
	node_id TEXT NOT NULL,
	event_id TEXT NOT NULL,
	PRIMARY KEY(node_id, event_id)
)`

const createNodeWorkloads = `CREATE TABLE IF NOT EXISTS node_workloads (
	node_id TEXT NOT NULL,
	run_id TEXT NOT NULL,
	attempt_id TEXT NOT NULL,
	observed_at TEXT NOT NULL,
	observation_json BLOB NOT NULL,
	PRIMARY KEY(node_id, run_id, attempt_id)
)`

func migrateNodes(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, createNodes); err != nil {
		return fmt.Errorf("migrate nodes: %w", err)
	}
	if _, err := db.ExecContext(ctx, addNodePurchase); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		return fmt.Errorf("migrate nodes: %w", err)
	}
	if err := flattenNodes(ctx, db); err != nil {
		return err
	}
	for _, statement := range []string{createNodeOperations, createNodeEvents, createNodeWorkloads} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migrate nodes: %w", err)
		}
	}
	return nil
}

func flattenNodes(ctx context.Context, db *sql.DB) error {
	partitioned, err := tableHasColumn(ctx, db, "nodes", "workspace_id")
	if err != nil || !partitioned {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	checks := []struct{ table, identity string }{
		{"nodes", "node_id"},
		{"node_operations", "node_id || '/' || operation_id"},
		{"node_events", "node_id || '/' || event_id"},
		{"node_workloads", "node_id || '/' || run_id || '/' || attempt_id"},
	}
	for _, check := range checks {
		var collision string
		err := tx.QueryRowContext(ctx, `SELECT `+check.identity+` FROM `+check.table+` GROUP BY `+check.identity+` HAVING COUNT(*) > 1 LIMIT 1`).Scan(&collision)
		if err == nil {
			return fmt.Errorf("migrate nodes: duplicate %s identity %q across workspaces", check.table, collision)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	for _, statement := range []string{
		`ALTER TABLE nodes RENAME TO nodes_workspace_legacy`,
		`ALTER TABLE node_operations RENAME TO node_operations_workspace_legacy`,
		`ALTER TABLE node_events RENAME TO node_events_workspace_legacy`,
		`ALTER TABLE node_workloads RENAME TO node_workloads_workspace_legacy`,
		createNodes, createNodeOperations, createNodeEvents, createNodeWorkloads,
		`INSERT INTO nodes (node_id, rental_id, generation, state, fencing_token, enrollment_token_id, enrollment_expires, enrolled_at, lease_expires, last_heartbeat_at, agent_version, facts_json, shadow_price_usd_per_hour, purchase_json)
		 SELECT node_id, rental_id, generation, state, fencing_token, enrollment_token_id, enrollment_expires, enrolled_at, lease_expires, last_heartbeat_at, agent_version, facts_json, shadow_price_usd_per_hour, purchase_json FROM nodes_workspace_legacy`,
		`INSERT INTO node_operations (node_id, operation_id, kind, fencing_token, state, issued_at, settled_at, failure, payload, sequence)
		 SELECT node_id, operation_id, kind, fencing_token, state, issued_at, settled_at, failure, payload, sequence FROM node_operations_workspace_legacy`,
		`INSERT INTO node_events (node_id, event_id) SELECT node_id, event_id FROM node_events_workspace_legacy`,
		`INSERT INTO node_workloads (node_id, run_id, attempt_id, observed_at, observation_json)
		 SELECT node_id, run_id, attempt_id, observed_at, observation_json FROM node_workloads_workspace_legacy`,
		`DROP TABLE nodes_workspace_legacy`, `DROP TABLE node_operations_workspace_legacy`,
		`DROP TABLE node_events_workspace_legacy`, `DROP TABLE node_workloads_workspace_legacy`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migrate nodes: flatten workspaces: %w", err)
		}
	}
	return tx.Commit()
}

// NodeStore is the durable node registry. A registry that forgets across a
// restart cannot promise not to launch twice, so every fact the reconciliation
// path needs is written here before a command reaches a node.
type NodeStore struct{ db *sql.DB }

func (store *NodeStore) Invite(ctx context.Context, record node.Record) error {
	facts, err := json.Marshal(record.Facts)
	if err != nil {
		return fmt.Errorf("encode node facts: %w", err)
	}
	// The sale this machine sits under, written at invitation because that is when
	// the operator states it and because a registry that forgot it would price every
	// machine it holds as though it were bought by the second.
	purchase, err := json.Marshal(record.Purchase)
	if err != nil {
		return fmt.Errorf("encode node purchase: %w", err)
	}
	_, err = store.db.ExecContext(ctx, `
		INSERT INTO nodes (
			node_id, rental_id, generation, state, fencing_token,
			enrollment_token_id, enrollment_expires, enrolled_at, lease_expires,
			last_heartbeat_at, agent_version, facts_json, shadow_price_usd_per_hour,
			purchase_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.ID, record.RentalID, record.Generation, string(record.State),
		record.FencingToken, record.EnrollmentTokenID, stamp(record.EnrollmentExpires),
		stamp(record.EnrolledAt), stamp(record.LeaseExpires), stamp(record.LastHeartbeatAt),
		record.AgentVersion, facts, record.ShadowPriceUSDPerHour, purchase,
	)
	if err != nil {
		if isConstraintViolation(err) {
			return fmt.Errorf("%w: %s", node.ErrIdentityExists, record.ID)
		}
		return fmt.Errorf("invite node %q: %w", record.ID, err)
	}
	return nil
}

// isConstraintViolation reports whether the driver refused a write because the
// schema did, matched on the code rather than on the message.
//
// It is what makes ErrIdentityExists an answer this store can give. Provisioning
// asks for an identity again on every attempt, precisely because it cannot know
// whether an earlier attempt landed a machine, and a store that reported the
// collision as an opaque failure would leave a Rental with a machine already
// rented and no route back to the invitation that machine is holding.
func isConstraintViolation(err error) bool {
	var sqliteError *modernsqlite.Error
	return errors.As(err, &sqliteError) && sqliteError.Code()&0xff == sqlite3.SQLITE_CONSTRAINT
}

func (store *NodeStore) Get(ctx context.Context, nodeID string) (node.Record, error) {
	return store.scanOne(ctx, `WHERE node_id = ?`, nodeID)
}

func (store *NodeStore) Find(ctx context.Context, nodeID string) (node.Record, error) {
	return store.scanOne(ctx, `WHERE node_id = ?`, nodeID)
}

func (store *NodeStore) List(ctx context.Context) ([]node.Record, error) {
	rows, err := store.db.QueryContext(ctx, nodeColumns+` ORDER BY node_id`)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var records []node.Record
	for rows.Next() {
		record, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (store *NodeStore) Enroll(
	ctx context.Context,
	nodeID string,
	enrollment node.Enrollment,
) (node.Record, error) {
	facts, err := json.Marshal(enrollment.Facts)
	if err != nil {
		return node.Record{}, fmt.Errorf("encode node facts: %w", err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return node.Record{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var state, tokenID string
	if err := tx.QueryRowContext(ctx,
		`SELECT state, enrollment_token_id FROM nodes WHERE node_id = ?`,
		nodeID,
	).Scan(&state, &tokenID); errors.Is(err, sql.ErrNoRows) {
		return node.Record{}, fmt.Errorf("%w: %s", node.ErrNotFound, nodeID)
	} else if err != nil {
		return node.Record{}, err
	}
	if node.State(state) == node.StateRetired {
		return node.Record{}, fmt.Errorf("%w: %s cannot enroll again", node.ErrRetired, nodeID)
	}
	if tokenID != enrollment.EnrollmentTokenID {
		return node.Record{}, fmt.Errorf("%w: %s", node.ErrEnrollmentSpent, nodeID)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE nodes SET
			state = ?, fencing_token = fencing_token + 1, enrollment_token_id = '',
			agent_version = ?, facts_json = ?, enrolled_at = ?, last_heartbeat_at = ?, lease_expires = ?
		WHERE node_id = ?`,
		string(node.StateReady), enrollment.AgentVersion, facts,
		stamp(enrollment.EnrolledAt), stamp(enrollment.EnrolledAt), stamp(enrollment.LeaseExpires),
		nodeID,
	); err != nil {
		return node.Record{}, fmt.Errorf("enroll node %q: %w", nodeID, err)
	}
	if err := tx.Commit(); err != nil {
		return node.Record{}, err
	}
	return store.Get(ctx, nodeID)
}

func (store *NodeStore) Reinvite(ctx context.Context, nodeID, enrollmentTokenID string, expires time.Time) error {
	result, err := store.db.ExecContext(ctx,
		`UPDATE nodes SET enrollment_token_id = ?, enrollment_expires = ? WHERE node_id = ?`,
		enrollmentTokenID, stamp(expires), nodeID)
	if err != nil {
		return fmt.Errorf("reinvite node %q: %w", nodeID, err)
	}
	return requireRow(result, nodeID)
}

func (store *NodeStore) Retire(ctx context.Context, nodeID string) error {
	result, err := store.db.ExecContext(ctx,
		`UPDATE nodes SET state = ? WHERE node_id = ?`,
		string(node.StateRetired), nodeID)
	if err != nil {
		return fmt.Errorf("retire node %q: %w", nodeID, err)
	}
	return requireRow(result, nodeID)
}

func (store *NodeStore) Heartbeat(
	ctx context.Context,
	nodeID string,
	facts capability.NodeFacts,
	leaseExpires time.Time,
) (node.Record, error) {
	encoded, err := json.Marshal(facts)
	if err != nil {
		return node.Record{}, fmt.Errorf("encode node facts: %w", err)
	}
	// A retired node reports on a Rental generation that is over. Renewing its
	// lease here would put it back in the one state the registry publishes as
	// capacity, so the machine Mercator gave up would be offered again by its own
	// agent's next heartbeat. The state is matched in the statement rather than
	// read first, so a retirement landing between the two cannot be missed.
	result, err := store.db.ExecContext(ctx, `
		UPDATE nodes SET state = ?, facts_json = ?, last_heartbeat_at = ?, lease_expires = ?
		WHERE node_id = ? AND state != ?`,
		string(node.StateReady), encoded, stamp(facts.ObservedAt), stamp(leaseExpires),
		nodeID, string(node.StateRetired))
	if err != nil {
		return node.Record{}, fmt.Errorf("heartbeat node %q: %w", nodeID, err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return node.Record{}, err
	}
	if changed == 0 {
		return node.Record{}, store.refusedHeartbeat(ctx, nodeID)
	}
	return store.Get(ctx, nodeID)
}

// refusedHeartbeat says which of the two reasons a heartbeat matched no row: an
// identity this control plane never invited, or one whose generation is over.
// They send an operator somewhere different, and a statement that changed nothing
// cannot say which on its own.
func (store *NodeStore) refusedHeartbeat(ctx context.Context, nodeID string) error {
	record, err := store.Get(ctx, nodeID)
	if err != nil {
		return err
	}
	return fmt.Errorf("%w: %s", node.ErrRetired, record.ID)
}

func (store *NodeStore) RecordEvent(ctx context.Context, event node.Event) (bool, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO node_events (node_id, event_id) VALUES (?, ?)`,
		event.NodeID, event.ID)
	if err != nil {
		return false, fmt.Errorf("record node event: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if inserted == 0 {
		return false, tx.Commit()
	}
	if event.Kind == node.EventWorkload {
		encoded, err := json.Marshal(event.Workload)
		if err != nil {
			return false, fmt.Errorf("encode workload observation: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO node_workloads (node_id, run_id, attempt_id, observed_at, observation_json)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(node_id, run_id, attempt_id)
			DO UPDATE SET observed_at = excluded.observed_at, observation_json = excluded.observation_json
			WHERE excluded.observed_at >= node_workloads.observed_at`,
			event.NodeID, event.Workload.RunID, event.Workload.AttemptID,
			stamp(event.Workload.ObservedAt), encoded,
		); err != nil {
			return false, fmt.Errorf("record workload observation: %w", err)
		}
	}
	return true, tx.Commit()
}

func (store *NodeStore) LatestWorkload(
	ctx context.Context,
	nodeID, runID, attemptID string,
) (capability.WorkloadObservation, bool, error) {
	var encoded []byte
	err := store.db.QueryRowContext(ctx, `
		SELECT observation_json FROM node_workloads
		WHERE node_id = ? AND run_id = ? AND attempt_id = ?`,
		nodeID, runID, attemptID).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return capability.WorkloadObservation{}, false, nil
	}
	if err != nil {
		return capability.WorkloadObservation{}, false, fmt.Errorf("read workload observation: %w", err)
	}
	var observation capability.WorkloadObservation
	if err := json.Unmarshal(encoded, &observation); err != nil {
		return capability.WorkloadObservation{}, false, fmt.Errorf("decode workload observation: %w", err)
	}
	return observation, true, nil
}

func (store *NodeStore) Workloads(ctx context.Context, nodeID string) ([]capability.WorkloadObservation, error) {
	rows, err := store.db.QueryContext(ctx, `
		SELECT observation_json FROM node_workloads
		WHERE node_id = ?
		ORDER BY run_id, attempt_id`, nodeID)
	if err != nil {
		return nil, fmt.Errorf("list workload observations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var observations []capability.WorkloadObservation
	for rows.Next() {
		var encoded []byte
		if err := rows.Scan(&encoded); err != nil {
			return nil, err
		}
		var observation capability.WorkloadObservation
		if err := json.Unmarshal(encoded, &observation); err != nil {
			return nil, fmt.Errorf("decode workload observation: %w", err)
		}
		observations = append(observations, observation)
	}
	return observations, rows.Err()
}

func (store *NodeStore) AppendOperation(ctx context.Context, operation node.Operation) (node.Operation, bool, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return node.Operation{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	existing, found, err := scanOperation(ctx, tx, operation.NodeID, operation.OperationID)
	if err != nil {
		return node.Operation{}, false, err
	}
	if found && !existing.Reissuable() {
		return existing, true, tx.Commit()
	}
	if found {
		// A refusal that left nothing on the machine is asked again under the same
		// identity. The row is rewritten rather than added beside itself, because
		// the identity names one command and its sequence is where the node was
		// first told about this content.
		if _, err := tx.ExecContext(ctx, `
			UPDATE node_operations
			SET kind = ?, fencing_token = ?, state = ?, issued_at = ?, settled_at = '', failure = '', payload = ?
			WHERE node_id = ? AND operation_id = ?`,
			string(operation.Kind), operation.FencingToken, string(operation.State), stamp(operation.IssuedAt),
			operation.Payload, operation.NodeID, operation.OperationID,
		); err != nil {
			return node.Operation{}, false, fmt.Errorf("reissue node operation: %w", err)
		}
		return operation, false, tx.Commit()
	}
	var sequence int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(sequence), 0) + 1 FROM node_operations WHERE node_id = ?`,
		operation.NodeID).Scan(&sequence); err != nil {
		return node.Operation{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO node_operations (
			node_id, operation_id, kind, fencing_token, state,
			issued_at, settled_at, failure, payload, sequence
		) VALUES (?, ?, ?, ?, ?, ?, '', '', ?, ?)`,
		operation.NodeID, operation.OperationID, string(operation.Kind),
		operation.FencingToken, string(operation.State), stamp(operation.IssuedAt),
		operation.Payload, sequence,
	); err != nil {
		return node.Operation{}, false, fmt.Errorf("record node operation: %w", err)
	}
	return operation, false, tx.Commit()
}

func (store *NodeStore) SettleOperation(ctx context.Context, nodeID string, result node.Result) error {
	state := node.OperationApplied
	if !result.Applied {
		state = node.OperationRefused
	}
	settled, err := store.db.ExecContext(ctx, `
		UPDATE node_operations SET state = ?, failure = ?, settled_at = ?
		WHERE node_id = ? AND operation_id = ? AND state = ?`,
		string(state), result.Failure, stamp(result.ReportedAt),
		nodeID, result.OperationID, string(node.OperationPending))
	if err != nil {
		return fmt.Errorf("settle node operation: %w", err)
	}
	changed, err := settled.RowsAffected()
	if err != nil {
		return err
	}
	if changed > 0 {
		return nil
	}
	// Already settled is not an error: a node that reports twice after a lost
	// response must not be told it did something wrong.
	if _, found, err := scanOperation(ctx, store.db, nodeID, result.OperationID); err != nil {
		return err
	} else if !found {
		return fmt.Errorf("node: %q has no operation %q to settle", nodeID, result.OperationID)
	}
	return nil
}

func (store *NodeStore) PendingOperations(ctx context.Context, nodeID string) ([]node.Operation, error) {
	return store.operations(ctx, nodeID, node.OperationPending)
}

func (store *NodeStore) AppliedOperationIDs(ctx context.Context, nodeID string) ([]string, error) {
	applied, err := store.operations(ctx, nodeID, node.OperationApplied)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(applied))
	for _, operation := range applied {
		ids = append(ids, operation.OperationID)
	}
	return ids, nil
}

func (store *NodeStore) ExpireLeases(ctx context.Context, now time.Time) ([]node.Record, error) {
	rows, err := store.db.QueryContext(ctx, nodeColumns+` WHERE state = ? AND lease_expires <= ? ORDER BY node_id`,
		string(node.StateReady), stamp(now))
	if err != nil {
		return nil, fmt.Errorf("find expired node leases: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var expired []node.Record
	for rows.Next() {
		record, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		record.State = node.StateLost
		expired = append(expired, record)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, record := range expired {
		if _, err := store.db.ExecContext(ctx,
			`UPDATE nodes SET state = ? WHERE node_id = ?`,
			string(node.StateLost), record.ID); err != nil {
			return nil, fmt.Errorf("mark node %q lost: %w", record.ID, err)
		}
	}
	return expired, nil
}

func (store *NodeStore) operations(ctx context.Context, nodeID string, state node.OperationState) ([]node.Operation, error) {
	rows, err := store.db.QueryContext(ctx, `
		SELECT operation_id, kind, fencing_token, state, issued_at, settled_at, failure, payload
		FROM node_operations
		WHERE node_id = ? AND state = ?
		ORDER BY sequence`, nodeID, string(state))
	if err != nil {
		return nil, fmt.Errorf("list node operations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var operations []node.Operation
	for rows.Next() {
		operation := node.Operation{NodeID: nodeID}
		var kind, operationState, issued, settled string
		if err := rows.Scan(&operation.OperationID, &kind, &operation.FencingToken, &operationState,
			&issued, &settled, &operation.Failure, &operation.Payload); err != nil {
			return nil, err
		}
		operation.Kind = node.CommandKind(kind)
		operation.State = node.OperationState(operationState)
		operation.IssuedAt = parseStamp(issued)
		operation.SettledAt = parseStamp(settled)
		operations = append(operations, operation)
	}
	return operations, rows.Err()
}

const nodeColumns = `SELECT node_id, rental_id, generation, state, fencing_token,
	enrollment_token_id, enrollment_expires, enrolled_at, lease_expires, last_heartbeat_at,
	agent_version, facts_json, shadow_price_usd_per_hour, purchase_json FROM nodes`

func (store *NodeStore) scanOne(ctx context.Context, where string, args ...any) (node.Record, error) {
	rows, err := store.db.QueryContext(ctx, nodeColumns+" "+where, args...)
	if err != nil {
		return node.Record{}, fmt.Errorf("read node: %w", err)
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return node.Record{}, err
		}
		return node.Record{}, fmt.Errorf("%w: %v", node.ErrNotFound, args)
	}
	return scanNode(rows)
}

type scanner interface{ Scan(...any) error }

func scanNode(rows scanner) (node.Record, error) {
	var record node.Record
	var state, enrollmentExpires, enrolledAt, leaseExpires, heartbeat string
	var facts, purchase []byte
	if err := rows.Scan(&record.ID, &record.RentalID, &record.Generation,
		&state, &record.FencingToken, &record.EnrollmentTokenID, &enrollmentExpires,
		&enrolledAt, &leaseExpires, &heartbeat, &record.AgentVersion, &facts,
		&record.ShadowPriceUSDPerHour, &purchase); err != nil {
		return node.Record{}, err
	}
	record.State = node.State(state)
	record.EnrollmentExpires = parseStamp(enrollmentExpires)
	record.EnrolledAt = parseStamp(enrolledAt)
	record.LeaseExpires = parseStamp(leaseExpires)
	record.LastHeartbeatAt = parseStamp(heartbeat)
	if err := json.Unmarshal(facts, &record.Facts); err != nil {
		return node.Record{}, fmt.Errorf("decode node facts: %w", err)
	}
	if err := json.Unmarshal(purchase, &record.Purchase); err != nil {
		return node.Record{}, fmt.Errorf("decode node purchase: %w", err)
	}
	return record, nil
}

type querier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func scanOperation(ctx context.Context, db querier, nodeID, operationID string) (node.Operation, bool, error) {
	operation := node.Operation{NodeID: nodeID, OperationID: operationID}
	var kind, state, issued, settled string
	err := db.QueryRowContext(ctx, `
		SELECT kind, fencing_token, state, issued_at, settled_at, failure, payload
		FROM node_operations WHERE node_id = ? AND operation_id = ?`,
		nodeID, operationID,
	).Scan(&kind, &operation.FencingToken, &state, &issued, &settled, &operation.Failure, &operation.Payload)
	if errors.Is(err, sql.ErrNoRows) {
		return node.Operation{}, false, nil
	}
	if err != nil {
		return node.Operation{}, false, fmt.Errorf("read node operation: %w", err)
	}
	operation.Kind = node.CommandKind(kind)
	operation.State = node.OperationState(state)
	operation.IssuedAt = parseStamp(issued)
	operation.SettledAt = parseStamp(settled)
	return operation, true, nil
}

func requireRow(result sql.Result, nodeID string) error {
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return fmt.Errorf("%w: %s", node.ErrNotFound, nodeID)
	}
	return nil
}

// stamp writes a time in a form that sorts lexically, so lease comparison is a
// string comparison in SQL rather than a decode of every row.
func stamp(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func parseStamp(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}

var _ node.Store = (*NodeStore)(nil)
