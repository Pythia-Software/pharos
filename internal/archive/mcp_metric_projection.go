package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

const metricProjectionVersion = "metrics-v2-stored-sessions"

// Projections are invalidated by authoritative table writes, never by MCP audit
// writes. Publication takes the writer lock before reading, so concurrent ingest
// cannot have its dirty marker removed by an obsolete calculation.
func (c *Catalog) ensureMetricSchema() error {
	_, err := c.DB.Exec(`CREATE TABLE IF NOT EXISTS mcp_metric_dirty(workspace_id TEXT PRIMARY KEY REFERENCES workspaces(id) ON DELETE CASCADE);
	CREATE TABLE IF NOT EXISTS mcp_metric_projection(entity TEXT NOT NULL,entity_id TEXT NOT NULL,workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,metrics_json TEXT NOT NULL,total_tokens REAL,cost_usd REAL,tool_error_count REAL,PRIMARY KEY(entity,entity_id));
	CREATE INDEX IF NOT EXISTS mcp_metric_workspace_idx ON mcp_metric_projection(workspace_id);
	CREATE INDEX IF NOT EXISTS mcp_metric_tokens_idx ON mcp_metric_projection(entity,total_tokens);
	CREATE INDEX IF NOT EXISTS mcp_metric_cost_idx ON mcp_metric_projection(entity,cost_usd);
	CREATE INDEX IF NOT EXISTS mcp_metric_errors_idx ON mcp_metric_projection(entity,tool_error_count);
	CREATE TABLE IF NOT EXISTS mcp_stat_snapshots(id TEXT PRIMARY KEY,created_at INTEGER NOT NULL,expires_at INTEGER NOT NULL,request_json TEXT NOT NULL,payload TEXT NOT NULL,secret TEXT NOT NULL);
	CREATE INDEX IF NOT EXISTS mcp_stat_expiry_idx ON mcp_stat_snapshots(expires_at);`)
	if err != nil {
		return err
	}
	dependencies := map[string]func(string) string{
		"workspaces":    func(row string) string { return "SELECT " + row + ".id AS workspace_id" },
		"conversations": func(row string) string { return "SELECT " + row + ".workspace_id AS workspace_id" },
		"messages": func(row string) string {
			return "SELECT workspace_id FROM conversations WHERE id=" + row + ".conversation_id"
		},
		"agent_sessions": func(row string) string { return "SELECT " + row + ".workspace_id AS workspace_id" },
		"agent_session_usage": func(row string) string {
			return "SELECT workspace_id FROM agent_sessions WHERE id=" + row + ".agent_session_id"
		},
		"agent_session_messages": func(row string) string {
			return "SELECT workspace_id FROM agent_sessions WHERE id=" + row + ".agent_session_id"
		},
		"tool_calls": func(row string) string { return "SELECT " + row + ".workspace_id AS workspace_id" },
		"tool_ledger_state": func(row string) string {
			return "SELECT workspace_id FROM conversations WHERE id=" + row + ".conversation_id"
		},
		"metrics":                       func(row string) string { return "SELECT " + row + ".workspace_id AS workspace_id" },
		"conversation_group_membership": func(row string) string { return "SELECT " + row + ".workspace_id AS workspace_id" },
		"cost_changes":                  func(string) string { return "SELECT id AS workspace_id FROM workspaces" },
		"model_aliases":                 func(string) string { return "SELECT id AS workspace_id FROM workspaces" },
	}
	for table, lookup := range dependencies {
		for _, event := range []string{"INSERT", "UPDATE", "DELETE"} {
			marks := []string{}
			rows := []string{"NEW"}
			if event == "DELETE" {
				rows = []string{"OLD"}
			} else if event == "UPDATE" {
				rows = []string{"OLD", "NEW"}
			}
			for _, row := range rows {
				marks = append(marks, `INSERT INTO mcp_metric_dirty SELECT workspace_id FROM (`+lookup(row)+`) WHERE workspace_id IN (SELECT id FROM workspaces) AND workspace_id NOT IN (SELECT workspace_id FROM mcp_metric_dirty);`)
			}
			if _, err = c.DB.Exec(`CREATE TRIGGER IF NOT EXISTS mcp_metric_` + table + `_` + strings.ToLower(event) + ` AFTER ` + event + ` ON ` + table + ` BEGIN ` + strings.Join(marks, " ") + ` END`); err != nil {
				return err
			}
		}
	}
	var version string
	err = c.DB.QueryRow("SELECT value FROM meta WHERE key='mcp_metric_version'").Scan(&version)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if version != metricProjectionVersion {
		_, err = c.DB.Exec(`INSERT OR IGNORE INTO mcp_metric_dirty SELECT id FROM workspaces;
		INSERT INTO meta(key,value) VALUES('mcp_metric_version','` + metricProjectionVersion + `') ON CONFLICT(key) DO UPDATE SET value=excluded.value;`)
	}
	return err
}

func (c *Catalog) refreshMetricProjections(ctx context.Context, batch int, workspaceIDs ...string) (int, error) {
	tx, finish, err := c.beginTrackedWrite(ctx, "metric-projection")
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(); finish() }()
	where, args := "", []any{}
	if workspaceIDs != nil {
		where = " WHERE workspace_id IN (SELECT value FROM json_each(?))"
		args = append(args, jsonText(workspaceIDs))
	}
	args = append(args, batch)
	rows, err := queryMapsContext(ctx, tx, `DELETE FROM mcp_metric_dirty WHERE workspace_id IN (SELECT workspace_id FROM mcp_metric_dirty`+where+` LIMIT ?) RETURNING workspace_id`, args...)
	if err != nil || len(rows) == 0 {
		return 0, err
	}
	book, err := c.loadPriceBookFrom(ctx, tx)
	if err != nil {
		return 0, err
	}
	for _, row := range rows {
		id := firstString(row["workspace_id"])
		if _, err = tx.ExecContext(ctx, "DELETE FROM mcp_metric_projection WHERE workspace_id=?", id); err != nil {
			return 0, err
		}
		if err = c.projectWorkspaceMetrics(ctx, tx, book, id, func(entity, entityID string, values map[string]statMetricValue) error {
			return storeMetricProjection(ctx, tx, entity, entityID, id, values)
		}); err != nil {
			return 0, err
		}
	}
	return len(rows), tx.Commit()
}

// metricProjections are metrics computed for reading but not stored, keyed by
// entity ("workspace" or "conversation") and then entity ID.
type metricProjections map[string]map[string]map[string]statMetricValue

// currentMetrics computes the metrics of workspaces whose projection is dirty
// in tx's snapshot, for a read that must not wait to store them: the Library
// maintenance loop does. Clean workspaces are skipped.
func (c *Catalog) currentMetrics(ctx context.Context, tx *sql.Tx, workspaces []string) (metricProjections, error) {
	fresh := metricProjections{"workspace": {}, "conversation": {}}
	dirty, err := queryMapsContext(ctx, tx, `SELECT workspace_id FROM mcp_metric_dirty WHERE workspace_id IN (SELECT value FROM json_each(?))`, jsonText(workspaces))
	if err != nil || len(dirty) == 0 {
		return fresh, err
	}
	book, err := c.loadPriceBookFrom(ctx, tx)
	if err != nil {
		return nil, err
	}
	for _, row := range dirty {
		if err = c.projectWorkspaceMetrics(ctx, tx, book, firstString(row["workspace_id"]), func(entity, entityID string, values map[string]statMetricValue) error {
			fresh[entity][entityID] = values
			return nil
		}); err != nil {
			return nil, err
		}
	}
	return fresh, nil
}

func absentMetrics(status string) map[string]statMetricValue {
	values := map[string]statMetricValue{}
	for _, d := range metricDefinitions {
		values[d.Name] = statMetricValue{Status: status}
	}
	return values
}

// childConversationQuery asks whether a conversation has children, from
// conversations_parent_idx alone.
const childConversationQuery = "SELECT EXISTS(SELECT 1 FROM conversations WHERE parent_id=?)"

// projectWorkspaceMetrics computes workspace id's metrics and its current
// conversations' metrics from tx, passing each entity's values to store.
func (c *Catalog) projectWorkspaceMetrics(ctx context.Context, tx *sql.Tx, book priceBook, id string, store func(entity, entityID string, values map[string]statMetricValue) error) error {
	conversations, err := queryMapsContext(ctx, tx, `SELECT c.id,c.provider,c.started_at,c.ended_at FROM conversations c WHERE c.workspace_id=? AND `+currentGroupConversation("c"), id)
	if err != nil {
		return err
	}
	workspace := absentMetrics("unavailable")
	for _, name := range []string{"user_turn_count", "tool_call_count", "tool_error_count", "compaction_count"} {
		workspace[name] = metricNumber(0)
	}
	var earliest, latest time.Time
	spanValid := len(conversations) > 0
	missingUsage := map[string]bool{}
	for _, conv := range conversations {
		cid := firstString(conv["id"])
		own := absentMetrics("unavailable")
		countRows, err := queryMapsContext(ctx, tx, `SELECT
		COUNT(CASE WHEN m.role='user' AND m.kind='message' THEN 1 END) turns,
		COUNT(CASE WHEN m.kind='metadata' AND (m.text LIKE '%"subtype":"compact_boundary"%' OR m.text LIKE '%"compaction_trigger":"%') THEN 1 END) compactions,
		COUNT(CASE WHEN m.role='user' AND m.kind='message' AND NOT EXISTS(SELECT 1 FROM agent_session_messages am JOIN agent_sessions a ON a.id=am.agent_session_id WHERE am.message_id=m.id AND a.native_id<>'main') THEN 1 END) own_turns,
		COUNT(CASE WHEN m.kind='metadata' AND (m.text LIKE '%"subtype":"compact_boundary"%' OR m.text LIKE '%"compaction_trigger":"%') AND NOT EXISTS(SELECT 1 FROM agent_session_messages am JOIN agent_sessions a ON a.id=am.agent_session_id WHERE am.message_id=m.id AND a.native_id<>'main') THEN 1 END) own_compactions
		FROM messages m WHERE m.conversation_id=?`, cid)
		if err != nil {
			return err
		}
		count := countRows[0]
		own["user_turn_count"] = metricNumber(float64(integer(count["own_turns"])))
		addMetricCount(workspace, "user_turn_count", float64(integer(count["turns"])))
		// Only providers with a retained event contract can establish zero.
		if provider := firstString(conv["provider"]); provider == "codex" || provider == "claude" || integer(count["compactions"]) > 0 {
			own["compaction_count"] = metricNumber(float64(integer(count["own_compactions"])))
			addMetricCount(workspace, "compaction_count", float64(integer(count["compactions"])))
		} else {
			workspace["compaction_count"] = statMetricValue{Value: workspace["compaction_count"].Value, Status: "partial"}
		}
		calls, err := queryMapsContext(ctx, tx, `SELECT COUNT(*) calls,COUNT(CASE WHEN t.status='error' THEN 1 END) errors,
		COUNT(CASE WHEN a.native_id='main' OR t.agent_session_id IS NULL THEN 1 END) own_calls,
		COUNT(CASE WHEN t.status='error' AND (a.native_id='main' OR t.agent_session_id IS NULL) THEN 1 END) own_errors
		FROM tool_calls t LEFT JOIN agent_sessions a ON a.id=t.agent_session_id AND a.conversation_id=t.conversation_id
		WHERE t.conversation_id=?`, cid)
		if err != nil {
			return err
		}
		var ledgerVersion string
		e := tx.QueryRowContext(ctx, "SELECT version FROM tool_ledger_state WHERE conversation_id=?", cid).Scan(&ledgerVersion)
		if e != nil && e != sql.ErrNoRows {
			return e
		}
		status := "complete"
		if ledgerVersion != toolLedgerVersion {
			status = "pending"
		}
		for _, pair := range [][3]string{{"tool_call_count", "calls", "own_calls"}, {"tool_error_count", "errors", "own_errors"}} {
			v := metricNumber(float64(integer(calls[0][pair[2]])))
			v.Status = status
			own[pair[0]] = v
			addMetricCount(workspace, pair[0], float64(integer(calls[0][pair[1]])))
			if status == "pending" {
				v = workspace[pair[0]]
				v.Status = "pending"
				workspace[pair[0]] = v
			}
		}
		start, sok := parseTime(firstString(conv["started_at"]))
		end, eok := parseTime(firstString(conv["ended_at"]))
		if sok && eok && !end.Before(start) {
			own["wall_clock_span_seconds"] = metricNumber(end.Sub(start).Seconds())
			if earliest.IsZero() || start.Before(earliest) {
				earliest = start
			}
			if latest.IsZero() || end.After(latest) {
				latest = end
			}
		} else {
			spanValid = false
		}
		usage, err := queryMapsContext(ctx, tx, `SELECT u.*,a.id session_id,a.native_id,a.model session_model,a.usage_status
		FROM agent_session_usage u JOIN agent_sessions a ON a.id=u.agent_session_id WHERE a.conversation_id=? AND a.usage_status<>'in-child-conversation'`, cid)
		if err != nil {
			return err
		}
		ownUsage := []map[string]any{}
		hasChildren := false
		for _, u := range usage {
			if firstString(u["native_id"]) != "main" {
				hasChildren = true
			}
		}
		var childExists bool
		if err = tx.QueryRowContext(ctx, childConversationQuery, cid).Scan(&childExists); err != nil {
			return err
		}
		hasChildren = hasChildren || childExists
		unassigned := false
		for _, u := range usage {
			if firstString(u["native_id"]) == "main" {
				if firstString(conv["provider"]) == "claude" && hasChildren && firstString(u["attribution"]) == "session-remainder" {
					unassigned = true
					continue
				}
				ownUsage = append(ownUsage, u)
			}
		}
		usageMetrics(own, ownUsage, book)
		if unassigned {
			for _, name := range []string{"total_tokens", "input_tokens", "output_tokens", "cache_read_input_tokens", "cost_usd"} {
				v := own[name]
				if v.Value != nil {
					v.Status = "partial"
				} else {
					v.Status = "unattributable"
				}
				own[name] = v
			}
		}
		// Workspace usage includes embedded delegates, once, and linked children
		// replace in-child-conversation placeholders in the existing ledger.
		part := absentMetrics("unavailable")
		usageMetrics(part, usage, book)
		for _, name := range []string{"total_tokens", "input_tokens", "output_tokens", "cache_read_input_tokens", "cost_usd"} {
			if part[name].Value == nil {
				missingUsage[name] = true
			}
		}
		mergeUsageMetrics(workspace, part)
		if err = metricFallback(ctx, tx, id, cid, own); err != nil {
			return err
		}
		if err = store("conversation", cid, own); err != nil {
			return err
		}
	}
	if spanValid {
		workspace["wall_clock_span_seconds"] = metricNumber(latest.Sub(earliest).Seconds())
	}
	for name := range missingUsage {
		v := workspace[name]
		if v.Value != nil {
			v.Status = "partial"
			workspace[name] = v
		}
	}
	if err = metricFallback(ctx, tx, id, "", workspace); err != nil {
		return err
	}
	return store("workspace", id, workspace)
}

func addMetricCount(values map[string]statMetricValue, name string, n float64) {
	v := values[name]
	if v.Value != nil {
		n += *v.Value
	}
	v.Value = &n
	values[name] = v
}

func usageMetrics(values map[string]statMetricValue, rows []map[string]any, book priceBook) {
	if len(rows) == 0 {
		return
	}
	totals := map[string]float64{}
	var cost any
	status := ""
	partial := false
	for _, row := range rows {
		for _, name := range []string{"total_tokens", "input_tokens", "output_tokens", "cache_read_input_tokens"} {
			totals[name] += float64(integer(row[name]))
		}
		partial = partial || integer(row["unclassified_tokens"]) > 0
		day := ""
		if hour, ok := parseTime(firstString(row["usage_hour"])); ok {
			day = localMidnight(hour, time.Local).Format("2006-01-02")
		}
		model := firstString(row["model"], row["session_model"])
		p := book.cost(model, day, ledgerTokens(row))
		cost = addCost(cost, p.cost)
		status = worsePriceStatus(status, p.status)
	}
	for name, n := range totals {
		v := metricNumber(n)
		if partial && name != "total_tokens" {
			v.Status = "partial"
		}
		values[name] = v
	}
	if n, ok := number(cost); ok {
		v := metricNumber(n)
		v.PriceStatus = status
		if status == "assumed" {
			v.Status = "assumed"
		} else if status != "priced" {
			v.Status = "partial"
		}
		values["cost_usd"] = v
	} else {
		values["cost_usd"] = statMetricValue{Status: "unavailable", PriceStatus: status}
	}
}

func mergeUsageMetrics(dest, src map[string]statMetricValue) {
	for _, name := range []string{"total_tokens", "input_tokens", "output_tokens", "cache_read_input_tokens", "cost_usd"} {
		s := src[name]
		priceStatus := worsePriceStatus(dest[name].PriceStatus, s.PriceStatus)
		if s.Value == nil {
			v := dest[name]
			v.PriceStatus = priceStatus
			dest[name] = v
			continue
		}
		d := dest[name]
		n := *s.Value
		if d.Value != nil {
			n += *d.Value
		}
		status := s.Status
		if d.Value != nil && d.Status != "complete" {
			status = d.Status
		}
		if s.Status == "partial" {
			status = "partial"
		}
		dest[name] = statMetricValue{Value: &n, Status: status, PriceStatus: priceStatus}
	}
}

// Only token evidence is a fallback; legacy textual error metrics are never
// promoted to tool failures. Deterministic selection prevents scope/version rows
// from silently overwriting one another.
func metricFallback(ctx context.Context, tx *sql.Tx, workspace, cid string, values map[string]statMetricValue) error {
	rows, err := queryMapsContext(ctx, tx, `SELECT name,value,status,coverage FROM metrics WHERE workspace_id=? AND COALESCE(conversation_id,'')=? AND name IN ('total_tokens','input_tokens','output_tokens','cache_read_input_tokens') ORDER BY observed_at DESC,extractor_version DESC,id DESC`, workspace, cid)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, row := range rows {
		name := firstString(row["name"])
		if seen[name] || values[name].Value != nil && (cid != "" || values[name].Status == "complete") {
			continue
		}
		seen[name] = true
		if n, ok := number(row["value"]); ok && n >= 0 && !math.IsNaN(n) && !math.IsInf(n, 0) {
			v := metricNumber(n)
			status := firstString(row["status"])
			if status == "unknown" || status == "unavailable" {
				continue
			}
			if coverage, ok := number(row["coverage"]); ok && coverage < 1 {
				v.Status = "partial"
			}
			if status == "partial" || status == "estimated" {
				v.Status = "partial"
			}
			if prior := values[name]; prior.Value != nil && (v.Status != "complete" || n < *prior.Value) {
				continue
			}
			values[name] = v
		}
	}
	return nil
}

// A conversation can move between workspaces. Its new workspace may publish
// before the old workspace clears its projection, so replace that same entity.
func storeMetricProjection(ctx context.Context, tx *sql.Tx, entity, id, workspace string, values map[string]statMetricValue) error {
	data, err := json.Marshal(values)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO mcp_metric_projection(entity,entity_id,workspace_id,metrics_json,total_tokens,cost_usd,tool_error_count) VALUES(?,?,?,?,?,?,?)
 ON CONFLICT(entity,entity_id) DO UPDATE SET workspace_id=excluded.workspace_id,metrics_json=excluded.metrics_json,total_tokens=excluded.total_tokens,cost_usd=excluded.cost_usd,tool_error_count=excluded.tool_error_count`, entity, id, workspace, string(data), values["total_tokens"].effective("complete"), values["cost_usd"].effective("complete"), values["tool_error_count"].effective("complete"))
	return err
}

func decodeMetrics(raw any) (map[string]statMetricValue, error) {
	values := absentMetrics("pending")
	if raw == nil {
		return values, nil
	}
	if err := json.Unmarshal([]byte(firstString(raw)), &values); err != nil {
		return nil, fmt.Errorf("metric projection: %w", err)
	}
	return values, nil
}
