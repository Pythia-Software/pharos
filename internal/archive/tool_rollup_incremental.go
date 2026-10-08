package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

var errToolPublicationChanged = errors.New("another tool rollup was published during construction")

type toolRollupBuild struct {
	full                                                 bool
	generation, day, fullRevision, publication, timezone string
}

func (c *Catalog) updateToolRollup(ctx context.Context, generation, day string, force bool) error {
	for attempt := 0; attempt < 3; attempt++ {
		err := c.constructToolRollup(ctx, generation, day, force)
		if !errors.Is(err, errToolPublicationChanged) {
			return err
		}
	}
	return errToolPublicationChanged
}

// Construction holds a WAL read snapshot, and writes only connection-local
// temporary tables. The published cube and its dirty tokens come from that same
// snapshot; ingest can continue, and newer tokens survive publication.
func (c *Catalog) constructToolRollup(ctx context.Context, generation, day string, force bool) error {
	conn, err := c.DB.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	for _, name := range []string{"tool_cube_merge", "tool_mirror_build", "tool_cube_build", "tool_dirty_build", "tool_day_build", "tool_daily_build"} {
		kind := "TABLE"
		if name == "tool_cube_merge" {
			kind = "VIEW"
		}
		if _, err := conn.ExecContext(ctx, "DROP "+kind+" IF EXISTS temp."+name); err != nil {
			return err
		}
		defer conn.ExecContext(context.Background(), "DROP "+kind+" IF EXISTS temp."+name)
	}
	build, err := c.prepareToolRollup(ctx, conn, generation, day, force)
	if err != nil {
		return err
	}
	return c.retryCatalogWrite(ctx, "tool-rollup-publish", nil, func() error { return c.publishToolPartitions(ctx, conn, build) })
}

func (c *Catalog) prepareToolRollup(ctx context.Context, conn *sql.Conn, generation, day string, force bool) (build toolRollupBuild, err error) {
	snapshot, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return build, err
	}
	defer snapshot.Rollback()
	built, err := readMeta(snapshot, ctx, "tool_rollup_generation")
	if err != nil {
		return build, err
	}
	builtDay, err := readMeta(snapshot, ctx, "tool_rollup_day")
	if err != nil {
		return build, err
	}
	fullRev, err := readMeta(snapshot, ctx, "tool_rollup_full_revision")
	if err != nil {
		return build, err
	}
	builtRev, err := readMeta(snapshot, ctx, "tool_rollup_built_full_revision")
	if err != nil {
		return build, err
	}
	publication, err := readMeta(snapshot, ctx, "tool_rollup_publication")
	if err != nil {
		return build, err
	}
	ledger, err := readMeta(snapshot, ctx, "tool_ledger_generation")
	if err != nil {
		return build, err
	}
	generation = ledger + "/" + toolRollupVersion
	builtZone, err := readMeta(snapshot, ctx, "tool_rollup_timezone")
	if err != nil {
		return build, err
	}
	zone := toolRollupTimezone()
	build = toolRollupBuild{timezone: zone, full: force || !strings.HasSuffix(built, "/"+toolRollupVersion) || builtDay != day || builtRev != fullRev || builtZone != zone,
		generation: generation, day: day, fullRevision: fullRev, publication: publication}
	if _, err := snapshot.ExecContext(ctx, "CREATE TEMP TABLE tool_dirty_build AS SELECT * FROM tool_rollup_dirty"); err != nil {
		return build, err
	}
	if _, err := snapshot.ExecContext(ctx, "CREATE INDEX temp.tool_dirty_build_idx ON tool_dirty_build(workspace_id)"); err != nil {
		return build, err
	}
	var dirty int
	if err := snapshot.QueryRowContext(ctx, "SELECT COUNT(*) FROM temp.tool_dirty_build").Scan(&dirty); err != nil {
		return build, err
	}
	// Unpartitioned invalidation by an older writer cannot safely reuse the cube.
	if generation != built && dirty == 0 {
		build.full = true
	}
	workspaces, err := queryMapsContext(ctx, snapshot, `SELECT w.id,w.source_kind,w.activity_at FROM workspaces w
 WHERE EXISTS(SELECT 1 FROM tool_calls t WHERE t.workspace_id=w.id)`)
	if err != nil {
		return build, err
	}
	links, err := queryMapsContext(ctx, snapshot, `SELECT a.workspace_id left_workspace,b.workspace_id right_workspace FROM conversation_identity_links l
 JOIN conversations a ON a.id=l.left_id JOIN conversations b ON b.id=l.right_id`)
	if err != nil {
		return build, err
	}
	if _, err := snapshot.ExecContext(ctx, "CREATE TEMP TABLE tool_mirror_build(workspace_id TEXT PRIMARY KEY)"); err != nil {
		return build, err
	}
	for _, row := range suppressMirrorRows(workspaces, links) {
		for _, id := range row["mirrored_workspace_ids"].([]string) {
			if _, err := snapshot.ExecContext(ctx, "INSERT INTO temp.tool_mirror_build VALUES(?)", id); err != nil {
				return build, err
			}
		}
	}
	// A representative change suppresses/restores entire workspaces, even if
	// the changed call belongs only to the other member of the mirror group.
	if _, err := snapshot.ExecContext(ctx, `INSERT INTO temp.tool_dirty_build
 SELECT workspace_id,'' FROM (SELECT workspace_id FROM tool_mirror_workspaces EXCEPT SELECT workspace_id FROM temp.tool_mirror_build)
 WHERE workspace_id NOT IN (SELECT workspace_id FROM temp.tool_dirty_build)
 UNION ALL SELECT workspace_id,'' FROM (SELECT workspace_id FROM temp.tool_mirror_build EXCEPT SELECT workspace_id FROM tool_mirror_workspaces)
 WHERE workspace_id NOT IN (SELECT workspace_id FROM temp.tool_dirty_build)`); err != nil {
		return build, err
	}
	cubeQuery := toolCubeSelect("temp.tool_mirror_build")
	if !build.full {
		cubeQuery = strings.Replace(cubeQuery, " GROUP BY ", " AND t.workspace_id IN (SELECT workspace_id FROM temp.tool_dirty_build) GROUP BY ", 1)
	}
	if _, err := snapshot.ExecContext(ctx, "CREATE TEMP TABLE tool_cube_build AS "+cubeQuery); err != nil {
		return build, err
	}
	if build.full {
		if _, err := snapshot.ExecContext(ctx, "CREATE TEMP VIEW tool_cube_merge AS SELECT * FROM temp.tool_cube_build"); err != nil {
			return build, err
		}
	} else {
		if _, err := snapshot.ExecContext(ctx, `CREATE TEMP TABLE tool_day_build AS SELECT DISTINCT day FROM main.tool_call_cube
 WHERE workspace_id IN (SELECT workspace_id FROM temp.tool_dirty_build)
 UNION SELECT DISTINCT day FROM temp.tool_cube_build`); err != nil {
			return build, err
		}
		if _, err := snapshot.ExecContext(ctx, `CREATE TEMP VIEW tool_cube_merge AS
 SELECT * FROM main.tool_call_cube WHERE workspace_id NOT IN (SELECT workspace_id FROM temp.tool_dirty_build)
 AND (day IN (SELECT day FROM temp.tool_day_build) OR day IS NULL AND EXISTS(SELECT 1 FROM temp.tool_day_build WHERE day IS NULL))
 UNION ALL SELECT * FROM temp.tool_cube_build`); err != nil {
			return build, err
		}
	}
	// Daily grouping and pricing also happen before acquiring the writer.
	book, err := c.loadPriceBookFrom(ctx, snapshot)
	if err != nil {
		return build, err
	}
	daily, err := queryMapsContext(ctx, snapshot, strings.Replace(toolRollupFromCube, "FROM tool_call_cube", "FROM temp.tool_cube_merge", 1))
	if err != nil {
		return build, err
	}
	if _, err := snapshot.ExecContext(ctx, "CREATE TEMP TABLE tool_daily_build AS SELECT * FROM tool_usage_daily WHERE 0"); err != nil {
		return build, err
	}
	stmt, err := snapshot.PrepareContext(ctx, "INSERT INTO temp.tool_daily_build("+strings.Join(toolUsageColumns, ",")+") VALUES("+placeholders(len(toolUsageColumns))+")")
	if err != nil {
		return build, err
	}
	for _, row := range daily {
		record := toolUsageRecord(book, row)
		values := make([]any, len(toolUsageColumns))
		for i, column := range toolUsageColumns {
			values[i] = record[column]
		}
		if _, err := stmt.ExecContext(ctx, values...); err != nil {
			stmt.Close()
			return build, err
		}
	}
	if err := stmt.Close(); err != nil {
		return build, err
	}
	if err := snapshot.Commit(); err != nil {
		return build, err
	}
	return build, nil
}

func (c *Catalog) publishToolPartitions(ctx context.Context, conn *sql.Conn, build toolRollupBuild) error {
	release, err := c.acquireWriteGate(ctx)
	if err != nil {
		return err
	}
	defer release()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM meta WHERE 0"); err != nil {
		return err
	}
	finish := c.trackWriter("tool-rollup")
	defer finish()
	started := time.Now()
	publication, err := readMeta(tx, ctx, "tool_rollup_publication")
	if err != nil {
		return err
	}
	if publication != build.publication {
		return errToolPublicationChanged
	}
	statements := []string{"DELETE FROM tool_mirror_workspaces", "INSERT INTO tool_mirror_workspaces SELECT * FROM temp.tool_mirror_build"}
	if build.full {
		statements = append(statements, "DROP TABLE IF EXISTS main.tool_call_cube", "CREATE TABLE main.tool_call_cube AS SELECT * FROM temp.tool_cube_build",
			"CREATE INDEX tool_call_cube_workspace_idx ON tool_call_cube(workspace_id)", "CREATE INDEX tool_call_cube_day_idx ON tool_call_cube(day)", "DELETE FROM tool_usage_daily")
	} else {
		statements = append(statements, "DELETE FROM tool_call_cube WHERE workspace_id IN (SELECT workspace_id FROM temp.tool_dirty_build)",
			"INSERT INTO tool_call_cube SELECT * FROM temp.tool_cube_build", `DELETE FROM tool_usage_daily WHERE day IN (SELECT day FROM temp.tool_day_build)
  OR day IS NULL AND EXISTS(SELECT 1 FROM temp.tool_day_build WHERE day IS NULL)`)
	}
	statements = append(statements, "INSERT INTO tool_usage_daily SELECT * FROM temp.tool_daily_build",
		`DELETE FROM tool_rollup_dirty WHERE EXISTS(SELECT 1 FROM temp.tool_dirty_build b WHERE b.workspace_id=tool_rollup_dirty.workspace_id AND b.revision=tool_rollup_dirty.revision)`)
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	for key, value := range map[string]string{"tool_rollup_generation": build.generation, "tool_rollup_day": build.day, "tool_rollup_timezone": build.timezone, "tool_rollup_built_full_revision": build.fullRevision,
		"tool_rollup_writer_ms": fmt.Sprint(time.Since(started).Milliseconds())} {
		if _, err := tx.ExecContext(ctx, "INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, value); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('tool_rollup_publication',hex(randomblob(16))) ON CONFLICT(key) DO UPDATE SET value=excluded.value`); err != nil {
		return err
	}
	return tx.Commit()
}

func toolRollupTimezone() string { return time.Local.String() + ":" + os.Getenv("TZ") }
