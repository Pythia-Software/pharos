package querytable

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
)

// ForeignKey is presentation metadata, not an executable join or permission grant.
type ForeignKey struct {
	SourceTable   string   `json:"sourceTable"`
	SourceColumns []string `json:"sourceColumns"`
	TargetTable   string   `json:"targetTable"`
	TargetColumns []string `json:"targetColumns"`
}

// RelationshipMetadataReader accepts a DB or a host-owned transaction.
type RelationshipMetadataReader interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// DiscoverSQLiteForeignKeys reads declared constraints for host-registered tables
// in the main schema. Table names are bound values, never interpolated SQL. It
// preserves composite-key order and resolves omitted target columns to the PK.
// Hosts map this metadata onto their field origins and registered object types.
func DiscoverSQLiteForeignKeys(ctx context.Context, db RelationshipMetadataReader, tables []string) ([]ForeignKey, error) {
	result := make([]ForeignKey, 0)
	seen := make(map[string]bool)
	for _, table := range tables {
		if table == "" || seen[table] {
			continue
		}
		seen[table] = true
		rows, err := db.QueryContext(ctx, `SELECT id,seq,"table","from","to" FROM pragma_foreign_key_list(?, 'main') ORDER BY id,seq`, table)
		if err != nil {
			return nil, fmt.Errorf("discover foreign keys for %q: %w", table, err)
		}
		constraints := make(map[int]*ForeignKey)
		var order []int
		for rows.Next() {
			var id, seq int
			var target, source string
			var column sql.NullString
			if err = rows.Scan(&id, &seq, &target, &source, &column); err != nil {
				rows.Close()
				return nil, err
			}
			fk := constraints[id]
			if fk == nil {
				fk = &ForeignKey{SourceTable: table, TargetTable: target}
				constraints[id] = fk
				order = append(order, id)
			}
			fk.SourceColumns = append(fk.SourceColumns, source)
			fk.TargetColumns = append(fk.TargetColumns, column.String)
		}
		err = rows.Err()
		closeErr := rows.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
		// Close the cursor before another query: one-connection pools are common.
		for _, id := range order {
			fk := constraints[id]
			missing := false
			for _, column := range fk.TargetColumns {
				missing = missing || column == ""
			}
			if missing {
				columns, err := sqlitePrimaryKey(ctx, db, fk.TargetTable)
				if err != nil {
					return nil, err
				}
				if len(columns) != len(fk.SourceColumns) {
					return nil, fmt.Errorf("foreign key %s -> %s has an unresolved target key", table, fk.TargetTable)
				}
				fk.TargetColumns = columns
			}
			result = append(result, *fk)
		}
	}
	return result, nil
}

func sqlitePrimaryKey(ctx context.Context, db RelationshipMetadataReader, table string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT name,pk FROM pragma_table_info(?, 'main') WHERE pk>0 ORDER BY pk`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type column struct {
		name     string
		position int
	}
	var columns []column
	for rows.Next() {
		var item column
		if err := rows.Scan(&item.name, &item.position); err != nil {
			return nil, err
		}
		columns = append(columns, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(columns, func(i, j int) bool { return columns[i].position < columns[j].position })
	result := make([]string, len(columns))
	for i, column := range columns {
		result[i] = column.name
	}
	return result, nil
}
