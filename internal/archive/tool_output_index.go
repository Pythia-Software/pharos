package archive

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"slices"
	"time"
)

// Tool output (kind tool_result) is not word-indexed. It is most of a
// catalog's message text (84% in a large one), and so most of messages_fts.
// Every other kind with text is: messages_fts, and so message_fts_rows and the
// substring index keyed off it, hold no tool result. Its text stays in
// messages, readable in the reader and through get_conversation_messages, and
// search_text scans it for a conversation or a few workspaces.
//
// Every writer of messages_fts selects through wordIndexedKind, and every read
// of messages_fts that could reach tool output filters by it too, so a catalog
// that has not finished removing its old tool-result postings searches the
// same population as one that has.
const unindexedKinds = "'tool_result'"

// wordIndexedKind is the SQL predicate, on the messages alias (with its dot,
// as messageFilters takes it), of the kinds messages_fts holds.
func wordIndexedKind(alias string) string {
	return alias + "kind NOT IN (" + unindexedKinds + ")"
}

// unindexedOnly is whether kinds asks only for kinds the word index leaves
// out, so that a word-index search of them could only ever be empty.
func unindexedOnly(kinds []string) bool {
	if len(kinds) == 0 {
		return false
	}
	for _, kind := range kinds {
		if kind != "tool_result" {
			return false
		}
	}
	return true
}

// kindArgument is an MCP kinds argument, decoded JSON or a Go caller's list.
func kindArgument(value any) []string {
	if list, ok := value.([]string); ok {
		return list
	}
	return stringSlice(value)
}

// toolOutputNotIndexed is what a word-index search says about tool output, in
// its result and in its tool description.
const toolOutputNotIndexed = "Tool output (tool_result messages) is not word-indexed, so word searches do not reach it. To search a conversation's tool output, use search_text with surface tools (or all) and conversation_id, workspace_id or up to 100 workspace_ids; that scans the stored text."

// toolOutputCoverage is the short form a search result carries. A result
// over its output budget sheds it before a match (fitConversationItems): the
// tool's description says the same.
const toolOutputCoverage = "Tool output is not word-indexed or searched here; scoped search_text scans it."

// A catalog written before tool output left the word index still holds its
// postings. They are removed in the background, oldest first: each batch
// deletes the tool results among the next toolOutputScan word-index rows, in
// one short transaction that also records the last row it checked. Then a
// merge loop rewrites the index without them, toolOutputMergePages pages a
// step: a deleted posting frees its stored text at once but stays in the
// index's segments until they are merged. The file shrinks only with VACUUM.
const (
	toolOutputCursorKey  = "message_fts_tool_output_cursor"
	toolOutputRemovedKey = "message_fts_tool_output_removed"
	toolOutputMergedKey  = "message_fts_tool_output_merged"
)

var (
	toolOutputScan       = 2000
	toolOutputMergePages = 2000
)

type toolOutputProgress struct {
	// Ready is true once the postings are removed and the index merged.
	Ready bool `json:"ready"`
	// Phase is "removing" or "merging" while not ready.
	Phase string `json:"phase,omitempty"`
	// Done and Total measure removal by word-index row number, which is
	// close to a count of rows: rows are numbered in order and rarely deleted.
	// Merging has no measured total.
	Done  int64 `json:"done,omitempty"`
	Total int64 `json:"total,omitempty"`
}

// toolOutputIndexProgress reads meta and, while removing, one index lookup.
func (c *Catalog) toolOutputIndexProgress(ctx context.Context) (toolOutputProgress, error) {
	var removed, merged, cursor sql.NullString
	err := c.reader().QueryRowContext(ctx, `SELECT (SELECT value FROM meta WHERE key=?),(SELECT value FROM meta WHERE key=?),(SELECT value FROM meta WHERE key=?)`,
		toolOutputRemovedKey, toolOutputMergedKey, toolOutputCursorKey).Scan(&removed, &merged, &cursor)
	if err != nil {
		return toolOutputProgress{}, err
	}
	switch {
	case merged.Valid:
		return toolOutputProgress{Ready: true}, nil
	case removed.Valid:
		return toolOutputProgress{Phase: "merging"}, nil
	}
	progress := toolOutputProgress{Phase: "removing"}
	var high sql.NullInt64
	if err := c.reader().QueryRowContext(ctx, `SELECT MAX(fts_rowid) FROM message_fts_rows`).Scan(&high); err != nil {
		return toolOutputProgress{}, err
	}
	var at int64
	fmt.Sscan(cursor.String, &at)
	progress.Done, progress.Total = min(at, high.Int64), high.Int64
	return progress, nil
}

// removeToolOutputBatch removes the tool results among the next batch of
// word-index rows and reports whether any rows remain to check.
func (c *Catalog) removeToolOutputBatch(ctx context.Context) (bool, error) {
	tx, finish, err := c.beginTrackedWrite(ctx, "word-index-tool-output")
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(); finish() }()
	var done string
	if err := tx.QueryRowContext(ctx, "SELECT value FROM meta WHERE key=?", toolOutputRemovedKey).Scan(&done); err == nil {
		return false, nil
	} else if err != sql.ErrNoRows {
		return false, err
	}
	var cursor int64
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE((SELECT CAST(value AS INTEGER) FROM meta WHERE key=?),0)", toolOutputCursorKey).Scan(&cursor); err != nil {
		return false, err
	}
	var last sql.NullInt64
	var rows string
	if err := tx.QueryRowContext(ctx, `WITH scanned AS (SELECT fts_rowid,message_id FROM message_fts_rows WHERE fts_rowid>? ORDER BY fts_rowid LIMIT ?)
		SELECT (SELECT MAX(fts_rowid) FROM scanned),
		(SELECT json_group_array(s.fts_rowid) FROM scanned s JOIN messages m ON m.id=s.message_id WHERE NOT `+wordIndexedKind("m.")+`)`,
		cursor, toolOutputScan).Scan(&last, &rows); err != nil {
		return false, err
	}
	if !last.Valid {
		if _, err := tx.ExecContext(ctx, "INSERT OR REPLACE INTO meta(key,value) VALUES(?,'1')", toolOutputRemovedKey); err != nil {
			return false, err
		}
		return false, tx.Commit()
	}
	if rows != "[]" {
		// Removing postings reorders no remaining match, and every search
		// already leaves tool output out of the word index, so open search
		// cursors stay valid: keep the generation the mapping's delete
		// trigger moves.
		var generation sql.NullString
		if err := tx.QueryRowContext(ctx, "SELECT value FROM meta WHERE key='text_search_generation'").Scan(&generation); err != nil && err != sql.ErrNoRows {
			return false, err
		}
		for _, statement := range []string{
			"DELETE FROM messages_fts WHERE rowid IN (SELECT value FROM json_each(?))",
			"DELETE FROM message_fts_rows WHERE fts_rowid IN (SELECT value FROM json_each(?))",
		} {
			if _, err := tx.ExecContext(ctx, statement, rows); err != nil {
				return false, err
			}
		}
		if generation.Valid {
			if _, err := tx.ExecContext(ctx, "UPDATE meta SET value=? WHERE key='text_search_generation'", generation.String); err != nil {
				return false, err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, "INSERT OR REPLACE INTO meta(key,value) VALUES(?,?)", toolOutputCursorKey, fmt.Sprint(last.Int64)); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// mergeWordIndexStep does one bounded step of merging the word index's
// segments into one, which drops the removed postings, and reports whether
// there is more to merge. A negative merge merges every segment, as optimize
// does, but writes at most that many pages a transaction; FTS5 changes at
// least two rows when it did any work.
func (c *Catalog) mergeWordIndexStep(ctx context.Context) (bool, error) {
	tx, finish, err := c.beginTrackedWrite(ctx, "word-index-merge")
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(); finish() }()
	var done string
	if err := tx.QueryRowContext(ctx, "SELECT value FROM meta WHERE key=?", toolOutputMergedKey).Scan(&done); err == nil {
		return false, nil
	} else if err != sql.ErrNoRows {
		return false, err
	}
	var before, after int64
	if err := tx.QueryRowContext(ctx, "SELECT total_changes()").Scan(&before); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO messages_fts(messages_fts,rank) VALUES('merge',?)", -toolOutputMergePages); err != nil {
		return false, err
	}
	if err := tx.QueryRowContext(ctx, "SELECT total_changes()").Scan(&after); err != nil {
		return false, err
	}
	more := after-before >= 2
	if !more {
		if _, err := tx.ExecContext(ctx, "INSERT OR REPLACE INTO meta(key,value) VALUES(?,'1')", toolOutputMergedKey); err != nil {
			return false, err
		}
	}
	return more, tx.Commit()
}

// toolOutputIndexStep is the migration's next step: a removal batch, then
// merge steps once removal is done.
func (c *Catalog) toolOutputIndexStep(ctx context.Context) (bool, error) {
	more, err := c.removeToolOutputBatch(ctx)
	if err != nil || more {
		return more, err
	}
	return c.mergeWordIndexStep(ctx)
}

// maintainToolOutputIndex removes tool output from the word index of a
// catalog written before it was left out, then returns.
func (c *Catalog) maintainToolOutputIndex(ctx context.Context) {
	// One operation for the whole migration, forgotten if there was none.
	pass := c.beginPass(ctx, "maintenance.search.tool-output", triggerVersion)
	defer func() { pass.end(nil) }()
	for {
		var more bool
		var err error
		pass.runIO(func() { more, err = c.toolOutputIndexStep(ctx) })
		if more {
			pass.worked(1)
		}
		wait := 20 * time.Millisecond
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			fmt.Fprintf(os.Stderr, "Word index tool output: %v\n", err)
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

// textSurfaceToolOutput is whether a search_text request can match tool
// output: its surface includes tool results and its kinds do not exclude them.
func textSurfaceToolOutput(o TextSearchOptions) bool {
	if o.Surface != "tools" && o.Surface != "all" {
		return false
	}
	return o.Kinds == nil || slices.Contains(o.Kinds, "all") || slices.Contains(o.Kinds, "tool_result")
}
