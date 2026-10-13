package archive

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	qt "github.com/Pythia-Software/query-table/backends/go"
)

// Opt in with PHAROS_SEARCH_REPLICA pointing at a consistent scratch backup.
// This harness is read-only and never calls OpenCatalog / migrations.
func TestTextSearchReplica(t *testing.T) {
	path := os.Getenv("PHAROS_SEARCH_REPLICA")
	if path == "" {
		t.Skip("set PHAROS_SEARCH_REPLICA to a scratch backup")
	}
	path, e := filepath.Abs(path)
	if e != nil {
		t.Fatal(e)
	}
	path, e = filepath.EvalSymlinks(path)
	if e != nil {
		t.Fatal(e)
	}
	if path == "/Volumes/euclid/Pharos/catalog/catalog.sqlite3" {
		t.Fatal("use a scratch backup")
	}
	db, e := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(1000)")
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	db.SetMaxOpenConns(4)
	c := &Catalog{DB: db, Path: path, now: time.Now}
	var conversation string
	if e = db.QueryRow(`SELECT conversation_id FROM text_search_documents GROUP BY conversation_id HAVING count(*) BETWEEN 30 AND 300 LIMIT 1`).Scan(&conversation); e != nil {
		t.Fatal(e)
	}
	flatExclusions := "test"
	for i := 0; i < 18; i++ {
		flatExclusions += fmt.Sprintf(" NOT pharosabsenttoken%d", i)
	}
	cases := []struct {
		name string
		o    TextSearchOptions
	}{
		{"rare", TextSearchOptions{Query: "heliotrope", Surface: "all"}},
		{"common", TextSearchOptions{Query: "test"}},
		{"boolean", TextSearchOptions{Query: `(timeout OR deadlock) NOT retry`}},
		{"later_or_branch", TextSearchOptions{Query: `pharosabsenttoken OR test`}},
		{"failed_or_branch", TextSearchOptions{Query: `(test AND pharosabsenttoken) OR search`}},
		{"flat_exclusions", TextSearchOptions{Query: flatExclusions}},
		{"phrase", TextSearchOptions{Query: `"race condition"`}},
		{"prefix", TextSearchOptions{Query: "migrat*"}},
		{"near", TextSearchOptions{Query: "timeout NEAR/8 retry"}},
		{"user_regex", TextSearchOptions{Query: `search.{0,80}(quality|inadequate)`, Mode: "regex", Surface: "user"}},
		{"user_literal", TextSearchOptions{Query: "foo::bar", Mode: "literal", Surface: "user", CaseSensitive: true}},
		{"reasoning_regex", TextSearchOptions{Query: `timeout.*retry`, Mode: "regex", Surface: "reasoning"}},
		{"scoped_regex", TextSearchOptions{Query: `[0-9a-f]{40}`, Mode: "regex", Surface: "all", ConversationID: conversation}},
		{"scoped_common", TextSearchOptions{Query: "test", ConversationID: conversation}},
		{"typed", TextSearchOptions{Query: "search", Surface: "typed"}},
		{"fuzzy", TextSearchOptions{Query: "retreive", Fuzzy: true}},
		{"fuzzy_case", TextSearchOptions{Query: "migration", Fuzzy: true, CaseSensitive: true}},
	}
	for pass := 0; pass < 2; pass++ {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		started := time.Now()
		choices, err := c.textSearchEditorOptions(ctx)
		cancel()
		if err != nil {
			t.Fatalf("editor metadata choices: %v", err)
		}
		t.Logf("pass=%d editor_options_ms=%d providers=%d sources=%d", pass, time.Since(started).Milliseconds(), len(choices["providers"].([]string)), len(choices["sources"].([]string)))
		for _, tc := range cases {
			result, e := c.SearchText(t.Context(), tc.o)
			if e != nil {
				t.Errorf("%s: %v", tc.name, e)
				continue
			}
			t.Logf("pass=%d case=%s ms=%d strategy=%s hits=%d candidates=%d bytes=%d complete=%t stop=%s skipped=%d", pass, tc.name, result.ElapsedMS, result.Strategy, len(result.Items), result.Candidates, result.VerifiedBytes, result.Complete, result.StopReason, result.SkippedOversize)
			if result.ElapsedMS > 2500 {
				t.Errorf("interactive budget exceeded: %s took %dms", tc.name, result.ElapsedMS)
			}
		}
	}
	// Exercise the exact predicates used by the contextual record composer.
	// Read the prepared replica directly; never run authorship/rollup maintenance.
	for _, sample := range []struct{ dataset, field, order, op, value string }{
		{"tool_calls", "command", "started_at", "contains", "parser"},
		{"tool_calls", "command", "started_at", "contains", "test"},
		{"writing_messages", "text", "sent_at", "contains", "parser"},
		{"writing_messages", "text", "sent_at", "matches_regex", "search.*quality"},
	} {
		schema, err := sqliteQuerySchema(sample.dataset)
		if err != nil {
			t.Fatal(err)
		}
		table, _ := sqlDatasetFor(sample.dataset)
		if sample.dataset == "tool_calls" {
			table.toolSearchIndexed, err = c.toolSearchReady(t.Context())
			if err != nil {
				t.Fatal(err)
			}
		}
		// This replica predates the later effort metadata column. Project only
		// the fields used by this read; this leaves production predicate SQL intact.
		for name := range schema.Fields {
			if name != schema.IDField && name != sample.field && name != sample.order {
				delete(schema.Fields, name)
			}
		}
		where := []qt.WhereTerm{{Field: sample.field, Op: sample.op, Value: sample.value}}
		d := queryV2Dataset(sample.dataset, schema)
		d.SourceSQL, d.SourceArgs = table.v2IndexedSource(schema, where, nil)
		query := qt.ServerQueryV2{Version: 2, Profile: qt.SQLiteExpressionProfile, WireQuery: qt.WireQuery{Where: where, Select: []string{schema.IDField, sample.field}, OrderBy: qt.OrderBys{{Field: sample.order, Dir: "desc"}}, Limit: 50}}
		ctx, cancel := context.WithTimeout(t.Context(), 2500*time.Millisecond)
		started := time.Now()
		_, err = d.ExecuteV2(ctx, db, &query, nil, qt.PlanOptions{})
		cancel()
		t.Logf("record dataset=%s op=%s value=%s elapsed_ms=%d error=%v", sample.dataset, sample.op, sample.value, time.Since(started).Milliseconds(), err)
	}
	for _, mode := range []string{"terms", "regex"} {
		started := time.Now()
		values := url.Values{"find": {"parser"}, "find_mode": {mode}, "find_surface": {"user"}, "find_fuzzy": {"0"}}
		table, matches, err := c.messageFindDataset(t.Context(), values, writingMessageDataset)
		if err != nil {
			t.Fatal(err)
		}
		schema, _ := sqliteQuerySchema("writing_messages")
		d := queryV2Dataset("writing_messages", schema)
		d.SourceSQL = table.v2Source(schema)
		q := qt.ServerQueryV2{Version: 2, Profile: qt.SQLiteExpressionProfile, WireQuery: qt.WireQuery{Select: []string{"id", "text", "sent_at", "typed_words"}, OrderBy: qt.OrderBys{{Field: "sent_at", Dir: "desc"}}, Limit: 50}}
		ctx, cancel := context.WithTimeout(t.Context(), 2500*time.Millisecond)
		_, err = d.ExecuteV2(ctx, db, &q, nil, qt.PlanOptions{})
		cancel()
		elapsed := time.Since(started).Milliseconds()
		t.Logf("message_composer mode=%s elapsed_ms=%d matches=%d search_ms=%d complete=%v error=%v", mode, elapsed, len(matches.search.Items), matches.search.ElapsedMS, matches.search.Complete, err)
		if err != nil || elapsed > 3000 {
			t.Errorf("bounded message composer exceeded budget: %v, %d ms", err, elapsed)
		}
	}
	// Prior Library implementation ranked every common FTS match before LIMIT.
	for _, query := range []string{"test", "heliotrope"} {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		start := time.Now()
		rows, e := db.QueryContext(ctx, `SELECT m.id FROM messages_fts f JOIN messages m ON m.id=f.message_id JOIN conversations c ON c.id=m.conversation_id JOIN workspaces w ON w.id=c.workspace_id WHERE messages_fts MATCH ? AND m.selected=1 AND m.kind IN ('message','thinking','reasoning','analysis') AND (m.role IN ('user','assistant') OR m.kind IN ('thinking','reasoning','analysis')) ORDER BY bm25(messages_fts),m.created_at DESC LIMIT 5001`, quoteFTS(query))
		n := 0
		if e == nil {
			for rows.Next() {
				n++
			}
			e = rows.Err()
			rows.Close()
		}
		t.Logf("baseline query=%s ms=%d rows=%d error=%v", query, time.Since(start).Milliseconds(), n, e)
		cancel()
	}
}
