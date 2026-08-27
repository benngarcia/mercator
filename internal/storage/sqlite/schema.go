package sqlite

import (
	"context"

	"github.com/benngarcia/mercator/internal/sqliteutil"
)

type schemaQueryer = sqliteutil.ColumnQueryer

func tableHasColumn(ctx context.Context, db schemaQueryer, table, column string) (bool, error) {
	return sqliteutil.HasColumn(ctx, db, table, column)
}
