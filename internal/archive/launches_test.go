package archive

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestHeadlessProgram(t *testing.T) {
	for _, test := range []struct{ program, subcommand, command, want string }{
		{"claude", "Label these cells", `timeout 120 claude -p "Label these cells" --model opus`, "claude"},
		{"claude", "$m", `claude --model "$m" --print`, "claude"},
		{"claude", "", `/Applications/Tools/bin/claude -p`, "claude"},
		{"claude", "", "claude --version", ""},
		{"claude", "", "command -v claude", ""},
		{"agy", "", `agy --prompt "Reply with OK" --model flash`, "agy"},
		{"agy", "", `agy --prompt-interactive "Start here"`, ""},
		{"agy", "", `agy -i "Start here"`, ""},
		{"codex", "exec", "codex exec --json -s read-only -", "codex"},
		{"codex", "", "codex --version", ""},
		{"gemini", "", "gemini -p hello", ""},
	} {
		if got := headlessProgram(test.program, test.subcommand, test.command); got != test.want {
			t.Errorf("headlessProgram(%q) = %q, want %q", test.command, got, test.want)
		}
	}
}

// A headless run is linked to the conversation whose command launched it: a
// command naming the agent that was running when the run started, or the
// command that started, moments before, the stream of runs it belongs to.
func TestAuthorshipLinksHeadlessRunsToLaunchingCommands(t *testing.T) {
	catalog, _ := testCatalog(t)
	if err := catalog.Initialize(); err != nil {
		t.Fatal(err)
	}
	prompt := "Label every cell in this workbook with the vocabulary below and reply with one line per cell"
	user := func(id, text, at string) MessageRecord {
		return MessageRecord{NativeID: id, Role: "user", Kind: "message", Text: text, CreatedAt: at, Selected: true}
	}
	work := func(id, provider, harness, location, at string, messages ...MessageRecord) WorkspaceRecord {
		kind := provider
		if harness == "conductor" {
			kind = "conductor"
		}
		return WorkspaceRecord{SourceID: id, SourceKind: kind, Account: "local", Title: id, Location: location, Conversations: []ConversationRecord{{
			NativeID: id, Provider: provider, Account: "local", Coverage: "complete", Harness: harness, StartedAt: at, Messages: messages}}}
	}
	run := func(id, provider, harness, location, at, text string) WorkspaceRecord {
		prompt := user(id+"-prompt", text, at)
		if harness == "codex/codex_exec" {
			prompt.Sender = "automation:codex-exec"
		}
		return work(id, provider, harness, location, at, prompt)
	}
	at := func(clock string) string {
		if !strings.Contains(clock, ".") {
			clock += ".000"
		}
		return "2026-09-01T" + clock + "Z"
	}
	records := []WorkspaceRecord{
		work("labeler", "claude", "claude-code/sdk-ts", "/work/labeler", at("12:00:00"),
			user("ask", "Run the labeling experiment on three models", at("12:00:00")),
			MessageRecord{NativeID: "call-a", Role: "assistant", Kind: "tool_call", CallID: "call-a", Text: `{"tool":"Bash"}`, CreatedAt: at("12:10:00"), Selected: true}),
		// Conductor's record of the same session: it claims the session and
		// shows this Mac's Conductor is read.
		work("labeler-conductor", "claude", "conductor", "/work/labeler", at("12:00:00")),
		work("other", "claude", "claude-code/cli", "/work/other", at("11:00:00")),
		// A command naming the agent was running.
		run("named", "claude", "claude-code/sdk-ts", "/private/tmp/a", at("12:10:05"), prompt),
		run("named-agy", "antigravity", "antigravity-cli", "/private/tmp/b", at("12:20:00"), prompt),
		// A script put in the background: its runs arrive as one stream.
		run("script-1", "claude", "claude-code/sdk-ts", "/private/tmp/script", at("12:40:05"), prompt),
		run("script-2", "codex", "codex/codex_exec", "/private/tmp/script", at("12:43:00"), prompt),
		run("script-3", "antigravity", "antigravity-cli", "/private/tmp/script", at("12:50:00"), prompt),
		// Two conversations started commands just before.
		run("ambiguous", "claude", "claude-code/sdk-ts", "/private/tmp/ambiguous", at("13:00:10"), prompt),
		// In another work's folder: an app there may have started it.
		run("elsewhere", "codex", "codex/codex_exec", "/work/other", at("13:20:10"), prompt),
		// In another work's folder, which the command names.
		run("named-folder", "claude", "claude-code/sdk-ts", "/work/other/.candidate/1", at("13:40:10"), prompt),
		// The command started too long before.
		run("late", "claude", "claude-code/sdk-ts", "/private/tmp/late", at("14:00:40"), prompt),
		// Another conversation's command began just after the run, which
		// Claude records to the millisecond.
		run("after", "claude", "claude-code/sdk-ts", "/private/tmp/after", at("15:00:00.500"), prompt),
		// Two conversations' commands were running; one names the agent.
		run("probe", "antigravity", "antigravity-cli", "/private/tmp/probe", at("15:30:10"), prompt),
		// Another Mac, whose Conductor is not read; an interactive terminal; a
		// session Conductor started.
		run("laptop", "claude", "claude-code/sdk-ts", "/private/tmp/a", at("12:10:40"), "Summarize the release notes"),
		run("terminal", "claude", "claude-code/cli", "/private/tmp/a", at("12:10:10"), "Why is the build slow today"),
		run("conductor-session", "claude", "claude-code/sdk-ts", "/work/session", at("12:10:20"), "Rename the settings page"),
		work("conductor-session-mirror", "claude", "conductor", "/work/session", at("12:10:20")),
	}
	for _, record := range records {
		tx, err := catalog.DB.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := ingestWorkspace(tx, record, false); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	ids := map[string]string{}
	rows, err := queryMaps(catalog.DB, "SELECT id,native_id FROM conversations")
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		ids[firstString(row["native_id"])] = firstString(row["id"])
	}
	callMessage, err := queryMaps(catalog.DB, "SELECT id FROM messages WHERE native_id='call-a'")
	if err != nil || len(callMessage) != 1 {
		t.Fatalf("call message = %v %v", callMessage, err)
	}
	exec := func(statement string, args ...any) {
		t.Helper()
		if _, err := catalog.DB.Exec(statement, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("UPDATE conversations SET origin_host_id='mac'")
	exec("UPDATE conversations SET origin_host_id='laptop' WHERE native_id='laptop'")
	for _, pair := range [][2]string{{"labeler-conductor", "labeler"}, {"conductor-session-mirror", "conductor-session"}} {
		exec(`INSERT INTO conversation_identity_links(left_id,right_id,relationship,confidence,evidence_json) VALUES(?,?,'native-alias',1,'{}')`, ids[pair[0]], ids[pair[1]])
	}
	call := func(id, conversation, message, started, ended string, background bool, commands ...[3]string) {
		texts := []string{}
		for _, command := range commands {
			texts = append(texts, command[0])
		}
		exec(`INSERT INTO tool_calls(id,workspace_id,conversation_id,sequence,provider,kind,tool_name,tool_category,status,call_message_id,started_at,ended_at,command,backgrounded)
			VALUES(?,(SELECT workspace_id FROM conversations WHERE id=?),?,1,'claude','tool_call','Bash','command','ok',?,?,?,?,?)`,
			id, conversation, conversation, message, at(started), at(ended), strings.Join(texts, " && "), background)
		for position, command := range commands {
			exec(`INSERT INTO tool_commands(tool_call_id,position,command,program,subcommand) VALUES(?,?,?,?,?)`, id, position+1, command[0], command[1], command[2])
		}
	}
	labeler, other := ids["labeler"], ids["other"]
	call("a", labeler, firstString(callMessage[0]["id"]), "12:10:00", "12:12:00", false,
		[3]string{"cd /tmp", "cd", ""}, [3]string{`timeout 120 claude -p "Label" --model opus`, "claude", "Label"})
	// agy records its start to the second, before the command's.
	call("b", labeler, "", "12:20:00.400", "12:21:00", false, [3]string{`agy -p "Label" --model flash`, "agy", ""})
	call("s", labeler, "", "12:40:00", "12:40:00.100", true, [3]string{"nohup python3 gold/run_exp.py e3 --models opus,sol,flash > run.log 2>&1", "python3", "run_exp.py"})
	call("x", labeler, "", "13:00:00", "13:05:00", false, [3]string{"python3 gold/chunk_gold.py run", "python3", "chunk_gold.py"})
	call("y", other, "", "13:00:05", "13:02:00", false, [3]string{"go test ./...", "go", "test"})
	call("z", labeler, "", "13:20:00", "13:21:00", false, [3]string{"python3 gold/prefix_probe.py", "python3", "prefix_probe.py"})
	call("t", labeler, "", "13:40:00", "13:45:00", false, [3]string{"TL1_DB_PATH=/work/other/.context/tl1.db python3 -m tl1 run", "python3", "-m tl1"})
	call("w", labeler, "", "14:00:00", "14:05:00", false, [3]string{"python3 gold/qa.py emulate", "python3", "qa.py"})
	call("u", labeler, "", "14:59:50", "15:01:00", false, [3]string{"python3 gold/validate.py", "python3", "validate.py"})
	call("r", other, "", "15:00:01.100", "15:00:01.300", false, [3]string{"rg -n ERROR testdata", "rg", ""})
	call("v", labeler, "", "15:30:05", "15:31:00", false, [3]string{"python3 gold/prefix_probe.py seq --cli agy --n 3", "python3", "prefix_probe.py"})
	call("g", other, "", "15:29:58", "15:32:00", false, [3]string{"go test ./...", "go", "test"})

	if err := catalog.RebuildAuthorship(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	launches, err := queryMaps(catalog.DB, "SELECT c.native_id child,p.native_id parent,l.tool_call_id,l.program FROM conversation_launches l JOIN conversations c ON c.id=l.child_id JOIN conversations p ON p.id=l.parent_id ORDER BY child")
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, launch := range launches {
		got = append(got, strings.Join([]string{firstString(launch["child"]), firstString(launch["parent"]), firstString(launch["tool_call_id"]), firstString(launch["program"])}, " "))
	}
	want := []string{"after labeler u claude", "named labeler a claude", "named-agy labeler b agy", "named-folder labeler t claude", "probe labeler v agy",
		"script-1 labeler s claude", "script-2 labeler s codex", "script-3 labeler s agy"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("launches:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	spans := map[string][]authorSpan{}
	authorship, err := queryMaps(catalog.DB, "SELECT c.native_id,a.spans_json FROM message_authorship a JOIN conversations c ON c.id=a.conversation_id")
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range authorship {
		var decoded []authorSpan
		if err := json.Unmarshal([]byte(firstString(row["spans_json"])), &decoded); err != nil {
			t.Fatal(err)
		}
		spans[firstString(row["native_id"])] = decoded
	}
	only := func(conversation, category, reason string) authorSpan {
		t.Helper()
		found := spans[conversation]
		if len(found) != 1 || found[0].Category != category || !strings.Contains(found[0].Reason, reason) {
			t.Fatalf("%s spans = %#v, want one %s span with %q", conversation, found, category, reason)
		}
		return found[0]
	}
	launched := only("named", spanAutomated, "Launched with claude -p by a command")
	if launched.Source != labeler || launched.Message != firstString(callMessage[0]["id"]) {
		t.Fatalf("launched span = %#v", launched)
	}
	only("named-agy", spanAutomated, "Launched with agy -p by a command")
	only("script-2", spanAutomated, "Launched with codex exec by a command")
	// Not linked: still sent by a headless run.
	only("ambiguous", spanAutomated, "Conductor did not start")
	only("late", spanAutomated, "Conductor did not start")
	only("elsewhere", spanAutomated, "codex exec run")
	only("laptop", spanTyped, "")
	only("terminal", spanTyped, "")
	only("conductor-session", spanTyped, "")

	writing, _, err := catalog.writingData(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byTitle := map[string]map[string]any{}
	for _, row := range writing {
		byTitle[firstString(row["title"])] = row
	}
	row := byTitle["labeler"]
	if byTitle["script-1"] != nil || byTitle["named"] != nil || byTitle["ambiguous"] == nil || row == nil ||
		integer(row["launched_works"]) != 8 || integer(row["subagent_works"]) != 0 || integer(row["typed_words"]) != 7 || integer(row["automated_words"]) != 8*int64(len(strings.Fields(prompt))) {
		t.Fatalf("writing rows = %#v", writing)
	}
}
