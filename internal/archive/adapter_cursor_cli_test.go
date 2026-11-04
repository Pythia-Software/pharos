package archive

import (
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// cursorCLISecret stands for each chat's encryption key and the CLI's
// sign-in; no capture may hold it, as text or hex-encoded as the CLI keeps
// its metadata.
const cursorCLISecret = "cursor-cli-secret-do-not-capture"

const (
	cursorCLIChatA = "a0000000-0000-4000-8000-00000000000a"
	cursorCLIChatB = "a0000000-0000-4000-8000-00000000000b"
)

// cursorCLIChatSpec is a chat as the fixture writes it: its messages, in
// order, the roots that list them (indexes into messages, the last the
// latest), its metadata and its meta.json (nil for none).
type cursorCLIChatSpec struct {
	agent, folder string
	messages      []any
	roots         [][]int
	meta          map[string]any
	metaJSON      map[string]any
}

// cursorCLIConversation is a chat in the shapes this Mac's holds: the CLI's
// system prompt and context, a timed prompt, a reply with encrypted thinking
// and a failed shell call, an edit and a closing reply. An older root holds a
// reply that was rewound, with the model it ran on, and another a prefix of
// the latest.
func cursorCLIConversation() cursorCLIChatSpec {
	cursor := func(model string) map[string]any {
		return map[string]any{"cursor": map[string]any{"modelName": model}}
	}
	outcome := func(output map[string]any) map[string]any {
		return map[string]any{"cursor": map[string]any{"highLevelToolCallResult": map[string]any{"output": output}}}
	}
	messages := []any{
		map[string]any{"role": "system", "content": "You are an agent."},
		map[string]any{"role": "user", "content": "<user_info>\nOS: darwin\n</user_info>"},
		map[string]any{"role": "user", "id": "u1", "content": []any{map[string]any{"type": "text", "text": "<timestamp>Wednesday, Nov 4, 2026, 11:18 AM (UTC-5)</timestamp>\n<user_query>\nFix the build\n</user_query>"}}},
		map[string]any{"role": "assistant", "id": "a1", "content": []any{
			map[string]any{"type": "reasoning", "text": "", "signature": "sig", "providerOptions": cursor("cursor-test-high")},
			map[string]any{"type": "text", "text": "Running make."},
			map[string]any{"type": "tool-call", "toolCallId": "call1", "toolName": "Shell", "args": map[string]any{"command": "make"}},
		}},
		map[string]any{"role": "tool", "id": "t1", "content": []any{map[string]any{"type": "tool-result", "toolCallId": "call1", "toolName": "Shell", "result": "make: *** [all] Error 2"}},
			"providerOptions": outcome(map[string]any{"failure": map[string]any{"exitCode": 2, "localExecutionTimeMs": 120}})},
		map[string]any{"role": "assistant", "id": "a2", "content": []any{map[string]any{"type": "tool-call", "toolCallId": "call2", "toolName": "StrReplace", "args": map[string]any{"path": "main.go"}}}},
		map[string]any{"role": "tool", "id": "t2", "content": []any{map[string]any{"type": "tool-result", "toolCallId": "call2", "toolName": "StrReplace", "result": "ok"}},
			"providerOptions": outcome(map[string]any{"success": map[string]any{"localExecutionTimeMs": 5}})},
		map[string]any{"role": "assistant", "id": "a3", "content": []any{map[string]any{"type": "text", "text": "Fixed."}}},
		map[string]any{"role": "assistant", "id": "r1", "content": []any{map[string]any{"type": "text", "text": "Abandoned reply.", "providerOptions": cursor("cursor-old")}}},
	}
	return cursorCLIChatSpec{
		agent: cursorCLIChatA, folder: "/work/app", messages: messages,
		roots: [][]int{{0, 1, 2, 8}, {0, 1, 2, 3, 4}, {0, 1, 2, 3, 4, 5, 6, 7}},
		meta: map[string]any{"agentId": cursorCLIChatA, "name": "Fix the build", "createdAt": "1793809020000", "mode": "agent",
			"lastUsedModel": "default", "blobEncryptionKey": cursorCLISecret},
		metaJSON: map[string]any{"schemaVersion": 1, "createdAtMs": 1793809020000, "hasConversation": true, "title": "Fix the build",
			"updatedAtMs": 1793809200000, "cwd": "/work/app"},
	}
}

// cursorCLIRootBlob encodes a root as the CLI does: its messages' IDs, the
// context meter and the folder's URI.
func cursorCLIRootBlob(ids []string, folder string) []byte {
	var out []byte
	field := func(number int, data []byte) {
		out = append(out, byte(number<<3|2), byte(len(data)))
		out = append(out, data...)
	}
	for _, id := range ids {
		raw, _ := hex.DecodeString(id)
		field(1, raw)
	}
	field(5, []byte{0x08, 0xb0, 0x09, 0x10, 0xc0, 0x9a, 0x0c}) // 1200 of 200000
	field(9, []byte("file://"+folder))
	return out
}

// cursorCLIRootIDs are the blob IDs of the messages a chat's root lists.
func cursorCLIRootIDs(chat cursorCLIChatSpec, root int) []string {
	ids := []string{}
	for _, index := range chat.roots[root] {
		ids = append(ids, cursorCLIBlobID([]byte(jsonText(chat.messages[index]))))
	}
	return ids
}

// cursorCLIWriteChat writes a chat under the CLI's folder root.
func cursorCLIWriteChat(t *testing.T, root string, chat cursorCLIChatSpec) string {
	t.Helper()
	dir := filepath.Join(root, "chats", hashBytes([]byte(chat.folder))[:32], chat.agent)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE blobs (id TEXT PRIMARY KEY, data BLOB); CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT)`); err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, message := range chat.messages {
		data := []byte(jsonText(message))
		ids = append(ids, cursorCLIBlobID(data))
		if _, err := db.Exec(`INSERT OR IGNORE INTO blobs VALUES(?, ?)`, ids[len(ids)-1], data); err != nil {
			t.Fatal(err)
		}
	}
	latest := ""
	for index := range chat.roots {
		data := cursorCLIRootBlob(cursorCLIRootIDs(chat, index), chat.folder)
		latest = cursorCLIBlobID(data)
		if _, err := db.Exec(`INSERT OR IGNORE INTO blobs VALUES(?, ?)`, latest, data); err != nil {
			t.Fatal(err)
		}
	}
	meta := map[string]any{"latestRootBlobId": latest}
	for key, value := range chat.meta {
		meta[key] = value
	}
	if _, err := db.Exec(`INSERT INTO meta VALUES('0', ?)`, hex.EncodeToString([]byte(jsonText(meta)))); err != nil {
		t.Fatal(err)
	}
	if chat.metaJSON != nil {
		writeSourceFile(t, filepath.Join(dir, "meta.json"), jsonText(chat.metaJSON))
	}
	return dir
}

// cursorCLIFixture writes the CLI's folder: the conversation, a short chat
// without meta.json, placed by its root, and the CLI's sign-in.
func cursorCLIFixture(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), ".cursor")
	cursorCLIWriteChat(t, root, cursorCLIConversation())
	cursorCLIWriteChat(t, root, cursorCLIChatSpec{agent: cursorCLIChatB, folder: "/work/docs",
		messages: []any{
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "<user_query>Summarize the docs</user_query>"}}},
			map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "They cover setup."}}},
		},
		roots: [][]int{{0, 1}},
		meta:  map[string]any{"agentId": cursorCLIChatB, "name": "New Agent", "createdAt": "1793809020000", "lastUsedModel": "cursor-small", "blobEncryptionKey": cursorCLISecret},
	})
	writeSourceFile(t, filepath.Join(root, "cli-config.json"), `{"authInfo":{"token":"`+cursorCLISecret+`"}}`)
	return root
}

func cursorCLISource(root string) SourceConfig {
	return SourceConfig{Name: "cursor-cli", Kind: "cursor-cli", Path: root, Account: "local", Enabled: true}
}

func cursorCLIRecords(t *testing.T, root string) map[string]WorkspaceRecord {
	t.Helper()
	adapter, err := MakeAdapter(cursorCLISource(root))
	if err != nil {
		t.Fatal(err)
	}
	records := map[string]WorkspaceRecord{}
	if err := adapter.Discover(func(record WorkspaceRecord) error { records[record.SourceID] = record; return nil }); err != nil {
		t.Fatal(err)
	}
	return records
}

// cursorCLIStore changes a chat's database.
func cursorCLIStore(t *testing.T, dir string, write func(db *sql.DB) error) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, "store.db"))
	if err == nil {
		err = write(db)
		db.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestCursorCLIChatKeepsItsTurnsAndTools(t *testing.T) {
	records := cursorCLIRecords(t, cursorCLIFixture(t))
	if len(records) != 2 {
		t.Fatalf("records = %v", records)
	}
	record := records[cursorCLIChatA]
	// What was rewound is neither the work's outcome nor its model.
	if record.SourceKind != "cursor-cli" || record.Title != "Fix the build" || record.Outcome != "Fixed." || record.Location != "/work/app" || record.Metadata["mode"] != "agent" || len(record.Conversations) != 1 {
		t.Fatalf("record = %+v", record)
	}
	conversation := record.Conversations[0]
	if conversation.Provider != "cursor" || conversation.Harness != "cursor-cli" || conversation.Model != "cursor-test-high" || conversation.Coverage != "complete" ||
		conversation.StartedAt != "2026-11-04T16:17:00.000Z" || conversation.EndedAt != "2026-11-04T16:20:00.000Z" ||
		conversation.NativeContext["context_tokens_used"] != int64(1200) || conversation.NativeContext["context_token_limit"] != int64(200000) {
		t.Fatalf("conversation = %+v", conversation)
	}
	// The CLI's own system prompt and context are not the person's; the
	// rewound reply follows what the latest root holds, not selected.
	if got, want := cursorKinds(conversation.Messages), "user:message assistant:metadata assistant:message assistant:tool_call tool:tool_result assistant:tool_call tool:tool_result assistant:message assistant:message"; got != want {
		t.Fatalf("kinds = %s, want %s", got, want)
	}
	messages := conversation.Messages
	if messages[0].Text != "Fix the build" || messages[0].CreatedAt != "2026-11-04T16:18:00.000Z" {
		t.Fatalf("prompt = %+v", messages[0])
	}
	// A prompt quoting the CLI's own tags is read whole.
	if prompt, _ := cursorCLIPrompt("<user_query>Explain <user_query>x</user_query> and fix it</user_query>"); prompt != "Explain <user_query>x</user_query> and fix it" {
		t.Fatalf("prompt quoting the tags = %q", prompt)
	}
	if !strings.Contains(messages[1].Text, "encrypted") {
		t.Fatalf("thinking = %+v", messages[1])
	}
	failed, edited := mapValueDefault(parseJSONValue(messages[4].Text)), mapValueDefault(parseJSONValue(messages[6].Text))
	if failed["is_error"] != true || jsonText(failed["details"]) != `{"duration_ms":120,"exit_code":2}` || messages[4].CallID != "call1" {
		t.Fatalf("failed call = %s", messages[4].Text)
	}
	if edited["is_error"] != false || jsonText(edited["details"]) != `{"duration_ms":5}` {
		t.Fatalf("edit = %s", messages[6].Text)
	}
	for index, message := range messages {
		if message.Selected != (index < len(messages)-1) {
			t.Fatalf("message %d (%s) selected = %v", index, message.Text, message.Selected)
		}
	}
	if messages[len(messages)-1].Text != "Abandoned reply." || messages[len(messages)-1].Model != "cursor-old" || messages[2].Model != "cursor-test-high" {
		t.Fatalf("rewound = %+v", messages[len(messages)-1])
	}
	// Without meta.json, the folder is the root's and the title the prompt;
	// the model is the one the chat last used.
	other := records[cursorCLIChatB]
	if other.Location != "/work/docs" || other.Title == "New Agent" || other.Conversations[0].Model != "cursor-small" {
		t.Fatalf("chat without meta.json = %+v", other)
	}
}

// A message whose blob the CLI no longer holds makes the chat partial, and
// what was indexed from it stays: one the latest root lists, one only an
// older root does, and the latest root itself. So does a message an older
// root repeats.
func TestCursorCLIMissingMessageStaysIndexed(t *testing.T) {
	c, _ := testCatalog(t)
	root := filepath.Join(t.TempDir(), ".cursor")
	dir := cursorCLIWriteChat(t, root, cursorCLIConversation())
	source := cursorCLISource(root)
	ingestSource(t, c, source)
	before := countRows(t, c, `SELECT COUNT(*) FROM messages`)
	reply := cursorCLIBlobID([]byte(jsonText(cursorCLIConversation().messages[7])))
	cursorCLIStore(t, dir, func(db *sql.DB) error {
		_, err := db.Exec(`DELETE FROM blobs WHERE id=?`, reply)
		return err
	})
	record := cursorCLIRecords(t, root)[cursorCLIChatA].Conversations[0]
	if record.Coverage != "partial" || strings.Join(record.Unread, ",") != reply[:16] {
		t.Fatalf("coverage %s, unread %v", record.Coverage, record.Unread)
	}
	ingestSource(t, c, source)
	if after := countRows(t, c, `SELECT COUNT(*) FROM messages`); after != before || countRows(t, c, `SELECT COUNT(*) FROM messages WHERE text='Fixed.'`) != 1 {
		t.Fatalf("messages %d, before %d", after, before)
	}
	rewound := cursorCLIBlobID([]byte(jsonText(cursorCLIConversation().messages[8])))
	cursorCLIStore(t, dir, func(db *sql.DB) error {
		_, err := db.Exec(`DELETE FROM blobs WHERE id=?`, rewound)
		return err
	})
	if record := cursorCLIRecords(t, root)[cursorCLIChatA].Conversations[0]; record.Coverage != "partial" || !slices.Contains(record.Unread, rewound[:16]) {
		t.Fatalf("without an older root's message: coverage %s, unread %v", record.Coverage, record.Unread)
	}
	ingestSource(t, c, source)
	if after := countRows(t, c, `SELECT COUNT(*) FROM messages`); after != before {
		t.Fatalf("without an older root's message: messages %d, before %d", after, before)
	}
	// Nor is it lost once nothing that older root lists is left.
	cursorCLIStore(t, dir, func(db *sql.DB) error {
		for _, message := range cursorCLIConversation().messages[:3] {
			if _, err := db.Exec(`DELETE FROM blobs WHERE id=?`, cursorCLIBlobID([]byte(jsonText(message)))); err != nil {
				return err
			}
		}
		return nil
	})
	if record := cursorCLIRecords(t, root)[cursorCLIChatA].Conversations[0]; !slices.Contains(record.Unread, rewound[:16]) {
		t.Fatalf("without what an older root lists: unread %v", record.Unread)
	}
	ingestSource(t, c, source)
	if n := countRows(t, c, `SELECT COUNT(*) FROM messages WHERE text='Abandoned reply.'`); n != 1 {
		t.Fatalf("without what an older root lists: %d rewound replies", n)
	}
	// The latest root is cut short, as one being written: the longest root
	// left orders what it lists.
	c, _ = testCatalog(t)
	root = filepath.Join(t.TempDir(), ".cursor")
	dir = cursorCLIWriteChat(t, root, cursorCLIConversation())
	source = cursorCLISource(root)
	ingestSource(t, c, source)
	var latest string
	cursorCLIStore(t, dir, func(db *sql.DB) error {
		var value string
		if err := db.QueryRow(`SELECT value FROM meta`).Scan(&value); err != nil {
			return err
		}
		raw, _ := hex.DecodeString(value)
		latest = firstString(mapValueDefault(parseJSONValue(string(raw)))["latestRootBlobId"])
		_, err := db.Exec(`UPDATE blobs SET data=substr(data, 1, 100) WHERE id=?`, latest)
		return err
	})
	if record := cursorCLIRecords(t, root)[cursorCLIChatA].Conversations[0]; record.Coverage != "partial" || record.Unread != nil || !record.Messages[0].Selected {
		t.Fatalf("with the latest root cut short: coverage %s, unread %v", record.Coverage, record.Unread)
	}
	ingestSource(t, c, source)
	if after := countRows(t, c, `SELECT COUNT(*) FROM messages`); after != before {
		t.Fatalf("with the latest root cut short: messages %d, before %d", after, before)
	}

	// An older root cut short, or overwritten with what looks like a
	// message: what it listed is not known, so is kept.
	for _, damage := range []string{`substr(data, 1, 50)`, `'{'`} {
		c, _ = testCatalog(t)
		root = filepath.Join(t.TempDir(), ".cursor")
		dir = cursorCLIWriteChat(t, root, cursorCLIConversation())
		source = cursorCLISource(root)
		ingestSource(t, c, source)
		cursorCLIStore(t, dir, func(db *sql.DB) error {
			_, err := db.Exec(`UPDATE blobs SET data=`+damage+` WHERE id=?`, cursorCLIBlobID(cursorCLIRootBlob(cursorCLIRootIDs(cursorCLIConversation(), 0), "/work/app")))
			return err
		})
		if record := cursorCLIRecords(t, root)[cursorCLIChatA].Conversations[0]; record.Coverage != "partial" || record.Unread != nil {
			t.Fatalf("with an older root's data %s: coverage %s, unread %v", damage, record.Coverage, record.Unread)
		}
		ingestSource(t, c, source)
		if n := countRows(t, c, `SELECT COUNT(*) FROM messages WHERE text='Abandoned reply.'`); n != 1 {
			t.Fatalf("with an older root's data %s: %d rewound replies", damage, n)
		}
	}

	// A prompt sent twice, then rewound to before the second.
	c, _ = testCatalog(t)
	root = filepath.Join(t.TempDir(), ".cursor")
	chat := cursorCLIConversation()
	chat.roots = [][]int{{0, 1, 2, 3, 2}, {0, 1, 2, 3}}
	cursorCLIWriteChat(t, root, chat)
	source = cursorCLISource(root)
	ingestSource(t, c, source)
	if n := countRows(t, c, `SELECT COUNT(*) FROM messages WHERE text='Fix the build'`); n != 2 {
		t.Fatalf("a prompt an older root repeats: %d copies", n)
	}
}

func TestCursorCLIReadsOnlyTheChatThatChanged(t *testing.T) {
	c, _ := testCatalog(t)
	root := cursorCLIFixture(t)
	source := cursorCLISource(root)
	ingestSource(t, c, source)
	if parsed := ingestSource(t, c, source).Parsed; parsed != 0 {
		t.Fatalf("unchanged: %d chats parsed", parsed)
	}
	// A turn: a new reply and the root that lists it. The chat keeps the
	// CLI's default name.
	chat := cursorCLIConversation()
	chat.meta["name"] = "New Agent"
	chat.messages = append(chat.messages, map[string]any{"role": "assistant", "id": "a4", "content": []any{map[string]any{"type": "text", "text": "Also ran the tests."}}})
	chat.roots = append(chat.roots, []int{0, 1, 2, 3, 4, 5, 6, 7, 9})
	dir := filepath.Join(root, "chats", hashBytes([]byte(chat.folder))[:32], chat.agent)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	cursorCLIWriteChat(t, root, chat)
	parsed := ingestSource(t, c, source).Parsed
	if n := countRows(t, c, `SELECT COUNT(*) FROM messages WHERE text='Also ran the tests.'`); n != 1 || parsed != 1 {
		t.Fatalf("after a turn: %d copies, %d chats parsed", n, parsed)
	}
	// Renamed: only meta.json changes.
	chat.metaJSON["title"] = "Fix the build and tests"
	writeSourceFile(t, filepath.Join(dir, "meta.json"), jsonText(chat.metaJSON))
	chat.metaJSON["updatedAtMs"] = 1793809300000
	writeSourceFile(t, filepath.Join(dir, "meta.json"), jsonText(chat.metaJSON))
	if parsed := ingestSource(t, c, source).Parsed; parsed != 1 || countRows(t, c, `SELECT COUNT(*) FROM conversations WHERE ended_at='2026-11-04T16:21:40.000Z'`) != 1 ||
		countRows(t, c, `SELECT COUNT(*) FROM workspaces WHERE title='Fix the build and tests'`) != 1 {
		t.Fatalf("after meta.json changed: %d chats parsed", parsed)
	}
}

func TestCursorCLICaptureHoldsOnlyTheChats(t *testing.T) {
	useHost(t, "host-a")
	root := cursorCLIFixture(t)
	source := cursorCLISource(root)
	plan, err := planCapture(source)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, item := range append(plan.databases, plan.files...) {
		got = append(got, filepath.Base(filepath.Dir(item.rel))+"/"+filepath.Base(item.rel))
	}
	slices.Sort(got)
	if strings.Join(got, ",") != cursorCLIChatA+"/meta.json,"+cursorCLIChatA+"/store.db,"+cursorCLIChatB+"/store.db" {
		t.Fatalf("capture plan = %v", got)
	}
	secret := func(text string) bool {
		return strings.Contains(text, cursorCLISecret) || strings.Contains(strings.ToLower(text), hex.EncodeToString([]byte(cursorCLISecret)))
	}
	// Metadata naming the key, and the chat, twice, as JSON allows.
	cursorCLIStore(t, filepath.Join(root, "chats", hashBytes([]byte("/work/docs"))[:32], cursorCLIChatB), func(db *sql.DB) error {
		_, err := db.Exec(`UPDATE meta SET value=hex(substr(CAST(unhex(value) AS TEXT), 1, length(CAST(unhex(value) AS TEXT))-1) || ?)`, `,"blobEncryptionKey":"`+cursorCLISecret+`","name":"Docs summary"}`)
		return err
	})
	c, config := testCatalog(t)
	config.Sources = []SourceConfig{source}
	ingestSource(t, c, source)
	// A capture stopped as a snapshot is written, before it is kept, leaves
	// nothing behind it should not hold.
	stopped := 0
	override(t, &captureFault, func(stage, rel string) error {
		if stage != "snapshot-written" {
			return nil
		}
		stopped++
		if secret(capturedText(t, config.CaptureRoot)) {
			t.Errorf("a capture stopped at %s holds a chat's key", rel)
		}
		return errors.New("stopped")
	})
	runCapture(t, config)
	captureFault = nil
	if stopped == 0 {
		t.Fatal("no snapshot was written")
	}
	if result := runCapture(t, config); !result.OK {
		t.Fatalf("capture = %+v", result)
	}
	if secret(capturedText(t, config.CaptureRoot)) {
		t.Fatal("the capture holds a chat's key or the CLI's sign-in")
	}
	messages := func(c *Catalog) int {
		return countRows(t, c, `SELECT COUNT(*) FROM messages m JOIN conversations v ON v.id=m.conversation_id WHERE v.harness='cursor-cli'`)
	}
	live := messages(c)
	useHost(t, "host-b")
	other, _ := testCatalog(t)
	indexCaptures(t, other, config.CaptureRoot, "host-a")
	if captured := messages(other); captured != live || live == 0 || countRows(t, other, `SELECT COUNT(*) FROM workspaces WHERE location IN ('/work/app','/work/docs')`) != 2 {
		t.Fatalf("captured %d messages, live %d", captured, live)
	}
	titles := func(c *Catalog) (titles string) {
		if err := c.DB.QueryRow(`SELECT group_concat(title, ',') FROM (SELECT title FROM workspaces ORDER BY source_id)`).Scan(&titles); err != nil {
			t.Fatal(err)
		}
		return titles
	}
	if got, want := titles(other), titles(c); got != want {
		t.Fatalf("captured titles %s, live %s", got, want)
	}
}

// A chat deleted before its last capture was indexed is indexed from the
// capture that holds it, and one indexed newer is not rolled back.
func TestCursorCLIChatDeletedBeforeIndexingIsKept(t *testing.T) {
	useHost(t, "host-a")
	root := cursorCLIFixture(t)
	source := cursorCLISource(root)
	config := captureTestConfig(t, source)
	runCapture(t, config)
	chat := cursorCLIConversation()
	dir := filepath.Join(root, "chats", hashBytes([]byte(chat.folder))[:32], chat.agent)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	runCapture(t, config)
	useHost(t, "host-b")
	c, _ := testCatalog(t)
	indexCaptures(t, c, config.CaptureRoot, "host-a")
	live, _ := testCatalog(t)
	ingestSource(t, live, cursorCLISource(cursorCLIFixture(t)))
	if got, want := cursorShape(t, c), cursorShape(t, live); got != want {
		t.Fatalf("salvaged %s, read live %s", got, want)
	}

	useHost(t, "host-a")
	root = cursorCLIFixture(t)
	source = cursorCLISource(root)
	config = captureTestConfig(t, source)
	runCapture(t, config)
	chat.messages = append(chat.messages, map[string]any{"role": "assistant", "id": "a4", "content": []any{map[string]any{"type": "text", "text": "Also ran the tests."}}})
	chat.roots = append(chat.roots, []int{0, 1, 2, 3, 4, 5, 6, 7, 9})
	dir = filepath.Join(root, "chats", hashBytes([]byte(chat.folder))[:32], chat.agent)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	cursorCLIWriteChat(t, root, chat)
	c, _ = testCatalog(t)
	ingestSource(t, c, source)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	runCapture(t, config)
	indexCaptures(t, c, config.CaptureRoot, "host-a")
	if n := countRows(t, c, `SELECT COUNT(*) FROM messages WHERE text='Also ran the tests.'`); n != 1 {
		t.Fatalf("the chat was rolled back: %d", n)
	}

	// Nor is a chat renamed in meta.json alone, under the CLI's default name.
	root = filepath.Join(t.TempDir(), ".cursor")
	chat = cursorCLIConversation()
	chat.meta["name"] = "New Agent"
	dir = cursorCLIWriteChat(t, root, chat)
	source = cursorCLISource(root)
	config = captureTestConfig(t, source)
	runCapture(t, config)
	chat.metaJSON["title"] = "Fix the build and tests"
	writeSourceFile(t, filepath.Join(dir, "meta.json"), jsonText(chat.metaJSON))
	// After the database last changed, as a rename is.
	if renamed := time.Now().Add(time.Minute); os.Chtimes(filepath.Join(dir, "meta.json"), renamed, renamed) != nil {
		t.Fatal("meta.json")
	}
	c, _ = testCatalog(t)
	ingestSource(t, c, source)
	indexCaptures(t, c, config.CaptureRoot, "host-a")
	if n := countRows(t, c, `SELECT COUNT(*) FROM workspaces WHERE title='Fix the build and tests'`); n != 1 {
		t.Fatalf("the rename was rolled back: %d", n)
	}
}

func TestCursorCLIChatIsVerifiedLive(t *testing.T) {
	c, _ := testCatalog(t)
	source := cursorCLISource(cursorCLIFixture(t))
	ingestSource(t, c, source)
	if result := c.auditUnit(t.Context(), firstSyncUnit(t, c, source), "manual", 0); result.Outcome != "passed" {
		t.Fatalf("verification: %+v", result)
	}
}

// The CLI is found and counted by the probe, offered to a Mac set up before
// Pharos read it, and chosen in guided setup; a ~/.cursor without chats, as
// the editor leaves, is not offered.
func TestCursorCLIIsFoundAndSetUp(t *testing.T) {
	home := probeHome(t)
	useHost(t, "host-a")
	probePut(t, filepath.Join(home, ".claude", "projects", "-r", probeID1+".jsonl"), "{}\n")
	probePut(t, filepath.Join(home, ".cursor", "extensions", "extensions.json"), "[]")
	if tools := probeTools(home, Config{}); strings.Contains(jsonText(tools), "cursor-cli") {
		t.Fatalf("the editor's folder was found as the CLI: %v", tools)
	}
	for _, candidate := range probeSources(home, Config{}, nil).Candidates {
		if candidate.Kind == "cursor-cli" {
			t.Fatalf("a folder without chats was offered: %+v", candidate)
		}
	}
	cursorCLIWriteChat(t, filepath.Join(home, ".cursor"), cursorCLIConversation())
	candidate := candidateNamed(t, probeSources(home, Config{}, nil), "cursor-cli")
	if candidate.Kind != "cursor-cli" || candidate.Status != "found" || candidate.Count != 1 || candidate.Unit != "session" || !candidate.Recommended {
		t.Fatalf("candidate = %+v", candidate)
	}
	server := probeServer(t, probeLibrary(t, ""))
	if code, result := probeCall(t, server, http.MethodPost, "/api/probe/accept", `{"accept":["claude"]}`); code != 200 {
		t.Fatalf("accept = %d %v", code, result)
	}
	if _, status := probeCall(t, server, http.MethodGet, "/api/probe/status", ""); status["new_sources"] != float64(1) {
		t.Fatalf("status of a Mac set up before the CLI = %v", status)
	}
	if code, result := probeCall(t, server, http.MethodPost, "/api/probe/accept", `{"kinds":["cursor-cli"],"accept":["cursor-cli"]}`); code != 200 || jsonText(result["added"]) != `["cursor-cli"]` {
		t.Fatalf("accept the CLI = %d %v", code, result)
	}
}

// What a rewind left behind is timed by its own prompts, and is neither the
// work's outcome nor its evidence, even when the latest turn has no reply.
func TestCursorCLIRewoundRepliesAreNotTheOutcome(t *testing.T) {
	c, _ := testCatalog(t)
	root := filepath.Join(t.TempDir(), ".cursor")
	chat := cursorCLIConversation()
	chat.messages = append(chat.messages, map[string]any{"role": "user", "id": "u2", "content": []any{map[string]any{"type": "text",
		"text": "<timestamp>Wednesday, Nov 4, 2026, 2:05 PM (UTC-5)</timestamp>\n<user_query>Now the docs</user_query>"}}})
	chat.roots = append(chat.roots, []int{0, 1, 2, 3, 4, 5, 6, 7, 9})
	cursorCLIWriteChat(t, root, chat)
	conversation := cursorCLIRecords(t, root)[cursorCLIChatA].Conversations[0]
	rewound := conversation.Messages[len(conversation.Messages)-1]
	if rewound.Text != "Abandoned reply." || rewound.CreatedAt != "2026-11-04T16:18:00.000Z" || conversation.EndedAt != "2026-11-04T19:05:00.000Z" {
		t.Fatalf("rewound reply at %s, conversation ended %s", rewound.CreatedAt, conversation.EndedAt)
	}
	ingestSource(t, c, cursorCLISource(root))
	var outcome, evidence string
	if err := c.DB.QueryRow(`SELECT COALESCE(outcome, ''), evidence_json FROM summaries`).Scan(&outcome, &evidence); err != nil {
		t.Fatal(err)
	}
	if fixed := cursorCLIBlobID([]byte(jsonText(chat.messages[7]))); outcome != "Fixed." || !strings.Contains(evidence, fixed) {
		t.Fatalf("outcome %q, evidence %s", outcome, evidence)
	}

	// Rewound to the prompt alone.
	c, _ = testCatalog(t)
	root = filepath.Join(t.TempDir(), ".cursor")
	chat = cursorCLIConversation()
	chat.roots = [][]int{{0, 1, 2, 8}, {0, 1, 2}}
	cursorCLIWriteChat(t, root, chat)
	ingestSource(t, c, cursorCLISource(root))
	if err := c.DB.QueryRow(`SELECT COALESCE(outcome, ''), evidence_json FROM summaries`).Scan(&outcome, &evidence); err != nil {
		t.Fatal(err)
	}
	if outcome != "" || strings.Contains(evidence, cursorCLIBlobID([]byte(jsonText(chat.messages[8])))) {
		t.Fatalf("with only a rewound reply: outcome %q, evidence %s", outcome, evidence)
	}
}

// A folder of chats that cannot be listed leaves the others to be indexed
// and captured.
func TestCursorCLIUnreadableFolderLeavesTheOthers(t *testing.T) {
	useHost(t, "host-a")
	root := cursorCLIFixture(t)
	folder := filepath.Join(root, "chats", hashBytes([]byte("/work/docs"))[:32])
	if err := os.Chmod(folder, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(folder, 0o755) })
	adapter, err := MakeAdapter(cursorCLISource(root))
	if err != nil {
		t.Fatal(err)
	}
	records := map[string]bool{}
	err = adapter.Discover(func(record WorkspaceRecord) error { records[record.SourceID] = true; return nil })
	if err == nil || !records[cursorCLIChatA] {
		t.Fatalf("read %v, error %v", records, err)
	}
	config := captureTestConfig(t, cursorCLISource(root))
	runCapture(t, config)
	store := filepath.Join("files", "chats", hashBytes([]byte("/work/app"))[:32], cursorCLIChatA, "store.db")
	if _, err := os.Stat(capturedPath(config, "cursor-cli", store)); err != nil {
		t.Fatalf("the readable chat was not captured: %v", err)
	}
	// So does a chat whose own folder cannot be read, and it is reported.
	if err := os.Chmod(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	chat := filepath.Join(folder, cursorCLIChatB)
	if err := os.Chmod(chat, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(chat, 0o755) })
	records = map[string]bool{}
	err = adapter.Discover(func(record WorkspaceRecord) error { records[record.SourceID] = true; return nil })
	if err == nil || !records[cursorCLIChatA] {
		t.Fatalf("with a chat's folder unreadable: read %v, error %v", records, err)
	}
}
