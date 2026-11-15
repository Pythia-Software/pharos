package archive

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	qt "github.com/Pythia-Software/query-table/backends/go"
	"github.com/gbdubs/pharos/internal/querytable"
)

// libraryFixture creates three workspaces touching every derived-column
// dependency; a and b are native-alias mirrors (b, the tl1 side, represents).
func libraryFixture(t *testing.T) (*Catalog, Config) {
	t.Helper()
	catalog, config := testCatalog(t)
	statements := []string{
		`INSERT INTO repositories(id,display_name,created_at,updated_at) VALUES('repo','pharos','x','x')`,
		`INSERT INTO workspaces(id,source_kind,source_account,source_id,repository_id,title,branch,activity_at,indexed_at) VALUES
			('a','codex','local','a','repo','Alpha','main','2026-09-23T03:00:00Z','x'),
			('b','tl1','local','b','repo','Bravo',NULL,'2026-09-23T01:00:00Z','x'),
			('c','canonical','local','c',NULL,'Charlie','feature','2026-09-23T02:00:00Z','x')`,
		`INSERT INTO conversations(id,workspace_id,provider,model,account,native_id) VALUES
			('ca','a','codex','gpt','local','ca'),('cb','b','tl1',NULL,'local','cb'),('cc','c','claude','opus','local','cc')`,
		`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,created_at,content_hash) VALUES
			('m1','cc','1','user','message','Please fix the parser','2026-09-23T02:00:00Z','h'),
			('m2','cc','2','assistant','tool_call','{}','2026-09-23T02:00:01Z','h'),
			('m3','cc','3','tool','tool_result','{"exit_code":2}','2026-09-23T02:00:02Z','h'),
			('m4','cc','4','assistant','message','Fixed it','2026-09-23T02:00:03Z','h'),
			('m5','ca','1','user','message','Mirror prompt','2026-09-23T03:00:00Z','h'),
			('k1','cc','k1','system','metadata','{"subtype":"compact_boundary","type":"system"}','2026-09-23T02:00:04Z','h'),
			('k2','cc','k2','system','metadata','{"compaction_trigger":"auto","payload":{},"type":"compacted"}','2026-09-23T02:00:05Z','h'),
			('k3','cc','k3','system','metadata','{"payload":{},"type":"compacted"}','2026-09-23T02:00:06Z','h')`,
		// The call failed by its exit status, which only the tool ledger records.
		`INSERT INTO tool_calls(id,workspace_id,conversation_id,call_message_id,result_message_id,sequence,provider,kind,tool_name,tool_category,status,error_type,exit_code)
			VALUES('t1','c','cc','m2','m3',1,'claude','tool','Bash','shell','error','nonzero_exit',2)`,
		`INSERT INTO metrics(workspace_id,name,value,unit,status,extractor_version,observed_at) VALUES
			('c','total_tokens',500,'tokens','observed','test','x'),('a','total_tokens',900,'tokens','observed','test','x')`,
		`INSERT INTO metric_ledger(workspace_id,sequence,event_kind,error_type) VALUES('c',1,'tool','timeout')`,
		`INSERT INTO change_sets(id,workspace_id,classification) VALUES('cs','c','attempted')`,
		`INSERT INTO change_files(change_set_id,path,status,tracked) VALUES('cs','src/parser.go','modified',1),('cs','README.md','modified',1)`,
		`INSERT INTO pull_requests(id,host,repository_id,number,title) VALUES('pr','github.com','repo',42,'Parser fix')`,
		`INSERT INTO work_pr_links(workspace_id,pr_id,relationship,confidence,evidence_json) VALUES('c','pr','associated',1,'{}')`,
		`INSERT INTO summaries(workspace_id,initiation,outcome,evidence_json,model,extractor_version,created_at) VALUES('b','Summarized purpose',NULL,'{}','m','v','x')`,
		`INSERT INTO conversation_identity_links(left_id,right_id,relationship,confidence,evidence_json) VALUES('ca','cb','native-alias',1,'{}')`,
		`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,parent_id,kind,provider,depth) VALUES
			('sc','c','cc','main',NULL,'root','claude',0),('sc1','c','cc','t1','sc','subagent','claude',1),
			('sc2','c','cc','t2','sc1','subagent','claude',2)`,
	}
	execTx(t, catalog.DB, statements...)
	return catalog, config
}

func dirtyCount(t *testing.T, catalog *Catalog) int {
	t.Helper()
	var count int
	if err := catalog.DB.QueryRow("SELECT COUNT(*) FROM workspace_library_dirty").Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func libraryRowsByID(t *testing.T, catalog *Catalog) map[string]map[string]any {
	t.Helper()
	rows, err := catalog.searchRows(SearchOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]map[string]any{}
	for _, row := range rows {
		byID[firstString(row["id"])] = row
	}
	return byID
}

func TestLibraryStoredRowsMatchLiveComputation(t *testing.T) {
	catalog, _ := libraryFixture(t)
	if dirtyCount(t, catalog) != 3 {
		t.Fatalf("new workspaces were not marked dirty: %d", dirtyCount(t, catalog))
	}
	live := libraryRowsByID(t, catalog)
	if err := catalog.refreshAllLibrary(context.Background()); err != nil {
		t.Fatal(err)
	}
	if dirtyCount(t, catalog) != 0 {
		t.Fatal("refresh left dirty workspaces")
	}
	stored := libraryRowsByID(t, catalog)
	if !reflect.DeepEqual(live, stored) {
		t.Fatalf("stored rows differ from live rows:\nlive   %#v\nstored %#v", live, stored)
	}
	c := stored["c"]
	for field, want := range map[string]any{
		"conversation_count": int64(1), "turn_count": int64(1), "tool_use_count": int64(1), "tool_error_count": int64(1),
		"changed_file_count": int64(2), "file_edit_count": int64(2), "token_count": float64(500), "pr_count": int64(1),
		"pr_numbers": "42", "providers": "claude", "models": "opus", "error_types": "timeout", "branch_count": int64(1),
		"repository_name": nil, "subagent_count": int64(2), "subagent_depth": int64(2), "compaction_count": int64(2),
	} {
		if !reflect.DeepEqual(c[field], want) {
			t.Errorf("c.%s = %#v, want %#v", field, c[field], want)
		}
	}
	if len(stored) != 2 || stored["a"] != nil || firstString(stored["b"]["purpose"]) != "Summarized purpose" {
		t.Fatalf("mirror or summary handling changed: %#v", stored)
	}
}

func TestLibraryReflectsEveryDependencyBeforeAndAfterRefresh(t *testing.T) {
	catalog, _ := libraryFixture(t)
	if err := catalog.refreshAllLibrary(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, change := range []struct {
		name, statement string
		check           func(map[string]map[string]any) bool
	}{
		{"message insert", `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash) VALUES('m6','cc','6','user','message','Again','h')`,
			func(rows map[string]map[string]any) bool { return integer(rows["c"]["turn_count"]) == 2 }},
		{"agent session insert", `INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,parent_id,kind,provider,depth) VALUES('sc3','c','cc','t3','sc','subagent','claude',1)`,
			func(rows map[string]map[string]any) bool { return integer(rows["c"]["subagent_count"]) == 3 }},
		{"tool call update", `UPDATE tool_calls SET status='ok',error_type=NULL WHERE id='t1'`,
			func(rows map[string]map[string]any) bool { return integer(rows["c"]["tool_error_count"]) == 0 }},
		{"tool call insert", `INSERT INTO tool_calls(id,workspace_id,conversation_id,sequence,provider,kind,tool_name,tool_category,status) VALUES('t2','c','cc',2,'claude','tool','Read','file','ok')`,
			func(rows map[string]map[string]any) bool { return integer(rows["c"]["tool_use_count"]) == 2 }},
		// A boundary is stored by a trigger on its message, and reclassified
		// without the message changing.
		{"compaction boundary delete", `DELETE FROM compaction_boundaries WHERE message_id='k1'`,
			func(rows map[string]map[string]any) bool { return integer(rows["c"]["compaction_count"]) == 1 }},
		{"compaction message insert", `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash) VALUES('k4','cc','k4','system','metadata','{"subtype":"compact_boundary","type":"system"}','h')`,
			func(rows map[string]map[string]any) bool { return integer(rows["c"]["compaction_count"]) == 2 }},
		// An abandoned branch's prompt was still sent.
		{"message leaves the branch", `UPDATE messages SET selected=0 WHERE id='m6'`,
			func(rows map[string]map[string]any) bool { return integer(rows["c"]["turn_count"]) == 2 }},
		{"change file delete", `DELETE FROM change_files WHERE path='README.md'`,
			func(rows map[string]map[string]any) bool { return integer(rows["c"]["changed_file_count"]) == 1 }},
		{"metric update", `UPDATE metrics SET value=700 WHERE workspace_id='c'`,
			func(rows map[string]map[string]any) bool { return integer(rows["c"]["token_count"]) == 700 }},
		{"pull request retitle", `UPDATE pull_requests SET title='Renamed' WHERE id='pr'`,
			func(rows map[string]map[string]any) bool {
				return strings.Contains(firstString(rows["c"]["pr_details"]), "Renamed")
			}},
		{"summary insert", `INSERT INTO summaries(workspace_id,outcome,evidence_json,model,extractor_version,created_at) VALUES('c','Shipped','{}','m','v','x')`,
			func(rows map[string]map[string]any) bool { return firstString(rows["c"]["outcome"]) == "Shipped" }},
		{"ledger insert", `INSERT INTO metric_ledger(workspace_id,sequence,event_kind,error_type) VALUES('c',2,'tool','crash')`,
			func(rows map[string]map[string]any) bool {
				return strings.Contains(firstString(rows["c"]["error_types"]), "crash")
			}},
		{"conversation insert", `INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('cc2','c','codex','local','cc2')`,
			func(rows map[string]map[string]any) bool { return integer(rows["c"]["conversation_count"]) == 2 }},
		{"workspace insert", `INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('d','canonical','local','d','Delta','x')`,
			func(rows map[string]map[string]any) bool {
				return rows["d"] != nil && integer(rows["d"]["conversation_count"]) == 0
			}},
		{"workspace delete", `DELETE FROM workspaces WHERE id='d'`,
			func(rows map[string]map[string]any) bool { return rows["d"] == nil }},
		{"workspace column update", `UPDATE workspaces SET title='Charlie 2' WHERE id='c'`,
			func(rows map[string]map[string]any) bool { return rows["c"]["title"] == "Charlie 2" }},
	} {
		if _, err := catalog.DB.Exec(change.statement); err != nil {
			t.Fatalf("%s: %v", change.name, err)
		}
		before := libraryRowsByID(t, catalog)
		if !change.check(before) {
			t.Fatalf("%s: not visible before refresh: %#v", change.name, before)
		}
		if err := catalog.refreshAllLibrary(context.Background()); err != nil {
			t.Fatal(err)
		}
		after := libraryRowsByID(t, catalog)
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("%s: refreshed rows differ:\nbefore %#v\nafter  %#v", change.name, before, after)
		}
	}
}

// A refresh computes from a read snapshot, so the writer is free meanwhile.
// What was written to a workspace while its row was computed must not be
// lost: the workspace stays marked and nothing computed before is stored.
func TestLibraryRefreshKeepsWorkspacesWrittenWhileComputing(t *testing.T) {
	catalog, _ := libraryFixture(t)
	ctx := context.Background()
	if err := catalog.refreshAllLibrary(ctx); err != nil {
		t.Fatal(err)
	}
	// Idle: nothing to do is found without the writer.
	writes := catalog.writers.next
	if count, err := catalog.refreshLibrary(ctx, 10, nil); err != nil || count != 0 {
		t.Fatalf("idle refresh: %d %v", count, err)
	}
	if catalog.writers.next != writes {
		t.Fatal("an idle refresh took the catalog writer")
	}
	turns := func(id string) (stored int) {
		t.Helper()
		if err := catalog.DB.QueryRow("SELECT turn_count FROM workspace_library WHERE workspace_id=?", id).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		return stored
	}
	for _, written := range []struct {
		name, during string
		want         int
	}{
		// A dependency written while the workspace is already marked.
		{"a message", `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash) VALUES('late','cc','late','user','message','Later','h')`, 3},
		// A repair marking it by hand, for a change no trigger saw.
		{"a mark", `INSERT OR IGNORE INTO workspace_library_dirty(workspace_id) VALUES('c')`, 4},
	} {
		execSQL(t, catalog.DB, `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash) VALUES('early-`+written.name+`','cc','early-`+written.name+`','user','message','Early','h')`,
			`UPDATE summaries SET initiation='Changed by `+written.name+`' WHERE workspace_id='b'`)
		before := turns("c")
		override(t, &libraryComputedHook, func() {
			libraryComputedHook = nil
			execSQL(t, catalog.DB, written.during)
		})
		attempted, stored, err := catalog.refreshLibraryBatch(ctx, 10, nil)
		if err != nil || attempted != 2 || stored != 1 {
			t.Fatalf("%s while computing: attempted %d, stored %d, %v", written.name, attempted, stored, err)
		}
		if got := turns("c"); got != before {
			t.Fatalf("%s while computing: stored a row computed before it (%d turns, was %d)", written.name, got, before)
		}
		var marked string
		if err := catalog.DB.QueryRow("SELECT group_concat(workspace_id) FROM workspace_library_dirty").Scan(&marked); err != nil || marked != "c" {
			t.Fatalf("%s while computing: marked %q, want c: %v", written.name, marked, err)
		}
		if err := catalog.refreshAllLibrary(ctx); err != nil {
			t.Fatal(err)
		}
		if got := turns("c"); got != written.want || dirtyCount(t, catalog) != 0 {
			t.Fatalf("after %s: %d turns stored, want %d; %d marked", written.name, got, written.want, dirtyCount(t, catalog))
		}
	}
	// A refresh that fails gives its workspaces back at once.
	execSQL(t, catalog.DB, `UPDATE summaries SET initiation='Again' WHERE workspace_id='b'`)
	stopped, cancel := context.WithCancel(ctx)
	override(t, &libraryComputedHook, func() { cancel() })
	if _, _, err := catalog.refreshLibraryBatch(stopped, 10, nil); err == nil {
		t.Fatal("a stopped refresh stored its batch")
	}
	libraryComputedHook = nil
	if count, err := catalog.refreshLibrary(ctx, 10, nil); err != nil || count != 1 || dirtyCount(t, catalog) != 0 {
		t.Fatalf("after a stopped refresh: %d refreshed, %d marked, %v", count, dirtyCount(t, catalog), err)
	}
}

// projectionTriggerCases are the stored projections whose UPDATE triggers
// mark only when a column their statements read has changed.
func projectionTriggerCases() []struct {
	name, trigger, dirty string
	statements           []string
	lookups              map[string]func(string) string
} {
	library := map[string]func(string) string{}
	for _, dependency := range libraryDependencies {
		if !dependency.insertOnly {
			library[dependency.table] = dependency.workspaces
		}
	}
	return []struct {
		name, trigger, dirty string
		statements           []string
		lookups              map[string]func(string) string
	}{
		{"Search view", "workspace_library_", "workspace_library_dirty", libraryViewStatements(), library},
		{"workspace metrics", "mcp_metric_", "mcp_metric_dirty", metricProjectionStatements, metricDependencies},
	}
}

func projectionTableColumns(t *testing.T, q queryer, table string) []string {
	t.Helper()
	rows, err := queryMaps(q, "SELECT name FROM pragma_table_info(?)", table)
	if err != nil || len(rows) == 0 {
		t.Fatalf("%s columns: %d %v", table, len(rows), err)
	}
	columns := []string{}
	for _, row := range rows {
		columns = append(columns, firstString(row["name"]))
	}
	return columns
}

// SQLite's own name resolution says which columns a projection reads: a
// statement no longer compiles against a table that lacks a column it reads.
// Every such column must be one its table's UPDATE trigger compares, or a
// change to it would leave stored rows stale with nothing marked.
func TestProjectionTriggersWatchEveryColumnRead(t *testing.T) {
	catalog, _ := testCatalog(t)
	for _, projection := range projectionTriggerCases() {
		for table, lookup := range projection.lookups {
			var trigger string
			if err := catalog.DB.QueryRow("SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?", projection.trigger+table+"_update").Scan(&trigger); err != nil {
				t.Fatalf("%s: %s update trigger: %v", projection.name, table, err)
			}
			// The trigger's own expression reads the changed row as NEW.
			statements := append(slices.Clone(projection.statements),
				"SELECT EXISTS("+strings.ReplaceAll(lookup("NEW"), "NEW.", "changed_row.")+") FROM "+table+" changed_row")
			columns := projectionTableColumns(t, catalog.DB, table)
			indexes, err := queryMaps(catalog.DB, `SELECT l.name,i.name column_name FROM pragma_index_list(?) l JOIN pragma_index_info(l.name) i
				WHERE l.name NOT LIKE 'sqlite_%' AND i.name IS NOT NULL ORDER BY l.name,i.seqno`, table)
			if err != nil {
				t.Fatal(err)
			}
			// Only a statement that names the table can read it, and only a
			// column it names (none here selects a bare *).
			reading := slices.DeleteFunc(statements, func(statement string) bool { return !strings.Contains(statement, table) })
			// A temporary table of the same name, without the column, is the
			// one the statements then name. Its indexes keep their names for
			// INDEXED BY, less the column.
			reads := func(column string) bool {
				tx, err := catalog.DB.Begin()
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				kept := slices.DeleteFunc(slices.Clone(columns), func(name string) bool { return name == column })
				shadow := []string{`CREATE TEMP TABLE "` + table + `" AS SELECT "` + strings.Join(kept, `","`) + `" FROM main."` + table + `" WHERE 0`}
				indexed := map[string][]string{}
				for _, row := range indexes {
					if name := firstString(row["column_name"]); name != column {
						indexed[firstString(row["name"])] = append(indexed[firstString(row["name"])], name)
					}
				}
				for name, on := range indexed {
					shadow = append(shadow, `CREATE INDEX temp."`+name+`" ON "`+table+`"("`+strings.Join(on, `","`)+`")`)
				}
				for _, statement := range shadow {
					if _, err := tx.Exec(statement); err != nil {
						t.Fatalf("%s without %s: %v", table, column, err)
					}
				}
				for _, statement := range reading {
					rows, err := tx.Query("EXPLAIN "+statement, make([]any, strings.Count(statement, "?"))...)
					if err == nil {
						rows.Close()
						continue
					}
					// A named index that held only this column is gone too.
					if !strings.Contains(err.Error(), "no such column") && !strings.Contains(err.Error(), "no such index") {
						t.Fatalf("%s without %s.%s: %v", projection.name, table, column, err)
					}
					return true
				}
				return false
			}
			watched := []string{}
			for _, column := range columns {
				if strings.Contains(trigger, `OLD."`+column+`" IS NOT NEW."`+column+`"`) {
					watched = append(watched, column)
				} else if strings.Contains(strings.Join(reading, "\n"), column) && reads(column) {
					t.Errorf("%s reads %s.%s, which its update trigger does not compare", projection.name, table, column)
				}
			}
			// The check itself must still see a read where there is one.
			if !slices.ContainsFunc(watched, reads) {
				t.Errorf("%s: none of the columns %s's trigger compares (%v) is seen to be read", projection.name, table, watched)
			}
		}
	}
}

// A writer that stores rows again unchanged, as an upgrade's recomputation
// and a repeated index do, leaves both projections alone.
func TestProjectionsIgnoreRowsRewrittenUnchanged(t *testing.T) {
	catalog, _ := libraryFixture(t)
	execTx(t, catalog.DB, `INSERT INTO tool_ledger_state(conversation_id,version,updated_at) VALUES('cc','v','x')`,
		`INSERT INTO agent_session_usage(agent_session_id,usage_hour,model,attribution,total_tokens) VALUES('sc','2026-09-23T02','opus','reported',5)`,
		`INSERT INTO agent_session_messages(agent_session_id,message_id,source_order) VALUES('sc1','m2',1)`,
		`INSERT INTO conversation_group_membership(conversation_id,workspace_id,active) VALUES('cc','c',1)`,
		`INSERT INTO claude_chat_settings_observations(host_id,source_name,account,organization_uuid,conversation_uuid,observed_at,time_basis,cache_entry,snapshot_id,model)
			VALUES('h','s','local','o','cc','2026-09-23T02:00:00Z','observed','e','snap','opus')`)
	if err := catalog.refreshAllLibrary(context.Background()); err != nil {
		t.Fatal(err)
	}
	rewritten := 0
	for _, projection := range projectionTriggerCases() {
		for table := range projection.lookups {
			if countRows(t, catalog, "SELECT COUNT(*) FROM "+table) == 0 {
				continue
			}
			rewritten++
			assignments := []string{}
			for _, column := range projectionTableColumns(t, catalog.DB, table) {
				assignments = append(assignments, `"`+column+`"="`+column+`"`)
			}
			execSQL(t, catalog.DB, "UPDATE "+table+" SET "+strings.Join(assignments, ","))
			if marked := countRows(t, catalog, "SELECT COUNT(*) FROM "+projection.dirty); marked != 0 {
				t.Errorf("%s: rewriting %s unchanged marked %d workspaces", projection.name, table, marked)
			}
		}
	}
	if rewritten < 20 {
		t.Fatalf("the fixture rewrote only %d dependency tables", rewritten)
	}
	// Columns neither projection reads: when a row was observed or indexed.
	execSQL(t, catalog.DB, `UPDATE pull_requests SET observed_at='later'`, `UPDATE workspaces SET indexed_at='later',title='Renamed'`)
	if library, metrics := dirtyCount(t, catalog), countRows(t, catalog, "SELECT COUNT(*) FROM mcp_metric_dirty"); library != 0 || metrics != 0 {
		t.Fatalf("unread columns marked %d Search view and %d metric workspaces", library, metrics)
	}
	// One they read still marks, in a statement that also rewrites others.
	execSQL(t, catalog.DB, `UPDATE messages SET kind=kind,role='assistant' WHERE id='m1'`)
	if library, metrics := dirtyCount(t, catalog), countRows(t, catalog, "SELECT COUNT(*) FROM mcp_metric_dirty"); library != 1 || metrics != 1 {
		t.Fatalf("a changed message marked %d Search view and %d metric workspaces, want 1 and 1", library, metrics)
	}
}

func TestLibraryQueryTableServesArbitrarySortsAndFilters(t *testing.T) {
	catalog, config := libraryFixture(t)
	if err := catalog.refreshAllLibrary(context.Background()); err != nil {
		t.Fatal(err)
	}
	server := testServer(t, config, catalog)
	post := func(path, body string) map[string]any {
		t.Helper()
		request := queryFixtureHTTPRequest(http.MethodPost, path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer test-token")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, response.Code, response.Body.String())
		}
		var decoded map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
			t.Fatal(err)
		}
		return decoded
	}
	sorted := post("/api/query/library", `{"select":["title"],"orderBy":[{"field":"title","dir":"asc"},{"field":"id","dir":"asc"}],"limit":10,"offset":0}`)
	rows := sorted["rows"].([]any)
	if sorted["total"] != float64(2) || rows[0].(map[string]any)["title"] != "Bravo" || rows[1].(map[string]any)["title"] != "Charlie" {
		t.Fatalf("unexpected sorted page: %#v", sorted)
	}
	if row := rows[1].(map[string]any); firstString(row["first_input"]) != "Please fix the parser" || firstString(row["last_response"]) != "Fixed it" || row["first_input_id"] != "m1" || row["last_response_id"] != "m4" {
		t.Fatalf("page rows lack previews: %#v", rows[1])
	}
	filtered := post("/api/query/library", `{"select":["title"],"where":[{"field":"tool_error_count","op":">=","value":"1"}],"orderBy":[{"field":"changed_file_count","dir":"desc"}],"limit":10,"offset":0}`)
	if filtered["total"] != float64(1) || filtered["rows"].([]any)[0].(map[string]any)["id"] != "c" {
		t.Fatalf("unexpected filtered page: %#v", filtered)
	}
	pending := post("/api/query/library", `{"select":["title"],"where":[{"field":"subagent_count","op":"=","value":""}],"limit":10,"offset":0}`)
	if pending["total"] != float64(2) {
		t.Fatalf("pending subagent count filter hid the library: %#v", pending)
	}
	metrics := post("/api/query/library/metrics", `{"version":2,"where":[],"orderBy":[],"limit":1,"offset":0,"metrics":[{"id":"n","op":"count","groupBy":["repository_name"]}]}`)
	var want queryFixtureMetricsResult
	encoded, _ := json.Marshal(metrics)
	if err := json.Unmarshal(encoded, &want); err != nil || want.Metrics[0].Error != "" {
		t.Fatal(err, metrics)
	}
	total := 0.0
	for _, bucket := range want.Metrics[0].Buckets {
		total += bucket.Value.(float64)
	}
	if total != 2 {
		t.Fatalf("mirrored workspaces were counted twice: %#v", metrics)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/query/library/distinct?field=changed_files&q=", nil)
	request.Header.Set("Authorization", "Bearer test-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "src/parser.go") {
		t.Fatalf("distinct changed files: %d %s", response.Code, response.Body.String())
	}
	get := func(path string) (int, map[string]any) {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer test-token")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		var decoded map[string]any
		_ = json.Unmarshal(response.Body.Bytes(), &decoded)
		return response.Code, decoded
	}
	code, stats := get("/api/query/library/field-stats?fields=title,changed_file_count,activity_at")
	if code != http.StatusOK {
		t.Fatalf("field stats: %d %#v", code, stats)
	}
	title, _ := stats["title"].(map[string]any)
	changed, _ := stats["changed_file_count"].(map[string]any)
	activity, _ := stats["activity_at"].(map[string]any)
	if title["distinct"] != float64(2) || title["min"] != nil || changed["min"] == nil || activity["max"] == nil {
		t.Fatalf("field stats: %#v", stats)
	}
	if code, body := get("/api/query/library/field-stats?fields=title,secret"); code != http.StatusBadRequest {
		t.Fatalf("unknown stats field: %d %#v", code, body)
	}
}

func TestLibraryProjectionRebuildsWhenDefinitionChanges(t *testing.T) {
	catalog, _ := libraryFixture(t)
	if err := catalog.refreshAllLibrary(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.DB.Exec("UPDATE meta SET value='stale' WHERE key='workspace_library_version'"); err != nil {
		t.Fatal(err)
	}
	if err := catalog.ensureLibrary(); err != nil {
		t.Fatal(err)
	}
	if dirtyCount(t, catalog) != 3 {
		t.Fatalf("rebuild did not mark every workspace dirty: %d", dirtyCount(t, catalog))
	}
	var stored int
	if err := catalog.DB.QueryRow("SELECT COUNT(*) FROM workspace_library").Scan(&stored); err != nil || stored != 0 {
		t.Fatalf("rebuild kept stale rows: %d %v", stored, err)
	}
	if len(libraryRowsByID(t, catalog)) != 2 {
		t.Fatal("rows unavailable while the rebuilt projection is dirty")
	}
	var legacy int
	if err := catalog.DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name LIKE 'workspace_query_stats%'").Scan(&legacy); err != nil || legacy != 0 {
		t.Fatalf("legacy query stats remain: %d %v", legacy, err)
	}
}

// A build that defines one column differently, adds one and drops one keeps
// every stored row: only those columns change, a workspace at a time, and no
// workspace is marked.
func TestLibraryColumnChangeRecomputesOnlyThatColumn(t *testing.T) {
	catalog, _ := libraryFixture(t)
	ctx := context.Background()
	if err := catalog.refreshAllLibrary(ctx); err != nil {
		t.Fatal(err)
	}
	stored := func(column, workspace string) any {
		t.Helper()
		var value any
		if err := catalog.DB.QueryRow(`SELECT "`+column+`" FROM workspace_library WHERE workspace_id=?`, workspace).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	owed := func() int { return countRows(t, catalog, "SELECT COUNT(*) FROM workspace_library_column_pending") }
	// Nothing recomputes a column whose definition stands: this stays.
	execSQL(t, catalog.DB, "UPDATE workspace_library SET pr_numbers='kept'")
	turns := stored("turn_count", "c")
	columns := []libraryColumn{}
	for _, column := range libraryColumns {
		switch column.name {
		case "turn_count":
			column.expr = "100+" + column.expr
		case "harness_versions":
			continue
		}
		columns = append(columns, column)
	}
	override(t, &libraryColumns, append(columns, libraryColumn{"message_count",
		"(SELECT COUNT(*) FROM conversations nx JOIN messages nm ON nm.conversation_id=nx.id WHERE nx.workspace_id=w.id)"}))
	generation := countRows(t, catalog, "SELECT value FROM query_cache_generation WHERE id=1")
	if err := catalog.ensureLibrary(); err != nil {
		t.Fatal(err)
	}
	if rows, marked := countRows(t, catalog, "SELECT COUNT(*) FROM workspace_library"), dirtyCount(t, catalog); rows != 3 || marked != 0 || owed() != 6 {
		t.Fatalf("after the change: %d stored rows, %d marked, %d column values owed; want 3, 0 and 6", rows, marked, owed())
	}
	// Until its turn, a workspace reads the earlier value; a new column is
	// unknown, not zero.
	row, err := catalog.libraryRows(ctx, "w.id='c'", nil, nil)
	if err != nil || len(row) != 1 || row[0]["turn_count"] != turns || row[0]["message_count"] != nil {
		t.Fatalf("before the backfill: %v turns (want %v), %v messages (want unknown): %v", row[0]["turn_count"], turns, row[0]["message_count"], err)
	}
	if pending, err := catalog.libraryPending(100); err != nil || pending != 3 {
		t.Fatalf("%d workspaces reported as updating, want 3 (%v)", pending, err)
	}
	if countRows(t, catalog, "SELECT COUNT(*) FROM pragma_table_info('workspace_library') WHERE name='harness_versions'") != 0 {
		t.Fatal("the removed column is still stored")
	}
	// One pass, then a restart: the next carries on from what is left.
	pass, err := catalog.refreshLibraryColumns(ctx, 2)
	if err != nil || pass.workspaces != 2 || owed() != 4 {
		t.Fatalf("first pass: %+v, %d owed (%v)", pass, owed(), err)
	}
	if err := catalog.ensureLibrary(); err != nil || owed() != 4 || dirtyCount(t, catalog) != 0 {
		t.Fatalf("after a restart: %d owed, %d marked (%v)", owed(), dirtyCount(t, catalog), err)
	}
	// A workspace written to meanwhile is its refresh's to compute.
	execSQL(t, catalog.DB, `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,created_at,content_hash)
		VALUES('m6','cb','6','user','message','More','2026-09-23T04:00:00Z','h')`)
	for {
		pass, err := catalog.refreshLibraryColumns(ctx, 2)
		if err != nil {
			t.Fatal(err)
		}
		if pass.workspaces == 0 {
			break
		}
	}
	if its := countRows(t, catalog, "SELECT COUNT(*) FROM workspace_library_column_pending WHERE workspace_id='b'"); its == 0 || owed() != its || dirtyCount(t, catalog) != 1 {
		t.Fatalf("the written workspace: %d values owed, %d of them its own, %d marked", owed(), its, dirtyCount(t, catalog))
	}
	if err := catalog.refreshAllLibrary(ctx); err != nil {
		t.Fatal(err)
	}
	if owed() != 0 || dirtyCount(t, catalog) != 0 {
		t.Fatalf("at the end: %d values owed, %d marked", owed(), dirtyCount(t, catalog))
	}
	want := map[string]map[string]int64{"a": {"turn_count": 101, "message_count": 1}, "b": {"turn_count": 101, "message_count": 1}, "c": {"turn_count": 101, "message_count": 7}}
	for workspace, values := range want {
		for column, value := range values {
			if got := stored(column, workspace); got != value {
				t.Errorf("%s.%s = %v, want %d", workspace, column, got, value)
			}
		}
		// b was refreshed whole, which computes every column.
		if kept := stored("pr_numbers", workspace); (kept == "kept") != (workspace != "b") {
			t.Errorf("%s: pr_numbers %v: an unchanged column is computed again only by a refresh", workspace, kept)
		}
	}
	// A cached request must not go on answering with the earlier values.
	if after := countRows(t, catalog, "SELECT value FROM query_cache_generation WHERE id=1"); after == generation {
		t.Fatal("stored values changed without the cache generation moving")
	}
	// A pass with nothing owed does not take the catalog writer.
	writes := catalog.writers.next
	if pass, err := catalog.refreshLibraryColumns(ctx, 2); err != nil || pass.workspaces != 0 || catalog.writers.next != writes {
		t.Fatalf("idle pass: %+v, took the writer %v (%v)", pass, catalog.writers.next != writes, err)
	}
}

// A catalog from the last build that versioned the view as a whole keeps its
// rows and its marks: only columns defined differently since are owed.
func TestLibraryUpgradeFromWholeVersionMarksNothing(t *testing.T) {
	for _, changed := range []bool{false, true} {
		catalog, _ := libraryFixture(t)
		if err := catalog.refreshAllLibrary(context.Background()); err != nil {
			t.Fatal(err)
		}
		// As that build left it: one version, its triggers, no column records.
		execSQL(t, catalog.DB, setMetaSQL("workspace_library_version", libraryLegacyVersion), "DELETE FROM workspace_library_columns",
			setMetaSQL(libraryTriggersKey, libraryLegacyVersion+":earlier"))
		want := []string{}
		if changed {
			columns := slices.Clone(libraryColumns)
			columns[0].expr = "1+" + columns[0].expr
			override(t, &libraryColumns, columns)
		}
		for _, column := range libraryColumns {
			if libraryLegacyDigests[column.name] != column.digest() {
				want = append(want, column.name)
			}
		}
		if changed && len(want) == 0 {
			t.Fatal("the changed column is not seen as changed")
		}
		if err := catalog.ensureLibrary(); err != nil {
			t.Fatal(err)
		}
		rows, err := queryMaps(catalog.DB, "SELECT column_name,COUNT(*) n FROM workspace_library_column_pending GROUP BY 1 ORDER BY 1")
		if err != nil {
			t.Fatal(err)
		}
		owed := []string{}
		for _, row := range rows {
			if row["n"] != int64(3) {
				t.Errorf("%v is owed for %v workspaces, want 3", row["column_name"], row["n"])
			}
			owed = append(owed, firstString(row["column_name"]))
		}
		slices.Sort(want)
		if !slices.Equal(owed, want) {
			t.Errorf("columns owed %v, want those defined differently since: %v", owed, want)
		}
		if stored, marked := countRows(t, catalog, "SELECT COUNT(*) FROM workspace_library"), dirtyCount(t, catalog); stored != 3 || marked != 0 {
			t.Fatalf("upgrade kept %d of 3 rows and marked %d workspaces", stored, marked)
		}
		var version string
		if err := catalog.DB.QueryRow("SELECT value FROM meta WHERE key='workspace_library_version'").Scan(&version); err != nil || version != libraryVersion() {
			t.Fatalf("version after the upgrade: %q (%v)", version, err)
		}
		// Its triggers are this build's, and still mark.
		execSQL(t, catalog.DB, "UPDATE messages SET role='assistant' WHERE id='m1'")
		if dirtyCount(t, catalog) != 1 {
			t.Fatalf("a changed message marked %d workspaces after the upgrade", dirtyCount(t, catalog))
		}
	}
}

// Every request shape must answer exactly as it would from full rows, whether
// the projection is stored or computed live.
func TestLibraryRequestsReadingFewerFieldsMatchFullRows(t *testing.T) {
	for _, refreshed := range []bool{false, true} {
		catalog, config := libraryFixture(t)
		if _, err := catalog.DB.Exec(`UPDATE workspaces SET purpose='Rewrite the parser',outcome='Parser merged' WHERE id='c'`); err != nil {
			t.Fatal(err)
		}
		// A priced workspace, since costs are attached only where needed.
		doc := testPricing()
		doc.Changes = append(doc.Changes, priceChange{Provider: "openai", Model: "gpt-6-sol", EffectiveFrom: "2026-01-01", Input: rate(10), CacheRead: rate(1), Output: rate(100), Status: "confirmed", SourceURL: "https://example.com/g"})
		if err := catalog.replacePricing(doc, "test"); err != nil {
			t.Fatal(err)
		}
		ingestUsageFixture(t, catalog)
		if _, err := catalog.DB.Exec(`UPDATE agent_sessions SET model='gpt-6-sol'`); err != nil {
			t.Fatal(err)
		}
		if refreshed {
			if err := catalog.refreshAllLibrary(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
		server := testServer(t, config, catalog)
		schema, err := server.queryTableSchema("library")
		if err != nil {
			t.Fatal(err)
		}
		call := func(method, path, body string) any {
			t.Helper()
			request := queryFixtureHTTPRequest(method, path, strings.NewReader(body))
			request.Header.Set("Authorization", "Bearer test-token")
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("%s %s: %d %s", method, path, response.Code, response.Body.String())
			}
			return decodeJSON(t, response.Body.Bytes())
		}
		fullRows := func(search string) []map[string]any {
			t.Helper()
			rows, err := catalog.computeSearchRows(SearchOptions{Query: search}, nil)
			if err != nil {
				t.Fatal(err)
			}
			return rows
		}
		for _, body := range []string{
			`{"select":["title","repository_name","source_kind","activity_at","token_count","cost_usd","tool_use_count","tool_error_count","pr_count","pr_numbers","main_merge_title","branch_count","file_edit_count","outcome"],"orderBy":[{"field":"activity_at","dir":"desc","nulls":"last"},{"field":"id","dir":"asc"}],"limit":50,"offset":0}`,
			`{"select":["title"],"where":[{"field":"purpose","op":"contains","value":"summarized"}],"limit":10,"offset":0}`,
			`{"select":["title"],"where":[{"field":"outcome","op":"is_not_null","value":""}],"orderBy":[{"field":"cost_usd","dir":"desc"}],"limit":10,"offset":0}`,
			`{"select":["title"],"where":[{"any":[{"field":"changed_files","op":"contains","value":"parser"},{"field":"title","op":"=","value":"Bravo"}]}],"orderBy":[{"field":"purpose","dir":"asc"}],"limit":10,"offset":0}`,
			`{"select":["cost_usd"],"where":[{"field":"price_status","op":"=","value":"priced"}],"orderBy":[{"field":"cost_usd","dir":"asc"}],"limit":5,"offset":0}`,
			`{"select":["pr_numbers"],"orderBy":[{"field":"pr_numbers","dir":"desc","extract":{"regex":"(\\d+)"}}],"limit":1,"offset":1}`,
		} {
			var query querytable.Query
			if err := json.Unmarshal([]byte(body), &query); err != nil {
				t.Fatal(err)
			}
			for _, search := range []string{"", "parser"} {
				want, err := querytable.Apply(fullRows(search), query, schema)
				if err != nil {
					t.Fatal(err)
				}
				if err := catalog.attachLibraryPreviews(context.Background(), want.Rows); err != nil {
					t.Fatal(err)
				}
				got := call(http.MethodPost, "/api/query/library?search="+search, body)
				if expected := decodeJSON(t, mustJSON(t, want)); !reflect.DeepEqual(got, expected) {
					t.Fatalf("refreshed=%v search=%q %s:\ngot  %v\nwant %v", refreshed, search, body, got, expected)
				}
			}
		}
		for _, field := range []string{"changed_files", "purpose", "outcome", "source_kind", "repository_name", "pr_numbers", "price_status"} {
			want, err := querytable.Distinct(fullRows(""), field, "", 50, schema)
			if err != nil {
				t.Fatal(err)
			}
			if got := call(http.MethodGet, "/api/query/library/distinct?field="+field, ""); !reflect.DeepEqual(got, decodeJSON(t, mustJSON(t, want))) {
				t.Fatalf("refreshed=%v distinct %s: got %v, want %v", refreshed, field, got, want)
			}
		}
		metrics := metricRequest(qt.AggSpec{ID: "n", Op: "count", GroupBy: []string{"repository_name"}}, qt.AggSpec{ID: "tokens", Op: "sum", Field: "token_count", GroupBy: []string{"source_kind"}},
			qt.AggSpec{ID: "outcomes", Op: "count_distinct", Field: "outcome"}, qt.AggSpec{ID: "cost", Op: "max", Field: "cost_usd"})
		metrics.Where = []qt.WhereTerm{{Field: "purpose", Op: "is_not_null"}}
		ctx := context.Background()
		tx, release, err := beginQueryMemory(ctx, catalog.reader())
		if err != nil {
			t.Fatal(err)
		}
		full := queryFixtureFor("library", PharosLibrarySchema())
		if full.SourceSQL, err = stageQueryRows(ctx, tx, "full_library_rows", fullRows(""), full.Schema); err != nil {
			t.Fatal(err)
		}
		want, err := full.executeFixtureIn(ctx, tx, nil, &metrics, qt.PlanOptions{})
		release()
		if err != nil {
			t.Fatal(err)
		}
		for _, metric := range want.Metrics.Metrics {
			if metric.Error != "" || len(metric.Buckets) == 0 {
				t.Fatalf("metric %s over full rows: %+v", metric.ID, metric)
			}
		}
		got := call(http.MethodPost, "/api/query/library/metrics", string(mustJSON(t, metrics)))
		var actual queryFixtureMetricsResult
		if err := json.Unmarshal(mustJSON(t, got), &actual); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(decodeJSON(t, mustJSON(t, actual)), decodeJSON(t, mustJSON(t, want.Metrics))) {
			t.Fatalf("refreshed=%v metrics: got %v, want %v", refreshed, got, want.Metrics)
		}
	}
}

// A default Library page must not read long text for rows it doesn't show:
// the cached row set carries none, and only the page is completed.
func TestLibraryDefaultPageReadsNoLongTextForOtherRows(t *testing.T) {
	catalog, config := libraryFixture(t)
	if err := catalog.refreshAllLibrary(context.Background()); err != nil {
		t.Fatal(err)
	}
	server := testServer(t, config, catalog)
	request := queryFixtureHTTPRequest(http.MethodPost, "/api/query/library", strings.NewReader(
		`{"select":["title","outcome","pr_numbers"],"orderBy":[{"field":"activity_at","dir":"desc","nulls":"last"},{"field":"id","dir":"asc"}],"limit":1,"offset":0}`))
	request.Header.Set("Authorization", "Bearer test-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("%d %s", response.Code, response.Body.String())
	}
	page := decodeJSON(t, response.Body.Bytes()).(map[string]any)["rows"].([]any)[0].(map[string]any)
	for _, field := range []string{"purpose", "outcome", "pr_details", "canonical_remote", "first_input", "cost_usd"} {
		if _, ok := page[field]; !ok {
			t.Errorf("page row lacks %s, which the UI renders: %v", field, page)
		}
	}
	if catalog.library.fields == nil {
		t.Fatal("the cache holds every column")
	}
	for _, row := range catalog.library.rows {
		for _, field := range append(append([]string{}, libraryLargeFields...), libraryCostFields...) {
			if _, ok := row[field]; ok {
				t.Fatalf("cached row carries %s: %v", field, row)
			}
		}
	}
	for _, value := range []func(libraryColumn) string{
		func(column libraryColumn) string { return "l." + column.name },
		func(column libraryColumn) string { return "(" + column.expr + ")" },
	} {
		statement := libraryRowSelect(value, catalog.library.fields)
		for _, text := range []string{"w.*", "purpose", "w.outcome", "summary_", "changed_files", "pr_details"} {
			if strings.Contains(statement, text) {
				t.Fatalf("default request reads %s for every row:\n%s", text, statement)
			}
		}
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func decodeJSON(t *testing.T, data []byte) any {
	t.Helper()
	var decoded any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

func TestLibraryPreviewsFollowTheirMessages(t *testing.T) {
	catalog, _ := libraryFixture(t)
	ctx := context.Background()
	var wantElapsed any = int64(3000)
	preview := func() (any, any) {
		t.Helper()
		page := []map[string]any{{"id": "c"}}
		if err := catalog.attachLibraryPreviews(ctx, page); err != nil {
			t.Fatal(err)
		}
		if got := page[0]["preview_elapsed_ms"]; !reflect.DeepEqual(got, wantElapsed) {
			t.Fatalf("preview elapsed time %v, want %v", got, wantElapsed)
		}
		return page[0]["first_input"], page[0]["last_response"]
	}
	stored := func() int {
		t.Helper()
		var count int
		if err := catalog.DB.QueryRow("SELECT COUNT(*) FROM workspace_previews WHERE workspace_id='c'").Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	execTx(t, catalog.DB,
		`INSERT INTO conversations(id,workspace_id,provider,account,native_id,parent_id,agent_depth) VALUES('cc-helper','c','claude','local','helper','cc',1)`,
		`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,created_at,content_hash) VALUES('helper-prompt','cc-helper','p','user','message','Investigate this delegated task','2026-09-23T01:57:00Z','h')`,
		`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,created_at,content_hash) VALUES('m0','cc','0','user','message','<system_instruction>Injected setup</system_instruction>','2026-09-23T01:59:00Z','h')`,
		`UPDATE messages SET text='<system_instruction>Injected setup</system_instruction>

Please fix the parser' WHERE id='m1'`)
	if err := catalog.refreshAllLibrary(ctx); err != nil {
		t.Fatal(err)
	}
	if first, last := preview(); first != "Please fix the parser" || last != "Fixed it" || stored() != 1 {
		t.Fatalf("preview %q/%q, stored %d", first, last, stored())
	}
	// A retained copied message must not displace the first human writing,
	// including when an older preview ID has already been cached.
	execTx(t, catalog.DB,
		`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,created_at,content_hash) VALUES('copied','cc','copy','user','message','Copied agent output','2026-09-23T01:58:00Z','h')`,
		`INSERT INTO message_authorship(message_id,conversation_id,workspace_id,sent_at,day,total_chars,spans_json) VALUES('copied','cc','c','2026-09-23T01:58:00Z','2026-09-23',19,'[{"start":0,"end":19,"category":"quoted"}]')`)
	if first, _ := preview(); first != "Please fix the parser" {
		t.Fatalf("copied text used as first ask: %q", first)
	}
	// A new response marks the workspace dirty, which drops the stored
	// preview until the refresh; meanwhile the page finds it itself.
	if _, err := catalog.DB.Exec(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,created_at,content_hash)
		VALUES('m9','cc','9','assistant','message','Shipped it','2026-09-23T03:00:00Z','h')`); err != nil {
		t.Fatal(err)
	}
	wantElapsed = int64(3600000)
	if _, last := preview(); last != "Shipped it" || stored() != 0 {
		t.Fatalf("latest response %q, stored %d", last, stored())
	}
	// A build without previews refreshes the projection only: no stale row.
	if _, err := catalog.DB.Exec("DELETE FROM workspace_library_dirty"); err != nil {
		t.Fatal(err)
	}
	if _, last := preview(); last != "Shipped it" || stored() != 0 {
		t.Fatalf("after an older build's refresh: %q, stored %d", last, stored())
	}
	// Large workspaces are filled in; small ones are quick to preview without,
	// and leave the queue that refresh put them on.
	if filled, err := catalog.refreshPreviews(ctx, 25); err != nil || filled != 0 {
		t.Fatalf("filled %d small workspaces (%v)", filled, err)
	}
	if queued := countRows(t, catalog, "SELECT COUNT(*) FROM workspace_previews_pending"); queued != 0 {
		t.Fatalf("%d small workspaces stay queued", queued)
	}
	// With none queued, a pass does not take the catalog writer.
	writes := catalog.writers.next
	if filled, err := catalog.refreshPreviews(ctx, 25); err != nil || filled != 0 || catalog.writers.next != writes {
		t.Fatalf("idle pass: filled %d, took the writer %v (%v)", filled, catalog.writers.next != writes, err)
	}
	// The older build refreshes it again once it has grown.
	execSQL(t, catalog.DB, "UPDATE workspace_library SET turn_count=1000 WHERE workspace_id='c'",
		"INSERT INTO workspace_library_dirty(workspace_id) VALUES('c')", "DELETE FROM workspace_library_dirty")
	if filled, err := catalog.refreshPreviews(ctx, 25); err != nil || filled != 1 || stored() != 1 {
		t.Fatalf("filled %d, stored %d (%v)", filled, stored(), err)
	}
	if _, last := preview(); last != "Shipped it" {
		t.Fatalf("filled preview %q", last)
	}
	// A catalog from before the queue has its large workspaces without
	// previews queued once, when the queue is created.
	execSQL(t, catalog.DB, "DROP TABLE workspace_previews_pending", "DELETE FROM workspace_previews")
	if err := catalog.ensureLibrary(); err != nil {
		t.Fatal(err)
	}
	if filled, err := catalog.refreshPreviews(ctx, 25); err != nil || filled != 1 || stored() != 1 || dirtyCount(t, catalog) != 0 {
		t.Fatalf("upgrade: filled %d, stored %d, %d marked (%v)", filled, stored(), dirtyCount(t, catalog), err)
	}
	for _, stamp := range []struct {
		at   string
		want any
	}{{"", nil}, {"invalid", nil}, {"2026-09-23T04:00:00Z", nil}, {"2026-09-23T03:00:00Z", int64(0)}} {
		if _, err := catalog.DB.Exec("UPDATE messages SET created_at=? WHERE id='m1'", stamp.at); err != nil {
			t.Fatal(err)
		}
		wantElapsed = stamp.want
		preview()
	}
}

func TestLibraryRowsReadTheWorkspaceIndex(t *testing.T) {
	catalog, _ := libraryFixture(t)
	stored := libraryRowSelect(func(column libraryColumn) string { return "l." + column.name }, libraryCompactFields) +
		" JOIN workspace_library l ON l.workspace_id=w.id WHERE w.id NOT IN (SELECT workspace_id FROM workspace_library_dirty)"
	plan, err := queryMaps(catalog.DB, "EXPLAIN QUERY PLAN "+stored)
	if err != nil {
		t.Fatal(err)
	}
	if text := jsonText(plan); !strings.Contains(text, "COVERING INDEX workspaces_library_idx") {
		t.Fatalf("Library rows read the workspace table, so workspaces_library_idx lacks a field they use: %s", text)
	}
}

func TestLibraryRefreshReadsMetricsThroughTheirWorkspace(t *testing.T) {
	catalog, _ := libraryFixture(t)
	plan, err := queryMaps(catalog.DB, "EXPLAIN QUERY PLAN "+libraryDerivedSelect("w.id=?"), "a")
	if err != nil {
		t.Fatal(err)
	}
	if text := jsonText(plan); strings.Contains(text, "metrics_name_value_idx") {
		t.Fatalf("a Library refresh walks every workspace's metric of a name for each workspace: %s", text)
	}
}
