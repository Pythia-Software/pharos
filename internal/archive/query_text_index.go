package archive

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	qt "github.com/Pythia-Software/query-table/backends/go"
)

// Unlike the general search candidate indexes, these index Go-normalized text
// with case-sensitive trigrams. A quoted MATCH phrase therefore answers the
// supported substring predicate exactly, including negation. Other operators
// and short/non-ASCII needles keep the ordinary typed predicate.
type queryTextIndex struct{ table, from, rowID, body, field, columns, projection string }

var toolQueryText = queryTextIndex{table: "tool_query_text_fts", from: "FROM tool_calls t", rowID: "t.rowid", body: "t.command", field: "command"}
var humanQueryText = queryTextIndex{table: "human_query_text_fts", from: "FROM message_authorship ma JOIN messages m ON m.id=ma.message_id", rowID: "ma.rowid", body: "m.text", field: "text"}
var toolQueryFacts = queryTextIndex{table: "tool_query_facts", from: "FROM tool_calls t", rowID: "t.rowid",
	columns:    "rowid,id,workspace_id,started_at,duration_ms,provider,activity,tool_category,day",
	projection: "t.rowid,t.id,t.workspace_id,t.started_at,t.duration_ms,t.provider,machine_activity(t.tool_category,t.command_category,t.tool_name,t.mcp_server,t.program),t.tool_category,date(t.started_at,'localtime')"}
var queryTextIndexBatch = 1000

func (index queryTextIndex) version() string {
	if index.table == toolQueryFacts.table {
		janName, janOffset := time.Date(2020, 1, 1, 12, 0, 0, 0, time.Local).Zone()
		julName, julOffset := time.Date(2020, 7, 1, 12, 0, 0, 0, time.Local).Zone()
		return fmt.Sprintf("%s/day-v1/%s/%s:%d/%s:%d", toolRollupVersion, toolRollupTimezone(), janName, janOffset, julName, julOffset)
	}
	return "1"
}

func (index *queryTextIndex) term(clause qt.WhereClause) bool {
	if index == nil || clause.Field != index.field || clause.Op != "contains" || len(clause.Value) < 3 {
		return false
	}
	for _, r := range clause.Value {
		if r > 127 || r == 0 {
			return false
		}
	}
	return true
}
func (d sqlDataset) residualWhere(where []qt.WhereTerm) []qt.WhereTerm {
	result := make([]qt.WhereTerm, 0, len(where))
	for _, term := range where {
		exact := len(term.Predicates()) > 0
		for _, clause := range term.Predicates() {
			exact = exact && d.queryText.term(clause)
		}
		if !exact {
			result = append(result, term)
		}
	}
	return result
}
func (index *queryTextIndex) predicate(clause qt.WhereClause, bind func(any) string, denseFacts bool) string {
	match := index.rowID + " IN (SELECT rowid FROM " + index.table + " WHERE " + index.table + " MATCH " + bind(substringQuery([]string{strings.ToLower(clause.Value)})) + ")"
	if clause.Negated {
		// docsize records every nonempty indexed body, including bodies shorter than
		// three runes. It supplies the compiler's null/empty guard without reading
		// each wide command/message payload.
		guard := "EXISTS (SELECT 1 FROM " + index.table + "_docsize WHERE id=" + index.rowID + ")"
		if denseFacts {
			// A reduction benefits from driving narrow facts by the nonempty
			// membership set. A raw record window instead probes membership
			// while walking its paging index, avoiding a full source sort.
			guard = index.rowID + " IN (SELECT id FROM " + index.table + "_docsize)"
		}
		return "(" + guard + " AND NOT (" + match + "))"
	}
	return match
}
func (c *Catalog) queryTextDataset(ctx context.Context, dataset string, table sqlDataset) (sqlDataset, error) {
	var index *queryTextIndex
	switch dataset {
	case "tool_calls":
		index = &toolQueryText
	case "writing_messages":
		index = &humanQueryText
	default:
		return table, nil
	}
	var ready string
	err := c.reader().QueryRowContext(ctx, "SELECT value FROM meta WHERE key=?", index.table+"_ready").Scan(&ready)
	if err == sql.ErrNoRows {
		return table, nil
	}
	if err == nil && ready == "1" {
		table.queryText = index
	}
	if err == nil && dataset == "tool_calls" {
		var built string
		err = c.reader().QueryRowContext(ctx, "SELECT COALESCE((SELECT value FROM meta WHERE key='tool_query_facts_ready'),'')").Scan(&built)
		table.scalarFacts = built == toolRollupVersion || strings.HasPrefix(built, toolRollupVersion+"/day-v1/")
		table.calendarFacts = built == toolQueryFacts.version()
	}
	return table, err
}
func (c *Catalog) indexQueryTextBatch(ctx context.Context, index queryTextIndex) (bool, error) {
	tx, finish, err := c.beginTrackedWrite(ctx, "query-text-index")
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(); finish() }()
	var ready string
	err = tx.QueryRowContext(ctx, "SELECT value FROM meta WHERE key=?", index.table+"_ready").Scan(&ready)
	if err == nil && ready == index.version() {
		return false, nil
	}
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}
	var cursor, last sql.NullInt64
	calendarUpgrade := index.table == toolQueryFacts.table && (ready == toolRollupVersion || strings.HasPrefix(ready, toolRollupVersion+"/day-v1/"))
	from := index.from
	if calendarUpgrade {
		from = "FROM tool_query_facts t"
	}
	cursorKey := index.table + "_cursor_" + index.version()
	if err = tx.QueryRowContext(ctx, "SELECT CAST(value AS INTEGER) FROM meta WHERE key=?", cursorKey).Scan(&cursor); err != nil && err != sql.ErrNoRows {
		return false, err
	}
	if err = tx.QueryRowContext(ctx, "SELECT MAX(id) FROM (SELECT "+index.rowID+" id "+from+" WHERE "+index.rowID+">? ORDER BY "+index.rowID+" LIMIT ?)", cursor.Int64, queryTextIndexBatch).Scan(&last); err != nil {
		return false, err
	}
	if !last.Valid {
		_, err = tx.ExecContext(ctx, "INSERT OR REPLACE INTO meta(key,value) VALUES(?,?)", index.table+"_ready", index.version())
		if err == nil {
			// A later timezone change may return to a previously built version.
			// Its old completed cursor must not skip the calendar rebuild.
			_, err = tx.ExecContext(ctx, "DELETE FROM meta WHERE key=?", cursorKey)
		}
	} else if calendarUpgrade {
		if _, err = tx.ExecContext(ctx, "UPDATE tool_query_facts SET day=date(started_at,'localtime') WHERE rowid>? AND rowid<=?", cursor.Int64, last.Int64); err != nil {
			return false, err
		}
		_, err = tx.ExecContext(ctx, "INSERT OR REPLACE INTO meta(key,value) VALUES(?,?)", cursorKey, fmt.Sprint(last.Int64))
	} else {
		if _, err = tx.ExecContext(ctx, "DELETE FROM "+index.table+" WHERE rowid>? AND rowid<=?", cursor.Int64, last.Int64); err != nil {
			return false, err
		}
		columns, projection, nonempty := index.columns, index.projection, ""
		if columns == "" {
			columns = "rowid,body"
			projection = index.rowID + ",qt_lower(" + index.body + ")"
			nonempty = " AND " + index.body + " IS NOT NULL AND " + index.body + "<>''"
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO "+index.table+"("+columns+") SELECT "+projection+" "+index.from+" WHERE "+index.rowID+">? AND "+index.rowID+"<=?"+nonempty, cursor.Int64, last.Int64); err != nil {
			return false, err
		}
		_, err = tx.ExecContext(ctx, "INSERT OR REPLACE INTO meta(key,value) VALUES(?,?)", cursorKey, fmt.Sprint(last.Int64))
	}
	if err != nil {
		return false, err
	}
	return last.Valid, tx.Commit()
}
func (c *Catalog) maintainQueryTextIndexes(ctx context.Context) {
	// One operation for the whole build, forgotten if there was nothing to build.
	pass := c.beginPass(ctx, "maintenance.search.query", triggerVersion)
	defer func() { pass.end(nil) }()
	for _, index := range []queryTextIndex{humanQueryText, toolQueryText, toolQueryFacts} {
		for {
			if c.queries.wait(ctx) != nil {
				return
			}
			var more bool
			var err error
			pass.runIO(func() { more, err = c.indexQueryTextBatch(ctx, index) })
			if more {
				pass.worked(1)
			}
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				fmt.Fprintf(os.Stderr, "Query text index: %v\n", err)
			} else if !more {
				break
			}
			wait := 20 * time.Millisecond
			if err != nil {
				wait = 5 * time.Second
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}
	}
}

// Install the current trigger bodies after additive columns have migrated.
// A derived calendar is rebuilt before its version is accepted by readers.
func (c *Catalog) ensureQueryFactTriggers() error {
	tx, err := c.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	schema := schemaSQL()
	for _, name := range []string{"tool_query_facts_insert", "tool_query_facts_delete", "tool_query_facts_update"} {
		if _, err = tx.Exec("DROP TRIGGER IF EXISTS " + name); err != nil {
			return err
		}
		start := strings.Index(schema, "CREATE TRIGGER IF NOT EXISTS "+name+" ")
		if start < 0 {
			return fmt.Errorf("missing query fact trigger %s", name)
		}
		end := strings.Index(schema[start:], "\nEND;")
		if end < 0 {
			return fmt.Errorf("incomplete query fact trigger %s", name)
		}
		if _, err = tx.Exec(schema[start : start+end+5]); err != nil {
			return err
		}
	}
	return tx.Commit()
}
