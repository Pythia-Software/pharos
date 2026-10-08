package archive

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func canonicalToolOutput(t *testing.T, c *Catalog) map[string][]map[string]any {
	t.Helper()
	output := map[string][]map[string]any{}
	for _, table := range []string{"tool_call_cube", "tool_usage_daily", "tool_mirror_workspaces"} {
		rows, err := queryMaps(c.DB, "SELECT * FROM "+table+" ORDER BY "+map[string]string{"tool_call_cube": "workspace_id,day,tool_name,model,program,status", "tool_usage_daily": "id", "tool_mirror_workspaces": "workspace_id"}[table])
		if err != nil {
			t.Fatal(err)
		}
		// Cube order can differ within equal group prefixes; canonicalize every row.
		texts := make([]string, len(rows))
		for i, row := range rows {
			texts[i] = jsonText(row)
		}
		sort.Strings(texts)
		rows = nil
		for _, text := range texts {
			var row map[string]any
			if err := decodeJSONText(text, &row); err != nil {
				t.Fatal(err)
			}
			rows = append(rows, row)
		}
		output[table] = rows
	}
	return output
}

func assertToolReference(t *testing.T, c *Catalog) {
	t.Helper()
	incremental := canonicalToolOutput(t, c)
	generation, _, day, _, err := c.toolRollupStale(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.rebuildToolRollup(t.Context(), generation, day); err != nil {
		t.Fatal(err)
	}
	if reference := canonicalToolOutput(t, c); !same(incremental, reference) {
		t.Fatalf("incremental rollup differs from full reference")
	}
}

func TestIncrementalToolRollupParity(t *testing.T) {
	c, _ := testCatalog(t)
	seedToolCalls(t, c, 1200)
	if err := c.ensureToolRollup(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		"UPDATE tool_calls SET result_tokens=result_tokens+33,carried_tokens=carried_tokens+999,status='error',error_type='timeout' WHERE id='call-0'",
		"UPDATE tool_calls SET started_at='2026-10-05T03:00:00.000Z',sequence=5000,model='gpt-5',tool_category='edit' WHERE id='call-1'",
		"DELETE FROM tool_calls WHERE conversation_id='conv-2'",
		"UPDATE workspaces SET title='New title',source_kind='codex',repository_id=NULL WHERE id='ws-0'",
		"UPDATE repositories SET display_name='Renamed' WHERE id='repo-1'",
		"UPDATE agent_sessions SET kind='root',depth=3 WHERE id='session-1'",
	} {
		if _, err := c.DB.Exec(query); err != nil {
			t.Fatal(err)
		}
		if err := c.ensureToolRollup(t.Context()); err != nil {
			t.Fatal(err)
		}
		assertToolReference(t, c)
	}
	if dirty := countRows(t, c, "SELECT COUNT(*) FROM tool_rollup_dirty"); dirty != 0 {
		t.Fatalf("dirty partitions remain: %d", dirty)
	}
}

func TestIncrementalToolRollupBoundedAndNoop(t *testing.T) {
	c, _ := testCatalog(t)
	seedToolCalls(t, c, 400)
	if err := c.ensureToolRollup(t.Context()); err != nil {
		t.Fatal(err)
	}
	before, err := queryMaps(c.DB, "SELECT rowid,* FROM tool_call_cube WHERE workspace_id='ws-3' ORDER BY rowid")
	if err != nil {
		t.Fatal(err)
	}
	publication, _ := c.metaValue(t.Context(), "tool_rollup_publication")
	if err := c.ensureToolRollup(t.Context()); err != nil {
		t.Fatal(err)
	}
	if after, _ := c.metaValue(t.Context(), "tool_rollup_publication"); after != publication {
		t.Fatal("no-op republished cube")
	}
	if _, err := c.DB.Exec("UPDATE tool_calls SET result_tokens=result_tokens+1 WHERE conversation_id='conv-0'"); err != nil {
		t.Fatal(err)
	}
	if err := c.ensureToolRollup(t.Context()); err != nil {
		t.Fatal(err)
	}
	after, err := queryMaps(c.DB, "SELECT rowid,* FROM tool_call_cube WHERE workspace_id='ws-3' ORDER BY rowid")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("unaffected workspace was rewritten")
	}
	assertToolReference(t, c)
}

func TestToolRollupConcurrentChangeSurvivesPublication(t *testing.T) {
	c, _ := testCatalog(t)
	seedToolCalls(t, c, 200)
	if err := c.ensureToolRollup(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DB.Exec("UPDATE tool_calls SET result_tokens=result_tokens+10 WHERE id='call-0'"); err != nil {
		t.Fatal(err)
	}
	conn, err := c.DB.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	generation, _, day, _, err := c.toolRollupStale(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	build, err := c.prepareToolRollup(t.Context(), conn, generation, day, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.DB.Exec("UPDATE tool_calls SET result_tokens=result_tokens+25 WHERE id='call-0'"); err != nil {
		t.Fatal(err)
	}
	if err := c.publishToolPartitions(t.Context(), conn, build); err != nil {
		t.Fatal(err)
	}
	if countRows(t, c, "SELECT COUNT(*) FROM tool_rollup_dirty") == 0 {
		t.Fatal("publication cleared a newer edit")
	}
	if err := c.ensureToolRollup(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertToolReference(t, c)
}

func canonicalFindings(t *testing.T, c *Catalog) map[string][]map[string]any {
	t.Helper()
	output := map[string][]map[string]any{}
	for _, spec := range []struct{ table, order string }{{"findings", "id"}, {"finding_daily", "finding_id,day"}, {"finding_aliases", "alias"}, {"finding_actions", "id"}, {"finding_cart", "finding_id"}, {"finding_interventions", "id"}} {
		rows, err := queryMaps(c.DB, "SELECT * FROM "+spec.table+" ORDER BY "+spec.order)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			delete(row, "updated_at")
			for column, value := range row {
				if strings.HasSuffix(column, "_json") && value != nil {
					var decoded any
					if err := json.Unmarshal([]byte(firstString(value)), &decoded); err != nil {
						t.Fatalf("%s.%s: %v", spec.table, column, err)
					}
					row[column] = decoded
				}
			}
		}
		output[spec.table] = rows
	}
	return output
}

func TestIncrementalFindingFeatureParity(t *testing.T) {
	now := time.Date(2026, 9, 28, 15, 0, 0, 0, time.Local)
	fixture := newFindingFixture(t, now)
	c := fixture.catalog
	fixture.monthOf(now, func(day time.Time, index int) {
		fixture.addConversation(day, "codex", []detectorCall{
			{program: "rg", command: "rg --files -g AGENTS.md", resultTokens: 100},
			{program: "deployctl", command: "deployctl push --bad", status: "error", signature: "unknown flag: --bad", resultTokens: 500},
			{program: "deployctl", command: "deployctl push", resultTokens: 100},
			{program: "sed", command: "sed -n '1,900p' main.go", resultTokens: 9000, carried: 90000},
			{category: "edit", tool: "Edit", path: "main.go"},
		}, 5, 0, 1000)
	})
	if err := c.RefreshFindings(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, c, "SELECT COUNT(*) FROM findings"); n == 0 {
		t.Fatal("fixture has no findings")
	}
	for _, query := range []string{
		"SELECT 1",
		"UPDATE tool_calls SET result_tokens=3500,carried_tokens=55000,sequence=20,status='ok' WHERE id='call-28-1'",
		"UPDATE tool_calls SET started_at='2026-06-01T00:00:00.000Z' WHERE conversation_id='conv-27'",
		"DELETE FROM tool_calls WHERE conversation_id='conv-26'",
		"UPDATE model_requests SET input_tokens=120000 WHERE conversation_id='conv-25'",
		"UPDATE repositories SET display_name='Renamed alpha' WHERE id='repo-1'",
	} {
		if _, err := c.DB.Exec(query); err != nil {
			t.Fatal(err)
		}
		if err := c.RefreshFindings(t.Context(), true); err != nil {
			t.Fatal(err)
		}
		incremental := canonicalFindings(t, c)
		if err := c.runFindingsPassReference(t.Context(), true); err != nil {
			t.Fatal(err)
		}
		reference := canonicalFindings(t, c)
		if !same(incremental, reference) {
			t.Fatalf("findings differ after %s", query)
		}
	}
	c.now = func() time.Time { return now.AddDate(0, 0, 35) }
	if err := c.RefreshFindings(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	incremental := canonicalFindings(t, c)
	if err := c.runFindingsPassReference(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	if !same(incremental, canonicalFindings(t, c)) {
		t.Fatal("rollover findings differ")
	}
}

func TestCancelledAnalysesRemainDirty(t *testing.T) {
	c, _ := testCatalog(t)
	seedToolCalls(t, c, 20)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := c.ensureToolRollup(ctx); err == nil {
		t.Fatal("cancelled rollup succeeded")
	}
	if countRows(t, c, "SELECT COUNT(*) FROM tool_rollup_dirty") == 0 {
		t.Fatal("cancellation cleared dirtiness")
	}
	if err := c.ensureToolRollup(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertToolReference(t, c)
}

func TestToolRollupMirrorsPricesAndRollover(t *testing.T) {
	c, _ := testCatalog(t)
	seedToolCalls(t, c, 200)
	current := time.Date(2026, 10, 8, 15, 0, 0, 0, time.Local)
	c.now = func() time.Time { return current }
	if err := c.ensureToolRollup(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`INSERT INTO conversation_identity_links(left_id,right_id,relationship,confidence,evidence_json) VALUES('conv-0','conv-1','mirror',1,'{}')`,
		`UPDATE workspaces SET source_kind='codex',activity_at='2026-10-08T00:00:00.000Z' WHERE id='ws-1'`,
		`UPDATE conversations SET workspace_id='ws-2' WHERE id='conv-1'`,
		`DELETE FROM conversation_identity_links`,
		`UPDATE cost_changes SET input_per_mtok=input_per_mtok+0.1 WHERE id=(SELECT id FROM cost_changes LIMIT 1)`,
	} {
		if _, err := c.DB.Exec(query); err != nil {
			t.Fatal(err)
		}
		if err := c.ensureToolRollup(t.Context()); err != nil {
			t.Fatal(err)
		}
		assertToolReference(t, c)
	}
	current = current.AddDate(0, 0, 1)
	if err := c.ensureToolRollup(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertToolReference(t, c)
}

func TestCompetingToolPublicationRejectsOldBuild(t *testing.T) {
	c, _ := testCatalog(t)
	seedToolCalls(t, c, 100)
	if err := c.ensureToolRollup(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DB.Exec("UPDATE tool_calls SET result_tokens=77 WHERE id='call-0'"); err != nil {
		t.Fatal(err)
	}
	conn, err := c.DB.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	generation, _, day, _, err := c.toolRollupStale(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	build, err := c.prepareToolRollup(t.Context(), conn, generation, day, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.DB.Exec("UPDATE tool_calls SET result_tokens=88 WHERE id='call-0'"); err != nil {
		t.Fatal(err)
	}
	if err := c.ensureToolRollup(t.Context()); err != nil {
		t.Fatal(err)
	}
	before := canonicalToolOutput(t, c)
	if err := c.publishToolPartitions(t.Context(), conn, build); !errors.Is(err, errToolPublicationChanged) {
		t.Fatalf("stale publisher was accepted: %v", err)
	}
	if !same(before, canonicalToolOutput(t, c)) {
		t.Fatal("rejected publication changed output")
	}
}

func TestFindingPromptInvalidatesWithoutTools(t *testing.T) {
	now := time.Date(2026, 9, 28, 15, 0, 0, 0, time.Local)
	fixture := newFindingFixture(t, now)
	c := fixture.catalog
	fixture.monthOf(now, func(day time.Time, index int) {
		fixture.addConversation(day, "codex", []detectorCall{
			{program: "deployctl", command: "deployctl --help"}, {program: "deployctl", command: "deployctl push --bad", status: "error", signature: "unknown flag: --bad"}, {program: "deployctl", command: "deployctl push"},
		}, 3, 0, 1000)
	})
	if err := c.RefreshFindings(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	before, err := c.metaValue(t.Context(), "tool_ledger_generation")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.DB.Exec(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,source_order,created_at,content_hash)
 VALUES('prompt-only','conv-28','prompt-only','user','message','Please run deployctl --help before deploying',0,'2026-09-28T09:00:00.000Z','prompt-only')`); err != nil {
		t.Fatal(err)
	}
	if after, _ := c.metaValue(t.Context(), "tool_ledger_generation"); before != after {
		t.Fatal("fixture unexpectedly changed tool generation")
	}
	if err := c.RefreshFindings(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	incremental := canonicalFindings(t, c)
	if err := c.runFindingsPassReference(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	if !same(incremental, canonicalFindings(t, c)) {
		t.Fatal("first-user cache missed prompt-only update")
	}
}

func TestFindingFirstUserCacheIndependentOfHelpSet(t *testing.T) {
	now := time.Date(2026, 9, 28, 15, 0, 0, 0, time.Local)
	fixture := newFindingFixture(t, now)
	c := fixture.catalog
	fixture.monthOf(now, func(day time.Time, index int) {
		fixture.addConversation(day, "codex", []detectorCall{
			{program: "deployctl", command: "deployctl --help"},
			{program: "deployctl", command: "deployctl push"},
		}, 3, 0, 1000)
	})
	day := dayTime(now.Format("2006-01-02"))
	existing := fixture.addConversation(day, "codex", []detectorCall{{program: "deployctl", command: "deployctl push"}}, 3, 0, 1000)
	addPrompt := func(id string) {
		t.Helper()
		if _, err := c.DB.Exec(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,source_order,created_at,content_hash)
 VALUES(?,?,?,'user','message','Please run deployctl --help before deploying',0,?,'prompt')`, "prompt-"+id, id, "prompt-"+id, formatTime(day.Add(9*time.Hour))); err != nil {
			t.Fatal(err)
		}
	}
	refresh := func() {
		t.Helper()
		if err := c.RefreshFindings(t.Context(), true); err != nil {
			t.Fatal(err)
		}
	}
	version := func() string {
		t.Helper()
		var value string
		if err := c.DB.QueryRow("SELECT version FROM finding_feature_builds WHERE feature='first-user'").Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	addPrompt(existing)
	refresh()
	built := version()
	// A prompt must be cached before its conversation ever runs --help.
	if countRows(t, c, "SELECT COUNT(*) FROM finding_feature_partitions WHERE feature='first-user' AND conversation_id=? AND rows_json<>'null'", existing) != 1 {
		t.Fatal("conversation without help did not cache its first prompt")
	}
	if _, err := c.DB.Exec(`CREATE TABLE first_user_write_audit(operation TEXT, conversation_id TEXT);
 CREATE TRIGGER first_user_audit_insert AFTER INSERT ON finding_feature_partitions WHEN NEW.feature='first-user'
 BEGIN INSERT INTO first_user_write_audit VALUES('insert',NEW.conversation_id); END;
 CREATE TRIGGER first_user_audit_update AFTER UPDATE ON finding_feature_partitions WHEN NEW.feature='first-user'
 BEGIN INSERT INTO first_user_write_audit VALUES('update',NEW.conversation_id); END;
 CREATE TRIGGER first_user_audit_delete AFTER DELETE ON finding_feature_partitions WHEN OLD.feature='first-user'
 BEGIN INSERT INTO first_user_write_audit VALUES('delete',OLD.conversation_id); END;`); err != nil {
		t.Fatal(err)
	}
	assertWrites := func(operation, id string, count int) {
		t.Helper()
		if version() != built {
			t.Fatal("help activity or clock advance changed the first-user cache version")
		}
		if total := countRows(t, c, "SELECT COUNT(*) FROM first_user_write_audit"); total != count {
			t.Fatalf("first-user partition writes: got %d, want %d", total, count)
		}
		if matching := countRows(t, c, "SELECT COUNT(*) FROM first_user_write_audit WHERE operation=? AND conversation_id=?", operation, id); matching != count {
			t.Fatalf("expected %d %s writes for %s, got %d", count, operation, id, matching)
		}
		if _, err := c.DB.Exec("DELETE FROM first_user_write_audit"); err != nil {
			t.Fatal(err)
		}
	}
	assertParity := func() {
		t.Helper()
		incremental := canonicalFindings(t, c)
		if err := c.runFindingsPassReference(t.Context(), true); err != nil {
			t.Fatal(err)
		}
		if !same(incremental, canonicalFindings(t, c)) {
			t.Fatal("first-user cache differs from reference")
		}
	}
	added := fixture.addConversation(day, "codex", []detectorCall{{program: "deployctl", command: "deployctl --help"}}, 3, 0, 1000)
	addPrompt(added)
	c.now = func() time.Time { return now.Add(time.Minute) }
	refresh()
	assertWrites("insert", added, 1)
	assertParity()
	// Changing only tools grows the help set without changing message revisions.
	if _, err := c.DB.Exec("UPDATE tool_calls SET command='deployctl --help' WHERE conversation_id=?", existing); err != nil {
		t.Fatal(err)
	}
	refresh()
	assertWrites("", "", 0)
	assertParity()
	if _, err := c.DB.Exec("UPDATE messages SET text='Please deploy' WHERE conversation_id=?", existing); err != nil {
		t.Fatal(err)
	}
	refresh()
	assertWrites("update", existing, 1)
	assertParity()
	refresh()
	assertWrites("", "", 0)
}

func TestFindingFeaturesOnlyRewriteChangedConversations(t *testing.T) {
	now := time.Date(2026, 9, 28, 15, 0, 0, 0, time.Local)
	fixture := newFindingFixture(t, now)
	c := fixture.catalog
	fixture.monthOf(now, func(day time.Time, index int) {
		fixture.addConversation(day, "codex", []detectorCall{
			{program: "sed", command: "sed -n '1,900p' main.go", resultTokens: 9000, carried: 90000}, {category: "edit", tool: "Edit", path: "main.go"},
		}, 3, 0, 1000)
	})
	if err := c.RefreshFindings(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DB.Exec(`CREATE TABLE feature_write_audit(conversation_id TEXT);
 CREATE TRIGGER feature_write_audit_update AFTER UPDATE ON finding_feature_partitions BEGIN INSERT INTO feature_write_audit VALUES(NEW.conversation_id); END;`); err != nil {
		t.Fatal(err)
	}
	if err := c.RefreshFindings(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	if countRows(t, c, "SELECT COUNT(*) FROM feature_write_audit") != 0 {
		t.Fatal("no-op rewrote feature partitions")
	}
	if _, err := c.DB.Exec("UPDATE tool_calls SET result_tokens=5000 WHERE id='call-28-0'"); err != nil {
		t.Fatal(err)
	}
	if err := c.RefreshFindings(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	if countRows(t, c, "SELECT COUNT(*) FROM feature_write_audit WHERE conversation_id<>'conv-28'") != 0 {
		t.Fatal("a tool edit rewrote unrelated feature partitions")
	}
	if countRows(t, c, "SELECT COUNT(*) FROM feature_write_audit WHERE conversation_id='conv-28'") == 0 {
		t.Fatal("edited feature partition was not refreshed")
	}
}

func TestFindingFeatureCorruptionRecovers(t *testing.T) {
	now := time.Date(2026, 9, 28, 15, 0, 0, 0, time.Local)
	fixture := newFindingFixture(t, now)
	c := fixture.catalog
	fixture.addConversation(dayTime(now.Format("2006-01-02")), "codex", []detectorCall{{program: "sed", command: "sed -n '1,900p' main.go", resultTokens: 9000, carried: 90000}}, 3, 0, 1000)
	if err := c.RefreshFindings(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DB.Exec("UPDATE finding_feature_partitions SET rows_json='broken' WHERE feature='heavy-output'"); err != nil {
		t.Fatal(err)
	}
	c.findings.featureMu.Lock()
	c.findings.featureInputs = nil // Simulate a process restart.
	c.findings.featureMu.Unlock()
	if err := c.RefreshFindings(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	if countRows(t, c, "SELECT COUNT(*) FROM finding_feature_partitions WHERE rows_json='broken'") != 0 {
		t.Fatal("corrupt cache was not repaired")
	}
	incremental := canonicalFindings(t, c)
	if err := c.runFindingsPassReference(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	if !same(incremental, canonicalFindings(t, c)) {
		t.Fatal("cache recovery differs from reference")
	}
}

func TestFindingEvidenceTimestampTiesAreStable(t *testing.T) {
	a := findingHandle{At: "2026-09-28", ConversationID: "a", ToolCallID: "b"}
	b := findingHandle{At: "2026-09-28", ConversationID: "b", ToolCallID: "a"}
	if !reflect.DeepEqual(limitHandles([]findingHandle{a, b}, 1), limitHandles([]findingHandle{b, a}, 1)) {
		t.Fatal("equal timestamps depend on input order")
	}
}

func TestFindingFeaturesRetainConcurrentInputRevision(t *testing.T) {
	now := time.Date(2026, 9, 28, 15, 0, 0, 0, time.Local)
	fixture := newFindingFixture(t, now)
	c := fixture.catalog
	fixture.addConversation(dayTime(now.Format("2006-01-02")), "codex", []detectorCall{{program: "sed", command: "sed -n '1,900p' main.go", resultTokens: 9000}}, 3, 0, 1000)
	snapshot, err := c.DB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Rollback()
	env, err := c.loadFindingEnv(t.Context(), snapshot, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	env.featureCache = true
	if _, err := c.DB.Exec("UPDATE tool_calls SET result_tokens=5000 WHERE id='call-1-0'"); err != nil {
		t.Fatal(err)
	}
	const query = "SELECT conversation_id,result_tokens FROM tool_calls WHERE 1 /* finding partition */"
	rows, err := env.featureRows("snapshot-test", query, "conversation_id")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || integer(rows[0]["result_tokens"]) != 9000 {
		t.Fatalf("mixed snapshot: %v", rows)
	}
	if err := snapshot.Commit(); err != nil {
		t.Fatal(err)
	}
	if countRows(t, c, `SELECT COUNT(*) FROM finding_tool_revisions r JOIN finding_feature_partitions p USING(conversation_id)
 WHERE p.feature='snapshot-test' AND p.revision<>r.revision`) != 1 {
		t.Fatal("old publication consumed the new revision")
	}
	env.db = c.DB
	rows, err = env.featureRows("snapshot-test", query, "conversation_id")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || integer(rows[0]["result_tokens"]) != 5000 {
		t.Fatalf("later pass missed concurrent edit: %v", rows)
	}
}

func TestFindingCallGroupsPreserveRawStrings(t *testing.T) {
	now := time.Date(2026, 9, 28, 15, 0, 0, 0, time.Local)
	fixture := newFindingFixture(t, now)
	c := fixture.catalog
	fixture.addConversation(dayTime(now.Format("2006-01-02")), "codex", []detectorCall{{program: " echo ", command: "echo hello"}}, 3, 0, 1000)
	env, err := c.loadFindingEnv(t.Context(), c.DB, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	env.featureCache = true
	groups, err := env.callGroups()
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].Program != " echo " {
		t.Fatalf("group key was normalized: %+v", groups)
	}
	exposure, err := failureExposures(env, map[string]bool{"program:echo": true, "program: echo ": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(exposure["program:echo"]) != 0 || len(exposure["program: echo "]) != 1 {
		t.Fatalf("distinct raw programs merged: %v", exposure)
	}
}
