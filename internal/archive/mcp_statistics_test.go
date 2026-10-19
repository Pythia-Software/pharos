package archive

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"
)

func statisticsFixture(t *testing.T) *Catalog {
	t.Helper()
	c, _ := testCatalog(t)
	if err := c.replacePricing(testPricing(), "test"); err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`INSERT INTO repositories(id,display_name,created_at,updated_at) VALUES('r','metrics','x','x')`,
		`INSERT INTO workspaces(id,source_kind,source_account,source_id,repository_id,title,activity_at,indexed_at,purpose,outcome) VALUES('w','codex','local','w','r','STATS_PREVIEW_SENTINEL','2026-09-20T01:00:00Z','x','STATS_PREVIEW_SENTINEL','STATS_PREVIEW_SENTINEL')`,
		`INSERT INTO conversations(id,workspace_id,provider,model,account,native_id,started_at,ended_at) VALUES('c1','w','codex','gpt-5.5','local','c1','2026-09-20T00:00:00Z','2026-09-20T00:00:10Z'),('c2','w','codex','gpt-5.5','local','c2','2026-09-20T00:00:10Z','2026-09-20T00:00:20Z')`,
		`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash) VALUES('m1','c1','m1','user','message','rare statistics phrase','h'),('m2','c1','m2','user','message','delegate prompt','h'),('m3','c2','m3','user','message','another task','h'),('k1','c1','k1','system','metadata','{"compaction_trigger":"auto"}','h'),('k2','c1','k2','system','metadata','{"compaction_trigger":"auto"}','h')`,
	}
	for _, s := range statements {
		if _, err := c.DB.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	main, delegate, second := stableID("agent-session", "c1", "main"), stableID("agent-session", "c1", "child"), stableID("agent-session", "c2", "main")
	for _, s := range []struct {
		id, cid, native string
		input, output   int
	}{{main, "c1", "main", 1000000, 100000}, {delegate, "c1", "child", 500000, 0}, {second, "c2", "main", 200000, 0}} {
		if _, err := c.DB.Exec(`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,provider,model,total_tokens,usage_status) VALUES(?,'w',?,?,'root','codex','gpt-5.5',?,'reported-reconciled')`, s.id, s.cid, s.native, s.input+s.output); err != nil {
			t.Fatal(err)
		}
		if _, err := c.DB.Exec(`INSERT INTO agent_session_usage(agent_session_id,usage_hour,model,attribution,input_tokens,uncached_input_tokens,output_tokens,total_tokens) VALUES(?,'2026-09-20T00:00:00Z','gpt-5.5','request',?,?,?,?)`, s.id, s.input, s.input, s.output, s.input+s.output); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"m2", "k2"} {
		if _, err := c.DB.Exec("INSERT INTO agent_session_messages VALUES(?,?,0)", delegate, id); err != nil {
			t.Fatal(err)
		}
	}
	for _, call := range []struct{ id, session, status string }{{"a", main, "ok"}, {"b", main, "error"}, {"d", delegate, "error"}} {
		if _, err := c.DB.Exec(`INSERT INTO tool_calls(id,workspace_id,conversation_id,agent_session_id,sequence,provider,kind,tool_name,tool_category,status) VALUES(?,'w','c1',?,0,'codex','tool_call','exec','command',?)`, call.id, call.session, call.status); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"c1", "c2"} {
		if _, err := c.DB.Exec("INSERT INTO tool_ledger_state VALUES(?,?,0,'x')", id, toolLedgerVersion); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

func statisticsCall(t *testing.T, c *Catalog, tool string, args map[string]any) map[string]any {
	t.Helper()
	v, err := callMCP(c, tool, args)
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	return v.(map[string]any)
}
func statisticsItems(result map[string]any) []map[string]any {
	return result["items"].([]map[string]any)
}

func TestStatisticsOwnAndWorkspaceScope(t *testing.T) {
	c := statisticsFixture(t)
	args := map[string]any{"entity": "conversation", "select": []string{"total_tokens", "cost_usd", "user_turn_count", "tool_error_count", "tool_call_count", "compaction_count", "wall_clock_span_seconds"}, "order_by": map[string]any{"metric": "total_tokens"}, "max_output_tokens": 8000}
	result := statisticsCall(t, c, "query_statistics", args)
	items := statisticsItems(result)
	if len(items) != 2 || items[0]["conversation_id"] != "c1" || items[0]["total_tokens"] != float64(1100000) || items[0]["tool_call_count"] != float64(2) || items[0]["tool_error_count"] != float64(1) || items[0]["user_turn_count"] != float64(1) || items[0]["compaction_count"] != float64(1) || items[0]["wall_clock_span_seconds"] != float64(10) {
		t.Fatalf("own usage: %#v", items)
	}
	if n, _ := number(items[0]["cost_usd"]); n < 3.599 || n > 3.601 {
		t.Fatalf("own cost: %v", n)
	}
	if strings.Contains(jsonText(result), "STATS_PREVIEW_SENTINEL") {
		t.Fatal("metadata hydrated previews")
	}
	args["metric_scope"] = "workspace"
	result = statisticsCall(t, c, "query_statistics", args)
	items = statisticsItems(result)
	if len(items) != 2 || items[0]["total_tokens"] != float64(1800000) || items[0]["tool_error_count"] != float64(2) || items[0]["user_turn_count"] != float64(3) || items[0]["wall_clock_span_seconds"] != float64(20) {
		t.Fatalf("workspace usage: %#v", items)
	}
	if n, _ := number(items[0]["cost_usd"]); n < 4.999 || n > 5.001 {
		t.Fatalf("workspace cost: %v", n)
	}
	args["metric_filters"] = []any{map[string]any{"name": "cost_usd", "op": "gt", "value": 4}}
	args["metric_scope"] = "conversation"
	if got := statisticsCall(t, c, "query_statistics", args); len(statisticsItems(got)) != 0 {
		t.Fatal("conversation threshold used workspace cost")
	}
	args["metric_scope"] = "workspace"
	if got := statisticsCall(t, c, "search_conversations", map[string]any{"result_mode": "metadata", "metric_scope": "workspace", "metric_filters": args["metric_filters"]}); len(statisticsItems(got)) != 2 {
		t.Fatal("workspace threshold lost conversations")
	}
}

func TestStatisticsQualityAndFullPopulationAggregates(t *testing.T) {
	c := statisticsFixture(t)
	if _, err := c.DB.Exec("UPDATE agent_session_usage SET unclassified_tokens=100 WHERE agent_session_id=?", stableID("agent-session", "c1", "main")); err != nil {
		t.Fatal(err)
	}
	missing := map[string]any{"entity": "conversation", "metric_filters": []any{map[string]any{"name": "input_tokens", "op": "is_missing"}}}
	items := statisticsItems(statisticsCall(t, c, "query_statistics", missing))
	if len(items) != 1 || items[0]["conversation_id"] != "c1" || items[0]["input_tokens"] != nil {
		t.Fatalf("partial missing: %#v", items)
	}
	missing["metric_quality"] = "available"
	if items = statisticsItems(statisticsCall(t, c, "query_statistics", missing)); len(items) != 0 {
		t.Fatal("available partial marked missing")
	}
	agg := map[string]any{"entity": "conversation", "result_type": "aggregate", "limit": 1, "aggregations": []any{map[string]any{"id": "n", "op": "count"}, map[string]any{"id": "mean", "op": "avg", "metric": "input_tokens"}, map[string]any{"id": "sum", "op": "sum", "metric": "input_tokens"}}}
	got := statisticsCall(t, c, "query_statistics", agg)
	groups := got["groups"].([]map[string]any)
	v := groups[0]["values"].(map[string]any)
	if v["n"] != 2 || v["mean"] != float64(200000) || v["sum"] != float64(200000) {
		t.Fatalf("complete aggregates %#v", got)
	}
	agg["metric_quality"] = "available"
	got = statisticsCall(t, c, "query_statistics", agg)
	v = got["groups"].([]map[string]any)[0]["values"].(map[string]any)
	if v["mean"] != float64(600000) || v["sum"] != float64(1200000) {
		t.Fatalf("available aggregates: %#v", got)
	}
	agg["metric_scope"] = "workspace"
	if _, err := callMCP(c, "query_statistics", agg); err == nil {
		t.Fatal("repeated workspace aggregation accepted")
	}
}

func TestStatisticsSnapshotsSurviveChangesAndReopen(t *testing.T) {
	c := statisticsFixture(t)
	args := map[string]any{"entity": "conversation", "select": []string{"total_tokens"}, "order_by": map[string]any{"metric": "total_tokens"}, "limit": 1}
	first := statisticsCall(t, c, "query_statistics", args)
	cursor := firstString(first["next_cursor"])
	if cursor == "" {
		t.Fatal("no continuation")
	}
	if _, err := c.DB.Exec("UPDATE agent_session_usage SET total_tokens=9999999 WHERE agent_session_id=?", stableID("agent-session", "c2", "main")); err != nil {
		t.Fatal(err)
	}
	other := reopenCatalog(t, c.Path)
	defer other.Close()
	args["cursor"] = cursor
	second := statisticsCall(t, other, "query_statistics", args)
	if items := statisticsItems(second); len(items) != 1 || items[0]["conversation_id"] != "c2" || items[0]["total_tokens"] != float64(200000) {
		t.Fatalf("snapshot changed: %#v", second)
	}
	args["metric_quality"] = "available"
	if _, err := callMCP(c, "query_statistics", args); err == nil {
		t.Fatal("changed cursor accepted")
	}
	delete(args, "cursor")
	args["snapshot_id"] = first["snapshot_id"]
	args["metric_quality"] = "complete"
	args["select"] = []string{"output_tokens"}
	if _, err := callMCP(c, "query_statistics", args); err == nil {
		t.Fatal("uncaptured metric accepted")
	}
	if _, err := c.DB.Exec("UPDATE mcp_stat_snapshots SET expires_at=0 WHERE id=?", first["snapshot_id"]); err != nil {
		t.Fatal(err)
	}
	args["select"] = []string{"total_tokens"}
	if _, err := callMCP(c, "query_statistics", args); err == nil {
		t.Fatal("expired snapshot accepted")
	}
}

func TestStatisticsMembershipAndBulkBudgetPaging(t *testing.T) {
	c := statisticsFixture(t)
	tx, err := c.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 30; i++ {
		if _, err = tx.Exec("INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES(?,'w','codex','local',?)", fmt.Sprintf("extra-%02d", i), fmt.Sprintf("extra-%02d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	first := statisticsCall(t, c, "query_statistics", map[string]any{"select": []string{"conversation_ids", "total_tokens"}})
	row := statisticsItems(first)[0]
	if row["conversation_ids_complete"] != false || row["conversation_count"] != 32 || len(row["conversation_ids"].([]string)) != 20 {
		t.Fatalf("membership: %#v", row)
	}
	access := row["conversation_ids_access"].(map[string]any)["arguments"].(map[string]any)
	members := statisticsCall(t, c, "query_statistics", access)
	if len(statisticsItems(members)) != 32 {
		t.Fatalf("flat IDs %#v", members)
	}
	if _, err = c.DB.Exec("INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) SELECT 'bulk-'||value,'codex','local','bulk-'||value,'secret','x' FROM json_each(?)", jsonText(func() []int {
		v := []int{}
		for i := 0; i < 510; i++ {
			v = append(v, i)
		}
		return v
	}())); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"select": []string{}, "limit": 500, "max_output_tokens": 1000}
	seen := map[string]bool{}
	for page := 0; page < 100; page++ {
		got := statisticsCall(t, c, "query_statistics", args)
		if len(jsonText(got)) > 3000 {
			t.Fatal("budget exceeded")
		}
		for _, item := range statisticsItems(got) {
			id := firstString(item["workspace_id"])
			if seen[id] {
				t.Fatalf("duplicate page row %s", id)
			}
			seen[id] = true
		}
		cursor := firstString(got["next_cursor"])
		if cursor == "" {
			break
		}
		args["cursor"] = cursor
	}
	if len(seen) != 511 {
		t.Fatalf("paging skipped rows: %d", len(seen))
	}
}

func TestStatisticsValidationAndMetricAliases(t *testing.T) {
	c := statisticsFixture(t)
	for _, args := range []map[string]any{{"select": []string{"purpose"}}, {"metric_filters": []any{map[string]any{"name": "total_tokens", "op": "gte"}}}, {"metric_filters": []any{map[string]any{"name": "tool_error_count", "op": "eq", "value": .5}}}, {"metric_filters": []any{map[string]any{"name": "total_tokens", "op": "gt", "value": 5}, map[string]any{"name": "total_tokens", "op": "lte", "value": 5}}}, {"offset": 0}, {"entity": "workspace", "metric_scope": "conversation"}, {"limit": 501}} {
		if _, err := callMCP(c, "query_statistics", args); err == nil {
			t.Fatalf("accepted invalid arguments: %#v", args)
		}
	}
	got := statisticsCall(t, c, "search_work", map[string]any{"result_mode": "metadata", "select": []string{"token_count"}, "repository": "metrics"})
	if statisticsItems(got)[0]["total_tokens"] != float64(1800000) {
		t.Fatalf("alias: %#v", got)
	}
	if _, err := callMCP(c, "list_metrics", nil); err != nil {
		t.Fatal(err)
	}
}

func TestStatisticsMetricOrderingAfterMirrorCollapse(t *testing.T) {
	c, _ := libraryFixture(t)
	got := statisticsCall(t, c, "search_work", map[string]any{"result_mode": "metadata", "order_by": map[string]any{"metric": "total_tokens", "direction": "desc"}})
	items := statisticsItems(got)
	if len(items) != 2 || items[0]["workspace_id"] != "b" || items[0]["total_tokens"] != float64(900) || items[1]["total_tokens"] != float64(500) {
		t.Fatalf("mirror metric sorting: %#v", got)
	}
}

func TestStatisticsIncrementalProjectionAndPriceInvalidation(t *testing.T) {
	c := statisticsFixture(t)
	args := map[string]any{"select": []string{"cost_usd"}, "retain_snapshot": true}
	first := statisticsCall(t, c, "query_statistics", args)
	before := statisticsItems(first)[0]["cost_usd"]
	if _, err := c.DB.Exec("UPDATE cost_changes SET input_per_mtok=4 WHERE model_key='gpt-5.5'"); err != nil {
		t.Fatal(err)
	}
	after := statisticsCall(t, c, "query_statistics", args)
	if statisticsItems(after)[0]["cost_usd"] == before {
		t.Fatal("price update didn't invalidate projection")
	}
	if _, err := c.DB.Exec("INSERT INTO mcp_calls(called_at,tool_name,status,duration_ms,response_bytes,estimated_output_tokens) VALUES('x','query_statistics','ok',0,0,0)"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := c.DB.QueryRow("SELECT COUNT(*) FROM mcp_metric_dirty").Scan(&n); err != nil || n != 0 {
		t.Fatalf("audit dirtied metrics: %d %v", n, err)
	}
	if _, err := c.refreshMetricProjections(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	c.now = func() time.Time { return time.Now().Add(10 * time.Minute) }
	if _, err := callMCP(c, "query_statistics", map[string]any{"snapshot_id": first["snapshot_id"], "select": []string{"cost_usd"}}); err == nil {
		t.Fatal("clock expiry ignored")
	}
}

func TestStatisticsSelectiveTextPrecedesBothLexicalCuts(t *testing.T) {
	override(t, &statisticsTextLimits, struct{ recent, ranked int }{20, 10})
	c := statisticsFixture(t)
	tx, err := c.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT INTO messages_fts(rowid,message_id,text) VALUES(1,'m1','needle')"); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec("INSERT INTO message_fts_rows VALUES('m1',1)"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < statisticsTextLimits.recent+1; i++ {
		id := fmt.Sprintf("noise-%05d", i)
		if _, err = tx.Exec("INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash) VALUES(?,'c2',?,'assistant','message','needle','h')", id, id); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec("INSERT INTO messages_fts(rowid,message_id,text) VALUES(?,?,'needle')", i+2, id); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec("INSERT INTO message_fts_rows VALUES(?,?)", id, i+2); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	got := statisticsCall(t, c, "search_conversations", map[string]any{"query": "needle", "result_mode": "metadata", "metric_filters": []any{map[string]any{"name": "total_tokens", "op": "gte", "value": 1000000}}})
	if items := statisticsItems(got); len(items) != 1 || items[0]["conversation_id"] != "c1" {
		t.Fatalf("rare older eligible match dropped: %#v", got)
	}
	if got["population_complete"] != false {
		t.Fatal("bounded text search claimed exhaustive population")
	}
}

func TestStatisticsAggregateSnapshotCanProjectCapturedRows(t *testing.T) {
	c := statisticsFixture(t)
	args := map[string]any{"entity": "conversation", "result_type": "aggregate", "retain_snapshot": true, "aggregations": []any{map[string]any{"id": "sum", "op": "sum", "metric": "total_tokens"}}}
	first := statisticsCall(t, c, "query_statistics", args)
	rows := statisticsCall(t, c, "query_statistics", map[string]any{"entity": "conversation", "select": []string{"total_tokens"}, "snapshot_id": first["snapshot_id"]})
	if len(statisticsItems(rows)) != 2 {
		t.Fatalf("aggregate snapshot row projection failed: %#v", rows)
	}
}

func TestStatisticsEmptyAggregatesAndBudgetErrors(t *testing.T) {
	c, _ := testCatalog(t)
	got := statisticsCall(t, c, "query_statistics", map[string]any{"result_type": "aggregate", "aggregations": []any{map[string]any{"id": "count", "op": "count"}, map[string]any{"id": "sum", "op": "sum", "metric": "cost_usd"}}})
	values := got["groups"].([]map[string]any)[0]["values"].(map[string]any)
	if values["count"] != 0 || values["sum"] != nil {
		t.Fatalf("empty unknown population: %#v", values)
	}
	c = statisticsFixture(t)
	if _, err := callMCP(c, "query_statistics", map[string]any{"select": []string{"cost_usd", "total_tokens", "input_tokens", "output_tokens"}, "max_output_tokens": 200}); err == nil {
		t.Fatal("metadata budget accepted no-progress page")
	}
}

func TestStatisticsOverviewAndPresenceOrdering(t *testing.T) {
	c := statisticsFixture(t)
	overview := statisticsCall(t, c, "get_conversation_overview", map[string]any{"conversation_id": "c1", "include_metrics": true, "max_output_tokens": 4000})
	if overview["metrics"].(map[string]any)["total_tokens"].(map[string]any)["effective_value"] != float64(1100000) {
		t.Fatalf("overview metrics missing: %#v", overview)
	}
	if _, err := c.DB.Exec("UPDATE agent_session_usage SET unclassified_tokens=100 WHERE agent_session_id=?", stableID("agent-session", "c1", "main")); err != nil {
		t.Fatal(err)
	}
	for _, direction := range []string{"asc", "desc"} {
		got := statisticsCall(t, c, "query_statistics", map[string]any{"entity": "conversation", "select": []string{"input_tokens"}, "order_by": map[string]any{"metric": "input_tokens", "direction": direction}})
		items := statisticsItems(got)
		if items[0]["conversation_id"] != "c2" || items[1]["input_tokens"] != nil {
			t.Fatalf("quality nulls not last in %s: %#v", direction, got)
		}
	}
	for _, args := range []map[string]any{{"limit": 0}, {"max_output_tokens": 0}, {"unknown": true}, {"metric_filters": []any{map[string]any{"name": "total_tokens", "op": "is_present", "extra": true}}}} {
		if _, err := callMCP(c, "query_statistics", args); err == nil {
			t.Fatalf("invalid args accepted %#v", args)
		}
	}
}

func TestStatisticsMetadataPayloadEfficiency(t *testing.T) {
	c := statisticsFixture(t)
	tx, err := c.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for i := 0; i < 100; i++ {
		wid, cid, sid := fmt.Sprintf("efficient-w-%03d", i), fmt.Sprintf("efficient-c-%03d", i), fmt.Sprintf("efficient-s-%03d", i)
		if _, err = tx.Exec("INSERT INTO workspaces(id,source_kind,source_account,source_id,title,purpose,outcome,activity_at,indexed_at) VALUES(?,'codex','local',?,?,?,?,'2026-09-20T00:00:00Z','x')", wid, wid, strings.Repeat("Title ", 30), strings.Repeat("Preview ", 200), strings.Repeat("Outcome ", 200)); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec("INSERT INTO conversations(id,workspace_id,provider,model,account,native_id) VALUES(?,?,'codex','gpt-5.5','local',?)", cid, wid, cid); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec("INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,provider,model,usage_status,total_tokens) VALUES(?,?,?,'main','root','codex','gpt-5.5','reported-reconciled',1000000)", sid, wid, cid); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec("INSERT INTO agent_session_usage(agent_session_id,usage_hour,model,attribution,input_tokens,uncached_input_tokens,total_tokens) VALUES(?,'2026-09-20T00:00:00Z','gpt-5.5','request',1000000,1000000,1000000)", sid); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err = c.refreshMetricProjections(context.Background(), 150); err != nil {
		t.Fatal(err)
	}
	metadata := statisticsCall(t, c, "query_statistics", map[string]any{"select": []string{"cost_usd", "total_tokens"}, "limit": 100, "max_output_tokens": 8000})
	cards := statisticsCall(t, c, "search_work", map[string]any{"select": []string{"cost_usd", "total_tokens"}, "limit": 100, "max_output_tokens": 4000})
	aggregate := statisticsCall(t, c, "query_statistics", map[string]any{"result_type": "aggregate", "aggregations": []any{map[string]any{"id": "mean", "op": "avg", "metric": "cost_usd"}}})
	mb, cb, ab := len(jsonText(metadata)), len(jsonText(cards)), len(jsonText(aggregate))
	mn, cn := len(statisticsItems(metadata)), len(statisticsItems(cards))
	if mn != 100 || cn == 0 || float64(mb)/float64(mn)*2 >= float64(cb)/float64(cn) {
		t.Fatalf("metadata not compact: %d/%d versus %d/%d", mb, mn, cb, cn)
	}
	if aggregate["total_matching"] != 101 || aggregate["items"] != nil {
		t.Fatalf("aggregate population/payload: %#v", aggregate)
	}
	t.Logf("100-row metadata: %d bytes (~%d tokens); cards: %d bytes for %d rows (~%d tokens); aggregate over 101 workspaces: %d bytes (~%d tokens)", mb, (mb+2)/3, cb, cn, (cb+2)/3, ab, (ab+2)/3)
}

func TestStatisticsReleaseAndConcurrentClients(t *testing.T) {
	c := statisticsFixture(t)
	if _, err := c.refreshMetricProjections(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		result any
		err    error
	}
	done := make(chan outcome, 4)
	for i := 0; i < 4; i++ {
		go func() {
			v, e := callMCP(c, "query_statistics", map[string]any{"select": []string{"total_tokens"}, "retain_snapshot": true})
			done <- outcome{v, e}
		}()
	}
	ids := map[string]bool{}
	for i := 0; i < 4; i++ {
		o := <-done
		if o.err != nil {
			t.Fatal(o.err)
		}
		r := o.result.(map[string]any)
		id := firstString(r["snapshot_id"])
		if ids[id] {
			t.Fatal("snapshot IDs reused across clients")
		}
		ids[id] = true
	}
	for id := range ids {
		got := statisticsCall(t, c, "query_statistics", map[string]any{"release_snapshot": true, "snapshot_id": id})
		if got["released"] != true {
			t.Fatal("release failed")
		}
		if _, err := callMCP(c, "query_statistics", map[string]any{"snapshot_id": id, "select": []string{"total_tokens"}}); err == nil {
			t.Fatal("released snapshot still readable")
		}
	}
}

func TestStatisticsPendingProjectionRetainsRecordedEvidence(t *testing.T) {
	c := statisticsFixture(t)
	_ = statisticsCall(t, c, "query_statistics", map[string]any{"select": []string{"total_tokens"}})
	if _, err := c.DB.Exec("UPDATE agent_session_usage SET total_tokens=9999 WHERE agent_session_id=?", stableID("agent-session", "c1", "main")); err != nil {
		t.Fatal(err)
	}
	r, err := parseMetricRequest("query_statistics", map[string]any{"select": []string{"total_tokens"}})
	if err != nil {
		t.Fatal(err)
	}
	s, err := c.buildStatisticsSnapshot(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Rows) != 1 || s.Rows[0].Metrics["total_tokens"].Status != "pending" || s.Rows[0].Metrics["total_tokens"].effective("available") != nil || *s.Rows[0].Metrics["total_tokens"].Value != 1800000 {
		t.Fatalf("pending evidence %#v", s.Rows)
	}
}

func TestStatisticsTransitiveMirrorsAcrossFilteredIntermediate(t *testing.T) {
	c, _ := libraryFixture(t)
	if _, err := c.DB.Exec("INSERT INTO conversation_identity_links VALUES('cb','cc','native-alias',1,'{}')"); err != nil {
		t.Fatal(err)
	}
	got := statisticsCall(t, c, "query_statistics", map[string]any{"workspace_ids": []string{"a", "c"}, "select": []string{"total_tokens", "mirrored_workspace_ids"}})
	if items := statisticsItems(got); len(items) != 1 || items[0]["workspace_id"] != "a" || items[0]["total_tokens"] != float64(900) {
		t.Fatalf("filtered intermediate duplicated logical workspace: %#v", got)
	}
}

func TestStatisticsAuditCountsDistinguishPagingAndCandidateLimits(t *testing.T) {
	if mcpResultTruncated(map[string]any{"returned_count": 2, "next_cursor": "page", "population_complete": true}) {
		t.Fatal("ordinary metadata paging marked incomplete")
	}
	if !mcpResultTruncated(map[string]any{"returned_count": 2, "population_complete": false}) {
		t.Fatal("limited text population not recorded")
	}
	if n := mcpResultCount(map[string]any{"returned_count": 2, "total_matching": 100}); n == nil || *n != 2 {
		t.Fatal("audit counted population instead of emitted rows")
	}
}

func TestStatisticsBudgetFailureDoesNotConsumeSnapshotSlots(t *testing.T) {
	c := statisticsFixture(t)
	for i := 0; i < 10; i++ {
		if _, err := callMCP(c, "query_statistics", map[string]any{"select": []string{"cost_usd", "total_tokens", "input_tokens", "output_tokens"}, "max_output_tokens": 200}); err == nil {
			t.Fatal("insufficient budget accepted")
		}
	}
	var count int
	if err := c.DB.QueryRow("SELECT COUNT(*) FROM mcp_stat_snapshots").Scan(&count); err != nil || count != 0 {
		t.Fatalf("budget failures leaked %d snapshots: %v", count, err)
	}
	_ = statisticsCall(t, c, "query_statistics", map[string]any{"select": []string{}})
}

func TestStatisticsVectorScoringUsesLeasedSnapshotConnection(t *testing.T) {
	c := statisticsFixture(t)
	if _, err := c.DB.Exec("INSERT INTO conversation_documents(conversation_id,vector_json,model,indexed_at) VALUES('c1',?,'test','x')", jsonText(semanticEmbed("needle"))); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DB.Exec("INSERT INTO conversation_vectors(conversation_id,vector) VALUES('c1',?)", vectorBlob(semanticEmbed("needle"))); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	txs := []*sql.Tx{}
	for i := 0; i < 4; i++ {
		tx, err := c.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		txs = append(txs, tx)
		defer tx.Rollback()
	}
	done := make(chan error, 4)
	for _, tx := range txs {
		go func(tx *sql.Tx) {
			scores, err := statisticsVectorScores(ctx, tx, conversationVectors, "needle", map[string]int{"c1": 0})
			if err == nil && (len(scores) != 1 || scores[0].id != "c1") {
				err = fmt.Errorf("unexpected scores: %#v", scores)
			}
			done <- err
		}(tx)
	}
	for range txs {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestStatisticsOverviewRefreshesRequestedWorkspace(t *testing.T) {
	c := statisticsFixture(t)
	tx, err := c.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec("DELETE FROM mcp_metric_dirty"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 150; i++ {
		id := fmt.Sprintf("dirty-%03d", i)
		if _, err = tx.Exec("INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES(?,'codex','local',?,'dirty','x')", id, id); err != nil {
			t.Fatal(err)
		}
	}
	// Put the requested workspace after more than a full arbitrary batch.
	if _, err = tx.Exec("INSERT INTO mcp_metric_dirty VALUES('w')"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	overview := statisticsCall(t, c, "get_conversation_overview", map[string]any{"conversation_id": "c1", "include_metrics": true, "max_output_tokens": 4000})
	metric := overview["metrics"].(map[string]any)["total_tokens"].(map[string]any)
	if metric["status"] != "complete" || metric["effective_value"] != float64(1100000) {
		t.Fatalf("requested workspace not refreshed on first call: %#v", metric)
	}
	var dirty int
	if err = c.DB.QueryRow("SELECT COUNT(*) FROM mcp_metric_dirty").Scan(&dirty); err != nil || dirty != 150 {
		t.Fatalf("overview refreshed unrelated workspaces: %d dirty, %v", dirty, err)
	}
}

func TestStatisticsOverviewInactiveMemberIsUnavailable(t *testing.T) {
	c := statisticsFixture(t)
	args := map[string]any{"conversation_id": "c1", "include_metrics": true, "max_output_tokens": 4000}
	_ = statisticsCall(t, c, "get_conversation_overview", args)
	if _, err := c.DB.Exec("INSERT INTO conversation_group_membership VALUES('c1','w',0)"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		overview := statisticsCall(t, c, "get_conversation_overview", args)
		if overview["metrics_unavailable_reason"] != "inactive_group_member" {
			t.Fatalf("missing inactive-member explanation: %#v", overview)
		}
		for name, raw := range overview["metrics"].(map[string]any) {
			metric := raw.(map[string]any)
			if metric["status"] != "unavailable" || metric["effective_value"] != nil || metric["value"].(*float64) != nil {
				t.Fatalf("inactive %s falsely pending or valued: %#v", name, metric)
			}
		}
	}
	if _, err := c.DB.Exec("UPDATE conversation_group_membership SET active=1 WHERE conversation_id='c1'"); err != nil {
		t.Fatal(err)
	}
	overview := statisticsCall(t, c, "get_conversation_overview", args)
	metric := overview["metrics"].(map[string]any)["total_tokens"].(map[string]any)
	if metric["status"] != "complete" || metric["effective_value"] != float64(1100000) || overview["metrics_unavailable_reason"] != nil {
		t.Fatalf("reactivated member not refreshed: %#v", overview)
	}
}

func TestStatisticsOneShotQueriesDoNotConsumeSnapshotSlots(t *testing.T) {
	c := statisticsFixture(t)
	queries := []struct {
		tool string
		args map[string]any
	}{
		{"query_statistics", map[string]any{"result_type": "aggregate", "aggregations": []any{map[string]any{"id": "count", "op": "count"}}}},
		{"query_statistics", map[string]any{"select": []string{"total_tokens"}}},
		{"query_statistics", map[string]any{"select": []string{"conversation_ids"}}},
		{"search_work", map[string]any{"result_mode": "metadata", "select": []string{}}},
		{"search_conversations", map[string]any{"result_mode": "metadata", "select": []string{"total_tokens"}}},
	}
	for _, query := range queries {
		for i := 0; i < 9; i++ {
			got := statisticsCall(t, c, query.tool, query.args)
			if got["snapshot_id"] != nil || got["snapshot_created_at"] != nil || got["snapshot_expires_at"] != nil || got["next_cursor"] != nil {
				t.Fatalf("one-shot %s unnecessarily retained state: %#v", query.tool, got)
			}
		}
	}
	var count int
	if err := c.DB.QueryRow("SELECT COUNT(*) FROM mcp_stat_snapshots").Scan(&count); err != nil || count != 0 {
		t.Fatalf("one-shot queries leaked %d snapshots: %v", count, err)
	}
	// Even a full set of explicitly retained snapshots must not block one-shots.
	retained := []string{}
	for i := 0; i < 8; i++ {
		got := statisticsCall(t, c, "query_statistics", map[string]any{"retain_snapshot": true, "select": []string{"total_tokens"}})
		id := firstString(got["snapshot_id"])
		if id == "" {
			t.Fatal("explicit retention ignored")
		}
		retained = append(retained, id)
	}
	for _, query := range queries {
		_ = statisticsCall(t, c, query.tool, query.args)
	}
	if _, err := callMCP(c, "query_statistics", map[string]any{"retain_snapshot": true, "select": []string{}}); err == nil {
		t.Fatal("explicit retention exceeded the snapshot cap")
	}
	for _, id := range retained {
		got := statisticsCall(t, c, "query_statistics", map[string]any{"snapshot_id": id, "select": []string{"total_tokens"}})
		if got["snapshot_id"] != id || statisticsItems(got)[0]["total_tokens"] != float64(1800000) {
			t.Fatal("one-shot query evicted a retained snapshot")
		}
	}
	for _, tool := range []string{"search_work", "search_conversations"} {
		if _, err := callMCP(c, tool, map[string]any{"retain_snapshot": true}); err == nil {
			t.Fatal("card mode accepted snapshot retention")
		}
	}
}
