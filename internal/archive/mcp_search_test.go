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
				conversation("chart-session", message("h1", "user", "The chart parser fails on dates"), message("h2", "assistant", "Parsed dates in UTC"),
					map[string]any{"id": "h3", "role": "tool", "kind": "tool_result", "text": "wombat"}),
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
	// Tool output is not word-indexed: its words match no message, and the
	// results say so.
	if got, err := catalog.searchConversations(map[string]any{"query": "wombat"}); err != nil || got["coverage"] != toolOutputCoverage || strings.Contains(jsonText(got), "message text") {
		t.Fatalf("search_conversations matched tool output: %v %v", got, err)
	}
	chart, _ := queryMaps(catalog.DB, "SELECT id FROM conversations WHERE native_id='chart-session'")
	if got, err := catalog.conversationPassages(map[string]any{"conversation_id": chart[0]["id"], "query": "wombat"}); err != nil || len(got["items"].([]map[string]any)) != 0 || got["coverage"] != toolOutputCoverage {
		t.Fatalf("passages matched tool output: %v %v", got, err)
	}
	if got, err := callMCP(catalog, "search_work", map[string]any{"query": "wombat"}); err != nil || got.(map[string]any)["coverage"] != toolOutputCoverage {
		t.Fatalf("search_work: %v %v", got, err)
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

func TestMCPWorkSearchPaginationAndBudget(t *testing.T) {
	catalog, _ := testCatalog(t)
	tx := seedTx(t, catalog.DB)
	preview := strings.Repeat("界 café \"quoted\"\n", 1000)
	title := strings.Repeat("Parser work ", 200)
	changedPath := strings.Repeat("nested/", 2500) + "parser.go"
	if _, err := tx.Exec(`INSERT INTO repositories(id,display_name,created_at,updated_at) VALUES('repo','parser','x','x')`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		id := fmt.Sprintf("work-%d", i)
		if _, err := tx.Exec(`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,purpose,outcome,activity_at,indexed_at)
			VALUES(?,'canonical','local',?,?,?,?,'2026-10-01','x')`, id, id, title, preview, preview); err != nil {
			t.Fatal(err)
		}
		// Imports also retain an explicit outcome in summaries. Keep that
		// production path and the full change/PR payloads in the fixture, so
		// bounding only the top-level purpose/outcome cannot hide the bug.
		if err := upsertSummary(tx, id, WorkspaceRecord{Outcome: preview}); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO change_sets(id,workspace_id,classification) VALUES(?,?,'attempted')`, id, id); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO change_files(change_set_id,path,status,tracked) VALUES(?,?,'modified',1)`, id, changedPath); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO pull_requests(id,host,repository_id,number,title) VALUES(?,'github.com','repo',?,?)`, id, i+1, preview); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO work_pr_links(workspace_id,pr_id,relationship,confidence,evidence_json) VALUES(?,?,'associated',1,'{}')`, id, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ limit, budget, maxBytes int }{
		{1, 4000, 12000},     // caller-limited pages
		{100, 2000, 6000},    // output-limited pages
		{100, 100000, 12000}, // the failing request's excessive budget is capped
	} {
		t.Run(fmt.Sprintf("limit=%d/budget=%d", tc.limit, tc.budget), func(t *testing.T) {
			seen := map[string]bool{}
			offset, cut := 0, false
			for {
				page := auditCall(t, catalog, "search_work", map[string]any{"limit": tc.limit, "offset": offset, "max_output_tokens": tc.budget})
				items := page["items"].([]map[string]any)
				if len(jsonText(page)) > tc.maxBytes || len(items) == 0 || integer(page["total"]) != 4 || integer(page["offset"]) != int64(offset) {
					t.Fatalf("invalid page: %s", jsonText(page))
				}
				cut = cut || page["truncated"] == true
				for _, item := range items {
					id := firstString(item["workspace_id"])
					if id == "" || seen[id] || item["title"] != title || item["freshness"] == nil {
						t.Fatalf("repeated or unusable card: %s", jsonText(item))
					}
					seen[id] = true
				}
				if page["next_offset"] == nil {
					break
				}
				next := int(integer(page["next_offset"]))
				if next != offset+len(items) {
					t.Fatalf("continuation skipped or repeated results: %d -> %d", offset, next)
				}
				offset = next
			}
			if len(seen) != 4 || tc.limit == 100 && !cut {
				t.Fatalf("incomplete pagination or no budget cut: %v, cut=%v", seen, cut)
			}
		})
	}
	// Too-small budgets must fail clearly rather than claim no matches or
	// return a continuation that never advances. Exhausted pages end cleanly.
	if _, err := callMCP(catalog, "search_work", map[string]any{"max_output_tokens": 200}); err == nil {
		t.Fatal("accepted a budget too small for one card")
	}
	end := auditCall(t, catalog, "search_work", map[string]any{"offset": 100})
	if len(end["items"].([]map[string]any)) != 0 || end["next_offset"] != nil {
		t.Fatalf("exhausted page: %s", jsonText(end))
	}
	// Preview clipping must leave the archived field available in full.
	var original string
	if err := catalog.DB.QueryRow("SELECT purpose FROM workspaces WHERE id='work-0'").Scan(&original); err != nil || original != preview {
		t.Fatalf("search changed archived text: %v", err)
	}
	for _, tc := range []struct{ section, field, want string }{
		{"outcomes", "outcome", preview},
		{"changes", "path", changedPath},
		{"prs", "title", preview},
	} {
		var recovered strings.Builder
		offset := 0
		for {
			page := auditCall(t, catalog, "get_work_detail", map[string]any{
				"workspace_id": "work-0", "section": tc.section, "field": tc.field,
				"text_offset": offset, "max_output_tokens": 4000,
			})
			recovered.WriteString(asString(page["text"]))
			if page["next_text_offset"] == nil {
				break
			}
			next := int(integer(page["next_text_offset"]))
			if next <= offset {
				t.Fatalf("%s field continuation did not advance", tc.section)
			}
			offset = next
		}
		if recovered.String() != tc.want {
			t.Fatalf("search lost full %s.%s field access", tc.section, tc.field)
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
