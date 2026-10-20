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
	for i, body := range []string{"ſearch quality", "Kelvin quality", "search\nquality", "abx", "abz"} {
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
	for _, q := range []string{`(?s)search.*quality`, `(?i)kelvin`, `ab(x|z)`, `(?i:search)|kelvin`, `(?s)search.*quality|nomatch`} {
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
