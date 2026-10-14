package querytable

import (
	"context"
	"database/sql"
	"strings"
)

// Count UTF-8 payload bytes, including error sidecars and caller-supplied
// scratch metadata, before and after materializing a temporary projection.
func (d QueryDatasetV3) stagingSizeSQL(columns []OutputColumn, extra ...string) string {
	parts := append([]string{}, extra...)
	for _, column := range columns {
		for _, alias := range []string{column.ValueAlias, column.ErrorAlias} {
			length := "length(CAST(CAST(" + queryIdent(alias) + " AS TEXT) AS BLOB))"
			if d.Dialect == "postgres" {
				length = "octet_length(CAST(" + queryIdent(alias) + " AS TEXT))"
			}
			parts = append(parts, "COALESCE("+length+",0)")
		}
	}
	return "COALESCE(SUM(" + strings.Join(parts, "+") + "),0)"
}

func (d QueryDatasetV3) checkCalendarStaging(ctx context.Context, tx *sql.Tx, source, sizeSQL string, args []any, maxPopulation int, maxBytes int64) error {
	var count, bytes int64
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*),"+sizeSQL+" FROM "+source, args...).Scan(&count, &bytes); err != nil {
		return err
	}
	if count > int64(maxPopulation) {
		return diagnostic("resource_limit", "calendar population exceeds host budget")
	}
	if bytes > maxBytes {
		return diagnostic("resource_limit", "calendar staging exceeds host byte budget")
	}
	return nil
}
