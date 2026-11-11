package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	qt "github.com/Pythia-Software/query-table/backends/go"
)

func TestSessionEffortSummarizesOnlyRecordedRequests(t *testing.T) {
	for _, tc := range []struct {
		name, settings string
		levels         []string
		known, unknown int64
		state          string
	}{
		{"startup-unknown", `"high","high"`, []string{"high"}, 2, 0, "single"},
		{"unknown-request", `null,"high"`, []string{"high"}, 1, 1, "single"},
		{"explicit-reset", `"high",null`, []string{"high"}, 1, 1, "single"},
		{"empty-reset", `"high",""`, []string{"high"}, 1, 1, "single"},
		{"multiple", `"high","low"`, []string{"high", "low"}, 2, 0, "multiple"},
		{"multiple-unknown", `"high",null,"low"`, []string{"high", "low"}, 2, 1, "multiple"},
		{"unknown-only", `null`, []string{}, 0, 1, "unknown"},
		{"literal-none", `"none"`, []string{"none"}, 1, 0, "single"},
		{"no-requests", ``, []string{}, 0, 0, "no-requests"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := testCatalog(t)
			dir := t.TempDir()
			lines := []string{
				`{"type":"session_meta","payload":{"id":"summary","model":"gpt-6"}}`,
				`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"startup before configuration"}]}}`,
			}
			if tc.settings != "" {
				for i, setting := range strings.Split(tc.settings, ",") {
					lines = append(lines,
						fmt.Sprintf(`{"type":"turn_context","payload":{"model":"gpt-6","effort":%s}}`, setting),
						fmt.Sprintf(`{"type":"token_usage_record","timestamp":"2026-09-20T12:0%d:00Z","payload":{"response_id":"request-%d","usage":{"input_tokens":100,"output_tokens":20}}}`, i, i))
				}
			}
			writeSourceFile(t, filepath.Join(dir, "rollout-summary.jsonl"), strings.Join(lines, "\n"))
			ingestSource(t, c, SourceConfig{Name: "summary", Kind: "codex", Path: dir, Account: "local", Enabled: true})
			var summary string
			var strict sql.NullString
			var workspace string
			if err := c.DB.QueryRow(`SELECT es.summary_json,a.effort,a.workspace_id FROM agent_sessions a`+sessionEffortSummaryJoin).Scan(&summary, &strict, &workspace); err != nil {
				t.Fatal(err)
			}
			want := &sessionEffortSummary{Levels: tc.levels, KnownRequests: tc.known, UnknownRequests: tc.unknown, State: tc.state, Coverage: "recorded-requests"}
			if got := readSessionEffortSummary(summary); !reflect.DeepEqual(got, want) {
				t.Fatalf("summary %s, want %+v", summary, want)
			}
			if strict.String != "" {
				t.Fatalf("summary changed strict configuration: %q", strict.String)
			}
			detail, err := c.WorkDetail(workspace)
			if err != nil {
				t.Fatal(err)
			}
			if got := detail["agent_sessions"].([]map[string]any)[0]["effort_summary"]; !reflect.DeepEqual(got, want) {
				t.Fatalf("detail summary %+v, want %+v", got, want)
			}
			bounded, err := c.boundedWorkDetail(map[string]any{"workspace_id": workspace, "section": "agent_sessions"})
			if err != nil {
				t.Fatal(err)
			}
			if got := bounded["items"].([]map[string]any)[0]["effort_summary"]; !reflect.DeepEqual(got, want) {
				t.Fatalf("bounded summary %+v, want %+v", got, want)
			}
		})
	}
}

func TestSessionEffortBackfillPreservesUsageAndResumes(t *testing.T) {
	c, config := testCatalog(t)
	seedDelegation(t, c)
	if err := c.replacePricing(testPricing(), "test"); err != nil {
		t.Fatal(err)
	}
	execTx(t, c.DB,
		`UPDATE model_requests SET effort='low' WHERE id='request'`,
		`INSERT INTO model_requests(id,conversation_id,agent_session_id,sequence,model,effort,total_tokens) VALUES('root-request','root','root-session',0,'claude-opus-4-8','high',30)`,
		`INSERT INTO agent_session_usage(agent_session_id,usage_hour,model,effort,attribution,input_tokens,uncached_input_tokens,total_tokens) VALUES('root-session','2026-01-01T00:00:00Z','claude-opus-4-8','high','request',30,30,30),('root-session','2026-01-01T00:00:00Z','claude-opus-4-8','','session-remainder',70,70,70)`,
		`INSERT INTO usage_attribution_state(workspace_id,version) VALUES('delegation-work','`+usageAttributionVersion+`')`,
	)
	snapshot := func(c *Catalog) string {
		t.Helper()
		data := map[string]any{}
		for _, table := range []string{"agent_sessions", "agent_session_usage", "model_requests", "metrics", "usage_attribution_state", "messages", "conversations"} {
			rows, err := queryMaps(c.DB, "SELECT * FROM "+table+" ORDER BY 1")
			if err != nil {
				t.Fatal(err)
			}
			data[table] = rows
		}
		ledger, err := queryMaps(c.DB, `SELECT u.*,u.model usage_model,a.model session_model,a.conversation_id,a.workspace_id
 FROM agent_session_usage u JOIN agent_sessions a ON a.id=u.agent_session_id
 ORDER BY u.agent_session_id,u.usage_hour,u.model,u.effort,u.attribution`)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := c.usageRowsFromLedger(time.UTC, false, ledger)
		if err != nil {
			t.Fatal(err)
		}
		priced := false
		for _, row := range rows {
			if cost, ok := row["cost_usd"].(float64); ok && cost > 0 {
				priced = true
			}
		}
		if !priced {
			t.Fatal("backfill fixture needs a nonzero calculated cost")
		}
		data["usage_and_costs"] = rows
		return jsonText(data)
	}
	before := snapshot(c)
	override(t, &sessionEffortRepairBatchSize, 2)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done, err := c.RepairSessionEffortSummaries(ctx, func(done, total int) { cancel() })
	if done != 2 || !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted repair: %d, %v", done, err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	c, err = OpenCatalog(config.CatalogPath)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if done, err := c.RepairSessionEffortSummaries(t.Context(), nil); done != 4 || err != nil {
		t.Fatalf("resumed repair: %d, %v", done, err)
	}
	if got := snapshot(c); got != before {
		t.Fatal("summary backfill changed IDs, tokens, buckets, effort, costs, or source records")
	}
	stamp, err := readCacheGenerations(t.Context(), c.DB)
	if err != nil {
		t.Fatal(err)
	}
	if done, err := c.RepairSessionEffortSummaries(t.Context(), nil); done != 0 || err != nil {
		t.Fatalf("repeated repair: %d, %v", done, err)
	}
	if got, err := readCacheGenerations(t.Context(), c.DB); err != nil || got != stamp {
		t.Fatalf("repeated repair wrote cache state: %+v -> %+v, %v", stamp, got, err)
	}
	fixtureExec(t, c, `UPDATE agent_session_effort_summaries SET version='old' WHERE agent_session_id='root-session'`)
	if done, err := c.RepairSessionEffortSummaries(t.Context(), nil); done != 1 || err != nil {
		t.Fatalf("stale summary repair: %d, %v", done, err)
	}
	// Request changes invalidate the projection without substituting defaults.
	fixtureExec(t, c, `UPDATE model_requests SET effort=NULL WHERE id='root-request'`)
	var count int
	if err := c.DB.QueryRow(`SELECT COUNT(*) FROM agent_session_effort_summaries WHERE agent_session_id='root-session'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("stale summary survived request reset: %d %v", count, err)
	}
	if done, err := c.RepairSessionEffortSummaries(t.Context(), nil); done != 1 || err != nil {
		t.Fatalf("reset repair: %d, %v", done, err)
	}
	// Moving a request invalidates both its previous owner and its new owner.
	fixtureExec(t, c, `UPDATE model_requests SET agent_session_id='embedded' WHERE id='root-request'`)
	if done, err := c.RepairSessionEffortSummaries(t.Context(), nil); done != 2 || err != nil {
		t.Fatalf("moved request repair: %d, %v", done, err)
	}
	fixtureExec(t, c, `DELETE FROM model_requests WHERE id='root-request'`)
	if done, err := c.RepairSessionEffortSummaries(t.Context(), nil); done != 1 || err != nil {
		t.Fatalf("deleted request repair: %d, %v", done, err)
	}
}

func TestEffortParsingPreservesTurnChangesAndUnknowns(t *testing.T) {
	parser := &jsonlAdapter{baseAdapter: baseAdapter{config: SourceConfig{Account: "local"}}, provider: "codex"}
	events := []map[string]any{}
	for index, line := range []string{
		`{"type":"session_meta","payload":{"id":"effort","model":"gpt-6"}}`,
		`{"type":"event_msg","payload":{"type":"agent_message","message":"Unknown"}}`,
		`{"type":"turn_context","payload":{"model":"gpt-6","effort":"LOW"}}`,
		`{"type":"event_msg","payload":{"type":"agent_message","message":"Low"}}`,
		`{"type":"turn_context","payload":{"model":"gpt-6","reasoning_effort":"xhigh"}}`,
		`{"type":"event_msg","payload":{"type":"agent_message","message":"Extra high"}}`,
		`{"type":"turn_context","payload":{"model":"gpt-6","effort":null}}`,
		`{"type":"event_msg","payload":{"type":"agent_message","message":"Reset"}}`,
	} {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		event["_line"] = index + 1
		events = append(events, event)
	}
	record, ok, err := parser.codex("rollout.jsonl", events)
	if err != nil || !ok {
		t.Fatalf("parse: %v %v", ok, err)
	}
	efforts := []string{}
	for _, message := range record.Conversations[0].Messages {
		efforts = append(efforts, message.Effort)
	}
	if !reflect.DeepEqual(efforts, []string{"", "low", "xhigh", ""}) {
		t.Fatalf("efforts = %v", efforts)
	}
	if record.Conversations[0].Effort != "" {
		t.Fatal("reset effort should remain unknown")
	}
	// A numeric thinking budget, prose, or future unrecognized level is not coerced into a known default.
	if recordedEffort(map[string]any{"thinking": map[string]any{"budget_tokens": 32000}}) != "" {
		t.Fatal("budget is not effort")
	}
	if recordedEffort(map[string]any{"reasoning": map[string]any{"effort": " Future "}}) != "future" {
		t.Fatal("lost provider level")
	}
}

func TestEffortUsageAccountingQueriesAndExports(t *testing.T) {
	catalog, config := testCatalog(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "rollout-effort.jsonl")
	fixture := strings.Join([]string{
		`{"type":"session_meta","timestamp":"2026-09-20T12:00:00Z","payload":{"id":"effort-usage","model":"gpt-6-sol"}}`,
		`{"type":"token_usage_record","timestamp":"2026-09-20T12:01:00Z","payload":{"response_id":"r0","usage":{"input_tokens":10,"output_tokens":1,"total_tokens":11}}}`,
		`{"type":"turn_context","payload":{"model":"gpt-6-sol","effort":"low"}}`,
		`{"type":"response_item","timestamp":"2026-09-20T12:02:00Z","payload":{"type":"function_call","call_id":"call-low","name":"exec_command","arguments":"{\"cmd\":\"cat SKILL.md\"}"}}`,
		`{"type":"response_item","timestamp":"2026-09-20T12:02:01Z","payload":{"type":"function_call_output","call_id":"call-low","output":"a skill"}}`,
		`{"type":"token_usage_record","timestamp":"2026-09-20T12:03:00Z","payload":{"response_id":"r1","usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120}}}`,
		`{"type":"turn_context","payload":{"model":"gpt-6-sol","reasoning_effort":"high"}}`,
		`{"type":"response_item","timestamp":"2026-09-20T12:04:00Z","payload":{"type":"function_call","call_id":"call-high","name":"exec_command","arguments":"{\"cmd\":\"git status\"}"}}`,
		`{"type":"response_item","timestamp":"2026-09-20T12:04:01Z","payload":{"type":"function_call_output","call_id":"call-high","output":"clean"}}`,
		`{"type":"token_usage_record","timestamp":"2026-09-20T12:05:00Z","payload":{"response_id":"r2","usage":{"input_tokens":200,"output_tokens":30,"total_tokens":230}}}`,
	}, "\n")
	writeSourceFile(t, path, fixture)
	adapter, err := MakeAdapter(SourceConfig{Name: "effort", Kind: "codex", Path: dir, Account: "local", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	ingest := func() {
		t.Helper()
		if result := catalog.Ingest(adapter, nil); result.Error != nil {
			t.Fatal(result.Error)
		}
	}
	ingest()
	verify := func(high string) {
		t.Helper()
		rows, err := catalog.usageRows(time.Local)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]int64{}
		for _, row := range rows {
			got[firstString(row["effort"])] += integer(row["total_tokens"])
		}
		want := map[string]int64{"": 11, "low": 120, high: 230}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("usage %v want %v", got, want)
		}
		hours, err := catalog.usageRowsByPeriod(time.Local, true)
		if err != nil || len(hours) != 3 {
			t.Fatalf("hourly buckets %v: %v", hours, err)
		}
		v2Schema, err := sqliteQuerySchema("usage")
		if err != nil {
			t.Fatal(err)
		}
		for _, scope := range []string{"", "shownRows"} {
			q := metricRequest(qt.AggSpec{ID: "effort-tokens", Expression: "SUM([total_tokens])", GroupBy: []string{"hour", "effort"}, Scope: scope})
			q.Where = []qt.WhereTerm{{Field: "effort", Op: "=", Value: high}}
			q.Limit = 1
			v2Result, err := testServer(t, config, catalog).executeQueryFixture(context.Background(), "usage", nil, v2Schema, nil, &q)
			if err != nil {
				t.Fatal(err)
			}
			if len(v2Result.Metrics.Metrics) != 1 || len(v2Result.Metrics.Metrics[0].Buckets) != 1 || integer(v2Result.Metrics.Metrics[0].Buckets[0].Value) != 230 {
				t.Fatalf("V2 filtered hourly metrics (%s): %#v", scope, v2Result)
			}
		}
		requests, err := queryMaps(catalog.DB, "SELECT effort,total_tokens FROM model_requests")
		if err != nil {
			t.Fatal(err)
		}
		requestTotals := map[string]int64{}
		for _, row := range requests {
			requestTotals[firstString(row["effort"])] += integer(row["total_tokens"])
		}
		if !reflect.DeepEqual(requestTotals, want) {
			t.Fatalf("requests: %v", requestTotals)
		}
	}
	verify("high")
	// No totals change on an idempotent ingest; metadata-only corrections rebuild the affected rows.
	ingest()
	verify("high")
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(fixture, `"reasoning_effort":"high"`, `"reasoning_effort":"max"`)), 0600); err != nil {
		t.Fatal(err)
	}
	ingest()
	verify("max")
	calls, err := queryMaps(catalog.DB, "SELECT effort FROM tool_calls ORDER BY sequence")
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0]["effort"] != "low" || calls[1]["effort"] != "max" {
		t.Fatalf("calls: %v", calls)
	}
	if err := catalog.ensureToolRollup(context.Background()); err != nil {
		t.Fatal(err)
	}
	rollup, err := queryMaps(catalog.DB, "SELECT effort,SUM(call_count) calls FROM tool_usage_daily GROUP BY effort")
	if err != nil || len(rollup) != 2 {
		t.Fatalf("rollup: %v %v", rollup, err)
	}
	works, err := catalog.searchRows(SearchOptions{Effort: "low"}, nil)
	if err != nil || len(works) != 1 {
		t.Fatalf("search: %v %v", works, err)
	}
	for effort, count := range map[string]int{"low": 1, "max": 1, "high": 0, " LOW ": 1, " Max ": 1, " High ": 0, "   ": 1} {
		rows, err := catalog.searchRows(SearchOptions{Effort: effort}, nil)
		if err != nil || len(rows) != count {
			t.Fatalf("work effort filter %q: %v %v", effort, rows, err)
		}
		result, err := catalog.searchConversations(map[string]any{"effort": effort})
		if err != nil || len(result["items"].([]map[string]any)) != count {
			t.Fatalf("MCP effort filter %q: %v %v", effort, result, err)
		}
	}
	library, err := catalog.cachedLibraryRows(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(library) != 1 || !strings.Contains(firstString(library[0]["efforts"]), "low") || !strings.Contains(firstString(library[0]["efforts"]), "max") {
		t.Fatalf("library: %v", library)
	}
}

func TestEffortUsageMigrationPreservesLegacyAccounting(t *testing.T) {
	catalog, _ := testCatalog(t)
	ingestUsageFixture(t, catalog)
	// Recreate the previous layout using exactly its old key, then initialize again.
	if _, err := catalog.DB.Exec(`ALTER TABLE agent_session_usage RENAME TO usage_new;
 CREATE TABLE agent_session_usage AS SELECT agent_session_id,usage_hour,model,attribution,input_tokens,uncached_input_tokens,
 cache_read_input_tokens,cache_creation_input_tokens,cache_creation_5m_input_tokens,cache_creation_1h_input_tokens,
 output_tokens,reasoning_output_tokens,unclassified_tokens,total_tokens FROM usage_new;
 DROP TABLE usage_new;`); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Initialize(); err != nil {
		t.Fatal(err)
	}
	var total, unknown int64
	if err := catalog.DB.QueryRow("SELECT SUM(total_tokens),SUM(effort='') FROM agent_session_usage").Scan(&total, &unknown); err != nil {
		t.Fatal(err)
	}
	if total != 3050 || unknown != 2 {
		t.Fatalf("migrated total=%d unknown=%d", total, unknown)
	}
	if _, err := catalog.DB.Exec(`INSERT INTO agent_session_usage(agent_session_id,usage_hour,model,effort,attribution,total_tokens)
 SELECT agent_session_id,usage_hour,model,'high',attribution,1 FROM agent_session_usage LIMIT 1`); err != nil {
		t.Fatal("effort is not part of the new primary key:", err)
	}
	if err := catalog.Initialize(); err != nil {
		t.Fatal(err)
	}
	if err := catalog.DB.QueryRow("SELECT SUM(total_tokens) FROM agent_session_usage").Scan(&total); err != nil || total != 3051 {
		t.Fatalf("second initialization: %d %v", total, err)
	}
}

func TestEffortFromClaudeAndCanonicalSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude.jsonl")
	writeSourceFile(t, path, strings.Join([]string{
		`{"type":"system","effortLevel":"medium","sessionId":"effort-claude"}`,
		`{"type":"assistant","uuid":"a1","message":{"id":"r1","model":"claude-opus-5-5","output_config":{"effort":"max"},"content":[{"type":"text","text":"Done"}],"usage":{"input_tokens":20,"output_tokens":10}}}`,
	}, "\n"))
	parser := &jsonlAdapter{baseAdapter: baseAdapter{config: SourceConfig{Account: "local"}}, provider: "claude"}
	record, ok, err := parser.parse(path)
	if err != nil || !ok {
		t.Fatalf("claude parse: %v %v", ok, err)
	}
	conversation := record.Conversations[0]
	if conversation.Effort != "max" {
		t.Fatalf("claude effort: %q", conversation.Effort)
	}
	for _, message := range conversation.Messages {
		if message.Effort != "max" {
			t.Fatalf("claude message effort: %q", message.Effort)
		}
	}
	export := &canonicalAdapter{baseAdapter: baseAdapter{config: SourceConfig{Kind: "canonical", Account: "local"}}}
	row := map[string]any{"id": "canonical-effort", "conversations": []any{map[string]any{
		"id": "canonical", "model": "gpt-6", "effort": "high", "messages": []any{
			map[string]any{"id": "inherit", "role": "assistant", "text": "inherited"},
			map[string]any{"id": "override", "role": "assistant", "text": "low", "effort": "low"},
			map[string]any{"id": "reset", "role": "assistant", "text": "unknown", "effort": nil},
		}}}}
	record, err = export.workspace(row, "export.json", 0)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, message := range record.Conversations[0].Messages {
		got = append(got, message.Effort)
	}
	if !reflect.DeepEqual(got, []string{"high", "low", ""}) {
		t.Fatalf("canonical effort: %v", got)
	}
}

func TestEffortFromConductorColumnsAndEnvelopes(t *testing.T) {
	catalog, _ := testCatalog(t)
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "conductor.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE sessions(id TEXT PRIMARY KEY,model TEXT,codex_thinking_level TEXT,claude_effort_level TEXT,agent_type TEXT);
 CREATE TABLE session_messages(id TEXT PRIMARY KEY,session_id TEXT,role TEXT,content TEXT,created_at TEXT);
 INSERT INTO sessions VALUES('effort-codex','gpt-6',' HIGH ','medium','codex');
 INSERT INTO sessions VALUES('effort-claude','claude-opus-5-5','high',' medium ','claude');
 INSERT INTO session_messages VALUES('m1','effort-codex','assistant','{"type":"assistant","message":{"id":"r-codex","usage":{"input_tokens":5,"output_tokens":10},"content":[{"type":"text","text":"hello"},{"type":"tool_use","id":"call-codex","name":"Read","input":{}}]}}','2026-09-20T12:00:00Z');
 INSERT INTO session_messages VALUES('m2','effort-codex','assistant','{"type":"assistant","message":{"reasoning":{"effort":"max"},"content":[{"type":"text","text":"changed"}]}}','2026-09-20T12:01:00Z');
 INSERT INTO session_messages VALUES('m3','effort-codex','assistant','still max','2026-09-20T12:02:00Z');
 INSERT INTO session_messages VALUES('m4','effort-codex','assistant','{"type":"assistant","message":{"effort":null,"content":[{"type":"text","text":"reset"}]}}','2026-09-20T12:03:00Z');
 INSERT INTO session_messages VALUES('m5','effort-claude','assistant','{"type":"assistant","message":{"id":"r-claude","usage":{"input_tokens":5,"output_tokens":10},"content":[{"type":"text","text":"hello"},{"type":"tool_use","id":"call-claude","name":"Read","input":{}}]}}','2026-09-20T12:00:00Z');`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	adapter, err := MakeAdapter(SourceConfig{Name: "conductor-effort", Kind: "conductor", Path: dir, Account: "local", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if result := catalog.Ingest(adapter, nil); result.Error != nil {
		t.Fatal(result.Error)
	}
	rows, err := queryMaps(catalog.DB, "SELECT m.text,m.effort FROM messages m JOIN conversations c ON c.id=m.conversation_id WHERE c.provider='codex' AND m.kind='message' ORDER BY m.source_order")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 || rows[0]["effort"] != "high" || rows[1]["effort"] != "max" || rows[2]["effort"] != "max" || rows[3]["effort"] != nil {
		t.Fatalf("conductor efforts: %v", rows)
	}
	conversations, err := queryMaps(catalog.DB, "SELECT provider,effort FROM conversations ORDER BY provider")
	if err != nil || len(conversations) != 2 || conversations[0]["effort"] != "medium" || conversations[1]["effort"] != nil {
		t.Fatalf("conductor conversation efforts: %v %v", conversations, err)
	}
	for _, table := range []string{"messages", "model_requests", "tool_calls"} {
		rows, err := queryMaps(catalog.DB, "SELECT c.provider,x.effort FROM "+table+" x JOIN conversations c ON c.id=x.conversation_id WHERE x.effort IN ('high','medium') ORDER BY c.provider")
		if err != nil || len(rows) < 2 || rows[0]["provider"] != "claude" || rows[len(rows)-1]["provider"] != "codex" {
			t.Fatalf("%s missing session effort: %v %v", table, rows, err)
		}
		for _, row := range rows {
			want := map[string]string{"claude": "medium", "codex": "high"}[firstString(row["provider"])]
			if row["effort"] != want {
				t.Fatalf("%s used the inactive agent's setting: %v", table, rows)
			}
		}
	}
	usage, err := queryMaps(catalog.DB, "SELECT effort,SUM(total_tokens) total FROM agent_session_usage GROUP BY effort ORDER BY effort")
	if err != nil || len(usage) != 2 || usage[0]["effort"] != "high" || usage[1]["effort"] != "medium" || integer(usage[0]["total"]) != 15 || integer(usage[1]["total"]) != 15 {
		t.Fatalf("conductor usage efforts: %v %v", usage, err)
	}
}

func TestConductorObservedModelSurvivesSyntheticError(t *testing.T) {
	catalog, _ := testCatalog(t)
	dir := t.TempDir()
	db := openWritableSQLite(t, filepath.Join(dir, "conductor.db"))
	execSQL(t, db,
		`CREATE TABLE sessions(id TEXT PRIMARY KEY,model TEXT,agent_type TEXT)`,
		`CREATE TABLE session_messages(id TEXT PRIMARY KEY,session_id TEXT,role TEXT,content TEXT,created_at TEXT)`,
		`INSERT INTO sessions VALUES('s','claude-opus-5','claude')`,
		`INSERT INTO session_messages VALUES('a','s','assistant','{"type":"assistant","message":{"model":"claude-sonnet-5","content":[{"type":"text","text":"reply"}],"usage":{"input_tokens":10,"output_tokens":1}}}','2026-09-20T12:00:00Z')`,
		`INSERT INTO session_messages VALUES('b','s','assistant','{"type":"assistant","message":{"model":"<synthetic>","content":[{"type":"text","text":"failed"}]}}','2026-09-20T12:01:00Z')`,
		`INSERT INTO session_messages VALUES('c','s','system','{"type":"result","usage":{"input_tokens":100,"output_tokens":10},"modelUsage":{"claude-sonnet-5":{"contextWindow":200000},"claude-haiku-5":{"contextWindow":200000}}}','2026-09-20T12:02:00Z')`)
	db.Close()
	ingestFixture(t, catalog, "conductor", dir)
	for _, table := range []string{"conversations", "agent_sessions"} {
		var model string
		if err := catalog.DB.QueryRow("SELECT model FROM " + table).Scan(&model); err != nil || model != "claude-sonnet-5" {
			t.Fatalf("%s model=%q: %v", table, model, err)
		}
	}
	var model, raw string
	if err := catalog.DB.QueryRow(`SELECT model,raw_text FROM messages WHERE text='failed'`).Scan(&model, &raw); err != nil || model != "<synthetic>" || raw == "" {
		t.Fatalf("error evidence=%q %q: %v", model, raw, err)
	}
	rows, err := queryMaps(catalog.DB, `SELECT model,total_tokens FROM agent_session_usage ORDER BY model`)
	if err != nil || len(rows) != 2 || rows[0]["model"] != "Unknown model" || integer(rows[0]["total_tokens"]) != 99 || rows[1]["model"] != "claude-sonnet-5" || integer(rows[1]["total_tokens"]) != 11 {
		t.Fatalf("usage=%v: %v", rows, err)
	}
	db = openWritableSQLite(t, filepath.Join(dir, "conductor.db"))
	execSQL(t, db, `ALTER TABLE session_messages ADD COLUMN full_message TEXT`)
	display := strings.Repeat("request display ", 40)
	if _, err := db.Exec(`INSERT INTO session_messages VALUES('d','s','assistant',?,'2026-09-20T12:03:00Z',?)`, display, `{"type":"assistant","message":{"id":"short-request","model":"claude-opus-4-8","usage":{"input_tokens":20,"output_tokens":2}}}`); err != nil {
		t.Fatal(err)
	}
	execSQL(t, db, `INSERT INTO session_messages VALUES('e','s','assistant','continued','2026-09-20T12:04:00Z',NULL)`)
	db.Close()
	ingestFixture(t, catalog, "conductor", dir)
	for _, table := range []string{"conversations", "agent_sessions"} {
		var model string
		if err := catalog.DB.QueryRow("SELECT model FROM " + table).Scan(&model); err != nil || model != "claude-opus-4-8" {
			t.Fatalf("alternate request %s model=%q: %v", table, model, err)
		}
	}
}

func TestConductorAlternateModelRetainsChildOwnership(t *testing.T) {
	catalog, _ := testCatalog(t)
	dir := t.TempDir()
	db := openWritableSQLite(t, filepath.Join(dir, "conductor.db"))
	execSQL(t, db,
		`CREATE TABLE sessions(id TEXT PRIMARY KEY,model TEXT,agent_type TEXT)`,
		`CREATE TABLE session_messages(id TEXT PRIMARY KEY,session_id TEXT,role TEXT,content TEXT,created_at TEXT,full_message TEXT)`,
		`INSERT INTO sessions VALUES('s','claude-opus-4-8','claude')`,
		`INSERT INTO session_messages VALUES('a','s','assistant','{"type":"assistant","message":{"model":"claude-opus-4-8","content":[{"type":"text","text":"parent"},{"type":"tool_use","id":"child","name":"Task","input":{}}]}}','2026-09-20T12:00:00Z',NULL)`)
	display := strings.Repeat("child reply ", 50)
	if _, err := db.Exec(`INSERT INTO session_messages VALUES('b','s','assistant',?,'2026-09-20T12:01:00Z',?)`, display,
		`{"type":"assistant","parent_tool_use_id":"child","message":{"id":"child-request","model":"claude-haiku-5","usage":{"input_tokens":10,"output_tokens":1}}}`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	ingestFixture(t, catalog, "conductor", dir)
	var model string
	if err := catalog.DB.QueryRow(`SELECT model FROM conversations`).Scan(&model); err != nil || model != "claude-opus-4-8" {
		t.Fatalf("parent conversation model=%q: %v", model, err)
	}
	for native, want := range map[string]string{"main": "claude-opus-4-8", "child": "claude-haiku-5"} {
		if err := catalog.DB.QueryRow(`SELECT model FROM agent_sessions WHERE native_id=?`, native).Scan(&model); err != nil || model != want {
			t.Fatalf("%s model=%q, want %q: %v", native, model, want, err)
		}
	}
	var parent string
	if err := catalog.DB.QueryRow(`SELECT parent_native_id FROM messages WHERE text=?`, display).Scan(&parent); err != nil || parent != "child" {
		t.Fatalf("child display parent=%q: %v", parent, err)
	}
	if total := countRows(t, catalog, `SELECT SUM(u.total_tokens) FROM agent_session_usage u JOIN agent_sessions a ON a.id=u.agent_session_id WHERE a.native_id='child'`); total != 11 {
		t.Fatalf("child tokens=%d", total)
	}
}

func TestConductorSessionEffortUsesOnlyActiveAgentSetting(t *testing.T) {
	for _, fixture := range []struct {
		name    string
		session map[string]any
		want    string
	}{
		{"codex", map[string]any{"agent_type": "codex", "codex_thinking_level": " HIGH ", "claude_effort_level": "medium"}, "high"},
		{"claude", map[string]any{"agent_type": "claude", "codex_thinking_level": "high", "claude_effort_level": " Medium "}, "medium"},
		{"unset", map[string]any{"agent_type": "codex", "codex_thinking_level": nil, "claude_effort_level": "high", "effort": "max"}, ""},
		{"empty", map[string]any{"agent_type": "claude", "codex_thinking_level": "high", "claude_effort_level": " "}, ""},
		{"unknown agent", map[string]any{"agent_type": "other", "codex_thinking_level": "high", "claude_effort_level": "medium"}, ""},
		{"legacy", map[string]any{"agent_type": "codex", "reasoning_effort": " Low "}, "low"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			if got := conductorSessionEffort(fixture.session); got != fixture.want {
				t.Fatalf("effort = %q, want %q", got, fixture.want)
			}
		})
	}
}

func TestEffortMessageIndexCreatedAfterLegacyColumnMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(strings.ReplaceAll(schemaSQL(), "  effort TEXT,\n", "")); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	catalog, err := OpenCatalog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	var index string
	if err := catalog.DB.QueryRow("SELECT sql FROM sqlite_master WHERE name='messages_conversation_model_effort_idx'").Scan(&index); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(index, "WHERE model IS NOT NULL OR effort IS NOT NULL") {
		t.Fatalf("missing partial effort index: %s", index)
	}
}

func TestEffortDoesNotAssignMixedAggregateToFinalSetting(t *testing.T) {
	messages := []MessageRecord{
		{NativeID: "low", Role: "assistant", Kind: "message", Text: "low", Effort: "low"},
		{NativeID: "high", Role: "assistant", Kind: "message", Text: "high", Effort: "high"},
		{NativeID: "result", Role: "system", Kind: "metadata", Text: `{"type":"result","usage":{"input_tokens":100,"output_tokens":20}}`, Effort: "high"},
	}
	sessions := agentSessionSummaries(messages)
	for bucket := range sessions[0].usage {
		if bucket.effort != "" {
			t.Fatalf("aggregate mislabeled %q", bucket.effort)
		}
	}
	for index := range messages {
		messages[index].Effort = "high"
	}
	sessions = agentSessionSummaries(messages)
	for bucket := range sessions[0].usage {
		if bucket.effort != "high" {
			t.Fatalf("uniform attempt lost effort: %q", bucket.effort)
		}
	}
	for name, want := range map[string]string{"Gemini 3.8 Flash (High)": "high", "Gemini Pro (Low)": "low", "Gemini Flash": "", "Claude (Thinking)": ""} {
		if got := antigravitySelectedEffort(name); got != want {
			t.Fatalf("selector %q = %q, want %q", name, got, want)
		}
	}
}

func TestEffortTL1InitialConfigurationRespectsTranscriptChanges(t *testing.T) {
	for _, fixture := range []struct {
		name, provider, content string
	}{
		{"codex-rollout.jsonl", "codex", strings.Join([]string{
			`{"type":"session_meta","payload":{"id":"effort-tl1","model":"gpt-6"}}`,
			`{"type":"event_msg","payload":{"type":"agent_message","message":"initial"}}`,
			`{"type":"turn_context","payload":{"model":"gpt-6","effort":"low"}}`,
			`{"type":"event_msg","payload":{"type":"agent_message","message":"changed"}}`,
			`{"type":"turn_context","payload":{"model":"gpt-6","effort":null}}`,
			`{"type":"event_msg","payload":{"type":"agent_message","message":"reset"}}`,
		}, "\n")},
		{"claude.jsonl", "claude", strings.Join([]string{
			`{"type":"assistant","uuid":"a1","message":{"role":"assistant","content":[{"type":"text","text":"initial"}]}}`,
			`{"type":"assistant","uuid":"a2","message":{"role":"assistant","effort":"low","content":[{"type":"text","text":"changed"}]}}`,
			`{"type":"assistant","uuid":"a3","message":{"role":"assistant","effort":null,"content":[{"type":"text","text":"reset"}]}}`,
		}, "\n")},
		{"codex-exec.jsonl", "codex", strings.Join([]string{
			`{"type":"thread.started","thread_id":"effort-tl1"}`,
			`{"type":"item.completed","item":{"id":"a1","type":"agent_message","text":"initial"}}`,
			`{"type":"item.completed","item":{"id":"a2","type":"agent_message","text":"changed","effort":"low"}}`,
			`{"type":"item.completed","item":{"id":"a3","type":"agent_message","text":"reset","effort":null}}`,
		}, "\n")},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			dir := t.TempDir()
			writeSourceFile(t, filepath.Join(dir, "task", fixture.name), fixture.content)
			adapter := &tl1Adapter{baseAdapter: baseAdapter{config: SourceConfig{Account: "local"}}}
			conversation, ok, err := adapter.transcript(tl1Installation{ID: "test", Transcripts: dir}, "task", map[string]any{"id": "attempt", "executor": fixture.provider, "effort": "high"}, fixture.name)
			if err != nil || !ok {
				t.Fatalf("transcript: %v %v", ok, err)
			}
			efforts := []string{}
			for _, message := range conversation.Messages {
				efforts = append(efforts, message.Effort)
			}
			if !reflect.DeepEqual(efforts, []string{"high", "low", ""}) || conversation.Effort != "" {
				t.Fatalf("initial setting overwrote transcript changes: %v (final %q)", efforts, conversation.Effort)
			}
		})
	}
}

func TestInferenceSpeedIngestionAndReplay(t *testing.T) {
	cases := []struct {
		provider, file, body string
		first                inferenceSpeed
	}{
		{"codex", "rollout-speed.jsonl", strings.Join([]string{
			`{"type":"session_meta","payload":{"id":"speed","model":"gpt-test"}}`,
			`{"type":"turn_context","payload":{"model":"gpt-test","service_tier":"priority"}}`,
			`{"type":"token_usage_record","timestamp":"2026-09-20T12:01:00Z","payload":{"response_id":"fast-request","usage":{"input_tokens":10,"output_tokens":1}}}`,
			`{"type":"turn_context","payload":{"model":"gpt-test"}}`,
			`{"type":"token_usage_record","timestamp":"2026-09-20T12:02:00Z","payload":{"response_id":"standard-request","usage":{"input_tokens":10,"output_tokens":1}}}`,
		}, "\n"), inferenceSpeed{Speed: "fast", ServiceTier: "priority", Source: "service_tier"}},
		// OpenAI's Ultrafast Processing is fast too.
		{"codex", "rollout-ultrafast.jsonl", strings.Join([]string{
			`{"type":"session_meta","payload":{"id":"ultrafast","model":"gpt-test"}}`,
			`{"type":"turn_context","payload":{"model":"gpt-test","service_tier":"ultrafast"}}`,
			`{"type":"token_usage_record","timestamp":"2026-09-20T12:01:00Z","payload":{"response_id":"ultrafast-request","usage":{"input_tokens":10,"output_tokens":1}}}`,
			`{"type":"turn_context","payload":{"model":"gpt-test","service_tier":"flex"}}`,
			`{"type":"token_usage_record","timestamp":"2026-09-20T12:02:00Z","payload":{"response_id":"flex-request","usage":{"input_tokens":10,"output_tokens":1}}}`,
		}, "\n"), inferenceSpeed{Speed: "fast", ServiceTier: "ultrafast", Source: "service_tier"}},
		{"canonical", "speed.json", `{"id":"speed","conversations":[{"id":"speed","provider":"codex","service_tier":"priority","messages":[{"id":"fast","role":"assistant","text":"first"},{"id":"standard","role":"assistant","service_tier":null,"text":"second"}]}]}`, inferenceSpeed{Speed: "fast", ServiceTier: "priority", Source: "service_tier"}},
		{"canonical", "explicit-speed.json", `{"id":"speed","conversations":[{"id":"speed","provider":"claude","messages":[{"id":"fast","role":"assistant","speed":"fast","text":"first","raw_text":"{\"message\":{\"id\":\"request-fast\",\"usage\":{\"service_tier\":\"standard\",\"input_tokens\":10,\"output_tokens\":1}}}"},{"id":"standard","role":"assistant","speed":"standard","text":"second","raw_text":"{\"message\":{\"id\":\"request-standard\",\"usage\":{\"service_tier\":\"priority\",\"input_tokens\":10,\"output_tokens\":1}}}"}]}]}`, inferenceSpeed{Speed: "fast", ServiceTier: "standard", Source: "speed"}},
		{"chatgpt", "conversations.json", `[{"id":"speed","mapping":{"a":{"message":{"id":"fast","author":{"role":"assistant"},"content":{"parts":["first"]},"create_time":1,"metadata":{"model_slug":"gpt-test","service_tier":"priority"}}},"b":{"message":{"id":"standard","author":{"role":"assistant"},"content":{"parts":["second"]},"create_time":2,"metadata":{"model_slug":"gpt-test"}}}}}]`, inferenceSpeed{Speed: "fast", ServiceTier: "priority", Source: "service_tier"}},
	}
	for _, sample := range cases {
		t.Run(sample.provider+"/"+sample.file, func(t *testing.T) {
			c, _ := testCatalog(t)
			root := t.TempDir()
			writeSourceFile(t, filepath.Join(root, sample.file), sample.body)
			adapter, err := MakeAdapter(SourceConfig{Name: "speed", Kind: sample.provider, Path: root, Account: "local", Enabled: true})
			if err != nil {
				t.Fatal(err)
			}
			if result := c.Ingest(adapter, nil); result.Error != nil {
				t.Fatal(result.Error)
			}
			rows, err := queryMaps(c.DB, "SELECT * FROM messages ORDER BY created_at,source_order")
			if err != nil || len(rows) != 2 {
				t.Fatalf("messages %+v: %v", rows, err)
			}
			if rows[0]["speed"] != sample.first.Speed || rows[0]["service_tier"] != sample.first.ServiceTier || rows[0]["speed_source"] != sample.first.Source || rows[1]["speed"] != "standard" {
				t.Fatalf("normalized messages %+v", rows)
			}
			retained := storedMessageRecords(rows)
			var replay []MessageRecord
			if err := json.Unmarshal([]byte(jsonText(retained)), &replay); err != nil {
				t.Fatal(err)
			}
			for i := range replay {
				if normalized := normalizeMessageInference(replay[i]); normalized.Speed != retained[i].Speed || normalized.ServiceTier != retained[i].ServiceTier || normalized.SpeedSource != retained[i].SpeedSource {
					t.Fatalf("replay changed inference settings: %+v", normalized)
				}
			}
			if sample.provider == "codex" || sample.file == "explicit-speed.json" {
				requests, err := queryMaps(c.DB, "SELECT speed,service_tier,speed_source FROM model_requests ORDER BY sequence")
				if err != nil || len(requests) != 2 {
					t.Fatalf("request configuration %+v: %v", requests, err)
				}
				for i, request := range requests {
					for _, field := range []string{"speed", "service_tier", "speed_source"} {
						if request[field] != rows[i][field] {
							t.Fatalf("request %d lost %s: %+v; message %+v", i, field, request, rows[i])
						}
					}
				}
				if _, err := c.DB.Exec("UPDATE model_requests SET speed='standard',service_tier=NULL,speed_source='assumed_standard';UPDATE tool_ledger_state SET version='before-speed-policy'"); err != nil {
					t.Fatal(err)
				}
				if _, err := c.BackfillToolLedger(t.Context(), nil); err != nil {
					t.Fatal(err)
				}
				rebuilt, err := queryMaps(c.DB, "SELECT speed,service_tier,speed_source FROM model_requests ORDER BY sequence")
				if err != nil || !reflect.DeepEqual(requests, rebuilt) {
					t.Fatalf("request replay lost configuration: %+v %v", rebuilt, err)
				}
				if sample.first.ServiceTier != "ultrafast" {
					return
				}
				// A catalog indexed when ultrafast counted as standard rebuilds
				// just the conversations that recorded it, once.
				if _, err := c.DB.Exec("UPDATE messages SET speed='standard' WHERE service_tier='ultrafast';UPDATE model_requests SET speed='standard' WHERE service_tier='ultrafast';UPDATE meta SET value='priority,fast' WHERE key='fast_service_tiers'"); err != nil {
					t.Fatal(err)
				}
				for want := range 2 {
					if err := c.ensureFastTiers(); err != nil {
						t.Fatal(err)
					}
					if built, err := c.BackfillToolLedger(t.Context(), nil); err != nil || built != 1-want {
						t.Fatalf("rebuilt %d conversations, want %d: %v", built, 1-want, err)
					}
				}
				for _, table := range []string{"messages", "model_requests"} {
					if speeds, err := queryMaps(c.DB, "SELECT DISTINCT speed FROM "+table+" WHERE service_tier='ultrafast'"); err != nil || len(speeds) != 1 || speeds[0]["speed"] != "fast" {
						t.Fatalf("%s kept the old policy: %+v %v", table, speeds, err)
					}
				}
			}
		})
	}
}
