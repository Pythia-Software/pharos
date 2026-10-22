//go:build probe

// Times text search and the record composers against interactive budgets on
// a consistent scratch backup of a real catalog. Probes build only with the
// probe tag, so they stay out of go test ./...:
//
//	PHAROS_SEARCH_REPLICA=/path/to/scratch-backup.sqlite3 \
//	  go test -tags probe -run TestTextSearchReplica -v ./internal/archive

package archive

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	qt "github.com/Pythia-Software/query-table/backends/go"
)

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
		{"common_filtered_date", TextSearchOptions{Query: "test", From: "2100-01-01"}},
		{"common_filtered_role", TextSearchOptions{Query: "test", Roles: []string{"pharos_missing_role"}}},
		{"boolean", TextSearchOptions{Query: `(timeout OR deadlock) NOT retry`}},
		{"later_or_branch", TextSearchOptions{Query: `pharosabsenttoken OR test`}},
		{"failed_or_branch", TextSearchOptions{Query: `(test AND pharosabsenttoken) OR search`}},
		{"flat_exclusions", TextSearchOptions{Query: flatExclusions}},
		{"phrase", TextSearchOptions{Query: `"race condition"`}},
		{"phrase_common", TextSearchOptions{Query: `"go test"`}},
		{"phrase_absent", TextSearchOptions{Query: `"pharosabsenttoken otherabsenttoken"`}},
		{"phrase_strict", TextSearchOptions{Query: `"race condition"`, CaseSensitive: true, Separators: true}},
		{"ordered", TextSearchOptions{Query: "timeout PRE/8 retry"}},
		{"user", TextSearchOptions{Query: "search", Surface: "user"}},
		{"assistant", TextSearchOptions{Query: "search", Surface: "assistant"}},
		{"tools", TextSearchOptions{Query: "search", Surface: "tools"}},
		{"all", TextSearchOptions{Query: "search", Surface: "all"}},
		{"library_phrase", TextSearchOptions{Query: `"race condition"`, Limit: libraryFindTextLimit, SelectedOnly: true}},
		{"library_common", TextSearchOptions{Query: "test", Limit: libraryFindTextLimit, SelectedOnly: true}},
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
			if filter := os.Getenv("PHAROS_SEARCH_CASE"); filter != "" && !strings.Contains(tc.name, filter) {
				continue
			}
			options := tc.o
			if strings.HasPrefix(tc.name, "library_") {
				options.TimeoutMS = 700
			}
			result, e := c.SearchText(t.Context(), options)
			if e != nil {
				t.Errorf("%s: %v", tc.name, e)
				continue
			}
			t.Logf("pass=%d case=%s ms=%d strategy=%s hits=%d candidates=%d bytes=%d complete=%t stop=%s skipped=%d", pass, tc.name, result.ElapsedMS, result.Strategy, len(result.Items), result.Candidates, result.VerifiedBytes, result.Complete, result.StopReason, result.SkippedOversize)
			if result.ElapsedMS > 2500 {
				t.Errorf("interactive budget exceeded: %s took %dms", tc.name, result.ElapsedMS)
			}
			if result.Candidates == 0 && result.NextCursor != "" {
				t.Errorf("%s offered continuation without consuming a candidate", tc.name)
			}
		}
	}
	if os.Getenv("PHAROS_SEARCH_ONLY") != "" {
		return
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

// Exercise the actual Library table preparation, grouping, row projection and
// shared search cache, as well as its standalone text engine. Uses the same
// read pool settings as the service; never opens the catalog for writing.
func TestSearchPageReplica(t *testing.T) {
	path := os.Getenv("PHAROS_SEARCH_REPLICA")
	if path == "" {
		t.Skip("set PHAROS_SEARCH_REPLICA to a scratch backup")
	}
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	if path == "/Volumes/euclid/Pharos/catalog/catalog.sqlite3" {
		t.Fatal("use a scratch backup")
	}
	db, err := sql.Open("sqlite", readerDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(readerConns)
	c := &Catalog{DB: db, Path: path, now: time.Now}
	defer c.closeQuery()
	config := testConfig(t)
	config.APIToken = "replica-test"
	server := testServer(t, config, c)
	document, err := querySchemaDocument("library")
	if err != nil {
		t.Fatal(err)
	}
	var defaults struct {
		DefaultSelect []struct{ Field string }
		DefaultSort   qt.OrderBys
	}
	if err = json.Unmarshal(document, &defaults); err != nil {
		t.Fatal(err)
	}
	selected := []string{}
	for _, column := range defaults.DefaultSelect {
		selected = append(selected, column.Field)
	}
	var workspace, conversation string
	if err = db.QueryRow(`SELECT c.workspace_id,c.id FROM text_search_documents d CROSS JOIN conversations c ON c.id=d.conversation_id ORDER BY d.message_rowid DESC LIMIT 1`).Scan(&workspace, &conversation); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, query, kind, mode, surface string
		sensitive, separators, fuzzy     bool
		filters                          url.Values
	}{
		{name: "word", query: "test"},
		{name: "phrase", query: `"race condition"`},
		{name: "common_phrase", query: `"go test"`},
		{name: "absent_phrase", query: `"pharosabsenttoken otherabsenttoken"`},
		{name: "boolean", query: `(timeout OR deadlock) NOT retry`},
		{name: "prefix", query: "migrat*"},
		{name: "near", query: "timeout NEAR/8 retry"},
		{name: "ordered", query: "timeout PRE/8 retry"},
		{name: "case", query: "Migration", sensitive: true},
		{name: "separators", query: `"race condition"`, separators: true},
		{name: "fuzzy", query: "retreive", fuzzy: true},
		{name: "user", query: "search", surface: "user"},
		{name: "assistant", query: "search", surface: "assistant"},
		{name: "reasoning", query: "search", surface: "reasoning"},
		{name: "tools", query: "search", surface: "tools"},
		{name: "all", query: "search", surface: "all"},
		{name: "typed", query: "search", surface: "typed"},
		{name: "pasted", query: "search", surface: "pasted"},
		{name: "quoted", query: "search", surface: "quoted"},
		{name: "resent", query: "search", surface: "resent"},
		{name: "template", query: "search", surface: "template"},
		{name: "harness", query: "search", surface: "harness"},
		{name: "automated", query: "search", surface: "automated"},
		{name: "attachment", query: "search", surface: "attachment"},
		{name: "system", query: "search", surface: "all", filters: url.Values{"find_roles": {"system"}}},
		{name: "developer", query: "search", surface: "all", filters: url.Values{"find_roles": {"developer"}}},
		{name: "literal", query: "foo::bar", mode: "literal", surface: "user"},
		{name: "regex", query: `search.{0,80}(quality|inadequate)`, mode: "regex", surface: "user"},
		{name: "scan", query: `[0-9a-f]{40}`, mode: "regex", surface: "user"},
		{name: "future_date", query: "test", filters: url.Values{"find_from": {"2100-01-01"}}},
		{name: "missing_role", query: "test", filters: url.Values{"find_roles": {"pharos_missing_role"}}},
		{name: "role", query: "test", filters: url.Values{"find_roles": {"user"}}},
		{name: "provider", query: `"go test"`, filters: url.Values{"find_provider": {"codex"}}},
		{name: "repository", query: `"go test"`, filters: url.Values{"find_repository": {"pharos"}}},
		{name: "workspace", query: "test", filters: url.Values{"find_workspace_id": {workspace}}},
		{name: "conversation", query: "test", filters: url.Values{"find_conversation_id": {conversation}}},
		{name: "files_common", query: "test", kind: "file"},
		{name: "files_absent", query: "pharosabsenttoken", kind: "file"},
		{name: "urls_common", query: "github.com", kind: "url"},
		{name: "urls_absent", query: "pharosabsenttoken", kind: "url"},
	}
	for pass := 0; pass < 2; pass++ {
		for _, tc := range cases {
			if filter := os.Getenv("PHAROS_SEARCH_CASE"); filter != "" && !strings.Contains(tc.name, filter) {
				continue
			}
			params := url.Values{"find": {tc.query}, "find_kind": {tc.kind}, "find_mode": {tc.mode}, "find_surface": {tc.surface}, "find_case": {boolFlag(tc.sensitive)}, "find_separators": {boolFlag(tc.separators)}, "find_fuzzy": {boolFlag(tc.fuzzy)}}
			for key, values := range tc.filters {
				params[key] = values
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			started := time.Now()
			q := qt.ServerQueryV2{Version: 2, Profile: qt.SQLiteExpressionProfile, WireQuery: qt.WireQuery{Select: selected, OrderBy: defaults.DefaultSort, Limit: 50}}
			body, err := json.Marshal(q)
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, "/api/query/library/rows-v2?"+params.Encode(), bytes.NewReader(body)).WithContext(ctx)
			request.Header.Set("Authorization", "Bearer "+config.APIToken)
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			cancel()
			elapsed := time.Since(started).Milliseconds()
			var result struct {
				Rows  []map[string]any
				Find  *libraryFindSummary
				Error string
			}
			err = json.Unmarshal(response.Body.Bytes(), &result)
			if err != nil || response.Code != http.StatusOK {
				t.Errorf("pass=%d page=%s ms=%d status=%d error=%v %s", pass, tc.name, elapsed, response.Code, err, result.Error)
				continue
			}
			searchMS, stop, hits := int64(0), "", 0
			if result.Find != nil && result.Find.Search != nil {
				searchMS = result.Find.Search.ElapsedMS
				stop = result.Find.Search.StopReason
				hits = result.Find.Search.Count
			}
			t.Logf("pass=%d page=%s ms=%d search_ms=%d hits=%d rows=%d stop=%s", pass, tc.name, elapsed, searchMS, hits, len(result.Rows), stop)
			if elapsed >= 1000 {
				t.Errorf("interactive page budget exceeded: %s took %d ms", tc.name, elapsed)
			}
			if result.Find != nil && (result.Find.Limited || result.Find.Search != nil && !result.Find.Search.Complete) {
				t.Errorf("%s published an incomplete workspace population: %s", tc.name, stop)
			}
		}
	}
}
