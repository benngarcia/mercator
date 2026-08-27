package sqliteutil

import (
	"context"
	"database/sql"
)

type ColumnQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func HasColumn(ctx context.Context, db ColumnQueryer, table, column string) (bool, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}
