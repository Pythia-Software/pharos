package archive

import (
	"bytes"
	"compress/gzip"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// opencodeSecret stands for the credentials OpenCode keeps beside its
// sessions; no capture may hold it.
const opencodeSecret = "sk-opencode-secret-key"

// opencodeFixture writes an OpenCode 2.2 database in the shape this Mac's
// holds: a session that asked a subagent for a review, the subagent's own
// session, a session whose content is in session_part, and the credential,
// account and settings tables beside them.
func opencodeFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	opencodeDatabase(t, filepath.Join(root, "opencode.db"), opencodeRows)
	return root
}

const opencodeRows = `
INSERT INTO project VALUES('p1','/work/app','git','app',1793720000000,1793720000000);
-- The person's session: the totals hold a request whose messages an undo
-- deleted, beyond the messages that remain.
INSERT INTO session_v2 VALUES('ses_root','p1',NULL,NULL,NULL,'wordstats','/work/app','Wordstats task','2.2.5',0.0,'{}',
  9000,600,300,8000,0,'build','{"id":"big-pickle","providerID":"opencode"}',1793720000000,1793720060000);
INSERT INTO session_message VALUES('m1','ses_root','user',4,1793720000000,1793720000000,'{"time":{"created":1793720000000},"text":"Count the words","files":[],"agents":[]}');
INSERT INTO session_message VALUES('m2','ses_root','assistant',5,1793720001000,1793720003000,'{"time":{"created":1793720001000,"completed":1793720003000},"agent":"build","model":{"id":"big-pickle","providerID":"opencode"},"content":[
  {"type":"reasoning","text":"Look at the files first.","native":{"reasoningField":"reasoning_content"},"time":{"created":1793720001100}},
  {"type":"tool","id":"call_1","name":"shell","state":{"status":"completed","input":{"command":"ls"},"content":[{"type":"text","text":"wordstats.py"}],"metadata":{"exit":0,"truncated":false}},"time":{"created":1793720001200,"ran":1793720001300,"completed":1793720001800}},
  {"type":"tool","id":"call_2","name":"shell","state":{"status":"completed","input":{"command":"cat missing.txt"},"content":[{"type":"text","text":"No such file"}],"metadata":{"exit":1,"truncated":false}},"time":{"created":1793720001200,"ran":1793720001300,"completed":1793720001400}}],
  "finish":"tool-calls","cost":0,"tokens":{"input":3000,"output":40,"reasoning":10,"cache":{"read":4000,"write":0}}}');
INSERT INTO session_message VALUES('m3','ses_root','assistant',14,1793720004000,1793720010000,'{"time":{"created":1793720004000},"agent":"build","model":{"id":"big-pickle","providerID":"opencode"},"content":[
  {"type":"tool","id":"call_3","name":"subagent","state":{"status":"completed","input":{"agent":"explore","prompt":"Review wordstats.py"},"content":[{"type":"text","text":"<subagent sessionID=\"ses_child\" state=\"completed\">Looks fine.</subagent>"}],"metadata":{"sessionID":"ses_child","truncated":false}},"time":{"created":1793720004100,"completed":1793720009000}}],
  "finish":"tool-calls","cost":0,"tokens":{"input":1000,"output":30,"reasoning":0,"cache":{"read":2000,"write":0}}}');
INSERT INTO session_message VALUES('m4','ses_root','assistant',27,1793720011000,1793720012000,'{"time":{"created":1793720011000},"agent":"build","model":{"id":"big-pickle","providerID":"opencode"},"content":[{"type":"text","text":"There are 74 words."}],"finish":"stop","cost":0,"tokens":{"input":500,"output":20,"reasoning":0,"cache":{"read":2000,"write":0}}}');
INSERT INTO session_message VALUES('m5','ses_root','idle',30,1793720012000,1793720012000,'{"time":{"created":1793720012000},"outcome":"succeeded"}');
-- The subagent's session; its totals equal its message.
INSERT INTO session_v2 VALUES('ses_child','p1',NULL,'ses_root',NULL,'review','/work/app','Review wordstats.py','2.2.5',0.0,'{}',
  700,50,5,300,0,'explore','{"id":"big-pickle","providerID":"opencode"}',1793720004200,1793720008000);
INSERT INTO session_message VALUES('c1','ses_child','user',4,1793720004200,1793720004200,'{"time":{"created":1793720004200},"text":"Review wordstats.py","files":[],"agents":[]}');
INSERT INTO session_message VALUES('c2','ses_child','assistant',5,1793720004300,1793720008000,'{"time":{"created":1793720004300},"agent":"explore","model":{"id":"big-pickle","providerID":"opencode"},"content":[{"type":"text","text":"Looks fine."}],"finish":"stop","cost":0,"tokens":{"input":700,"output":45,"reasoning":5,"cache":{"read":300,"write":0}}}');
-- A session whose assistant content is in session_part.
INSERT INTO session_v2 VALUES('ses_parts','p1',NULL,NULL,NULL,'parts','/work/app','Parts','2.2.5',0.0,'{}',
  100,10,0,0,0,'build','{"id":"big-pickle","providerID":"opencode"}',1793721000000,1793721001000);
-- Its prompt quotes usage JSON, which is not usage.
INSERT INTO session_message VALUES('q1','ses_parts','user',1,1793721000000,1793721000000,'{"time":{"created":1793721000000},"text":"{\"usage\":{\"input_tokens\":1000,\"output_tokens\":200}}"}');
INSERT INTO session_message VALUES('q2','ses_parts','assistant',2,1793721000500,1793721001000,'{"time":{"created":1793721000500},"model":{"id":"big-pickle","providerID":"opencode"},"tokens":{"input":100,"output":10,"reasoning":0,"cache":{"read":0,"write":0}}}');
INSERT INTO session_part VALUES('q2','ses_parts',1,'text',NULL,1793721001000,1793721001000,'{"type":"text","text":"world"}');
INSERT INTO session_part VALUES('q2','ses_parts',0,'text',NULL,1793721000900,1793721000900,'{"type":"text","text":"Hello,"}');
-- An image a part names as media:<hash>.
INSERT INTO blob VALUES('h1','image/png',4,1,1,x'89504e47',1793721000000);
INSERT INTO blob_ref VALUES('ses_parts','q1','h1',0);
INSERT INTO credential VALUES('opencode','` + opencodeSecret + `');
INSERT INTO kv VALUES('auth','{"key":"` + opencodeSecret + `"}');
`

func opencodeDatabase(t *testing.T, path, rows string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	schema := `
CREATE TABLE project (id text PRIMARY KEY, worktree text NOT NULL, vcs text, name text, time_created integer NOT NULL, time_updated integer NOT NULL);
CREATE TABLE session_v2 (id text PRIMARY KEY, project_id text NOT NULL, workspace_id text, parent_id text, fork_session_id text, slug text NOT NULL,
  directory text NOT NULL, title text, version text NOT NULL, cost real DEFAULT 0 NOT NULL, metadata text, tokens_input integer DEFAULT 0 NOT NULL,
  tokens_output integer DEFAULT 0 NOT NULL, tokens_reasoning integer DEFAULT 0 NOT NULL, tokens_cache_read integer DEFAULT 0 NOT NULL,
  tokens_cache_write integer DEFAULT 0 NOT NULL, agent text, model text, time_created integer NOT NULL, time_updated integer NOT NULL);
CREATE TABLE session_message (id text PRIMARY KEY, session_id text NOT NULL, type text NOT NULL, seq integer NOT NULL, time_created integer NOT NULL, time_updated integer NOT NULL, data text NOT NULL);
CREATE TABLE session_part (message_id text NOT NULL, session_id text NOT NULL, ordinal integer NOT NULL, type text NOT NULL, call_id text, time_created integer NOT NULL, time_updated integer NOT NULL, data text NOT NULL, PRIMARY KEY(message_id, ordinal));
CREATE TABLE blob (hash text PRIMARY KEY, mime text NOT NULL, bytes integer NOT NULL, width integer, height integer, data blob NOT NULL, time_created integer NOT NULL);
CREATE TABLE blob_ref (session_id text NOT NULL, message_id text NOT NULL, hash text NOT NULL, seq integer NOT NULL, PRIMARY KEY(message_id, hash, seq));
CREATE TABLE credential (id text PRIMARY KEY, secret text NOT NULL);
CREATE TABLE kv (key text PRIMARY KEY, value text NOT NULL);
CREATE VIEW secrets AS SELECT secret FROM credential;
`
	if _, err := db.Exec(schema + rows); err != nil {
		t.Fatal(err)
	}
}

func discoverOpenCode(t *testing.T, root string) map[string]WorkspaceRecord {
	t.Helper()
	adapter, err := MakeAdapter(SourceConfig{Name: "opencode", Kind: "opencode", Path: root, Account: "local"})
	if err != nil {
		t.Fatal(err)
	}
	records := map[string]WorkspaceRecord{}
	if err := adapter.Discover(func(record WorkspaceRecord) error {
		records[record.SourceID] = record
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return records
}

func TestOpenCodeSessionKeepsItsTurnsToolsAndSubagent(t *testing.T) {
	records := discoverOpenCode(t, opencodeFixture(t))
	if len(records) != 2 {
		t.Fatalf("records = %v", records)
	}
	session := records["ses_root"]
	if session.SourceKind != "opencode" || session.Title != "Wordstats task" || session.Location != "/work/app" || session.Purpose != "Count the words" || session.Outcome != "There are 74 words." {
		t.Fatalf("session = %+v", session)
	}
	if len(session.Conversations) != 2 {
		t.Fatalf("conversations = %+v", session.Conversations)
	}
	main, review := session.Conversations[0], session.Conversations[1]
	if main.NativeID != "ses_root" || main.Provider != "opencode" || main.Harness != "opencode" || main.HarnessVersionLast != "2.2.5" || main.Model != "big-pickle" {
		t.Fatalf("main = %+v", main)
	}
	if review.NativeID != "ses_child" || review.ParentNativeID != "ses_root" || review.AgentDepth != 1 || review.AgentPath != "explore" {
		t.Fatalf("subagent = %+v", review)
	}
	kinds := func(messages []MessageRecord) string {
		out := []string{}
		for _, message := range messages {
			out = append(out, message.Role+":"+message.Kind)
		}
		return strings.Join(out, " ")
	}
	if got, want := kinds(main.Messages), "user:message assistant:reasoning assistant:tool_call tool:tool_result assistant:tool_call tool:tool_result system:metadata assistant:delegation tool:delegation_result system:metadata assistant:message system:metadata system:metadata"; got != want {
		t.Fatalf("main messages\n got %s\nwant %s", got, want)
	}
	byID := map[string]MessageRecord{}
	for _, message := range main.Messages {
		byID[message.NativeID] = message
	}
	if call, result := byID["m2:block:1"], byID["result:call_1"]; call.CallID != "call_1" || result.Text != `{"content":[{"text":"wordstats.py","type":"text"}],"details":{"duration_ms":500,"exit_code":0},"is_error":false,"tool":"shell"}` {
		t.Fatalf("call %+v result %+v", call, result)
	}
	if reasoning := byID["m2:block:0"]; reasoning.Text != "Look at the files first." || reasoning.Kind != "reasoning" {
		t.Fatalf("reasoning = %+v", reasoning)
	}
	if prompt := review.Messages[0]; prompt.Role != agentRole || prompt.Sender != "agent:ses_root" {
		t.Fatalf("subagent prompt = %+v", prompt)
	}
	parts := records["ses_parts"].Conversations[0].Messages
	if got := kinds(parts); got != "user:message assistant:message assistant:message system:metadata" || parts[1].Text != "Hello," || parts[2].Text != "world" {
		t.Fatalf("content in session_part: %s %+v", got, parts)
	}
}

// opencodeMoreRows add a session with each other kind of message, and a fork
// of ses_parts that starts with copies of its messages.
const opencodeMoreRows = `
INSERT INTO session_v2 VALUES('ses_more','p1',NULL,NULL,NULL,'more','/work/app','More','2.2.5',0.0,'{}',
  200,20,0,0,0,'build','{"id":"big-pickle","providerID":"opencode"}',1793722000000,1793722010000);
INSERT INTO session_message VALUES('r1','ses_more','user',1,1793722000000,1793722000000,'{"text":"Tidy up"}');
INSERT INTO session_message VALUES('r2','ses_more','synthetic',2,1793722000100,1793722000100,'{"text":"The user switched to plan mode."}');
INSERT INTO session_message VALUES('r3','ses_more','system',3,1793722000200,1793722000200,'{"text":"Stay in /work/app."}');
INSERT INTO session_message VALUES('r4','ses_more','skill',4,1793722000300,1793722000300,'{"skill":"tidy","text":"Run gofmt first."}');
INSERT INTO session_message VALUES('r5','ses_more','shell',5,1793722000400,1793722000400,'{"command":"git status","status":"completed","exit":1,"output":"not a repo"}');
-- Its content list is empty; its parts are in session_part.
INSERT INTO session_message VALUES('r6','ses_more','assistant',6,1793722000500,1793722001000,'{"model":{"id":"big-pickle"},"content":[],"tokens":{"input":200,"output":20,"reasoning":0,"cache":{"read":0,"write":0}}}');
INSERT INTO session_part VALUES('r6','ses_more',0,'text',NULL,1793722000900,1793722000900,'{"type":"text","text":"Done."}');
INSERT INTO session_message VALUES('r7','ses_more','compaction',7,1793722002000,1793722002000,'{"summary":"Tidied the repo."}');
-- The fork's totals count only its own request.
INSERT INTO session_v2 VALUES('ses_fork','p1',NULL,NULL,'ses_parts','fork','/work/app','Parts fork','2.2.5',0.0,'{}',
  50,5,0,0,0,'build','{"id":"big-pickle","providerID":"opencode"}',1793723000000,1793723001000);
INSERT INTO session_message VALUES('evt_ab_1','ses_fork','user',1,1793721000000,1793721000000,'{"text":"Say hello"}');
INSERT INTO session_message VALUES('evt_ab_2','ses_fork','assistant',2,1793721000500,1793721001000,'{"model":{"id":"big-pickle"},"content":[{"type":"text","text":"Hello, world"}],"tokens":{"input":100,"output":10,"reasoning":0,"cache":{"read":0,"write":0}}}');
INSERT INTO session_message VALUES('msg_f3','ses_fork','user',3,1793723000000,1793723000000,'{"text":"Now in French"}');
INSERT INTO session_message VALUES('msg_f4','ses_fork','assistant',4,1793723000500,1793723001000,'{"model":{"id":"big-pickle"},"content":[{"type":"text","text":"Bonjour"}],"tokens":{"input":50,"output":5,"reasoning":0,"cache":{"read":0,"write":0}}}');
`

// What OpenCode adds to a prompt is context, a command the person ran is a
// call, a compaction is a boundary, parts replace an empty content list, and
// a fork keeps the messages it copied without counting their requests again.
func TestOpenCodeOtherMessagesAndForks(t *testing.T) {
	root := t.TempDir()
	opencodeDatabase(t, filepath.Join(root, "opencode.db"), opencodeRows+opencodeMoreRows)
	records := discoverOpenCode(t, root)
	kinds := func(messages []MessageRecord) string {
		out := []string{}
		for _, message := range messages {
			out = append(out, message.Role+":"+message.Kind)
		}
		return strings.Join(out, " ")
	}
	more := records["ses_more"].Conversations[0].Messages
	if got, want := kinds(more), "user:message system:context system:context system:context user:tool_call tool:tool_result assistant:message system:metadata system:metadata"; got != want {
		t.Fatalf("messages\n got %s\nwant %s", got, want)
	}
	if !strings.Contains(more[1].Text, `"source":"opencode_synthetic"`) || !strings.Contains(more[3].Text, `"source":"opencode_skill:tidy"`) || !strings.Contains(more[3].Text, "Run gofmt first.") {
		t.Fatalf("context = %+v", more[1:4])
	}
	if call, result := more[4], more[5]; call.CallID == "" || result.CallID != call.CallID || !strings.Contains(call.Text, "git status") ||
		result.Text != `{"content":"not a repo","details":{"exit_code":1},"is_error":true,"tool":"shell"}` {
		t.Fatalf("shell call %+v result %+v", call, result)
	}
	if more[6].Text != "Done." || !isCompactionMarker(more[8]) {
		t.Fatalf("parts %+v compaction %+v", more[6], more[8])
	}
	fork := records["ses_fork"]
	if got := kinds(fork.Conversations[0].Messages); got != "user:message assistant:message user:message assistant:message system:metadata" || fork.Metadata["forked_from"] != "ses_parts" {
		t.Fatalf("fork %s %+v", got, fork.Metadata)
	}

	c, _ := testCatalog(t)
	ingestSource(t, c, SourceConfig{Name: "opencode", Kind: "opencode", Path: root, Account: "local", Enabled: true})
	usage := func(id string) int {
		return countRows(t, c, `SELECT COALESCE(SUM(u.total_tokens),0) FROM agent_session_usage u JOIN agent_sessions a ON a.id=u.agent_session_id JOIN conversations v ON v.id=a.conversation_id WHERE v.native_id=?`, id)
	}
	if got := usage("ses_fork"); got != 55 {
		t.Fatalf("fork usage %d, want its own request's 55", got)
	}
	if got := usage("ses_parts"); got != 110 {
		t.Fatalf("source usage %d", got)
	}
	if n := countRows(t, c, `SELECT COUNT(*) FROM tool_calls WHERE provider='opencode' AND call_id='r5:shell'`); n != 1 {
		t.Fatalf("%d calls for the command the person ran", n)
	}
}

func TestOpenCodeUsageCountsEachRequestOnce(t *testing.T) {
	c, _ := testCatalog(t)
	ingestSource(t, c, SourceConfig{Name: "opencode", Kind: "opencode", Path: opencodeFixture(t), Account: "local", Enabled: true})
	// Messages: 7050 + 3030 + 2520; the session's totals hold 1300 more
	// input and 210 more output and reasoning than they do, which the
	// residual keeps. The subagent's 1050 and the parts session's 110.
	session := func(id string) int {
		return countRows(t, c, `SELECT COALESCE(SUM(u.total_tokens),0) FROM agent_session_usage u JOIN agent_sessions a ON a.id=u.agent_session_id JOIN conversations v ON v.id=a.conversation_id WHERE v.native_id=?`, id)
	}
	if got, want := session("ses_root"), 9000+8000+600+300; got != want {
		t.Fatalf("root usage %d, want its totals %d", got, want)
	}
	if got := session("ses_child"); got != 700+300+50+5 {
		t.Fatalf("subagent usage %d", got)
	}
	if got := session("ses_parts"); got != 110 {
		t.Fatalf("parts session usage %d", got)
	}
	// call_1 and call_2 split m2's 50 output tokens.
	if n := countRows(t, c, `SELECT COUNT(*) FROM tool_calls WHERE provider='opencode' AND call_id IN ('call_1','call_2') AND output_tokens=25`); n != 2 {
		t.Fatalf("%d calls share their request's output", n)
	}
}

// capturedText is everything a capture holds, gzip-compressed records
// decompressed.
func capturedText(t *testing.T, root string) string {
	t.Helper()
	var all bytes.Buffer
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if reader, err := gzip.NewReader(bytes.NewReader(data)); err == nil {
			if plain, err := io.ReadAll(reader); err == nil {
				data = plain
			}
		}
		all.Write(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return all.String()
}

func TestOpenCodeCaptureHoldsOnlyTheSessions(t *testing.T) {
	useHost(t, "host-a")
	c, config := testCatalog(t)
	root := opencodeFixture(t)
	source := SourceConfig{Name: "opencode", Kind: "opencode", Path: root, Account: "local", Enabled: true}
	config.Sources = []SourceConfig{source}
	ingestSource(t, c, source)
	if result := runCapture(t, config); !result.OK {
		t.Fatalf("capture = %+v", result)
	}
	snapshot := capturedPath(config, "opencode", "files/opencode.db")
	db, err := openReadOnlySQLite(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	tables, err := tableNames(db)
	db.Close()
	for table := range tables {
		if !opencodeCaptureTables[table] {
			err = errors.Join(err, fmt.Errorf("captured %s", table))
		}
	}
	if err != nil || !tables["session_v2"] || !tables["session_message"] || !tables["blob"] || !tables["blob_ref"] {
		t.Fatalf("captured tables %v %v", tables, err)
	}
	if strings.Contains(capturedText(t, config.CaptureRoot), opencodeSecret) {
		t.Fatal("the capture holds OpenCode's credentials")
	}
	messages := func(c *Catalog) int {
		return countRows(t, c, `SELECT COUNT(*) FROM messages m JOIN conversations v ON v.id=m.conversation_id WHERE v.provider='opencode'`)
	}
	live := messages(c)
	useHost(t, "host-b")
	other, _ := testCatalog(t)
	indexCaptures(t, other, config.CaptureRoot, "host-a")
	if captured := messages(other); captured != live || live == 0 {
		t.Fatalf("captured %d messages, live %d", captured, live)
	}

	// Copied before indexing, as rows: the same sessions, and still no
	// credentials.
	useHost(t, "host-a")
	recorded, recordedConfig := testCatalog(t)
	source.Options = map[string]any{"sqlite_record_capture": true}
	recordedConfig.Sources = []SourceConfig{source}
	ingestSource(t, recorded, source)
	if got := messages(recorded); got != live {
		t.Fatalf("copied before indexing: %d messages, live %d", got, live)
	}
	if strings.Contains(capturedText(t, recorded.captureRootPath()), opencodeSecret) {
		t.Fatal("the rows copied before indexing hold OpenCode's credentials")
	}
	// They read the same as the capture's own, so it indexes on its host.
	if result := runCapture(t, recordedConfig); !result.OK {
		t.Fatalf("capture = %+v", result)
	}
	ingestSource(t, recorded, source)
	indexCaptures(t, recorded, recordedConfig.CaptureRoot, "host-a")
}

func TestOpenCodeReadsOnlyTheSessionThatChanged(t *testing.T) {
	c, _ := testCatalog(t)
	root := opencodeFixture(t)
	source := SourceConfig{Name: "opencode", Kind: "opencode", Path: root, Account: "local", Enabled: true}
	ingestSource(t, c, source)
	db, err := sql.Open("sqlite", filepath.Join(root, "opencode.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO session_message VALUES('q3','ses_parts','user',3,1793721002000,1793721002000,'{"time":{"created":1793721002000},"text":"Again"}');
		UPDATE session_v2 SET time_updated=1793721002000 WHERE id='ses_parts'`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	parsed := ingestSource(t, c, source).Parsed
	if n := countRows(t, c, `SELECT COUNT(*) FROM messages WHERE native_id='q3'`); n != 1 || parsed != 1 {
		t.Fatalf("after a new prompt: %d copies, %d sessions parsed", n, parsed)
	}

	// An undo deletes messages and rows; the session is read again.
	db, err = sql.Open("sqlite", filepath.Join(root, "opencode.db"))
	if err == nil {
		_, err = db.Exec(`DELETE FROM session_message WHERE id='q3'`)
		db.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	if parsed := ingestSource(t, c, source).Parsed; parsed != 1 {
		t.Fatalf("after an undo: %d sessions parsed", parsed)
	}
}

// A database in the 1.x schema is reported, read live or from either kind of
// capture, and its sessions are kept in the capture for a later reader.
func TestOpenCodeOlderSchemaIsReportedNotSkipped(t *testing.T) {
	useHost(t, "host-a")
	root := t.TempDir()
	path := filepath.Join(root, "opencode.db")
	db, err := sql.Open("sqlite", path)
	if err == nil {
		_, err = db.Exec(`CREATE TABLE session (id text); CREATE TABLE message (id text); CREATE TABLE part (id text); CREATE TABLE credential (secret text)`)
		db.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	reported := func(name string, err any) {
		t.Helper()
		if !strings.Contains(fmt.Sprint(err), "before 2.0") {
			t.Fatalf("%s: %v", name, err)
		}
	}
	source := SourceConfig{Name: "opencode", Kind: "opencode", Path: root, Account: "local", Enabled: true}
	adapter, _ := MakeAdapter(source)
	reported("live", adapter.Discover(func(WorkspaceRecord) error { return nil }))

	c, config := testCatalog(t)
	config.Sources = []SourceConfig{source}
	runCapture(t, config)
	useHost(t, "host-b")
	targets, err := captureTargets(config.CaptureRoot, []string{"host-a"}, false, nil)
	if err != nil || len(targets) != 1 {
		t.Fatalf("captures %v %v", targets, err)
	}
	result := c.IndexCapture(t.Context(), targets[0], nil)
	reported("captured", result.Error)
	if !strings.Contains(fmt.Sprint(result.Error), path) {
		t.Fatalf("the error names %v", result.Error)
	}
	useHost(t, "host-a")
	source.Options = map[string]any{"sqlite_record_capture": true}
	adapter, _ = MakeAdapter(source)
	reported("copied before indexing", c.Ingest(adapter, nil).Error)
}

// A session deleted in OpenCode between two captures, before either was
// indexed, is indexed from the older one; a subagent whose session remains
// is not taken from it, and keeps what was added since.
func TestOpenCodeSessionDeletedBeforeIndexingIsKept(t *testing.T) {
	useHost(t, "host-a")
	root := opencodeFixture(t)
	source := SourceConfig{Name: "opencode", Kind: "opencode", Path: root, Account: "local", Enabled: true}
	config := captureTestConfig(t, source)
	runCapture(t, config)
	db, err := sql.Open("sqlite", filepath.Join(root, "opencode.db"))
	if err == nil {
		_, err = db.Exec(`DELETE FROM session_part WHERE session_id='ses_parts'; DELETE FROM session_message WHERE session_id='ses_parts'; DELETE FROM session_v2 WHERE id='ses_parts';
			INSERT INTO session_message VALUES('c3','ses_child','user',6,1793720009000,1793720009000,'{"time":{"created":1793720009000},"text":"One more look"}');
			UPDATE session_v2 SET time_updated=1793720009000 WHERE id='ses_child';
			DELETE FROM session_message WHERE session_id='ses_root'; DELETE FROM session_v2 WHERE id='ses_root'`)
		db.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	runCapture(t, config)
	useHost(t, "host-b")
	c, _ := testCatalog(t)
	indexCaptures(t, c, config.CaptureRoot, "host-a")
	for _, id := range []string{"ses_root", "ses_child", "ses_parts"} {
		if n := countRows(t, c, `SELECT COUNT(*) FROM conversations WHERE native_id=?`, id); n != 1 {
			t.Fatalf("%s: %d conversations", id, n)
		}
	}
	if n := countRows(t, c, `SELECT COUNT(*) FROM messages WHERE native_id='c3'`); n != 1 {
		t.Fatalf("the subagent's newer prompt: %d", n)
	}
}

// Rows copied before indexing while OpenCode writes are copied again, never
// kept as the newer database's.
func TestOpenCodeRowsCopiedDuringAWriteAreCopiedAgain(t *testing.T) {
	c, _ := testCatalog(t)
	root := opencodeFixture(t)
	source := SourceConfig{Name: "opencode", Kind: "opencode", Path: root, Account: "local", Enabled: true, Options: map[string]any{"sqlite_record_capture": true}}
	// OpenCode writes through a write-ahead log, which readers don't block.
	db, err := sql.Open("sqlite", filepath.Join(root, "opencode.db"))
	if err == nil {
		_, err = db.Exec(`PRAGMA journal_mode=WAL`)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	written := false
	override(t, &recordExportReading, func(file string) {
		if written {
			return
		}
		written = true
		if _, err := db.Exec(`INSERT INTO session_message VALUES('q3','ses_parts','user',3,1793721002000,1793721002000,'{"time":{"created":1793721002000},"text":"Again"}');
			UPDATE session_v2 SET time_updated=1793721002000 WHERE id='ses_parts'`); err != nil {
			t.Error(err)
		}
	})
	adapter, _ := MakeAdapter(source)
	c.Ingest(adapter, nil)
	ingestSource(t, c, source)
	if n := countRows(t, c, `SELECT COUNT(*) FROM messages WHERE native_id='q3'`); n != 1 || !written {
		t.Fatalf("the prompt written during the copy: %d", n)
	}
}

// A subagent's own subagent follows it, and is two levels down.
func TestOpenCodeNestedSubagentFollowsItsParent(t *testing.T) {
	root := t.TempDir()
	opencodeDatabase(t, filepath.Join(root, "opencode.db"), opencodeRows+`
INSERT INTO session_v2 VALUES('ses_a','p1',NULL,'ses_child',NULL,'nested','/work/app','Nested','2.2.5',0.0,'{}',
  0,0,0,0,0,'explore','{"id":"big-pickle","providerID":"opencode"}',1793720005000,1793720006000);
INSERT INTO session_message VALUES('n1','ses_a','user',1,1793720005000,1793720005000,'{"time":{"created":1793720005000},"text":"Look closer"}');
`)
	conversations := discoverOpenCode(t, root)["ses_root"].Conversations
	order := []string{}
	for _, conversation := range conversations {
		order = append(order, conversation.NativeID)
	}
	if strings.Join(order, " ") != "ses_root ses_child ses_a" || conversations[2].ParentNativeID != "ses_child" || conversations[2].AgentDepth != 2 {
		t.Fatalf("conversations %v, nested %+v", order, conversations[len(conversations)-1])
	}
}

// Verification reads a session as indexing does, live or from rows copied
// before indexing.
func TestOpenCodeSessionIsVerifiedLive(t *testing.T) {
	for _, recorded := range []bool{false, true} {
		t.Run(map[bool]string{false: "live", true: "copied"}[recorded], func(t *testing.T) {
			c, _ := testCatalog(t)
			// A folder's name can hold the # that separates a session.
			root := filepath.Join(t.TempDir(), "work#2")
			opencodeDatabase(t, filepath.Join(root, "opencode.db"), opencodeRows)
			source := SourceConfig{Name: "opencode", Kind: "opencode", Path: root, Account: "local", Enabled: true, Options: map[string]any{"sqlite_record_capture": recorded}}
			ingestSource(t, c, source)
			if result := c.auditUnit(t.Context(), firstSyncUnit(t, c, source), "manual", 0); result.Outcome != "passed" {
				t.Fatalf("verification: %+v", result)
			}
		})
	}
}

// OpenCode is found and counted by the probe, offered to a Mac set up before
// Pharos read it, and chosen in guided setup; XDG_DATA_HOME moves it.
func TestOpenCodeIsFoundAndSetUp(t *testing.T) {
	home := probeHome(t)
	useHost(t, "host-a")
	probePut(t, filepath.Join(home, ".claude", "projects", "-r", probeID1+".jsonl"), "{}\n")
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	opencodeDatabase(t, filepath.Join(home, "data", "opencode", "opencode.db"), opencodeRows)
	candidate := candidateNamed(t, probeSources(home, Config{}, nil), "opencode")
	if candidate.Kind != "opencode" || candidate.Status != "found" || candidate.Count != 3 || candidate.Unit != "session" || !candidate.Recommended {
		t.Fatalf("candidate = %+v", candidate)
	}
	server := probeServer(t, probeLibrary(t, ""))
	if code, result := probeCall(t, server, http.MethodPost, "/api/probe/accept", `{"accept":["claude"]}`); code != 200 {
		t.Fatalf("accept = %d %v", code, result)
	}
	if _, status := probeCall(t, server, http.MethodGet, "/api/probe/status", ""); status["new_sources"] != float64(1) {
		t.Fatalf("status of a Mac set up before OpenCode = %v", status)
	}
	if code, result := probeCall(t, server, http.MethodPost, "/api/probe", `{"kinds":["opencode"]}`); code != 200 || !strings.Contains(jsonText(result["candidates"]), `"kind":"opencode"`) {
		t.Fatalf("guided probe = %d %v", code, result)
	}
	if code, result := probeCall(t, server, http.MethodPost, "/api/probe/accept", `{"kinds":["opencode"],"accept":["opencode"]}`); code != 200 || jsonText(result["added"]) != `["opencode"]` {
		t.Fatalf("accept opencode = %d %v", code, result)
	}
}
