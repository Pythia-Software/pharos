package archive

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestMCPConversationSearchRanking(t *testing.T) {
	catalog, _ := testCatalog(t)
	message := func(id, role, text string) map[string]any {
		return map[string]any{"id": id, "role": role, "text": text}
	}
	conversation := func(id string, messages ...map[string]any) map[string]any {
		return map[string]any{"id": id, "provider": "codex", "messages": messages}
	}
	document := map[string]any{"workspaces": []any{
		map[string]any{"id": "work-1", "title": "Parser maintenance", "activity_at": "2026-09-20T12:00:00Z",
			"repository": map[string]any{"canonical_remote": "github.com/acme/parser", "display_name": "parser"},
			"conversations": []any{
				conversation("parser-session", message("p1", "user", "Fix the tokenizer parser bug"), message("p2", "assistant", "Fixed the tokenizer; tests pass")),
				conversation("lexer-session", message("l1", "user", "Speed up the lexer"), message("l2", "assistant", "The parser now streams tokens")),
			}},
		map[string]any{"id": "work-2", "title": "Dashboard colors", "activity_at": "2026-09-21T12:00:00Z",
			"repository": map[string]any{"canonical_remote": "github.com/acme/web", "display_name": "web"},
			"conversations": []any{
				conversation("css-session", message("c1", "user", "Improve the dashboard colors"), message("c2", "assistant", "Updated the palette")),
				conversation("chart-session", message("h1", "user", "The chart parser fails on dates"), message("h2", "assistant", "Parsed dates in UTC")),
				conversation("empty-session"),
			}},
	}}
	payload, _ := json.Marshal(document)
	export := filepath.Join(t.TempDir(), "export.json")
	if err := os.WriteFile(export, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	adapter, err := MakeAdapter(SourceConfig{Name: "fixture", Kind: "canonical", Path: export, Account: "local", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if result := catalog.Ingest(adapter, nil); result.Error != nil {
		t.Fatal(result.Error)
	}
	// native names a result by its conversation and the message its snippet
	// comes from, with why it matched.
	native := func(item map[string]any) string {
		var conversation, message string
		if err := catalog.DB.QueryRow("SELECT native_id FROM conversations WHERE id=?", item["conversation_id"]).Scan(&conversation); err != nil {
			t.Fatal(err)
		}
		if id := firstString(item["message_id"]); id != "" {
			if err := catalog.DB.QueryRow("SELECT native_id FROM messages WHERE id=?", id).Scan(&message); err != nil {
				t.Fatal(err)
			}
		}
		return fmt.Sprintf("%s/%s: %v", conversation, message, item["relevance_reason"])
	}
	// The default card budget fits three of these cards. Conversations of
	// one workspace with no start time tie on score and recency, and fall back
	// to conversation ID order.
	for _, test := range []struct {
		args  map[string]any
		want  []string
		total int
		next  any
	}{
		{map[string]any{"query": "parser"}, []string{
			"parser-session/p1: message text, conversation context, title",
			"lexer-session/l2: message text, conversation context, title",
			"chart-session/h1: message text, conversation context"}, 4, 3},
		{map[string]any{"query": "tokenizer bug"}, []string{"parser-session/p1: message text, conversation context"}, 1, nil},
		{map[string]any{"query": "dashboard"}, []string{
			"css-session/c1: message text, conversation context, title",
			"empty-session/: conversation context, title",
			"chart-session/h1: title"}, 3, nil},
		{map[string]any{"query": "palette colors"}, []string{"empty-session/: conversation context", "css-session/c1: conversation context"}, 2, nil},
		// The query is the title of work-1, so its conversations rank by context.
		{map[string]any{"query": "Parser maintenance"}, []string{
			"lexer-session/l1: conversation context, title",
			"parser-session/p1: conversation context, title",
			"chart-session/h1: conversation context"}, 3, nil},
		// Without a query, the most recent work comes first.
		{map[string]any{"query": ""}, []string{
			"empty-session/: structured filters or recent activity",
			"css-session/c1: structured filters or recent activity",
			"chart-session/h1: structured filters or recent activity"}, 5, 3},
		{map[string]any{"query": "", "repository": "web"}, []string{
			"empty-session/: structured filters or recent activity",
			"css-session/c1: structured filters or recent activity",
			"chart-session/h1: structured filters or recent activity"}, 3, nil},
		{map[string]any{"query": "parser", "repository": "parser"}, []string{
			"parser-session/p1: message text, conversation context, title",
			"lexer-session/l2: message text, conversation context, title"}, 2, nil},
		{map[string]any{"query": "parser", "limit": 1, "offset": 1}, []string{"lexer-session/l2: message text, conversation context, title"}, 4, 2},
		// A small budget keeps the best card, without its reason.
		{map[string]any{"query": "parser", "max_output_tokens": 200}, []string{"parser-session/p1: <nil>"}, 4, 1},
	} {
		got, err := catalog.searchConversations(test.args)
		if err != nil {
			t.Fatal(err)
		}
		items := []string{}
		for _, item := range got["items"].([]map[string]any) {
			items = append(items, native(item))
		}
		if !slices.Equal(items, test.want) || got["total"] != test.total || got["next_offset"] != test.next {
			t.Errorf("%v: items %q, total %v, next %v; want %q, %d, %v", test.args, items, got["total"], got["next_offset"], test.want, test.total, test.next)
		}
	}
	// Passages come from the chosen conversation only, best first.
	for _, query := range []string{"parser", "tokenizer", "dates"} {
		for _, id := range []string{"parser-session", "chart-session"} {
			conversations, _ := queryMaps(catalog.DB, "SELECT id FROM conversations WHERE native_id=?", id)
			got, err := catalog.conversationPassages(map[string]any{"conversation_id": conversations[0]["id"], "query": query})
			if err != nil {
				t.Fatal(err)
			}
			want, err := queryMaps(catalog.DB, `SELECT m.id message_id FROM messages_fts JOIN messages m ON m.id=messages_fts.message_id
				WHERE m.conversation_id=? AND messages_fts MATCH ? ORDER BY bm25(messages_fts) LIMIT 5`, conversations[0]["id"], ftsQuery(query))
			if err != nil {
				t.Fatal(err)
			}
			ids := []string{}
			for _, item := range got["items"].([]map[string]any) {
				ids = append(ids, firstString(item["message_id"]))
			}
			expected := []string{}
			for _, row := range want {
				expected = append(expected, firstString(row["message_id"]))
			}
			if strings.Join(ids, ",") != strings.Join(expected, ",") {
				t.Fatalf("passages %q in %s = %v, want %v", query, id, ids, expected)
			}
		}
	}
}

func TestQueryMetricsMatchesItsFullScan(t *testing.T) {
	catalog, _ := libraryFixture(t)
	for _, statement := range []string{
		`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES
			('d','codex','local','d','Delta','x'),('e','codex','local','e','Echo','x'),('f','codex','local','f','Foxtrot','x')`,
		`INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('cf','f','codex','local','cf')`,
		`INSERT INTO metrics(workspace_id,conversation_id,name,value,unit,status,extractor_version) VALUES
			('d',NULL,'score',5,'points','observed','v1'),('e',NULL,'score',NULL,'points','unknown','v1'),
			('f',NULL,'score',40,'points','observed','v1'),('f','cf','score',2,'points','observed','v1')`,
	} {
		if _, err := catalog.DB.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	key := func(row map[string]any) string {
		return fmt.Sprint(row["workspace_id"], "|", row["value"], "|", row["status"])
	}
	for _, args := range []map[string]any{
		{"name": "score"}, {"name": "score", "limit": 2}, {"name": "score", "minimum": 3}, {"name": "score", "maximum": 10},
		{"name": "score", "minimum": 3, "maximum": 10, "limit": 4}, {"name": "absent"}, {"name": "score", "limit": 1},
	} {
		result, err := callMCP(catalog, "query_metrics", args)
		if err != nil {
			t.Fatal(err)
		}
		got := result.(map[string]any)["items"].([]map[string]any)
		clauses, values := "m.name=?", []any{args["name"]}
		if args["minimum"] != nil {
			clauses += " AND (m.value>=? OR m.value IS NULL)"
			values = append(values, args["minimum"])
		}
		if args["maximum"] != nil {
			clauses += " AND (m.value<=? OR m.value IS NULL)"
			values = append(values, args["maximum"])
		}
		limit := int(integer(valueOr(args["limit"], 50)))
		want, err := queryMaps(catalog.DB, `SELECT w.id workspace_id,w.title,w.source_kind,m.value,m.unit,m.status,m.coverage,m.definition
			FROM workspaces w LEFT JOIN metrics m ON m.workspace_id=w.id AND `+clauses+` ORDER BY m.value IS NULL,m.value DESC LIMIT ?`, append(values, limit)...)
		if err != nil {
			t.Fatal(err)
		}
		want = catalog.suppressMirrors(want)
		if len(got) != len(want) {
			t.Fatalf("%v: %d rows, want %d", args, len(got), len(want))
		}
		// Rows without a value have no order; ranked ones must match in order.
		unranked := map[string]int{}
		for index := range want {
			if want[index]["value"] != nil {
				if key(got[index]) != key(want[index]) {
					t.Fatalf("%v row %d: %s, want %s", args, index, key(got[index]), key(want[index]))
				}
				continue
			}
			unranked[key(want[index])]++
			unranked[key(got[index])]--
		}
		for row, count := range unranked {
			if count != 0 && !(args["limit"] != nil && limit < 7) {
				t.Fatalf("%v: unranked rows differ at %s", args, row)
			}
		}
	}
}
