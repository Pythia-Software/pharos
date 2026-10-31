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
// of messages_fts that could reach tool output filters out tool results. Reads
// that rank only a window of recent postings (search_conversations, Library
// search evidence) filter inside the window while old postings remain
// (toolOutputPostingsLeft), so a catalog still removing them searches the same
// population as one that has.
//
// An older Pharos that opens the library and ingests writes tool-output
// postings again, and once the removal here is done nothing removes them:
// every search filters them out, so they cost only index size. That is
// accepted rather than rescanning the index after every open.
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
// deletes the tool results among the next word-index rows, in one short
// transaction that also records the last row it checked. A deleted posting
// frees its stored text at once but leaves delete keys in the index's
// segments until they are merged, so a bounded merge loop follows. The file
// shrinks only with VACUUM.
const (
	toolOutputCursorKey  = "message_fts_tool_output_cursor"
	toolOutputRemovedKey = "message_fts_tool_output_removed"
	toolOutputMergedKey  = "message_fts_tool_output_merged"
	// toolOutputEndKey is the last word-index row when removal began: rows
	// after it were written by this writer, which never indexes tool output,
	// so removal ends there however fast ingest adds rows.
	toolOutputEndKey = "message_fts_tool_output_end"
	// toolOutputMergeKey is the pages the merge loop may still write.
	toolOutputMergeKey = "message_fts_tool_output_merge_pages"
)

// A batch's cost is mostly the text of the tool output it deletes, which
// FTS5 reads back and tokenizes, and that varies by orders of magnitude. So
// batches are sized as they go to hold the writer about toolOutputHold:
// rows of the index to check, or pages to merge, between these bounds.
var (
	toolOutputHold       = 250 * time.Millisecond
	toolOutputScan       = batchBounds{least: 1, first: 200, most: 5000}
	toolOutputMergePages = batchBounds{least: 16, first: 200, most: 2000}
)

type batchBounds struct{ least, first, most int }

// batchSizer sizes the next batch from how long the last one held the
// writer: halved when it held it over twice toolOutputHold, doubled when
// under half.
type batchSizer struct {
	bounds batchBounds
	size   int
}

func (s *batchSizer) next() int {
	if s.size == 0 {
		s.size = s.bounds.first
	}
	return min(max(s.size, s.bounds.least), s.bounds.most)
}

func (s *batchSizer) held(d time.Duration) {
	switch size := s.next(); {
	case d > 2*toolOutputHold:
		s.size = max(s.bounds.least, size/2)
	case d < toolOutputHold/2:
		s.size = min(s.bounds.most, size*2)
	}
}

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
	var end sql.NullInt64
	err := c.reader().QueryRowContext(ctx, `SELECT (SELECT value FROM meta WHERE key=?),(SELECT value FROM meta WHERE key=?),(SELECT value FROM meta WHERE key=?),(SELECT CAST(value AS INTEGER) FROM meta WHERE key=?)`,
		toolOutputRemovedKey, toolOutputMergedKey, toolOutputCursorKey, toolOutputEndKey).Scan(&removed, &merged, &cursor, &end)
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
	high := end
	if !high.Valid {
		if err := c.reader().QueryRowContext(ctx, `SELECT MAX(fts_rowid) FROM message_fts_rows`).Scan(&high); err != nil {
			return toolOutputProgress{}, err
		}
	}
	var at int64
	fmt.Sscan(cursor.String, &at)
	progress.Done, progress.Total = min(at, high.Int64), high.Int64
	return progress, nil
}

// removeToolOutputBatch removes the tool results among the next rows of the
// word index, reports whether any rows remain to check, and how long it held
// the writer.
func (c *Catalog) removeToolOutputBatch(ctx context.Context, rows int) (more bool, held time.Duration, err error) {
	tx, finish, err := c.beginTrackedWrite(ctx, "word-index-tool-output")
	if err != nil {
		return false, 0, err
	}
	started := time.Now()
	defer func() { _ = tx.Rollback(); finish(); held = time.Since(started) }()
	more, err = removeToolOutput(ctx, tx, rows)
	if err == nil {
		err = tx.Commit()
	}
	return more, 0, err
}

// While postings are removed, FTS5 merges only in the migration's own
// bounded steps. Each removal batch writes a segment of delete keys, and
// FTS5's automerge and crisismerge would otherwise merge whole levels of the
// index inside one batch: on a 94 GB catalog that held the writer for up to a
// minute. So automerge is off and crisismerge is only a backstop, and every
// removal batch is followed by an incremental merge of at most a few thousand
// pages (toolOutputMigration.step). The batch that finds nothing left to
// remove restores the policy Initialize sets (as does every open), before the
// merge loop, so that ingest during the merge loop merges as it always does.
//
// wordIndexMergePolicy keeps large bulk imports from spending most of their
// time merging FTS segments inside individual message inserts (Initialize).
const wordIndexMergePolicy = "INSERT INTO messages_fts(messages_fts,rank) VALUES('automerge',8);INSERT INTO messages_fts(messages_fts,rank) VALUES('crisismerge',32)"

const removalMergePolicy = "INSERT INTO messages_fts(messages_fts,rank) VALUES('automerge',0);INSERT INTO messages_fts(messages_fts,rank) VALUES('crisismerge',500)"

func removeToolOutput(ctx context.Context, tx *sql.Tx, scan int) (bool, error) {
	var done string
	if err := tx.QueryRowContext(ctx, "SELECT value FROM meta WHERE key=?", toolOutputRemovedKey).Scan(&done); err == nil {
		return false, nil
	} else if err != sql.ErrNoRows {
		return false, err
	}
	var cursor int64
	var end sql.NullInt64
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE((SELECT CAST(value AS INTEGER) FROM meta WHERE key=?),0),(SELECT CAST(value AS INTEGER) FROM meta WHERE key=?)", toolOutputCursorKey, toolOutputEndKey).Scan(&cursor, &end); err != nil {
		return false, err
	}
	if !end.Valid {
		if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(fts_rowid),0) FROM message_fts_rows").Scan(&end); err != nil {
			return false, err
		}
		if _, err := tx.ExecContext(ctx, "INSERT OR REPLACE INTO meta(key,value) VALUES(?,?)", toolOutputEndKey, fmt.Sprint(end.Int64)); err != nil {
			return false, err
		}
	}
	var last sql.NullInt64
	var rows string
	if err := tx.QueryRowContext(ctx, `WITH scanned AS (SELECT fts_rowid,message_id FROM message_fts_rows WHERE fts_rowid>? AND fts_rowid<=? ORDER BY fts_rowid LIMIT ?)
		SELECT (SELECT MAX(fts_rowid) FROM scanned),
		(SELECT json_group_array(s.fts_rowid) FROM scanned s JOIN messages m ON m.id=s.message_id WHERE NOT `+wordIndexedKind("m.")+`)`,
		cursor, end.Int64, scan).Scan(&last, &rows); err != nil {
		return false, err
	}
	if !last.Valid {
		// The merge loop's budget: index rows written or deleted, about two
		// for each page rewritten, so this allows rewriting the index twice.
		pages, err := wordIndexLeafPages(ctx, tx)
		if err != nil {
			return false, err
		}
		if _, err := tx.ExecContext(ctx, wordIndexMergePolicy); err != nil {
			return false, err
		}
		_, err = tx.ExecContext(ctx, "INSERT OR REPLACE INTO meta(key,value) VALUES(?,'1'),(?,?)", toolOutputRemovedKey, toolOutputMergeKey, fmt.Sprint(4*pages))
		return false, err
	}
	if rows != "[]" {
		if _, err := tx.ExecContext(ctx, removalMergePolicy); err != nil {
			return false, err
		}
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
		// Fuzzy cursors are different: their expansions come from the word
		// index's vocabulary, and removal can drop a spelling found only in
		// tool output. A fuzzy cursor carries its expansions (SearchText), so
		// only a page that would expand differently rejects it; removal moves
		// no generation, and so never fails a page in flight.
	}
	_, err := tx.ExecContext(ctx, "INSERT OR REPLACE INTO meta(key,value) VALUES(?,?)", toolOutputCursorKey, fmt.Sprint(last.Int64))
	return true, err
}

// mergeWordIndexStep does one bounded step of merging the word index and
// reports whether there is more to merge. During removal (final false) it is
// an incremental merge after each batch. Finally, it is a negative merge,
// which merges every segment into one, as optimize does, and so drops the
// removed postings with the delete keys that cancel them; it stops when one
// segment remains or the budget set when removal ended is spent. Ingest that
// adds a segment restarts a negative merge, so the budget, counted in the
// index rows each step actually wrote or deleted, also caps what restarts
// cost on a library that syncs. FTS5 changes at least two rows when a merge
// did any work.
func (c *Catalog) mergeWordIndexStep(ctx context.Context, pages int, final bool) (more bool, held time.Duration, err error) {
	tx, finish, err := c.beginTrackedWrite(ctx, "word-index-merge")
	if err != nil {
		return false, 0, err
	}
	started := time.Now()
	defer func() { _ = tx.Rollback(); finish(); held = time.Since(started) }()
	more, err = mergeWordIndex(ctx, tx, pages, final)
	if err == nil {
		err = tx.Commit()
	}
	return more, 0, err
}

func mergeWordIndex(ctx context.Context, tx *sql.Tx, pages int, final bool) (bool, error) {
	var budget sql.NullInt64
	if final {
		var done string
		if err := tx.QueryRowContext(ctx, "SELECT value FROM meta WHERE key=?", toolOutputMergedKey).Scan(&done); err == nil {
			return false, nil
		} else if err != sql.ErrNoRows {
			return false, err
		}
		if err := tx.QueryRowContext(ctx, "SELECT CAST(value AS INTEGER) FROM meta WHERE key=?", toolOutputMergeKey).Scan(&budget); err != nil && err != sql.ErrNoRows {
			return false, err
		}
		pages = -pages
	}
	var before, after int64
	if err := tx.QueryRowContext(ctx, "SELECT total_changes()").Scan(&before); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO messages_fts(messages_fts,rank) VALUES('merge',?)", pages); err != nil {
		return false, err
	}
	if err := tx.QueryRowContext(ctx, "SELECT total_changes()").Scan(&after); err != nil {
		return false, err
	}
	worked := after - before
	if !final {
		return worked >= 2, nil
	}
	segments, err := wordIndexSegments(ctx, tx)
	if err != nil {
		return false, err
	}
	left := budget.Int64 - worked
	if worked >= 2 && segments > 1 && left > 0 {
		_, err := tx.ExecContext(ctx, "UPDATE meta SET value=? WHERE key=?", fmt.Sprint(left), toolOutputMergeKey)
		return true, err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM meta WHERE key=?", toolOutputMergeKey); err != nil {
		return false, err
	}
	_, err = tx.ExecContext(ctx, "INSERT OR REPLACE INTO meta(key,value) VALUES(?,'1')", toolOutputMergedKey)
	return false, err
}

// wordIndexSegments is the number of the word index's segments, found the
// same way: a seek to the first page of each.
func wordIndexSegments(ctx context.Context, tx *sql.Tx) (int64, error) {
	var segments int64
	err := tx.QueryRowContext(ctx, `WITH RECURSIVE segment(id) AS (
		SELECT (SELECT id>>37 FROM messages_fts_data WHERE id>=(1<<37) ORDER BY id LIMIT 1)
		UNION ALL SELECT (SELECT id>>37 FROM messages_fts_data WHERE id>=((segment.id+1)<<37) ORDER BY id LIMIT 1) FROM segment WHERE segment.id IS NOT NULL)
		SELECT COUNT(*) FROM segment WHERE segment.id IS NOT NULL`).Scan(&segments)
	return segments, err
}

// wordIndexLeafPages is the leaf pages of the word index's segments. FTS5
// numbers a segment's leaves (segment id << 37) + page, so this seeks to each
// segment's first and last leaf rather than reading the index.
func wordIndexLeafPages(ctx context.Context, tx *sql.Tx) (int64, error) {
	var pages int64
	err := tx.QueryRowContext(ctx, `WITH RECURSIVE segment(id) AS (
		SELECT (SELECT id>>37 FROM messages_fts_data WHERE id>=(1<<37) ORDER BY id LIMIT 1)
		UNION ALL SELECT (SELECT id>>37 FROM messages_fts_data WHERE id>=((segment.id+1)<<37) ORDER BY id LIMIT 1) FROM segment WHERE segment.id IS NOT NULL)
		SELECT COALESCE(SUM((SELECT MAX(id) FROM messages_fts_data WHERE id BETWEEN segment.id<<37 AND (segment.id<<37)+2147483647)-(segment.id<<37)),0)
		FROM segment WHERE segment.id IS NOT NULL`).Scan(&pages)
	return pages, err
}

// toolOutputPostingsLeft is whether old tool-output postings may remain in
// the word index: until removal finishes. A read that ranks a window of
// recent postings filters them inside the window only then, since the filter
// costs a lookup per posting.
func (c *Catalog) toolOutputPostingsLeft(ctx context.Context) (bool, error) {
	var removed int
	err := c.reader().QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM meta WHERE key=?)", toolOutputRemovedKey).Scan(&removed)
	return removed == 0, err
}

// While old tool-output postings remain, a ranked search finds its window
// of recent matches by walking at most toolOutputWalk of the newest postings
// for the word, under toolOutputWalkTime: leaving tool output out costs a
// lookup of each posting's message, and for a word common in tool output the
// walk could otherwise pass hundreds of thousands of them.
var (
	toolOutputWalk     = 50_000
	toolOutputWalkTime = 2 * time.Second
)

// toolOutputCut is what a ranked search says when that walk cut it short.
const toolOutputCut = " Old tool-output postings are still being removed from the word index, so this search ranked fewer recent messages than usual."

// rankRecent runs a ranked search's query over its window of recent
// matches. run gets the SQL condition, on messages_fts after its MATCH, of
// the postings in the window, and the context to run under. While old
// tool-output postings remain, the window is the recentMatches most recent
// postings that are not tool output, found by a walk bounded by
// toolOutputWalk and by the search's walk deadline (one for the whole
// search). If the walk is capped or out of time, or the ranking then is, it
// falls back to plain, the window before this migration: the most recent
// postings of any kind, ranked by FTS5 alone, with tool output left out only
// after ranking. That ranks fewer matches, and cut says so.
func (c *Catalog) rankRecent(parent, walk context.Context, parsed, plain string, run func(ctx context.Context, window string) error) (cut bool, err error) {
	left, err := c.toolOutputPostingsLeft(parent)
	if err != nil || !left {
		if err != nil {
			return false, err
		}
		return false, run(parent, plain)
	}
	if cutoff, walked, err := c.toolOutputWalkCutoff(walk, parsed); err == nil && walked {
		err = run(walk, " AND rowid>="+fmt.Sprint(cutoff)+" AND NOT "+toolOutputPosting("messages_fts"))
		if err == nil || walk.Err() == nil || parent.Err() != nil {
			return false, err
		}
	} else if err != nil && (walk.Err() == nil || parent.Err() != nil) {
		return false, err
	}
	return true, run(parent, plain)
}

// toolOutputWalkCutoff walks the newest postings matching parsed, at most
// toolOutputWalk of them, to the recentMatches-th that is not tool output.
// walked is false when the walk was capped first.
func (c *Catalog) toolOutputWalkCutoff(ctx context.Context, parsed string) (cutoff int64, walked bool, err error) {
	var low, kept sql.NullInt64
	var count int
	err = c.reader().QueryRowContext(ctx, `WITH walk AS (SELECT rowid FROM messages_fts WHERE messages_fts MATCH ? ORDER BY rowid DESC LIMIT ?)
		SELECT (SELECT MIN(rowid) FROM walk),(SELECT COUNT(*) FROM walk),
		(SELECT walk.rowid FROM walk WHERE NOT `+toolOutputPosting("walk")+` ORDER BY walk.rowid DESC LIMIT 1 OFFSET ?)`,
		parsed, toolOutputWalk, recentMatches-1).Scan(&low, &count, &kept)
	switch {
	case err != nil:
		return 0, false, err
	case kept.Valid:
		return kept.Int64, true, nil
	default:
		return low.Int64, count < toolOutputWalk, nil
	}
}

// toolOutputPosting is the SQL predicate, on a messages_fts row, that it is
// an old tool-output posting.
func toolOutputPosting(fts string) string {
	return `EXISTS(SELECT 1 FROM message_fts_rows tr JOIN messages tm ON tm.id=tr.message_id WHERE tr.fts_rowid=` + fts + `.rowid AND NOT ` + wordIndexedKind("tm.") + `)`
}

// toolOutputMigration is the migration's batch sizes, and whether the
// last removal batch still needs its merge step.
type toolOutputMigration struct {
	removal, merge batchSizer
	removed        bool
}

func newToolOutputMigration() *toolOutputMigration {
	return &toolOutputMigration{removal: batchSizer{bounds: toolOutputScan}, merge: batchSizer{bounds: toolOutputMergePages}}
}

// step is the migration's next step: a removal batch, then merge steps
// once removal is done.
func (m *toolOutputMigration) step(ctx context.Context, c *Catalog) (bool, error) {
	if m.removed {
		m.removed = false
		_, held, err := c.mergeWordIndexStep(ctx, m.merge.next(), false)
		m.merge.held(held)
		return err == nil, err
	}
	more, held, err := c.removeToolOutputBatch(ctx, m.removal.next())
	if err != nil || more {
		m.removal.held(held)
		m.removed = more
		return more, err
	}
	more, held, err = c.mergeWordIndexStep(ctx, m.merge.next(), true)
	m.merge.held(held)
	return more, err
}

// maintainToolOutputIndex removes tool output from the word index of a
// catalog written before it was left out, then returns.
func (c *Catalog) maintainToolOutputIndex(ctx context.Context) {
	// One operation for the whole migration, forgotten if there was none.
	pass := c.beginPass(ctx, "maintenance.search.tool-output", triggerVersion)
	defer func() { pass.end(nil) }()
	migration := newToolOutputMigration()
	for {
		if c.queries.wait(ctx) != nil {
			return
		}
		var more bool
		var err error
		pass.runIO(func() { more, err = migration.step(ctx, c) })
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
// output: its surface includes tool results and its roles and kinds do not
// exclude them. Every writer stores tool output with role tool.
func textSurfaceToolOutput(o TextSearchOptions) bool {
	if o.Surface != "tools" && o.Surface != "all" || len(o.Roles) > 0 && !slices.Contains(o.Roles, "tool") {
		return false
	}
	return o.Kinds == nil || slices.Contains(o.Kinds, "all") || slices.Contains(o.Kinds, "tool_result")
}
