package archive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestRepairAgentSessionModelsPreservesAccounting(t *testing.T) {
	c, config := testCatalog(t)
	seedDelegation(t, c)
	execSQL(t, c.DB, `DELETE FROM meta WHERE key='agent_session_model_version'; UPDATE conversations SET model='opus'; UPDATE agent_sessions SET model='opus';
		INSERT INTO agent_session_usage(agent_session_id,usage_hour,model,attribution,total_tokens) VALUES
		('child-session','2026-03-01','haiku','request',200),
		('nested-session','2026-03-01','haiku','request',100),
		('nested-session','2026-03-02','sonnet','request',200),
		('embedded','2026-03-01','','session-start',50);
		UPDATE model_requests SET model='opus';`)
	// A retained request beats stale configured and derived model fields.
	execSQL(t, c.DB, `UPDATE messages SET raw_text='{"type":"assistant","message":{"model":"claude-haiku-4-5-20251001"}}',role='assistant',kind='metadata' WHERE id='nested-result';
		INSERT INTO agent_session_messages(agent_session_id,message_id,source_order) VALUES('child-session','nested-result',0);
		INSERT INTO messages(id,conversation_id,native_id,role,kind,text,raw_text,content_hash)
		VALUES('json-answer','child','json-answer','assistant','message','{"object":"response","model":"gpt-4.1","output":[]}','', 'json-answer');
		INSERT INTO agent_session_messages(agent_session_id,message_id,source_order) VALUES('child-session','json-answer',1);
		UPDATE model_requests SET final_message_id='json-answer' WHERE agent_session_id='child-session'`)
	before := func() string {
		t.Helper()
		snapshot := map[string]any{}
		for _, table := range []string{"messages", "agent_session_messages", "agent_session_usage", "agent_session_token_evidence", "model_requests", "tool_calls", "metrics", "usage_attribution_state"} {
			rows, err := queryMaps(c.DB, "SELECT * FROM "+table+" ORDER BY rowid")
			if err != nil {
				t.Fatal(err)
			}
			snapshot[table] = rows
		}
		rows, err := queryMaps(c.DB, `SELECT id,workspace_id,conversation_id,native_id,parent_id,child_conversation_id,kind,depth,started_at,ended_at,total_tokens,input_tokens,output_tokens,usage_status FROM agent_sessions ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		snapshot["sessions"] = rows
		data, _ := json.Marshal(snapshot)
		return string(data)
	}
	wantAccounting := before()
	var generation int64
	if err := c.DB.QueryRow(`SELECT value FROM query_cache_generation WHERE id=1`).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	if got := upgradePending(t, c)["session-models"]; got != 1 {
		t.Fatalf("pending=%d", got)
	}
	// Warm the cached projection before repair.
	if _, err := c.delegationSnapshot(t.Context(), "delegation-work"); err != nil {
		t.Fatal(err)
	}
	report, err := c.RepairAgentSessionModels(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.Updated != 4 || report.Multiple != 1 || report.PricingFallback != 1 {
		t.Fatalf("report=%+v", report)
	}
	for id, want := range map[string]string{"child-session": "claude-haiku-4-5-20251001", "launch-alias": "claude-haiku-4-5-20251001", "nested-session": multipleSessionModels, "nested-alias": "Unknown model", "embedded": "opus", "root-session": "opus"} {
		var got string
		if err := c.DB.QueryRow(`SELECT model FROM agent_sessions WHERE id=?`, id).Scan(&got); err != nil || got != want {
			t.Errorf("%s model=%q want %q: %v", id, got, want, err)
		}
	}
	if before() != wantAccounting {
		t.Fatal("metadata repair changed accounting, hierarchy, IDs or retained evidence")
	}
	var afterGeneration int64
	if err := c.DB.QueryRow(`SELECT value FROM query_cache_generation WHERE id=1`).Scan(&afterGeneration); err != nil || afterGeneration <= generation {
		t.Fatal("model writes did not invalidate cached views", err)
	}
	if got := upgradePending(t, c)["session-models"]; got != 0 {
		t.Fatalf("pending after repair=%d", got)
	}
	if again, err := c.RepairAgentSessionModels(t.Context(), nil); err != nil || again.Updated != 0 {
		t.Fatalf("repeat=%+v: %v", again, err)
	}
	if before() != wantAccounting {
		t.Fatal("repeat repair changed accounting")
	}
	snapshot, err := c.delegationSnapshot(t.Context(), "delegation-work")
	if err != nil || snapshot.ByID["child-session"].Model != "claude-haiku-4-5-20251001" {
		t.Fatal("delegation did not see repaired metadata", err)
	}
	status, body := delegationGET(t, testServer(t, config, c), "/api/work/delegation-work/delegation?mode=rank&search=claude-haiku-4-5-20251001")
	if status != 200 || len(body["rows"].([]any)) != 1 || body["rows"].([]any)[0].(map[string]any)["model"] != "claude-haiku-4-5-20251001" {
		t.Fatal("delegation filtering/JSON did not use repaired metadata", status, body)
	}
	tx, err := c.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	metas, err := loadDelegationConversationMeta(t.Context(), tx, []string{"child"})
	if err != nil || metas["child"].Model != "claude-haiku-4-5-20251001" {
		t.Fatal("external child metadata reverted to conversation model", metas, err)
	}
}

func sessionModelLinkedFixture() WorkspaceRecord {
	return WorkspaceRecord{SourceID: "linked-model", SourceKind: "claude", Account: "local", Conversations: []ConversationRecord{
		{NativeID: "session", Provider: "claude", Account: "local", Model: "opus", Messages: []MessageRecord{
			{NativeID: "launch", Role: "assistant", Kind: "delegation", CallID: "child-call", Text: `{"tool":"Agent","input":{"model":"sonnet","prompt":"Child assignment"}}`, Selected: true},
			{NativeID: "result", Role: "user", Kind: "delegation_result", CallID: "child-call", Text: `{"details":{"agentId":"x"}}`, Selected: true},
		}},
		{NativeID: "session:subagent:agent-x", ParentNativeID: "session", AgentDepth: 1, Provider: "claude", Account: "local", Model: "haiku", Messages: []MessageRecord{
			{NativeID: "assignment", Role: "agent", Kind: "message", Text: "Child assignment", Selected: true},
			{NativeID: "usage", Role: "system", Kind: "metadata", Model: "haiku", Text: `{"type":"assistant","message":{"model":"haiku","usage":{"input_tokens":10}}}`, Selected: true},
		}},
	}}
}

func TestRepairAgentSessionModelsLinkedIngest(t *testing.T) {
	c, _ := testCatalog(t)
	record := sessionModelLinkedFixture()
	model := func() string {
		t.Helper()
		var model string
		if err := c.DB.QueryRow(`SELECT model FROM agent_sessions WHERE native_id='child-call' AND child_conversation_id IS NOT NULL`).Scan(&model); err != nil {
			t.Fatal(err)
		}
		return model
	}
	ingestRecordTx(t, c, record)
	if got := model(); got != "haiku" {
		t.Fatalf("initial linked launch model=%q", got)
	}
	before, err := queryMaps(c.DB, `SELECT * FROM agent_session_usage ORDER BY agent_session_id,usage_hour,model`)
	if err != nil {
		t.Fatal(err)
	}
	// A catalog repaired by an older build must stay correct on the next
	// parent edit, even though its one-time upgrade is already complete.
	execSQL(t, c.DB, `UPDATE agent_sessions SET model='sonnet' WHERE native_id='child-call'`)
	if _, err := c.RepairAgentSessionModels(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		record.Conversations[0].Messages = append(record.Conversations[0].Messages, MessageRecord{NativeID: fmt.Sprint("later-", i), Role: "assistant", Kind: "message", Text: `{"object":"response","model":"gpt-4.1"}`, Selected: true})
		ingestRecordTx(t, c, record)
		if got := model(); got != "haiku" {
			t.Fatalf("reingest linked launch model=%q", got)
		}
		if pending := upgradePending(t, c)["session-models"]; pending != 0 {
			t.Fatalf("normal ingest scheduled another repair: %d", pending)
		}
	}
	after, err := queryMaps(c.DB, `SELECT * FROM agent_session_usage ORDER BY agent_session_id,usage_hour,model`)
	if err != nil || jsonText(before) != jsonText(after) {
		t.Fatalf("reingest changed accounted usage: %v", err)
	}
}

func TestRepairAgentSessionModelsResumes(t *testing.T) {
	c, _ := testCatalog(t)
	seedDelegation(t, c)
	execSQL(t, c.DB, `DELETE FROM meta WHERE key='agent_session_model_version'; UPDATE agent_sessions SET model='opus';
		INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('later','claude','local','later','Later','2026-01-01');
		INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('later','later','claude','local','later');
		INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,provider,model) VALUES('later','later','later','child','subagent','claude','opus')`)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if _, err := c.RepairAgentSessionModels(ctx, func(done, total int) {
		if done == 1 {
			cancel()
		}
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("interruption=%v", err)
	}
	if version, err := c.metaValue(t.Context(), "agent_session_model_version"); err != nil || version != "" {
		t.Fatalf("interrupted pass marked current: %q %v", version, err)
	}
	if report, err := c.RepairAgentSessionModels(t.Context(), nil); err != nil || report.Updated != 1 {
		t.Fatalf("resume=%+v: %v", report, err)
	}
	if report, err := c.RepairAgentSessionModels(t.Context(), nil); err != nil || report.Updated != 0 {
		t.Fatalf("repeat=%+v: %v", report, err)
	}
}

func TestRepairSessionBoundsPreservesAccounting(t *testing.T) {
	c, _ := testCatalog(t)
	override(t, &sessionBoundsRepairBatchSize, 2)
	start, end := "2026-09-20T10:00:00.000Z", "2026-09-20T10:20:00.000Z"
	cases := []struct {
		name, start, end, completed, coverage string
		files                                 []string
		timed, envelope, eligible             bool
	}{
		{name: "complete", start: start, end: end, completed: end, eligible: true},
		{name: "ongoing historical zero", start: start, end: start, eligible: true},
		{name: "ongoing", start: start, eligible: true},
		{name: "zero completed", start: start, end: start, completed: start, eligible: true},
		{name: "mismatch", start: start, end: start, completed: end},
		{name: "reversed", start: end, end: start, completed: start},
		{name: "invalid", start: "unknown", end: end, completed: end},
		{name: "invalid end", start: start, end: "unknown", completed: "unknown"},
		{name: "multiple", start: start, end: end, completed: end, files: []string{"1-codex.jsonl", "1-claude.jsonl"}},
		{name: "partial", start: start, end: end, completed: end, coverage: "partial"},
		{name: "timed", start: start, end: end, completed: end, timed: true},
		{name: "explicit envelope", start: start, end: end, envelope: true, eligible: true},
	}
	record := WorkspaceRecord{SourceID: "timing", SourceKind: "tl1", Account: "local"}
	want := 0
	for i, tc := range cases {
		m := MessageRecord{NativeID: "request", Kind: "metadata", Model: "gpt-5", RawText: `{"type":"turn.completed","usage":{"input_tokens":100,"output_tokens":20}}`}
		if tc.timed {
			m.CreatedAt = start
		}
		conversation := ConversationRecord{NativeID: tl1ConversationNativeID("install", fmt.Sprint(i), "1-codex.jsonl"), Provider: "codex", Account: "local", Model: "gpt-5", StartedAt: tc.start, EndedAt: tc.end, Coverage: tc.coverage, Messages: []MessageRecord{m, {NativeID: "launch", CallID: "child", Kind: "delegation"}}}
		if tc.envelope {
			conversation.SessionBounds = attemptSessionBounds(start, end)
		}
		record.Conversations = append(record.Conversations, conversation)
		if tc.eligible {
			want++
		}
	}
	ingestRecordTx(t, c, record)
	tx, err := c.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for i, tc := range cases {
		if tc.envelope {
			continue
		} // source-independent retained proof
		files := tc.files
		if files == nil {
			files = []string{"1-codex.jsonl"}
		}
		if _, err := tx.Exec(`INSERT INTO tl1_attempts(installation_id,attempt_id,task_id,started_at,completed_at,transcripts_json,conversation_native_id) VALUES('install',?,'task',?,?,?,?)`, fmt.Sprint(i), tc.start, nilIfEmpty(tc.completed), jsonText(files), record.Conversations[i].NativeID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(`UPDATE agent_sessions SET started_at=NULL,ended_at=NULL,time_bounds_source='unavailable' WHERE native_id='main'`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	facts := func() string {
		t.Helper()
		result := []any{}
		for _, query := range []string{
			`SELECT * FROM agent_sessions ORDER BY id`, `SELECT * FROM agent_session_usage ORDER BY agent_session_id,usage_hour,model,effort,attribution`,
			`SELECT * FROM model_requests ORDER BY id`, `SELECT * FROM messages ORDER BY id`, `SELECT * FROM usage_attribution_state ORDER BY workspace_id`,
		} {
			rows, err := queryMaps(c.DB, query)
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range rows {
				delete(row, "started_at")
				delete(row, "ended_at")
				delete(row, "time_bounds_source")
			}
			result = append(result, rows)
		}
		return jsonText(result)
	}
	before := facts()
	preview, err := c.RepairSessionBounds(t.Context(), false)
	if err != nil || preview.Eligible != want || preview.Updated != 0 || facts() != before {
		t.Fatalf("preview=%+v, %v", preview, err)
	}
	result, err := c.RepairSessionBounds(t.Context(), true)
	if err != nil || result.Updated != want || result.WithTokens != want || facts() != before {
		t.Fatalf("repair=%+v, %v", result, err)
	}
	for i, tc := range cases {
		var gotStart, gotEnd, source string
		if err := c.DB.QueryRow(`SELECT COALESCE(s.started_at,''),COALESCE(s.ended_at,''),s.time_bounds_source FROM agent_sessions s JOIN conversations c ON c.id=s.conversation_id WHERE c.native_id=? AND s.native_id='main'`, record.Conversations[i].NativeID).Scan(&gotStart, &gotEnd, &source); err != nil {
			t.Fatal(err)
		}
		if tc.eligible {
			wantEnd := tc.completed
			if tc.envelope {
				wantEnd = end
			}
			if gotStart != start || gotEnd != wantEnd || source != "attempt-envelope" {
				t.Errorf("%s: %s / %s (%s)", tc.name, gotStart, gotEnd, source)
			}
		} else if gotStart != "" || gotEnd != "" || source != "unavailable" {
			t.Errorf("%s was filled", tc.name)
		}
	}
	if again, err := c.RepairSessionBounds(t.Context(), true); err != nil || again.Updated != 0 {
		t.Fatalf("second repair=%+v, %v", again, err)
	}
	// A later usage replay must retain the explicit timing provenance too.
	if _, err := c.DB.Exec(`DELETE FROM usage_attribution_state`); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RepairUsageAttribution(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	var envelopes int
	if err := c.DB.QueryRow(`SELECT COUNT(*) FROM agent_sessions WHERE time_bounds_source='attempt-envelope'`).Scan(&envelopes); err != nil || envelopes != want {
		t.Fatalf("usage replay lost envelopes: %d, %v", envelopes, err)
	}
}

func TestClaudeNonWriterCopyPreservesRootUsage(t *testing.T) {
	catalog, _ := testCatalog(t)
	makeRecord := func(origin string, complete bool) WorkspaceRecord {
		messages := []MessageRecord{{NativeID: "r1", Kind: "metadata", Model: "opus", Text: `{"type":"assistant","message":{"id":"r1","usage":{"input_tokens":90,"output_tokens":10}}}`}}
		claim := `{"type":"cost-state","modelUsage":{"opus":{"input_tokens":100,"output_tokens":10}}}`
		if complete {
			messages = append(messages, MessageRecord{NativeID: "r2", Kind: "metadata", Model: "opus", Text: `{"type":"assistant","message":{"id":"r2","usage":{"input_tokens":180,"output_tokens":20}}}`})
			claim = `{"type":"cost-state","modelUsage":{"opus":{"input_tokens":280,"output_tokens":30}}}`
		}
		messages = append(messages, MessageRecord{NativeID: "claim", Kind: "metadata", Text: claim})
		return WorkspaceRecord{SourceID: "shared-claude", SourceKind: "claude", Account: "local", Conversations: []ConversationRecord{{
			NativeID: "shared-claude", Provider: "claude", Model: "opus", Account: "local", Origin: origin,
			StartedAt: "2026-09-20T10:00:00Z", Messages: messages,
		}}}
	}
	ingestAs(t, catalog, "host-a", newCopyFixture(t, makeRecord("/a/session.jsonl", true)))
	var before, beforeLedger int64
	if err := catalog.DB.QueryRow("SELECT total_tokens FROM agent_sessions WHERE native_id='main'").Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := catalog.DB.QueryRow("SELECT COALESCE(SUM(total_tokens),0) FROM agent_session_usage").Scan(&beforeLedger); err != nil {
		t.Fatal(err)
	}
	if before != 310 || beforeLedger != 310 {
		t.Fatalf("initial usage = %d/%d, want 310", before, beforeLedger)
	}
	if result := ingestAs(t, catalog, "host-b", newCopyFixture(t, makeRecord("/b/session.jsonl", false))); result.Workspaces != 1 {
		t.Fatalf("non-writer copy was not processed: %+v", result)
	}
	var after, afterLedger int64
	if err := catalog.DB.QueryRow("SELECT total_tokens FROM agent_sessions WHERE native_id='main'").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if err := catalog.DB.QueryRow("SELECT COALESCE(SUM(total_tokens),0) FROM agent_session_usage").Scan(&afterLedger); err != nil {
		t.Fatal(err)
	}
	if after != before || afterLedger != beforeLedger {
		t.Fatalf("non-writer copy changed usage from %d/%d to %d/%d", before, beforeLedger, after, afterLedger)
	}
}

func TestRepairUsageAttributionBoundsWAL(t *testing.T) {
	catalog, _ := testCatalog(t)
	record := WorkspaceRecord{SourceID: "repair-wal", SourceKind: "claude", Account: "local", Conversations: []ConversationRecord{{
		NativeID: "repair-wal", Provider: "claude", Model: "opus", Account: "local",
		Messages: []MessageRecord{{NativeID: "request", Kind: "metadata", Text: `{"type":"assistant","message":{"id":"request","usage":{"input_tokens":10,"output_tokens":1}}}`}},
	}}}
	tx, err := catalog.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = ingestWorkspace(tx, record, false); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// Catalogs indexed before attribution was versioned have no state rows.
	if _, err := catalog.DB.Exec("DELETE FROM usage_attribution_state"); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(catalog.Path + "-wal")
	if err != nil || before.Size() == 0 {
		t.Fatalf("initial WAL: %v, %v", before, err)
	}
	repaired, err := catalog.repairUsageAttribution(context.Background(), nil, 1)
	if err != nil || repaired != 1 {
		t.Fatalf("repair = %d, %v", repaired, err)
	}
	after, err := os.Stat(catalog.Path + "-wal")
	if err != nil || after.Size() >= before.Size() {
		t.Fatalf("WAL did not shrink after repair: before=%d after=%v err=%v", before.Size(), after, err)
	}
}

func TestSubagentCountRequiresLinkedChildConversation(t *testing.T) {
	catalog, _ := testCatalog(t)
	statements := []string{
		`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES
			('codex-parent','codex','local','cp','Codex parent','2026-09-28'),('codex-child','codex','local','cc','Codex child','2026-09-28'),
			('claude-old','claude','local','co','Claude without delegation result','2026-09-28'),('claude-linked','claude','local','cl','Claude linked','2026-09-28')`,
		`INSERT INTO conversations(id,workspace_id,provider,account,native_id,agent_depth) VALUES
			('cp-root','codex-parent','codex','local','cp-root',0),('cc-child','codex-child','codex','local','cc-child',1),
			('co-root','claude-old','claude','local','co-root',0),('co-child','claude-old','claude','local','co-root:subagent:agent-one',1),
			('cl-root','claude-linked','claude','local','cl-root',0),('cl-child','claude-linked','claude','local','cl-root:subagent:agent-one',1)`,
		`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,provider,depth,usage_status,child_conversation_id) VALUES
			('cp-main','codex-parent','cp-root','main','root','codex',0,'unavailable',NULL),
			('cp-task','codex-parent','cp-root','task','subagent','codex',1,'unavailable',NULL),
			('cc-main','codex-child','cc-child','main','subagent','codex',1,'reported-reconciled',NULL),
			('co-main','claude-old','co-root','main','root','claude',0,'unavailable',NULL),
			('co-task','claude-old','co-root','task','subagent','claude',1,'unavailable',NULL),
			('co-child-main','claude-old','co-child','main','subagent','claude',1,'reported-reconciled',NULL),
			('cl-main','claude-linked','cl-root','main','root','claude',0,'unavailable',NULL),
			('cl-task','claude-linked','cl-root','task','subagent','claude',1,'in-child-conversation','cl-child'),
			('cl-child-main','claude-linked','cl-child','main','subagent','claude',1,'reported-reconciled',NULL)`,
	}
	for _, statement := range statements {
		if _, err := catalog.DB.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	rows := libraryRowsByID(t, catalog)
	for workspaceID, want := range map[string]int64{"codex-parent": 1, "codex-child": 0, "claude-old": 1, "claude-linked": 1} {
		if got := integer(rows[workspaceID]["subagent_count"]); got != want {
			t.Errorf("%s subagent_count=%d, want %d", workspaceID, got, want)
		}
		if got := integer(rows[workspaceID]["subagent_depth"]); got != want {
			t.Errorf("%s subagent_depth=%d, want %d", workspaceID, got, want)
		}
	}
}

func TestClaudeGroupUsageAndDelegation(t *testing.T) {
	for _, claim := range []struct {
		name, costState string
		want            int64
	}{
		{"side call", `{"type":"cost-state","modelUsage":{"opus":{"input_tokens":540,"output_tokens":60},"haiku":{"input_tokens":9,"output_tokens":1}}}`, 610},
		{"claim below children", `{"type":"cost-state","modelUsage":{"opus":{"input_tokens":200,"output_tokens":50}}}`, 600},
		// Claude Code names claims by context variant; requests use the base ID.
		{"context variant claim", `{"type":"cost-state","modelUsage":{"claude-opus[1m]":{"input_tokens":540,"output_tokens":60}}}`, 600},
	} {
		t.Run(claim.name, func(t *testing.T) {
			catalog, _ := testCatalog(t)
			rootID := "claude-root"
			root := ConversationRecord{NativeID: rootID, Provider: "claude", Model: "opus", Account: "local", StartedAt: "2026-09-20T10:00:00Z", Messages: []MessageRecord{
				{NativeID: "root-request", Kind: "metadata", Model: "opus", Text: `{"type":"assistant","message":{"id":"root-request","usage":{"input_tokens":90,"output_tokens":10}}}`},
				{NativeID: "delegate", Kind: "delegation", CallID: "tool-1", Text: `{}`},
				{NativeID: "delegation-result", Kind: "delegation_result", CallID: "tool-1", Text: `{"details":{"agentId":"child1"}}`},
				{NativeID: "claim", Kind: "metadata", Text: claim.costState},
			}}
			child := func(id, request string, input, output int) ConversationRecord {
				text := jsonText(map[string]any{"type": "assistant", "message": map[string]any{"id": request, "usage": map[string]any{"input_tokens": input, "output_tokens": output}}})
				return ConversationRecord{NativeID: rootID + ":subagent:agent-" + id, ParentNativeID: rootID, Provider: "claude", Model: "opus", Account: "local", AgentDepth: 1, Messages: []MessageRecord{{NativeID: request, Kind: "metadata", Model: "opus", Text: text}}}
			}
			record := WorkspaceRecord{SourceID: rootID, SourceKind: "claude", Account: "local", Conversations: []ConversationRecord{root, child("child1", "child-request-1", 180, 20), child("child2", "child-request-2", 270, 30)}}
			tx, err := catalog.DB.Begin()
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err = ingestWorkspace(tx, record, false); err != nil {
				tx.Rollback()
				t.Fatal(err)
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			var sessions, ledger, metric, remainder int64
			if err := catalog.DB.QueryRow("SELECT COALESCE(SUM(total_tokens),0) FROM agent_sessions").Scan(&sessions); err != nil {
				t.Fatal(err)
			}
			if err := catalog.DB.QueryRow("SELECT COALESCE(SUM(total_tokens),0) FROM agent_session_usage").Scan(&ledger); err != nil {
				t.Fatal(err)
			}
			if err := catalog.DB.QueryRow("SELECT value FROM metrics WHERE name='total_tokens' AND conversation_id IS NULL").Scan(&metric); err != nil {
				t.Fatal(err)
			}
			if err := catalog.DB.QueryRow("SELECT COALESCE(SUM(total_tokens),0) FROM agent_session_usage WHERE attribution='session-remainder'").Scan(&remainder); err != nil {
				t.Fatal(err)
			}
			if sessions != claim.want || ledger != claim.want || metric != claim.want || remainder != claim.want-600 {
				t.Fatalf("sessions=%d ledger=%d metric=%d remainder=%d, want %d", sessions, ledger, metric, remainder, claim.want)
			}
			var status, linked string
			if err := catalog.DB.QueryRow("SELECT usage_status,child_conversation_id FROM agent_sessions WHERE native_id='tool-1'").Scan(&status, &linked); err != nil {
				t.Fatal(err)
			}
			if status != "in-child-conversation" || linked == "" {
				t.Fatalf("delegation status=%q child=%q", status, linked)
			}
			workspaceID := stableID("workspace", "claude", "local", rootID)
			detail, err := catalog.WorkDetail(workspaceID)
			if err != nil || len(detail["agent_sessions"].([]map[string]any)) != 3 {
				t.Fatalf("visible sessions=%v, err=%v", detail["agent_sessions"], err)
			}
			for _, conversation := range detail["conversations"].([]map[string]any) {
				if firstString(conversation["native_id"]) == rootID && integer(conversation["token_usage"].(tokenCounts)["total_tokens"]) != claim.want-500 {
					t.Fatalf("root detail usage = %v", conversation["token_usage"])
				}
			}
			if _, err := catalog.DB.Exec("UPDATE agent_sessions SET total_tokens=999999 WHERE native_id='main' AND depth=0"); err != nil {
				t.Fatal(err)
			}
			// Imitate a catalog indexed before attribution was versioned.
			if _, err := catalog.DB.Exec("DELETE FROM usage_attribution_state"); err != nil {
				t.Fatal(err)
			}
			repaired, err := catalog.RepairUsageAttribution(context.Background(), nil)
			if err != nil || repaired != 1 {
				t.Fatalf("repair = %d, %v", repaired, err)
			}
			if err := catalog.DB.QueryRow("SELECT COALESCE(SUM(total_tokens),0) FROM agent_sessions").Scan(&sessions); err != nil || sessions != claim.want {
				t.Fatalf("repaired sessions = %d, %v; want %d", sessions, err, claim.want)
			}
			if repaired, err = catalog.RepairUsageAttribution(context.Background(), nil); err != nil || repaired != 0 {
				t.Fatalf("repair did not resume cleanly: %d, %v", repaired, err)
			}
		})
	}
}

func TestCostStateWithoutRequestsAndConductorAnonymousUsage(t *testing.T) {
	t.Run("repair retained Agent result usage", func(t *testing.T) {
		catalog, _ := testCatalog(t)
		messages := []MessageRecord{
			{NativeID: "launch", Role: "assistant", Kind: "delegation", CallID: "child", Model: "claude-opus-5", Selected: true, Text: `{"tool":"Agent","input":{"model":"sonnet"}}`},
			{NativeID: "handback", Role: "tool", Kind: "delegation_result", CallID: "child", Model: "claude-opus-5", Selected: true, CreatedAt: "2026-08-05T13:17:21Z", Text: `{"details":{"status":"completed","totalToolUseCount":0,"totalTokens":110,"usage":{"input_tokens":5,"cache_read_input_tokens":90,"cache_creation_input_tokens":5,"output_tokens":10}}}`},
		}
		record := WorkspaceRecord{SourceID: "result-usage", SourceKind: "conductor", Account: "local", Conversations: []ConversationRecord{{NativeID: "result-usage", Provider: "claude", Account: "local", Model: "claude-opus-5", Messages: messages}}}
		tx, err := catalog.DB.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err = ingestWorkspace(tx, record, false); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
		// Reproduce a catalog derived by v4: the metadata was retained but
		// never contributed to the child's session or usage ledger.
		if _, err := catalog.DB.Exec(`DELETE FROM agent_session_usage;
			UPDATE agent_sessions SET total_tokens=0,usage_status='unavailable';
			UPDATE usage_attribution_state SET version='4'`); err != nil {
			t.Fatal(err)
		}
		if repaired, err := catalog.RepairUsageAttribution(context.Background(), nil); err != nil || repaired != 1 {
			t.Fatalf("repair = %d, %v", repaired, err)
		}
		var total int64
		var model string
		if err := catalog.DB.QueryRow("SELECT SUM(total_tokens),model FROM agent_session_usage").Scan(&total, &model); err != nil || total != 110 || model != "sonnet" {
			t.Fatalf("repaired ledger = %d tokens, model %q, %v", total, model, err)
		}
		if repaired, err := catalog.RepairUsageAttribution(context.Background(), nil); err != nil || repaired != 0 {
			t.Fatalf("repeated repair = %d, %v", repaired, err)
		}
	})
	t.Run("unchanged conversation attribution upgrade", func(t *testing.T) {
		for _, omitted := range []bool{false, true} {
			t.Run(map[bool]string{false: "complete workspace", true: "omitted conversation"}[omitted], func(t *testing.T) {
				c, _ := testCatalog(t)
				record := WorkspaceRecord{SourceID: "usage-upgrade", SourceKind: "conductor", Account: "local", Title: "Initial", Conversations: []ConversationRecord{
					{NativeID: "upgrade-root", Provider: "claude", Account: "local", Messages: []MessageRecord{
						{NativeID: "launch", Kind: "delegation", CallID: "child", Text: `{"tool":"Agent","input":{}}`},
						{NativeID: "result", Kind: "delegation_result", CallID: "child", Text: agentFooterResult(116837)},
					}},
					{NativeID: "other", Provider: "claude", Account: "local", Messages: []MessageRecord{{NativeID: "reply", Kind: "message", Role: "assistant", Text: "Other conversation"}}},
				}}
				write := func() {
					t.Helper()
					tx, err := c.DB.Begin()
					if err != nil {
						t.Fatal(err)
					}
					defer tx.Rollback()
					if _, _, err = ingestWorkspace(tx, record, false); err != nil {
						t.Fatal(err)
					}
					if err = tx.Commit(); err != nil {
						t.Fatal(err)
					}
				}
				write()
				execSQL(t, c.DB, `DELETE FROM agent_session_token_evidence; UPDATE usage_attribution_state SET version='4'`)
				record.Title = "Updated workspace title"
				if omitted {
					record.Conversations = record.Conversations[:1]
				}
				write()
				var evidence int
				if err := c.DB.QueryRow(`SELECT COUNT(*) FROM agent_session_token_evidence`).Scan(&evidence); err != nil || evidence != 1 {
					t.Fatalf("unchanged conversation did not backfill evidence: %d: %v", evidence, err)
				}
				want := 0
				if omitted {
					want = 1
				}
				if repaired, err := c.RepairUsageAttribution(t.Context(), nil); err != nil || repaired != want {
					t.Fatalf("repair processed=%d want %d (omitted=%v): %v", repaired, want, omitted, err)
				}
			})
		}
	})
	noRequests := []MessageRecord{{NativeID: "claim", Text: `{"type":"cost-state","modelUsage":{"opus":{"input_tokens":40,"output_tokens":10}}}`}}
	if got := conversationTokenCounts(noRequests)["total_tokens"]; got != 50 {
		t.Fatalf("cost-state without requests = %v, want 50", got)
	}
	blocks := []MessageRecord{
		{NativeID: "first", Kind: "metadata", Text: `{"type":"assistant","message":{"usage":{"input_tokens":100,"output_tokens":10}}}`},
		{NativeID: "second", Kind: "metadata", Text: `{"type":"assistant","message":{"usage":{"input_tokens":100,"output_tokens":20}}}`},
	}
	if got := conversationTokenCounts(blocks)["total_tokens"]; got != 120 {
		t.Fatalf("anonymous session usage = %v, want 120", got)
	}
	requests, _ := buildToolLedger(blocks, "opus")
	if len(requests) != 1 || requests[0].Counts["total_tokens"] != 120 {
		t.Fatalf("anonymous request ledger = %#v", requests)
	}
	separate := append(append([]MessageRecord{}, blocks...),
		MessageRecord{NativeID: "result", Kind: "tool_result", Text: `{}`},
		MessageRecord{NativeID: "third", Kind: "metadata", Text: `{"type":"assistant","message":{"usage":{"input_tokens":100,"output_tokens":10}}}`})
	if got := conversationTokenCounts(separate)["total_tokens"]; got != 230 {
		t.Fatalf("separate anonymous requests = %v, want 230", got)
	}
}

func TestUsageAttributionHealth(t *testing.T) {
	catalog, _ := testCatalog(t)
	record := WorkspaceRecord{SourceID: "health", SourceKind: "claude", Account: "local", Conversations: []ConversationRecord{{NativeID: "health", Provider: "claude", Model: "opus", Account: "local", StartedAt: time.Now().UTC().Format(time.RFC3339), Messages: []MessageRecord{{NativeID: "request", Kind: "metadata", Text: `{"type":"assistant","message":{"id":"request","usage":{"input_tokens":100,"output_tokens":10}}}`}}}}}
	tx, err := catalog.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = ingestWorkspace(tx, record, false); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.DB.Exec("UPDATE agent_sessions SET total_tokens=200000 WHERE native_id='main'"); err != nil {
		t.Fatal(err)
	}
	aggregate := WorkspaceRecord{SourceID: "aggregate", SourceKind: "conductor", Account: "local", Conversations: []ConversationRecord{{NativeID: "aggregate", Provider: "codex", Model: "gpt-test", Account: "local", StartedAt: time.Now().UTC().Format(time.RFC3339), Messages: []MessageRecord{{NativeID: "total", Kind: "metadata", Text: `{"type":"result","usage":{"total_tokens":500}}`}}}}}
	tx, err = catalog.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = ingestWorkspace(tx, aggregate, false); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	rows, err := catalog.UsageAttributionHealth(context.Background())
	if err != nil || len(rows) != 2 || integer(rows[0]["mismatched"]) != 1 || firstString(rows[1]["status"]) != "session totals only" {
		t.Fatalf("health rows=%#v err=%v", rows, err)
	}
}

func recoveredUsageGroup() (ConversationRecord, ConversationRecord) {
	root := ConversationRecord{NativeID: "recovered-root", Provider: "claude", Model: "opus", Account: "local", Messages: []MessageRecord{
		{NativeID: "parent", Kind: "metadata", Model: "opus", Text: `{"type":"assistant","message":{"id":"parent","usage":{"input_tokens":100,"output_tokens":10}}}`},
		{NativeID: "launch", Kind: "delegation", CallID: "child", Model: "opus", Text: `{"tool":"Agent","input":{"model":"sonnet"}}`},
		{NativeID: "result", Kind: "delegation_result", CallID: "child", Text: `{"details":{"agentId":"child1","status":"completed","totalToolUseCount":0,"totalTokens":110,"usage":{"input_tokens":100,"output_tokens":10}}}`},
	}}
	child := ConversationRecord{NativeID: "recovered-root:subagent:agent-child1", ParentNativeID: "recovered-root", AgentDepth: 1, Provider: "claude", Model: "sonnet", Account: "local", Messages: []MessageRecord{
		{NativeID: "child-request", Kind: "metadata", Model: "sonnet", Text: `{"type":"assistant","message":{"id":"child-request","usage":{"input_tokens":100,"output_tokens":10}}}`},
	}}
	return root, child
}

func TestClaudeRecoveredResultsReconcileAcrossGroup(t *testing.T) {
	for _, check := range []struct {
		name                                string
		retainedChild, claim, aggregateRoot bool
	}{
		{"result fills missing child", false, false, false},
		{"group claim includes recovered result", false, true, false},
		{"child transcript owns result", true, false, false},
		{"child transcript and group claim", true, true, false},
		{"aggregate root and child transcript", true, true, true},
	} {
		t.Run(check.name, func(t *testing.T) {
			root, child := recoveredUsageGroup()
			if check.aggregateRoot {
				root.Messages = root.Messages[1:]
			}
			if check.claim {
				root.Messages = append(root.Messages, MessageRecord{NativeID: "claim", Kind: "metadata", Text: `{"type":"cost-state","modelUsage":{"opus":{"input_tokens":100,"output_tokens":10},"sonnet":{"input_tokens":100,"output_tokens":10}}}`})
			}
			record := WorkspaceRecord{SourceID: "recovered-group", SourceKind: "claude", Account: "local", Conversations: []ConversationRecord{root}}
			if check.retainedChild {
				record.Conversations = append(record.Conversations, child)
			}
			if total := metricValue(reconciledTokenMetrics(record), "total_tokens"); total != 220 {
				t.Errorf("reconciled total=%v, want 220", total)
			}
			c, _ := testCatalog(t)
			tx, err := c.DB.Begin()
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err = ingestWorkspace(tx, record, false); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			assertTotals := func() {
				t.Helper()
				var total int64
				for _, table := range []string{"agent_sessions", "agent_session_usage"} {
					if err := c.DB.QueryRow("SELECT COALESCE(SUM(total_tokens),0) FROM " + table).Scan(&total); err != nil || total != 220 {
						t.Errorf("%s total=%d want 220: %v", table, total, err)
					}
				}
			}
			assertTotals()
			if check.retainedChild {
				var status string
				if err = c.DB.QueryRow(`SELECT usage_status FROM agent_sessions WHERE native_id='child'`).Scan(&status); err != nil || status != "in-child-conversation" {
					t.Errorf("launch status=%q %v", status, err)
				}
			}
			if check.retainedChild {
				var workspace, conversation string
				if err = c.DB.QueryRow(`SELECT workspace_id,id FROM conversations WHERE native_id='recovered-root'`).Scan(&workspace, &conversation); err != nil {
					t.Fatal(err)
				}
				body, err := c.WorkConversation(workspace, conversation)
				if err != nil {
					t.Fatal(err)
				}
				flagged := false
				for _, message := range body["messages"].([]map[string]any) {
					flagged = flagged || message["native_id"] == "result" && message["usage_in_child_conversation"] == true
				}
				if !flagged {
					t.Error("reader did not receive child-owned result annotation")
				}
			}
			if _, err = c.DB.Exec(`DELETE FROM usage_attribution_state`); err != nil {
				t.Fatal(err)
			}
			if _, err = c.RefineUsageTimeline(nil); err != nil {
				t.Fatal(err)
			}
			assertTotals()
			if _, err = c.RepairUsageAttribution(t.Context(), nil); err != nil {
				t.Fatal(err)
			}
			assertTotals()
		})
	}
}

func TestUsageRepairPreservesUnreconstructableSessions(t *testing.T) {
	for _, check := range []struct {
		name, source, provider string
		mixed, ingestUpgrade   bool
	}{
		{"legacy Codex", "codex", "codex", false, false},
		{"legacy Claude", "claude", "claude", false, false},
		{"mixed workspace", "conductor", "codex", true, false},
		{"legacy Codex ingest upgrade", "codex", "codex", false, true},
		{"legacy Claude ingest upgrade", "claude", "claude", false, true},
	} {
		t.Run(check.name, func(t *testing.T) {
			c, _ := testCatalog(t)
			record := WorkspaceRecord{SourceID: "legacy-usage", SourceKind: check.source, Account: "local", Conversations: []ConversationRecord{{NativeID: "legacy", Provider: check.provider, Account: "local", Model: "gpt-test", Messages: []MessageRecord{{NativeID: "reply", Role: "assistant", Kind: "message", Text: "Retained reply without accounting envelope"}}}}}
			if check.mixed {
				record.Conversations = append(record.Conversations, ConversationRecord{NativeID: "newer", Provider: "codex", Account: "local", Model: "gpt-test", Messages: []MessageRecord{{NativeID: "request", Kind: "metadata", Text: `{"type":"assistant","message":{"id":"request","usage":{"input_tokens":50,"output_tokens":5}}}`}}})
			}
			tx, err := c.DB.Begin()
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err = ingestWorkspace(tx, record, false); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if _, err = c.DB.Exec(`DELETE FROM usage_attribution_state;
 UPDATE agent_sessions SET total_tokens=110,input_tokens=100,output_tokens=10,usage_status='reported-reconciled' WHERE conversation_id IN (SELECT id FROM conversations WHERE native_id='legacy');
 INSERT INTO agent_session_usage(agent_session_id,usage_hour,model,attribution,input_tokens,output_tokens,total_tokens)
 SELECT id,'2026-09-20T10:00:00Z',model,'session-start',100,10,110 FROM agent_sessions WHERE conversation_id IN (SELECT id FROM conversations WHERE native_id='legacy');
 INSERT INTO metrics(workspace_id,name,value,unit,status,extractor_version) SELECT id,'total_tokens',110,'tokens','reported-reconciled','legacy' FROM workspaces`); err != nil {
				t.Fatal(err)
			}
			legacySQL := `SELECT u.* FROM agent_session_usage u JOIN agent_sessions a ON a.id=u.agent_session_id JOIN conversations c ON c.id=a.conversation_id WHERE c.native_id='legacy'`
			before, err := queryMaps(c.DB, legacySQL)
			if err != nil {
				t.Fatal(err)
			}
			if check.ingestUpgrade {
				record.Title = "Updated workspace title"
				tx, err := c.DB.Begin()
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				if _, _, err = ingestWorkspace(tx, record, false); err != nil {
					t.Fatal(err)
				}
				if err = tx.Commit(); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = c.RefineUsageTimeline(nil); err != nil {
				t.Fatal(err)
			}
			if _, err = c.RepairUsageAttribution(t.Context(), nil); err != nil {
				t.Fatal(err)
			}
			after, err := queryMaps(c.DB, legacySQL)
			if err != nil {
				t.Fatal(err)
			}
			if jsonText(before) != jsonText(after) {
				t.Errorf("legacy ledger changed from %v to %v", before, after)
			}
			var total int64
			want := int64(110)
			if check.mixed {
				want += 55
			}
			for _, table := range []string{"agent_sessions", "agent_session_usage"} {
				if err = c.DB.QueryRow("SELECT SUM(total_tokens) FROM " + table).Scan(&total); err != nil || total != want {
					t.Errorf("%s total=%d want %d: %v", table, total, want, err)
				}
			}
			if err = c.DB.QueryRow(`SELECT MAX(value) FROM metrics WHERE name='total_tokens'`).Scan(&total); err != nil || total != want {
				t.Errorf("workspace metrics=%d want %d: %v", total, want, err)
			}
			if repaired, err := c.RepairUsageAttribution(t.Context(), nil); err != nil || repaired != 0 {
				t.Errorf("repeated repair=%d %v", repaired, err)
			}
		})
	}
	for _, model := range []string{"sonnet", "Unknown model"} {
		t.Run("legacy child in process claim/"+model, func(t *testing.T) {
			c, _ := testCatalog(t)
			root, child := recoveredUsageGroup()
			root.Messages[2].Text = `{"details":{"agentId":"child1","status":"async_launched"}}`
			root.Messages = append(root.Messages, MessageRecord{NativeID: "claim", Kind: "metadata", Text: `{"type":"cost-state","modelUsage":{"opus":{"input_tokens":100,"output_tokens":10},"sonnet":{"input_tokens":200,"output_tokens":20}}}`})
			child.Model = model
			child.Messages = []MessageRecord{{NativeID: "child-text", Kind: "message", Role: "assistant", Text: "Retained without accounting"}}
			record := WorkspaceRecord{SourceID: "legacy-group", SourceKind: "claude", Account: "local", Conversations: []ConversationRecord{root, child}}
			tx, err := c.DB.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, _, err = ingestWorkspace(tx, record, false); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			execSQL(t, c.DB, `DELETE FROM usage_attribution_state;
 UPDATE agent_sessions SET total_tokens=220,input_tokens=200,output_tokens=20,usage_status='reported-reconciled' WHERE conversation_id IN (SELECT id FROM conversations WHERE agent_depth=1);
 INSERT INTO agent_session_usage(agent_session_id,usage_hour,model,attribution,input_tokens,output_tokens,total_tokens)
 SELECT id,'2026-09-20T10:00:00Z',model,'session-start',200,20,220 FROM agent_sessions WHERE conversation_id IN (SELECT id FROM conversations WHERE agent_depth=1)`)
			ledgerSQL := `SELECT u.* FROM agent_session_usage u JOIN agent_sessions a ON a.id=u.agent_session_id JOIN conversations c ON c.id=a.conversation_id WHERE c.agent_depth=1`
			before, err := queryMaps(c.DB, ledgerSQL)
			if err != nil {
				t.Fatal(err)
			}
			if repaired, err := c.RepairUsageAttribution(t.Context(), nil); err != nil || repaired != 1 {
				t.Fatalf("repair=%d: %v", repaired, err)
			}
			after, err := queryMaps(c.DB, ledgerSQL)
			if err != nil || jsonText(before) != jsonText(after) {
				t.Fatalf("preserved child ledger changed: %v -> %v: %v", before, after, err)
			}
			for _, query := range []string{`SELECT SUM(total_tokens) FROM agent_sessions`, `SELECT SUM(total_tokens) FROM agent_session_usage`, `SELECT value FROM metrics WHERE name='total_tokens' AND conversation_id IS NULL`} {
				var total int
				if err := c.DB.QueryRow(query).Scan(&total); err != nil || total != 330 {
					t.Fatalf("legacy usage counted again: total=%d want 330: %v", total, err)
				}
			}
		})
	}

}

func TestUsageRepairStillAppliesExplicitZeroAndCopies(t *testing.T) {
	for _, copied := range []bool{false, true} {
		t.Run(map[bool]string{false: "explicit zero", true: "settled copy"}[copied], func(t *testing.T) {
			c, _ := testCatalog(t)
			root, _ := recoveredUsageGroup()
			root.Messages = root.Messages[:1]
			record := WorkspaceRecord{SourceID: "zero-usage", SourceKind: "claude", Account: "local", Conversations: []ConversationRecord{root}}
			tx, err := c.DB.Begin()
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err = ingestWorkspace(tx, record, false); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			var workspace, conversation string
			if err = c.DB.QueryRow(`SELECT workspace_id,id FROM conversations`).Scan(&workspace, &conversation); err != nil {
				t.Fatal(err)
			}
			if copied {
				root.Messages[0].RawText = jsonText(map[string]any{"type": claudeUsageCopyType, "copy_of": "other-owner", "copied": parseJSONValue(root.Messages[0].Text)})
			} else {
				root.Messages[0].RawText = `{"type":"assistant","message":{"id":"parent","usage":{"input_tokens":0,"output_tokens":0}}}`
			}
			tx, err = c.DB.Begin()
			if err != nil {
				t.Fatal(err)
			}
			if err = rebuildWorkspaceUsage(tx, workspace, []ConversationRecord{root}, []string{conversation}, nil); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			var total int64
			if err = c.DB.QueryRow(`SELECT SUM(total_tokens) FROM agent_sessions`).Scan(&total); err != nil || total != 0 {
				t.Errorf("authoritative zero retained stale %d tokens: %v", total, err)
			}
		})
	}
}

func TestClaudeRecoveredGroupClaimModels(t *testing.T) {
	for _, check := range []struct {
		name, model, extraModel string
		noParent, beforeResult  bool
		input, want             float64
	}{
		{"concrete family for launch alias", "claude-sonnet-5[1m]", "", false, false, 100, 220},
		{"only recovered request", "sonnet", "", true, false, 100, 110},
		{"claim before recovered result", "sonnet", "", false, true, 100, 220},
		{"positive claim remainder", "sonnet", "", false, false, 200, 320},
		{"ambiguous model stays unpriced", "claude-sonnet-5", "claude-sonnet-6", false, false, 100, 330},
	} {
		t.Run(check.name, func(t *testing.T) {
			root, _ := recoveredUsageGroup()
			if check.noParent {
				root.Messages = root.Messages[1:]
			}
			models := map[string]any{check.model: map[string]any{"input_tokens": check.input, "output_tokens": 10}}
			if !check.noParent {
				models["opus"] = map[string]any{"input_tokens": 100, "output_tokens": 10}
			}
			if check.extraModel != "" {
				models[check.extraModel] = map[string]any{"input_tokens": 100, "output_tokens": 10}
			}
			claim := MessageRecord{NativeID: "claim", Kind: "metadata", Text: jsonText(map[string]any{"type": "cost-state", "modelUsage": models})}
			if check.beforeResult {
				last := root.Messages[len(root.Messages)-1]
				root.Messages = append(root.Messages[:len(root.Messages)-1], claim, last)
			} else {
				root.Messages = append(root.Messages, claim)
			}
			record := WorkspaceRecord{SourceID: "claim-models", SourceKind: "claude", Account: "local", Conversations: []ConversationRecord{root}}
			if total := metricValue(reconciledTokenMetrics(record), "total_tokens"); total != check.want {
				t.Errorf("total=%v want %v", total, check.want)
			}
			if check.extraModel != "" {
				remainder := claudeGroupRemainder(record.Conversations, root)
				if len(remainder) != 1 || remainder["Unknown model"]["total_tokens"] != 110 {
					t.Errorf("ambiguous remainder=%+v", remainder)
				}
			}
		})
	}
}

func TestClaudeRecoveredResultYieldsWhenChildArrives(t *testing.T) {
	for _, claim := range []bool{false, true} {
		t.Run(map[bool]string{false: "requests only", true: "group remainder"}[claim], func(t *testing.T) {
			c, _ := testCatalog(t)
			root, child := recoveredUsageGroup()
			want := int64(220)
			if claim {
				want = 330
				root.Messages = append(root.Messages, MessageRecord{NativeID: "claim", Kind: "metadata", Text: `{"type":"cost-state","modelUsage":{"opus":{"input_tokens":200,"output_tokens":20},"sonnet":{"input_tokens":100,"output_tokens":10}}}`})
			}
			record := WorkspaceRecord{SourceID: "later-child", SourceKind: "claude", Account: "local", Conversations: []ConversationRecord{root}}
			write := func() {
				t.Helper()
				tx, err := c.DB.Begin()
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				if _, _, err = ingestWorkspace(tx, record, false); err != nil {
					t.Fatal(err)
				}
				if err = tx.Commit(); err != nil {
					t.Fatal(err)
				}
			}
			write()
			fingerprint := func() string {
				t.Helper()
				var value string
				if err := c.DB.QueryRow(`SELECT i.fingerprint FROM conversation_derivation_inputs i JOIN conversations c ON c.id=i.conversation_id WHERE c.agent_depth=0`).Scan(&value); err != nil {
					t.Fatal(err)
				}
				return value
			}
			sourceInput := fingerprint()
			record.Conversations = append(record.Conversations, child)
			write()
			var total int64
			if err := c.DB.QueryRow(`SELECT SUM(total_tokens) FROM agent_session_usage`).Scan(&total); err != nil || total != want {
				t.Fatalf("late child duplicated recovered usage: %d want %d: %v", total, want, err)
			}
			var status string
			if err := c.DB.QueryRow(`SELECT usage_status FROM agent_sessions WHERE native_id='child'`).Scan(&status); err != nil || status != "in-child-conversation" {
				t.Fatalf("late child did not replace launch: %q %v", status, err)
			}
			if fingerprint() != sourceInput {
				t.Fatal("ownership changed the source fingerprint used by delivery checkpoints")
			}
			execSQL(t, c.DB, `CREATE TABLE usage_write_probe(target TEXT)`)
			for _, table := range []string{"agent_sessions", "agent_session_usage", "tool_calls", "model_requests", "conversation_instructions"} {
				for _, op := range []string{"INSERT", "UPDATE", "DELETE"} {
					execSQL(t, c.DB, "CREATE TRIGGER probe_"+table+op+" AFTER "+op+" ON "+table+" BEGIN INSERT INTO usage_write_probe VALUES('"+table+"'); END")
				}
			}
			loads := 0
			load := storedWorkspaceConversations
			override(t, &storedWorkspaceConversations, func(ctx context.Context, q queryer, workspace string) ([]ConversationRecord, []string, error) {
				loads++
				return load(ctx, q, workspace)
			})
			write()
			var writes int
			if err := c.DB.QueryRow(`SELECT COUNT(*) FROM usage_write_probe`).Scan(&writes); err != nil || writes != 0 || loads != 0 {
				t.Fatalf("unchanged ingest made %d derived writes and %d full workspace loads: %v", writes, loads, err)
			}
			// A source replacement may remove accounting. Unlike a message-only
			// repair preserving a legacy fact, ingest clears that child's counts,
			// so its parent result must become countable in this same transaction.
			record.Conversations[1].Messages = []MessageRecord{{NativeID: "child-text", Kind: "message", Role: "assistant", Text: "Child retained without accounting"}}
			write()
			var recovered int
			if err := c.DB.QueryRow(`SELECT total_tokens FROM agent_sessions WHERE native_id='child'`).Scan(&recovered); err != nil || recovered != 110 {
				t.Fatalf("lost child accounting left parent fallback=%d: %v", recovered, err)
			}
			if err := c.DB.QueryRow(`SELECT SUM(total_tokens) FROM agent_session_usage`).Scan(&total); err != nil || total != want {
				t.Fatalf("accounting removal total=%d want %d: %v", total, want, err)
			}
			execSQL(t, c.DB, `DELETE FROM usage_write_probe`)
			loads = 0
			write()
			if err := c.DB.QueryRow(`SELECT COUNT(*) FROM usage_write_probe`).Scan(&writes); err != nil || writes != 0 || loads != 0 {
				t.Fatalf("stable removed accounting made %d writes and %d workspace loads: %v", writes, loads, err)
			}
			record.Conversations[1] = child
			write()
			if err := c.DB.QueryRow(`SELECT total_tokens FROM agent_sessions WHERE native_id='child'`).Scan(&recovered); err != nil || recovered != 0 {
				t.Fatalf("restored child accounting duplicated the parent result: %d: %v", recovered, err)
			}
			// An explicit zero is still child accounting evidence. It must not
			// revive a stale positive parent result or cause later sync rewrites.
			record.Conversations[1].Messages[0].Text = `{"type":"assistant","message":{"id":"child-request","usage":{"input_tokens":0,"output_tokens":0}}}`
			write()
			if err := c.DB.QueryRow(`SELECT total_tokens FROM agent_sessions WHERE native_id='child'`).Scan(&recovered); err != nil || recovered != 0 {
				t.Fatalf("explicit child zero revived stale recovered usage: %d: %v", recovered, err)
			}
			execSQL(t, c.DB, `DELETE FROM usage_write_probe`)
			write()
			if err := c.DB.QueryRow(`SELECT COUNT(*) FROM usage_write_probe`).Scan(&writes); err != nil || writes != 0 {
				t.Fatalf("unchanged ingest after ownership reversal rewrote derived data %d times: %v", writes, err)
			}
		})
	}
}
