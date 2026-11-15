package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
	"time"
)

const metricProjectionVersion = "metrics-v5-estimate-provenance"

// Projections are invalidated by authoritative table writes and by price
// changes that reprice a workspace's usage (replacePricing), never by MCP audit
// writes. A projection is computed from a read snapshot and published in a
// short write (refreshMetricProjections). Before it reads, the refresh claims
// the workspaces it will compute; every write that would mark a workspace
// dirty also releases its claim, so a calculation made obsolete while it ran
// is not published and the workspace stays dirty.
func (c *Catalog) ensureMetricSchema() error {
	_, err := c.DB.Exec(`CREATE TABLE IF NOT EXISTS mcp_metric_dirty(workspace_id TEXT PRIMARY KEY REFERENCES workspaces(id) ON DELETE CASCADE);
	CREATE TABLE IF NOT EXISTS mcp_metric_claims(workspace_id TEXT PRIMARY KEY REFERENCES workspaces(id) ON DELETE CASCADE,token TEXT NOT NULL,claimed_at INTEGER NOT NULL);
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
	}
	// Prices are not a trigger dependency: replacePricing marks the
	// workspaces whose usage a price change reprices. Earlier builds marked
	// every workspace for each price row written.
	for _, table := range []string{"cost_changes", "model_aliases"} {
		for _, event := range []string{"insert", "update", "delete"} {
			if _, err = c.DB.Exec(`DROP TRIGGER IF EXISTS mcp_metric_` + table + `_` + event + `; DROP TRIGGER IF EXISTS mcp_metric_release_` + table + `_` + event); err != nil {
				return err
			}
		}
	}
	for table, lookup := range dependencies {
		for _, event := range []string{"INSERT", "UPDATE", "DELETE"} {
			marks, releases := []string{}, []string{}
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
			// Claims exist only while a refresh computes, so this trigger's
			// body is skipped for nearly every write.
			for _, row := range rows {
				releases = append(releases, `DELETE FROM mcp_metric_claims WHERE workspace_id IN (`+lookup(row)+`);`)
			}
			if _, err = c.DB.Exec(`CREATE TRIGGER IF NOT EXISTS mcp_metric_release_` + table + `_` + strings.ToLower(event) + ` AFTER ` + event + ` ON ` + table +
				` WHEN EXISTS(SELECT 1 FROM mcp_metric_claims) BEGIN ` + strings.Join(releases, " ") + ` END`); err != nil {
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
		DELETE FROM mcp_metric_claims;
		INSERT INTO meta(key,value) VALUES('mcp_metric_version','` + metricProjectionVersion + `') ON CONFLICT(key) DO UPDATE SET value=excluded.value;`)
	}
	return err
}

// metricClaimAbandoned is how old a claim is before another refresh may take
// its workspaces: longer than any batch takes to compute. A refresh that
// outlives its claim publishes nothing for those workspaces.
var metricClaimAbandoned = 10 * time.Minute

// metricProjectionBatch is how many workspaces the service computes from one
// snapshot and publishes in one write. Small enough that the publication
// holds the writer for tens of milliseconds and one written workspace wastes
// little; large enough that a full recomputation is not thousands of commits.
var metricProjectionBatch = 100

// maintainWorkspaceMetrics stores dirty workspaces' metrics as its own
// operation. It does not wait for an index or its analysis chain: a batch is
// computed from a read snapshot and published in one short write, and a
// workspace written meanwhile loses its claim and is computed again later.
// The Search view's Stop and the Search schedule hold it like the view.
func (c *Catalog) maintainWorkspaceMetrics(ctx context.Context) {
	schedule := searchScheduleGate{}
	var pass *daemonPass
	defer func() { pass.end(nil) }()
	for {
		if schedule.wait(ctx, c, &pass) != nil || c.queries.wait(pass.context(ctx)) != nil {
			return
		}
		wait := 2 * time.Second
		if batch, done, running := c.libraryPass(ctx); running {
			if pass == nil {
				pass = c.beginPass(ctx, kindMetrics, triggerChained)
			}
			var count, published int
			var err error
			pass.runIO(batch, func(ctx context.Context) { count, published, err = c.refreshMetricBatch(ctx, metricProjectionBatch) })
			stopped := batch.Err() != nil
			done()
			pass.worked(published)
			if err != nil || count == 0 {
				pass.end(err)
				pass = nil
			}
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				if !stopped {
					fmt.Fprintf(os.Stderr, "Workspace metrics: %v\n", err)
					wait = 5 * time.Second
				}
			} else if published > 0 {
				wait = 0
			} else if count > 0 {
				// Everything it computed was written meanwhile (an index is
				// on those workspaces): let it finish rather than recompute
				// them as fast as they change.
				wait = time.Second
			}
		} else if pass != nil {
			// Stopped: no operation stays open to hold up an eject.
			pass.end(nil)
			pass = nil
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// metricProjectionComputedHook, when set (tests), runs after a refresh has
// computed its batch and before it publishes.
var metricProjectionComputedHook func()

// refreshMetricProjections publishes the projections of up to batch dirty
// workspaces and reports how many it attempted. It holds the catalog writer
// twice, briefly: to claim them, and to store what it computed in between
// from a read snapshot.
func (c *Catalog) refreshMetricProjections(ctx context.Context, batch int, workspaceIDs ...string) (int, error) {
	attempted, _, err := c.refreshMetricBatch(ctx, batch, workspaceIDs...)
	return attempted, err
}

// refreshMetricBatch is refreshMetricProjections, also reporting how many of
// the attempted workspaces it published: fewer when writes released claims
// while it computed.
func (c *Catalog) refreshMetricBatch(ctx context.Context, batch int, workspaceIDs ...string) (attempted, published int, err error) {
	where, args := "", []any{}
	if workspaceIDs != nil {
		where = " WHERE workspace_id IN (SELECT value FROM json_each(?))"
		args = append(args, jsonText(workspaceIDs))
	}
	// A workspace another refresh has claimed (the app's and a CLI's loops
	// overlap) is that refresh's to publish. Its claim stands until it is
	// released, published or abandoned: taking it over would have each
	// refresh revoke the other's and neither store anything.
	abandoned := time.Now().Add(-metricClaimAbandoned).Unix()
	unclaimed := " workspace_id NOT IN (SELECT workspace_id FROM mcp_metric_claims WHERE claimed_at>=?)"
	if where == "" {
		where = " WHERE" + unclaimed
	} else {
		where += " AND" + unclaimed
	}
	args = append(args, abandoned, batch)
	// Nothing dirty and unclaimed: return without waiting for the writer.
	pending, err := queryMapsContext(ctx, c.reader(), `SELECT workspace_id FROM mcp_metric_dirty`+where+` LIMIT ?`, args...)
	if err != nil || len(pending) == 0 {
		return 0, 0, err
	}
	ids := make([]string, 0, len(pending))
	for _, row := range pending {
		ids = append(ids, firstString(row["workspace_id"]))
	}
	// Claim before reading. Only a claim whose refresh never finished is
	// dropped; its workspaces are still dirty. One taken since the read above
	// is kept, and this refresh computes the rest.
	token := randomToken()
	if err := c.writeTransaction(ctx, "metric-projection claim", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM mcp_metric_claims WHERE claimed_at<?`, abandoned); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO mcp_metric_claims(workspace_id,token,claimed_at) SELECT workspace_id,?,? FROM mcp_metric_dirty WHERE workspace_id IN (SELECT value FROM json_each(?))`,
			token, time.Now().Unix(), jsonText(ids))
		return err
	}); err != nil {
		return 0, 0, err
	}
	// Compute what the claim covers from one snapshot that includes it.
	type projection struct {
		entity, id string
		values     map[string]statMetricValue
		json       string
	}
	computed := map[string][]projection{}
	completed := func(error) {}
	if err := func() error {
		snapshot, err := c.reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return err
		}
		defer snapshot.Rollback()
		claimed, err := queryMapsContext(ctx, snapshot, `SELECT workspace_id FROM mcp_metric_claims WHERE token=?`, token)
		if err != nil || len(claimed) == 0 {
			return err
		}
		completed = maintenanceBatch(ctx, snapshot, "Updating workspace metrics", "workspace", len(claimed),
			"SELECT count(*) FROM mcp_metric_dirty")
		book, err := c.loadPriceBookFrom(ctx, snapshot)
		if err != nil {
			return err
		}
		claims := make([]string, 0, len(claimed))
		for _, row := range claimed {
			id := firstString(row["workspace_id"])
			claims, computed[id] = append(claims, id), []projection{}
		}
		if err := c.projectMetrics(ctx, snapshot, book, claims, func(id, entity, entityID string, values map[string]statMetricValue) error {
			data, err := json.Marshal(values)
			computed[id] = append(computed[id], projection{entity, entityID, values, string(data)})
			return err
		}); err != nil {
			return err
		}
		return nil
	}(); err != nil {
		return 0, 0, err
	}
	if metricProjectionComputedHook != nil {
		metricProjectionComputedHook()
	}
	tx, finish, err := c.beginTrackedWrite(ctx, "metric-projection")
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = tx.Rollback(); finish() }()
	for id, rows := range computed {
		// Released since the claim: something it depends on was written, so
		// what was computed may be obsolete. It is still dirty.
		var held int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mcp_metric_claims WHERE workspace_id=? AND token=?`, id, token).Scan(&held); err != nil {
			return 0, 0, err
		}
		if held == 0 {
			continue
		}
		published++
		// Most recomputations change nothing (a version change, or one
		// conversation of many written), and every row written moves four
		// indexes: drop what the workspace no longer has, store what differs.
		kept := make([]string, 0, len(rows))
		for _, row := range rows {
			kept = append(kept, row.entity+":"+row.id)
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM mcp_metric_projection WHERE workspace_id=? AND entity||':'||entity_id NOT IN (SELECT value FROM json_each(?))`, id, jsonText(kept)); err != nil {
			return 0, 0, err
		}
		for _, row := range rows {
			if err := storeMetricProjection(ctx, tx, row.entity, row.id, id, row.values, row.json); err != nil {
				return 0, 0, err
			}
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM mcp_metric_dirty WHERE workspace_id=?", id); err != nil {
			return 0, 0, err
		}
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM mcp_metric_claims WHERE token=?", token); err != nil {
		return 0, 0, err
	}
	err = tx.Commit()
	completed(err)
	// Workspaces written to meanwhile stay dirty and count as attempted, so
	// the caller comes back for them.
	if err != nil {
		published = 0
	}
	return len(ids), published, err
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
	ids := make([]string, 0, len(dirty))
	for _, row := range dirty {
		ids = append(ids, firstString(row["workspace_id"]))
	}
	if err = c.projectMetrics(ctx, tx, book, ids, func(_, entity, entityID string, values map[string]statMetricValue) error {
		fresh[entity][entityID] = values
		return nil
	}); err != nil {
		return nil, err
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

// metricStore receives one computed entity ("workspace" or "conversation").
type metricStore func(workspace, entity, entityID string, values map[string]statMetricValue) error

// metricQueryChunk is how many workspaces one set of grouped queries reads.
// A statement per conversation costs more than what it reads once the pages
// are cached, so the inputs of a chunk's conversations are read together.
var metricQueryChunk = 100

// projectMetrics computes the metrics of each of the workspaces and of their
// current conversations from tx, passing each entity's values to store: a
// workspace's conversations first, then the workspace.
func (c *Catalog) projectMetrics(ctx context.Context, tx *sql.Tx, book priceBook, workspaces []string, store metricStore) error {
	for offset := 0; offset < len(workspaces); offset += metricQueryChunk {
		if err := projectMetricChunk(ctx, tx, book, workspaces[offset:min(offset+metricQueryChunk, len(workspaces))], store); err != nil {
			return err
		}
	}
	return nil
}

// metricInputs are what a chunk's conversations contribute, keyed by
// conversation ID. fallback is keyed by workspace and conversation ("" for
// the workspace's own rows), newest evidence first.
type metricInputs struct {
	counts, calls map[string]map[string]any
	ledger        map[string]string
	usage         map[string][]map[string]any
	delegates     map[string]bool
	fallback      map[[2]string][]map[string]any
}

func loadMetricInputs(ctx context.Context, tx *sql.Tx, workspaces, conversations string) (metricInputs, error) {
	in := metricInputs{counts: map[string]map[string]any{}, calls: map[string]map[string]any{}, ledger: map[string]string{},
		usage: map[string][]map[string]any{}, delegates: map[string]bool{}, fallback: map[[2]string][]map[string]any{}}
	const chosen = ` IN (SELECT value FROM json_each(?))`
	const delegated = `EXISTS(SELECT 1 FROM agent_session_messages am JOIN agent_sessions a ON a.id=am.agent_session_id WHERE am.message_id=m.id AND a.native_id<>'main')`
	const compaction = `m.kind='metadata' AND (m.text LIKE '%"subtype":"compact_boundary"%' OR m.text LIKE '%"compaction_trigger":"%')`
	rows, err := queryMapsContext(ctx, tx, `SELECT m.conversation_id,
		COUNT(CASE WHEN m.role='user' AND m.kind='message' THEN 1 END) turns,
		COUNT(CASE WHEN `+compaction+` THEN 1 END) compactions,
		COUNT(CASE WHEN m.role='user' AND m.kind='message' AND NOT `+delegated+` THEN 1 END) own_turns,
		COUNT(CASE WHEN `+compaction+` AND NOT `+delegated+` THEN 1 END) own_compactions
		FROM messages m WHERE m.conversation_id`+chosen+` GROUP BY m.conversation_id`, conversations)
	if err != nil {
		return in, err
	}
	for _, row := range rows {
		in.counts[firstString(row["conversation_id"])] = row
	}
	rows, err = queryMapsContext(ctx, tx, `SELECT t.conversation_id,COUNT(*) calls,COUNT(CASE WHEN t.status='error' THEN 1 END) errors,
		COUNT(CASE WHEN a.native_id='main' OR t.agent_session_id IS NULL THEN 1 END) own_calls,
		COUNT(CASE WHEN t.status='error' AND (a.native_id='main' OR t.agent_session_id IS NULL) THEN 1 END) own_errors
		FROM tool_calls t LEFT JOIN agent_sessions a ON a.id=t.agent_session_id AND a.conversation_id=t.conversation_id
		WHERE t.conversation_id`+chosen+` GROUP BY t.conversation_id`, conversations)
	if err != nil {
		return in, err
	}
	for _, row := range rows {
		in.calls[firstString(row["conversation_id"])] = row
	}
	rows, err = queryMapsContext(ctx, tx, `SELECT conversation_id,version FROM tool_ledger_state WHERE conversation_id`+chosen, conversations)
	if err != nil {
		return in, err
	}
	for _, row := range rows {
		in.ledger[firstString(row["conversation_id"])] = firstString(row["version"])
	}
	// One conversation's rows keep the order they are stored in: costs are
	// summed in it, and a different order moves a sum's last digits.
	rows, err = queryMapsContext(ctx, tx, `SELECT u.*,a.id session_id,a.native_id,a.model session_model,a.usage_status,a.conversation_id usage_conversation
		FROM json_each(?) chosen CROSS JOIN agent_sessions a ON a.conversation_id=chosen.value CROSS JOIN agent_session_usage u ON u.agent_session_id=a.id
		WHERE a.usage_status<>'in-child-conversation'`, conversations)
	if err != nil {
		return in, err
	}
	for _, row := range rows {
		id := firstString(row["usage_conversation"])
		in.usage[id] = append(in.usage[id], row)
	}
	// Navigation placeholders and external children are delegates too.
	rows, err = queryMapsContext(ctx, tx, `SELECT chosen.value id FROM json_each(?) chosen
		WHERE EXISTS(SELECT 1 FROM agent_sessions WHERE conversation_id=chosen.value AND native_id<>'main')
		OR EXISTS(SELECT 1 FROM conversations WHERE parent_id=chosen.value)`, conversations)
	if err != nil {
		return in, err
	}
	for _, row := range rows {
		in.delegates[firstString(row["id"])] = true
	}
	// +name: by workspace. The index on name holds every workspace's rows.
	rows, err = queryMapsContext(ctx, tx, `SELECT workspace_id,COALESCE(conversation_id,'') conversation_id,name,value,status,coverage FROM metrics
		WHERE workspace_id`+chosen+` AND +name IN ('total_tokens','input_tokens','output_tokens','cache_read_input_tokens')
		ORDER BY observed_at DESC,extractor_version DESC,id DESC`, workspaces)
	if err != nil {
		return in, err
	}
	for _, row := range rows {
		key := [2]string{firstString(row["workspace_id"]), firstString(row["conversation_id"])}
		in.fallback[key] = append(in.fallback[key], row)
	}
	return in, nil
}

func projectMetricChunk(ctx context.Context, tx *sql.Tx, book priceBook, workspaces []string, store metricStore) error {
	chosen := jsonText(workspaces)
	rows, err := queryMapsContext(ctx, tx, `SELECT c.id,c.workspace_id,c.provider,c.started_at,c.ended_at FROM conversations c
		WHERE c.workspace_id IN (SELECT value FROM json_each(?)) AND `+currentGroupConversation("c"), chosen)
	if err != nil {
		return err
	}
	conversations, ids := map[string][]map[string]any{}, []string{}
	for _, row := range rows {
		workspace := firstString(row["workspace_id"])
		conversations[workspace] = append(conversations[workspace], row)
		ids = append(ids, firstString(row["id"]))
	}
	in, err := loadMetricInputs(ctx, tx, chosen, jsonText(ids))
	if err != nil {
		return err
	}
	for _, id := range workspaces {
		if err := projectWorkspaceMetrics(book, id, conversations[id], in, store); err != nil {
			return err
		}
	}
	return nil
}

// projectWorkspaceMetrics computes workspace id's metrics and its current
// conversations' metrics from what was read for them.
func projectWorkspaceMetrics(book priceBook, id string, conversations []map[string]any, in metricInputs, store metricStore) error {
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
		count := in.counts[cid]
		own["user_turn_count"] = metricNumber(float64(integer(count["own_turns"])))
		addMetricCount(workspace, "user_turn_count", float64(integer(count["turns"])))
		// Only providers with a retained event contract can establish zero.
		if provider := firstString(conv["provider"]); provider == "codex" || provider == "claude" || integer(count["compactions"]) > 0 {
			own["compaction_count"] = metricNumber(float64(integer(count["own_compactions"])))
			addMetricCount(workspace, "compaction_count", float64(integer(count["compactions"])))
		} else {
			workspace["compaction_count"] = statMetricValue{Value: workspace["compaction_count"].Value, Status: "partial"}
		}
		calls := in.calls[cid]
		status := "complete"
		if in.ledger[cid] != toolLedgerVersion {
			status = "pending"
		}
		for _, pair := range [][3]string{{"tool_call_count", "calls", "own_calls"}, {"tool_error_count", "errors", "own_errors"}} {
			v := metricNumber(float64(integer(calls[pair[2]])))
			v.Status = status
			own[pair[0]] = v
			addMetricCount(workspace, pair[0], float64(integer(calls[pair[1]])))
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
		usage := in.usage[cid]
		// Aggregate usage belongs to the owner when there are no delegates that
		// could share it.
		hasDelegates := in.delegates[cid]
		ownUsage := []map[string]any{}
		unassigned := false
		for _, u := range usage {
			if firstString(u["native_id"]) == "main" {
				if hasDelegates && strings.HasSuffix(firstString(u["attribution"]), "-remainder") {
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
		metricFallback(in.fallback[[2]string{id, cid}], true, own)
		if err := store(id, "conversation", cid, own); err != nil {
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
	metricFallback(in.fallback[[2]string{id, ""}], false, workspace)
	return store(id, "workspace", id, workspace)
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
	estimated, missingInput, knownInput := false, false, false
	for _, row := range rows {
		estimate := estimateFlag(row["is_estimate"])
		missing := estimate && !estimateFlag(row["input_tokens_available"])
		estimated = estimated || estimate
		missingInput = missingInput || missing
		knownInput = knownInput || !missing
		for _, name := range []string{"total_tokens", "input_tokens", "output_tokens", "cache_read_input_tokens"} {
			totals[name] += float64(integer(row[name]))
		}
		partial = partial || integer(row["unclassified_tokens"]) > 0
		day := ""
		if hour, ok := parseTime(firstString(row["usage_hour"])); ok {
			day = localMidnight(hour, time.Local).Format("2006-01-02")
		}
		model := firstString(row["model"], row["session_model"])
		p := book.ledgerCost(row, model, day, ledgerTokens(row))
		cost = addCost(cost, p.cost)
		status = worsePriceStatus(status, p.status)
	}
	for name, n := range totals {
		v := metricNumber(n)
		v.IsEstimate = estimated
		if estimated {
			v.Status = "partial"
		}
		if missingInput && (name == "input_tokens" || name == "cache_read_input_tokens") {
			v.Status = "partial"
			if !knownInput {
				v.Value = nil
				v.Status = "unavailable"
			}
		}
		if partial && name != "total_tokens" && v.Value != nil {
			v.Status = "partial"
		}
		values[name] = v
	}
	if n, ok := number(cost); ok {
		v := metricNumber(n)
		v.PriceStatus = status
		v.IsEstimate = estimated
		if status == "assumed" {
			v.Status = "assumed"
		} else if status != "priced" {
			v.Status = "partial"
		}
		values["cost_usd"] = v
	} else {
		values["cost_usd"] = statMetricValue{IsEstimate: estimated, Status: "unavailable", PriceStatus: status}
	}
}

func mergeUsageMetrics(dest, src map[string]statMetricValue) {
	for _, name := range []string{"total_tokens", "input_tokens", "output_tokens", "cache_read_input_tokens", "cost_usd"} {
		s := src[name]
		priceStatus := worsePriceStatus(dest[name].PriceStatus, s.PriceStatus)
		if s.Value == nil {
			v := dest[name]
			v.PriceStatus = priceStatus
			v.IsEstimate = v.IsEstimate || s.IsEstimate
			if s.IsEstimate && v.Value != nil && s.Status != "complete" {
				v.Status = "partial"
			}
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
		if s.Status == "partial" || d.IsEstimate && d.Status != "complete" {
			status = "partial"
		}
		dest[name] = statMetricValue{IsEstimate: d.IsEstimate || s.IsEstimate, Value: &n, Status: status, PriceStatus: priceStatus}
	}
}

// Only token evidence is a fallback; legacy textual error metrics are never
// promoted to tool failures. Deterministic selection prevents scope/version rows
// from silently overwriting one another.
func metricFallback(rows []map[string]any, conversation bool, values map[string]statMetricValue) {
	seen := map[string]bool{}
	for _, row := range rows {
		name := firstString(row["name"])
		if seen[name] || values[name].Status == "unattributable" || values[name].Value != nil && (conversation || values[name].Status == "complete") {
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
}

// A conversation can move between workspaces. Its new workspace may publish
// before the old workspace clears its projection, so replace that same entity.
// The sorted columns are functions of data, so a row whose data and workspace
// are already stored is left unwritten.
func storeMetricProjection(ctx context.Context, tx *sql.Tx, entity, id, workspace string, values map[string]statMetricValue, data string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO mcp_metric_projection(entity,entity_id,workspace_id,metrics_json,total_tokens,cost_usd,tool_error_count) VALUES(?,?,?,?,?,?,?)
 ON CONFLICT(entity,entity_id) DO UPDATE SET workspace_id=excluded.workspace_id,metrics_json=excluded.metrics_json,total_tokens=excluded.total_tokens,cost_usd=excluded.cost_usd,tool_error_count=excluded.tool_error_count
 WHERE workspace_id<>excluded.workspace_id OR metrics_json<>excluded.metrics_json`, entity, id, workspace, data, values["total_tokens"].effective("complete"), values["cost_usd"].effective("complete"), values["tool_error_count"].effective("complete"))
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
