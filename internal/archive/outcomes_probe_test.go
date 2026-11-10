//go:build probe

// Derives session outcomes from a real library's sessions, to time the pass
// and see how its labels fall. It reads an extract of only what outcomes read
// (the sessions, their edits, test, build and Git calls, and those calls'
// messages), made with the sqlite3 shell from a catalog backup opened
// immutable and read-only, so the probe never touches the backup itself:
//
//	sqlite3 /tmp/outcomes.db "ATTACH 'file:BACKUP?immutable=1&mode=ro' AS src;
//	  CREATE TABLE workspaces AS SELECT id,source_kind,source_account,source_id,title,activity_at,indexed_at FROM src.workspaces;
//	  CREATE TABLE conversations AS SELECT id,workspace_id,provider,account,native_id FROM src.conversations;
//	  CREATE TABLE tool_calls AS SELECT id,workspace_id,conversation_id,sequence,provider,kind,tool_name,tool_category,status,started_at,
//	    path_absolute,repo_path,path_repository_id,path_scope,lines_added,lines_removed,command,command_category,call_message_id,result_message_id
//	    FROM src.tool_calls WHERE tool_category='edit' OR command_category IN ('test','build') OR command LIKE '%git%' OR command LIKE '%gh pr%';
//	  CREATE TABLE tool_mirror_workspaces AS SELECT workspace_id FROM src.tool_mirror_workspaces;
//	  CREATE TEMP TABLE wanted AS SELECT DISTINCT r FROM (SELECT m.rowid r FROM tool_calls t JOIN src.messages m ON m.id=t.call_message_id
//	    WHERE t.tool_category='edit' AND t.status='ok' UNION SELECT m.rowid FROM tool_calls t JOIN src.messages m ON m.id=t.result_message_id
//	    WHERE t.command LIKE '%git%' OR t.command LIKE '%gh pr%') ORDER BY r;
//	  CREATE TABLE messages AS SELECT m.id,m.conversation_id,m.native_id,m.role,m.kind,m.text,m.created_at,m.content_hash
//	    FROM wanted w CROSS JOIN src.messages m ON m.rowid=w.r;"
//	PHAROS_OUTCOMES_SOURCE=/tmp/outcomes.db PHAROS_TEST_PROCESSES=1 \
//	  go test -tags probe -run TestOutcomesProbe -v -timeout 60m ./internal/archive

package archive

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestOutcomesProbe(t *testing.T) {
	source := os.Getenv("PHAROS_OUTCOMES_SOURCE")
	if source == "" {
		t.Skip("set PHAROS_OUTCOMES_SOURCE to an extract of a catalog backup")
	}
	catalog, _ := testCatalog(t)
	ctx := context.Background()
	conn, err := catalog.DB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	exec := func(statement string, args ...any) {
		t.Helper()
		if _, err := conn.ExecContext(ctx, statement, args...); err != nil {
			t.Fatal(statement, err)
		}
	}
	// Outcomes read messages and calls but need none of the derived tables
	// their triggers keep.
	triggers, err := queryMapsContext(ctx, conn, `SELECT name FROM sqlite_master WHERE type='trigger' AND (tbl_name='messages' OR tbl_name='tool_calls' AND name NOT LIKE 'outcomes_%')`)
	if err != nil {
		t.Fatal(err)
	}
	for _, trigger := range triggers {
		exec(`DROP TRIGGER "` + firstString(trigger["name"]) + `"`)
	}
	exec(`ATTACH DATABASE ? AS extract`, source)
	for _, table := range []struct{ name, columns string }{
		{"workspaces", "id,source_kind,source_account,source_id,title,activity_at,indexed_at"},
		{"conversations", "id,workspace_id,provider,account,native_id"},
		{"messages", "id,conversation_id,native_id,role,kind,text,created_at,content_hash"},
		{"tool_calls", "id,workspace_id,conversation_id,sequence,provider,kind,tool_name,tool_category,status,started_at,path_absolute,repo_path,path_repository_id,path_scope,lines_added,lines_removed,command,command_category,call_message_id,result_message_id"},
		{"tool_mirror_workspaces", "workspace_id"},
	} {
		started := time.Now()
		exec("INSERT OR IGNORE INTO " + table.name + "(" + table.columns + ") SELECT " + table.columns + " FROM extract." + table.name)
		t.Logf("copied %s in %s", table.name, time.Since(started).Round(time.Second))
	}
	exec(`DETACH DATABASE extract`)
	conn.Close()

	started := time.Now()
	if err := catalog.refreshSessionOutcomes(ctx); err != nil {
		t.Fatal(err)
	}
	t.Logf("derived every session in %s", time.Since(started).Round(time.Millisecond))
	report := func(query string) {
		t.Helper()
		rows, err := queryMaps(catalog.DB, query)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			t.Logf("%v", row)
		}
	}
	report(`SELECT ` + outcomeLabelSQL("w") + ` label,COUNT(*) sessions FROM workspaces w WHERE w.id NOT IN (SELECT workspace_id FROM tool_mirror_workspaces) GROUP BY 1 ORDER BY 2 DESC`)
	report(`SELECT COUNT(*) edited,SUM(kept_lines IS NULL) unread,SUM(heavily_reworked) reworked,SUM(rework_removed_lines>0) any_removed,
		ROUND(1.0*SUM(heavily_reworked)/COUNT(*),3) reworked_share FROM session_outcomes WHERE files_changed>0
		AND workspace_id NOT IN (SELECT workspace_id FROM tool_mirror_workspaces)`)
	report(`SELECT SUM(LENGTH(kept_lines)+LENGTH(removed_lines)) fingerprint_bytes,COUNT(*) session_files FROM session_files`)

	// One new session's worth of work: the most recent sessions marked again.
	execTx(t, catalog.DB, `INSERT INTO session_outcome_dirty SELECT id FROM workspaces ORDER BY activity_at DESC LIMIT 10`)
	started = time.Now()
	if err := catalog.refreshSessionOutcomes(ctx); err != nil {
		t.Fatal(err)
	}
	t.Logf("derived the 10 latest sessions again in %s", time.Since(started).Round(time.Millisecond))
}
