package archive

import (
	"context"
	"database/sql"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"
)

// Peer links connect independently started top-level work that worked on the
// same change: typically an implementing conversation and a review whose
// findings were pasted back into it. They come only from signals any harness
// leaves (docs/peer-conversations-design.md):
//
//   - copied_into: a user turn quoted another conversation's reply
//     (quoted message_authorship_spans with a source), within the authorship
//     window.
//   - read_written_by: a conversation's tool output (a diff, a file read)
//     showed lines another conversation wrote in its edit calls shortly before.
//   - tl1_candidate: TL1 tasks that worked on the same candidate.
//
// Timing confirms a content match: the reviewer started as the writer's turn
// ended, or the writer's next prompt followed the review. Quotes and TL1
// candidates are high confidence; a read is high only when timing confirms a
// diff (peerReadLink). Low links are shown as uncertain. Links are stored between mirror representatives of the
// workspaces holding each family's top-level conversation, so a helper never
// becomes a peer.

// peerLinksVersion names the derivation rules; a new version rebuilds.
const peerLinksVersion = "peers-v4"

// Tunables are package variables so tests can lower them.
var (
	// peerWindow bounds how long before a read the read lines were written,
	// matching the authorship window for copied agent output.
	peerWindow = 7 * 24 * time.Hour
	// peerMinLines is the fewest matched lines that count.
	peerMinLines = 3
	// peerRatio is how far the top writer must lead the runner-up when the
	// lines were only read (not shown as a diff).
	peerRatio = 3
	// peerTimingGap is the longest hand-off pause timing accepts.
	peerTimingGap = 30 * time.Minute
	// peerRecheck is how long before the earliest new tool call readers are
	// derived again: a review's length plus the pause before the writer's
	// next prompt.
	peerRecheck = 3 * time.Hour
	// peerReaderOwnLines: a reader that had written this many lines of its own
	// before it read another's is more likely a parallel implementer than a
	// reviewer, so its match needs the writer's next prompt to follow it.
	peerReaderOwnLines = 1
	// peerMinCopyWords is the fewest quoted words a copy link needs: the
	// authorship minimum for a copied span.
	peerMinCopyWords = 12
	// peerCopyWords is the fewest quoted words a certain copy link needs.
	peerCopyWords = 40
	// peerCopyGap is the longest a reply can wait to be quoted and still link.
	peerCopyGap = 24 * time.Hour
	// peerMaxPeers caps the related work a page shows.
	peerMaxPeers = 24
)

const (
	peerHigh = "high"
	peerLow  = "low"
)

// peerDiffCall selects ledger calls that show changes: their added lines are
// unmerged work. A call's program is its first command's, so a compound call
// (`git status && git diff`) is checked command by command.
const peerDiffCall = `(` + peerDiffCommand + ` OR ` + peerCompoundCall + ` AND EXISTS(SELECT 1 FROM tool_commands tc
 WHERE tc.tool_call_id=t.id AND ` + peerDiffCommandTC + `))`

// What makes a call t one a link can be derived from. The statements below
// and the triggers (peerTriggerDDL, which reads these for the row written)
// share them, so a call the statements can use is one the triggers note.
const (
	// A command that shows changes: a call's own first command, and a
	// compound call's command tc.
	peerDiffCommand   = `t.program IN ('git','gh','jj') AND t.subcommand IN ('diff','show','pr')`
	peerDiffCommandTC = `tc.program IN ('git','gh','jj') AND tc.subcommand IN ('diff','show','pr')`
	peerCompoundCall  = `t.command_count>1`
	// A call that wrote lines, and one that read a file.
	peerWriteCall = `(t.tool_category='edit' OR t.has_heredoc=1)`
	peerFileRead  = `t.tool_category='read'`
)

// peerCallBears is whether the written row of tool_calls can be a write, a
// read or a look at changes: no link is derived from any other call.
func peerCallBears(row string) string {
	return "(" + strings.ReplaceAll(peerWriteCall+" OR "+peerFileRead+" OR "+peerDiffCommand+" OR "+peerCompoundCall, "t.", row+".") + ")"
}

// The statements that read the tool ledger. The triggers that note a call
// changed in place (peerTriggerDDL) compare the columns these read, so every
// read of tool_calls or tool_commands for a link belongs here.
const (
	// A new call has a new rowid.
	peerNewCallsQuery = `SELECT conversation_id,MIN(started_at),MAX(started_at) FROM tool_calls WHERE rowid>? AND started_at IS NOT NULL GROUP BY +conversation_id`
	// Conversations with a call that writes or shows changes.
	peerLinkableQuery = `SELECT j.value FROM json_each(?) j WHERE EXISTS(SELECT 1 FROM tool_calls t
 WHERE t.conversation_id=j.value AND (` + peerWriteCall + ` OR ` + peerDiffCall + `))`
	// Readers: conversations with a call that shows changes. The unary +
	// lets a later run take the start-time index rather than scan the ledger
	// in conversation order for DISTINCT.
	peerReadersQuery      = `SELECT DISTINCT +t.conversation_id FROM tool_calls t WHERE ` + peerDiffCall
	peerReadersAmongQuery = `SELECT j.value FROM json_each(?) j WHERE EXISTS(SELECT 1 FROM tool_calls t WHERE t.conversation_id=j.value AND ` + peerDiffCall + `)`
	// Conversations that read or looked at changes in a span of time.
	peerReadSinceQuery = `SELECT DISTINCT +t.conversation_id FROM tool_calls t WHERE (` + peerDiffCall + ` OR ` + peerFileRead + `) AND t.started_at>=?`
	peerReadUntil      = ` AND t.started_at<=?`
	// A reader's diff and file-read calls, oldest first.
	peerReadsQuery = `SELECT t.result_message_id,COALESCE(t.started_at,''),COALESCE(` + peerDiffCall + `,0) FROM tool_calls t
 WHERE t.conversation_id IN (SELECT value FROM json_each(?)) AND t.result_message_id IS NOT NULL AND (` + peerDiffCall + ` OR ` + peerFileRead + `)
 ORDER BY t.started_at,t.id`
	// A day's edit calls with what they wrote.
	peerWritesQuery = `SELECT t.conversation_id,COALESCE(t.call_message_id,''),t.started_at,m.text FROM tool_calls t
 JOIN messages m ON m.id=t.call_message_id
 WHERE t.started_at>=? AND t.started_at<? AND ` + peerWriteCall
)

var peerLedgerStatements = []string{peerNewCallsQuery, peerLinkableQuery, peerReadersQuery, peerReadersAmongQuery,
	peerReadSinceQuery + peerReadUntil, peerReadsQuery, peerWritesQuery}

// peerLedgerCalls are the ledger tables a link is derived from, each with
// the conversation and start of the call a written row belongs to.
var peerLedgerCalls = map[string]func(row string) string{
	"tool_calls": func(row string) string {
		return "SELECT " + row + ".conversation_id conversation_id," + row + ".started_at started_at"
	},
	"tool_commands": func(row string) string {
		return "SELECT t.conversation_id,t.started_at FROM tool_calls t WHERE t.id=" + row + ".tool_call_id"
	},
}

// peerLedgerWorkspace is the workspace of that call, kept with the note for
// when the conversation is gone. A call that only moves workspace changes no
// link, so this is not part of what the update triggers compare.
var peerLedgerWorkspace = map[string]func(row string) string{
	"tool_calls": func(row string) string { return row + ".workspace_id" },
	"tool_commands": func(row string) string {
		return "(SELECT workspace_id FROM tool_calls WHERE id=" + row + ".tool_call_id)"
	},
}

// peerTriggerDDL notes the ledger writes a refresh would not otherwise see:
// the conversation of a call that changed in place, with the call's start,
// so the readers around it are derived again. A new call has a new rowid,
// which the refresh reads. A call that can bear on a link (peerCallBears)
// and is deleted, or updated in a column a link is derived from
// (projectionChange over peerLedgerStatements), is noted with its start
// before and after; so is the call of a command that shows changes when
// that command is added, removed or changed. A call stored again with only
// other columns different, as a replay of the ledger for a new version
// stores most, notes nothing, and neither does any other kind of call.
func peerTriggerDDL(q queryer) ([]string, error) {
	note := func(table, row string) string {
		return "INSERT INTO work_peer_links_changed(conversation_id,first,last,revision,workspace_id) SELECT conversation_id,started_at,started_at,hex(randomblob(8))," +
			peerLedgerWorkspace[table](row) + " FROM (" + peerLedgerCalls[table](row) + ") WHERE started_at IS NOT NULL ON CONFLICT(conversation_id) DO UPDATE SET" +
			" first=min(first,excluded.first),last=max(last,excluded.last),revision=excluded.revision,workspace_id=COALESCE(excluded.workspace_id,workspace_id);"
	}
	shows := func(row string) string {
		return "(" + strings.ReplaceAll(peerDiffCommandTC, "tc.", row+".") + ")"
	}
	// Only a call a link can be derived from, and only a command that shows
	// changes, before or after, bears on a link.
	relevant := map[string]map[string]string{
		"tool_calls":    {"DELETE": peerCallBears("OLD"), "UPDATE": "(" + peerCallBears("OLD") + " OR " + peerCallBears("NEW") + ")"},
		"tool_commands": {"INSERT": shows("NEW"), "DELETE": shows("OLD"), "UPDATE": "(" + shows("OLD") + " OR " + shows("NEW") + ")"},
	}
	statements := []string{}
	for _, table := range slices.Sorted(maps.Keys(peerLedgerCalls)) {
		// The columns compared are the ones named in UPDATE OF, so an
		// update of other columns does not evaluate the trigger at all.
		changed, columns, err := projectionChange(q, table, append(slices.Clone(peerLedgerStatements), peerLedgerCalls[table]("NEW")))
		if err != nil {
			return nil, err
		}
		for _, event := range []string{"INSERT", "UPDATE", "DELETE"} {
			if table == "tool_calls" && event == "INSERT" {
				continue
			}
			when, body := []string{}, note(table, "NEW")
			if condition := relevant[table][event]; condition != "" {
				when = append(when, condition)
			}
			switch event {
			case "UPDATE":
				when, body = append(when, "("+changed+")"), note(table, "OLD")+" "+body
			case "DELETE":
				body = note(table, "OLD")
			}
			statement := "CREATE TRIGGER work_peer_links_" + table + "_" + strings.ToLower(event) + " AFTER " + event
			if event == "UPDATE" {
				statement += ` OF "` + strings.Join(columns, `","`) + `"`
			}
			statement += " ON " + table
			if len(when) > 0 {
				statement += " WHEN " + strings.Join(when, " AND ")
			}
			statements = append(statements, statement+" BEGIN "+body+" END")
		}
	}
	return statements, nil
}

// ensurePeerLinkTriggers installs the triggers when they are not the ones
// this build generates, replacing an earlier build's (which noted a late
// result, and nothing else, by its time alone). It marks nothing: what an
// earlier build noted in work_peer_links_dirty is still read.
func (c *Catalog) ensurePeerLinkTriggers() error {
	triggers, err := peerTriggerDDL(c.DB)
	if err != nil {
		return err
	}
	want := triggerDigest(triggers)
	// An earlier build that opens the catalog installs its trigger again.
	var current bool
	if err := c.DB.QueryRow(`SELECT NOT EXISTS(SELECT 1 FROM sqlite_master WHERE type='trigger' AND name='work_peer_links_result_update')
 AND EXISTS(SELECT 1 FROM meta WHERE key='peer_links_triggers' AND value=?)`, want).Scan(&current); err != nil || current {
		return err
	}
	tx, err := c.beginWrite(context.Background())
	if err != nil {
		return err
	}
	defer tx.Rollback()
	names, err := queryMaps(tx, "SELECT name FROM sqlite_master WHERE type='trigger' AND name GLOB 'work_peer_links_*'")
	if err != nil {
		return err
	}
	for _, row := range names {
		if _, err := tx.Exec(`DROP TRIGGER IF EXISTS "` + firstString(row["name"]) + `"`); err != nil {
			return err
		}
	}
	for _, statement := range triggers {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	if _, err := tx.Exec("INSERT OR REPLACE INTO meta(key,value) VALUES('peer_links_triggers',?)", want); err != nil {
		return err
	}
	return tx.Commit()
}

// peerFamilies maps conversations to the representative workspace of their
// family's top-level conversation.
type peerFamilies struct {
	root map[string]string   // conversation → root workspace
	top  map[string][]string // root workspace → its top-level conversations
	// within is the root workspaces of the conversations a workspace holds.
	within map[string]map[string]bool
}

func (c *Catalog) loadPeerFamilies(ctx context.Context, q queryer) (*peerFamilies, error) {
	representatives, err := c.authorshipRepresentatives(ctx)
	if err != nil {
		return nil, err
	}
	type conv struct{ workspace, parent string }
	convs := map[string]conv{}
	rows, err := q.QueryContext(ctx, `SELECT id,workspace_id,COALESCE(parent_id,'') FROM conversations`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var cv conv
		if err := rows.Scan(&id, &cv.workspace, &cv.parent); err != nil {
			rows.Close()
			return nil, err
		}
		convs[id] = cv
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	launcher := map[string]string{}
	rows, err = q.QueryContext(ctx, `SELECT child_id,parent_id FROM conversation_launches`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var child, parent string
		if err := rows.Scan(&child, &parent); err != nil {
			rows.Close()
			return nil, err
		}
		launcher[child] = parent
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	f := &peerFamilies{root: map[string]string{}, top: map[string][]string{}, within: map[string]map[string]bool{}}
	ids := make([]string, 0, len(convs))
	for id := range convs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		top := id
		for range 64 {
			next := convs[top].parent
			if _, ok := convs[next]; !ok {
				next = launcher[top]
			}
			if _, ok := convs[next]; !ok || next == top {
				break
			}
			top = next
		}
		workspace := convs[top].workspace
		if rep := representatives[workspace]; rep != "" {
			workspace = rep
		}
		f.root[id] = workspace
		if f.within[convs[id].workspace] == nil {
			f.within[convs[id].workspace] = map[string]bool{}
		}
		f.within[convs[id].workspace][workspace] = true
		if top == id && convs[id].workspace == workspace {
			f.top[workspace] = append(f.top[workspace], id)
		}
	}
	return f, nil
}

type peerLink struct {
	left, right, kind, confidence string
	leftMessage, rightMessage, at string
	evidence                      map[string]any
}

// RefreshPeerLinks brings work_peer_links up to date. Copy and TL1 links are
// rederived whenever writing estimates or TL1 change; read links only for
// readers whose reads or preceding writes are new since the last run.
func (c *Catalog) RefreshPeerLinks(ctx context.Context) error {
	authorship, err := c.metaValue(ctx, "authorship_generation")
	if err != nil {
		return err
	}
	version, err := c.metaValue(ctx, "peer_links_version")
	if err != nil {
		return err
	}
	builtAuthorship, err := c.metaValue(ctx, "peer_links_authorship")
	if err != nil {
		return err
	}
	tl1, err := c.peerTL1Signature(ctx)
	if err != nil {
		return err
	}
	builtTL1, err := c.metaValue(ctx, "peer_links_tl1")
	if err != nil {
		return err
	}
	// Only new rules rebuild everything: a full pass reads every diff in the
	// library (minutes on a large one), and the tool ledger's generation moves
	// with nearly every index.
	want := peerLinksVersion
	full := version != want
	var lastRow int64
	if !full {
		stored, err := c.metaValue(ctx, "peer_links_tool_rowid")
		if err != nil {
			return err
		}
		fmt.Sscan(stored, &lastRow)
	}
	var maxRow int64
	if err := c.reader().QueryRowContext(ctx, `SELECT COALESCE(MAX(rowid),0) FROM tool_calls WHERE rowid>?`, lastRow).Scan(&maxRow); err != nil {
		return err
	}
	if maxRow < lastRow {
		maxRow = lastRow
	}
	// What is new is kept by conversation, with the span of its times: one
	// earliest time for the whole library made a single old-dated import
	// (months of chats, which hold no tool call) rescan every reader since.
	var calls, turns []peerEvidence
	if !full {
		if calls, err = c.peerEvidence(ctx, peerNewCallsQuery, lastRow); err != nil {
			return err
		}
	}
	// A call changed in place keeps its rowid: one stored before its result
	// came back, or one a replay of the ledger classified differently.
	// Triggers note its conversation and start (peerTriggerDDL).
	changed, err := c.peerChanges(ctx)
	if err != nil {
		return err
	}
	// An earlier build noted only the earliest such start in the library.
	var updated, revision sql.NullString
	if err := c.reader().QueryRowContext(ctx, `SELECT since,revision FROM work_peer_links_dirty WHERE id=1`).Scan(&updated, &revision); err != nil && err != sql.ErrNoRows {
		return err
	}
	// Timing comes from prompts and replies, so one that arrives without a
	// tool call (the writer's next prompt after a review) rechecks too.
	var lastMessage, maxMessage int64
	if !full {
		stored, err := c.metaValue(ctx, "peer_links_message_rowid")
		if err != nil {
			return err
		}
		fmt.Sscan(stored, &lastMessage)
		if err := c.reader().QueryRowContext(ctx, `SELECT COALESCE(MAX(rowid),0) FROM messages WHERE rowid>?`, lastMessage).Scan(&maxMessage); err != nil {
			return err
		}
		if turns, err = c.peerEvidence(ctx, `SELECT conversation_id,MIN(created_at),MAX(created_at) FROM messages WHERE rowid>? AND +kind='message' AND created_at IS NOT NULL GROUP BY +conversation_id`, lastMessage); err != nil {
			return err
		}
	} else if err := c.reader().QueryRowContext(ctx, `SELECT COALESCE(MAX(rowid),0) FROM messages`).Scan(&maxMessage); err != nil {
		return err
	}
	if maxMessage < lastMessage {
		maxMessage = lastMessage
	}
	contentDirty := full || builtAuthorship != authorship || builtTL1 != tl1
	if !full && !contentDirty && len(calls) == 0 && len(turns) == 0 && len(changed) == 0 && !updated.Valid {
		return nil
	}
	reportWork(ctx, "linking related conversations", "", 0, -1)
	families, err := c.loadPeerFamilies(ctx, c.reader())
	if err != nil {
		return err
	}
	var scope *peerScope
	if !full {
		if scope, err = c.peerReadScope(ctx, families, calls, turns, changed, updated); err != nil {
			return err
		}
	}
	readsDirty := full || scope != nil
	if !readsDirty && !contentDirty {
		// Only turns that no link can involve arrived (an import of chats):
		// remember they were seen, so the next run does not weigh them again.
		return c.writeTransaction(ctx, "note related conversations' inputs", func(tx *sql.Tx) error {
			for key, value := range map[string]string{"peer_links_tool_rowid": fmt.Sprint(maxRow), "peer_links_message_rowid": fmt.Sprint(maxMessage)} {
				if err := setMeta(tx, key, value); err != nil {
					return err
				}
			}
			return nil
		})
	}
	var copies, tasks []peerLink
	if contentDirty {
		if copies, err = c.derivePeerCopies(ctx, families); err != nil {
			return err
		}
		if tasks, err = c.derivePeerTL1(ctx, families); err != nil {
			return err
		}
	}
	var reads []peerLink
	var readers []string
	if readsDirty {
		if reads, readers, err = c.derivePeerReads(ctx, families, scope); err != nil {
			return err
		}
	}
	return c.writeTransaction(ctx, "store related conversations", func(tx *sql.Tx) error {
		if full {
			if _, err := tx.ExecContext(ctx, `DELETE FROM work_peer_links`); err != nil {
				return err
			}
		} else {
			// Work that joined another family (a new mirror or launcher) is no
			// longer a root; its links went with it.
			seen := map[string]bool{}
			roots := []string{}
			for _, root := range families.root {
				if !seen[root] {
					seen[root] = true
					roots = append(roots, root)
				}
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM work_peer_links WHERE left_workspace_id NOT IN (SELECT value FROM json_each(?1)) OR right_workspace_id NOT IN (SELECT value FROM json_each(?1))`, jsonText(roots)); err != nil {
				return err
			}
		}
		if contentDirty && !full {
			if _, err := tx.ExecContext(ctx, `DELETE FROM work_peer_links WHERE kind IN ('copied_into','tl1_candidate')`); err != nil {
				return err
			}
		}
		if readsDirty && !full {
			// A family the evidence arrived in may no longer be a reader at
			// all: its only call that showed changes was removed, or a replay
			// classified it as something else.
			redone := slices.Clone(readers)
			for root := range scope.roots {
				if root != "" {
					redone = append(redone, root)
				}
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM work_peer_links WHERE kind='read_written_by' AND right_workspace_id IN (SELECT value FROM json_each(?))`, jsonText(redone)); err != nil {
				return err
			}
		}
		insert, err := tx.PrepareContext(ctx, `INSERT INTO work_peer_links(left_workspace_id,right_workspace_id,kind,confidence,left_message_id,right_message_id,at,evidence_json)
 SELECT ?,?,?,?,?,?,?,? WHERE EXISTS(SELECT 1 FROM workspaces WHERE id=?) AND EXISTS(SELECT 1 FROM workspaces WHERE id=?)
 ON CONFLICT(left_workspace_id,right_workspace_id,kind,left_message_id,right_message_id) DO UPDATE SET confidence=excluded.confidence,at=excluded.at,evidence_json=excluded.evidence_json`)
		if err != nil {
			return err
		}
		defer insert.Close()
		for _, group := range [][]peerLink{copies, tasks, reads} {
			for _, l := range group {
				if _, err := insert.ExecContext(ctx, l.left, l.right, l.kind, l.confidence, l.leftMessage, l.rightMessage, nullIfEmpty(l.at), jsonText(l.evidence), l.left, l.right); err != nil {
					return err
				}
			}
		}
		// A call that changed while this ran gave its conversation another
		// revision, which stays for the next run.
		if len(changed) > 0 {
			seen := make([][2]string, len(changed))
			for i, change := range changed {
				seen[i] = [2]string{change.conversation, change.revision}
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM work_peer_links_changed WHERE (conversation_id,revision) IN (SELECT j.value->>0,j.value->>1 FROM json_each(?) j)`, jsonText(seen)); err != nil {
				return err
			}
		}
		if revision.Valid {
			if _, err := tx.ExecContext(ctx, `DELETE FROM work_peer_links_dirty WHERE revision=?`, revision.String); err != nil {
				return err
			}
		}
		for key, value := range map[string]string{"peer_links_version": want, "peer_links_tool_rowid": fmt.Sprint(maxRow), "peer_links_message_rowid": fmt.Sprint(maxMessage), "peer_links_authorship": authorship, "peer_links_tl1": tl1, "peer_links_built_at": now()} {
			if err := setMeta(tx, key, value); err != nil {
				return err
			}
		}
		return nil
	})
}

func nullIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// derivePeerCopies turns copied agent output into links from the quoted
// conversation to the one that quoted it, one per pair of messages.
func (c *Catalog) derivePeerCopies(ctx context.Context, f *peerFamilies) ([]peerLink, error) {
	// Only quoted agent output is a hand-off: a resent prompt is the person
	// reusing their own words, and launch prompts and forms are generated.
	rows, err := c.reader().QueryContext(ctx, `SELECT s.source_conversation_id,COALESCE(s.source_message_id,''),m.conversation_id,m.id,COALESCE(m.created_at,''),SUM(s.words),
 COALESCE((SELECT created_at FROM messages WHERE id=s.source_message_id),'')
 FROM message_authorship_spans s JOIN messages m ON m.id=s.message_id
 WHERE s.source_conversation_id IS NOT NULL AND s.source_conversation_id<>'' AND s.category='quoted'
 GROUP BY s.source_conversation_id,s.source_message_id,m.id HAVING SUM(s.words)>=?`, peerMinCopyWords)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	links := []peerLink{}
	for rows.Next() {
		var sourceConv, sourceMessage, conv, message, at, sourceAt string
		var words int
		if err := rows.Scan(&sourceConv, &sourceMessage, &conv, &message, &at, &words, &sourceAt); err != nil {
			return nil, err
		}
		left, right := f.root[sourceConv], f.root[conv]
		if left == "" || right == "" || left == right {
			continue
		}
		// Measured against Conductor workspaces and checkouts: a reply of 40
		// or more words quoted within a day is the same work 0.89 of the
		// time; shorter quotes 0.6; quotes after a day 0.19.
		confidence := peerLow
		evidence := map[string]any{"words": words}
		if from, ok := parseTime(sourceAt); ok {
			to, ok := parseTime(at)
			if !ok || to.Sub(from) > peerCopyGap {
				continue
			}
			evidence["minutes"] = int(to.Sub(from).Minutes())
			if words >= peerCopyWords {
				confidence = peerHigh
			}
		}
		links = append(links, peerLink{left: left, right: right, kind: "copied_into", confidence: confidence,
			leftMessage: sourceMessage, rightMessage: message, at: at, evidence: evidence})
	}
	return links, rows.Err()
}

func (c *Catalog) peerTL1Signature(ctx context.Context) (string, error) {
	var count int
	var latest sql.NullString
	err := c.reader().QueryRowContext(ctx, `SELECT COUNT(*),MAX(COALESCE(updated_at,created_at)) FROM tl1_tasks WHERE COALESCE(candidate_id,'')<>''`).Scan(&count, &latest)
	return fmt.Sprintf("%d/%s", count, latest.String), err
}

// derivePeerTL1 links TL1 tasks that worked on one candidate, each to the
// candidate's first task, so every task's page reaches the others within the
// two hops a page follows.
func (c *Catalog) derivePeerTL1(ctx context.Context, f *peerFamilies) ([]peerLink, error) {
	rows, err := c.reader().QueryContext(ctx, `SELECT t.installation_id,t.candidate_id,t.workspace_id,COALESCE(t.created_at,'') FROM tl1_tasks t
 WHERE COALESCE(t.candidate_id,'')<>'' AND t.workspace_id IS NOT NULL ORDER BY t.installation_id,t.candidate_id,t.created_at,t.task_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// A TL1 task's workspace holds its attempts' top-level conversations.
	rootOf := func(workspace string) string {
		for _, cv := range f.top[workspace] {
			return f.root[cv]
		}
		return workspace
	}
	links := []peerLink{}
	key, first, seen := "", "", map[string]bool{}
	for rows.Next() {
		var installation, candidate, workspace, at string
		if err := rows.Scan(&installation, &candidate, &workspace, &at); err != nil {
			return nil, err
		}
		if next := installation + "\x00" + candidate; next != key {
			key, first, seen = next, "", map[string]bool{}
		}
		root := rootOf(workspace)
		if first == "" {
			first = root
		} else if root != first && !seen[root] {
			links = append(links, peerLink{left: first, right: root, kind: "tl1_candidate", confidence: peerHigh, at: at,
				evidence: map[string]any{"candidate": candidate}})
		}
		seen[root] = true
	}
	return links, rows.Err()
}

// peerWrite is one edit call's written lines.
type peerWrite struct {
	root, message string
	at            time.Time
}

type peerRead struct {
	message string
	at      time.Time
	diff    bool
}

// peerEvidence is the span of one conversation's new calls or turns.
type peerEvidence struct {
	conversation string
	from, to     time.Time
}

func (c *Catalog) peerEvidence(ctx context.Context, query string, after int64) ([]peerEvidence, error) {
	rows, err := c.reader().QueryContext(ctx, query, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	evidence := []peerEvidence{}
	for rows.Next() {
		var conversation, first, last string
		if err := rows.Scan(&conversation, &first, &last); err != nil {
			return nil, err
		}
		from, ok := parseTime(first)
		to, also := parseTime(last)
		if ok && also {
			evidence = append(evidence, peerEvidence{conversation, from, to})
		}
	}
	return evidence, rows.Err()
}

// peerChange is a conversation whose calls changed in place, with the span of
// their starts and their workspace. No link is derived from a call whose
// start cannot be read as a time, so a span such starts hide has no ends.
type peerChange struct {
	conversation, revision, workspace string
	from, to                          *time.Time
}

func (c *Catalog) peerChanges(ctx context.Context) ([]peerChange, error) {
	rows, err := c.reader().QueryContext(ctx, `SELECT conversation_id,first,last,revision,COALESCE(workspace_id,'') FROM work_peer_links_changed ORDER BY conversation_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	changes := []peerChange{}
	for rows.Next() {
		var first, last string
		change := peerChange{}
		if err := rows.Scan(&change.conversation, &first, &last, &change.revision, &change.workspace); err != nil {
			return nil, err
		}
		// The ends are the least and greatest start as text. When only one
		// reads as a time, it is the one known start.
		if from, ok := parseTime(first); ok {
			change.from, change.to = &from, &from
		}
		if to, ok := parseTime(last); ok {
			if change.to = &to; change.from == nil {
				change.from = &to
			}
		}
		changes = append(changes, change)
	}
	return changes, rows.Err()
}

// peerScope is the readers new evidence can have changed: the families it
// arrived in, and those that read while it could count.
type peerScope struct {
	roots map[string]bool
	// spans are merged; a nil end is open (what an earlier build noted of a
	// call changed in place is one time for the library).
	spans []peerSpan
}

type peerSpan struct {
	from time.Time
	to   *time.Time
}

// peerReadScope turns what arrived since the last run into the readers to
// derive again, or nil when no read link can have changed.
//
// A new call or turn at t concerns its own family, and any reader that read
// between t-peerRecheck (the writer's next prompt confirms a review that
// came before it) and t+peerWindow (lines count for that long after they
// are written). A turn concerns anyone only in a family's top-level
// conversation, where timing is read, and only if that family has a call
// that writes or shows changes: without one it is neither side of a link.
//
// A call changed in place, or deleted, concerns the same readers as a new
// one at its start. When its conversation is gone too, its family is found
// by what remains: the families of its workspace, and any reader whose link
// is anchored on a message that no longer exists.
func (c *Catalog) peerReadScope(ctx context.Context, f *peerFamilies, calls, turns []peerEvidence, changed []peerChange, late sql.NullString) (*peerScope, error) {
	scope := &peerScope{roots: map[string]bool{}}
	add := func(e peerEvidence) {
		to := e.to.Add(peerWindow)
		scope.roots[f.root[e.conversation]] = true
		scope.spans = append(scope.spans, peerSpan{e.from.Add(-peerRecheck), &to})
	}
	for _, e := range calls {
		if f.root[e.conversation] != "" {
			add(e)
		}
	}
	top := map[string]bool{}
	for _, list := range f.top {
		for _, cv := range list {
			top[cv] = true
		}
	}
	spoke := map[string]bool{}
	for _, e := range turns {
		if root := f.root[e.conversation]; root != "" && top[e.conversation] {
			spoke[root] = true
		}
	}
	if len(spoke) > 0 {
		members := []string{}
		for cv, root := range f.root {
			if spoke[root] {
				members = append(members, cv)
			}
		}
		sort.Strings(members)
		involved, err := queryStrings(ctx, c.reader(), peerLinkableQuery, jsonText(members))
		if err != nil {
			return nil, err
		}
		linkable := map[string]bool{}
		for _, cv := range involved {
			linkable[f.root[cv]] = true
		}
		for _, e := range turns {
			if top[e.conversation] && linkable[f.root[e.conversation]] {
				add(e)
			}
		}
	}
	gone := false
	for _, change := range changed {
		if root := f.root[change.conversation]; root != "" {
			scope.roots[root] = true
		} else {
			gone = true
			if change.workspace != "" {
				scope.roots[change.workspace] = true
				for root := range f.within[change.workspace] {
					scope.roots[root] = true
				}
			}
		}
		if change.from != nil {
			to := change.to.Add(peerWindow)
			scope.spans = append(scope.spans, peerSpan{change.from.Add(-peerRecheck), &to})
		}
	}
	if gone {
		orphaned, err := queryStrings(ctx, c.reader(), `SELECT DISTINCT l.right_workspace_id FROM work_peer_links l WHERE l.kind='read_written_by'
 AND (l.right_message_id<>'' AND NOT EXISTS(SELECT 1 FROM messages m WHERE m.id=l.right_message_id)
  OR l.left_message_id<>'' AND NOT EXISTS(SELECT 1 FROM messages m WHERE m.id=l.left_message_id))`)
		if err != nil {
			return nil, err
		}
		for _, root := range orphaned {
			scope.roots[root] = true
		}
	}
	if at, ok := parseTime(late.String); ok && late.Valid {
		scope.spans = append(scope.spans, peerSpan{from: at.Add(-peerRecheck)})
	}
	if len(scope.spans) == 0 {
		if len(scope.roots) == 0 {
			return nil, nil
		}
		return scope, nil
	}
	sort.Slice(scope.spans, func(i, j int) bool { return scope.spans[i].from.Before(scope.spans[j].from) })
	merged := scope.spans[:1]
	for _, span := range scope.spans[1:] {
		last := &merged[len(merged)-1]
		switch {
		case last.to == nil:
		case span.from.After(*last.to):
			merged = append(merged, span)
		case span.to == nil || span.to.After(*last.to):
			last.to = span.to
		}
	}
	scope.spans = merged
	return scope, nil
}

// peerScopeSince is the scope of everything read at or after from.
func peerScopeSince(from time.Time) *peerScope {
	return &peerScope{roots: map[string]bool{}, spans: []peerSpan{{from: from}}}
}

// derivePeerReads finds, for each conversation that looked at changes (ran
// a diff), the work whose freshly written lines it read. With a scope, only
// its readers are redone, together with everything they read.
func (c *Catalog) derivePeerReads(ctx context.Context, f *peerFamilies, scope *peerScope) ([]peerLink, []string, error) {
	q := c.reader()
	// Readers: families with a call that shows changes.
	readerQuery := peerReadersQuery
	args := []any{}
	if scope != nil {
		// Candidates read or looked at changes within a span, or are the
		// families the evidence arrived in; of them, readers have a diff.
		candidates := map[string]bool{}
		for root := range scope.roots {
			candidates[root] = true
		}
		for _, span := range scope.spans {
			query, bounds := peerReadSinceQuery, []any{formatTime(span.from)}
			if span.to != nil {
				query, bounds = query+peerReadUntil, append(bounds, formatTime(*span.to))
			}
			convs, err := queryStrings(ctx, q, query, bounds...)
			if err != nil {
				return nil, nil, err
			}
			for _, cv := range convs {
				candidates[f.root[cv]] = true
			}
		}
		members := []string{}
		for cv, root := range f.root {
			if root != "" && candidates[root] {
				members = append(members, cv)
			}
		}
		sort.Strings(members)
		readerQuery = peerReadersAmongQuery
		args = append(args, jsonText(members))
	}
	readerConvs, err := queryStrings(ctx, q, readerQuery, args...)
	if err != nil {
		return nil, nil, err
	}
	readerSet := map[string]bool{}
	for _, cv := range readerConvs {
		if root := f.root[cv]; root != "" {
			readerSet[root] = true
		}
	}
	readers := make([]string, 0, len(readerSet))
	for root := range readerSet {
		readers = append(readers, root)
	}
	sort.Strings(readers)
	members := map[string][]string{}
	for cv, root := range f.root {
		if readerSet[root] {
			members[root] = append(members[root], cv)
		}
	}
	// Each reader's diff and file-read calls, oldest first.
	type readerReads struct {
		root  string
		reads []peerRead
	}
	all := []readerReads{}
	for _, root := range readers {
		convs := members[root]
		sort.Strings(convs)
		rows, err := q.QueryContext(ctx, peerReadsQuery, jsonText(convs))
		if err != nil {
			return nil, nil, err
		}
		r := readerReads{root: root}
		for rows.Next() {
			var message, at string
			var diff bool
			if err := rows.Scan(&message, &at, &diff); err != nil {
				rows.Close()
				return nil, nil, err
			}
			if t, ok := parseTime(at); ok {
				r.reads = append(r.reads, peerRead{message: message, at: t, diff: diff})
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, nil, err
		}
		if len(r.reads) > 0 {
			all = append(all, r)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].reads[0].at.Before(all[j].reads[0].at) })
	index := newPeerWriteIndex(c, f)
	clock := &peerClock{c: c, f: f, turns: map[string]*peerTurns{}}
	text, err := q.PrepareContext(ctx, `SELECT text FROM messages WHERE id=?`)
	if err != nil {
		return nil, nil, err
	}
	defer text.Close()
	links := []peerLink{}
	for i, r := range all {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		reportWork(ctx, "linking related conversations", "conversations", i, len(all))
		first, last := r.reads[0].at, r.reads[len(r.reads)-1].at
		if err := index.cover(ctx, first.Add(-peerWindow), last); err != nil {
			return nil, nil, err
		}
		index.evictBefore(first.Add(-peerWindow))
		link, err := c.peerReadLink(ctx, text, index, clock, f, r.root, r.reads)
		if err != nil {
			return nil, nil, err
		}
		if link != nil {
			links = append(links, *link)
		}
	}
	// The loop reports each reader as it starts; end on all of them done.
	reportWork(ctx, "linking related conversations", "conversations", len(all), len(all))
	return links, readers, nil
}

// peerReadLink picks the writer whose lines a reader read, if one stands out.
func (c *Catalog) peerReadLink(ctx context.Context, text *sql.Stmt, index *peerWriteIndex, clock *peerClock, f *peerFamilies, reader string, reads []peerRead) (*peerLink, error) {
	type tally struct {
		added, shown     int
		write            *peerWrite
		readMsg, diffMsg string
		firstRead        time.Time
	}
	tallies := map[string]*tally{}
	// A line counts once as shown and once as added: one first seen in a
	// file read still counts when a later diff adds it.
	seen, seenAdded := map[uint64]bool{}, map[uint64]bool{}
	for _, read := range reads {
		var body string
		if err := text.QueryRowContext(ctx, read.message).Scan(&body); err != nil {
			if err == sql.ErrNoRows {
				continue
			}
			return nil, err
		}
		shown, added := map[uint64]bool{}, map[uint64]bool{}
		if read.diff {
			readPeerLines(body, shown, added)
		} else {
			readPeerLines(body, shown, nil)
		}
		for hash := range shown {
			newShown, newAdded := !seen[hash], added[hash] && !seenAdded[hash]
			if !newShown && !newAdded {
				continue
			}
			seen[hash] = true
			if newAdded {
				seenAdded[hash] = true
			}
			write := index.uniqueWriter(hash, read.at, reader)
			if write == nil {
				continue
			}
			writer := write.root
			t := tallies[writer]
			if t == nil {
				t = &tally{write: write, readMsg: read.message, firstRead: read.at}
				tallies[writer] = t
			} else if read.message == t.readMsg && t.write.before(write) {
				// The lines of a read come in no order: of those the first
				// read showed, anchor on the writer's latest write, so a
				// rescan stores the same link every time.
				t.write = write
			}
			if newShown {
				t.shown++
			}
			if newAdded {
				if t.diffMsg == "" {
					t.diffMsg = read.message
				}
				t.added++
			}
		}
	}
	rank := func(score func(*tally) int) (string, int, int) {
		writers := make([]string, 0, len(tallies))
		for writer := range tallies {
			writers = append(writers, writer)
		}
		sort.Slice(writers, func(i, j int) bool {
			a, b := score(tallies[writers[i]]), score(tallies[writers[j]])
			return a > b || a == b && writers[i] < writers[j]
		})
		if len(writers) == 0 {
			return "", 0, 0
		}
		second := 0
		if len(writers) > 1 {
			second = score(tallies[writers[1]])
		}
		return writers[0], score(tallies[writers[0]]), second
	}
	basis := ""
	writer, top, second := rank(func(t *tally) int { return t.added })
	if top >= peerMinLines && top > second {
		basis = "diff"
	} else if writer, top, second = rank(func(t *tally) int { return t.shown }); top >= peerMinLines && top >= peerRatio*second {
		basis = "read"
	} else {
		return nil, nil
	}
	t := tallies[writer]
	timing, err := clock.timing(ctx, writer, reader)
	if err != nil {
		return nil, err
	}
	own := index.writtenBy(reader, t.firstRead)
	// Measured on a full library against Conductor workspaces and TL1 work
	// items (peer_links_probe_test.go): a diff followed by the writer's next
	// prompt is the same work 0.97 of the time; begun as the writer's turn
	// ended, by a reader that wrote nothing itself and with a clear lead,
	// 0.87. Without timing, or for lines only read, it is near a coin flip,
	// and file reads without timing are almost never related.
	reviewer := own < peerReaderOwnLines
	lead := top >= peerRatio*second
	confidence := ""
	switch {
	case basis == "diff" && (timing == "return" || timing == "start" && reviewer && lead):
		confidence = peerHigh
	case basis == "diff" && timing == "start" && reviewer:
		// A close runner-up: 0.79.
		confidence = peerLow
	case !lead:
	case basis == "diff" && (reviewer || timing != ""):
		confidence = peerLow
	case basis == "read" && reviewer && timing != "":
		confidence = peerLow
	}
	if confidence == "" {
		return nil, nil
	}
	// A diff link is anchored on the diff that showed the lines.
	readMsg := t.readMsg
	if basis == "diff" {
		readMsg = t.diffMsg
	}
	return &peerLink{left: writer, right: reader, kind: "read_written_by", confidence: confidence,
		leftMessage: t.write.message, rightMessage: readMsg, at: formatTime(t.firstRead),
		evidence: map[string]any{"basis": basis, "lines": top, "runner_up": second, "timing": timing, "reader_lines": own}}, nil
}

// peerClock holds each family's prompt and reply times for one derivation,
// read once per family: a busy implementer is timed against every reader
// that saw its lines, and reading its messages again for each was most of a
// full pass.
type peerClock struct {
	c     *Catalog
	f     *peerFamilies
	turns map[string]*peerTurns
}

// peerTurns are a family's top-level prompt and reply times, ascending.
type peerTurns struct{ prompts, replies []time.Time }

func (k *peerClock) of(ctx context.Context, root string) (*peerTurns, error) {
	if t := k.turns[root]; t != nil {
		return t, nil
	}
	// The unary + keeps SQLite on the conversation index; the prose index
	// would scan every prompt in the library.
	rows, err := k.c.reader().QueryContext(ctx, `SELECT role,created_at FROM messages
 WHERE conversation_id IN (SELECT value FROM json_each(?)) AND +kind='message' AND +role IN ('user','assistant') AND created_at IS NOT NULL`, jsonText(k.f.top[root]))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	t := &peerTurns{}
	for rows.Next() {
		var role, at string
		if err := rows.Scan(&role, &at); err != nil {
			return nil, err
		}
		if when, ok := parseTime(at); ok {
			if role == "user" {
				t.prompts = append(t.prompts, when)
			} else {
				t.replies = append(t.replies, when)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, list := range [][]time.Time{t.prompts, t.replies} {
		sort.Slice(list, func(i, j int) bool { return list[i].Before(list[j]) })
	}
	k.turns[root] = t
	return t, nil
}

// timing reports whether the reader's span lines up with the writer's turns:
// "start" when the reader began within peerTimingGap of the writer's last
// reply, with no writer prompt between; "return" when the writer's next
// prompt came within peerTimingGap of the reader's last reply.
func (k *peerClock) timing(ctx context.Context, writer, reader string) (string, error) {
	w, err := k.of(ctx, writer)
	if err != nil {
		return "", err
	}
	r, err := k.of(ctx, reader)
	if err != nil {
		return "", err
	}
	if len(r.prompts) > 0 {
		start := r.prompts[0]
		if i := sort.Search(len(w.replies), func(i int) bool { return w.replies[i].After(start) }) - 1; i >= 0 {
			end := w.replies[i]
			j := sort.Search(len(w.prompts), func(j int) bool { return w.prompts[j].After(end) })
			if (j == len(w.prompts) || w.prompts[j].After(start)) && start.Sub(end) <= peerTimingGap {
				return "start", nil
			}
		}
	}
	if len(r.replies) > 0 {
		last := r.replies[len(r.replies)-1]
		if j := sort.Search(len(w.prompts), func(j int) bool { return !w.prompts[j].Before(last) }); j < len(w.prompts) && w.prompts[j].Sub(last) <= peerTimingGap {
			return "return", nil
		}
	}
	return "", nil
}

// peerWriteIndex holds the lines written in a sliding time window, loaded a
// day at a time, so a reader looks up only writes from the week before it.
type peerWriteIndex struct {
	c       *Catalog
	f       *peerFamilies
	days    map[int64][]peerWriteLines
	lines   map[uint64][]*peerWrite
	through int64 // last loaded day
}

type peerWriteLines struct {
	write  *peerWrite
	hashes []uint64
}

func newPeerWriteIndex(c *Catalog, f *peerFamilies) *peerWriteIndex {
	return &peerWriteIndex{c: c, f: f, days: map[int64][]peerWriteLines{}, lines: map[uint64][]*peerWrite{}, through: -1 << 62}
}

func peerDay(t time.Time) int64 { return t.UTC().Unix() / 86400 }

// cover loads every day from start to end not yet loaded.
func (x *peerWriteIndex) cover(ctx context.Context, start, end time.Time) error {
	first, last := peerDay(start), peerDay(end)
	if first <= x.through {
		first = x.through + 1
	}
	for day := first; day <= last; day++ {
		if err := x.load(ctx, day); err != nil {
			return err
		}
		x.through = day
	}
	return nil
}

func (x *peerWriteIndex) load(ctx context.Context, day int64) error {
	from, to := time.Unix(day*86400, 0), time.Unix((day+1)*86400, 0)
	rows, err := x.c.reader().QueryContext(ctx, peerWritesQuery, formatTime(from), formatTime(to))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cv, message, at, body string
		if err := rows.Scan(&cv, &message, &at, &body); err != nil {
			return err
		}
		root := x.f.root[cv]
		when, ok := parseTime(at)
		if root == "" || !ok {
			continue
		}
		written := map[uint64]bool{}
		writtenPeerLines(body, written)
		if len(written) == 0 {
			continue
		}
		w := &peerWrite{root: root, message: message, at: when}
		entry := peerWriteLines{write: w, hashes: make([]uint64, 0, len(written))}
		for hash := range written {
			entry.hashes = append(entry.hashes, hash)
			x.lines[hash] = append(x.lines[hash], w)
		}
		x.days[day] = append(x.days[day], entry)
	}
	return rows.Err()
}

// evictBefore drops days entirely before t.
func (x *peerWriteIndex) evictBefore(t time.Time) {
	limit := peerDay(t)
	for day, entries := range x.days {
		if day >= limit {
			continue
		}
		for _, entry := range entries {
			for _, hash := range entry.hashes {
				kept := x.lines[hash][:0]
				for _, w := range x.lines[hash] {
					if w != entry.write {
						kept = append(kept, w)
					}
				}
				if len(kept) == 0 {
					delete(x.lines, hash)
				} else {
					x.lines[hash] = kept
				}
			}
		}
		delete(x.days, day)
	}
}

// before orders writes for choosing an anchor: by time, and by message for
// writes at one time, so the choice never depends on the order they are met.
func (w *peerWrite) before(other *peerWrite) bool {
	return other.at.After(w.at) || other.at.Equal(w.at) && other.message > w.message
}

// uniqueWriter returns the latest write of a line by the one other family
// that wrote it in the window before at, or nil. A line several families
// wrote is shared code, not evidence.
func (x *peerWriteIndex) uniqueWriter(hash uint64, at time.Time, reader string) *peerWrite {
	var chosen *peerWrite
	for _, w := range x.lines[hash] {
		if w.at.After(at) || w.at.Before(at.Add(-peerWindow)) {
			continue
		}
		if w.root == reader {
			// The reader wrote it too: its own work, not another's.
			return nil
		}
		if chosen != nil && chosen.root != w.root {
			return nil
		}
		// Anchor on the latest write of the line before the read.
		if chosen == nil || chosen.before(w) {
			chosen = w
		}
	}
	return chosen
}

// writtenBy counts the lines a family wrote in the window before t.
func (x *peerWriteIndex) writtenBy(root string, t time.Time) int {
	count := 0
	for _, entries := range x.days {
		for _, entry := range entries {
			if entry.write.root == root && entry.write.at.Before(t) && !entry.write.at.Before(t.Add(-peerWindow)) {
				count += len(entry.hashes)
			}
		}
	}
	return count
}

func queryStrings(ctx context.Context, q queryer, query string, args ...any) ([]string, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, rows.Err()
}
