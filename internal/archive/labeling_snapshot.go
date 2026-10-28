package archive

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// labelingMinSchema is the oldest catalog labeling reads: schema 7 brought
// the tool ledger (tool_calls, tool_commands), which headless-run launches
// and question-tool answers are read from, and messages.sender. Anything
// newer that a catalog lacks is filled with empty values and stated in the
// manifest's coverage, never silently counted as zero.
const labelingMinSchema = 7

// labelingColumn is a column preparation or the shared queries it calls
// read, with the value to use when an older catalog lacks it.
type labelingColumn struct{ name, fallback string }

// labelingTables lists every table and column preparation reads. Required
// columns have no fallback.
var labelingTables = []struct {
	table   string
	columns []labelingColumn
	// note explains what a catalog without this table cannot provide.
	note string
}{
	{"meta", []labelingColumn{{"key", ""}, {"value", ""}}, ""},
	{"workspaces", []labelingColumn{{"id", ""}, {"source_kind", ""}, {"title", ""}, {"activity_at", "NULL"}, {"location", "NULL"}, {"repository_id", "NULL"}}, ""},
	{"repositories", []labelingColumn{{"id", "NULL"}, {"display_name", "NULL"}}, "repository names are unavailable"},
	{"conversations", []labelingColumn{{"id", ""}, {"workspace_id", ""}, {"provider", ""}, {"parent_id", "NULL"}, {"agent_depth", "0"},
		{"started_at", "NULL"}, {"model", "NULL"}, {"effort", "NULL"}, {"harness", "NULL"}, {"origin_host_id", "NULL"}}, ""},
	{"messages", []labelingColumn{{"id", ""}, {"conversation_id", ""}, {"role", ""}, {"kind", ""}, {"text", ""}, {"created_at", "NULL"},
		{"content_hash", "''"}, {"selected", "1"}, {"sender", "NULL"}, {"reasoning_format", "NULL"}, {"call_id", "NULL"}, {"source_order", "NULL"}}, ""},
	{"conversation_identity_links", []labelingColumn{{"left_id", "NULL"}, {"right_id", "NULL"}}, "no identity links: mirrors are not recognized and are counted as separate work"},
	{"conversation_launches", []labelingColumn{{"child_id", "NULL"}, {"parent_id", "NULL"}}, ""},
	{"agent_sessions", []labelingColumn{{"id", "NULL"}, {"workspace_id", "NULL"}, {"conversation_id", "NULL"}, {"native_id", "NULL"}, {"kind", "NULL"},
		{"parent_id", "NULL"}, {"model", "NULL"}, {"effort", "NULL"}, {"delegation_message_id", "NULL"}, {"child_conversation_id", "NULL"}},
		"no agent sessions: delegation depth falls back to stored depth and delegation duplicates are not found"},
	{"agent_session_messages", []labelingColumn{{"agent_session_id", "NULL"}, {"message_id", "NULL"}}, "no embedded agent session ownership"},
	{"tool_calls", []labelingColumn{{"id", "NULL"}, {"call_id", "NULL"}, {"conversation_id", "NULL"}, {"call_message_id", "NULL"}, {"result_message_id", "NULL"},
		{"tool_name", "NULL"}, {"tool_category", "NULL"}, {"command", "NULL"}, {"backgrounded", "0"}, {"started_at", "NULL"}, {"ended_at", "NULL"}},
		"no tool ledger: headless-run launches and question-tool answers are not read"},
	{"tool_commands", []labelingColumn{{"tool_call_id", "NULL"}, {"program", "NULL"}, {"subcommand", "NULL"}, {"command", "NULL"}},
		"no parsed shell commands: headless runs are not linked to the commands that launched them"},
	{"reasoning_extraction_state", []labelingColumn{{"conversation_id", "NULL"}, {"version", "NULL"}, {"unavailable_count", "0"}}, ""},
}

// labelingColumnNotes explain what a missing column costs.
var labelingColumnNotes = map[string]string{
	"messages.sender":                      "messages.sender is absent: senders recorded by newer ingestion (automation, other agents) are unknown, so authorship can count such prompts as typed",
	"messages.selected":                    "messages.selected is absent: every message is treated as on the selected branch",
	"messages.reasoning_format":            "messages.reasoning_format is absent: reasoning summaries are not told from full text",
	"messages.source_order":                "messages.source_order is absent: transcript order falls back to send time",
	"conversations.harness":                "conversations.harness is absent: headless claude/agy runs are not recognized from their harness",
	"conversations.origin_host_id":         "conversations.origin_host_id is absent: Claude SDK sessions Conductor did not start are not recognized",
	"agent_sessions.child_conversation_id": "agent_sessions.child_conversation_id is absent: delegations are not linked to child conversations",
	"agent_sessions.delegation_message_id": "agent_sessions.delegation_message_id is absent: delegations are not linked to their children",
}

// labelingCompatibility checks a catalog has what preparation needs and,
// for an older one, shadows each table that lacks optional columns (or is
// absent) with a TEMP view that supplies them, so the shared queries run
// unchanged. Views live only in the connection's temporary schema; the
// catalog is opened read-only. It returns the coverage statements.
func labelingCompatibility(ctx context.Context, tx *sql.Tx) ([]string, map[string]bool, error) {
	present := map[string]bool{}
	coverage := []string{}
	views := []string{}
	for _, table := range labelingTables {
		rows, err := queryMapsContext(ctx, tx, "SELECT name FROM pragma_table_info(?)", table.table)
		if err != nil {
			return nil, nil, err
		}
		columns := map[string]bool{}
		for _, row := range rows {
			columns[firstString(row["name"])] = true
		}
		present[table.table] = len(columns) > 0
		selects, missing := []string{}, []string{}
		for _, column := range table.columns {
			if columns[column.name] {
				continue
			}
			if column.fallback == "" {
				return nil, nil, fmt.Errorf("catalog lacks %s.%s, which labeling needs", table.table, column.name)
			}
			selects = append(selects, column.fallback+" AS "+column.name)
			missing = append(missing, column.name)
			if note := labelingColumnNotes[table.table+"."+column.name]; note != "" && len(columns) > 0 {
				coverage = append(coverage, note)
			}
		}
		switch {
		case len(columns) == 0:
			views = append(views, fmt.Sprintf("CREATE TEMP VIEW %s AS SELECT %s WHERE 0", table.table, strings.Join(selects, ",")))
			if table.note != "" {
				coverage = append(coverage, table.table+" is absent: "+table.note)
			}
		case len(missing) > 0:
			views = append(views, fmt.Sprintf("CREATE TEMP VIEW %s AS SELECT *,%s FROM main.%s", table.table, strings.Join(selects, ","), table.table))
			present[table.table+":partial"] = true
		}
	}
	if len(views) > 0 {
		// query_only would refuse even TEMP views; mode=ro still refuses
		// every write to the catalog itself.
		if _, err := tx.ExecContext(ctx, "PRAGMA query_only=0"); err != nil {
			return nil, nil, err
		}
		for _, view := range views {
			if _, err := tx.ExecContext(ctx, view); err != nil {
				return nil, nil, fmt.Errorf("compatibility view: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx, "PRAGMA query_only=1"); err != nil {
			return nil, nil, err
		}
	}
	sort.Strings(coverage)
	return coverage, present, nil
}

// openLabelingSnapshot opens the catalog read-only on one connection and
// begins the one read transaction every preparation read runs in, so they
// all see one snapshot. It never initializes or migrates the catalog.
//
// A live catalog is opened mode=ro with query_only, including its WAL. Its
// read transaction keeps the service from checkpointing past it for as long
// as preparation runs, so preparing from a copy is better. A static copy
// (copy is set: a backup nothing writes) is opened immutable, which reads no
// WAL and touches no -shm; it is refused while a non-empty WAL beside it
// means the copy is not complete in its main file.
func openLabelingSnapshot(ctx context.Context, path string, copy, allowStaleShm bool) (*sql.DB, *sql.Tx, map[string]any, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, nil, nil, err
	}
	// A library's catalog folder holds Pharos's lock files; a backup's does not.
	for _, lock := range []string{".pharos-service.lock", ".pharos-use.lock"} {
		if _, err := os.Stat(filepath.Join(filepath.Dir(path), lock)); err == nil {
			return nil, nil, nil, fmt.Errorf("%s sits beside %s, so it is a library's live catalog; prepare from a backup copy", path, lock)
		}
	}
	access := map[string]any{"mode": "live", "uri": "mode=ro, query_only, one read transaction on one connection"}
	query := "?mode=ro&_pragma=busy_timeout(10000)&_pragma=query_only(1)"
	if copy {
		if wal, err := os.Stat(path + "-wal"); err == nil && wal.Size() > 0 {
			return nil, nil, nil, fmt.Errorf("%s has a %d-byte write-ahead log, so it is not a complete static copy; use a checkpointed backup", path, wal.Size())
		}
		// An open WAL database always has a -shm; a copy's is stale at best.
		if shm, err := os.Stat(path + "-shm"); err == nil && shm.Size() > 0 && !allowStaleShm {
			return nil, nil, nil, fmt.Errorf("%s has a %d-byte -shm, so something may have it open; if you know nothing does (a stale file from the backup), pass --allow-stale-shm", path, shm.Size())
		}
		query = "?mode=ro&immutable=1&_pragma=query_only(1)"
		access = map[string]any{"mode": "snapshot_copy", "uri": "mode=ro, immutable=1, query_only, one connection", "path": path, "allow_stale_shm": allowStaleShm,
			"bytes": info.Size(), "modified_at": formatTime(info.ModTime().Truncate(time.Millisecond))}
	}
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path}).String()+query)
	if err != nil {
		return nil, nil, nil, err
	}
	db.SetMaxOpenConns(1)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		db.Close()
		return nil, nil, nil, err
	}
	return db, tx, access, nil
}
