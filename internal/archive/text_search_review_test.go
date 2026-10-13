package archive

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
)

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
	for _, query := range []string{strings.Repeat("(", 17) + "cache" + strings.Repeat(")", 17), "cache" + strings.Repeat(" NOT absent", 128)} {
		if _, err := c.SearchText(t.Context(), TextSearchOptions{Query: query}); err == nil {
			t.Fatalf("query limit bypassed: %s", query)
		}
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
	for _, match := range node.strictLocations(body, true, false) {
		if word := body[match[0]:match[1]]; word != "migration" && word != "migrations" {
			t.Fatalf("non-token expansion matched: %q", word)
		}
	}
	if matches := node.strictLocations(body, true, false); len(matches) != 2 {
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
	server := NewServer(config, catalog)
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
	// Rows and metrics must still honor the table filter and resume its cursor.
	where := `[{"field":"title","op":"=","value":"Charlie"}]`
	request(t, http.MethodPost, "/api/query/library/rows-v2?"+params.Encode(), `{"version":2,"profile":"qt-sqlite-v1","select":["title"],"where":`+where+`,"limit":10}`)
	request(t, http.MethodPost, "/api/query/library/metrics?"+params.Encode(), `{"version":2,"profile":"qt-sqlite-v1","where":`+where+`,"metrics":[{"id":"n","op":"count","groupBy":[]}]}`)
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
