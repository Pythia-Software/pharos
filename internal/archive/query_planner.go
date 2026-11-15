package archive

import (
	"context"
	"database/sql"
	"fmt"
)

// Analyze only query-source tables, with bounded index samples. A catalog also
// contains large FTS/vector shadow tables; an unrestricted optimize on a fresh
// connection can spend minutes analyzing those before the UI is ready.
var queryPlannerTables = []string{
	"tool_calls", "tool_usage_daily", "skill_usages", "message_authorship",
	"messages", "conversations", "workspaces", "repositories", "agent_sessions",
}

func (c *Catalog) ensureQueryPlannerStatistics(ctx context.Context) error {
	conn, err := c.DB.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	for _, index := range machineCallIndexes {
		if _, err := conn.ExecContext(ctx, index); err != nil {
			return err
		}
	}
	var cube bool
	if err := conn.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE type='table' AND name='tool_call_cube')").Scan(&cube); err != nil {
		return err
	}
	if cube {
		if _, err := conn.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS tool_call_cube_count_idx ON tool_call_cube(call_count)"); err != nil {
			return err
		}
	}
	var limit int
	if err := conn.QueryRowContext(ctx, "PRAGMA analysis_limit").Scan(&limit); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "PRAGMA analysis_limit=1000"); err != nil {
		return err
	}
	defer conn.ExecContext(context.WithoutCancel(ctx), fmt.Sprintf("PRAGMA analysis_limit=%d", limit))
	missing, err := plannerTablesToAnalyze(ctx, conn)
	if err != nil {
		return err
	}
	for _, table := range queryPlannerTables {
		if missing[table] {
			if _, err := conn.ExecContext(ctx, "ANALYZE "+sqlIdentifier(table)); err != nil {
				return err
			}
		}
	}
	return nil
}

// plannerTablesToAnalyze are the query-source tables without statistics,
// or with an index that has none. The startup upgrade's plan reads it too
// (initialize_progress.go), before the indexes this build adds exist.
func plannerTablesToAnalyze(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (map[string]bool, error) {
	var exists bool
	if err := q.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE name='sqlite_stat1')").Scan(&exists); err != nil {
		return nil, err
	}
	missing := map[string]bool{}
	for _, table := range queryPlannerTables {
		var analyzed bool
		if exists {
			if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_stat1 WHERE tbl=?)
 AND NOT EXISTS(SELECT 1 FROM sqlite_schema i WHERE i.type='index' AND i.tbl_name=? AND i.sql IS NOT NULL
 AND NOT EXISTS(SELECT 1 FROM sqlite_stat1 s WHERE s.idx=i.name))`, table, table).Scan(&analyzed); err != nil {
				return nil, err
			}
		}
		missing[table] = !analyzed
	}
	return missing, nil
}
