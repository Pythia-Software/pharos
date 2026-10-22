package archive

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Intercept the candidate statement or the generation reads, leaving the rest
// on real SQLite. This makes timeouts deterministic instead of depending on
// machine speed or a multi-million-row fixture.
type searchHookDriver struct {
	driver.Driver
	query      func(context.Context) error
	next       func(context.Context) error
	generation func(context.Context) error
}
type searchHookConn struct {
	driver.Conn
	hooks *searchHookDriver
}
type searchHookRows struct {
	driver.Rows
	ctx  context.Context
	next func(context.Context) error
}

func (d *searchHookDriver) Open(name string) (driver.Conn, error) {
	c, err := d.Driver.Open(name)
	return &searchHookConn{Conn: c, hooks: d}, err
}
func (c *searchHookConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if c.hooks.generation != nil && strings.Contains(query, "key='text_search_generation'") {
		if err := c.hooks.generation(ctx); err != nil {
			return nil, err
		}
	}
	candidate := strings.Contains(query, "AS search_body")
	if candidate && c.hooks.query != nil {
		if err := c.hooks.query(ctx); err != nil {
			return nil, err
		}
	}
	rows, err := c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
	if candidate && err == nil && c.hooks.next != nil {
		rows = &searchHookRows{Rows: rows, ctx: ctx, next: c.hooks.next}
	}
	return rows, err
}
func (r *searchHookRows) Next(values []driver.Value) error {
	if err := r.next(r.ctx); err != nil {
		return err
	}
	return r.Rows.Next(values)
}

var searchHookID atomic.Uint64

func hookTextSearch(t *testing.T, c *Catalog, query, next func(context.Context) error) {
	t.Helper()
	hookSearchReads(t, c, &searchHookDriver{query: query, next: next})
}

// hookTextSearchGeneration calls read before each read of the search
// generation: one before a page and one after it.
func hookTextSearchGeneration(t *testing.T, c *Catalog, read func(context.Context) error) {
	t.Helper()
	hookSearchReads(t, c, &searchHookDriver{generation: read})
}

func hookSearchReads(t *testing.T, c *Catalog, hooks *searchHookDriver) {
	t.Helper()
	original := c.reader()
	hooks.Driver = original.Driver()
	name := fmt.Sprintf("search-hook-%d", searchHookID.Add(1))
	sql.Register(name, hooks)
	db, err := sql.Open(name, c.Path)
	if err != nil {
		t.Fatal(err)
	}
	// Searches read through the read pool; writes stay on the writer pool.
	reads := c.reads
	c.reads = db
	t.Cleanup(func() { db.Close(); c.reads = reads })
}

func TestTextSearchTimeoutRequiresProgress(t *testing.T) {
	slowTest(t)
	for _, phase := range []string{"query", "first row", "after row"} {
		for _, resumed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/resumed=%t", phase, resumed), func(t *testing.T) {
				c := textFixture(t)
				o := TextSearchOptions{Query: "cache", Limit: 1}
				if resumed {
					first, err := c.SearchText(t.Context(), o)
					if err != nil || first.NextCursor == "" {
						t.Fatalf("first page: %+v %v", first, err)
					}
					o.Cursor = first.NextCursor
				}
				o.Limit, o.TimeoutMS = 50, 500
				stall := func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
				if phase == "query" {
					hookTextSearch(t, c, stall, nil)
				} else {
					rows := 0
					hookTextSearch(t, c, nil, func(ctx context.Context) error {
						rows++
						if phase == "after row" && rows == 1 {
							return nil
						}
						return stall(ctx)
					})
				}
				result, err := c.SearchText(t.Context(), o)
				if err != nil || result.Complete || result.CountRelation != "lower_bound" {
					t.Fatalf("timeout must report incomplete coverage: %+v %v", result, err)
				}
				if phase == "after row" {
					if len(result.Items) != 1 || result.NextCursor == "" || result.NextCursor == o.Cursor || result.StopReason != "time_budget" {
						t.Fatalf("lost resumable progress: %+v", result)
					}
				} else if len(result.Items) != 0 || result.NextCursor != "" || result.StopReason != "time_budget_no_progress" || !strings.Contains(result.StopMessage, "Narrow") {
					t.Fatalf("non-advancing cursor offered: %+v", result)
				}
			})
		}
	}
}

func appendSearchMessage(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash) VALUES('appended','c','appended','user','message','cache','new')`); err != nil {
		return err
	}
	if err = replaceMessageFTS(tx, "appended"); err != nil {
		return err
	}
	return tx.Commit()
}

func TestTextSearchAppendAndNoopKeepCursor(t *testing.T) {
	for _, mode := range []string{"advanced", "literal", "regex"} {
		t.Run(mode, func(t *testing.T) {
			c := textFixture(t)
			o := TextSearchOptions{Query: "cache", Mode: mode, Surface: "user", Limit: 1}
			first, err := c.SearchText(t.Context(), o)
			if err != nil || first.NextCursor == "" {
				t.Fatalf("first page: %+v %v", first, err)
			}
			if _, err := c.DB.Exec(`UPDATE messages SET text=text, selected=selected; UPDATE conversations SET provider=provider; UPDATE workspaces SET source_kind=source_kind; UPDATE message_authorship_spans SET category=category`); err != nil {
				t.Fatal(err)
			}
			// Exercise the ingest upsert twice, including its conflict path, then
			// append; an old conflict flag must not poison the next insert.
			for i := 0; i < 2; i++ {
				if _, err := c.DB.Exec(messageWriteSQL, "a", "c", "a", "user", "message", nil, nil, "cache race condition timeout retry foo::bar café", nil, 0, "2026-10-11T12:00:00Z", nil, nil, nil, nil, "a", 1, nil); err != nil {
					t.Fatal(err)
				}
			}
			if err := appendSearchMessage(c.DB); err != nil {
				t.Fatal(err)
			}
			// Ordinary searches do not depend on authorship classification.
			if _, err := c.DB.Exec(`DELETE FROM message_authorship_spans`); err != nil {
				t.Fatal(err)
			}
			o.Cursor, o.Limit = first.NextCursor, 50
			rest, err := c.SearchText(t.Context(), o)
			if err != nil || !rest.Complete || rest.Count != 3 || !slices.Equal(resultIDs(rest), []string{"a", "o"}) || rest.Generation != first.Generation {
				t.Fatalf("append/no-op invalidated remaining messages: %+v %v", rest, err)
			}
		})
	}
}

func TestTextSearchGenerationTracksRelevantChanges(t *testing.T) {
	for _, tc := range []struct {
		name, surface, sql string
		fuzzy              bool
	}{
		{"message edit", "user", `UPDATE messages SET text=text||' edited' WHERE id='a'`, false},
		{"message delete", "user", `DELETE FROM messages WHERE id='a'`, false},
		{"message replacement", "user", `INSERT OR REPLACE INTO messages(id,conversation_id,native_id,role,kind,text,content_hash) VALUES('a','c','a','user','message','cache replaced','new')`, false},
		{"span edit", "typed", `UPDATE message_authorship_spans SET end_byte=11 WHERE message_id='span' AND category='typed'`, false},
		{"span rebuild", "typed", `DELETE FROM message_authorship_spans`, false},
		{"FTS reorder", "user", `DELETE FROM message_fts_rows WHERE message_id='a'`, false},
		{"FTS replacement", "user", `INSERT OR REPLACE INTO message_fts_rows(message_id,fts_rowid) VALUES('a',99999)`, false},
		{"fuzzy vocabulary", "user", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := textFixture(t)
			o := TextSearchOptions{Query: "cache", Surface: tc.surface, Fuzzy: tc.fuzzy, Limit: 1}
			first, err := c.SearchText(t.Context(), o)
			if err != nil || first.NextCursor == "" {
				t.Fatalf("first page: %+v %v", first, err)
			}
			if tc.sql != "" {
				_, err = c.DB.Exec(tc.sql)
			} else {
				err = appendSearchMessage(c.DB)
			}
			if err != nil {
				t.Fatal(err)
			}
			o.Cursor = first.NextCursor
			if _, err := c.SearchText(t.Context(), o); err == nil || !strings.Contains(err.Error(), "cursor expired") {
				t.Fatalf("unsafe continuation accepted: %v", err)
			}
		})
	}
}

func TestTextSearchAppendDuringPage(t *testing.T) {
	for _, edit := range []bool{false, true} {
		t.Run(fmt.Sprintf("edit=%t", edit), func(t *testing.T) {
			c := textFixture(t)
			original := c.DB
			rows := 0
			hookTextSearch(t, c, nil, func(ctx context.Context) error {
				rows++
				if rows != 2 {
					return nil
				}
				if edit {
					_, err := original.ExecContext(ctx, `UPDATE messages SET text=text||' edited' WHERE id='a'`)
					return err
				}
				return appendSearchMessage(original)
			})
			result, err := c.SearchText(t.Context(), TextSearchOptions{Query: "cache"})
			if edit {
				if err == nil || !strings.Contains(err.Error(), "messages changed") {
					t.Fatalf("overlapping edit accepted: %+v %v", result, err)
				}
			} else if err != nil || !result.Complete || len(result.Items) != 3 {
				t.Fatalf("overlapping append rejected: %+v %v", result, err)
			}
		})
	}
}

// An overlap check that cannot finish withholds the page with a retryable
// stop instead of failing the search, which Library Find showed as a 500.
func TestTextSearchSlowGenerationCheck(t *testing.T) {
	c := textFixture(t)
	config := defaultConfig(t.TempDir() + "/config.toml")
	config.APIToken = "test-token"
	server := testServer(t, config, c)
	// The check waits for what is left of a 300 ms budget, or 10 ms if the
	// page used it all.
	override(t, &textSearchCheckTimeout, 10*time.Millisecond)
	var stall atomic.Bool
	var reads atomic.Int64
	var stallAt atomic.Int64
	stallAt.Store(2)
	hookTextSearchGeneration(t, c, func(ctx context.Context) error {
		if reads.Add(1) == stallAt.Load() && stall.Load() {
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	})
	stall.Store(true)
	req := httptest.NewRequest(http.MethodPost, "/api/search/text", strings.NewReader(`{"query":"cache","limit":1,"timeout_ms":300}`))
	req.Header.Set("Authorization", "Bearer test-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, req)
	var result textSearchResult
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil {
		t.Fatalf("slow check failed the search: %d %s", response.Code, response.Body.String())
	}
	if len(result.Items) != 0 || result.Count != 0 || result.Complete || result.NextCursor != "" ||
		result.StopReason != "consistency_check_timeout" || !strings.Contains(result.StopMessage, "Retry") {
		t.Fatalf("unconfirmed page not withheld: %+v", result)
	}
	find := LibraryFindOptions{Query: "cache", Search: TextSearchOptions{TimeoutMS: 300}}
	// Library's cache-generation read precedes the search's two reads.
	stallAt.Store(reads.Load() + 3)
	set, err := c.libraryFindMatches(t.Context(), find)
	if err != nil || len(set.matches) != 0 || !set.limited || set.search.StopReason != "consistency_check_timeout" {
		t.Fatalf("Library Find did not withhold the page: %+v %v", set, err)
	}
	// Library Find does not cache the withheld page, so a retry searches again.
	stall.Store(false)
	if set, err = c.libraryFindMatches(t.Context(), find); err != nil || len(set.matches) == 0 {
		t.Fatalf("retry did not search again: %+v %v", set, err)
	}
}

func TestTextSearchCursorTriggerUpgrade(t *testing.T) {
	c := textFixture(t)
	// Simulate an already-opened catalog from the first PR implementation.
	if _, err := c.DB.Exec(`DELETE FROM meta WHERE key='text_search_cursor_version';
 CREATE TRIGGER text_search_message_insert AFTER INSERT ON messages BEGIN
 UPDATE meta SET value=CAST(value AS INTEGER)+1 WHERE key='text_search_generation'; END;
 CREATE TRIGGER text_search_message_update AFTER UPDATE ON messages BEGIN
 UPDATE meta SET value=CAST(value AS INTEGER)+1 WHERE key='text_search_generation'; END;
 CREATE TRIGGER text_search_message_authorship_spans_delete AFTER DELETE ON message_authorship_spans BEGIN
 UPDATE meta SET value=CAST(value AS INTEGER)+1 WHERE key='text_search_generation'; END;`); err != nil {
		t.Fatal(err)
	}
	o := TextSearchOptions{Query: "cache", Limit: 1}
	old, err := c.SearchText(t.Context(), o)
	if err != nil || old.NextCursor == "" {
		t.Fatalf("legacy page: %+v %v", old, err)
	}
	for i := 0; i < 2; i++ {
		if _, err := c.DB.Exec(schemaSQL()); err != nil {
			t.Fatal(err)
		}
	}
	o.Cursor = old.NextCursor
	if _, err := c.SearchText(t.Context(), o); err == nil {
		t.Fatal("pre-upgrade cursor accepted")
	}
	o.Cursor = ""
	first, err := c.SearchText(t.Context(), o)
	if err != nil {
		t.Fatal(err)
	}
	if err := appendSearchMessage(c.DB); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DB.Exec(`UPDATE messages SET text=text; DELETE FROM message_authorship_spans`); err != nil {
		t.Fatal(err)
	}
	o.Cursor, o.Limit = first.NextCursor, 50
	if rest, err := c.SearchText(t.Context(), o); err != nil || !rest.Complete || rest.Count != 3 {
		t.Fatalf("legacy triggers still invalidate continuation: %+v %v", rest, err)
	}
}

func TestTextSearchSparseRolesResumeRejectedPostings(t *testing.T) {
	c := textFixture(t)
	options := TextSearchOptions{Query: "race", Surface: "all", Roles: []string{"assistant"}, MaxCandidates: 1}
	first, err := c.SearchText(t.Context(), options)
	if err != nil || len(first.Items) != 0 || first.Count != 0 || first.NextCursor == "" || first.Candidates != 1 || first.VerifiedBytes != 0 {
		t.Fatalf("rejected role did not advance: %+v %v", first, err)
	}
	options.Cursor = first.NextCursor
	ids := []string{}
	var last textSearchResult
	for page := 0; page < 10; page++ {
		last, err = c.SearchText(t.Context(), options)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, resultIDs(last)...)
		if last.NextCursor == "" {
			break
		}
		options.Cursor = last.NextCursor
	}
	if !last.Complete || last.Count != 1 || len(ids) != 1 || ids[0] != "b" {
		t.Fatalf("role pagination lost matches: %v %+v", ids, last)
	}
}
