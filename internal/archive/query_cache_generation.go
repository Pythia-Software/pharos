package archive

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// These tables supply the cached Library, Usage, Writing, health, delegation,
// transcript, TL1 and Parallelism views. Maintenance outputs (workspace_library/
// previews, MCP projections, binary vector copies and parallelism_activity),
// audit records, preferences and progress metadata do not change those values.
// Keep this list alongside new cached data dependencies.
// Text-search documents do affect search coverage while their index catches up.
// Origin publication is also observable data: it can follow its source commit,
// so readers of the previous projection must be invalidated when it is saved.
var queryCacheTables = []string{
	"hosts", "machines", "host_machines", "origin_observations", "conversation_sightings", "conversation_origins",
	"repositories", "workspaces", "workspace_names", "work_items", "conversations",
	"messages", "message_authorship", "message_authorship_spans", "conversation_launches", "work_peer_links",
	"conversation_documents", "conversation_identity_links", "conversation_group_membership",
	"conversation_name_candidates", "authorship_message_state",
	"declared_native_aliases", "agent_sessions", "agent_session_usage", "agent_session_messages",
	"agent_session_token_evidence", "usage_attribution_state",
	"model_requests", "tool_calls", "tool_ledger_state", "tool_commands", "tool_urls",
	"skill_usages", "conversation_instructions", "tool_usage_daily", "tool_mirror_workspaces",
	"cost_changes", "model_aliases", "summaries", "semantic_documents",
	"change_sets", "change_files", "pull_requests", "work_pr_links",
	"metrics", "metric_ledger", "text_search_documents", "reasoning_extraction_state",
	"tl1_installations", "tl1_flavors", "tl1_tasks", "tl1_attempts", "tl1_candidates",
	"tl1_events", "tl1_review_findings", "tl1_human_touches", "tl1_configuration_health",
	"agent_events", "agent_event_messages",
	// Typed execution evidence decides parents, bindings and occurrence
	// ownership in delegation, quick stats, activity and parallelism; a
	// backfill writes nothing else. Publication never writes it.
	"execution_claims", "execution_evidence_state",
	// One stamp per rebuilt conversation; its facts are written in the same
	// transaction.
	"agent_event_fact_conversations",
	"message_labels", "message_labels_models", "message_labels_conversations", "message_labels_dirty",
	// Enqueue and publication invalidate the label view once per conversation;
	// individual fact/membership writes are internal to that atomic refresh.
	"message_label_facts_dirty",
}

// queryCacheDomains are the tables only some cached views read: what the
// labeling and agent-event passes write, one conversation at a time, for
// hours on a large library. A write to one advances its domain's count as
// well as the generation, so a view that reads neither (cacheGenerations)
// is not recomputed after every conversation those passes finish.
var queryCacheDomains = map[string]string{
	"message_labels": "labels", "message_labels_models": "labels", "message_labels_conversations": "labels",
	"message_labels_dirty": "labels", "message_label_facts_dirty": "labels",
	"agent_events": "events", "agent_event_messages": "events", "agent_event_fact_conversations": "events",
}

// cacheGenerations are the cached-view generation and, of its advances, how
// many were writes to a domain's tables alone.
type cacheGenerations struct{ all, labels, events int64 }

// A view's generation counts the advances that can change it. The whole
// generation is always safe, and is what an older executable's triggers
// advance alone.
const (
	generationAll    = "all"
	generationEvents = "events" // everything but label writes
	generationCore   = "core"   // neither label nor agent-event writes
)

func (g cacheGenerations) of(scope string) int64 {
	switch scope {
	case generationCore:
		return g.all - g.labels - g.events
	case generationEvents:
		return g.all - g.labels
	}
	return g.all
}

// cacheGenerationsSQL reads cacheGenerations in one statement, so the three
// counts describe one snapshot. A catalog without the domain table (not yet
// initialized by this build) fails it; readCacheGenerations falls back.
const cacheGenerationsSQL = `SELECT value,
 COALESCE((SELECT value FROM query_cache_domains WHERE name='labels'),0),
 COALESCE((SELECT value FROM query_cache_domains WHERE name='events'),0)
 FROM query_cache_generation WHERE id=1`

type rowQueryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func readCacheGenerations(ctx context.Context, q rowQueryer) (cacheGenerations, error) {
	var g cacheGenerations
	err := q.QueryRowContext(ctx, cacheGenerationsSQL).Scan(&g.all, &g.labels, &g.events)
	if err != nil && strings.Contains(err.Error(), "no such table: query_cache_domains") {
		g = cacheGenerations{}
		err = q.QueryRowContext(ctx, "SELECT value FROM query_cache_generation WHERE id=1").Scan(&g.all)
	}
	return g, err
}

// advanceCacheGeneration is for a change no trigger sees (a publication
// recorded in meta, say): it advances the generation and domain's count,
// as that domain's triggers do.
func advanceCacheGeneration(ctx context.Context, tx *sql.Tx, domain string) error {
	_, err := tx.ExecContext(ctx, "UPDATE query_cache_generation SET value=value+1 WHERE id=1")
	if err == nil {
		_, err = tx.ExecContext(ctx, "UPDATE query_cache_domains SET value=value+1 WHERE name=?", domain)
		if err != nil && strings.Contains(err.Error(), "no such table") {
			err = nil // the whole generation moved, which every view heeds
		}
	}
	return err
}

// Re-observing unchanged inputs refreshes these bookkeeping fields without
// changing any cached view. Metrics IDs are internal keys, not cached facts.
var queryCacheBookkeepingColumns = map[string]map[string]bool{
	"hosts":                  {"first_seen_at": true, "last_seen_at": true},
	"conversation_sightings": {"first_seen_at": true, "last_seen_at": true, "messages": true, "ended_at": true, "origin": true, "source_name": true},
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
 WHERE NOT EXISTS(SELECT 1 FROM query_cache_generation WHERE id=1);
 CREATE TABLE IF NOT EXISTS query_cache_domains (name TEXT PRIMARY KEY, value INTEGER NOT NULL) WITHOUT ROWID;
 INSERT OR IGNORE INTO query_cache_domains(name,value) VALUES('labels',0),('events',0)`); err != nil {
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
		if len(columns) == 0 {
			continue
		} // Optional derived ledgers may not exist yet.
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
			domain := ""
			if name := queryCacheDomains[table]; name != "" {
				domain = " UPDATE query_cache_domains SET value=value+1 WHERE name='" + name + "';"
			}
			statement := fmt.Sprintf(`CREATE TRIGGER query_cache_%s_%s AFTER %s ON %s%s
 BEGIN UPDATE query_cache_generation SET value=value+1 WHERE id=1;%s END`, table, event, event, table, when, domain)
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
	var queryTextReady bool
	err := c.DB.QueryRowContext(ctx, `SELECT
 (SELECT COUNT(*) FROM workspace_library_dirty),
 (SELECT COUNT(*) FROM mcp_metric_dirty),
 EXISTS(SELECT 1 FROM meta WHERE key='text_search_documents_version' AND value='1'),
 (SELECT COUNT(*) FROM meta WHERE key IN ('human_query_text_fts_ready','tool_query_text_fts_ready') AND value='1')=2 AND
 EXISTS(SELECT 1 FROM meta WHERE key='tool_query_facts_ready' AND value=?)`, toolQueryFacts.version()).Scan(&library, &metrics, &textReady, &queryTextReady)
	return map[string]any{"ok": true, "projections_ready": library == 0 && metrics == 0 && textReady && queryTextReady,
		"library_pending": library, "metrics_pending": metrics, "text_search_ready": textReady, "query_text_ready": queryTextReady}, err
}
