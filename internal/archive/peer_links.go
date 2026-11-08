package archive

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
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
const peerLinksVersion = "peers-v1"

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
	// peerMaxPeers caps the related work a page shows.
	peerMaxPeers = 24
)

const (
	peerHigh = "high"
	peerLow  = "low"
)

// peerDiffCall selects ledger calls that show changes: their added lines are
// unmerged work.
const peerDiffCall = `t.program IN ('git','gh','jj') AND t.subcommand IN ('diff','show','pr')`

// peerFamilies maps conversations to the representative workspace of their
// family's top-level conversation.
type peerFamilies struct {
	root map[string]string   // conversation → root workspace
	top  map[string][]string // root workspace → its top-level conversations
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
	f := &peerFamilies{root: map[string]string{}, top: map[string][]string{}}
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
	var since sql.NullString
	if err := c.reader().QueryRowContext(ctx, `SELECT COALESCE(MAX(rowid),0),MIN(started_at) FROM tool_calls WHERE rowid>?`, lastRow).Scan(&maxRow, &since); err != nil {
		return err
	}
	if maxRow < lastRow {
		maxRow = lastRow
	}
	readsDirty := full || since.Valid
	contentDirty := full || builtAuthorship != authorship || builtTL1 != tl1
	if !readsDirty && !contentDirty {
		return nil
	}
	reportWork(ctx, "linking related conversations", "", 0, -1)
	families, err := c.loadPeerFamilies(ctx, c.reader())
	if err != nil {
		return err
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
		// Recheck readers that read shortly before the new calls as well:
		// the writer's next prompt, which confirms a review, comes after it.
		from := ""
		if !full && since.Valid {
			from = since.String
			if at, ok := parseTime(from); ok {
				from = formatTime(at.Add(-peerRecheck))
			}
		}
		if reads, readers, err = c.derivePeerReads(ctx, families, from); err != nil {
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
		if readsDirty && !full && len(readers) > 0 {
			if _, err := tx.ExecContext(ctx, `DELETE FROM work_peer_links WHERE kind='read_written_by' AND right_workspace_id IN (SELECT value FROM json_each(?))`, jsonText(readers)); err != nil {
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
		for key, value := range map[string]string{"peer_links_version": want, "peer_links_tool_rowid": fmt.Sprint(maxRow), "peer_links_authorship": authorship, "peer_links_tl1": tl1, "peer_links_built_at": now()} {
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
	rows, err := c.reader().QueryContext(ctx, `SELECT s.source_conversation_id,COALESCE(s.source_message_id,''),m.conversation_id,m.id,COALESCE(m.created_at,''),SUM(s.words)
 FROM message_authorship_spans s JOIN messages m ON m.id=s.message_id
 WHERE s.source_conversation_id IS NOT NULL AND s.source_conversation_id<>'' AND s.category='quoted'
 GROUP BY s.source_conversation_id,s.source_message_id,m.id HAVING SUM(s.words)>=?`, peerMinCopyWords)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	links := []peerLink{}
	for rows.Next() {
		var sourceConv, sourceMessage, conv, message, at string
		var words int
		if err := rows.Scan(&sourceConv, &sourceMessage, &conv, &message, &at, &words); err != nil {
			return nil, err
		}
		left, right := f.root[sourceConv], f.root[conv]
		if left == "" || right == "" || left == right {
			continue
		}
		links = append(links, peerLink{left: left, right: right, kind: "copied_into", confidence: peerHigh,
			leftMessage: sourceMessage, rightMessage: message, at: at, evidence: map[string]any{"words": words}})
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

// derivePeerReads finds, for each conversation that looked at changes (ran
// a diff), the work whose freshly written lines it read. With from set, only
// readers with a diff call at or after from are redone, together with
// everything they read.
func (c *Catalog) derivePeerReads(ctx context.Context, f *peerFamilies, from string) ([]peerLink, []string, error) {
	q := c.reader()
	// Readers: families with a call that shows changes.
	// The unary + lets a later run take the start-time index rather than
	// scan the ledger in conversation order for DISTINCT.
	readerQuery := `SELECT DISTINCT +t.conversation_id FROM tool_calls t WHERE ` + peerDiffCall
	args := []any{}
	if from != "" {
		readerQuery += ` AND t.started_at>=?`
		args = append(args, from)
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
		rows, err := q.QueryContext(ctx, `SELECT t.result_message_id,COALESCE(t.started_at,''),COALESCE(`+peerDiffCall+`,0) FROM tool_calls t
 WHERE t.conversation_id IN (SELECT value FROM json_each(?)) AND t.result_message_id IS NOT NULL AND (`+peerDiffCall+` OR t.tool_category='read')
 ORDER BY t.started_at`, jsonText(convs))
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
	return links, readers, nil
}

// peerReadLink picks the writer whose lines a reader read, if one stands out.
func (c *Catalog) peerReadLink(ctx context.Context, text *sql.Stmt, index *peerWriteIndex, clock *peerClock, f *peerFamilies, reader string, reads []peerRead) (*peerLink, error) {
	type tally struct {
		added, shown      int
		writeMsg, readMsg string
		firstRead         time.Time
	}
	tallies := map[string]*tally{}
	seen := map[uint64]bool{}
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
			if seen[hash] {
				continue
			}
			seen[hash] = true
			writer, message, ok := index.uniqueWriter(hash, read.at, reader)
			if !ok {
				continue
			}
			t := tallies[writer]
			if t == nil {
				t = &tally{writeMsg: message, readMsg: read.message, firstRead: read.at}
				tallies[writer] = t
			}
			t.shown++
			if added[hash] {
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
	// Measured against Conductor workspaces on a full library
	// (peer_links_probe_test.go): a diff with the writer's next prompt
	// following, or begun as the writer's turn ended by a reader that had
	// written nothing itself, is the same work 0.87 of the time. Without
	// timing, or for lines only read, it is near a coin flip at best, and
	// file reads without timing are almost never related.
	reviewer := own < peerReaderOwnLines
	lead := top >= peerRatio*second
	confidence := ""
	switch {
	case basis == "diff" && (timing == "return" || timing == "start" && reviewer):
		confidence = peerHigh
	case !lead:
	case basis == "diff" && (reviewer || timing != ""):
		confidence = peerLow
	case basis == "read" && reviewer && timing != "":
		confidence = peerLow
	}
	if confidence == "" {
		return nil, nil
	}
	return &peerLink{left: writer, right: reader, kind: "read_written_by", confidence: confidence,
		leftMessage: t.writeMsg, rightMessage: t.readMsg, at: formatTime(t.firstRead),
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
	rows, err := x.c.reader().QueryContext(ctx, `SELECT t.conversation_id,COALESCE(t.call_message_id,''),t.started_at,m.text FROM tool_calls t
 JOIN messages m ON m.id=t.call_message_id
 WHERE t.started_at>=? AND t.started_at<? AND (t.tool_category='edit' OR t.has_heredoc=1)`, formatTime(from), formatTime(to))
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

// uniqueWriter returns the one other family that wrote a line in the window
// before at. A line several families wrote is shared code, not evidence.
func (x *peerWriteIndex) uniqueWriter(hash uint64, at time.Time, reader string) (string, string, bool) {
	var chosen *peerWrite
	for _, w := range x.lines[hash] {
		if w.at.After(at) || w.at.Before(at.Add(-peerWindow)) {
			continue
		}
		if w.root == reader {
			// The reader wrote it too: its own work, not another's.
			return "", "", false
		}
		if chosen != nil && chosen.root != w.root {
			return "", "", false
		}
		// Anchor on the latest write of the line before the read.
		if chosen == nil || w.at.After(chosen.at) {
			chosen = w
		}
	}
	if chosen == nil {
		return "", "", false
	}
	return chosen.root, chosen.message, true
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
