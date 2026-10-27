package archive

import (
	"context"
	"database/sql"
	"testing"
	"time"

	qt "github.com/Pythia-Software/query-table/backends/go"
)

func TestTaskOriginsInheritAcrossProvidersAndMirrors(t *testing.T) {
	c, _ := testCatalog(t)
	execTx(t, c.DB,
		`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('root','codex','local','root','root','2026-10-26'),('wrapper','conductor','local','wrapper','wrapper','2026-10-26'),('child','claude','local','child','child','2026-10-26')`,
		`INSERT INTO conversations(id,workspace_id,provider,account,native_id,harness,agent_depth) VALUES('r','root','codex','local','r','codex/codex_exec',0),('w','wrapper','codex','local','w','conductor',0),('c','child','claude','local','c','claude-code/cli',0)`,
		`INSERT INTO conversation_identity_links(left_id,right_id,relationship,confidence,evidence_json) VALUES('r','w','native-alias',1,'{}')`,
		`INSERT INTO conversation_launches(child_id,parent_id,tool_call_id,program) VALUES('c','r','call','claude')`,
	)
	origins, err := publishedOrigins(c, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"r", "w", "c"} {
		if origins[id]["channel"] != "conductor" {
			t.Fatalf("%s: %#v", id, origins[id])
		}
	}
	if origins["c"]["channel_status"] != "inherited" {
		t.Fatal(origins["c"])
	}
}

func TestTaskOriginMachinesAndLateParents(t *testing.T) {
	c, _ := testCatalog(t)
	add := func(cv ConversationRecord) {
		t.Helper()
		err := c.writeTransaction(t.Context(), "test origin", func(tx *sql.Tx) error {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('work','canonical','local','work','work','2026-10-26')`); err != nil {
				return err
			}
			id, err := upsertConversation(tx, "work", "", cv, "capture")
			if err != nil {
				return err
			}
			return recordOriginObservation(tx, id, "capture", "canonical", cv)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	child := ConversationRecord{NativeID: "child", Provider: "claude", Account: "local", ParentProvider: "codex", ParentNativeID: "parent", AgentDepth: 1, Harness: "codex/codex_exec", Initiation: &InitiationRecord{ExecutionMachine: &MachineRecord{"remote", "Remote", "cloud"}}}
	add(child)
	childID := stableID("conversation", "claude", "local", "child")
	origins, err := publishedOrigins(c, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if origins[childID]["channel"] != nil || origins[childID]["machine_id"] != nil || origins[childID]["execution_machine_id"] != "remote" {
		t.Fatal(origins[childID])
	}
	add(ConversationRecord{NativeID: "parent", Provider: "codex", Account: "local", Initiation: &InitiationRecord{Channel: "desktop", Machine: &MachineRecord{"laptop", "Laptop", "device"}, ExecutionMachine: &MachineRecord{"laptop", "Laptop", "device"}}})
	origins, err = publishedOrigins(c, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	row := origins[childID]
	if row["channel"] != "desktop" || row["machine_id"] != "laptop" || row["execution_machine_id"] != "remote" {
		t.Fatal(row)
	}
	for _, includeMessages := range []bool{true, false} {
		detail, err := c.workDetail("work", includeMessages)
		if err != nil {
			t.Fatal(err)
		}
		var child map[string]any
		for _, cv := range detail["conversations"].([]map[string]any) {
			if cv["id"] == childID {
				child = cv
			}
		}
		if child["channel"] != "desktop" || child["machine_id"] != "laptop" || child["execution_machine_id"] != "remote" || child["origin_evidence"] == nil {
			t.Fatalf("work detail (messages=%v) lost child attribution: %#v", includeMessages, child)
		}
	}
	if _, err := c.DB.Exec("UPDATE conversations SET origin_host_id='another-copy' WHERE id=?", childID); err != nil {
		t.Fatal(err)
	}
	origins, err = publishedOrigins(c, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if origins[childID]["machine_id"] != "laptop" {
		t.Fatal(origins[childID])
	}
	add(ConversationRecord{NativeID: "alias-child", Provider: "claude", Account: "local", ParentProvider: "codex", ParentNativeID: "parent-alias", AgentDepth: 1})
	aliasID := stableID("conversation", "claude", "local", "alias-child")
	origins, err = publishedOrigins(c, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if origins[aliasID]["channel_status"] != "missing_parent" {
		t.Fatal(origins[aliasID])
	}
	if _, err := c.DB.Exec(`UPDATE conversations SET aliases_json='["parent-alias"]' WHERE native_id='parent'`); err != nil {
		t.Fatal(err)
	}
	origins, err = publishedOrigins(c, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if origins[aliasID]["channel"] != "desktop" || origins[aliasID]["machine_id"] != "laptop" {
		t.Fatal(origins[aliasID])
	}
}

func TestTaskOriginUnknownConflictAndCycle(t *testing.T) {
	c, _ := testCatalog(t)
	execTx(t, c.DB,
		`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('work','canonical','local','work','work','2026-10-26')`,
		`INSERT INTO conversations(id,workspace_id,provider,account,native_id,harness,parent_id,agent_depth) VALUES('unknown','work','codex','local','u','codex',NULL,0),('child','work','codex','local','c','codex/codex_exec','unknown',1),('cycle1','work','codex','local','a','codex/codex_exec','cycle2',1),('cycle2','work','codex','local','b','codex/codex_exec','cycle1',1),('conflict','work','codex','local','x','codex',NULL,0)`,
		`INSERT INTO origin_observations(conversation_id,evidence_key,channel,machine_id,execution_machine_id,priority,evidence,parent_native_id) VALUES('conflict','a','desktop',NULL,NULL,30,'producer a',NULL),('conflict','b','cli',NULL,NULL,30,'producer b',NULL)`,
		`INSERT INTO conversations(id,workspace_id,provider,account,native_id,harness) VALUES('parent-conflict','work','codex','local','pc','codex/codex_exec'),('launch-conflict','work','codex','local','lc','codex/codex_exec'),('launch-alias','work','codex','local','la','codex/codex_exec')`,
		`INSERT INTO conversation_identity_links(left_id,right_id,relationship,confidence,evidence_json) VALUES('launch-conflict','launch-alias','native-alias',1,'{}')`,
		`INSERT INTO origin_observations(conversation_id,evidence_key,priority,evidence,parent_native_id) VALUES('parent-conflict','a',30,'producer a','u'),('parent-conflict','b',30,'producer b','x')`,
		`INSERT INTO conversation_launches(child_id,parent_id,tool_call_id,program) VALUES('launch-conflict','unknown','call-a','codex'),('launch-alias','conflict','call-b','codex')`,
	)
	origins, err := publishedOrigins(c, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if origins["unknown"]["channel"] != nil || origins["child"]["channel"] != nil {
		t.Fatal(origins)
	}
	if origins["cycle1"]["channel_status"] != "cycle" || origins["conflict"]["channel_status"] != "conflict" {
		t.Fatal(origins)
	}
	for _, id := range []string{"parent-conflict", "launch-conflict"} {
		if origins[id]["channel_status"] != "conflict" || origins[id]["machine_status"] != "conflict" {
			t.Fatalf("%s: %#v", id, origins[id])
		}
	}
}

func TestTaskOriginUsageKeepsOwnProvider(t *testing.T) {
	c, config := testCatalog(t)
	execTx(t, c.DB,
		`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('work','codex','local','work','work','2026-10-26')`,
		`INSERT INTO conversations(id,workspace_id,provider,account,native_id,harness) VALUES('root','work','codex','local','r','codex/codex_desktop'),('child','work','claude','local','c','claude-code/cli')`,
		`UPDATE conversations SET parent_id='root',agent_depth=1 WHERE id='child'`,
		`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,depth,provider,model,usage_status) VALUES('session','work','child','main','root',1,'claude','claude-sonnet-4-6','reported')`,
		`INSERT INTO agent_session_usage(agent_session_id,usage_hour,model,attribution,total_tokens) VALUES('session','2026-10-26T12:00:00Z','claude-sonnet-4-6','request',100)`,
	)
	publishOrigins(t, c)
	rows, err := c.usageRows(time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0]["provider"] != "claude" || rows[0]["channel"] != "desktop" || integer(rows[0]["total_tokens"]) != 100 {
		t.Fatal(rows)
	}
	if err := c.writeTransaction(t.Context(), "origin fixture", func(tx *sql.Tx) error {
		if err := recordOriginObservation(tx, "root", "fixture", "canonical", ConversationRecord{Initiation: &InitiationRecord{Channel: "desktop", Machine: &MachineRecord{"laptop", "Laptop", "device"}, ExecutionMachine: &MachineRecord{"laptop", "Laptop", "device"}}}); err != nil {
			return err
		}
		return recordOriginObservation(tx, "child", "fixture", "canonical", ConversationRecord{Initiation: &InitiationRecord{ExecutionMachine: &MachineRecord{"laptop", "Laptop", "device"}}})
	}); err != nil {
		t.Fatal(err)
	}
	execTx(t, c.DB,
		`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at,selected) VALUES('human','root','human','user','message','Fix the parser','human','2026-10-26T12:00:00Z',1)`,
		`INSERT INTO tool_calls(id,workspace_id,conversation_id,agent_session_id,sequence,provider,kind,tool_name,tool_category,status,started_at) VALUES('call','work','child','session',0,'claude','tool_call','Read','read','ok','2026-10-26T12:00:00Z')`,
		`INSERT INTO skill_usages(id,workspace_id,conversation_id,agent_session_id,evidence_message_id,skill_name,evidence_type,status,created_at) VALUES('skill','work','child','session','human','review','invocation','observed','2026-10-26T12:00:00Z')`)
	if err := c.RebuildAuthorship(t.Context(), "origin fixture"); err != nil {
		t.Fatal(err)
	}
	publishProjections(t, c)
	s := testServer(t, config, c)
	for _, dataset := range []string{"usage", "tool_calls", "skill_usages", "conversation_activity"} {
		for field, value := range map[string]string{"channels": "desktop", "machines": "laptop", "execution_machines": "laptop"} {
			q := groupRequest(field, qt.SummaryColumn{ID: "facts", Op: "count"})
			q.Source.Where = []qt.WhereTerm{{Field: field, Op: "=", Value: value}}
			schema, err := sqliteQuerySchema(dataset)
			if err != nil {
				t.Fatal(err)
			}
			got, _, err := s.executeQueryV3(t.Context(), dataset, nil, schema, q)
			if err != nil || len(got.Rows) != 1 || got.Rows[0][field] != value || integer(got.Rows[0]["facts"]) < 1 {
				t.Fatalf("%s %s membership: %+v, %v", dataset, field, got, err)
			}
		}
	}
}

func TestTaskOriginBookkeepingDoesNotBlockReads(t *testing.T) {
	c, config := testCatalog(t)
	seedToolCalls(t, c, 16)
	if err := c.ensureToolRollup(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DB.Exec(`UPDATE workspaces SET indexed_at='later'`); err != nil {
		t.Fatal(err)
	}
	release, err := c.acquireWriteGate(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	held := seedTx(t, c.DB)
	if _, err := held.Exec(`DELETE FROM meta WHERE 0`); err != nil {
		t.Fatal(err)
	}
	defer held.Rollback()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	s := testServer(t, config, c)
	q := groupRequest("provider", qt.SummaryColumn{ID: "calls", Op: "count"})
	if _, _, err := s.executeQueryV3(ctx, "tool_calls", nil, PharosToolCallsSchema(), q); err != nil {
		t.Fatalf("bookkeeping-only sync forced an attribution writer: %v", err)
	}
}

func TestTaskOriginRollupsAndReadonlyQueries(t *testing.T) {
	c, _ := testCatalog(t)
	seedToolCalls(t, c, 64)
	execTx(t, c.DB, `UPDATE conversations SET harness='codex/codex_desktop' WHERE id='conv-0'`,
		`UPDATE conversations SET parent_id='conv-0',agent_depth=1,harness='claude-code/cli' WHERE id='conv-1'`,
		`UPDATE conversations SET harness='claude-code/cli' WHERE id='conv-2'`,
		`UPDATE agent_sessions SET child_conversation_id='conv-1' WHERE id='session-0'`)
	var linkedCalls int
	if err := c.DB.QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE workspace_id='ws-0' AND agent_session_id='session-0'`).Scan(&linkedCalls); err != nil || linkedCalls == 0 {
		t.Fatalf("fixture needs parent-workspace calls linked to the child: %d, %v", linkedCalls, err)
	}
	declare := func(channel, executionMachine string) {
		t.Helper()
		if err := c.writeTransaction(t.Context(), "origin declaration", func(tx *sql.Tx) error {
			if err := recordOriginObservation(tx, "conv-0", "fixture", "canonical", ConversationRecord{Origin: "root", Initiation: &InitiationRecord{Channel: channel, Machine: &MachineRecord{"laptop", "Laptop", "device"}, ExecutionMachine: &MachineRecord{"laptop", "Laptop", "device"}}}); err != nil {
				return err
			}
			return recordOriginObservation(tx, "conv-1", "fixture", "canonical", ConversationRecord{Origin: "child", Harness: "claude-code/cli", Initiation: &InitiationRecord{ExecutionMachine: &MachineRecord{executionMachine, "Remote worker", "cloud"}}})
		}); err != nil {
			t.Fatal(err)
		}
	}
	check := func() {
		t.Helper()
		if err := c.ensureToolRollup(t.Context()); err != nil {
			t.Fatal(err)
		}
		raw, err := queryMaps(c.DB, `SELECT COALESCE(o.channel,'') channel,COALESCE(o.machine_id,'') machine_id,COALESCE(o.execution_machine_id,'') execution_machine_id,COUNT(*) calls,COALESCE(SUM(t.duration_ms),0) duration FROM tool_calls t LEFT JOIN agent_sessions a ON a.id=t.agent_session_id LEFT JOIN conversation_origins o ON o.conversation_id=COALESCE(a.child_conversation_id,t.conversation_id) GROUP BY 1,2,3 ORDER BY 1,2,3`)
		if err != nil {
			t.Fatal(err)
		}
		summary, err := queryMaps(c.DB, `SELECT COALESCE(channel,'') channel,COALESCE(machine_id,'') machine_id,COALESCE(execution_machine_id,'') execution_machine_id,SUM(call_count) calls,SUM(total_duration_ms) duration FROM tool_usage_daily GROUP BY 1,2,3 ORDER BY 1,2,3`)
		if err != nil {
			t.Fatal(err)
		}
		if !same(raw, summary) {
			t.Fatalf("origin summary differs from raw calls:\n%#v\n%#v", raw, summary)
		}
		cube, err := queryMaps(c.DB, `SELECT COALESCE(channel,'') channel,COALESCE(machine_id,'') machine_id,COALESCE(execution_machine_id,'') execution_machine_id,SUM(call_count) calls,COALESCE(SUM(duration_ms_sum),0) duration FROM tool_call_cube GROUP BY 1,2,3 ORDER BY 1,2,3`)
		if err != nil {
			t.Fatal(err)
		}
		if !same(raw, cube) {
			t.Fatalf("origin cube differs from raw calls:\n%#v\n%#v", raw, cube)
		}
	}
	declare("desktop", "remote")
	check()
	origins, err := publishedOrigins(c, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if origins["conv-1"]["channel"] != "desktop" || origins["conv-1"]["machine_id"] != "laptop" || origins["conv-1"]["execution_machine_id"] != "remote" {
		t.Fatal(origins["conv-1"])
	}
	for _, child := range []any{nil, "conv-1"} {
		if _, err := c.DB.Exec(`UPDATE agent_sessions SET child_conversation_id=? WHERE id='session-0'`, child); err != nil {
			t.Fatal(err)
		}
		check()
	}
	// Only the child's placement changes; calls stored under its parent must
	// follow that correction in both the cube and the daily summary.
	declare("desktop", "other-worker")
	check()
	// A correction dirties the descendant workspace's cube partition too.
	declare("cli", "other-worker")
	check()
	if _, err := c.DB.Exec("UPDATE origin_observations SET channel='desktop' WHERE conversation_id='conv-0'"); err != nil {
		t.Fatal(err)
	}
	light, err := OpenCatalogForQuery(c.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer light.Close()
	held := seedTx(t, c.DB)
	if _, err := held.Exec("DELETE FROM meta WHERE 0"); err != nil {
		t.Fatal(err)
	}
	value, err := light.queryStatistics("query_statistics", map[string]any{"entity": "conversation", "channel": "desktop", "result_mode": "metadata", "select": []any{"conversation_id", "channel", "machine_id", "execution_machine_id"}, "max_output_tokens": 4000})
	if err != nil {
		t.Fatal(err)
	}
	items := value.(map[string]any)["items"].([]map[string]any)
	if len(items) != 2 {
		t.Fatalf("origin filter lost a child on the read-only path: %#v", value)
	}
	for _, item := range items {
		if item["channel"] != "desktop" || item["machine_id"] != "laptop" {
			t.Fatal(item)
		}
	}
	if err := held.Rollback(); err != nil {
		t.Fatal(err)
	}
}

func TestTaskOriginMachineEvidenceAndWebFallback(t *testing.T) {
	c, _ := testCatalog(t)
	execTx(t, c.DB, `INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('local','codex','local','local','Local','x'),('export','chatgpt','local','export','Export','x')`,
		`INSERT INTO conversations(id,workspace_id,provider,account,native_id,harness) VALUES('local','local','codex','local','local','codex/codex_exec'),('export','export','chatgpt','local','export','chatgpt-export'),('web','export','chatgpt','local','web','chatgpt-export')`,
		`INSERT INTO machines VALUES('device','Renamable Mac','device')`,
		`INSERT INTO host_machines VALUES('host-a','device'),('host-b','device')`,
		`INSERT INTO conversation_sightings(conversation_id,host_id,origin,source_name,first_seen_at,last_seen_at) VALUES('local','host-a','a','codex','x','x'),('local','host-b','b','codex','x','x'),('export','host-a','download','chatgpt','x','x')`,
		`INSERT INTO origin_observations(conversation_id,evidence_key,channel,priority,evidence) VALUES('web','producer','web',30,'explicit web origin')`)
	origins, err := publishedOrigins(c, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if origins["local"]["machine_id"] != "device" || origins["local"]["machine_status"] != "inferred" {
		t.Fatal(origins["local"])
	}
	if origins["export"]["channel"] != nil || origins["export"]["machine_id"] != nil {
		t.Fatal(origins["export"])
	}
	if origins["web"]["machine_id"] != "environment:web" || origins["web"]["machine_kind"] != "web" {
		t.Fatal(origins["web"])
	}
	if _, err := c.DB.Exec("UPDATE machines SET label='New name' WHERE id='device'"); err != nil {
		t.Fatal(err)
	}
	origins, err = publishedOrigins(c, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if origins["local"]["machine_id"] != "device" || origins["local"]["machine"] != "New name" {
		t.Fatal(origins["local"])
	}
	if _, err := c.DB.Exec(`INSERT INTO conversation_sightings(conversation_id,host_id,origin,source_name,first_seen_at,last_seen_at) VALUES('local','unmapped-host','copy','codex','x','x')`); err != nil {
		t.Fatal(err)
	}
	origins, err = publishedOrigins(c, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if origins["local"]["machine_id"] != nil {
		t.Fatalf("an unmapped copy must invalidate inferred machine placement: %#v", origins["local"])
	}
}

func TestTaskOriginKeepsInitialMarkerAfterResume(t *testing.T) {
	c, _ := testCatalog(t)
	if err := c.writeTransaction(t.Context(), "resumed origin", func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('work','codex','local','work','work','x')`); err != nil {
			return err
		}
		cv := ConversationRecord{NativeID: "session", Provider: "codex", Account: "local", Harness: "codex/codex_desktop", NativeContext: map[string]any{"originator": "codex_exec", "source": "exec"}}
		id, err := upsertConversation(tx, "work", "", cv, "host")
		if err != nil {
			return err
		}
		return recordOriginObservation(tx, id, "host", "codex", cv)
	}); err != nil {
		t.Fatal(err)
	}
	origins, err := publishedOrigins(c, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range origins {
		if row["channel"] != "cli" {
			t.Fatal(row)
		}
	}
	for _, marker := range []string{"vscode", "exec"} {
		if err := c.writeTransaction(t.Context(), "SDK origin", func(tx *sql.Tx) error {
			cv := ConversationRecord{NativeID: marker, Provider: "codex", Account: "local", Harness: "codex/codex_desktop", NativeContext: map[string]any{"originator": "codex_sdk_ts", "source": marker}}
			id, err := upsertConversation(tx, "work", "", cv, "host")
			if err != nil {
				return err
			}
			return recordOriginObservation(tx, id, "host", "codex", cv)
		}); err != nil {
			t.Fatal(err)
		}
		origins, err = publishedOrigins(c, t.Context())
		if err != nil {
			t.Fatal(err)
		}
		id := stableID("conversation", "codex", "local", marker)
		if origins[id]["channel"] != nil {
			t.Fatalf("SDK runtime %s established an initiating channel: %#v", marker, origins[id])
		}
	}
}

func TestTaskOriginStatisticsThreeDimensions(t *testing.T) {
	c := statisticsFixture(t)
	execTx(t, c.DB, `UPDATE conversations SET harness='codex/codex_desktop' WHERE id='c1'`, `UPDATE conversations SET harness='codex/codex_exec' WHERE id='c2'`)
	publishOrigins(t, c)
	value, err := c.queryStatistics("query_statistics", map[string]any{"entity": "conversation", "result_type": "aggregate", "group_by": []any{"provider", "channel", "machine_id"}, "aggregations": []any{map[string]any{"id": "tokens", "op": "sum", "metric": "total_tokens"}}, "max_output_tokens": 4000})
	if err != nil {
		t.Fatal(err)
	}
	groups := value.(map[string]any)["groups"].([]map[string]any)
	var total int64
	for _, group := range groups {
		total += integer(group["values"].(map[string]any)["tokens"])
	}
	baseline, err := c.queryStatistics("query_statistics", map[string]any{"entity": "conversation", "result_type": "aggregate", "aggregations": []any{map[string]any{"id": "tokens", "op": "sum", "metric": "total_tokens"}}, "max_output_tokens": 4000})
	if err != nil {
		t.Fatal(err)
	}
	expected := integer(baseline.(map[string]any)["groups"].([]map[string]any)[0]["values"].(map[string]any)["tokens"])
	if len(groups) != 2 || total != expected {
		t.Fatalf("origin grouping changed totals: %#v", value)
	}
}

// publishedOrigins publishes origins, as the writer that changed their inputs
// does, and reads them.
func publishedOrigins(c *Catalog, ctx context.Context) (map[string]map[string]any, error) {
	if err := c.ensureOrigins(ctx); err != nil {
		return nil, err
	}
	return c.conversationOrigins(ctx)
}

// An ingest pauses the rest of the Library maintenance loop, which still
// publishes the origins of the conversations it adds, spaced while it runs.
func TestTaskOriginsPublishDuringAnIngest(t *testing.T) {
	override(t, &originsRunInterval, 50*time.Millisecond)
	c, _ := testCatalog(t)
	// What beginIngest does for a sync or a capture index run.
	c.projectionBatches.Add(1)
	c.ingestRuns.Add(1)
	defer c.ingestRuns.Add(-1)
	defer c.projectionBatches.Add(-1)
	ctx, cancel := context.WithCancel(t.Context())
	stopped := make(chan struct{})
	go func() { c.maintainLibrary(ctx); close(stopped) }()
	defer func() { cancel(); <-stopped }()
	for _, id := range []string{"first", "second"} {
		execTx(t, c.DB,
			`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('`+id+`','codex','local','`+id+`','`+id+`','2026-10-26')`,
			`INSERT INTO conversations(id,workspace_id,provider,account,native_id,harness) VALUES('`+id+`','`+id+`','codex','local','`+id+`','codex/codex_desktop')`)
		if !waitFor(10*time.Second, func() {
			for countRows(t, c, "SELECT COUNT(*) FROM conversation_origins WHERE conversation_id='"+id+"' AND channel='desktop'") == 0 {
				time.Sleep(10 * time.Millisecond)
			}
		}) {
			t.Fatalf("%s: origins not published during the ingest", id)
		}
	}
}
