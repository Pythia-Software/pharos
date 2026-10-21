package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

func textFixture(t *testing.T) *Catalog {
	c, _ := testCatalog(t)
	for _, q := range []string{
		`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('w','codex','local','w','Search tests','2026-10-12'),('other','claude','local','other','Other work','2026-10-12')`,
		`INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('c','w','codex','local','c'),('other','other','claude','local','other')`,
		`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at) VALUES
 ('a','c','a','user','message','cache race condition timeout retry foo::bar café','a','2026-10-11T12:00:00Z'),
 ('b','c','b','assistant','message','buffer race condition redis migration','b','2026-10-12T12:00:00Z'),
 ('r','c','r','assistant','reasoning','timeout then retry','r','2026-10-12'),
 ('t','c','t','tool','tool_result','timeout secret-only 0123456789abcdef0123456789abcdef0123456789','t','2026-10-12'),
 ('o','other','o','user','message','CACHE race condition foo--bar','o','2026-10-12'),
 ('span','c','span','user','message','typed cache. pasted secret redis','span','2026-10-12')`,
		`INSERT INTO messages_fts(message_id,text) SELECT id,text FROM messages ORDER BY rowid`,
		`INSERT INTO message_fts_rows SELECT message_id,rowid FROM messages_fts`,
		`INSERT INTO messages_trigram(rowid,text) SELECT f.rowid,m.text FROM messages_fts f JOIN messages m ON m.id=f.message_id WHERE m.kind IN (` + substringKinds + `)`,
		`INSERT INTO message_authorship_spans(message_id,position,category,rule,start_byte,end_byte,chars,words) VALUES('span',0,'typed','typed',0,12,12,2),('span',1,'pasted','pasted',13,32,19,3)`,
	} {
		if _, e := c.DB.Exec(q); e != nil {
			t.Fatal(e)
		}
	}
	for {
		more, e := c.indexTextSearchBatch(t.Context())
		if e != nil {
			t.Fatal(e)
		}
		if !more {
			break
		}
	}
	return c
}
func resultIDs(r textSearchResult) []string {
	ids := []string{}
	for _, item := range r.Items {
		ids = append(ids, item.MessageID)
	}
	slices.Sort(ids)
	return ids
}
func TestTextSearchQueries(t *testing.T) {
	c := textFixture(t)
	cases := []struct {
		o    TextSearchOptions
		want []string
	}{
		{TextSearchOptions{Query: `(cache OR buffer) AND "race condition" NOT redis`}, []string{"a", "o"}},
		{TextSearchOptions{Query: `cache "race condition"`}, []string{"a", "o"}},
		{TextSearchOptions{Query: `migrat*`}, []string{"b"}},
		{TextSearchOptions{Query: `"migrat"*`}, []string{"b"}},
		{TextSearchOptions{Query: `timeout NEAR/0 retry`}, []string{"a"}},
		{TextSearchOptions{Query: `timeout NEAR/1 retry`}, []string{"a", "r"}},
		{TextSearchOptions{Query: `timeout PRE/1 retry`}, []string{"a", "r"}},
		{TextSearchOptions{Query: `retry PRE/1 timeout`}, []string{}},
		{TextSearchOptions{Query: `"race condition" PRE/10 cafe`}, []string{"a"}},
		{TextSearchOptions{Query: `cache NOT (cafe PRE/10 timeout)`}, []string{"a", "o", "span"}},
		{TextSearchOptions{Query: `cache cafe`, Separators: true}, []string{"a"}},
		{TextSearchOptions{Query: `foo::bar`, Mode: "literal", Surface: "user"}, []string{"a"}},
		{TextSearchOptions{Query: `foo[:]+bar`, Mode: "regex", Surface: "user"}, []string{"a"}},
		{TextSearchOptions{Query: `cache NOT CACHE`, CaseSensitive: true}, []string{"a", "span"}},
		{TextSearchOptions{Query: `cafe`}, []string{"a"}},
		{TextSearchOptions{Query: `"foo::bar"`, Separators: true}, []string{"a"}},
		{TextSearchOptions{Query: `secret`, Surface: "typed"}, []string{}},
		{TextSearchOptions{Query: `cache NOT redis`, Surface: "typed"}, []string{"span"}},
		{TextSearchOptions{Query: `secret`, Surface: "pasted"}, []string{"span"}},
		{TextSearchOptions{Query: `timeout`, Surface: "reasoning"}, []string{"r"}},
		{TextSearchOptions{Query: `secret`, Surface: "tools"}, []string{"t"}},
		{TextSearchOptions{Query: `race`, From: "2026-10-12", Provider: "codex"}, []string{"b"}},
		{TextSearchOptions{Query: `cache OR buffer`, Mode: "terms"}, []string{}},
		{TextSearchOptions{Query: `(?s)timeout.*retry`, Mode: "regex", Surface: "reasoning"}, []string{"r"}},
		{TextSearchOptions{Query: `secret`, WorkspaceIDs: []string{}}, []string{}},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%s/%s/%s", tc.o.Query, tc.o.Mode, tc.o.Surface), func(t *testing.T) {
			r, e := c.SearchText(t.Context(), tc.o)
			if e != nil {
				t.Fatal(e)
			}
			if !slices.Equal(resultIDs(r), tc.want) || !r.Complete {
				t.Fatalf("got %v complete=%v stop=%s; want %v", resultIDs(r), r.Complete, r.StopReason, tc.want)
			}
			// The workspace table establishes the same full population regardless
			// of message pagination/work budgets, retaining one hit and an exact
			// message count for each matching workspace.
			options := tc.o
			options.workspaceRows = true
			options.Limit, options.MaxCandidates, options.MaxBytes = 1, 1, 64
			population, err := c.SearchText(t.Context(), options)
			want := map[string]int{}
			for _, id := range tc.want {
				workspace := "w"
				if id == "o" {
					workspace = "other"
				}
				want[workspace]++
			}
			if err != nil || !population.Complete || population.Count != len(tc.want) || len(population.Items) != len(want) {
				t.Fatalf("workspace population: %+v err=%v; want %v", population, err, want)
			}
			for _, item := range population.Items {
				if item.Count != want[item.WorkspaceID] {
					t.Fatalf("workspace %s: %d matching messages; want %d", item.WorkspaceID, item.Count, want[item.WorkspaceID])
				}
			}
		})
	}
}

func TestTextSearchEditorValidationDoesNotSearch(t *testing.T) {
	config := defaultConfig(t.TempDir() + "/config.toml")
	config.APIToken = "test-token"
	// Validation needs no catalog: a malformed draft never starts a text scan.
	server := testServer(t, config, nil)
	for _, tc := range []struct {
		body   string
		status int
	}{
		{`{"query":"(cache OR buffer) NOT redis"}`, 200},
		{`{"where":{"op":"term","text":"OR"}}`, 200},
		{`{"query":"cache OR"}`, 400},
		{`{"query":"(?<=foo)bar","mode":"regex"}`, 400},
		{`{"where":{"op":"and","children":[]}}`, 400},
		{`{"query":"cache","from":"2026-10-12","to":"2026-10-01"}`, 400},
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/search/text/validate", strings.NewReader(tc.body))
		req.Header.Set("Authorization", "Bearer test-token")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, req)
		if response.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.body, response.Code, response.Body.String())
		}
		if tc.status == 200 && !strings.Contains(response.Body.String(), `"where"`) {
			t.Fatal(response.Body.String())
		}
	}
}

func TestTextSearchVisualLibraryRequests(t *testing.T) {
	catalog, config := libraryFixture(t)
	if _, err := catalog.DB.Exec(`INSERT INTO messages_fts(message_id,text) SELECT id,text FROM messages`); err != nil {
		t.Fatal(err)
	}
	if err := catalog.refreshAllLibrary(t.Context()); err != nil {
		t.Fatal(err)
	}
	server := testServer(t, config, catalog)
	where := `{"op":"and","children":[{"op":"or","children":[{"op":"term","text":"parser"},{"op":"term","text":"tokenizer"}]},{"op":"phrase","text":"fix the"}]}`
	for _, endpoint := range []string{"/api/query/library", "/api/query/library/rows-v2"} {
		body := `{"select":["title"],"limit":10}`
		if strings.Contains(endpoint, "v2") {
			body = `{"version":2,"profile":"qt-sqlite-v1",` + body[1:]
		}
		req := httptest.NewRequest(http.MethodPost, endpoint+"?find_fuzzy=0&find_where="+url.QueryEscape(where), strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer test-token")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, req)
		var result map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if response.Code != 200 || result["total"] != float64(1) || result["find"] == nil {
			t.Fatalf("visual query ignored: %d %s", response.Code, response.Body.String())
		}
	}
	for _, bad := range []string{"null", "{", strings.Repeat("x", 32001)} {
		_, _, err := libraryFindRequest(url.Values{"find_where": []string{bad}})
		if err == nil {
			t.Fatal("malformed visual query silently ignored")
		}
	}
	options, err := catalog.textSearchEditorOptions(t.Context())
	if err != nil || !slices.Equal(options["providers"].([]string), []string{"claude", "codex", "tl1"}) {
		t.Fatal(options, err)
	}
}
func TestTextSearchCursorAndBudgets(t *testing.T) {
	c := textFixture(t)
	o := TextSearchOptions{Query: "cache OR timeout", Limit: 1}
	seen := map[string]bool{}
	var last textSearchResult
	for i := 0; i < 20; i++ {
		r, e := c.SearchText(t.Context(), o)
		if e != nil {
			t.Fatal(e)
		}
		for _, item := range r.Items {
			if seen[item.MessageID] {
				t.Fatal("duplicate result")
			}
			seen[item.MessageID] = true
		}
		last = r
		if r.NextCursor == "" {
			break
		}
		o.Cursor = r.NextCursor
	}
	if len(seen) != 4 || !last.Complete || last.Count != 4 {
		t.Fatalf("pagination: %#v %#v", seen, last)
	}
	r, e := c.SearchText(t.Context(), TextSearchOptions{Query: "n.ma.ch", Mode: "regex", Surface: "user", MaxCandidates: 1})
	if e != nil {
		t.Fatal(e)
	}
	if r.Complete || r.NextCursor == "" || r.StopReason != "candidate_budget" {
		t.Fatalf("empty scan must resume: %#v", r)
	}
	if _, e := c.SearchText(t.Context(), TextSearchOptions{Query: "different", Mode: "regex", Surface: "user", Cursor: r.NextCursor}); e == nil {
		t.Fatal("changed query accepted cursor")
	}
	if _, e := c.DB.Exec(`UPDATE messages SET text=text||' edit' WHERE id='a'`); e != nil {
		t.Fatal(e)
	}
	if _, e := c.SearchText(t.Context(), TextSearchOptions{Query: "n.ma.ch", Mode: "regex", Surface: "user", Cursor: r.NextCursor}); e == nil {
		t.Fatal("stale cursor accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, e := c.SearchText(ctx, TextSearchOptions{Query: "cache"}); e == nil {
		t.Fatal("cancellation ignored")
	}
	if _, e := c.SearchText(t.Context(), TextSearchOptions{Query: ".*", Mode: "regex", Surface: "all"}); e == nil {
		t.Fatal("unscoped all-text regex accepted")
	}
	if _, e := c.DB.Exec(`UPDATE messages SET text=? WHERE id='a'`, strings.Repeat("x", (1<<20)+1)); e != nil {
		t.Fatal(e)
	}
	r, e = c.SearchText(t.Context(), TextSearchOptions{Query: "x", Mode: "regex", Surface: "user"})
	if e != nil {
		t.Fatal(e)
	}
	if r.Complete || r.SkippedOversize != 1 || r.CountRelation != "lower_bound" {
		t.Fatalf("oversize: %#v", r)
	}
}
func TestTextSearchSyntaxErrors(t *testing.T) {
	for _, q := range []string{`"unclosed`, `x OR`, `NOT x`, `(x OR y`, `x NEAR/101 y`, `x NEAR/2 (a OR b)`, `col?r`, `*suffix`, `x AND OR y`} {
		if _, e := parseTextQuery(q, "advanced"); e == nil {
			t.Errorf("accepted %q", q)
		}
	}
}

func TestTextSearchFlatBooleanQueries(t *testing.T) {
	for _, separator := range []string{" ", " AND ", " OR "} {
		query := strings.TrimSuffix(strings.Repeat("cache"+separator, 40), separator)
		node, err := parseTextQuery(query, "advanced")
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		if err := validateTextTree(node, 0, &count); err != nil {
			t.Fatalf("flat expression consumed nesting budget: %v", err)
		}
		if count != 41 {
			t.Fatalf("flat expression has %d nodes, want 41", count)
		}
	}
	if _, err := parseTextQuery(strings.Repeat("(", 17)+"cache"+strings.Repeat(")", 17), "advanced"); err == nil {
		t.Fatal("accepted excessive group nesting")
	}
}

func TestTextSearchSurfaceIndexMaintenance(t *testing.T) {
	c := textFixture(t)
	if _, e := c.DB.Exec(`UPDATE messages SET role='assistant',kind='analysis',text='changed' WHERE id='a'`); e != nil {
		t.Fatal(e)
	}
	var surface string
	var bytes int
	if e := c.DB.QueryRow(`SELECT surface,byte_length FROM text_search_documents WHERE message_id='a'`).Scan(&surface, &bytes); e != nil {
		t.Fatal(e)
	}
	if surface != "reasoning" || bytes != 7 {
		t.Fatal(surface, bytes)
	}
	if _, e := c.DB.Exec(`DELETE FROM messages WHERE id='a'`); e != nil {
		t.Fatal(e)
	}
	var n int
	if e := c.DB.QueryRow(`SELECT count(*) FROM text_search_documents WHERE message_id='a'`).Scan(&n); e != nil || n != 0 {
		t.Fatal(n, e)
	}
	// SQLite REPLACE need not fire delete triggers and can assign a new rowid.
	// The insert trigger must replace old metadata even when the new surface
	// no longer belongs in the small-surface index.
	for _, kind := range []string{"message", "tool_result"} {
		if _, e := c.DB.Exec(`INSERT OR REPLACE INTO messages(id,conversation_id,native_id,role,kind,text,content_hash)
		 VALUES('b','c','b','assistant',?,'replacement','replacement')`, kind); e != nil {
			t.Fatal(e)
		}
		var count int
		if e := c.DB.QueryRow(`SELECT count(*) FROM text_search_documents d JOIN messages m ON m.rowid=d.message_rowid
		 WHERE d.message_id='b' AND m.id=d.message_id AND d.byte_length=11`).Scan(&count); e != nil {
			t.Fatal(e)
		}
		want := 0
		if kind == "message" {
			want = 1
		}
		if count != want {
			t.Fatalf("REPLACE kind=%s has %d consistent metadata rows, want %d", kind, count, want)
		}
		if e := c.DB.QueryRow(`SELECT count(*) FROM text_search_documents WHERE message_id='b'`).Scan(&count); e != nil || count != want {
			t.Fatalf("REPLACE left stale metadata: count=%d err=%v", count, e)
		}
	}
}

func TestTextSearchRangeKinds(t *testing.T) {
	c := textFixture(t)
	r, err := c.SearchText(t.Context(), TextSearchOptions{Query: "cafe", Separators: true})
	if err != nil || len(r.Items) != 1 || r.Items[0].RangeKind != "excerpt" {
		t.Fatalf("lexical fallback was labeled an exact match: %+v %v", r, err)
	}
	r, err = c.SearchText(t.Context(), TextSearchOptions{Query: "^", Mode: "regex", Surface: "user"})
	if err != nil || len(r.Items) == 0 {
		t.Fatal(r, err)
	}
	for _, item := range r.Items {
		if item.RangeKind != "match" || item.StartByte != 0 || item.EndByte != 0 {
			t.Fatalf("zero-width regex range changed: %+v", item)
		}
	}
}

func TestTextSearchMCPOutputContinuation(t *testing.T) {
	c := textFixture(t)
	if _, err := c.DB.Exec(`UPDATE messages SET text=? WHERE id='a'`, strings.Repeat("x", (1<<20)+1)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("output-%d", i)
		if _, err := c.DB.Exec(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash)
		 VALUES(?,'c',?,'user','message','small result','hash')`, id, id); err != nil {
			t.Fatal(err)
		}
	}
	args := map[string]any{"query": "^", "mode": "regex", "surface": "user", "limit": 100, "max_output_tokens": 1200}
	seen := map[string]bool{}
	var last textSearchResult
	for page := 0; page < 50; page++ {
		result, err := c.searchTextMCP(t.Context(), args)
		if err != nil {
			t.Fatal(err)
		}
		if page == 0 && (result.StopReason != "output_budget" || result.SkippedOversize != 0) {
			t.Fatalf("output cursor did not rewind counts: %+v", result)
		}
		for _, item := range result.Items {
			if seen[item.MessageID] {
				t.Fatalf("MCP continuation repeated %s", item.MessageID)
			}
			seen[item.MessageID] = true
		}
		last = result
		if result.NextCursor == "" {
			break
		}
		args["cursor"] = result.NextCursor
	}
	if len(seen) != 22 || last.Count != 22 || last.SkippedOversize != 1 || last.NextCursor != "" || last.Complete {
		t.Fatalf("MCP output lost results or skip coverage: seen=%d result=%+v", len(seen), last)
	}
}

func TestTextSearchHTTPAndMCPParity(t *testing.T) {
	c := textFixture(t)
	config := defaultConfig(t.TempDir() + "/config.toml")
	config.APIToken = "test-token"
	server := testServer(t, config, c)
	req := httptest.NewRequest(http.MethodPost, "/api/search/text", strings.NewReader(`{"query":"cache NOT redis","selected_only":true}`))
	req.Header.Set("Authorization", "Bearer test-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, req)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var httpResult textSearchResult
	if e := json.Unmarshal(response.Body.Bytes(), &httpResult); e != nil {
		t.Fatal(e)
	}
	mcp, e := c.searchTextMCP(t.Context(), map[string]any{"query": "cache NOT redis", "selected_only": true})
	if e != nil {
		t.Fatal(e)
	}
	if !slices.Equal(resultIDs(httpResult), resultIDs(mcp)) {
		t.Fatalf("HTTP/MCP differ: %v %v", resultIDs(httpResult), resultIDs(mcp))
	}
	hits, _, e := c.libraryFindHits(t.Context(), LibraryFindOptions{Query: "cache NOT redis", Search: TextSearchOptions{SelectedOnly: true}})
	if e != nil {
		t.Fatal(e)
	}
	got := map[string]int{}
	for _, h := range hits {
		got[h.ConversationID] = h.Count
	}
	want := map[string]int{}
	for _, i := range mcp.Items {
		want[i.ConversationID]++
	}
	if !same(got, want) {
		t.Fatalf("Library/MCP differ: %#v %#v", got, want)
	}
}

func TestTextSearchTrigramSupersetAndByteBudget(t *testing.T) {
	c := textFixture(t)
	for i, body := range []string{"ſearch quality", "Kelvin quality", "search\nquality", "abx", "abz", "search unrelated", "quality unrelated", "nomatch foo", "foo::bar", "SEARCH QUALITY", "needle quality", "λ search quality"} {
		id := fmt.Sprintf("unicode%d", i)
		if _, e := c.DB.Exec(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash) VALUES(?,'c',?,'user','message',?,'hash')`, id, id, body); e != nil {
			t.Fatal(e)
		}
		if _, e := c.DB.Exec(`INSERT INTO messages_fts(message_id,text) VALUES(?,?)`, id, body); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := c.DB.Exec(`INSERT OR REPLACE INTO message_fts_rows SELECT message_id,rowid FROM messages_fts;DELETE FROM messages_trigram;INSERT INTO messages_trigram(rowid,text) SELECT f.rowid,m.text FROM messages_fts f JOIN messages m ON m.id=f.message_id WHERE m.kind IN ('message','result','delegation','delegation_result')`); e != nil {
		t.Fatal(e)
	}
	for _, q := range []string{`(?s)search.*quality`, `(?i)kelvin`, `ab(x|z)`, `(?i:search)|kelvin`, `(?s)search.*quality|nomatch`, `search.*(?:quality|inadequate)`, `(?:search|needle).*quality`, `(?:search|ab).*quality`, `(?:search.*)?quality`, `(?:search.*){0,2}quality`, `(?:search.*){1,2}quality`, `(?-i:SEARCH).*QUALITY`, `(?:foo::bar|nomatch)`, `(?:search|quality|)`, `search.*[^x]*quality`} {
		options := TextSearchOptions{Query: q, Mode: "regex", Surface: "user"}
		r, e := c.SearchText(t.Context(), options)
		if e != nil {
			t.Fatal(e)
		}
		pattern, e := regexp.Compile("(?i)" + q)
		if e != nil {
			t.Fatal(e)
		}
		rows, e := queryMapsContext(t.Context(), c.DB, `SELECT id,text FROM messages WHERE role='user' AND kind='message'`)
		if e != nil {
			t.Fatal(e)
		}
		want := []string{}
		for _, row := range rows {
			if pattern.MatchString(firstString(row["text"])) {
				want = append(want, firstString(row["id"]))
			}
		}
		slices.Sort(want)
		if !slices.Equal(resultIDs(r), want) {
			t.Fatalf("unsafe candidate filter %q got %v want %v", q, resultIDs(r), want)
		}
	}
	r, e := c.SearchText(t.Context(), TextSearchOptions{Query: "a", Mode: "literal", Surface: "user", MaxBytes: 4})
	if e != nil {
		t.Fatal(e)
	}
	if r.Complete || r.StopReason != "byte_budget" || r.NextCursor == "" || r.VerifiedBytes > 4 {
		t.Fatalf("byte budget: %#v", r)
	}
}

func TestTextSearchSpanBoundariesAndRegexWork(t *testing.T) {
	c := textFixture(t)
	r, e := c.SearchText(t.Context(), TextSearchOptions{Query: `(?P<word>cache)`, Mode: "regex", Surface: "typed"})
	if e != nil {
		t.Fatal(e)
	}
	if len(r.Items) != 1 || strings.Contains(r.Items[0].Snippet, "secret") || len(r.Items[0].Captures) != 1 || r.Items[0].Captures[0].StartByte != 6 || r.Items[0].Captures[0].Name != "word" {
		t.Fatalf("span capture leaked/corrupted: %#v", r.Items)
	}
	if _, e = c.DB.Exec(`UPDATE messages SET text=? WHERE id='a'`, strings.Repeat("a", 1<<16)); e != nil {
		t.Fatal(e)
	}
	r, e = c.SearchText(t.Context(), TextSearchOptions{Query: `(?:a?){300}b`, Mode: "regex", Surface: "user"})
	if e != nil {
		t.Fatal(e)
	}
	if r.SkippedOversize < 1 || r.Complete {
		t.Fatalf("complex regex was not byte bounded: %#v", r)
	}
	if _, e = c.SearchText(t.Context(), TextSearchOptions{Query: `(?:a?){1000}`, Mode: "regex", Surface: "user"}); e == nil {
		t.Fatal("unbounded regex program accepted")
	}
}

func TestTextSearchExcerptFindsMatchingAlternative(t *testing.T) {
	c := textFixture(t)
	body := strings.Repeat("preface ", 40) + "redis useful context"
	if _, err := c.DB.Exec(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash) VALUES('long','c','long','user','message',?,'hash')`, body); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DB.Exec(`INSERT INTO messages_fts(message_id,text) VALUES('long',?)`, body); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"zzz OR redis", "(zzz AND buffer) OR redis", "(buffer NOT migration) OR redis", "(zzz OR redis) NOT missing"} {
		t.Run(query, func(t *testing.T) {
			result, err := c.SearchText(t.Context(), TextSearchOptions{Query: query})
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(resultIDs(result), []string{"b", "long", "span"}) {
				t.Fatalf("unexpected hits: %v", resultIDs(result))
			}
			for _, item := range result.Items {
				var text string
				if err := c.DB.QueryRow(`SELECT text FROM messages WHERE id=?`, item.MessageID).Scan(&text); err != nil {
					t.Fatal(err)
				}
				start := strings.Index(text, "redis")
				if item.StartByte != start || item.EndByte != start+len("redis") || !strings.Contains(item.Snippet, "redis") {
					t.Errorf("%s: expected redis at %d:%d, got %+v", item.MessageID, start, start+len("redis"), item)
				}
			}
		})
	}
	// Normalized FTS tokens may have no strict spelling location. Fall back to
	// another positive clause, never to words that only occur in the exclusion.
	for _, tc := range []struct{ body, query, match string }{
		{"café then redis", "cafe AND redis", "redis"},
		{"prefix redis suffix", "missing NOT redis", ""},
	} {
		node, err := parseTextQuery(tc.query, "advanced")
		if err != nil {
			t.Fatal(err)
		}
		lo, hi := lexicalExcerpt(tc.body, node)
		if tc.match == "" {
			if lo != 0 || hi != len(tc.body) {
				t.Fatalf("exclusion became the excerpt: %d:%d", lo, hi)
			}
		} else if tc.body[lo:hi] != tc.match {
			t.Fatalf("wrong positive excerpt: %q", tc.body[lo:hi])
		}
	}
}

func TestTextSearchFlatExclusions(t *testing.T) {
	c := textFixture(t)
	for _, separator := range []string{" NOT ", " AND NOT "} {
		for _, mixed := range []bool{false, true} {
			query := "cache"
			for i := 0; i < 18; i++ {
				query += separator + fmt.Sprintf("absent%d", i)
				if mixed {
					query += " AND cache"
				}
			}
			result, err := c.SearchText(t.Context(), TextSearchOptions{Query: query})
			if err != nil {
				t.Fatalf("flat exclusions (mixed=%t): %v", mixed, err)
			}
			if !slices.Equal(resultIDs(result), []string{"a", "o", "span"}) {
				t.Fatalf("exclusions changed hits: %v", resultIDs(result))
			}
			result, err = c.SearchText(t.Context(), TextSearchOptions{Query: query + " NOT cache"})
			if err != nil || len(result.Items) != 0 {
				t.Fatalf("last exclusion ignored: %+v %v", result, err)
			}
		}
	}
	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"cache NOT (redis NOT secret)", []string{"a", "o", "span"}},
		{"cache NOT redis NOT secret", []string{"a", "o"}},
		{"(cache NOT redis) NOT secret", []string{"a", "o"}},
		{"cache NOT redis AND timeout OR buffer", []string{"a", "b"}},
	} {
		result, err := c.SearchText(t.Context(), TextSearchOptions{Query: tc.query})
		if err != nil || !slices.Equal(resultIDs(result), tc.want) {
			t.Fatalf("%s: %v %v", tc.query, resultIDs(result), err)
		}
	}
	// TestTextSearchFlatBooleanQueries rejects excessive nesting in the parser.
	if _, err := c.SearchText(t.Context(), TextSearchOptions{Query: "cache" + strings.Repeat(" NOT absent", 128)}); err == nil {
		t.Fatal("query limit bypassed: 128 exclusions")
	}
}

func TestTextSearchFuzzyCaseSensitiveExpansionBoundaries(t *testing.T) {
	c := textFixture(t)
	for _, sample := range []struct{ id, body string }{{"plural", "migrations"}, {"upper", "Migrations"}} {
		if _, err := c.DB.Exec(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash) VALUES(?,'c',?,'user','message',?,'hash')`, sample.id, sample.id, sample.body); err != nil {
			t.Fatal(err)
		}
		if _, err := c.DB.Exec(`INSERT INTO messages_fts(message_id,text) VALUES(?,?)`, sample.id, sample.body); err != nil {
			t.Fatal(err)
		}
	}
	for _, sensitive := range []bool{false, true} {
		result, err := c.SearchText(t.Context(), TextSearchOptions{Query: "migration", Fuzzy: true, CaseSensitive: sensitive})
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"b", "plural", "upper"}
		if sensitive {
			want = want[:2]
		}
		if !slices.Equal(resultIDs(result), want) {
			t.Errorf("case_sensitive=%t: got %v, want %v", sensitive, resultIDs(result), want)
		}
		for _, item := range result.Items {
			if item.MessageID == "plural" && (item.StartByte != 0 || item.EndByte != len("migrations")) {
				t.Errorf("case_sensitive=%t: plural excerpt %d:%d", sensitive, item.StartByte, item.EndByte)
			}
		}
	}
	node := &textQuery{Op: "term", Text: "migration", expansions: []string{"migration", "migrations"}}
	body := "premigrations migrationsx migrations migration"
	for _, match := range node.strictLocations(body, true, false, 0) {
		if word := body[match[0]:match[1]]; word != "migration" && word != "migrations" {
			t.Fatalf("non-token expansion matched: %q", word)
		}
	}
	if matches := node.strictLocations(body, true, false, 0); len(matches) != 2 {
		t.Fatalf("expected exactly two whole tokens: %v", matches)
	}
	if start, end := lexicalExcerpt("prefix migrations suffix", node); start != 7 || end != 17 {
		t.Fatalf("plural excerpt missed the expansion: %d:%d", start, end)
	}
}

func TestTextSearchByteBudgetAlwaysAdvances(t *testing.T) {
	c := textFixture(t)
	for _, body := range []string{"cache", "cache larger than the page budget"} {
		if _, err := c.DB.Exec(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash) VALUES(?,'c',?,'user','message',?,'hash')`, body, body, body); err != nil {
			t.Fatal(err)
		}
		if _, err := c.DB.Exec(`INSERT INTO messages_fts(message_id,text) VALUES(?,?)`, body, body); err != nil {
			t.Fatal(err)
		}
	}
	for _, mode := range []string{"terms", "literal", "regex"} {
		t.Run(mode, func(t *testing.T) {
			options := TextSearchOptions{Query: "cache", Mode: mode, MaxBytes: 10, MaxCandidates: 1}
			var last textSearchResult
			var before int64 = 1<<63 - 1
			seen := []string{}
			for page := 0; page < 20; page++ {
				result, err := c.SearchText(t.Context(), options)
				if err != nil {
					t.Fatal(err)
				}
				if result.VerifiedBytes > options.MaxBytes {
					t.Fatalf("byte limit exceeded: %+v", result)
				}
				seen = append(seen, resultIDs(result)...)
				last = result
				if result.NextCursor == "" {
					break
				}
				var cursor textSearchCursor
				data, err := base64Cursor(result.NextCursor)
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(data, &cursor); err != nil {
					t.Fatal(err)
				}
				if cursor.Before >= before {
					t.Fatalf("page %d did not advance past candidate %d: %+v", page, before, result)
				}
				before, options.Cursor = cursor.Before, result.NextCursor
			}
			if last.NextCursor != "" || last.Complete || last.CountRelation != "lower_bound" || last.SkippedOversize == 0 || !slices.Equal(seen, []string{"cache"}) {
				t.Fatalf("scan must finish, count oversized records, and return smaller matches: seen=%v result=%+v", seen, last)
			}
		})
	}
}

func TestLibraryFindMetadataIgnoresScopedCursor(t *testing.T) {
	catalog, config := libraryFixture(t)
	if _, err := catalog.DB.Exec(`INSERT INTO messages_fts(message_id,text) SELECT id,text FROM messages`); err != nil {
		t.Fatal(err)
	}
	if err := catalog.refreshAllLibrary(t.Context()); err != nil {
		t.Fatal(err)
	}
	// A one-hit page gives the same scoped cursor as the Library's larger batch.
	page, err := catalog.SearchText(t.Context(), TextSearchOptions{Query: "parser", WorkspaceIDs: []string{"c"}, SelectedOnly: true, Limit: 1})
	if err != nil || page.NextCursor == "" {
		t.Fatalf("scoped cursor: %+v %v", page, err)
	}
	server := testServer(t, config, catalog)
	params := url.Values{"find": {"parser"}, "find_fuzzy": {"0"}, "find_cursor": {page.NextCursor}}
	request := func(t *testing.T, method, path, body string) string {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer test-token")
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, recorder.Code, recorder.Body.String())
		}
		return recorder.Body.String()
	}
	// Workspace rows and metrics cannot resume a message batch: a cursor would
	// silently exclude earlier workspaces before global filtering/sorting.
	where := `[{"field":"title","op":"=","value":"Charlie"}]`
	for endpoint, body := range map[string]string{
		"rows-v2": `{"version":2,"profile":"qt-sqlite-v1","select":["title"],"where":` + where + `,"limit":10}`,
		"metrics": `{"version":2,"profile":"qt-sqlite-v1","where":` + where + `,"metrics":[{"id":"n","op":"count","groupBy":[]}]}`,
	} {
		response := apiCall(t, server, config.APIToken, http.MethodPost, "/api/query/library/"+endpoint+"?"+params.Encode(), body)
		if response.Code != 400 || !strings.Contains(response.Body.String(), "without a message cursor") {
			t.Fatalf("workspace search accepted a message cursor: %d %s", response.Code, response.Body.String())
		}
	}
	for _, endpoint := range []string{"distinct?field=title", "field-stats?fields=title"} {
		t.Run(endpoint, func(t *testing.T) {
			path := "/api/query/library/" + endpoint + "&" + params.Encode()
			got := request(t, http.MethodGet, path, "")
			withoutCursor := strings.Replace(path, "&find_cursor="+url.QueryEscape(page.NextCursor), "", 1)
			want := request(t, http.MethodGet, withoutCursor, "")
			if got != want || strings.Contains(got, "Bravo") {
				t.Fatalf("metadata changed with cursor: got %s, want %s", got, want)
			}
		})
	}
}

func TestTextSearchMetadataPlanningPreservesMatches(t *testing.T) {
	c := textFixture(t)
	execTx(t, c.DB,
		`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at) VALUES
 ('offset','c','offset','user','message','cache','offset','2026-10-12T00:30:00+01:00'),
 ('space','c','space','assistant','message','cache','space','2026-10-11 23:45:00'),
 ('system-reasoning','c','system-reasoning','system','reasoning','cache','system','2026-10-12T00:00:00Z')`,
		`INSERT INTO messages_fts(message_id,text) SELECT id,text FROM messages WHERE id IN ('offset','space','system-reasoning')`,
		`INSERT OR REPLACE INTO message_fts_rows SELECT message_id,rowid FROM messages_fts`)
	cases := []struct {
		o    TextSearchOptions
		want []string
	}{
		{TextSearchOptions{Query: "cache", From: "2100-01-01"}, []string{}},
		{TextSearchOptions{Query: "cache", Roles: []string{"missing"}}, []string{}},
		{TextSearchOptions{Query: "cache", Roles: []string{"system"}}, []string{"system-reasoning"}},
		{TextSearchOptions{Query: "cache", Kinds: []string{"reasoning"}}, []string{"system-reasoning"}},
		{TextSearchOptions{Query: "cache", From: "2026-10-11T23:00:00Z", To: "2026-10-11"}, []string{"offset", "space"}},
		{TextSearchOptions{Query: "cache", Surface: "reasoning", To: "2026-10-12"}, []string{"system-reasoning"}},
		{TextSearchOptions{Query: "cache", Surface: "user", Roles: []string{"assistant"}}, []string{}},
	}
	for _, maxRows := range []int{256, 1} {
		override(t, &textSearchPlanMaxRows, maxRows)
		for _, tc := range cases {
			result, err := c.SearchText(t.Context(), tc.o)
			if err != nil || !result.Complete || !slices.Equal(resultIDs(result), tc.want) {
				t.Fatalf("limit=%d options=%+v ids=%v complete=%v err=%v", maxRows, tc.o, resultIDs(result), result.Complete, err)
			}
		}
	}
	// Legacy/partial mappings must still find their unmapped eligible messages.
	execTx(t, c.DB, `DELETE FROM message_fts_rows WHERE message_id IN ('offset','space')`)
	result, err := c.SearchText(t.Context(), cases[4].o)
	if err != nil || !slices.Equal(resultIDs(result), cases[4].want) {
		t.Fatalf("partial mapping: %v %v", resultIDs(result), err)
	}
}

func TestTextSearchASCIIPrefilterIsConservative(t *testing.T) {
	verifier, err := newTextVerifier(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer verifier.db.Close()
	for _, query := range []string{"cache", `"foo bar"`, "foo*", "cache NOT redis", "(missing AND cache) OR retry", "timeout NEAR/8 retry", "retry PRE/8 timeout", "café", "cache café"} {
		node, err := parseTextQuery(query, "advanced")
		if err != nil {
			t.Fatal(err)
		}
		for _, body := range []string{"cache", "cache redis", "foo_bar", "foobar", "prefixcache cachex cache", "timeout retry", "retry then timeout", "cafe", "café", "cache cafe", "unrelated", "foo\x00bar"} {
			_, _, match, err := verifier.match(t.Context(), node, body, false, false)
			if err != nil {
				t.Fatal(err)
			}
			if match && !node.asciiCandidate(body) {
				t.Fatalf("prefilter discarded %q in %q", query, body)
			}
		}
	}
}

func TestSearchSnippetBoundsUnicode(t *testing.T) {
	body := strings.Repeat("é", 200) + "needle" + strings.Repeat("🦉", 200)
	got := searchSnippet(body, 400, 406)
	want := "…" + strings.Repeat("é", 90) + "needle" + strings.Repeat("🦉", 130) + "…"
	if got != want {
		t.Fatalf("snippet changed rune boundaries: %q", got)
	}
	// The display truncation may land inside a multibyte character.
	got = searchSnippet(body, 400, 409)
	if !utf8.ValidString(got) {
		t.Fatal("snippet split a UTF-8 rune")
	}
}
