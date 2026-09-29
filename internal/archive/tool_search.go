package archive

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"regexp/syntax"
	"time"
	"unicode/utf8"
)

// Old catalogs are indexed in small transactions. Triggers index new calls
// immediately; a batch replaces any rows they already indexed.
const toolSearchBatch = 500

func (c *Catalog) toolSearchReady(ctx context.Context) (bool, error) {
	var version string
	err := c.DB.QueryRowContext(ctx, "SELECT value FROM meta WHERE key='tool_command_fts_version'").Scan(&version)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return version == "1", err
}

func (c *Catalog) indexToolSearchBatch(ctx context.Context) (bool, error) {
	tx, finish, err := c.beginTrackedWrite(ctx, "tool-search-index")
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(); finish() }()
	var version string
	if err := tx.QueryRowContext(ctx, "SELECT value FROM meta WHERE key='tool_command_fts_version'").Scan(&version); err == nil {
		return false, nil
	} else if err != sql.ErrNoRows {
		return false, err
	}
	var cursor, last sql.NullInt64
	if err := tx.QueryRowContext(ctx, "SELECT CAST(value AS INTEGER) FROM meta WHERE key='tool_command_fts_cursor'").Scan(&cursor); err != nil && err != sql.ErrNoRows {
		return false, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT MAX(rowid) FROM (SELECT rowid FROM tool_calls WHERE rowid>? ORDER BY rowid LIMIT ?)`, cursor.Int64, toolSearchBatch).Scan(&last); err != nil {
		return false, err
	}
	if !last.Valid {
		if _, err := tx.ExecContext(ctx, "INSERT OR REPLACE INTO meta(key,value) VALUES('tool_command_fts_version','1')"); err != nil {
			return false, err
		}
		return false, tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM tool_command_fts WHERE rowid>? AND rowid<=?", cursor.Int64, last.Int64); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO tool_command_fts(rowid,command,program,subcommand,command_name)
		SELECT rowid,command,program,subcommand,TRIM(COALESCE(program,'')||' '||COALESCE(subcommand,'')) FROM tool_calls WHERE rowid>? AND rowid<=?
		AND (COALESCE(command,'')<>'' OR COALESCE(program,'')<>'' OR COALESCE(subcommand,'')<>'')`, cursor.Int64, last.Int64); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT OR REPLACE INTO meta(key,value) VALUES('tool_command_fts_cursor',?)", fmt.Sprint(last.Int64)); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func (c *Catalog) maintainToolSearch(ctx context.Context) {
	for {
		more, err := c.indexToolSearchBatch(ctx)
		wait := 20 * time.Millisecond
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			fmt.Fprintf(os.Stderr, "Tool search index: %v\n", err)
			wait = 5 * time.Second
		} else if !more {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// The trigram index is only a candidate filter. The ordinary SQL predicate
// still decides the result, preserving LIKE and Go regexp semantics.
func toolSearchTerm(clauseField, op, value string) string {
	if clauseField != "command" && clauseField != "program" && clauseField != "subcommand" && clauseField != "command_name" {
		return ""
	}
	var literal string
	switch op {
	case "contains", "starts_with", "ends_with":
		literal = value
	case "matches_regex":
		parsed, err := syntax.Parse(value, syntax.Perl)
		if err != nil {
			return ""
		}
		literal = regexRequiredLiteral(parsed)
	default:
		return ""
	}
	if utf8.RuneCountInString(literal) < 3 {
		return ""
	}
	for _, r := range literal {
		if r > 127 {
			return "" // SQLite LIKE and FTS fold non-ASCII differently.
		}
	}
	return clauseField + ":" + substringQuery([]string{literal})
}

// A concatenation must contain every required literal in its children;
// choose its longest one. Other regex forms may match without any one literal.
func regexRequiredLiteral(expr *syntax.Regexp) string {
	switch expr.Op {
	case syntax.OpLiteral:
		if expr.Flags&syntax.FoldCase != 0 {
			return ""
		}
		return string(expr.Rune)
	case syntax.OpCapture:
		return regexRequiredLiteral(expr.Sub[0])
	case syntax.OpConcat:
		best := ""
		for _, sub := range expr.Sub {
			if literal := regexRequiredLiteral(sub); len(literal) > len(best) {
				best = literal
			}
		}
		return best
	case syntax.OpPlus:
		return regexRequiredLiteral(expr.Sub[0])
	case syntax.OpRepeat:
		if expr.Min > 0 {
			return regexRequiredLiteral(expr.Sub[0])
		}
	}
	return ""
}

func toolSearchPredicate(term string) string {
	if term == "" {
		return ""
	}
	return "t.rowid IN (SELECT rowid FROM tool_command_fts WHERE tool_command_fts MATCH ?)"
}
