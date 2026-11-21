package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"
)

// Saved delegation summaries: what a workspace's own snapshot reads of an
// execution that also has streams in another workspace.
//
// A workspace snapshot reads only its own rows (delegationProjection.
// unqueried). A Codex child archived in its own workspace, or a session
// launched into another Conductor workspace, therefore has nothing to show on
// its parent's page, and computing every child's workspace snapshot when
// that page opens would cost seconds per child. Maintenance instead saves,
// for each workspace that holds such an execution's transcript, the row its
// own snapshot reads (the same metrics, with the same ownership of copied
// history), and a page shows those saved rows for executions it could not
// read. Workspace totals stay each workspace's own; group totals count them.
//
//   - delegation_summary_hosts lists the transcripts that hold such
//     executions. The sweep keeps it current from the execution resolution,
//     at each execution source generation.
//   - Triggers mark a host transcript dirty when what its snapshot reads
//     changes: its messages, tool calls and requests (through session shape's
//     marks), sessions, usage and agent events. Hosts only: other
//     conversations pay one indexed lookup.
//   - refreshDelegationSummariesFor reads the host's workspace snapshot,
//     without saved rows, and replaces that workspace's summaries; a change
//     made meanwhile keeps its transcript dirty.
//
// A page reads summaries as they are and says when one is being updated.

// delegationSummariesVersion names what a summary holds. Changing it clears
// every summary and host, so the sweep finds the hosts again.
const delegationSummariesVersion = "2"

const delegationSummariesSweptKey = "delegation_summaries_swept"

// delegationSummaryWorkspaces bounds how many workspace snapshots one step
// reads: each takes about as long as opening that workspace's page.
var delegationSummaryWorkspaces = 4

// delegationSummary is one execution as one workspace read it.
type delegationSummary struct {
	Self              map[string]delegationMetric `json:"self"`
	AccountedTokens   map[string]delegationMetric `json:"accounted_tokens,omitempty"`
	TokenAttribution  *sessionTokenAttribution    `json:"token_attribution,omitempty"`
	Start             *float64                    `json:"start,omitempty"`
	End               *float64                    `json:"end,omitempty"`
	Usage             string                      `json:"usage_status,omitempty"`
	OccurrencePartial string                      `json:"occurrence_partial,omitempty"`
	EventsPending     bool                        `json:"events_pending,omitempty"`
	EditedFiles       []string                    `json:"edited_files,omitempty"`
	ReadFiles         []string                    `json:"read_files,omitempty"`
	// The union of its tool spans and of its harness waits: what Self +
	// children's tool and background time need, not every call.
	ToolSpans []delegationSpan `json:"tool_spans,omitempty"`
	WaitSpans []delegationSpan `json:"wait_spans,omitempty"`
	Episodes  []delegationSpan `json:"episodes,omitempty"`
}

// delegationActivity is a summarized row's timeline marks: its tool calls
// and request times, read only for the Timeline's lanes (savedActivity).
type delegationActivity struct {
	Tools    []delegationTool `json:"tools,omitempty"`
	Requests []float64        `json:"requests,omitempty"`
}

// delegationSavedRef tells a reader where a row's metrics were read.
type delegationSavedRef struct {
	// Workspaces whose summaries the row shows.
	Workspaces []string `json:"workspaces"`
	// UpdatedAt is the oldest of those summaries' saves (RFC 3339).
	UpdatedAt string `json:"updated_at"`
	// Pending: a transcript a summary reads changed since it was saved.
	Pending bool `json:"pending,omitempty"`
	// Content revisions are private to the snapshot and its Timeline reads.
	revisions map[string]string
}

// delegationSummaryHost marks transcripts that hold summarized executions.
func delegationSummaryHost(conversation string) string {
	return "EXISTS(SELECT 1 FROM delegation_summary_hosts h WHERE h.conversation_id=" + conversation + ")"
}

func delegationSummaryMark(conversation string) string {
	return "INSERT INTO delegation_summary_dirty(conversation_id,revision) SELECT " + conversation + ",hex(randomblob(8)) WHERE " + conversation + ` IS NOT NULL
 ON CONFLICT(conversation_id) DO UPDATE SET revision=excluded.revision;`
}

// ensureDelegationSummaries creates the summary tables and their triggers,
// after the agent-event tables they watch.
func (c *Catalog) ensureDelegationSummaries() error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS delegation_summaries (
  node_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  summary TEXT NOT NULL,
  activity TEXT NOT NULL,
  revision TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL,
  PRIMARY KEY(node_id, workspace_id)) WITHOUT ROWID`,
		`CREATE INDEX IF NOT EXISTS delegation_summaries_workspace_idx ON delegation_summaries(workspace_id)`,
		`CREATE TABLE IF NOT EXISTS delegation_summary_hosts (
  conversation_id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL) WITHOUT ROWID`,
		`CREATE INDEX IF NOT EXISTS delegation_summary_hosts_workspace_idx ON delegation_summary_hosts(workspace_id)`,
		`CREATE TABLE IF NOT EXISTS delegation_summary_dirty (
  conversation_id TEXT PRIMARY KEY,
  revision TEXT NOT NULL) WITHOUT ROWID`,
	}
	// Session shape marks a conversation for every change to its messages,
	// authorship, tool calls and requests; its bulk marks for a new
	// derivation carry an empty revision and are not changes.
	shape := func(event string) string {
		return fmt.Sprintf(`CREATE TRIGGER delegation_summary_shape_%s AFTER %s ON session_shape_dirty
WHEN NEW.revision<>'' AND %s BEGIN %s END`, event, map[string]string{"insert": "INSERT", "update": "UPDATE OF revision"}[event],
			delegationSummaryHost("NEW.conversation_id"), delegationSummaryMark("NEW.conversation_id"))
	}
	byConversation := func(table, event, row string) string {
		return fmt.Sprintf(`CREATE TRIGGER delegation_summary_%s_%s AFTER %s ON %s WHEN %s BEGIN %s END`,
			table, event, map[string]string{"insert": "INSERT", "update": "UPDATE", "delete": "DELETE"}[event], table,
			delegationSummaryHost(row+".conversation_id"), delegationSummaryMark(row+".conversation_id"))
	}
	usage := func(table, event, row string) string {
		conversation := "(SELECT a.conversation_id FROM agent_sessions a WHERE a.id=" + row + ".agent_session_id)"
		return fmt.Sprintf(`CREATE TRIGGER delegation_summary_%s_%s AFTER %s ON %s WHEN %s BEGIN %s END`,
			table, event, map[string]string{"insert": "INSERT", "update": "UPDATE", "delete": "DELETE"}[event], table,
			delegationSummaryHost(conversation), delegationSummaryMark(conversation))
	}
	statements = append(statements, "DROP TRIGGER IF EXISTS delegation_summary_shape_insert", "DROP TRIGGER IF EXISTS delegation_summary_shape_update",
		shape("insert"), shape("update"))
	for _, table := range []string{"agent_sessions", "agent_events"} {
		for _, event := range []string{"insert", "update", "delete"} {
			row := "NEW"
			if event == "delete" {
				row = "OLD"
			}
			statements = append(statements, "DROP TRIGGER IF EXISTS delegation_summary_"+table+"_"+event, byConversation(table, event, row))
		}
	}
	for _, event := range []string{"insert", "update", "delete"} {
		row := "NEW"
		if event == "delete" {
			row = "OLD"
		}
		statements = append(statements, "DROP TRIGGER IF EXISTS delegation_summary_usage_"+event)
		for _, table := range []string{"agent_session_usage", "agent_session_token_evidence"} {
			statements = append(statements, "DROP TRIGGER IF EXISTS delegation_summary_"+table+"_"+event, usage(table, event, row))
		}
		statements = append(statements, "DROP TRIGGER IF EXISTS delegation_summary_attribution_"+event,
			fmt.Sprintf(`CREATE TRIGGER delegation_summary_attribution_%s AFTER %s ON usage_attribution_state BEGIN
 INSERT INTO delegation_summary_dirty SELECT conversation_id,hex(randomblob(8)) FROM delegation_summary_hosts WHERE workspace_id=%s.workspace_id
 ON CONFLICT(conversation_id) DO UPDATE SET revision=excluded.revision; END`, event, strings.ToUpper(event), row))
	}
	// Session shape does not read these tool fields; delegation does.
	columns := "workspace_id,agent_session_id,started_at,ended_at,duration_ms,path_absolute,repo_path,path_repository_id,wait_kind,wait_ended_at,wait_status,wait_events"
	var old, next []string
	for _, column := range strings.Split(columns, ",") {
		old, next = append(old, "OLD."+column), append(next, "NEW."+column)
	}
	statements = append(statements, "DROP TRIGGER IF EXISTS delegation_summary_tool_details",
		`CREATE TRIGGER delegation_summary_tool_details AFTER UPDATE OF `+columns+` ON tool_calls
WHEN (`+strings.Join(old, ",")+`) IS NOT (`+strings.Join(next, ",")+`) BEGIN
 INSERT INTO delegation_summary_dirty SELECT h.conversation_id,hex(randomblob(8)) FROM delegation_summary_hosts h
 WHERE h.conversation_id IN (OLD.conversation_id,NEW.conversation_id)
 ON CONFLICT(conversation_id) DO UPDATE SET revision=excluded.revision; END`)
	tx, err := c.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("%w: %s", err, statement)
		}
	}
	if err := addColumnIfMissing(context.Background(), tx, "delegation_summaries", "revision", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := delegationSummaryEventTriggers(context.Background(), tx); err != nil {
		return err
	}
	var version string
	if err := tx.QueryRow("SELECT value FROM meta WHERE key='delegation_summaries_version'").Scan(&version); err != nil && err != sql.ErrNoRows {
		return err
	}
	if version != delegationSummariesVersion {
		for _, statement := range []string{"DELETE FROM delegation_summaries", "DELETE FROM delegation_summary_hosts", "DELETE FROM delegation_summary_dirty",
			"DELETE FROM meta WHERE key='" + delegationSummariesSweptKey + "'",
			"INSERT INTO meta(key,value) VALUES('delegation_summaries_version','" + delegationSummariesVersion + "') ON CONFLICT(key) DO UPDATE SET value=excluded.value",
		} {
			if _, err := tx.Exec(statement); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// The event ledger is optional and may be created after initialization.
// Freshness changes even when an event rebuild publishes no event rows.
func delegationSummaryEventTriggers(ctx context.Context, tx *sql.Tx) error {
	var present bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE type='table' AND name='delegation_summary_hosts')").Scan(&present); err != nil || !present {
		return err
	}
	for _, table := range []string{"agent_events_dirty", "agent_events_conversations", "agent_events_execution_stale"} {
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE type='table' AND name=?)", table).Scan(&present); err != nil {
			return err
		}
		if !present {
			continue
		}
		for _, event := range []string{"INSERT", "UPDATE", "DELETE"} {
			row := "NEW"
			if event == "DELETE" {
				row = "OLD"
			}
			statement := fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS delegation_summary_%s_%s AFTER %s ON %s WHEN %s BEGIN %s END`,
				table, strings.ToLower(event), event, table, delegationSummaryHost(row+".conversation_id"), delegationSummaryMark(row+".conversation_id"))
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
	}
	return nil
}

// Pricing is replaced independently of transcripts. Reuse the repricing
// projection's affected workspaces, including aliases and historical rates.
func markDelegationSummaryWorkspaces(ctx context.Context, tx *sql.Tx, workspaces []string) error {
	var present bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE type='table' AND name='delegation_summary_hosts')").Scan(&present); err != nil || !present {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO delegation_summary_dirty
 SELECT conversation_id,hex(randomblob(8)) FROM delegation_summary_hosts WHERE workspace_id IN (SELECT value FROM json_each(?))
 ON CONFLICT(conversation_id) DO UPDATE SET revision=excluded.revision`, jsonText(workspaces))
	return err
}

// delegationSummaryHosts are the transcripts, by workspace, of executions
// that also have streams in another workspace: the ones some workspace's
// snapshot leaves unqueried.
func delegationSummaryHosts(res *executionResolution) map[string]string {
	p := delegationProjection{res: res}
	hosts := map[string]string{}
	for _, x := range res.Executions {
		if len(x.Streams) < 2 {
			continue
		}
		cross := false
		for _, ref := range x.Streams {
			if st := res.Stream(ref); st != nil && p.unqueried(x, st.WorkspaceID) {
				cross = true
				break
			}
		}
		if !cross {
			continue
		}
		for _, ref := range x.Streams {
			if st := res.Stream(ref); st != nil && ref.Kind == sourceConversation && st.WorkspaceID != "" {
				hosts[st.ConversationID] = st.WorkspaceID
			}
		}
	}
	return hosts
}

// delegationSummariesSweepVersion is the execution source generation the
// sweep must reach.
func (c *Catalog) delegationSummariesSweepVersion(ctx context.Context) (string, error) {
	generation, tracked, err := executionSourceGeneration(ctx, c.reader())
	if err != nil || !tracked {
		return "", err
	}
	return fmt.Sprint(generation), nil
}

func (c *Catalog) prepareDelegationSummaries(ctx context.Context, _ string) error {
	if !c.delegationSummariesMu.TryLock() {
		return errWorkDomainBusy
	}
	defer c.delegationSummariesMu.Unlock()
	return c.sweepDelegationSummaries(ctx)
}

// sweepDelegationSummaries brings the hosts up to the execution resolution:
// a new host, a host that moved or is no longer one, and a host whose
// execution sources changed since the last sweep are marked dirty.
func (c *Catalog) sweepDelegationSummaries(ctx context.Context) error {
	tx, err := c.reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	generation, tracked, err := executionSourceGeneration(ctx, tx)
	if err != nil || !tracked {
		_ = tx.Rollback()
		return err
	}
	var swept sql.NullInt64
	if err := tx.QueryRowContext(ctx, "SELECT CAST(value AS INTEGER) FROM meta WHERE key=?", delegationSummariesSweptKey).Scan(&swept); err != nil && err != sql.ErrNoRows {
		_ = tx.Rollback()
		return err
	}
	if swept.Valid && swept.Int64 >= generation {
		_ = tx.Rollback()
		return nil
	}
	res, err := c.executionResolutionAt(ctx, tx)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	want := delegationSummaryHosts(res)
	stored := map[string]string{}
	rows, err := queryMapsContext(ctx, tx, "SELECT conversation_id,workspace_id FROM delegation_summary_hosts")
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	for _, row := range rows {
		stored[firstString(row["conversation_id"])] = firstString(row["workspace_id"])
	}
	var changed []string
	if swept.Valid {
		if changed, err = executionChangedSince(ctx, tx, swept.Int64); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	_ = tx.Rollback()
	dirty := map[string]bool{}
	retired := map[string]bool{}
	for cid, ws := range want {
		if stored[cid] != ws {
			dirty[cid] = true
		}
	}
	for cid, ws := range stored {
		if want[cid] != ws {
			retired[ws] = true
		}
		if _, ok := want[cid]; !ok {
			dirty[cid] = true
		}
	}
	// A move or removal invalidates the old workspace's projection. Remove
	// it atomically with the host mapping, and rebuild its remaining hosts.
	for cid, ws := range want {
		if retired[ws] {
			dirty[cid] = true
		}
	}
	for _, cid := range changed {
		if _, ok := want[cid]; ok {
			dirty[cid] = true
		}
	}
	return c.writeTransaction(ctx, "delegation-summaries", func(tx *sql.Tx) error {
		for _, ws := range slices.Sorted(maps.Keys(retired)) {
			if _, err := tx.ExecContext(ctx, "DELETE FROM delegation_summaries WHERE workspace_id=?", ws); err != nil {
				return err
			}
		}
		for cid := range stored {
			if _, ok := want[cid]; !ok {
				if _, err := tx.ExecContext(ctx, "DELETE FROM delegation_summary_hosts WHERE conversation_id=?", cid); err != nil {
					return err
				}
			}
		}
		for _, cid := range slices.Sorted(maps.Keys(want)) {
			if stored[cid] == want[cid] {
				continue
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO delegation_summary_hosts(conversation_id,workspace_id) VALUES(?,?)
 ON CONFLICT(conversation_id) DO UPDATE SET workspace_id=excluded.workspace_id`, cid, want[cid]); err != nil {
				return err
			}
		}
		for _, cid := range slices.Sorted(maps.Keys(dirty)) {
			if _, err := tx.ExecContext(ctx, `INSERT INTO delegation_summary_dirty(conversation_id,revision) VALUES(?,hex(randomblob(8)))
 ON CONFLICT(conversation_id) DO UPDATE SET revision=excluded.revision`, cid); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value
 WHERE CAST(meta.value AS INTEGER)<CAST(excluded.value AS INTEGER)`, delegationSummariesSweptKey, fmt.Sprint(generation))
		return err
	})
}

const delegationSummaryPendingSQL = `SELECT conversation_id id FROM delegation_summary_dirty WHERE conversation_id IN (SELECT value FROM json_each(?))`

// delegationSummariesPendingFor is which selected transcripts' summaries are
// pending: an indexed lookup, for a selector.
func (c *Catalog) delegationSummariesPendingFor(ctx context.Context, ids []string) ([]string, error) {
	pending, err := workDomainPending(ctx, c.reader(), delegationSummaryPendingSQL, workDomainIDs(ids))
	return sortedKeys(pending), err
}

// refreshDelegationSummaries stores up to limit workspaces' dirty summaries,
// for the classic maintenance pass and tests, and reports how many
// transcripts it finished.
func (c *Catalog) refreshDelegationSummaries(ctx context.Context, limit int) (int, error) {
	if !c.delegationSummariesMu.TryLock() {
		return 0, nil
	}
	err := c.sweepDelegationSummaries(ctx)
	c.delegationSummariesMu.Unlock()
	if err != nil {
		return 0, err
	}
	done := 0
	for done < limit {
		rows, err := queryMapsContext(ctx, c.reader(), "SELECT conversation_id FROM delegation_summary_dirty ORDER BY conversation_id LIMIT ?", min(limit-done, 64))
		if err != nil || len(rows) == 0 {
			return done, err
		}
		ids := make([]string, len(rows))
		for i, row := range rows {
			ids[i] = firstString(row["conversation_id"])
		}
		step, err := c.refreshDelegationSummariesFor(ctx, ids)
		if errors.Is(err, errWorkDomainBusy) {
			return done, nil
		}
		if err != nil {
			return done, err
		}
		if len(step.Done)+len(step.Superseded) == 0 {
			return done, nil
		}
		done += len(step.Done) + len(step.Superseded)
	}
	return done, nil
}

// refreshDelegationSummariesFor saves the summaries of the selected dirty
// transcripts' workspaces, at most delegationSummaryWorkspaces of them.
func (c *Catalog) refreshDelegationSummariesFor(ctx context.Context, ids []string) (workDomainStep, error) {
	var step workDomainStep
	ids = workDomainIDs(ids)
	if len(ids) == 0 {
		return step, nil
	}
	if !c.delegationSummariesMu.TryLock() {
		return step, errWorkDomainBusy
	}
	defer c.delegationSummariesMu.Unlock()
	// The revision each transcript had before its workspace was read: a
	// change meanwhile leaves it dirty. A transcript no longer retained is
	// read for the workspace that last held it.
	dirty, err := queryMapsContext(ctx, c.reader(), `SELECT d.conversation_id,d.revision,COALESCE(h.workspace_id,c.workspace_id) workspace_id
 FROM delegation_summary_dirty d LEFT JOIN conversations c ON c.id=d.conversation_id LEFT JOIN delegation_summary_hosts h ON h.conversation_id=d.conversation_id
 WHERE d.conversation_id IN (SELECT value FROM json_each(?)) ORDER BY workspace_id,d.conversation_id`, jsonText(ids))
	if err != nil {
		return step, err
	}
	type marked struct{ conversation, revision string }
	byWorkspace := map[string][]marked{}
	order := []string{}
	attempted := map[string]bool{}
	var cleared []marked
	for _, row := range dirty {
		m := marked{firstString(row["conversation_id"]), firstString(row["revision"])}
		ws := firstString(row["workspace_id"])
		if ws == "" {
			cleared, attempted[m.conversation] = append(cleared, m), true
			continue
		}
		if byWorkspace[ws] == nil {
			if len(order) == delegationSummaryWorkspaces {
				continue
			}
			order = append(order, ws)
		}
		byWorkspace[ws] = append(byWorkspace[ws], m)
		attempted[m.conversation] = true
	}
	type saved struct {
		workspace string
		rows      map[string][2]string
	}
	var batch []saved
	for _, ws := range order {
		rows, err := c.readDelegationSummaries(ctx, ws)
		if err != nil {
			if ctx.Err() != nil {
				return step, ctx.Err()
			}
			for _, m := range byWorkspace[ws] {
				step.Failed = append(step.Failed, m.conversation)
				delete(attempted, m.conversation)
			}
			continue
		}
		batch = append(batch, saved{ws, rows})
	}
	now := c.clock().UTC().Format(time.RFC3339)
	if len(batch) > 0 || len(cleared) > 0 {
		err = c.writeTransaction(ctx, "delegation-summaries", func(tx *sql.Tx) error {
			for _, b := range batch {
				if _, err := tx.ExecContext(ctx, "DELETE FROM delegation_summaries WHERE workspace_id=?", b.workspace); err != nil {
					return err
				}
				for _, node := range slices.Sorted(maps.Keys(b.rows)) {
					if _, err := tx.ExecContext(ctx, "INSERT INTO delegation_summaries(node_id,workspace_id,summary,activity,updated_at,revision) VALUES(?,?,?,?,?,?)", node, b.workspace, b.rows[node][0], b.rows[node][1], now, hashBytes([]byte(jsonText(b.rows[node])))); err != nil {
						return err
					}
				}
				for _, m := range byWorkspace[b.workspace] {
					if _, err := tx.ExecContext(ctx, "DELETE FROM delegation_summary_dirty WHERE conversation_id=? AND revision=?", m.conversation, m.revision); err != nil {
						return err
					}
				}
			}
			for _, m := range cleared {
				if _, err := tx.ExecContext(ctx, "DELETE FROM delegation_summary_dirty WHERE conversation_id=? AND revision=?", m.conversation, m.revision); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return step, err
		}
	}
	pending, err := workDomainPending(ctx, c.reader(), delegationSummaryPendingSQL, ids)
	for _, id := range step.Failed {
		delete(pending, id) // classified as failed, not again as remaining
	}
	failed := step.Failed
	step.Failed = nil
	step.classify(slices.DeleteFunc(slices.Clone(ids), func(id string) bool { return slices.Contains(failed, id) }), attempted, pending)
	step.Failed = failed
	return step, err
}

// readDelegationSummaries reads the workspace's own snapshot and encodes, by
// row ID, each execution that has streams elsewhere and a transcript here:
// its summary and its activity. A workspace too large to analyze saves none.
func (c *Catalog) readDelegationSummaries(ctx context.Context, workspace string) (map[string][2]string, error) {
	out := map[string][2]string{}
	var hosts int
	if err := c.reader().QueryRowContext(ctx, "SELECT count(*) FROM delegation_summary_hosts WHERE workspace_id=?", workspace).Scan(&hosts); err != nil || hosts == 0 {
		return out, err
	}
	snapshot, err := c.readDelegationSnapshotRead(ctx, workspace, nil, false)
	if err != nil {
		if err.Error() == errDelegationTooLarge().Error() {
			return out, nil
		}
		return nil, err
	}
	if snapshot == nil || snapshot.projection.res == nil {
		return out, nil
	}
	p := snapshot.projection
	for _, n := range snapshot.Nodes {
		x := p.res.Executions[n.execution]
		if n.ContextOnly || n.read == nil || x == nil || !p.hasTranscriptIn(x, workspace) || !p.unqueried(x, workspace) {
			continue
		}
		encoded, err := json.Marshal(n.summary())
		if err != nil {
			return nil, err
		}
		activity, err := json.Marshal(delegationActivity{Tools: n.tools, Requests: n.requests})
		if err != nil {
			return nil, err
		}
		out[n.ID] = [2]string{string(encoded), string(activity)}
	}
	return out, nil
}

// summary is what this workspace read of the row (n.read, before it was
// marked partial for its streams elsewhere).
func (n *delegationNode) summary() delegationSummary {
	self := maps.Clone(n.read)
	delete(self, "children")
	delete(self, "leaves")
	delete(self, "depth")
	if m, ok := n.Self["events"]; ok && n.EventsPending {
		self["events"] = m
	}
	sum := delegationSummary{Self: self, AccountedTokens: maps.Clone(n.accountedTokens), TokenAttribution: n.TokenAttribution, Start: n.Start, End: n.End, Usage: n.Usage, OccurrencePartial: n.OccurrencePartial, EventsPending: n.EventsPending, Episodes: n.episodes,
		EditedFiles: slices.Sorted(maps.Keys(n.editedFiles)), ReadFiles: slices.Sorted(maps.Keys(n.readFiles))}
	for _, t := range n.tools {
		if t.Start != nil && t.End != nil && *t.End >= *t.Start {
			sum.ToolSpans = append(sum.ToolSpans, delegationSpan{*t.Start, *t.End})
		}
	}
	waits, _, _ := delegationWaitSpans(n.tools)
	sum.ToolSpans, sum.WaitSpans = delegationMergeSpans(sum.ToolSpans), delegationMergeSpans(waits)
	return sum
}

// delegationMergeSpans is the spans' union as disjoint, ordered spans.
func delegationMergeSpans(spans []delegationSpan) []delegationSpan {
	if len(spans) == 0 {
		return nil
	}
	spans = slices.Clone(spans)
	sort.Slice(spans, func(i, j int) bool { return spans[i].Start < spans[j].Start })
	out := []delegationSpan{spans[0]}
	for _, s := range spans[1:] {
		if last := &out[len(out)-1]; s.Start <= last.End {
			last.End = max(last.End, s.End)
		} else {
			out = append(out, s)
		}
	}
	return out
}

// fillSaved shows, for rows this workspace read nothing of, the summaries
// other workspaces saved. Each keeps what was read here (own) for the
// workspace's totals. One summary is shown as saved; several (an execution
// whose transcripts are held in several workspaces) add up, as partial.
func (s *delegationSnapshot) fillSaved(ctx context.Context, q queryer, workspace string, rows []*delegationNode) error {
	if len(rows) == 0 {
		return nil
	}
	ids := make([]string, 0, len(rows))
	for _, n := range rows {
		ids = append(ids, n.ID)
	}
	saved, err := queryMapsContext(ctx, q, `SELECT s.node_id,s.workspace_id,s.summary,s.updated_at,s.revision,
 EXISTS(SELECT 1 FROM delegation_summary_hosts h JOIN delegation_summary_dirty d ON d.conversation_id=h.conversation_id WHERE h.workspace_id=s.workspace_id) pending
 FROM delegation_summaries s WHERE s.node_id IN (SELECT value FROM json_each(?)) AND s.workspace_id<>? ORDER BY s.node_id,s.workspace_id`, jsonText(ids), workspace)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return nil // a catalog this build has not initialized
		}
		return err
	}
	byNode := map[string][]map[string]any{}
	for _, row := range saved {
		byNode[firstString(row["node_id"])] = append(byNode[firstString(row["node_id"])], row)
	}
	for _, n := range rows {
		found := byNode[n.ID]
		if len(found) == 0 {
			continue
		}
		ref := &delegationSavedRef{revisions: map[string]string{}}
		var sums []delegationSummary
		for _, row := range found {
			var sum delegationSummary
			if json.Unmarshal([]byte(firstString(row["summary"])), &sum) != nil || len(sum.Self) == 0 {
				continue
			}
			sums = append(sums, sum)
			ref.Workspaces = append(ref.Workspaces, firstString(row["workspace_id"]))
			ref.revisions[firstString(row["workspace_id"])] = firstString(row["revision"])
			if at := firstString(row["updated_at"]); ref.UpdatedAt == "" || at < ref.UpdatedAt {
				ref.UpdatedAt = at
			}
			ref.Pending = ref.Pending || integer(row["pending"]) != 0
		}
		if len(sums) == 0 {
			continue
		}
		own := *n
		own.Self = maps.Clone(n.Self)
		n.own, n.Saved = &own, ref
		sum := delegationCombineSummaries(sums)
		for key, m := range sum.Self {
			if key != "children" && key != "leaves" && key != "depth" {
				n.Self[key] = m
			}
		}
		n.accountedTokens, n.TokenAttribution = sum.AccountedTokens, sum.TokenAttribution
		// The unread row may still carry its local launch handle's envelope.
		n.Start, n.End = sum.Start, sum.End
		if n.Usage == "" || n.Usage == "unavailable" {
			n.Usage = sum.Usage
		}
		if n.OccurrencePartial == "" {
			n.OccurrencePartial = sum.OccurrencePartial
		}
		n.EventsPending = n.EventsPending || sum.EventsPending
		n.editedFiles, n.readFiles = map[string]bool{}, map[string]bool{}
		for _, key := range sum.EditedFiles {
			n.editedFiles[key] = true
		}
		for _, key := range sum.ReadFiles {
			n.readFiles[key] = true
		}
		n.savedTools, n.savedWaits = sum.ToolSpans, sum.WaitSpans
		if len(n.episodes) == 0 {
			n.episodes = sum.Episodes
		}
	}
	// Hydration can shrink a launch envelope as well as expand it. Derive
	// the viewport again from the rows' actual activity bounds.
	s.Start, s.End = nil, nil
	for _, n := range s.Nodes {
		if n.Start != nil && (s.Start == nil || *n.Start < *s.Start) {
			s.Start = n.Start
		}
		if n.End != nil && (s.End == nil || *n.End > *s.End) {
			s.End = n.End
		}
	}
	return nil
}

// delegationCombineSummaries adds several workspaces' summaries of one
// execution. Counts add; spans, files and the time bounds combine; every
// value is partial, since the parts were read separately.
func delegationCombineSummaries(sums []delegationSummary) delegationSummary {
	if len(sums) == 1 {
		return sums[0]
	}
	out := delegationSummary{Self: map[string]delegationMetric{}}
	files, reads := map[string]bool{}, map[string]bool{}
	addMetrics := func(target, source map[string]delegationMetric) {
		for key, m := range source {
			prior, seen := target[key]
			switch {
			case !seen || prior.Value == nil:
				target[key] = m
			case m.Value != nil:
				v := *prior.Value
				switch key {
				case "start":
					v = min(v, *m.Value)
				case "depth":
					v = max(v, *m.Value)
				default:
					v += *m.Value
				}
				prior.Value, prior.Known, prior.Total = &v, prior.Known+m.Known, prior.Total+m.Total
				prior.IsEstimate = prior.IsEstimate || m.IsEstimate
				target[key] = prior
			}
		}
	}
	accountedKeys := map[string]bool{}
	if slices.ContainsFunc(sums, func(sum delegationSummary) bool { return sum.TokenAttribution != nil }) {
		out.TokenAttribution = &sessionTokenAttribution{}
	}
	for _, sum := range sums {
		addMetrics(out.Self, sum.Self)
		for key := range sum.AccountedTokens {
			accountedKeys[key] = true
		}
		if a := out.TokenAttribution; a != nil {
			if part := sum.TokenAttribution; part != nil {
				a.Accounted += part.Accounted
				a.Direct += part.Direct
				a.Unallocated += part.Unallocated
				// Last-request observations on segments of one execution
				// can overlap, as in the live workspace reader.
				a.Observed = max(a.Observed, part.Observed)
				a.Evidence = append(a.Evidence, part.Evidence...)
			} else if m := sum.Self["tokens"]; m.Value != nil {
				a.Accounted += *m.Value
				a.Direct += *m.Value
			}
		}
		if sum.Start != nil && (out.Start == nil || *sum.Start < *out.Start) {
			out.Start = sum.Start
		}
		if sum.End != nil && (out.End == nil || *sum.End > *out.End) {
			out.End = sum.End
		}
		if out.Usage == "" || out.Usage == "unavailable" {
			out.Usage = sum.Usage
		}
		out.OccurrencePartial = firstString(out.OccurrencePartial, sum.OccurrencePartial)
		out.EventsPending = out.EventsPending || sum.EventsPending
		for _, f := range sum.EditedFiles {
			files[f] = true
		}
		for _, f := range sum.ReadFiles {
			reads[f] = true
		}
		out.ToolSpans = append(out.ToolSpans, sum.ToolSpans...)
		out.WaitSpans = append(out.WaitSpans, sum.WaitSpans...)
		out.Episodes = append(out.Episodes, sum.Episodes...)
	}
	if len(accountedKeys) > 0 {
		out.AccountedTokens = map[string]delegationMetric{}
		for _, sum := range sums {
			part := map[string]delegationMetric{}
			for key := range accountedKeys {
				m, ok := sum.AccountedTokens[key]
				if !ok {
					m = sum.Self[key]
				}
				part[key] = m
			}
			addMetrics(out.AccountedTokens, part)
		}
	}
	if a := out.TokenAttribution; a != nil {
		m := out.Self["tokens"]
		if a.Direct > 0 || a.Observed > 0 {
			v := max(a.Direct, a.Observed)
			m.Value = &v
		} else {
			m.Value = nil // the accounted pool has no known direct allocation
		}
		out.Self["tokens"] = m
	}
	out.ToolSpans, out.WaitSpans = delegationMergeSpans(out.ToolSpans), delegationMergeSpans(out.WaitSpans)
	out.Episodes = delegationMergeSpans(out.Episodes)
	out.EditedFiles, out.ReadFiles = slices.Sorted(maps.Keys(files)), slices.Sorted(maps.Keys(reads))
	// Spans and sets do not add: they are recomputed from the union.
	if m, ok := out.Self["files"]; ok && m.Value != nil {
		v := float64(len(files))
		m.Value = &v
		out.Self["files"] = m
	}
	if m, ok := out.Self["filesRead"]; ok && m.Value != nil {
		v := float64(len(reads))
		m.Value = &v
		out.Self["filesRead"] = m
	}
	for key, spans := range map[string][]delegationSpan{"toolTime": out.ToolSpans, "toolAgent": out.ToolSpans, "backgroundTime": out.WaitSpans} {
		if m, ok := out.Self[key]; ok && m.Value != nil {
			v := delegationUnion(spans)
			m.Value = &v
			out.Self[key] = m
		}
	}
	if out.Start != nil && out.End != nil {
		for _, key := range []string{"clock", "total"} {
			if m, ok := out.Self[key]; ok && m.Value != nil {
				v := *out.End - *out.Start
				m.Value = &v
				out.Self[key] = m
			}
		}
	}
	for _, metrics := range []map[string]delegationMetric{out.Self, out.AccountedTokens} {
		for key, m := range metrics {
			if m.Value != nil && m.Quality != "unknown" {
				m.Quality = "partial"
				metrics[key] = m
			}
		}
	}
	return out
}

// delegationSummariesWorkDefinition: saved summaries under population
// execution, a projection the Search view's Stop holds, like session shape.
// Its unit is the host transcript; a step reads whole workspaces, so the
// queue is ordered by workspace. The sweep runs as admitted preparation at
// each execution source generation.
func (s *Server) delegationSummariesWorkDefinition() workDefinition {
	c := s.Catalog
	task := &conversationWorkTask{server: s, kind: kindDelegationSummaries, cpus: 1, pending: c.delegationSummariesPendingFor, filterFailures: true,
		prepareVersion: c.delegationSummariesSweepVersion, prepare: c.prepareDelegationSummaries,
		selectSQL: `SELECT d.conversation_id id,d.revision FROM delegation_summary_dirty d LEFT JOIN conversations c ON c.id=d.conversation_id
 WHERE d.conversation_id NOT IN (SELECT value FROM json_each(?)) ORDER BY c.workspace_id,d.conversation_id LIMIT ?`,
		step: func(ctx context.Context, ids []string, _ workAllocation) (workDomainStep, error) {
			return c.refreshDelegationSummariesFor(ctx, ids)
		}}
	return workDefinition{Kind: kindDelegationSummaries, Task: projectionHeldTask{catalog: c, workTask: task}}
}

// delegationSummariesSweepDue: the execution sources moved past the sweep.
func (c *Catalog) delegationSummariesSweepDue(ctx context.Context) bool {
	generation, tracked, err := executionSourceGeneration(ctx, c.reader())
	if err != nil || !tracked {
		return false
	}
	var swept sql.NullInt64
	if err := c.reader().QueryRowContext(ctx, "SELECT CAST(value AS INTEGER) FROM meta WHERE key=?", delegationSummariesSweptKey).Scan(&swept); err != nil && err != sql.ErrNoRows {
		return false
	}
	return !swept.Valid || swept.Int64 < generation
}

// savedActivity reads the tool calls and request times saved for the given
// summarized rows, for their Timeline lanes, keyed by row ID. A row whose
// transcripts several workspaces hold has every part's.
func (c *Catalog) savedActivity(ctx context.Context, snapshot *delegationSnapshot, ids []string) (map[string]delegationActivity, error) {
	out := map[string]delegationActivity{}
	type key struct{ node, workspace string }
	wanted := map[key]string{}
	nodes := []string{}
	for _, id := range ids {
		if n := snapshot.ByID[id]; n != nil && n.Saved != nil && len(n.tools) == 0 && len(n.requests) == 0 {
			nodes = append(nodes, id)
			for _, ws := range n.Saved.Workspaces {
				wanted[key{id, ws}] = n.Saved.revisions[ws]
			}
		}
	}
	if len(nodes) == 0 {
		return out, nil
	}
	rows, err := queryMapsContext(ctx, c.reader(), `SELECT node_id,workspace_id,activity,revision FROM delegation_summaries WHERE node_id IN (SELECT value FROM json_each(?))`, jsonText(nodes))
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		id := firstString(row["node_id"])
		k := key{id, firstString(row["workspace_id"])}
		revision, ok := wanted[k]
		if !ok {
			continue // saved after this snapshot read its summaries
		}
		if revision == "" || revision != firstString(row["revision"]) {
			return nil, errDelegationSnapshotChanged
		}
		delete(wanted, k)
		var a delegationActivity
		if json.Unmarshal([]byte(firstString(row["activity"])), &a) != nil {
			continue
		}
		have := out[id]
		have.Tools, have.Requests = append(have.Tools, a.Tools...), append(have.Requests, a.Requests...)
		out[id] = have
	}
	if len(wanted) > 0 {
		return nil, errDelegationSnapshotChanged
	}
	return out, nil
}

// laneMembers are the streams a row's Timeline lane reads. A row shown from
// a saved summary read nothing here; its lane reads its streams in the
// workspace that holds its transcript (n.WorkspaceID), as that workspace's
// own lane does.
func (s *delegationSnapshot) laneMembers(n *delegationNode) []string {
	members := s.members[n.ID]
	if len(members) == 0 {
		members = []string{n.ID}
	}
	if n.Saved == nil || s.projection.res == nil {
		return members
	}
	x := s.projection.res.Executions[n.execution]
	if x == nil {
		return members
	}
	members = slices.Clone(members)
	for _, ref := range x.Streams {
		st := s.projection.res.Stream(ref)
		if st == nil || st.WorkspaceID != n.WorkspaceID {
			continue
		}
		switch ref.Kind {
		case sourceSession:
			members = append(members, ref.ID)
		case sourceConversation:
			members = append(members, "conversation:"+st.ConversationID)
			if st.MainSessionID != "" {
				members = append(members, st.MainSessionID)
			}
		}
	}
	return uniqueStrings(members)
}
