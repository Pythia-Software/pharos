package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
)

// requestTimingVersion names the derivation stored in model_request_timings.
// Changing it marks every conversation for recomputation.
const requestTimingVersion = "timings-v3-inference-speed-policy"

// requestTimingColumns are model_request_timings' columns, in the order
// thinkingSource returns them.
const requestTimingColumns = `id,conversation_id,agent_session_id,workspace_id,native_id,previous_request_id,sequence,requested_at,observed_millis,
 model,effort,speed,service_tier,speed_source,input_tokens,cache_read_input_tokens,output_tokens,reasoning_output_tokens,compacted_before,provider,harness,agent_depth,
 source_kind,repository_name,machine,preceding_tool,timing_quality,gap_seconds,tool_result_at`

// requestTimingSQL derives each request's timing evidence within the
// conversations {conversations} selects: the previous request in its stream
// and the tool results it followed. Tool calls never link a request in another
// conversation, so a conversation's evidence depends only on its own rows and
// attributes.
// The timing deliberately uses the first retained usage-bearing response
// event, not an unverified request-send clock. Previous response and
// incoming-tool evidence are computed before any dashboard filter.
const requestTimingSQL = `WITH incoming AS (SELECT next_request_id,MAX(unixepoch(ended_at,'subsec')) anchor,
 SUM(CASE WHEN unixepoch(ended_at,'subsec') IS NULL THEN 1 ELSE 0 END) missing,
 COUNT(DISTINCT COALESCE(agent_session_id,'')) streams,MIN(COALESCE(agent_session_id,'')) stream,
 CASE WHEN COUNT(DISTINCT tool_category)=1 THEN MIN(tool_category) ELSE 'mixed' END preceding_tool
 FROM tool_calls WHERE next_request_id IN (SELECT id FROM model_requests WHERE conversation_id IN ({conversations})) GROUP BY next_request_id),
 ordered AS (SELECT mr.id,mr.conversation_id,mr.agent_session_id,mr.native_id,mr.sequence,mr.requested_at,mr.model,mr.effort,mr.speed,mr.service_tier,mr.speed_source,
 mr.input_tokens,mr.cache_read_input_tokens,mr.output_tokens,mr.reasoning_output_tokens,mr.compacted_before,
 LAG(mr.id) OVER stream previous_request_id,LAG(unixepoch(requested_at,'subsec')) OVER stream previous_at
 FROM model_requests mr WHERE mr.conversation_id IN ({conversations})
 WINDOW stream AS (PARTITION BY conversation_id,COALESCE(agent_session_id,'') ORDER BY mr.sequence,mr.id)),
 evidence AS (SELECT p.*,unixepoch(p.requested_at,'subsec') observed_at,i.anchor,i.missing,i.streams,i.stream,
 COALESCE(i.preceding_tool,'none') preceding_tool,c.workspace_id,c.provider,c.harness,c.agent_depth,
 w.source_kind,r.display_name repository_name,COALESCE(o.execution_machine,o.machine,'Unknown') machine
 FROM ordered p CROSS JOIN conversations c ON c.id=p.conversation_id JOIN workspaces w ON w.id=c.workspace_id
 LEFT JOIN repositories r ON r.id=w.repository_id LEFT JOIN incoming i ON i.next_request_id=p.id
 LEFT JOIN conversation_origins o ON o.conversation_id=c.id),
 durations AS (SELECT *,ROUND((observed_at-anchor)*1000)/1000.0 observed_gap FROM evidence),
 qualified AS (SELECT *,CASE
 WHEN observed_at IS NULL THEN 'Missing response timestamp'
 WHEN anchor IS NULL THEN 'No linked tool timestamp'
 WHEN missing>0 THEN 'Incomplete tool timestamps'
 WHEN streams<>1 OR (stream<>'' AND stream<>COALESCE(agent_session_id,'')) THEN 'Different agent stream'
 WHEN previous_at IS NOT NULL AND anchor<=previous_at THEN 'Stale tool anchor'
 WHEN observed_gap<=0 THEN 'Nonpositive gap'
 WHEN observed_gap<0.1 THEN 'Below 100 ms'
 WHEN observed_gap>600 THEN 'Above 600 seconds'
 ELSE 'Usable timing' END timing_quality FROM durations)
 SELECT id,conversation_id,agent_session_id,workspace_id,native_id,previous_request_id,sequence,requested_at,CAST(ROUND(observed_at*1000) AS INTEGER) observed_millis,
 COALESCE(NULLIF(model,''),'Unreported') model,COALESCE(NULLIF(effort,''),'Unreported') effort,CASE WHEN LOWER(speed)='fast' THEN 'fast' ELSE 'standard' END speed,service_tier,COALESCE(NULLIF(speed_source,''),'assumed_standard') speed_source,
 input_tokens,cache_read_input_tokens,output_tokens,reasoning_output_tokens,compacted_before,provider,harness,agent_depth,
 source_kind,repository_name,machine,preceding_tool,timing_quality,
 CASE WHEN timing_quality='Usable timing' THEN observed_gap END gap_seconds,
 strftime('%Y-%m-%dT%H:%M:%fZ',anchor,'unixepoch') tool_result_at FROM qualified`

func requestTimings(conversations string) string {
	return strings.ReplaceAll(requestTimingSQL, "{conversations}", conversations)
}

// requestExclusionSQL lists the conversations the timing population leaves
// out: inactive group members, and all but one transcript of each explicit
// native-alias family, preferring native, non-mirrored evidence.
const requestExclusionSQL = `WITH RECURSIVE
 alias_edges(a,b) AS (SELECT left_id,right_id FROM conversation_identity_links WHERE relationship='native-alias'
 UNION SELECT right_id,left_id FROM conversation_identity_links WHERE relationship='native-alias'),
 alias_members(id,root) AS (SELECT a,a FROM alias_edges UNION SELECT e.b,m.root FROM alias_members m JOIN alias_edges e ON e.a=m.id),
 families AS (SELECT id,MIN(root) root FROM alias_members GROUP BY id)
 SELECT conversation_id FROM conversation_group_membership WHERE active=0
 UNION SELECT id FROM (SELECT c.id,ROW_NUMBER() OVER (PARTITION BY f.root ORDER BY
 (w.source_kind=c.provider) DESC,(w.id NOT IN (SELECT workspace_id FROM tool_mirror_workspaces)) DESC,
 (SELECT COUNT(*) FROM model_requests mr WHERE mr.conversation_id=c.id) DESC,c.id DESC) choice
 FROM families f CROSS JOIN conversations c ON c.id=f.id JOIN workspaces w ON w.id=c.workspace_id
 WHERE NOT EXISTS(SELECT 1 FROM conversation_group_membership gm WHERE gm.conversation_id=c.id AND gm.active=0)
 AND EXISTS(SELECT 1 FROM model_requests mr WHERE mr.conversation_id=c.id)) WHERE choice>1`

// thinkingSource is the timing population. Stored timings are current except
// for the conversations triggers marked since the last refresh, which are
// derived again here; likewise the stored exclusions, which are listed again
// only while marked stale (CASE evaluates the IN list it chooses, once). The
// result is the same either way: the stored rows only spare reading every
// window partition and family on each query.
var thinkingSource = `SELECT * FROM (SELECT ` + requestTimingColumns + ` FROM model_request_timings
 WHERE conversation_id NOT IN (SELECT conversation_id FROM model_request_timing_dirty)
 UNION ALL SELECT * FROM (` + requestTimings("SELECT conversation_id FROM model_request_timing_dirty") + `)) t
 WHERE CASE WHEN EXISTS(SELECT 1 FROM meta WHERE key='model_request_exclusions_stale')
 THEN t.conversation_id NOT IN (` + requestExclusionSQL + `)
 ELSE t.conversation_id NOT IN (SELECT conversation_id FROM model_request_exclusions) END`

var thinkingRequestDataset = func() sqlDataset {
	columns := map[string]string{}
	for _, name := range []string{"id", "conversation_id", "agent_session_id", "workspace_id", "native_id", "sequence", "requested_at", "model", "effort", "speed", "service_tier", "speed_source", "input_tokens", "cache_read_input_tokens", "output_tokens", "reasoning_output_tokens", "compacted_before", "provider", "harness", "agent_depth", "source_kind", "repository_name", "machine", "preceding_tool", "timing_quality", "gap_seconds", "tool_result_at"} {
		columns[name] = "d." + name
	}
	columns["model_effort"] = "model_effort(d.model,d.effort)"
	columns["model_speed"] = "d.model || ' · ' || CASE WHEN d.speed='fast' THEN 'Fast' ELSE 'Standard' END"
	columns["model_effort_speed"] = "d.model || ' · ' || d.effort || ' · ' || CASE WHEN d.speed='fast' THEN 'Fast' ELSE 'Standard' END"
	columns["day"] = "date(d.requested_at,'localtime')"
	columns["week"] = "date(d.requested_at,'localtime','-6 days','weekday 1')"
	columns["month"] = "strftime('%Y-%m',d.requested_at,'localtime')"
	return sqlDataset{from: "FROM (" + thinkingSource + ") d", columns: columns, timestamps: map[string]string{"requested_at": "d.requested_at"},
		newestBounds: map[string]newestBound{"requested_at": {
			nth:    "SELECT requested_at FROM model_request_timings ORDER BY requested_at DESC LIMIT 1 OFFSET ?",
			column: "d.requested_at",
		}}}
}()

// ensureRequestTimings installs the stored timing projection. Triggers mark a
// conversation whenever its requests, the tool calls they follow, or its
// attributes change, and mark the exclusions stale whenever a family's or
// group's inputs change. They run in the writer's transaction, whichever
// build or process writes.
func (c *Catalog) ensureRequestTimings() error {
	// Date ranges read the covering index alone, in requested_at order, rather
	// than visiting rows stored a conversation at a time.
	if _, err := c.DB.Exec(`CREATE TABLE IF NOT EXISTS model_request_timings(id TEXT NOT NULL, conversation_id TEXT NOT NULL,
 agent_session_id TEXT, workspace_id TEXT, native_id TEXT, previous_request_id TEXT, sequence INTEGER, requested_at TEXT, observed_millis INTEGER,
 model TEXT NOT NULL, effort TEXT NOT NULL, speed TEXT, service_tier TEXT, speed_source TEXT, input_tokens INTEGER, cache_read_input_tokens INTEGER, output_tokens INTEGER, reasoning_output_tokens INTEGER,
 compacted_before INTEGER, provider TEXT, harness TEXT, agent_depth INTEGER, source_kind TEXT, repository_name TEXT, machine TEXT,
 preceding_tool TEXT NOT NULL, timing_quality TEXT NOT NULL, gap_seconds REAL, tool_result_at TEXT,
 PRIMARY KEY(conversation_id,id)) WITHOUT ROWID;`); err != nil {
		return err
	}
	if has, err := c.hasColumn("model_request_timings", "speed"); err != nil {
		return err
	} else if !has {
		if _, err := c.DB.Exec("ALTER TABLE model_request_timings ADD COLUMN speed TEXT; DROP INDEX IF EXISTS model_request_timings_requested_idx"); err != nil {
			return err
		}
	}
	for _, column := range []string{"service_tier", "speed_source"} {
		if has, err := c.hasColumn("model_request_timings", column); err != nil {
			return err
		} else if !has {
			if _, err := c.DB.Exec("ALTER TABLE model_request_timings ADD COLUMN " + column + " TEXT; DROP INDEX IF EXISTS model_request_timings_requested_idx"); err != nil {
				return err
			}
		}
	}
	if _, err := c.DB.Exec(`CREATE INDEX IF NOT EXISTS model_request_timings_requested_idx ON model_request_timings(requested_at,` + requestTimingColumns + `);
CREATE TABLE IF NOT EXISTS model_request_timing_dirty(conversation_id TEXT PRIMARY KEY) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS model_request_exclusions(conversation_id TEXT PRIMARY KEY) WITHOUT ROWID;`); err != nil {
		return err
	}
	mark := func(conversation string) string {
		return "INSERT INTO model_request_timing_dirty VALUES(" + conversation + ") ON CONFLICT(conversation_id) DO NOTHING;"
	}
	markAll := func(selection string) string {
		return "INSERT INTO model_request_timing_dirty " + selection + " ON CONFLICT(conversation_id) DO NOTHING;"
	}
	followed := func(ref string) string {
		return markAll("SELECT conversation_id FROM model_requests WHERE id=" + ref + ".next_request_id")
	}
	inWorkspace := func(ref string) string {
		return markAll("SELECT id FROM conversations WHERE workspace_id=" + ref + ".id")
	}
	inRepository := func(ref string) string {
		return markAll("SELECT c.id FROM workspaces w JOIN conversations c ON c.workspace_id=w.id WHERE w.repository_id=" + ref + ".id")
	}
	// Upserts name every column, so UPDATE OF alone fires for unchanged rows.
	changed := func(columns ...string) string {
		terms := make([]string, len(columns))
		for i, column := range columns {
			terms[i] = "OLD." + column + " IS NOT NEW." + column
		}
		return "(" + strings.Join(terms, " OR ") + ")"
	}
	const stale = "INSERT INTO meta(key,value) VALUES('model_request_exclusions_stale','1') ON CONFLICT(key) DO NOTHING;"
	aliased := func(conversation string) string {
		return "EXISTS(SELECT 1 FROM conversation_identity_links WHERE relationship='native-alias' AND (left_id=" + conversation + " OR right_id=" + conversation + "))"
	}
	for _, trigger := range []string{
		"model_requests_insert AFTER INSERT ON model_requests BEGIN " + mark("NEW.conversation_id") + " END",
		"model_requests_update AFTER UPDATE ON model_requests BEGIN " + mark("OLD.conversation_id") + mark("NEW.conversation_id") + " END",
		"model_requests_delete AFTER DELETE ON model_requests BEGIN " + mark("OLD.conversation_id") + " END",
		"tools_insert AFTER INSERT ON tool_calls WHEN NEW.next_request_id IS NOT NULL BEGIN " + followed("NEW") + " END",
		"tools_update AFTER UPDATE OF next_request_id,ended_at,agent_session_id,tool_category ON tool_calls WHEN " + changed("next_request_id", "ended_at", "agent_session_id", "tool_category") + " BEGIN " + followed("OLD") + followed("NEW") + " END",
		"tools_delete AFTER DELETE ON tool_calls WHEN OLD.next_request_id IS NOT NULL BEGIN " + followed("OLD") + " END",
		"conversations_insert AFTER INSERT ON conversations BEGIN " + mark("NEW.id") + " END",
		"conversations_update AFTER UPDATE OF workspace_id,provider,harness,agent_depth ON conversations WHEN " + changed("workspace_id", "provider", "harness", "agent_depth") + " BEGIN " + mark("OLD.id") + mark("NEW.id") + " END",
		"conversations_delete AFTER DELETE ON conversations BEGIN " + mark("OLD.id") + " END",
		"workspaces_insert AFTER INSERT ON workspaces BEGIN " + inWorkspace("NEW") + " END",
		"workspaces_update AFTER UPDATE OF source_kind,repository_id ON workspaces WHEN " + changed("source_kind", "repository_id") + " BEGIN " + inWorkspace("NEW") + " END",
		"workspaces_delete AFTER DELETE ON workspaces BEGIN " + inWorkspace("OLD") + " END",
		"repositories_insert AFTER INSERT ON repositories BEGIN " + inRepository("NEW") + " END",
		"repositories_update AFTER UPDATE OF display_name ON repositories WHEN " + changed("display_name") + " BEGIN " + inRepository("NEW") + " END",
		"repositories_delete AFTER DELETE ON repositories BEGIN " + inRepository("OLD") + " END",
		"origins_insert AFTER INSERT ON conversation_origins BEGIN " + mark("NEW.conversation_id") + " END",
		"origins_update AFTER UPDATE OF machine,execution_machine ON conversation_origins WHEN " + changed("machine", "execution_machine") + " BEGIN " + mark("NEW.conversation_id") + " END",
		"origins_delete AFTER DELETE ON conversation_origins BEGIN " + mark("OLD.conversation_id") + " END",
		// A family's choice also counts its members' requests.
		"exclusions_requests_insert AFTER INSERT ON model_requests WHEN " + aliased("NEW.conversation_id") + " BEGIN " + stale + " END",
		"exclusions_requests_update AFTER UPDATE OF conversation_id ON model_requests WHEN " + changed("conversation_id") + " BEGIN " + stale + " END",
		"exclusions_requests_delete AFTER DELETE ON model_requests WHEN " + aliased("OLD.conversation_id") + " BEGIN " + stale + " END",
		"exclusions_conversations_insert AFTER INSERT ON conversations WHEN " + aliased("NEW.id") + " BEGIN " + stale + " END",
		"exclusions_conversations_update AFTER UPDATE OF workspace_id,provider ON conversations WHEN " + changed("workspace_id", "provider") + " BEGIN " + stale + " END",
		"exclusions_conversations_delete AFTER DELETE ON conversations BEGIN " + stale + " END",
		"exclusions_workspaces_update AFTER UPDATE OF source_kind ON workspaces WHEN " + changed("source_kind") + " BEGIN " + stale + " END",
	} {
		if _, err := c.DB.Exec("CREATE TRIGGER IF NOT EXISTS request_timings_" + trigger); err != nil {
			return err
		}
	}
	for _, table := range []string{"conversation_identity_links", "conversation_group_membership", "tool_mirror_workspaces"} {
		for _, event := range []string{"INSERT", "UPDATE", "DELETE"} {
			if _, err := c.DB.Exec("CREATE TRIGGER IF NOT EXISTS request_timings_exclusions_" + table + "_" + strings.ToLower(event) +
				" AFTER " + event + " ON " + table + " BEGIN " + stale + " END"); err != nil {
				return err
			}
		}
	}
	// A new derivation, or a catalog that predates the projection, marks every
	// conversation; the refresh then fills the tables in the background.
	_, err := c.DB.Exec(`INSERT INTO model_request_timing_dirty SELECT DISTINCT conversation_id FROM model_requests
 WHERE COALESCE((SELECT value FROM meta WHERE key='model_request_timings_version'),'')<>?1
 UNION SELECT DISTINCT conversation_id FROM model_request_timings
 WHERE COALESCE((SELECT value FROM meta WHERE key='model_request_timings_version'),'')<>?1
 ON CONFLICT(conversation_id) DO NOTHING;
 INSERT INTO meta(key,value) SELECT 'model_request_exclusions_stale','1'
 WHERE COALESCE((SELECT value FROM meta WHERE key='model_request_timings_version'),'')<>?1 ON CONFLICT(key) DO NOTHING;
 INSERT INTO meta(key,value) VALUES('model_request_timings_version',?1) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, requestTimingVersion)
	return err
}

// requestTimingBatch bounds each refresh transaction, so a sync waits for at
// most one batch of conversations.
var requestTimingBatch = 200

// refreshRequestTimings stores the timings of every marked conversation, and
// the exclusions when they are stale. Each batch derives, replaces and
// unmarks its conversations in one transaction. It then continues any
// validation evidence backfill.
func (c *Catalog) refreshRequestTimings(ctx context.Context) error {
	if err := c.writeTransaction(ctx, "request-exclusions", func(tx *sql.Tx) error {
		var stale bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM meta WHERE key='model_request_exclusions_stale')").Scan(&stale); err != nil || !stale {
			return err
		}
		for _, statement := range []string{"DELETE FROM model_request_exclusions", "INSERT INTO model_request_exclusions " + requestExclusionSQL,
			"DELETE FROM meta WHERE key='model_request_exclusions_stale'"} {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	insert := "INSERT INTO model_request_timings(" + requestTimingColumns + ") " + requestTimings("SELECT value FROM json_each(?1)")
	for {
		done := false
		if err := c.writeTransaction(ctx, "request-timings", func(tx *sql.Tx) error {
			rows, err := queryMaps(tx, "SELECT conversation_id FROM model_request_timing_dirty LIMIT ?", requestTimingBatch)
			if err != nil {
				return err
			}
			if done = len(rows) == 0; done {
				return nil
			}
			ids := make([]string, len(rows))
			for i, row := range rows {
				ids[i] = firstString(row["conversation_id"])
			}
			batch, err := json.Marshal(ids)
			if err != nil {
				return err
			}
			for _, statement := range []string{"DELETE FROM model_request_timings WHERE conversation_id IN (SELECT value FROM json_each(?1))", insert,
				"DELETE FROM model_request_timing_dirty WHERE conversation_id IN (SELECT value FROM json_each(?1))"} {
				if _, err := tx.ExecContext(ctx, statement, string(batch)); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		} else if done {
			return c.backfillRequestEvidence(ctx)
		}
	}
}

// requestTimingsPending reports whether the stored timings or exclusions are
// behind their inputs, or the validation evidence is still being backfilled.
func (c *Catalog) requestTimingsPending(ctx context.Context) (bool, error) {
	var pending bool
	err := c.reader().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM model_request_timing_dirty)
 OR EXISTS(SELECT 1 FROM meta WHERE key IN ('model_request_exclusions_stale','model_request_evidence_cursor'))`).Scan(&pending)
	return pending, err
}

// currentRequestTimings is refreshRequestTimings for a page, which never waits
// for it: the query itself derives whatever is marked, so the stored rows only
// need to catch up in the background, with the tools stage.
func (c *Catalog) currentRequestTimings(ctx context.Context) error {
	pending, err := c.requestTimingsPending(ctx)
	if err != nil || !pending {
		return err
	}
	var deferred bool
	if err := c.reader().QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sync_deferred WHERE name='tools' AND generation>completed_generation)").Scan(&deferred); err != nil {
		return err
	}
	if !deferred {
		c.refreshToolRollup()
	}
	return nil
}
