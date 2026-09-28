package archive

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gbdubs/pharos/internal/querytable"
)

func TestParseShellCommand(t *testing.T) {
	cases := []struct {
		line, program, subcommand, category string
		segments                            int
		pipe, redirect, heredoc             bool
	}{
		{"cd /tmp/repo && git -C sub status --short | head -5", "git", "status", "vcs", 3, true, false, false},
		{"FOO=1 timeout 30 go test ./... 2>&1", "go", "test", "test", 1, false, false, false},
		{"npm run build > build.log", "npm", "run build", "build", 1, false, true, false},
		{"python3 -m pytest -q tests", "python3", "-m pytest", "test", 1, false, false, false},
		{"sed -i '' 's/a/b/' file.go", "sed", "", "file-write", 1, false, false, false},
		{"sed -n '1,20p' file.go", "sed", "", "read", 1, false, false, false},
		{"echo '---' ; rg -n 'a|b' src", "rg", "", "search", 2, false, false, false},
		{"git commit -m \"$(cat <<'EOF'\nFix it; don't break\n\nCo-Authored-By: x\nEOF\n)\" && git push", "git", "commit", "vcs", 2, false, false, true},
		{"cat > out.txt <<EOF\nline one && two\nEOF\nls -la", "cat", "", "read", 2, false, true, true},
		{"sudo -n /usr/bin/lsof -i :3000 &", "lsof", "", "process", 1, false, false, false},
	}
	for _, test := range cases {
		parsed := parseShellCommand(test.line)
		primary := parsed.primary()
		if primary.Program != test.program || primary.Subcommand != test.subcommand || primary.Category != test.category {
			t.Errorf("%q: primary %q/%q/%q, want %q/%q/%q", test.line, primary.Program, primary.Subcommand, primary.Category, test.program, test.subcommand, test.category)
		}
		if len(parsed.Segments) != test.segments {
			t.Errorf("%q: %d segments %#v, want %d", test.line, len(parsed.Segments), parsed.Segments, test.segments)
		}
		if parsed.HasPipe != test.pipe || parsed.HasRedirect != test.redirect || parsed.HasHeredoc != test.heredoc {
			t.Errorf("%q: pipe/redirect/heredoc %v/%v/%v", test.line, parsed.HasPipe, parsed.HasRedirect, parsed.HasHeredoc)
		}
	}
	if !parseShellCommand("sleep 10 &").IsBackgrounded || parseShellCommand("go test 2>&1").IsBackgrounded {
		t.Fatal("background detection")
	}
}

func writeFixture(t *testing.T, root, name string, lines []string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func ingestFixture(t *testing.T, catalog *Catalog, kind, root string) {
	t.Helper()
	adapter, err := MakeAdapter(SourceConfig{Name: kind, Kind: kind, Path: root, Account: "local", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if result := catalog.Ingest(adapter, nil); result.Error != nil {
		t.Fatal(result.Error)
	}
}

func toolCallsByProgram(t *testing.T, catalog *Catalog) map[string]map[string]any {
	t.Helper()
	rows, err := queryMaps(catalog.DB, "SELECT * FROM tool_calls ORDER BY sequence")
	if err != nil {
		t.Fatal(err)
	}
	calls := map[string]map[string]any{}
	for _, row := range rows {
		calls[firstString(row["program"], row["tool_name"])] = row
	}
	return calls
}

var claudeToolFixture = []string{
	`{"type":"user","uuid":"u1","sessionId":"claude-tools","cwd":"/tmp/repo","timestamp":"2026-09-20T10:00:00.000Z","message":{"role":"user","content":"check the repo"}}`,
	`{"type":"assistant","uuid":"a1","parentUuid":"u1","sessionId":"claude-tools","timestamp":"2026-09-20T10:00:05.000Z","message":{"id":"msg_1","role":"assistant","model":"claude-opus-5","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"cd /tmp/repo && git status --short | head -5"}}],"usage":{"input_tokens":10,"cache_read_input_tokens":1000,"cache_creation_input_tokens":0,"output_tokens":50}}}`,
	`{"type":"user","uuid":"u2","parentUuid":"a1","sessionId":"claude-tools","timestamp":"2026-09-20T10:00:07.500Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"M a.go\nM b.go","is_error":false}]},"toolUseResult":{"stdout":"M a.go\nM b.go","stderr":"","interrupted":false,"isImage":false}}`,
	`{"type":"assistant","uuid":"a2","parentUuid":"u2","sessionId":"claude-tools","timestamp":"2026-09-20T10:00:09.000Z","message":{"id":"msg_2","role":"assistant","model":"claude-opus-5","content":[{"type":"tool_use","id":"toolu_2","name":"Bash","input":{"command":"go test ./..."}}],"usage":{"input_tokens":10,"cache_read_input_tokens":1150,"cache_creation_input_tokens":0,"output_tokens":30}}}`,
	`{"type":"user","uuid":"u3","parentUuid":"a2","sessionId":"claude-tools","timestamp":"2026-09-20T10:00:21.000Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_2","content":"Exit code 1\n--- FAIL: TestThing","is_error":true}]},"toolUseResult":"Error: Exit code 1"}`,
	`{"type":"assistant","uuid":"a3","parentUuid":"u3","sessionId":"claude-tools","timestamp":"2026-09-20T10:00:25.000Z","message":{"id":"msg_3","role":"assistant","model":"claude-opus-5","content":[{"type":"tool_use","id":"toolu_3","name":"Edit","input":{"file_path":"/tmp/repo/a.go","old_string":"x","new_string":"y"}}],"usage":{"input_tokens":10,"cache_read_input_tokens":1290,"cache_creation_input_tokens":0,"output_tokens":20}}}`,
	`{"type":"user","uuid":"u4","parentUuid":"a3","sessionId":"claude-tools","timestamp":"2026-09-20T10:00:26.000Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_3","content":"The user doesn't want to proceed with this tool use.","is_error":true}]}}`,
	`{"type":"assistant","uuid":"a4","parentUuid":"u4","sessionId":"claude-tools","timestamp":"2026-09-20T10:00:30.000Z","message":{"id":"msg_4","role":"assistant","model":"claude-opus-5","content":[{"type":"text","text":"Stopped."}],"usage":{"input_tokens":10,"cache_read_input_tokens":1340,"cache_creation_input_tokens":0,"output_tokens":5}}}`,
}

func TestClaudeToolLedger(t *testing.T) {
	catalog, _ := testCatalog(t)
	root := t.TempDir()
	writeFixture(t, root, "project/claude-tools.jsonl", claudeToolFixture)
	ingestFixture(t, catalog, "claude", root)
	calls := toolCallsByProgram(t, catalog)
	git, gotest, edit := calls["git"], calls["go"], calls["Edit"]
	if git == nil || gotest == nil || edit == nil || len(calls) != 3 {
		t.Fatalf("calls: %#v", calls)
	}
	if git["subcommand"] != "status" || git["command_category"] != "vcs" || integer(git["has_pipe"]) != 1 || integer(git["command_count"]) != 3 {
		t.Fatalf("git call: %#v", git)
	}
	if git["status"] != "ok" || integer(git["duration_ms"]) != 2500 || git["duration_source"] != "timestamps" {
		t.Fatalf("git outcome: %#v", git)
	}
	// msg_2's prompt (1160) grew by 100 over msg_1's prompt (1010) plus output (50).
	if integer(git["result_tokens"]) != 100 || git["result_tokens_source"] != "measured" {
		t.Fatalf("git context: %#v", git)
	}
	// msg_2, msg_3, and msg_4 all re-read the result.
	if integer(git["carried_requests"]) != 3 || integer(git["carried_tokens"]) != 300 || integer(git["output_tokens"]) != 50 {
		t.Fatalf("git carry: %#v", git)
	}
	if gotest["subcommand"] != "test" || gotest["command_category"] != "test" || gotest["status"] != "error" || gotest["error_type"] != "nonzero_exit" || integer(gotest["exit_code"]) != 1 {
		t.Fatalf("go test call: %#v", gotest)
	}
	if integer(gotest["test_failure"]) != 1 || !strings.Contains(firstString(gotest["error_signature"]), "FAIL:") {
		t.Fatalf("go test failure fields: %#v", gotest)
	}
	if integer(gotest["result_tokens"]) != 110 || integer(gotest["duration_ms"]) != 12000 {
		t.Fatalf("go test tokens/duration: %#v", gotest)
	}
	if edit["error_type"] != "user_rejected" || edit["tool_category"] != "edit" || edit["file_path"] != "/tmp/repo/a.go" {
		t.Fatalf("edit call: %#v", edit)
	}
	var requests, growth int64
	if err := catalog.DB.QueryRow("SELECT COUNT(*),SUM(COALESCE(context_growth_tokens,0)) FROM model_requests").Scan(&requests, &growth); err != nil {
		t.Fatal(err)
	}
	if requests != 4 || growth != 100+110+30 {
		t.Fatalf("requests %d growth %d", requests, growth)
	}
	var commands int
	if err := catalog.DB.QueryRow("SELECT COUNT(*) FROM tool_commands WHERE program='head'").Scan(&commands); err != nil || commands != 1 {
		t.Fatalf("tool commands: %d %v", commands, err)
	}
}

func TestCodexExecToolLedger(t *testing.T) {
	catalog, _ := testCatalog(t)
	root := t.TempDir()
	writeFixture(t, root, "sessions/2026/09/20/rollout-codex-tools.jsonl", []string{
		`{"type":"session_meta","timestamp":"2026-09-20T10:00:00Z","payload":{"id":"codex-tools","cwd":"/tmp/repo","model":"gpt-5.5"}}`,
		`{"type":"response_item","timestamp":"2026-09-20T10:00:01Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"find foo"}]}}`,
		`{"type":"response_item","timestamp":"2026-09-20T10:00:02Z","payload":{"type":"custom_tool_call","name":"exec","call_id":"call_1","input":"const cmds=[\"pwd\",\"rg -n foo .\"]; for (const cmd of cmds) await tools.exec_command({cmd})"}}`,
		`{"type":"token_usage_record","timestamp":"2026-09-20T10:00:02.100Z","payload":{"response_id":"r1","usage":{"input_tokens":1000,"cached_input_tokens":0,"output_tokens":40,"total_tokens":1040},"thread_token_usage":{"total_tokens":1040}}}`,
		`{"type":"event_msg","timestamp":"2026-09-20T10:00:02.200Z","payload":{"type":"item_completed","item":{"type":"CommandExecution","id":"exec-1","command":["/bin/zsh","-lc","pwd"],"exit_code":0,"status":"completed","duration":{"secs":0,"nanos":5000000}}}}`,
		`{"type":"event_msg","timestamp":"2026-09-20T10:00:03.200Z","payload":{"type":"item_completed","item":{"type":"CommandExecution","id":"exec-2","command":["/bin/zsh","-lc","rg -n foo ."],"exit_code":1,"status":"completed","duration":{"secs":1,"nanos":0}}}}`,
		`{"type":"response_item","timestamp":"2026-09-20T10:00:03.300Z","payload":{"type":"custom_tool_call_output","call_id":"call_1","output":[{"type":"input_text","text":"Script completed\nWall time 1.2 seconds\nOutput:\n"},{"type":"input_text","text":"/tmp/repo"}]}}`,
		`{"type":"token_usage_record","timestamp":"2026-09-20T10:00:05Z","payload":{"response_id":"r2","usage":{"input_tokens":1100,"cached_input_tokens":1000,"output_tokens":10,"total_tokens":1110},"thread_token_usage":{"total_tokens":2150}}}`,
		`{"type":"response_item","timestamp":"2026-09-20T10:00:05.100Z","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"No matches."}]}}`,
	})
	ingestFixture(t, catalog, "codex", root)
	calls := toolCallsByProgram(t, catalog)
	call := calls["rg"]
	if call == nil || len(calls) != 1 {
		t.Fatalf("calls: %#v", calls)
	}
	// rg exiting 1 means "no matches", not a failure.
	if call["status"] != "ok" || call["command_category"] != "search" || integer(call["command_count"]) != 2 {
		t.Fatalf("exec call: %#v", call)
	}
	if integer(call["duration_ms"]) != 1200 || call["duration_source"] != "reported" || integer(call["result_tokens"]) != 60 || call["model"] != "gpt-5.5" {
		t.Fatalf("exec measures: %#v", call)
	}
	commands, err := queryMaps(catalog.DB, "SELECT * FROM tool_commands ORDER BY position")
	if err != nil || len(commands) != 2 {
		t.Fatalf("commands: %#v %v", commands, err)
	}
	if commands[1]["operator"] != "script" || integer(commands[1]["exit_code"]) != 1 || integer(commands[1]["duration_ms"]) != 1000 {
		t.Fatalf("second command: %#v", commands[1])
	}
}

func TestToolTablesQueryInSQL(t *testing.T) {
	catalog, _ := testCatalog(t)
	root := t.TempDir()
	writeFixture(t, root, "project/claude-tools.jsonl", claudeToolFixture)
	ingestFixture(t, catalog, "claude", root)
	ctx := context.Background()
	if err := catalog.ensureToolRollup(ctx); err != nil {
		t.Fatal(err)
	}
	load := func(name string) querytable.Schema {
		document, err := querySchemaDocument(name)
		if err != nil {
			t.Fatal(err)
		}
		schema, err := querytable.LoadSchema(document)
		if err != nil {
			t.Fatal(err)
		}
		return schema
	}
	tools, calls := load("tools"), load("tool_calls")
	summary, err := toolRollupDataset.Rows(ctx, catalog.DB, querytable.Query{
		Where:   []querytable.WhereTerm{{Field: "tool_name", Op: "=", Value: "Bash"}},
		OrderBy: []querytable.OrderBy{{Field: "call_count", Dir: "desc"}}, Limit: 10}, tools)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Total != 2 || len(summary.Rows) != 2 {
		t.Fatalf("summary rows: %#v", summary)
	}
	for _, row := range summary.Rows {
		if row["day"] != "2026-09-20" && row["day"] == nil {
			t.Fatalf("day bucket: %#v", row)
		}
		if row["model_family"] != "opus-5" || row["tool_cost_usd"] == nil {
			t.Fatalf("summary row: %#v", row)
		}
	}
	aggregate, err := toolRollupDataset.Aggregate(ctx, catalog.DB, querytable.AggregationRequest{Aggregations: []querytable.Aggregation{
		{ID: "errors", Op: "sum", Field: "error_count", GroupBy: []string{"tool_name"}},
		{ID: "calls", Op: "sum", Field: "call_count"},
	}}, tools)
	if err != nil {
		t.Fatal(err)
	}
	if len(aggregate.Metrics) != 2 || len(aggregate.Metrics[0].Buckets) != 2 || aggregate.Metrics[1].Buckets[0].Value != 3.0 {
		t.Fatalf("aggregations: %#v", aggregate)
	}
	failures, err := toolCallDataset.Rows(ctx, catalog.DB, querytable.Query{Where: []querytable.WhereTerm{
		{Any: []querytable.WhereClause{{Field: "error_type", Op: "=", Value: "nonzero_exit"}, {Field: "command", Op: "contains", Value: "GIT STATUS"}}},
		{Field: "command", Op: "matches_regex", Value: `^(go|cd) `},
	}, Limit: 10}, calls)
	if err != nil {
		t.Fatal(err)
	}
	if failures.Total != 2 {
		t.Fatalf("call filter: %#v", failures)
	}
	negated, err := toolCallDataset.Rows(ctx, catalog.DB, querytable.Query{Where: []querytable.WhereTerm{{Field: "program", Op: "=", Value: "git", Negated: true}}, Limit: 10}, calls)
	if err != nil || negated.Total != 1 {
		t.Fatalf("negation keeps only non-null non-matching values: %#v %v", negated, err)
	}
	distinct, err := toolCallDataset.Distinct(ctx, catalog.DB, "program", "gi", 10, calls)
	if err != nil || strings.Join(distinct.Values, ",") != "git" || !distinct.HasNull {
		t.Fatalf("distinct: %#v %v", distinct, err)
	}
	stats, err := toolCallDataset.FieldStats(ctx, catalog.DB, []string{"program", "duration_ms", "tool_cost_usd", "program"}, calls)
	if err != nil || stats["program"].Distinct == nil || *stats["program"].Distinct != 2 || stats["program"].Min != nil || stats["duration_ms"].Min == nil {
		t.Fatalf("field stats: %#v %v", stats, err)
	}
	if _, priced := stats["tool_cost_usd"]; priced {
		t.Fatalf("field stats reported a field priced after paging: %#v", stats)
	}
	if _, err := toolCallDataset.FieldStats(ctx, catalog.DB, []string{"secret"}, calls); err == nil {
		t.Fatal("field stats accepted an unknown field")
	}
	detail, err := catalog.toolCallDetail(ctx, firstString(failures.Rows[0]["id"]))
	if err != nil || detail == nil || detail["result_text"] == "" || detail["request"] == nil {
		t.Fatalf("detail: %#v %v", detail, err)
	}
}

func TestToolLedgerBackfillFromStoredMessages(t *testing.T) {
	catalog, _ := testCatalog(t)
	root := t.TempDir()
	writeFixture(t, root, "project/claude-tools.jsonl", claudeToolFixture)
	ingestFixture(t, catalog, "claude", root)
	if _, err := catalog.DB.Exec("DELETE FROM tool_calls; DELETE FROM model_requests; DELETE FROM tool_ledger_state"); err != nil {
		t.Fatal(err)
	}
	built, err := catalog.BackfillToolLedger(context.Background(), nil)
	if err != nil || built != 1 {
		t.Fatalf("backfill: %d %v", built, err)
	}
	calls := toolCallsByProgram(t, catalog)
	if len(calls) != 3 || integer(calls["git"]["result_tokens"]) != 100 || calls["go"]["error_type"] != "nonzero_exit" {
		t.Fatalf("backfilled calls: %#v", calls)
	}
	status, err := catalog.ToolLedgerStatus(context.Background())
	if err != nil || integer(status["pending_conversations"]) != 0 || integer(status["tool_calls"]) != 3 {
		t.Fatalf("status: %#v %v", status, err)
	}
}

func TestShellSubstitutions(t *testing.T) {
	cases := []struct {
		line, program, sub string
		segments           int
	}{
		{`task_tmpdir=$(mktemp -d /tmp/x.XXXX) && mkdir -p "$task_tmpdir/a" && go test ./...`, "go", "test", 3},
		{`resource_dir=$(mktemp -d) object_dir=$(mktemp -d)`, "mktemp", "", 1},
		{`resource_dir=$(mktemp -d) object_dir=$(mktemp -d) cargo test`, "cargo", "test", 1},
		{`SHA=$(git rev-parse HEAD) && go build`, "go", "build", 2},
		{`TOK=$(curl -sS -X POST "$ISS/t" | jq -r .access_token)`, "curl", "", 1},
		{"X=`date +%s`", "date", "", 1},
		{`n=$((n+1))`, "", "", 1},
		{`for k in a b; do c=$(grep -c "^$k$" f); done`, "grep", "", 3},
		{`X="$(jq -r '.a' f)"`, "jq", "", 1},
		{`export P=$(gcloud secrets versions access latest)`, "export", "", 1},
		{`echo "GOROOT=$(go env GOROOT)"`, "echo", "", 1},
		{`(cd sub && go test ./...)`, "go", "test", 2},
		{`DSN=$(sops --decrypt f 2>/dev/null | python3 -c 'print(1)')`, "sops", "", 1},
	}
	for _, test := range cases {
		parsed := parseShellCommand(test.line)
		got := parsed.primary()
		if len(parsed.Segments) != test.segments || got.Program != test.program || got.Subcommand != test.sub || strings.HasPrefix(got.Program, "-") {
			t.Errorf("%q: primary %q %q, %d segments: %#v", test.line, got.Program, got.Subcommand, len(parsed.Segments), parsed.Segments)
		}
	}
}

func TestToolErrorSignatureAndTestFailure(t *testing.T) {
	for _, test := range []struct{ input, want string }{
		{"Chunk ID: x\nWall time: 0.1 seconds\nProcess exited with code 1\nOriginal token count: 9\nOutput:\nTraceback (most recent call last):\nUnicodeDecodeError: 'utf-8' codec can't decode byte 0x8b", "UnicodeDecodeError:"},
		{"Script failed\nWall time: 0.2 seconds\nOutput:\nconst err = new Error(message);\nError: Stream closed", "Error: Stream closed"},
		{"<tool_use_error>Blocked: sleep 10 followed by command</tool_use_error>", "Blocked:"},
	} {
		got := toolErrorSignature(test.input)
		if !strings.Contains(got, test.want) {
			t.Errorf("%q: %q, want %q", test.input, got, test.want)
		}
	}
	if got := toolErrorSignature("UnicodeDecodeError: 'utf-8' codec can't decode byte 0x8b"); !strings.Contains(got, "utf-<n>") || !strings.Contains(got, "0x<id>") {
		t.Fatalf("codec signature: %q", got)
	}
	code := int64(1)
	call := toolCall{Status: "error", ExitCode: &code, CommandCategory: "test"}
	if !toolTestFailure(&call, "failed") {
		t.Fatal("nonzero test run")
	}
	call.Status = "ok"
	call.ExitCode = nil
	if !toolTestFailure(&call, "--- FAIL: TestA") {
		t.Fatal("masked test failure")
	}
	if toolTestFailure(&call, "PASS") {
		t.Fatal("passing test")
	}
	for _, subcommand := range []string{"attestation", "latest"} {
		call.CommandCategory = ""
		call.Commands = []toolCommand{{Subcommand: subcommand}}
		if toolTestFailure(&call, "--- FAIL: TestA") {
			t.Fatalf("non-test subcommand %q", subcommand)
		}
	}
	call.Commands = []toolCommand{{Subcommand: "run test:e2e"}}
	if !toolTestFailure(&call, "--- FAIL: TestA") {
		t.Fatal("test script subcommand")
	}
}

func TestRepoRelativePath(t *testing.T) {
	roots := []repoRoot{{"/Users/a/src/pharos", "pharos"}, {"/Users/a/src/other", "other"}}
	cases := []struct{ path, cwd, rel, repo, scope string }{
		{"/Users/a/src/pharos/a.go", "", "a.go", "pharos", "repo"},
		{"a.go", "/Users/a/src/other", "a.go", "other", "repo"},
		{"/Users/a/src/pharos/.conductor/x/a.go", "", "a.go", "pharos", "repo"},
		{"/Users/a/src/pharos/.conductor/x/.claude/worktrees/agent-y/a.go", "", "a.go", "pharos", "repo"},
		{"/Users/a/conductor/workspaces/pharos/karachi/.claude/worktrees/agent-1/x.go", "", "x.go", "pharos", "repo"},
		{"/Users/a/src/pharos/.task-worktrees/x/a.go", "", "a.go", "pharos", "repo"},
		{"/Users/a/src/pharos/.codex/worktrees/x/y/a.go", "", "a.go", "pharos", "repo"},
		{"/Users/a/conductor/workspaces/pharos/karachi/a.go", "", "a.go", "pharos", "repo"},
		{"/tmp/x/a.go", "", "", "", "temp"},
		{"/Users/a/src/pharos", "", ".", "pharos", "repo"},
		{"/Users/a/.claude/projects/x.jsonl", "", "", "", "external"},
		{"/opt/share/file", "", "", "", "external"},
	}
	home, _ := os.UserHomeDir()
	if _, _, scope := repoRelativePath(filepath.Join(home, ".claude", "projects", "x"), "", roots); scope != "agent_home" {
		t.Fatalf("agent home scope: %q", scope)
	}
	for _, test := range cases {
		rel, repo, scope := repoRelativePath(test.path, test.cwd, roots)
		if rel != test.rel || repo != test.repo || scope != test.scope {
			t.Errorf("%s: %q %q %q", test.path, rel, repo, scope)
		}
	}
}

func TestHarnessFailuresOverrideCommandExit(t *testing.T) {
	for _, test := range []struct{ content, kind string }{
		{"Tool permission request failed: Error: Stream closed", "harness_error"},
		{"<tool_use_error>Tool permission request failed: Error: Stream closed</tool_use_error>", "harness_error"},
		{"<tool_use_error>Blocked: sleep 10 followed by another command</tool_use_error>", "hook_blocked"},
		{"Exit code 1\nBlocked: 2 tests failed", "nonzero_exit"},
		{"Blocked: 2", "nonzero_exit"},
		{"Exit code 1\nCORS request blocked: origin denied", "nonzero_exit"},
		{"Error: Stream closed", "nonzero_exit"},
		{"Exit code 1\ngrpc transport: Error: Stream closed", "nonzero_exit"},
	} {
		call := toolCall{Category: "command", Program: "bash", resultIndex: 1}
		classifyToolOutcome(&call, test.content, true, nil)
		if call.Status != "error" || call.ErrorType != test.kind {
			t.Errorf("%q: %s/%s", test.content, call.Status, call.ErrorType)
		}
	}
}

func TestLedgerSubstitutionPrograms(t *testing.T) {
	lines := []string{
		`task_tmpdir=$(mktemp -d /tmp/x.XXXX) && go test ./...`,
		`TOK=$(curl -sS -X POST "$ISS/t" | jq -r .access_token)`,
		`DSN=$(sops --decrypt f 2>/dev/null | python3 -c 'print(1)')`,
	}
	messages := []MessageRecord{}
	for i, line := range lines {
		id := strconv.Itoa(i)
		input, _ := json.Marshal(map[string]any{"tool": "exec_command", "input": map[string]any{"cmd": line}})
		output, _ := json.Marshal(map[string]any{"content": "ok", "is_error": false})
		messages = append(messages, MessageRecord{Kind: "tool_call", NativeID: "call" + id, CallID: id, Text: string(input)}, MessageRecord{Kind: "tool_result", NativeID: "result" + id, CallID: id, Text: string(output)})
	}
	_, calls := buildToolLedger(messages, "")
	if len(calls) != len(lines) {
		t.Fatalf("%d calls", len(calls))
	}
	if calls[0].Program != "go" || calls[1].Program != "curl" || calls[2].Program != "sops" {
		t.Fatalf("primary programs: %q %q %q", calls[0].Program, calls[1].Program, calls[2].Program)
	}
	for _, call := range calls {
		for _, cmd := range call.Commands {
			if strings.HasPrefix(cmd.Program, "-") {
				t.Errorf("bad program %q in %q", cmd.Program, cmd.Command)
			}
		}
	}
}

func TestUnwrapToolOutput(t *testing.T) {
	input := "Chunk ID: x\nWall time: 1 seconds\nProcess exited with code 1\nOriginal token count: 5\nOutput:\n<tool_use_error>Error: failed</tool_use_error>\nScript failed\nOutput:\nlast line"
	got := unwrapToolOutput(input)
	if got != "Error: failed\nlast line" {
		t.Fatalf("unwrapped %q", got)
	}
	jsonResult := `{"content":[{"type":"input_text","text":"Script completed\nOutput:\nError: broken"}]}`
	if got = unwrapToolOutput(jsonResult); got != "Error: broken" {
		t.Fatalf("JSON unwrapped %q", got)
	}
}
