package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/benngarcia/mercator/internal/connection"
	"github.com/benngarcia/mercator/internal/credential"
	"github.com/benngarcia/mercator/internal/eventlog"
)

// flattenWorkspaceSchema owns the one transition from a partitioned Mercator
// database to one deployment-global schema. Every table moves in the same
// SQLite transaction, so any identity collision leaves the complete database
// in its pre-upgrade shape.
func flattenWorkspaceSchema(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin workspace removal: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := prepareLegacyNodeSchema(ctx, tx); err != nil {
		return err
	}
	if err := discardDuplicateDeletedConnections(ctx, tx); err != nil {
		return err
	}
	for _, migrate := range []func(context.Context, *sql.Tx) error{
		eventlog.FlattenWorkspacePartitions,
		credential.FlattenWorkspacePartitions,
		flattenRentalSchedulesTx,
		flattenRunsTx,
		flattenNodesTx,
		flattenRentalsTx,
	} {
		if err := migrate(ctx, tx); err != nil {
			return err
		}
	}
	if err := dropLegacyWorkspaceCatalogTx(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit workspace removal: %w", err)
	}
	return nil
}

func discardDuplicateDeletedConnections(ctx context.Context, tx *sql.Tx) error {
	partitioned, err := tableHasColumn(ctx, tx, "events", "workspace_id")
	if err != nil || !partitioned {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT stream_id
		FROM events
		WHERE stream_type = 'connection'
		GROUP BY stream_id
		HAVING COUNT(DISTINCT workspace_id) > 1`)
	if err != nil {
		return fmt.Errorf("find duplicate legacy connections: %w", err)
	}
	var connectionIDs []string
	for rows.Next() {
		var connectionID string
		if err := rows.Scan(&connectionID); err != nil {
			_ = rows.Close()
			return fmt.Errorf("read duplicate legacy connection: %w", err)
		}
		connectionIDs = append(connectionIDs, connectionID)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("read duplicate legacy connections: %w", err)
	}

	credentialsExist, err := tableHasColumn(ctx, tx, "connection_secret", "connection_id")
	if err != nil {
		return err
	}
	for _, connectionID := range connectionIDs {
		var activeCopies int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*)
			FROM events AS terminal
			JOIN (
				SELECT workspace_id, MAX(stream_version) AS stream_version
				FROM events
				WHERE stream_type = 'connection' AND stream_id = ?
				GROUP BY workspace_id
			) AS latest
			ON latest.workspace_id = terminal.workspace_id
			AND latest.stream_version = terminal.stream_version
			WHERE terminal.stream_type = 'connection'
			AND terminal.stream_id = ?
			AND terminal.event_type <> ?`, connectionID, connectionID, connection.EventConnectionDeleted).Scan(&activeCopies); err != nil {
			return fmt.Errorf("inspect duplicate legacy connection %s: %w", connectionID, err)
		}
		if activeCopies != 0 {
			return fmt.Errorf("workspace migration cannot flatten duplicate active connection %s", connectionID)
		}
		if credentialsExist {
			var credentials int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM connection_secret WHERE connection_id = ?`, connectionID).Scan(&credentials); err != nil {
				return fmt.Errorf("inspect duplicate legacy connection credential %s: %w", connectionID, err)
			}
			if credentials != 0 {
				return fmt.Errorf("workspace migration cannot discard deleted duplicate connection %s: %d credential rows remain", connectionID, credentials)
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM events WHERE stream_type = 'connection' AND stream_id = ?`, connectionID); err != nil {
			return fmt.Errorf("discard duplicate deleted connection %s: %w", connectionID, err)
		}
	}
	return nil
}

// prepareLegacyNodeSchema completes additions older Mercator releases made
// before flattening. These writes remain inside the outer migration and vanish
// with it if any later collision refuses the upgrade.
func prepareLegacyNodeSchema(ctx context.Context, tx *sql.Tx) error {
	partitioned, err := tableHasColumn(ctx, tx, "nodes", "workspace_id")
	if err != nil || !partitioned {
		return err
	}
	for _, statement := range []string{createNodeOperations, createNodeEvents, createNodeWorkloads} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("prepare legacy nodes: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, addNodePurchase); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		return fmt.Errorf("prepare legacy nodes: %w", err)
	}
	return nil
}

func dropLegacyWorkspaceCatalogTx(ctx context.Context, tx *sql.Tx) error {
	for _, statement := range []string{
		`DROP TABLE IF EXISTS workspace_members`,
		`DROP TABLE IF EXISTS workspaces`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("drop legacy workspace catalog: %w", err)
		}
	}
	return nil
}
