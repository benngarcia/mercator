package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

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
