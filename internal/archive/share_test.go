package archive

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func seedSharedWork(t *testing.T, catalog *Catalog) string {
	t.Helper()
	record := WorkspaceRecord{
		SourceID: "share-me", SourceKind: "claude", Account: "gbw@example.com", Title: "Fix the </script> parser",
		Conversations: []ConversationRecord{{
			NativeID: "thread", Provider: "claude", Account: "gbw@example.com", Model: "claude-opus-5-5", Origin: "gbws-macbook",
			Messages: []MessageRecord{
				{NativeID: "u1", Role: "user", Kind: "message", Text: "Please fix the parser </script><script>alert(1)</script>", Selected: true, EvidenceLocator: "/Users/gbw/.claude/projects/secret.jsonl:1"},
				{NativeID: "a1", Role: "assistant", Kind: "message", Text: "Done.", Selected: true},
			},
		}},
	}
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
	rows, err := queryMaps(catalog.DB, "SELECT id FROM workspaces WHERE source_id='share-me'")
	if err != nil || len(rows) != 1 {
		t.Fatalf("workspace rows = %v %v", rows, err)
	}
	return firstString(rows[0]["id"])
}

func TestSharedWorkIsStandaloneAndOmitsArchiveDetails(t *testing.T) {
	catalog, config := testCatalog(t)
	id := seedSharedWork(t, catalog)
	response := serveTest(NewServer(config, catalog), http.MethodGet, "/api/share/work/"+url.PathEscape(id))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	if disposition := response.Header().Get("Content-Disposition"); !strings.HasPrefix(disposition, "attachment;") || !strings.Contains(disposition, "pharos-fix-the-script-parser.html") {
		t.Fatalf("Content-Disposition = %q", disposition)
	}
	page := response.Body.String()
	// Nothing may be fetched from the service that is no longer there.
	if strings.Contains(page, `"/assets/`) || strings.Contains(page, "<script src=") || strings.Contains(page, "<link ") {
		t.Fatalf("page still loads external files: %v", regexp.MustCompile(`<(script src|link )[^>]*>`).FindAllString(page, -1))
	}
	if !strings.Contains(page, "Content-Security-Policy") || !strings.Contains(page, "Shared from a") || !strings.Contains(page, shareRepositoryURL) {
		t.Fatal("page lacks its policy or its attribution footer")
	}
	// The conversation's own text must not be able to close the data element.
	data := regexp.MustCompile(`(?s)<script type="application/json" id="pharosShareData">(.*?)</script>`).FindStringSubmatch(page)
	if data == nil || strings.Contains(data[1], "</") || !strings.Contains(data[1], "Please fix the parser") {
		t.Fatalf("embedded data = %.200q", data)
	}
	for _, private := range []string{"gbw@example.com", "gbws-macbook", "secret.jsonl", "content_hash", "evidence_locator"} {
		if strings.Contains(page, private) {
			t.Errorf("page leaks %q", private)
		}
	}
	if !strings.Contains(page, "<title>Fix the &lt;/script&gt; parser · Pharos</title>") {
		t.Error("title is missing or unescaped")
	}
}

func TestSharedWorkRefusesWorkWithoutConversation(t *testing.T) {
	catalog, config := testCatalog(t)
	server := NewServer(config, catalog)
	if response := serveTest(server, http.MethodGet, "/api/share/work/missing"); response.Code != http.StatusNotFound {
		t.Fatalf("missing work status = %d", response.Code)
	}
	if response := serveTest(server, http.MethodGet, "/api/share"); response.Code != http.StatusNotFound {
		t.Fatalf("empty selection status = %d", response.Code)
	}
	tx, err := catalog.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ingestWorkspace(tx, WorkspaceRecord{SourceID: "empty", SourceKind: "claude", Account: "local", Title: "Empty"}, false); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	rows, _ := queryMaps(catalog.DB, "SELECT id FROM workspaces WHERE source_id='empty'")
	if len(rows) != 1 {
		t.Fatalf("rows = %v", rows)
	}
	if response := serveTest(server, http.MethodGet, "/api/share/work/"+url.PathEscape(firstString(rows[0]["id"]))); response.Code != http.StatusNotFound {
		t.Fatalf("conversation-less work status = %d", response.Code)
	}
}

func TestSharedFilename(t *testing.T) {
	if got := sharedFilename("ignored", 3); got != "pharos-3-conversations.html" {
		t.Errorf("sharedFilename for several works = %q", got)
	}
	for title, want := range map[string]string{
		"Fix the parser": "pharos-fix-the-parser.html", "": "pharos-conversation.html", "  ✨ ": "pharos-conversation.html",
		strings.Repeat("word ", 30): "pharos-" + strings.Repeat("word-", 11) + "word.html",
	} {
		if got := sharedFilename(title, 1); got != want {
			t.Errorf("sharedFilename(%q) = %q, want %q", title, got, want)
		}
	}
}

func sharedPayload(t *testing.T, page []byte) (payload struct {
	Works    []map[string]any            `json:"works"`
	Datasets map[string][]map[string]any `json:"datasets"`
	Calls    map[string]map[string]any   `json:"tool_calls"`
}) {
	t.Helper()
	match := regexp.MustCompile(`(?s)<script type="application/json" id="pharosShareData">(.*?)</script>`).FindSubmatch(page)
	if match == nil {
		t.Fatal("page has no embedded data")
	}
	if err := json.Unmarshal(match[1], &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestSharedExportScopesTablesToItsWorks(t *testing.T) {
	catalog, _ := testCatalog(t)
	seedToolCalls(t, catalog, 300)
	if _, err := catalog.DB.Exec("UPDATE workspaces SET location='/Users/someone/secret', owner='someone'"); err != nil {
		t.Fatal(err)
	}
	page, name, err := catalog.SharedHTML(context.Background(), []string{"ws-1", "ws-0", "ws-1"})
	if err != nil {
		t.Fatal(err)
	}
	if name != "pharos-2-conversations.html" {
		t.Errorf("file name = %q", name)
	}
	payload := sharedPayload(t, page)
	if len(payload.Works) != 2 || len(payload.Datasets["library"]) != 2 {
		t.Fatalf("works = %d, library rows = %d; want 2 each", len(payload.Works), len(payload.Datasets["library"]))
	}
	var want int
	if err := catalog.DB.QueryRow("SELECT COUNT(*) FROM tool_calls WHERE workspace_id IN ('ws-0','ws-1') AND workspace_id NOT IN (SELECT workspace_id FROM tool_mirror_workspaces)").Scan(&want); err != nil {
		t.Fatal(err)
	}
	calls := payload.Datasets["tool_calls"]
	if want == 0 || len(calls) != want || len(payload.Calls) != want {
		t.Fatalf("tool calls = %d (details %d), want %d", len(calls), len(payload.Calls), want)
	}
	for _, call := range calls {
		if workspace := firstString(call["workspace_id"]); workspace != "ws-0" && workspace != "ws-1" {
			t.Fatalf("call from outside the export: %v", call)
		}
	}
	// The Tools summary regroups those same calls, and only those.
	var summarized int64
	for _, row := range payload.Datasets["tools"] {
		summarized += int64(row["call_count"].(float64))
	}
	if summarized != int64(want) {
		t.Errorf("tools summary counts %d calls, want %d", summarized, want)
	}
	for _, row := range payload.Datasets["library"] {
		for _, private := range []string{"location", "owner"} {
			if _, ok := row[private]; ok {
				t.Errorf("library row keeps %s", private)
			}
		}
	}
}

func TestSharedMessagesKeepOnlyListedFields(t *testing.T) {
	kept := sharedMessages([]map[string]any{{
		"id": "m1", "native_id": "n1", "role": "user", "kind": "message", "text": "hello", "created_at": "2026-09-01T00:00:00Z",
		"evidence_locator": "/Users/gbw/x.jsonl:3", "content_hash": "abc", "sender": "someone", "future_column": "would leak",
		"authorship": map[string]any{"typed_words": 1, "pasted_words": 0, "spans": []map[string]any{
			{"category": "typed", "words": 1, "excerpt": "hello", "source": map[string]any{"title": "another conversation"}},
		}},
	}})
	encoded, _ := json.Marshal(kept)
	for _, leaked := range []string{"evidence_locator", "content_hash", "sender", "future_column", "would leak", "another conversation"} {
		if strings.Contains(string(encoded), leaked) {
			t.Errorf("shared message leaks %q: %s", leaked, encoded)
		}
	}
	if kept[0]["text"] != "hello" || kept[0]["id"] != "m1" {
		t.Errorf("shared message lost what the reader needs: %s", encoded)
	}
}

func TestSharedCheckReportsFailureInsteadOfAFile(t *testing.T) {
	catalog, config := testCatalog(t)
	server := NewServer(config, catalog)
	id := seedSharedWork(t, catalog)
	ok := serveTest(server, http.MethodGet, "/api/share?check=1&id="+url.QueryEscape(id))
	if ok.Code != http.StatusOK || strings.HasPrefix(ok.Header().Get("Content-Disposition"), "attachment") || !strings.Contains(ok.Body.String(), `"filename"`) {
		t.Fatalf("check = %d %q %.100s", ok.Code, ok.Header().Get("Content-Disposition"), ok.Body.String())
	}
	// No selected work has a conversation: a JSON error, never an attachment.
	if _, err := catalog.DB.Exec("DELETE FROM conversations"); err != nil {
		t.Fatal(err)
	}
	failed := serveTest(server, http.MethodGet, "/api/share?check=1&id="+url.QueryEscape(id))
	if failed.Code != http.StatusNotFound || !strings.Contains(failed.Body.String(), "error") {
		t.Fatalf("failed check = %d %s", failed.Code, failed.Body.String())
	}
}
