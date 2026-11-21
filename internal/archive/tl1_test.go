package archive

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTL1DollarDisplay(t *testing.T) {
	for value, want := range map[float64]string{
		0: "$0.00", 0.0001: "$0.00", 12.3456: "$12.35", 99.99: "$99.99",
		99.995: "$100", 100: "$100", 1234.56: "$1235", -1234.56: "$-1235", -0.0001: "$0.00",
	} {
		if got := tl1Dollars(value); got != want {
			t.Errorf("tl1Dollars(%v) = %q, want %q", value, got, want)
		}
	}
}

func TestClassifyTL1Error(t *testing.T) {
	for _, test := range []struct{ text, class, signature, attribution string }{
		{"worker wren died", "worker_died", "worker died", tl1AttributionInfrastructure},
		{"execution interrupted by process restart", "process_restart", "execution interrupted by process restart", tl1AttributionInfrastructure},
		{"codex exited with code 1: Reading additional input from stdin...\nNot inside a trusted directory and --skip-git-repo-check was not specified.", "executor_exit", "codex exited with code 1: Not inside a trusted directory and --skip-git-repo-check was not specified.", tl1AttributionConfiguration},
		{"contract_repair_exhausted: missing_output_fields: missing output field(s): explo; marker_missing: replacement 3f2a9c81d0", "output_contract", "contract_repair_exhausted: missing_output_fields: marker_missing", tl1AttributionContract},
		{"executor completed without a TL1_HANDOFF marker", "output_contract", "executor completed without a TL1_HANDOFF marker", tl1AttributionContract},
		{"Script exited with code 2", "script_exit", "script exited with code 2", tl1AttributionScript},
		{"watchdog timeout: no transcript activity for 34 minutes", "timeout", "watchdog timeout: no transcript activity for <n> minutes", tl1AttributionAgent},
		{"runtime_error", "unexplained", "runtime_error without a recorded reason", tl1AttributionAgent},
		{"", "", "", ""},
	} {
		class, signature, attribution := classifyTL1Error(test.text)
		if class != test.class || signature != test.signature || attribution != test.attribution {
			t.Errorf("classifyTL1Error(%q) = %q, %q, %q; want %q, %q, %q", test.text, class, signature, attribution, test.class, test.signature, test.attribution)
		}
	}
	// Run-specific identifiers must not split one failure into many clusters.
	_, first, _ := classifyTL1Error("failed to open /tmp/run-1/a.json for 1b39841311af4993b8fa6b957e1ff187")
	_, second, _ := classifyTL1Error("failed to open /tmp/run-2/b.json for 9199899a05e04eed8640833fa7dd50c0")
	if first != second {
		t.Errorf("signatures differ by run detail: %q vs %q", first, second)
	}
}

// tl1Fixture builds a TL1 installation with the current TL1 schema subset
// Pharos reads: flavors with agent configurations and transitions, a
// candidate whose implementation ran on Codex (transcript only, no summary)
// and whose review failed on an executor error, plus events and findings.
func tl1Fixture(t *testing.T, extraFailures int) string {
	t.Helper()
	root := t.TempDir()
	repository := filepath.Join(root, "repo")
	transcripts := filepath.Join(root, "transcripts")
	for _, directory := range []string{repository, filepath.Join(transcripts, "task-impl"), filepath.Join(transcripts, "task-review")} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	configPath := filepath.Join(repository, "tl1.json")
	if err := os.WriteFile(configPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	codex := strings.Join([]string{
		`{"type":"thread.started","thread_id":"thread-1"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"Implementing the fix."}}`,
		`{"type":"item.completed","item":{"id":"item_1","type":"command_execution","command":"go test ./...","aggregated_output":"FAIL","exit_code":1,"status":"completed"}}`,
		`{"type":"item.completed","item":{"id":"item_2","type":"file_change","changes":[{"path":"a.go","kind":"update"}],"status":"completed"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":1000000,"cached_input_tokens":900000,"output_tokens":20000,"reasoning_output_tokens":5000}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(transcripts, "task-impl", "1-codex.jsonl"), []byte(codex), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(transcripts, "task-review", "1-script.stderr.log"), []byte("boom\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(root, "tl1.db")
	db, err := sql.Open("sqlite", database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`
CREATE TABLE task_flavors (id TEXT PRIMARY KEY, name TEXT NOT NULL, execution_class TEXT, template TEXT, agent_configurations TEXT, default_agent_configuration TEXT, transitions TEXT, budgets TEXT, outputs_schema TEXT, read_only BOOLEAN, shape_version TEXT);
CREATE TABLE candidates (id TEXT PRIMARY KEY, title TEXT, status TEXT, canonical_branch TEXT, base_sha TEXT, head_sha TEXT, workflow_steps INTEGER, workflow_step_limit INTEGER, created_at TEXT);
CREATE TABLE tasks (id TEXT PRIMARY KEY, flavor_id TEXT, title TEXT, status TEXT, outcome TEXT, terminal_kind TEXT, terminal_reason TEXT, handoff_message TEXT, outputs TEXT, candidate_id TEXT, parent_task_id TEXT, shape_version TEXT, inputs TEXT, created_at TEXT, claimed_at TEXT, completed_at TEXT, updated_at TEXT);
CREATE TABLE task_attempts (id TEXT PRIMARY KEY, task_id TEXT, attempt_number INTEGER, worktree_id TEXT, executor TEXT, model TEXT, effort TEXT, agent_configuration TEXT, harness_id TEXT, status TEXT, failure_reason TEXT, started_at TEXT, completed_at TEXT);
CREATE TABLE transcript_summaries (attempt_id TEXT, filename TEXT, cost_usd REAL, model TEXT);
CREATE TABLE task_events (id INTEGER PRIMARY KEY, task_id TEXT, event_type TEXT, agent TEXT, detail TEXT, created_at TEXT);
CREATE TABLE review_findings (id TEXT PRIMARY KEY, candidate_id TEXT, review_task_id TEXT, severity TEXT, file_path TEXT, explanation TEXT, created_at TEXT);
INSERT INTO task_flavors VALUES
 ('f-impl','implement','llm','Fix the bug.','{"codex-terra-high":{"executor":"codex","model":"gpt-5.6-terra","effort":"high"}}','codex-terra-high','{"ready":{"handoff":"review"}}','{"workflow_steps":60}','{"summary":"text"}',0,'v2'),
 ('f-review','review','llm','Review it.','{"codex-terra-high":{"executor":"codex","model":"gpt-5.6-terra","effort":"high"}}','codex-terra-high','{"approved":{"handoff":"human"}}','{}','{}',1,'v1'),
 ('f-human','human-escalation','human','','{}',NULL,'{}','{}','{}',0,'v1');
INSERT INTO candidates VALUES ('cand-1','Fix parser','open','candidate/1','base','head',3,60,'2026-09-20 10:00:00');
INSERT INTO tasks VALUES
 ('task-impl','f-impl','Implement parser fix','done','ready',NULL,NULL,'ready',NULL,'cand-1',NULL,'v2','{}','2026-09-20 10:00:00','2026-09-20 10:00:05','2026-09-20 10:20:00','2026-09-20 10:20:00'),
 ('task-review','f-review','Review parser fix','done','runtime_error',NULL,'codex exited with code 1: Reading additional input from stdin...
Not inside a trusted directory and --skip-git-repo-check was not specified.',NULL,NULL,'cand-1','task-impl','v1','{}','2026-09-20 10:21:00','2026-09-20 10:21:02','2026-09-20 10:21:04','2026-09-20 10:21:04'),
 ('task-human','f-human','Recover review','pending',NULL,NULL,NULL,NULL,NULL,'cand-1','task-review','v1','{}','2026-09-20 10:22:00',NULL,NULL,'2026-09-20 10:22:00');
INSERT INTO task_attempts VALUES
 ('att-impl','task-impl',1,'wt-1','codex','gpt-5.6-terra','high','codex-terra-high','codex','succeeded',NULL,'2026-09-20 10:00:05','2026-09-20 10:20:00'),
 ('att-review','task-review',1,'wt-1','codex','gpt-5.6-terra','high','codex-terra-high','codex','succeeded',NULL,'2026-09-20 10:21:02','2026-09-20 10:21:04');
INSERT INTO task_events VALUES (1,'task-review','escalated','coordinator','{"reason":"runtime_error"}','2026-09-20 10:21:05');
INSERT INTO review_findings VALUES ('finding-1','cand-1','task-review','major','a.go','Off by one','2026-09-20 10:21:03');
`)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < extraFailures; index++ {
		task, attempt := fmt.Sprintf("task-extra-%d", index), fmt.Sprintf("att-extra-%d", index)
		if _, err := db.Exec(`INSERT INTO tasks(id,flavor_id,title,status,outcome,terminal_reason,candidate_id,shape_version,created_at,updated_at) VALUES(?,'f-review','Review again','done','runtime_error','codex exited with code 1: Not inside a trusted directory and --skip-git-repo-check was not specified.','cand-1','v1','2026-09-20 11:00:00','2026-09-20 11:00:00')`, task); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO task_attempts(id,task_id,attempt_number,worktree_id,executor,model,effort,agent_configuration,status,started_at,completed_at) VALUES(?,?,1,'wt','codex','gpt-5.6-terra','high','codex-terra-high','succeeded','2026-09-20 11:00:00','2026-09-20 11:00:02')`, attempt, task); err != nil {
			t.Fatal(err)
		}
	}
	registry := filepath.Join(root, "registry.json")
	payload, _ := json.Marshal(map[string]any{"schema_version": 1, "installations": []any{map[string]any{"installation_id": "install-1", "project_name": "fixture", "config_path": configPath, "db_path": database, "code_repo": repository, "transcripts_dir": transcripts}}})
	if err := os.WriteFile(registry, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	return registry
}

func ingestTL1Fixture(t *testing.T, catalog *Catalog, extraFailures int) {
	t.Helper()
	adapter, err := MakeAdapter(SourceConfig{Name: "tl1", Kind: "tl1", Path: tl1Fixture(t, extraFailures), Account: "local", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if result := catalog.Ingest(adapter, nil); result.Error != nil {
		t.Fatalf("ingest failed: %v", result.Error)
	}
}

func TestTL1AttemptModelIsOnlyASummaryFallback(t *testing.T) {
	for _, observed := range []string{"<synthetic>", "claude-sonnet-5"} {
		t.Run(observed, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "task", "1-claude.jsonl")
			parseJSONL(t, "claude", path, claudeTurn("s", "reply", "", "assistant", text("reply"), claudeEntry{"message": map[string]any{"model": observed}}), claudeTurn("s", "error", "", "assistant", text("failed"), claudeEntry{"message": map[string]any{"model": "<synthetic>"}}))
			adapter := &tl1Adapter{}
			c, ok, err := adapter.transcript(tl1Installation{ID: "install", Transcripts: dir}, "task", map[string]any{"id": "attempt", "model": "claude-opus-5"}, "1-claude.jsonl")
			want := observed
			if observed == "<synthetic>" {
				want = "claude-opus-5"
			}
			if err != nil || !ok || c.Model != want {
				t.Fatalf("model=%q want %q: %v", c.Model, want, err)
			}
			if m := byNativeID(c.Messages)["error"]; m.Model != "<synthetic>" {
				t.Fatalf("synthetic message changed: %+v", m)
			}
		})
	}
}

func TestTL1IngestCapturesWorkflowAndCodexTranscripts(t *testing.T) {
	catalog, _ := testCatalog(t)
	ingestTL1Fixture(t, catalog, 0)

	var tasks, attempts, events, findings int
	for table, target := range map[string]*int{"tl1_tasks": &tasks, "tl1_attempts": &attempts, "tl1_events": &events, "tl1_review_findings": &findings} {
		if err := catalog.DB.QueryRow("SELECT COUNT(*) FROM " + table + " WHERE installation_id='install-1'").Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	if tasks != 3 || attempts != 2 || events != 1 || findings != 1 {
		t.Fatalf("snapshot counts: tasks=%d attempts=%d events=%d findings=%d", tasks, attempts, events, findings)
	}
	// TL1 writes no transcript summary for Codex; the transcript must still be
	// found on disk and its exec-format usage accounted for.
	var conversation string
	if err := catalog.DB.QueryRow("SELECT conversation_native_id FROM tl1_attempts WHERE attempt_id='att-impl'").Scan(&conversation); err != nil {
		t.Fatal(err)
	}
	if conversation != "install-1:att-impl:1-codex.jsonl" {
		t.Fatalf("Codex attempt not linked to its transcript: %q", conversation)
	}
	// TL1 orchestrates; the provider is the agent that wrote the transcript.
	var conversationProvider, sessionProvider string
	if err := catalog.DB.QueryRow(`SELECT c.provider,s.provider FROM conversations c JOIN agent_sessions s ON s.conversation_id=c.id
		WHERE c.native_id=?`, conversation).Scan(&conversationProvider, &sessionProvider); err != nil {
		t.Fatal(err)
	}
	if conversationProvider != "codex" || sessionProvider != "codex" {
		t.Fatalf("TL1 Codex transcript provider: conversation=%q session=%q", conversationProvider, sessionProvider)
	}
	var start, end, timing string
	if err := catalog.DB.QueryRow(`SELECT s.started_at,s.ended_at,s.time_bounds_source FROM agent_sessions s JOIN conversations c ON c.id=s.conversation_id WHERE c.native_id=?`, conversation).Scan(&start, &end, &timing); err != nil {
		t.Fatal(err)
	}
	if start != "2026-09-20T10:00:05.000Z" || end != "2026-09-20T10:20:00.000Z" || timing != "attempt-envelope" {
		t.Fatalf("Codex root envelope: %s / %s (%s)", start, end, timing)
	}
	var timedRequests int
	if err := catalog.DB.QueryRow(`SELECT COUNT(*) FROM model_requests r JOIN conversations c ON c.id=r.conversation_id WHERE c.native_id=? AND r.requested_at IS NOT NULL`, conversation).Scan(&timedRequests); err != nil || timedRequests != 0 {
		t.Fatalf("timestamp-free requests acquired times: %d, %v", timedRequests, err)
	}
	var input, cached, output, reasoning int64
	if err := catalog.DB.QueryRow(`SELECT s.input_tokens,s.cache_read_input_tokens,s.output_tokens,s.reasoning_output_tokens
		FROM agent_sessions s JOIN conversations c ON c.id=s.conversation_id WHERE c.native_id=?`, conversation).Scan(&input, &cached, &output, &reasoning); err != nil {
		t.Fatal(err)
	}
	if input != 1_000_000 || cached != 900_000 || output != 20_000 || reasoning != 5_000 {
		t.Fatalf("Codex exec usage: input=%d cached=%d output=%d reasoning=%d", input, cached, output, reasoning)
	}
	var class, attribution, workspace string
	if err := catalog.DB.QueryRow("SELECT error_class,error_attribution,workspace_id FROM tl1_tasks WHERE task_id='task-review'").Scan(&class, &attribution, &workspace); err != nil {
		t.Fatal(err)
	}
	if class != "executor_exit" || attribution != tl1AttributionConfiguration {
		t.Fatalf("review error classified as %s/%s", class, attribution)
	}
	var workspaces int
	if err := catalog.DB.QueryRow("SELECT COUNT(*) FROM workspaces WHERE id=? AND source_kind='tl1'", workspace).Scan(&workspaces); err != nil || workspaces != 1 {
		t.Fatalf("TL1 task does not join its Library workspace %s: %d %v", workspace, workspaces, err)
	}
}

// A resumed Codex run appends to the attempt's file and numbers its items
// from item_0 again. Every item must be kept, each call with its own result,
// and under the same IDs however much the file later grows.
func TestTL1CodexItemNumberingRestarts(t *testing.T) {
	catalog, _ := testCatalog(t)
	registry := tl1Fixture(t, 0)
	path := filepath.Join(filepath.Dir(registry), "transcripts", "task-impl", "1-codex.jsonl")
	command := func(id, command string, exit int) string {
		return fmt.Sprintf(`{"type":"item.completed","item":{"id":%q,"type":"command_execution","command":%q,"aggregated_output":%q,"exit_code":%d,"status":"completed"}}`, id, command, "output of "+command, exit)
	}
	usage := `{"type":"turn.completed","usage":{"input_tokens":10,"cached_input_tokens":0,"output_tokens":1,"reasoning_output_tokens":0}}`
	lines := []string{
		`{"type":"thread.started","thread_id":"thread-1"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"First run."}}`,
		command("item_1", "make build", 0),
		usage,
		`{"type":"thread.started","thread_id":"thread-1"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"Second run."}}`,
		command("item_1", "make check", 1),
		command("item_2", "make lint", 0),
		usage,
	}
	type call struct{ command, status, result string }
	ingest := func() (map[string]string, map[string]call) {
		t.Helper()
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		adapter, err := MakeAdapter(SourceConfig{Name: "tl1", Kind: "tl1", Path: registry, Account: "local", Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		if result := catalog.Ingest(adapter, nil); result.Error != nil {
			t.Fatalf("ingest failed: %v", result.Error)
		}
		rows, err := queryMaps(catalog.DB, `SELECT m.native_id,m.id FROM messages m JOIN conversations c ON c.id=m.conversation_id WHERE c.native_id='install-1:att-impl:1-codex.jsonl'`)
		if err != nil {
			t.Fatal(err)
		}
		messages := map[string]string{}
		for _, row := range rows {
			messages[firstString(row["native_id"])] = firstString(row["id"])
		}
		rows, err = queryMaps(catalog.DB, `SELECT t.call_id,t.command,t.status,json_extract(r.text,'$.content') result FROM tool_calls t
			JOIN conversations c ON c.id=t.conversation_id LEFT JOIN messages r ON r.id=t.result_message_id WHERE c.native_id='install-1:att-impl:1-codex.jsonl'`)
		if err != nil {
			t.Fatal(err)
		}
		calls := map[string]call{}
		for _, row := range rows {
			calls[firstString(row["call_id"])] = call{firstString(row["command"]), firstString(row["status"]), firstString(row["result"])}
		}
		return messages, calls
	}
	messages, calls := ingest()
	// Two replies, three calls with their results, and two usage records.
	if len(messages) != 10 {
		t.Fatalf("stored %d of 10 messages: %v", len(messages), messages)
	}
	if messages["item_0"] == "" || messages["item_1:call"] == "" || messages["item_2:result"] == "" {
		t.Fatalf("IDs that never repeat changed: %v", messages)
	}
	if len(calls) != 3 {
		t.Fatalf("ledger holds %d of 3 calls: %+v", len(calls), calls)
	}
	statuses := map[string]string{}
	for id, call := range calls {
		if call.result != "output of "+call.command {
			t.Fatalf("call %s (%s) took another call's result %q", id, call.command, call.result)
		}
		statuses[call.command] = call.status
	}
	if statuses["make build"] != "ok" || statuses["make check"] != "error" || statuses["make lint"] != "ok" {
		t.Fatalf("statuses: %v", statuses)
	}

	lines = append(lines, `{"type":"thread.started","thread_id":"thread-1"}`, `{"type":"turn.started"}`, command("item_1", "make again", 0), command("item_2", "make more", 2), usage)
	grown, calls := ingest()
	for native, id := range messages {
		if grown[native] != id {
			t.Fatalf("message %s changed identity when the file grew: %q, was %q", native, grown[native], id)
		}
	}
	if len(grown) != 15 || len(calls) != 5 {
		t.Fatalf("after growth: %d of 15 messages, %d of 5 calls", len(grown), len(calls))
	}
	for id, call := range calls {
		if call.result != "output of "+call.command {
			t.Fatalf("after growth, call %s (%s) took another call's result %q", id, call.command, call.result)
		}
	}
}

func TestTL1AttemptSessionEnvelopeScope(t *testing.T) {
	registry := tl1Fixture(t, 0)
	installation := tl1Installation{ID: "install", Transcripts: filepath.Join(filepath.Dir(registry), "transcripts")}
	a := &tl1Adapter{baseAdapter: baseAdapter{config: SourceConfig{Account: "local"}}}
	for _, tc := range []struct {
		name, end    string
		files        []string
		wantEnvelope bool
	}{
		{"completed", "2026-09-20T10:20:00Z", []string{"1-codex.jsonl"}, true},
		{"ongoing", "", []string{"1-codex.jsonl"}, true},
		{"multiple transcripts", "2026-09-20T10:20:00Z", []string{"1-codex.jsonl", "1-other.jsonl"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attempt := map[string]any{"id": "attempt", "attempt_number": 1, "executor": "codex", "started_at": "2026-09-20T10:00:00Z", "completed_at": tc.end}
			record, err := a.task(installation, map[string]any{"id": "task-impl"}, []map[string]any{attempt}, nil, nil, nil, nil, nil, nil, tc.files)
			if err != nil || len(record.Conversations) != 1 {
				t.Fatalf("task: %+v, %v", record, err)
			}
			conversation := record.Conversations[0]
			if (rootSessionEnvelope(conversation) != nil) != tc.wantEnvelope {
				t.Fatalf("attempt scope: %+v", conversation.SessionBounds)
			}
			if tc.end == "" && conversation.EndedAt != "" {
				t.Fatal("ongoing attempt manufactured a completion")
			}
		})
	}
	t.Run("completion arrives after ongoing ingest", func(t *testing.T) {
		c, _ := testCatalog(t)
		conversation := ConversationRecord{NativeID: "ongoing-root", Provider: "codex", Account: "local", StartedAt: "2026-09-20T10:00:00.000Z", SessionBounds: attemptSessionBounds("2026-09-20T10:00:00Z", ""), Messages: []MessageRecord{{NativeID: "request", Kind: "metadata", RawText: `{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":2}}`}}}
		record := WorkspaceRecord{SourceID: "ongoing", SourceKind: "tl1", Account: "local", Conversations: []ConversationRecord{conversation}}
		ingestRecordTx(t, c, record)
		end := "2026-09-20T10:20:00.000Z"
		record.Conversations[0].EndedAt = end
		record.Conversations[0].SessionBounds = attemptSessionBounds(conversation.StartedAt, end)
		ingestRecordTx(t, c, record)
		var conversationEnd, sessionEnd, source string
		if err := c.DB.QueryRow(`SELECT c.ended_at,s.ended_at,s.time_bounds_source FROM conversations c JOIN agent_sessions s ON s.conversation_id=c.id WHERE c.native_id='ongoing-root'`).Scan(&conversationEnd, &sessionEnd, &source); err != nil {
			t.Fatal(err)
		}
		if conversationEnd != end || sessionEnd != end || source != "attempt-envelope" {
			t.Fatalf("completion: %s / %s (%s)", conversationEnd, sessionEnd, source)
		}
	})
}

// TL1 before registry.json listed projects in workspaces.json, and TL1 goes
// on listing each one from there until it is next used and registered.
func TestTL1RegistryRowsIncludeUnregisteredLegacyProjects(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, value any) string {
		path := filepath.Join(dir, name)
		payload, _ := json.Marshal(value)
		probePut(t, path, string(payload))
		return path
	}
	project := func(name, config string) map[string]any {
		return map[string]any{"project_name": name, "config_path": config, "code_repo": filepath.Dir(config), "last_used": "2026-04-29T08:12:49"}
	}
	// alpha keeps TL1's default locations; beta's tl1.json moves them.
	alpha := write("alpha/tl1.json", map[string]any{"project_name": "alpha"})
	beta := write("beta/tl1.json", map[string]any{"project_name": "beta", "db_path": "state/beta.db", "transcripts_dir": "/elsewhere/beta"})
	gamma := write("gamma/tl1.json", map[string]any{"project_name": "gamma"})
	delta := write("delta/tl1.json", map[string]any{"project_name": "delta"})
	legacy := write(tl1LegacyRegistryFile, map[string]any{"workspaces": []any{project("alpha", alpha), project("beta", beta), project("gamma", gamma), project("delta", delta)}})
	rows, err := tl1RegistryRows(legacy, os.ReadFile)
	if err != nil || len(rows) != 4 {
		t.Fatalf("legacy rows = %v %v", rows, err)
	}
	if rows[0]["db_path"] != filepath.Join(dir, "alpha.db") || rows[0]["transcripts_dir"] != filepath.Join(dir, "alpha", "transcripts") || rows[0]["installation_id"] != nil {
		t.Fatalf("alpha = %v", rows[0])
	}
	if rows[1]["db_path"] != filepath.Join(dir, "beta", "state", "beta.db") || rows[1]["transcripts_dir"] != "/elsewhere/beta" {
		t.Fatalf("beta = %v", rows[1])
	}
	// Once TL1 registers gamma, and another installation claims delta's
	// database, only alpha and beta remain listed from workspaces.json,
	// whichever registry file the source names.
	registry := write(tl1RegistryFile, map[string]any{"installations": []any{
		map[string]any{"installation_id": "g", "project_name": "gamma", "config_path": gamma, "code_repo": filepath.Dir(gamma), "db_path": filepath.Join(dir, "gamma.db")},
		map[string]any{"installation_id": "d", "project_name": "delta-v2", "config_path": filepath.Join(dir, "delta-v2", "tl1.json"), "code_repo": dir, "db_path": filepath.Join(dir, "delta.db")},
	}})
	for _, path := range []string{registry, legacy} {
		rows, err := tl1RegistryRows(path, os.ReadFile)
		names := []string{}
		for _, row := range rows {
			names = append(names, firstString(row["project_name"]))
		}
		if err != nil || strings.Join(names, ",") != "gamma,delta-v2,alpha,beta" {
			t.Fatalf("rows from %s = %v %v", filepath.Base(path), names, err)
		}
	}
	other := write("other.json", map[string]any{"projects": []any{}})
	if _, err := tl1RegistryRows(other, os.ReadFile); err == nil || !strings.Contains(err.Error(), "installations or workspaces") {
		t.Fatalf("unrecognized registry error = %v", err)
	}
}

func TestTL1IngestsLegacyWorkspaces(t *testing.T) {
	root := filepath.Dir(tl1Fixture(t, 0))
	if err := os.Remove(filepath.Join(root, tl1RegistryFile)); err != nil {
		t.Fatal(err)
	}
	repository, database := filepath.Join(root, "repo"), filepath.Join(root, "tl1.db")
	config := filepath.Join(repository, "tl1.json")
	probePut(t, config, `{"project_name":"fixture","db_path":"../tl1.db","transcripts_dir":"../transcripts"}`)
	payload, _ := json.Marshal(map[string]any{"workspaces": []any{map[string]any{"project_name": "fixture", "config_path": config, "code_repo": repository, "last_used": "2026-04-29T08:12:49"}}})
	legacy := filepath.Join(root, tl1LegacyRegistryFile)
	probePut(t, legacy, string(payload))
	catalog, _ := testCatalog(t)
	adapter, err := MakeAdapter(SourceConfig{Name: "tl1", Kind: "tl1", Path: legacy, Account: "local", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if result := catalog.Ingest(adapter, nil); result.Error != nil || result.Workspaces != 3 {
		t.Fatalf("ingest = %+v", result)
	}
	// A legacy project has no installation ID; its database identifies it.
	var tasks int
	var project, conversation string
	if err := catalog.DB.QueryRow("SELECT COUNT(*) FROM tl1_tasks WHERE installation_id=?", database).Scan(&tasks); err != nil || tasks != 3 {
		t.Fatalf("tasks = %d %v", tasks, err)
	}
	if err := catalog.DB.QueryRow("SELECT project FROM tl1_installations WHERE installation_id=?", database).Scan(&project); err != nil || project != "fixture" {
		t.Fatalf("installation project = %q %v", project, err)
	}
	if err := catalog.DB.QueryRow("SELECT conversation_native_id FROM tl1_attempts WHERE attempt_id='att-impl'").Scan(&conversation); err != nil || conversation != database+":att-impl:1-codex.jsonl" {
		t.Fatalf("transcript = %q %v", conversation, err)
	}
	// Once TL1 registers the project, its database has an installation ID,
	// and the rows stored under its path must go, or the project's history
	// would count twice.
	registered, _ := json.Marshal(map[string]any{"installations": []any{map[string]any{"installation_id": "install-1", "project_name": "fixture", "config_path": config, "db_path": database, "code_repo": repository, "transcripts_dir": filepath.Join(root, "transcripts")}}})
	registry := filepath.Join(root, tl1RegistryFile)
	probePut(t, registry, string(registered))
	if adapter, err = MakeAdapter(SourceConfig{Name: "tl1", Kind: "tl1", Path: registry, Account: "local", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if result := catalog.Ingest(adapter, nil); result.Error != nil {
		t.Fatalf("ingest = %+v", result)
	}
	installations, err := catalog.tl1Installations()
	if err != nil || len(installations) != 1 || installations[0]["installation_id"] != "install-1" || integer(installations[0]["tasks"]) != 3 {
		t.Fatalf("installations after registration = %v %v", installations, err)
	}
}

// tl1Rekey turns a fixture into another installation of the same project:
// TL1 IDs are random, so another Mac's tasks never share them.
func tl1Rekey(t *testing.T, registry, installation, prefix string) {
	t.Helper()
	root := filepath.Dir(registry)
	db, err := sql.Open("sqlite", filepath.Join(root, "tl1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		"UPDATE tasks SET id=?1||id, parent_task_id=?1||parent_task_id, candidate_id=?1||candidate_id",
		"UPDATE task_attempts SET id=?1||id, task_id=?1||task_id",
		"UPDATE candidates SET id=?1||id",
		"UPDATE task_events SET task_id=?1||task_id",
		"UPDATE review_findings SET id=?1||id, candidate_id=?1||candidate_id, review_task_id=?1||review_task_id",
	} {
		if _, err := db.Exec(statement, prefix); err != nil {
			t.Fatal(err)
		}
	}
	for _, task := range []string{"task-impl", "task-review"} {
		if err := os.Rename(filepath.Join(root, "transcripts", task), filepath.Join(root, "transcripts", prefix+task)); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(registry)
	if err != nil {
		t.Fatal(err)
	}
	probePut(t, registry, strings.Replace(string(data), `"install-1"`, `"`+installation+`"`, 1))
}

func ingestTL1Registry(t *testing.T, catalog *Catalog, registry string) {
	t.Helper()
	adapter, err := MakeAdapter(SourceConfig{Name: "tl1", Kind: "tl1", Path: registry, Account: "local", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if result := catalog.Ingest(adapter, nil); result.Error != nil {
		t.Fatalf("ingest failed: %v", result.Error)
	}
}

// A project TL1 runs on two Macs is two installations of one project. The
// analysis combines them, and can narrow to either Mac.
func TestTL1CombinesAProjectAcrossMacs(t *testing.T) {
	catalog, _ := testCatalog(t)
	useHost(t, "host_a")
	first := tl1Fixture(t, 0)
	ingestTL1Registry(t, catalog, first)
	useHost(t, "host_b")
	second := tl1Fixture(t, 2)
	tl1Rekey(t, second, "install-2", "b-")
	ingestTL1Registry(t, catalog, second)
	// A database carried to a third Mac holds the same tasks, counted once.
	useHost(t, "host_c")
	data, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	carried := filepath.Join(t.TempDir(), tl1RegistryFile)
	probePut(t, carried, strings.Replace(string(data), `"install-1"`, `"install-3"`, 1))
	ingestTL1Registry(t, catalog, carried)

	installations, err := catalog.tl1Installations()
	if err != nil {
		t.Fatal(err)
	}
	projects := tl1Projects(installations)
	if len(projects) != 1 || projects[0]["project"] != "fixture" || len(projects[0]["installations"].([]map[string]any)) != 3 {
		t.Fatalf("projects = %v", projects)
	}
	overview, err := catalog.TL1Overview(tl1Selection{}, tl1Window{})
	if err != nil {
		t.Fatal(err)
	}
	totals := overview["totals"].(map[string]any)
	if overview["project"] != "fixture" || totals["tasks"] != 8 || totals["llm_attempts"] != 6 {
		t.Fatalf("combined totals: project=%v %v", overview["project"], totals)
	}
	if clusters := overview["errors"].([]map[string]any); len(clusters) != 1 || clusters[0]["count"] != 4 {
		t.Fatalf("both Macs' review failures should cluster together: %v", clusters)
	}
	prompt := firstString(mustTL1Flavor(t, catalog, "review")["prompt"])
	for _, want := range []string{"Runs on 3 Macs", "Mac host_a", "Mac host_b"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("flavor prompt is missing %q:\n%s", want, prompt)
		}
	}
	candidate, err := catalog.TL1Candidate(tl1Selection{}, "b-cand-1")
	if err != nil {
		t.Fatal(err)
	}
	if tasks := candidate["tasks"].([]map[string]any); len(tasks) != 5 || tasks[0]["mac"] != "Mac host_b" {
		t.Fatalf("host_b candidate: %v", tasks)
	}
	narrowed, err := catalog.TL1Overview(tl1Selection{Installation: "install-2"}, tl1Window{})
	if err != nil {
		t.Fatal(err)
	}
	if tasks := narrowed["totals"].(map[string]any)["tasks"]; tasks != 5 {
		t.Fatalf("host_b alone: %v tasks", tasks)
	}
	if _, err := catalog.TL1Overview(tl1Selection{Project: "missing"}, tl1Window{}); err == nil {
		t.Fatal("an unknown project should be an error")
	}
	rows, err := catalog.tl1AttemptRows()
	if err != nil || len(rows) != 6 {
		t.Fatalf("tl1_attempts rows = %d %v", len(rows), err)
	}
	for _, row := range rows {
		if row["model_effort"] != modelEffort(firstString(row["model"]), firstString(row["effort"])) {
			t.Fatalf("attempt lost model and effort pair: %v", row)
		}
	}
}

func TestTL1ReingestCorrectsLegacyProviderInPlace(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run("AGY previously parsed as "+provider, func(t *testing.T) {
			catalog, _ := testCatalog(t)
			registry := tl1Fixture(t, 0)
			tl1AGYFixture(t, filepath.Dir(registry))
			adapter, err := MakeAdapter(SourceConfig{Name: "tl1", Kind: "tl1", Path: registry, Account: "local", Enabled: true})
			if err != nil {
				t.Fatal(err)
			}
			// Older parsers wrote this transcript under the fallback provider.
			if err := adapter.Discover(func(record WorkspaceRecord) error {
				if record.SourceID == "install-1:task-agy" {
					for index := range record.Conversations {
						conversation := &record.Conversations[index]
						conversation.Provider, conversation.Harness, conversation.Coverage = provider, "claude-code", "complete"
						conversation.Messages, conversation.Aliases = nil, nil
					}
					ingestRecordTx(t, catalog, record)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			const native = "install-1:att-agy:1-agy.jsonl"
			oldID := stableID("conversation", provider, "local", native)
			for range 2 {
				if result := catalog.Ingest(adapter, nil); result.Error != nil {
					t.Fatal(result.Error)
				}
				rows, err := queryMaps(catalog.DB, "SELECT id,provider,(SELECT COUNT(*) FROM messages WHERE conversation_id=c.id) messages FROM conversations c WHERE native_id=?", native)
				if err != nil || len(rows) != 1 || firstString(rows[0]["id"]) != oldID || rows[0]["provider"] != "antigravity" || integer(rows[0]["messages"]) == 0 {
					t.Fatalf("AGY upgrade must fill the existing conversation %s: %v, %v", oldID, rows, err)
				}
			}
		})
	}

	catalog, _ := testCatalog(t)
	ingestTL1Fixture(t, catalog, 0)
	native := "install-1:att-impl:1-codex.jsonl"
	var id string
	if err := catalog.DB.QueryRow("SELECT id FROM conversations WHERE native_id=?", native).Scan(&id); err != nil {
		t.Fatal(err)
	}
	// Simulate a catalog written when TL1 transcripts were filed under 'tl1'.
	if _, err := catalog.DB.Exec("UPDATE conversations SET provider='tl1' WHERE native_id=?", native); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.DB.Exec("DELETE FROM meta WHERE key='tl1_native_provider_version'"); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Initialize(); err != nil {
		t.Fatal(err)
	}
	var fingerprint sql.NullString
	if err := catalog.DB.QueryRow("SELECT fingerprint FROM source_states WHERE kind='tl1'").Scan(&fingerprint); err != nil || fingerprint.Valid {
		t.Fatalf("TL1 source not queued for re-read: %v %v", fingerprint, err)
	}
	ingestTL1Fixture(t, catalog, 0)
	var count int
	var provider, newID string
	if err := catalog.DB.QueryRow("SELECT COUNT(*),MAX(provider),MAX(id) FROM conversations WHERE native_id=?", native).Scan(&count, &provider, &newID); err != nil {
		t.Fatal(err)
	}
	if count != 1 || provider != "codex" || newID != id {
		t.Fatalf("legacy TL1 conversation not corrected in place: count=%d provider=%q id=%q (was %q)", count, provider, newID, id)
	}
}

func TestTL1OverviewFindsConcernsWithPrompts(t *testing.T) {
	catalog, _ := testCatalog(t)
	ingestTL1Fixture(t, catalog, 0)
	overview, err := catalog.TL1Overview(tl1Selection{}, tl1Window{})
	if err != nil {
		t.Fatal(err)
	}
	totals := overview["totals"].(map[string]any)
	if totals["llm_attempts"] != 2 || totals["human_tasks"] != 1 || totals["pending_human"] != 1 {
		t.Fatalf("unexpected totals: %v", totals)
	}
	if cost, _ := totals["cost_usd"].(float64); cost <= 0 {
		t.Fatalf("Codex tokens were not priced: %v", totals["cost_usd"])
	}
	var implement map[string]any
	for _, row := range overview["matrix"].([]map[string]any) {
		if row["flavor"] == "implement" && row["configuration"] == "codex-terra-high" {
			implement = row
		}
	}
	if implement == nil || implement["advanced"] != 1 || implement["default"] != true {
		t.Fatalf("implement matrix row: %v", implement)
	}
	errors := overview["errors"].([]map[string]any)
	if len(errors) != 1 || errors[0]["human_followups"] != 1 || !strings.Contains(firstString(errors[0]["signature"]), "trusted directory") {
		t.Fatalf("error clusters: %v", errors)
	}
	human := overview["human"].(map[string]any)
	if human["error_caused"] != 1 {
		t.Fatalf("human task should be attributed to the upstream error: %v", human)
	}
	reviews := overview["reviews"].(map[string]any)
	if authors := reviews["by_implementation"].([]map[string]any); len(authors) != 1 || authors[0]["configuration"] != "codex-terra-high" || authors[0]["major_or_blocker"] != 1 {
		t.Fatalf("review finding attribution: %v", reviews["by_implementation"])
	}
	followups := overview["followups"].(map[string]any)
	var reviewHandoff, humanFollowup map[string]any
	for _, row := range followups["next"].([]map[string]any) {
		if row["from_flavor"] == "implement" && row["next_flavor"] == "review" {
			reviewHandoff = row
		}
	}
	for _, row := range followups["after"].([]map[string]any) {
		if row["from_flavor"] == "implement" && row["after_flavor"] == "human-escalation" {
			humanFollowup = row
		}
	}
	if reviewHandoff == nil || reviewHandoff["next_model"] != "gpt-5.6-terra" || reviewHandoff["with_findings"] != 1 || reviewHandoff["findings"] != 1 || reviewHandoff["paths"] != 1 {
		t.Fatalf("review handoff and findings: %v", reviewHandoff)
	}
	if humanFollowup == nil || humanFollowup["next_model"] != "gpt-5.6-terra" || humanFollowup["after_outcome"] != "Pending" || humanFollowup["with_findings"] != 1 {
		t.Fatalf("human follow-up after review: %v", humanFollowup)
	}
	if _, ok := overview["review_prompt"]; ok {
		t.Error("TL1's recommendations are findings; the overview makes none")
	}
	candidate, err := catalog.TL1Candidate(tl1Selection{Installation: "install-1"}, "cand-1")
	if err != nil {
		t.Fatal(err)
	}
	if tasks := candidate["tasks"].([]map[string]any); len(tasks) != 3 || tasks[1]["disposition"] != tl1Errored {
		t.Fatalf("candidate timeline: %v", tasks)
	}
	flavor, err := catalog.TL1Flavor(tl1Selection{Project: "fixture"}, "implement", tl1Window{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(firstString(flavor["prompt"]), "Fix the bug.") {
		t.Fatalf("flavor prompt should include the template: %s", flavor["prompt"])
	}
	// Scope "current" drops runs of superseded definitions.
	current, err := catalog.TL1Overview(tl1Selection{Installation: "install-1"}, tl1Window{Scope: "current"})
	if err != nil {
		t.Fatal(err)
	}
	if current["totals"].(map[string]any)["tasks"] != 3 {
		t.Fatalf("fixture tasks all use current shape versions: %v", current["totals"])
	}
}

func TestTL1FollowupsSeparateReviewModelsAndHumanDecisions(t *testing.T) {
	data := &tl1Data{TaskByID: map[string]*tl1Task{}}
	for _, run := range []struct {
		id, model, decision string
		findings            int
	}{
		{"a", "review-model-a", "remand", 2},
		{"b", "review-model-b", "approve", 0},
	} {
		implement := &tl1Task{ID: "implement-" + run.id, Flavor: "implement", ExecutionClass: "llm", Outcome: "ready"}
		review := &tl1Task{ID: "review-" + run.id, Flavor: "agent-review", ExecutionClass: "llm", Outcome: "reviewed", Parent: implement}
		human := &tl1Task{ID: "human-" + run.id, Flavor: "human-review", ExecutionClass: "human", Parent: review}
		implement.Attempts = []*tl1Attempt{{Configuration: "implement-config", Model: "implementation-model"}}
		review.Attempts = []*tl1Attempt{{Configuration: "review-config", Model: run.model}}
		for _, task := range []*tl1Task{implement, review, human} {
			data.Tasks = append(data.Tasks, task)
			data.TaskByID[task.ID] = task
		}
		data.Touches = append(data.Touches, map[string]any{"kind": "response", "task_id": human.ID, "action": run.decision})
		for range run.findings {
			data.Findings = append(data.Findings, map[string]any{"review_task_id": review.ID})
		}
	}
	followups := tl1Followups(data)
	after := followups["after"].([]map[string]any)
	if len(after) != 2 {
		t.Fatalf("expected distinct model and decision paths: %v", after)
	}
	byModel := map[string]map[string]any{}
	for _, row := range after {
		byModel[firstString(row["next_model"])] = row
	}
	if a := byModel["review-model-a"]; a["after_outcome"] != "remand" || a["with_findings"] != 1 || a["findings"] != 2 {
		t.Fatalf("review model a: %v", a)
	}
	if b := byModel["review-model-b"]; b["after_outcome"] != "approve" || b["with_findings"] != 0 {
		t.Fatalf("review model b: %v", b)
	}
}

func TestTL1APIAndMCP(t *testing.T) {
	catalog, config := testCatalog(t)
	ingestTL1Fixture(t, catalog, 4)
	server := testServer(t, config, catalog)
	get := func(path string) map[string]any {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer test-token")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, response.Code, response.Body.String())
		}
		value := map[string]any{}
		_ = json.Unmarshal(response.Body.Bytes(), &value)
		return value
	}
	if installations := get("/api/tl1")["installations"].([]any); len(installations) != 1 {
		t.Fatalf("installations: %v", installations)
	}
	if _, ok := get("/api/tl1/overview?days=0")["findings"].([]any); !ok {
		t.Fatal("overview should list the findings scoped to TL1 flavors")
	}
	latest := get("/api/tl1/overview?since=latest")
	if window := latest["window"].(map[string]any); window["mode"] != "latest_enqueue" || latest["enqueues"] == nil {
		t.Fatalf("latest enqueue window: %v", window)
	}
	if bounded := get("/api/tl1/overview?since=2026-09-20T00:00:00Z&until=2026-09-20T10:00:00Z"); bounded["window"].(map[string]any)["tasks"] != float64(0) {
		t.Fatalf("no fixture task was created before 10:00: %v", bounded["window"])
	}

	request := httptest.NewRequest(http.MethodPost, "/api/query/tl1_attempts", strings.NewReader(`{"limit":10,"where":[{"field":"disposition","op":"=","value":"error"}]}`))
	request.Header.Set("Authorization", "Bearer test-token")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	var rows struct{ Rows []map[string]any }
	if err := json.Unmarshal(response.Body.Bytes(), &rows); err != nil || response.Code != http.StatusOK || len(rows.Rows) != 5 || rows.Rows[0]["flavor"] != "review" {
		t.Fatalf("tl1_attempts query: %d %s", response.Code, response.Body.String())
	}

	if err := catalog.SetMCPEnabled(true); err != nil {
		t.Fatal(err)
	}
	listed := handleMCP(catalog, map[string]any{"id": 1, "method": "tools/list"})
	names := jsonText(listed)
	for _, tool := range []string{"tl1_overview", "tl1_flavor", "tl1_errors", "tl1_candidate", "list_findings", "get_finding"} {
		if !strings.Contains(names, `"`+tool+`"`) {
			t.Errorf("MCP tools/list is missing %s", tool)
		}
	}
	if strings.Contains(names, `"tl1_detector"`) {
		t.Error("tl1_detector is retired: TL1 concerns are findings")
	}
	value, err := callMCP(catalog, "tl1_overview", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := value.(map[string]any)["findings"]; !ok {
		t.Fatalf("compact overview should list findings: %v", value)
	}
}

// TestTL1EnqueueSpikesMatchTL1 ports TL1's own dashboard test: child tasks do
// not count, a burst needs five root tasks within ten-minute gaps, and the
// latest burst starts the default window.
func TestTL1EnqueueSpikesMatchTL1(t *testing.T) {
	tasks := []*tl1Task{}
	add := func(prefix string, count int, created, createdBy, parent, shape string) {
		for index := 0; index < count; index++ {
			tasks = append(tasks, &tl1Task{ID: fmt.Sprintf("%s-%d", prefix, index), Flavor: "flavor", ShapeVersion: shape, CreatedAt: canonicalTime(created), CreatedBy: createdBy, ParentID: parent})
		}
	}
	add("old", 3, "2026-01-01 08:00:00", "", "", "v1")
	add("new", 6, "2026-01-02 12:00:00", "", "", "v1")
	add("child", 6, "2026-01-03 16:00:00", "new-0", "", "v1")
	add("retry", 6, "2026-01-03 16:00:00", "", "new-1", "v1")
	add("latest", 5, "2026-01-04 09:30:00", "", "", "v2")
	spikes := tl1EnqueueSpikes(tasks)
	if len(spikes) != 2 || spikes[0].StartedAt != "2026-01-04T09:30:00.000Z" || spikes[0].RootTasks != 5 || spikes[1].StartedAt != "2026-01-02T12:00:00.000Z" || spikes[1].RootTasks != 6 {
		t.Fatalf("spikes: %+v", spikes)
	}
	// The flavor's v2 definition first ran in the latest enqueue's period.
	if len(spikes[0].Changed) != 1 || spikes[0].Changed[0] != "flavor" || len(spikes[1].Changed) != 0 || len(spikes[0].ChangedDuring) != 0 {
		t.Fatalf("definition changes: %+v", spikes)
	}
	// A revision partway through a period is reported separately.
	add("late", 1, "2026-01-04 11:00:00", "", "", "v3")
	if spikes := tl1EnqueueSpikes(tasks); len(spikes[0].ChangedDuring) != 1 || len(spikes[0].Changed) != 1 {
		t.Fatalf("mid-period change: %+v", spikes[0])
	}
	// A gap longer than ten minutes splits a burst; four roots are not large.
	gapped := []*tl1Task{}
	for index, minute := range []int{0, 5, 10, 21, 25, 30, 35} {
		gapped = append(gapped, &tl1Task{ID: fmt.Sprint(index), CreatedAt: canonicalTime(fmt.Sprintf("2026-01-05 10:%02d:00", minute))})
	}
	if spikes := tl1EnqueueSpikes(gapped); len(spikes) != 0 {
		t.Fatalf("3 + 4 root tasks split by an 11-minute gap are not large enqueues: %+v", spikes)
	}

	since, until, err := tl1ResolveWindow(tl1Window{Since: tl1LatestEnqueue}, spikes, time.Now())
	if err != nil || since != "2026-01-04T09:30:00.000Z" || until != "" {
		t.Fatalf("latest enqueue window: %q %q %v", since, until, err)
	}
	since, until, err = tl1ResolveWindow(tl1Window{Since: "2026-01-02T12:00:00Z", Until: "2026-01-04T09:30:00Z", Days: 7}, spikes, time.Now())
	if err != nil || since != "2026-01-02T12:00:00.000Z" || until != "2026-01-04T09:30:00.000Z" {
		t.Fatalf("enqueue period window: %q %q %v", since, until, err)
	}
	if _, _, err := tl1ResolveWindow(tl1Window{Since: "yesterday-ish"}, spikes, time.Now()); err == nil {
		t.Fatal("an unparseable bound should be rejected")
	}
}

func mustTL1Flavor(t *testing.T, catalog *Catalog, name string) map[string]any {
	t.Helper()
	flavor, err := catalog.TL1Flavor(tl1Selection{}, name, tl1Window{})
	if err != nil {
		t.Fatal(err)
	}
	return flavor
}

// TL1 runs Antigravity agents through the agy CLI, whose stream-json
// transcript names Antigravity's own conversation. The TL1 conversation reads
// the stream (prompt, replies, tool calls, per-request usage) and links to the
// session the Antigravity source indexes, so the run counts once and both sit
// in the TL1 project.
func TestTL1AgyTranscriptLinksItsAntigravitySession(t *testing.T) {
	catalog, _ := testCatalog(t)
	registry := tl1Fixture(t, 0)
	root := filepath.Dir(registry)
	session := tl1AGYFixture(t, root)
	folder := filepath.Join(root, "transcripts", "task-agy")
	// An attempt can leave several executors' transcripts. Its executor is
	// only a fallback when a transcript's filename does not name one.
	for _, executor := range []string{"codex", "claude", "agy"} {
		adapter := &tl1Adapter{}
		conversation, ok, err := adapter.transcript(tl1Installation{ID: "install-1", Transcripts: filepath.Dir(folder)}, "task-agy",
			map[string]any{"id": "att-agy", "executor": executor, "model": "gpt-5.6-terra"}, "1-agy.jsonl")
		if err != nil || !ok || conversation.Provider != "antigravity" || conversation.Model != "gemini-4-neon" || len(conversation.Messages) == 0 {
			t.Fatalf("AGY transcript under %s attempt: provider=%q model=%q messages=%d ok=%v: %v", executor, conversation.Provider, conversation.Model, len(conversation.Messages), ok, err)
		}
	}
	// The session as Antigravity keeps it, run in TL1's transcripts folder.
	native := filepath.Join(t.TempDir(), "antigravity-cli")
	antigravityTranscript(t, native, session, "transcript_full.jsonl", antigravitySteps(
		map[string]any{"source": "USER_EXPLICIT", "type": "USER_INPUT", "content": "Design the crib.", "created_at": "2026-11-20T22:04:04Z"},
		map[string]any{"source": "MODEL", "type": "PLANNER_RESPONSE", "thinking": "Read the request first.", "content": "All done.", "created_at": "2026-11-20T22:14:06Z"},
	))
	antigravityRequestRows(t, native, session, []any{[]uint64{1}, "gemini-4-neon", 200, 60, 3000, 15})
	// The native source keeps children in the parent's workspace. The TL1
	// stream names the parent alone and does not repeat this child's usage.
	const child = "aaaaaaaa-0000-4000-8000-000000000002"
	antigravityTranscript(t, native, child, "transcript_full.jsonl", antigravitySteps(
		map[string]any{"source": "USER_EXPLICIT", "type": "USER_INPUT", "content": "Check the crib.", "created_at": "2026-11-20T22:05:00Z"},
		map[string]any{"source": "MODEL", "type": "PLANNER_RESPONSE", "content": "Checked.", "created_at": "2026-11-20T22:06:00Z"},
	))
	antigravityRequestRows(t, native, child, []any{[]uint64{1}, "gemini-4-neon", 1000, 100, 0, 0})
	antigravitySummaryRows(t, native,
		[]any{session, "Design the crib", jsonText([]string{"file://" + filepath.Join(folder, ".att-agy-ipc-3o7scs7w")}), "", 0, ""},
		[]any{child, "Check the crib", "[]", session, 1, "checker"})

	ingest := func(source SourceConfig) {
		t.Helper()
		adapter, err := MakeAdapter(source)
		if err != nil {
			t.Fatal(err)
		}
		if result := catalog.Ingest(adapter, nil); result.Error != nil {
			t.Fatalf("ingest %s: %v", source.Kind, result.Error)
		}
	}
	ingest(SourceConfig{Name: "tl1", Kind: "tl1", Path: registry, Account: "local", Enabled: true})
	ingest(SourceConfig{Name: "antigravity-cli", Kind: "antigravity", Path: native, Account: "local", Enabled: true})

	const conversation = "install-1:att-agy:1-agy.jsonl"
	var provider, model, effort, harness, coverage, workspace string
	if err := catalog.DB.QueryRow(`SELECT c.provider,c.model,c.effort,c.harness,c.coverage,c.workspace_id FROM conversations c WHERE c.native_id=?`, conversation).Scan(&provider, &model, &effort, &harness, &coverage, &workspace); err != nil {
		t.Fatal(err)
	}
	// The stream reports only a summary of each tool's output.
	if provider != "antigravity" || model != "gemini-4-neon" || effort != "medium" || harness != "antigravity-cli" || coverage != "partial" {
		t.Fatalf("agy conversation: provider=%q model=%q effort=%q harness=%q coverage=%q", provider, model, effort, harness, coverage)
	}
	rows, err := queryMaps(catalog.DB, `SELECT m.role,m.kind,m.text FROM messages m JOIN conversations c ON c.id=m.conversation_id WHERE c.native_id=? AND m.kind IN ('message','tool_result') ORDER BY m.source_order`, conversation)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, row := range rows {
		got = append(got, firstString(row["role"])+": "+firstString(row["text"]))
	}
	if len(got) != 4 || got[0] != "user: Design the crib." || !strings.Contains(got[1], "3 lines, 120 bytes") || !strings.Contains(got[2], "file does not exist") || got[3] != "assistant: All done." {
		t.Fatalf("agy messages: %q", got)
	}
	var calls, failed int
	if err := catalog.DB.QueryRow(`SELECT COUNT(*),COALESCE(SUM(t.status='error'),0) FROM tool_calls t JOIN conversations c ON c.id=t.conversation_id WHERE c.native_id=?`, conversation).Scan(&calls, &failed); err != nil || calls != 2 || failed != 1 {
		t.Fatalf("tool calls = %d (%d failed), %v", calls, failed, err)
	}
	// Input counts cached input, output counts thinking, and the result's
	// totals fill in what the steps left out.
	var input, cached, output, reasoning int64
	if err := catalog.DB.QueryRow(`SELECT s.input_tokens,s.cache_read_input_tokens,s.output_tokens,s.reasoning_output_tokens
		FROM agent_sessions s JOIN conversations c ON c.id=s.conversation_id WHERE c.native_id=?`, conversation).Scan(&input, &cached, &output, &reasoning); err != nil {
		t.Fatal(err)
	}
	if input != 3200 || cached != 3000 || output != 60 || reasoning != 15 {
		t.Fatalf("agy usage: input=%d cached=%d output=%d reasoning=%d", input, cached, output, reasoning)
	}
	var relationship string
	if err := catalog.DB.QueryRow(`SELECT l.relationship FROM conversation_identity_links l JOIN conversations a ON a.id=l.left_id JOIN conversations b ON b.id=l.right_id
		WHERE (a.native_id=? AND b.native_id=?) OR (a.native_id=? AND b.native_id=?)`, conversation, session, session, conversation).Scan(&relationship); err != nil || relationship != "native-alias" {
		t.Fatalf("TL1 and Antigravity copies not linked: %q %v", relationship, err)
	}
	var nativeWorkspace, nativeProject, taskRepository string
	if err := catalog.DB.QueryRow(`SELECT w.id,COALESCE(w.repository_id,w.folder_repository_id,''),(SELECT COALESCE(repository_id,'') FROM workspaces WHERE id=?)
		FROM workspaces w JOIN conversations c ON c.workspace_id=w.id WHERE c.native_id=?`, workspace, session).Scan(&nativeWorkspace, &nativeProject, &taskRepository); err != nil {
		t.Fatal(err)
	}
	if taskRepository == "" || nativeProject != taskRepository {
		t.Fatalf("Antigravity session's project %q, TL1 task's %q", nativeProject, taskRepository)
	}
	// Count the linked parent once, while retaining native-only children.
	ledger, err := catalog.representativeLedger()
	if err != nil {
		t.Fatal(err)
	}
	childID := stableID("conversation", "antigravity", "local", child)
	childTokens, parentTokens := int64(0), int64(0)
	for _, entry := range ledger {
		if firstString(entry["conversation_id"]) == childID {
			childTokens += integer(entry["total_tokens"])
		} else if firstString(entry["workspace_id"]) == nativeWorkspace {
			t.Fatalf("Antigravity copy counted beside its TL1 task: %v", entry)
		}
		if firstString(entry["workspace_id"]) == workspace {
			parentTokens += integer(entry["total_tokens"])
		}
	}
	if childTokens != 1100 || parentTokens != 3260 {
		t.Fatalf("linked usage: parent=%d child=%d", parentTokens, childTokens)
	}
	// The header summary uses a compact ledger of its own. It must make the
	// same choice as the Usage page, and the child's root stays the TL1 copy.
	summaryLedger, err := catalog.usageSummaryLedger(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var summaryTokens int64
	for _, entry := range summaryLedger {
		if entry.workspaceID == nativeWorkspace || entry.workspaceID == workspace {
			summaryTokens += entry.tokens["total_tokens"]
		}
	}
	if summaryTokens != parentTokens+childTokens {
		t.Fatalf("summary usage=%d, parent+child=%d", summaryTokens, parentTokens+childTokens)
	}
	lineage, err := catalog.usageConversationRoots(t.Context(), ledger)
	if err != nil {
		t.Fatal(err)
	}
	if root := lineage.roots[childID]; firstString(root["workspace_id"]) != workspace {
		t.Fatalf("native child's usage must retain its TL1 root: %v", root)
	}
	overview, err := catalog.TL1Overview(tl1Selection{}, tl1Window{})
	if err != nil {
		t.Fatal(err)
	}
	var agy map[string]any
	for _, row := range overview["coverage"].(map[string]any)["by_executor"].([]map[string]any) {
		if row["executor"] == "agy" {
			agy = row
		}
	}
	if agy["attempts"] != 1 || agy["with_tokens"] != 1 || agy["priced_from_tokens"] != 1 {
		t.Fatalf("agy coverage: %v", agy)
	}
	candidate, err := catalog.TL1Candidate(tl1Selection{}, "cand-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range candidate["tasks"].([]map[string]any) {
		if task["task_id"] == "task-agy" {
			// $3 input, $0.15 cached input and $15 output per million.
			if cost, _ := task["cost_usd"].(float64); math.Abs(cost-(200*3+3000*0.15+60*15)/1e6) > 1e-9 || task["tokens"] != int64(3260) {
				t.Fatalf("agy task cost %v, tokens %v", task["cost_usd"], task["tokens"])
			}
		}
	}
}

// tl1AGYFixture adds to a tl1Fixture installation a design task whose one
// attempt ran on agy, with its stream-json transcript and inputs, and returns
// the Antigravity conversation ID the stream names.
func tl1AGYFixture(t *testing.T, root string) string {
	t.Helper()
	const session = "9ef2ffd2-0000-4000-8000-000000000001"
	execSQL(t, openWritableSQLite(t, filepath.Join(root, "tl1.db")),
		`INSERT INTO task_flavors VALUES ('f-design','design','llm','Design it.','{"agy-gemini-4-neon-medium-medium":{"executor":"agy","model":"gemini-4-neon-medium","effort":"medium"}}','agy-gemini-4-neon-medium-medium','{}','{}','{}',0,'v1')`,
		`INSERT INTO tasks(id,flavor_id,title,status,outcome,candidate_id,shape_version,inputs,created_at,claimed_at,completed_at,updated_at) VALUES ('task-agy','f-design','Design the crib','done','ready','cand-1','v1','{}','2026-11-20 22:04:00','2026-11-20 22:04:00','2026-11-20 22:14:29','2026-11-20 22:14:29')`,
		`INSERT INTO task_attempts VALUES ('att-agy','task-agy',1,'wt-2','agy','gemini-4-neon-medium','medium','agy-gemini-4-neon-medium-medium','agy','succeeded',NULL,'2026-11-20 22:04:01','2026-11-20 22:14:29')`)
	usage := func(input, output, thinking, cache int) string {
		return fmt.Sprintf(`{"input_tokens":%d,"output_tokens":%d,"thinking_tokens":%d,"cache_read_tokens":%d,"total_tokens":%d}`, input, output, thinking, cache, input+output)
	}
	step := func(index int, state, kind, rest string) string {
		return fmt.Sprintf(`{"event":"step_update","step_update":{"conversation_id":%q,"step_index":%d,"state":%q,"step_type":%q%s}}`, session, index, state, kind, rest)
	}
	folder := filepath.Join(root, "transcripts", "task-agy")
	writeSourceFile(t, filepath.Join(folder, "1-agy.jsonl"), strings.Join([]string{
		fmt.Sprintf(`{"event":"init","conversation_id":%q,"init":{"model":"gemini-4-neon-medium","cwd":"/repo/.candidate-worktrees/c1","tools":["run_command","read_file"]}}`, session),
		step(0, "DONE", "user_input", ""),
		step(1, "DONE", "agent_response", `,"usage":`+usage(100, 40, 10, 1000)),
		step(2, "ACTIVE", "tool", `,"tool_name":"run_command","tool_info":{"name":"run_command","parameters":{"CommandLine":"make test"}}`),
		step(2, "DONE", "tool", `,"tool_name":"run_command","tool_info":{"name":"run_command","parameters":{"CommandLine":"make test"},"output":"3 lines, 120 bytes"}`),
		step(3, "ERROR", "tool", `,"tool_name":"read_file","tool_info":{"name":"read_file","parameters":{"Path":"/repo/missing.go"},"error":{"type":"TOOL_ERROR","message":"file does not exist"}}`),
		step(4, "ACTIVE", "agent_response", `,"text_delta":"All "`),
		step(4, "DONE", "agent_response", `,"text_delta":"done.","usage":`+usage(50, 20, 5, 2000)),
		step(5, "DONE", "system_message", ""),
		// The run's totals cover a request no step reported.
		fmt.Sprintf(`{"event":"result","result":{"conversation_id":%q,"status":"SUCCESS","response":"All done.","duration_seconds":628,"num_turns":1,"usage":%s}}`, session, usage(200, 60, 15, 3000)),
	}, "\n")+"\n")
	writeSourceFile(t, filepath.Join(folder, "1-agy-inputs.ndjson"), strings.Join([]string{
		`{"event":"launch_requested","format":"tl1.invocation/v1","observed_at":"2026-11-20T22:04:01.702299Z","stdin_text":"{\"event\": \"user\", \"message\": {\"content\": \"Design the crib.\"}}"}`,
		`{"event":"process_completed","format":"tl1.invocation/v1","observed_at":"2026-11-20T22:14:29.086Z","exit_code":0}`,
	}, "\n")+"\n")
	return session
}
