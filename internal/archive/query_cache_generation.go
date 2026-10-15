package archive

import (
	"context"
	"fmt"
	"strings"
)

// These tables supply the cached Library, Usage, Writing, health, delegation,
// transcript and TL1 views. Maintenance outputs (workspace_library/previews,
// MCP projections and binary vector copies), audit records, preferences and
// progress metadata do not change those values. Keep this list alongside new
// cached data dependencies.
// Text-search documents do affect search coverage while their index catches up.
var queryCacheTables = []string{
	"hosts",
	"repositories", "workspaces", "workspace_names", "work_items", "conversations",
	"messages", "message_authorship", "message_authorship_spans", "conversation_launches",
	"conversation_documents", "conversation_identity_links", "conversation_group_membership",
	"declared_native_aliases", "agent_sessions", "agent_session_usage", "agent_session_messages",
	"model_requests", "tool_calls", "tool_ledger_state", "tool_commands", "tool_urls",
	"skill_usages", "conversation_instructions", "tool_usage_daily", "tool_mirror_workspaces",
	"cost_changes", "model_aliases", "summaries", "semantic_documents",
	"change_sets", "change_files", "pull_requests", "work_pr_links",
	"metrics", "metric_ledger", "text_search_documents",
	"tl1_installations", "tl1_flavors", "tl1_tasks", "tl1_attempts", "tl1_candidates",
	"tl1_events", "tl1_review_findings", "tl1_human_touches", "tl1_configuration_health",
}

// Re-observing unchanged inputs refreshes these bookkeeping fields without
// changing any cached view. Metrics IDs are internal keys, not cached facts.
var queryCacheBookkeepingColumns = map[string]map[string]bool{
	"hosts":                  {"first_seen_at": true, "last_seen_at": true},
	"repositories":           {"created_at": true, "updated_at": true},
	"workspaces":             {"indexed_at": true},
	"conversations":          {"origin_host_id": true},
	"conversation_documents": {"indexed_at": true},
	"summaries":              {"created_at": true},
	"semantic_documents":     {"indexed_at": true},
	"metrics":                {"id": true, "observed_at": true},
}

// Triggers run in the source transaction, including writes made by another
// process or an older executable. Rollback also rolls back its generation.
// Installing them at the end of Initialize avoids tracking migration backfills.
func (c *Catalog) ensureQueryCacheGeneration() error {
	tx, finish, err := c.beginTrackedWrite(context.Background(), "query-cache-schema")
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(); finish() }()
	if _, err = tx.Exec(`CREATE TABLE IF NOT EXISTS query_cache_generation (
 id INTEGER PRIMARY KEY CHECK(id=1), value INTEGER NOT NULL);
 INSERT INTO query_cache_generation(id,value) SELECT 1,1
 WHERE NOT EXISTS(SELECT 1 FROM query_cache_generation WHERE id=1)`); err != nil {
		return err
	}
	old, err := queryMaps(tx, `SELECT name FROM sqlite_schema WHERE type='trigger' AND name GLOB 'query_cache_*'`)
	if err != nil {
		return err
	}
	for _, trigger := range old {
		if _, err = tx.Exec("DROP TRIGGER " + sqlIdentifier(firstString(trigger["name"]))); err != nil {
			return err
		}
	}
	for _, table := range queryCacheTables {
		columns, err := queryMaps(tx, "PRAGMA table_info("+sqlIdentifier(table)+")")
		if err != nil {
			return err
		}
		changed := make([]string, 0, len(columns))
		for _, column := range columns {
			field := firstString(column["name"])
			if queryCacheBookkeepingColumns[table][field] {
				continue
			}
			name := sqlIdentifier(field)
			changed = append(changed, "OLD."+name+" IS NOT NEW."+name)
		}
		for _, event := range []string{"INSERT", "UPDATE", "DELETE"} {
			when := ""
			if event == "UPDATE" {
				when = " WHEN " + strings.Join(changed, " OR ")
			}
			// Recreate on Initialize so newly added source columns participate.
			statement := fmt.Sprintf(`CREATE TRIGGER query_cache_%s_%s AFTER %s ON %s%s
 BEGIN UPDATE query_cache_generation SET value=value+1 WHERE id=1; END`, table, event, event, table, when)
			if _, err = tx.Exec(statement); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// projectionReadiness counts small dirty sets, never scans message bodies or
// starts work. Dev can wait for catch-up without burdening cheap liveness probes.
func (c *Catalog) projectionReadiness(ctx context.Context) (map[string]any, error) {
	var library, metrics int64
	var textReady bool
	err := c.DB.QueryRowContext(ctx, `SELECT
 (SELECT COUNT(*) FROM workspace_library_dirty),
 (SELECT COUNT(*) FROM mcp_metric_dirty),
 EXISTS(SELECT 1 FROM meta WHERE key='text_search_documents_version' AND value='1')`).Scan(&library, &metrics, &textReady)
	return map[string]any{"ok": true, "projections_ready": library == 0 && metrics == 0 && textReady,
		"library_pending": library, "metrics_pending": metrics, "text_search_ready": textReady}, err
}
