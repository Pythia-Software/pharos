package archive

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/gbdubs/pharos/internal/querytable"
)

// Parallelism estimates how many agents were active at once from transcript
// timestamps; there is no turn-start/turn-end or inference telemetry. It ports
// tools/concurrency-prototype.py (activity) and web/src/concurrency-data.ts
// (units, gap bridging) with these deliberate differences:
//
//   - Messages are ordered by timestamp (ties in insertion order), not by
//     source_order: that column lies past the message text, and reading it
//     visits every long message's overflow pages (3.6s against 12-19s warm
//     for a month of a 71 GB catalog).
//   - A conversation's whole transcript is timed, then clipped to the period,
//     instead of messages within the period ±2h.
//   - Mirrors are removed like Usage: by workspace (suppressMirrors, which
//     prefers native transcripts over Conductor's) and by identity link
//     (usageConversationRoots' canonical conversation), not by the larger
//     message count among native-ID aliases. Labels use the resolved
//     workspace title, which already borrows a mirror's human title.
//   - Delegation follows usageConversationRoots: a run launched by another
//     conversation is delegated even at agent_depth 0, and a continuation
//     counts toward its root's agent.
//   - Hours are local (time.Local), not a fixed America/Denver.
const (
	parallelismActivityVersion = "1"
	// parallelismDays bounds the derived period: local days through today.
	parallelismDays        = 400
	parallelismSettingsKey = "pharos-concurrency-v1"
	parallelismBatch       = 250
)

type parallelismSpans = [][2]int64

type parallelismActivity struct {
	Turns, ModelSpans parallelismSpans
	Unexplained       int64
}

type parallelismEvent struct {
	role, kind string
	at         int64
}

// parallelismTimeline is the prototype's activity(). A model span runs from a
// user or tool input to the last response before the next input; tool time is
// not model time. A turn runs from a prompt to its last response or tool
// result. An hour of silence ends both, a request over ten minutes is an
// unexplained wait (counted, not kept), and an unanswered prompt adds nothing.
func parallelismTimeline(events []parallelismEvent) parallelismActivity {
	const none = math.MinInt64
	var model, turns parallelismSpans
	var unexplained int64
	pending, response, turnStart, turnEnd, previous := int64(none), int64(none), int64(none), int64(none), int64(none)
	closeRequest := func() {
		if pending != none && response != none && response > pending {
			if response-pending <= 10*60_000 {
				model = append(model, [2]int64{pending, response})
			} else {
				unexplained++
			}
		}
		pending, response = none, none
	}
	closeTurn := func() {
		if turnStart != none && turnEnd != none && turnEnd > turnStart {
			turns = append(turns, [2]int64{turnStart, turnEnd})
		}
		turnStart, turnEnd = none, none
	}
	for _, event := range events {
		if previous != none && event.at-previous > 60*60_000 {
			closeRequest()
			closeTurn()
		}
		previous = event.at
		switch {
		case event.role == "user" && event.kind == "message":
			closeRequest()
			closeTurn()
			pending, turnStart = event.at, event.at
		case event.kind == "tool_result" || event.kind == "delegation_result":
			closeRequest()
			pending = event.at
			if turnStart != none {
				turnEnd = event.at
			}
		case event.role == "assistant" && (event.kind == "message" || event.kind == "thinking" || event.kind == "tool_call" || event.kind == "delegation"):
			if pending != none && event.at >= pending {
				response = event.at
			}
			// Capture can begin mid-turn; don't extend it back to the start.
			if turnStart == none {
				turnStart = event.at
			}
			turnEnd = event.at
		}
	}
	closeRequest()
	closeTurn()
	return parallelismActivity{Turns: mergeSpans(turns, 0), ModelSpans: mergeSpans(model, 0), Unexplained: unexplained}
}

// mergeSpans is mergeIntervals: spans at most gap apart join.
func mergeSpans(spans parallelismSpans, gap int64) parallelismSpans {
	sorted := slices.Clone(spans)
	slices.SortFunc(sorted, func(a, b [2]int64) int { return cmp.Compare(a[0], b[0]) })
	result := parallelismSpans{}
	for _, span := range sorted {
		if span[1] <= span[0] {
			continue
		}
		if n := len(result); n > 0 && span[0]-result[n-1][1] <= gap {
			result[n-1][1] = max(result[n-1][1], span[1])
		} else {
			result = append(result, span)
		}
	}
	return result
}

func clipSpans(spans parallelismSpans, from, to int64) parallelismSpans {
	result := parallelismSpans{}
	for _, span := range spans {
		if span[0] < to && span[1] > from {
			result = append(result, [2]int64{max(span[0], from), min(span[1], to)})
		}
	}
	return result
}

// readParallelismActivity times one conversation's transcript in q's snapshot.
// The index supplies created_at; role and kind precede the message text.
func readParallelismActivity(ctx context.Context, q queryer, id string) (parallelismActivity, error) {
	owned := map[string]bool{}
	rows, err := q.QueryContext(ctx, `SELECT am.message_id FROM agent_sessions a JOIN agent_session_messages am ON am.agent_session_id=a.id
 WHERE a.conversation_id=? AND a.native_id<>'main'`, id)
	if err != nil {
		return parallelismActivity{}, err
	}
	for rows.Next() {
		var message string
		if err := rows.Scan(&message); err != nil {
			rows.Close()
			return parallelismActivity{}, err
		}
		owned[message] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return parallelismActivity{}, err
	}
	rows, err = q.QueryContext(ctx, `SELECT m.id,m.role,m.kind,m.created_at FROM messages m INDEXED BY messages_conversation_idx
 WHERE m.conversation_id=? AND m.created_at IS NOT NULL ORDER BY m.created_at`, id)
	if err != nil {
		return parallelismActivity{}, err
	}
	defer rows.Close()
	events := []parallelismEvent{}
	for rows.Next() {
		var message, role, kind, created string
		if err := rows.Scan(&message, &role, &kind, &created); err != nil {
			return parallelismActivity{}, err
		}
		if owned[message] || role != "user" && role != "assistant" && kind != "tool_result" && kind != "delegation_result" {
			continue
		}
		at, err := time.Parse(timeLayout, created)
		if err != nil {
			var ok bool
			if at, ok = parseTime(created); !ok {
				continue
			}
		}
		events = append(events, parallelismEvent{role: role, kind: kind, at: at.UnixMilli()})
	}
	if err := rows.Err(); err != nil {
		return parallelismActivity{}, err
	}
	return parallelismTimeline(events), nil
}

// ensureParallelismActivity marks every conversation dirty when the timing
// rules change, so the stored spans are rebuilt (incrementally) under them.
func (c *Catalog) ensureParallelismActivity() error {
	var version string
	err := c.DB.QueryRow("SELECT value FROM meta WHERE key='parallelism_activity_version'").Scan(&version)
	if err != nil && err != sql.ErrNoRows || version == parallelismActivityVersion {
		return err
	}
	tx, err := c.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, statement := range []string{"DELETE FROM parallelism_activity",
		"INSERT INTO parallelism_dirty SELECT id,'' FROM conversations WHERE true ON CONFLICT(conversation_id) DO NOTHING",
		"INSERT INTO meta(key,value) VALUES('parallelism_activity_version','" + parallelismActivityVersion + "') ON CONFLICT(key) DO UPDATE SET value=excluded.value",
	} {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type parallelismDerived struct {
	id, revision string
	activity     *parallelismActivity // nil: outside the period or deleted
}

// parallelismPeriod is the derived period: parallelismDays local days through
// today, and the earliest end a conversation in it can have.
func parallelismPeriod(now time.Time) (start, end time.Time, cutoff string) {
	now = now.In(time.Local)
	start = time.Date(now.Year(), now.Month(), now.Day()-parallelismDays+1, 0, 0, 0, 0, time.Local)
	end = time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.Local)
	return start, end, formatTime(start.AddDate(0, 0, -1))
}

const parallelismDirtySQL = `SELECT d.conversation_id,d.revision,c.id IS NOT NULL AND (c.ended_at IS NULL OR c.ended_at>=?) needed
 FROM parallelism_dirty d LEFT JOIN conversations c ON c.id=d.conversation_id`

func deriveParallelismBatch(ctx context.Context, q queryer, cutoff string, limit int) ([]parallelismDerived, error) {
	dirty, err := queryMapsContext(ctx, q, parallelismDirtySQL+" LIMIT ?", cutoff, limit)
	if err != nil {
		return nil, err
	}
	result := make([]parallelismDerived, 0, len(dirty))
	for _, row := range dirty {
		derived := parallelismDerived{id: firstString(row["conversation_id"]), revision: asString(row["revision"])}
		if integer(row["needed"]) != 0 {
			activity, err := readParallelismActivity(ctx, q, derived.id)
			if err != nil {
				return nil, err
			}
			derived.activity = &activity
		}
		result = append(result, derived)
	}
	return result, nil
}

// Publishing an old derivation is harmless: its conversation stays dirty
// unless the token it read is still current.
func (c *Catalog) publishParallelism(ctx context.Context, batch []parallelismDerived) error {
	return c.writeTransaction(ctx, "parallelism-activity", func(tx *sql.Tx) error {
		for _, derived := range batch {
			var err error
			if derived.activity == nil {
				_, err = tx.ExecContext(ctx, "DELETE FROM parallelism_activity WHERE conversation_id=?", derived.id)
			} else {
				_, err = tx.ExecContext(ctx, "INSERT OR REPLACE INTO parallelism_activity(conversation_id,turns,model_spans,unexplained) VALUES(?,?,?,?)",
					derived.id, encodeSpans(derived.activity.Turns), encodeSpans(derived.activity.ModelSpans), derived.activity.Unexplained)
			}
			if err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, "DELETE FROM parallelism_dirty WHERE conversation_id=? AND revision=?", derived.id, derived.revision); err != nil {
				return err
			}
		}
		return nil
	})
}

// refreshParallelism stores up to limit dirty conversations' activity, in
// short read snapshots and write transactions, and reports how many. Unless
// asked to wait, it leaves the work to a caller already doing it.
func (c *Catalog) refreshParallelism(ctx context.Context, limit int, wait bool) (int, error) {
	if wait {
		c.parallelismMu.Lock()
	} else if !c.parallelismMu.TryLock() {
		return 0, nil
	}
	defer c.parallelismMu.Unlock()
	_, _, cutoff := parallelismPeriod(c.clock())
	done := 0
	for done < limit {
		tx, err := c.reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return done, err
		}
		batch, err := deriveParallelismBatch(ctx, tx, cutoff, min(limit-done, parallelismBatch))
		_ = tx.Rollback()
		if err != nil || len(batch) == 0 {
			return done, err
		}
		if err := c.publishParallelism(ctx, batch); err != nil {
			return done, err
		}
		done += len(batch)
	}
	return done, nil
}

// parallelismActivities returns every conversation's activity in the period:
// stored spans for clean conversations and fresh ones for dirty conversations.
// Storing the dirty ones is left to maintainLibrary.
func (c *Catalog) parallelismActivities(ctx context.Context, cutoff string) (map[string]parallelismActivity, error) {
	tx, err := c.reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	dirty, err := deriveParallelismBatch(ctx, tx, cutoff, -1)
	if err != nil {
		return nil, err
	}
	result := map[string]parallelismActivity{}
	skip := map[string]bool{}
	for _, derived := range dirty {
		skip[derived.id] = true
		if derived.activity != nil {
			result[derived.id] = *derived.activity
		}
	}
	rows, err := tx.QueryContext(ctx, "SELECT conversation_id,turns,model_spans,unexplained FROM parallelism_activity")
	if err != nil {
		return nil, err
	}
	unreadable := []string{}
	for rows.Next() {
		var id string
		var turns, model []byte
		var activity parallelismActivity
		if err := rows.Scan(&id, &turns, &model, &activity.Unexplained); err != nil {
			rows.Close()
			return nil, err
		}
		if skip[id] {
			continue
		}
		var ok, okModel bool
		activity.Turns, ok = decodeSpans(turns)
		activity.ModelSpans, okModel = decodeSpans(model)
		if !ok || !okModel {
			unreadable = append(unreadable, id)
			continue
		}
		result[id] = activity
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Stored spans are disposable; derive any damaged ones again.
	for _, id := range unreadable {
		if result[id], err = readParallelismActivity(ctx, tx, id); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// Spans are stored as varints, each start as the gap from the previous end
// and then its length: compact, and quick to read for every conversation.
func encodeSpans(spans parallelismSpans) []byte {
	data, previous := make([]byte, 0, 4*len(spans)+8), int64(0)
	for _, span := range spans {
		data = binary.AppendVarint(data, span[0]-previous)
		data = binary.AppendUvarint(data, uint64(span[1]-span[0]))
		previous = span[1]
	}
	return data
}

func decodeSpans(data []byte) (parallelismSpans, bool) {
	spans, previous := parallelismSpans{}, int64(0)
	for len(data) > 0 {
		gap, n := binary.Varint(data)
		if n <= 0 {
			return nil, false
		}
		length, m := binary.Uvarint(data[n:])
		if m <= 0 {
			return nil, false
		}
		data = data[n+m:]
		start := previous + gap
		previous = start + int64(length)
		spans = append(spans, [2]int64{start, previous})
	}
	return spans, true
}

// concurrencyThread and concurrencyData are web/src/concurrency-data.ts's
// ConcurrencyThread and ConcurrencyData. workspace_id is the checkout
// location (the user's workspace), work_id the Pharos workspace.
type concurrencyThread struct {
	ID                 string           `json:"id"`
	ConversationID     string           `json:"conversation_id"`
	WorkID             string           `json:"work_id"`
	WorkspaceID        string           `json:"workspace_id"`
	WorkspaceName      string           `json:"workspace_name"`
	Repository         string           `json:"repository"`
	Title              string           `json:"title"`
	Channel            string           `json:"channel"`
	MachineID          string           `json:"machine_id"`
	ExecutionMachineID string           `json:"execution_machine_id"`
	Provider           string           `json:"provider"`
	Model              string           `json:"model"`
	AgentDepth         int64            `json:"agent_depth"`
	Turns              parallelismSpans `json:"turns"`
	ModelSpans         parallelismSpans `json:"model_spans"`
	RootConversationID string           `json:"root_conversation_id,omitempty"`
	location           string
	rootProvider       string
	rootModel          string
}

type concurrencyData struct {
	GeneratedAt string              `json:"generated_at"`
	Timezone    string              `json:"timezone"`
	PeriodStart string              `json:"period_start"`
	PeriodEnd   string              `json:"period_end"`
	Days        []string            `json:"days"`
	Threads     []concurrencyThread `json:"threads"`
	Coverage    struct {
		Conversations   int    `json:"conversations"`
		MirrorsRemoved  int    `json:"mirrors_removed"`
		UnexplainedWait int64  `json:"unexplained_waits_excluded"`
		Timing          string `json:"timing"`
		Catalog         string `json:"catalog"`
	} `json:"coverage"`
	start, end int64
}

// parallelismData is the period's threads, kept until the catalog changes.
// Settings only select among them, so this key excludes them.
func (c *Catalog) parallelismData(ctx context.Context) (*concurrencyData, error) {
	now := c.clock().In(time.Local)
	return cachedValue(ctx, c, "parallelism-threads:"+now.Format("2006-01-02")+":"+time.Local.String(), func(ctx context.Context) (*concurrencyData, error) {
		return c.loadParallelism(ctx, now)
	})
}

func (c *Catalog) loadParallelism(ctx context.Context, now time.Time) (*concurrencyData, error) {
	_, _, cutoff := parallelismPeriod(now)
	activities, err := c.parallelismActivities(ctx, cutoff)
	if err != nil {
		return nil, err
	}
	return c.parallelismThreads(ctx, now, activities)
}

// parallelismThreads attributes each conversation's activity: mirrors
// removed, delegated runs marked, and every thread placed in its root's
// workspace and repository.
func (c *Catalog) parallelismThreads(ctx context.Context, now time.Time, activities map[string]parallelismActivity) (*concurrencyData, error) {
	start, end, cutoff := parallelismPeriod(now)
	conversations, err := queryMapsContext(ctx, c.reader(), `SELECT c.id,c.workspace_id,c.provider,c.agent_depth,
 COALESCE(NULLIF(c.model,''),(SELECT a.model FROM agent_sessions a WHERE a.conversation_id=c.id AND a.native_id='main' LIMIT 1)) model
 FROM conversations c WHERE (c.ended_at IS NULL OR c.ended_at>=?) AND c.provider IN ('codex','claude','antigravity')
 AND `+currentGroupConversation("c")+` ORDER BY c.id`, cutoff)
	if err != nil {
		return nil, err
	}
	workspaces := map[string]map[string]any{}
	rows, err := queryMapsContext(ctx, c.reader(), `SELECT w.id,w.title,w.location,w.source_kind,w.activity_at,r.display_name repository_name
 FROM workspaces w INDEXED BY workspaces_library_idx LEFT JOIN repositories r ON r.id=w.repository_id`)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		workspaces[firstString(row["id"])] = row
	}
	candidates, seen := []map[string]any{}, map[string]bool{}
	for _, conversation := range conversations {
		id := firstString(conversation["workspace_id"])
		if workspace := workspaces[id]; workspace != nil && !seen[id] {
			seen[id] = true
			candidates = append(candidates, map[string]any{"id": id, "source_kind": workspace["source_kind"], "activity_at": workspace["activity_at"]})
		}
	}
	mirrors, kept := map[string][]string{}, []map[string]any{}
	for _, row := range c.suppressMirrors(candidates) {
		id := firstString(row["id"])
		mirrors[id], _ = row["mirrored_workspace_ids"].([]string)
		kept = append(kept, map[string]any{"workspace_id": id})
	}
	lineage, err := c.usageConversationRoots(kept)
	if err != nil {
		return nil, err
	}
	data := &concurrencyData{GeneratedAt: now.UTC().Format(time.RFC3339), Timezone: time.Local.String(), Threads: []concurrencyThread{},
		PeriodStart: start.Format("2006-01-02"), PeriodEnd: end.AddDate(0, 0, -1).Format("2006-01-02"), start: start.UnixMilli(), end: end.UnixMilli()}
	for day := start; day.Before(end); day = day.AddDate(0, 0, 1) {
		data.Days = append(data.Days, day.Format("2006-01-02"))
	}
	data.Coverage.Timing, data.Coverage.Catalog = "estimated", "pharos"
	for _, conversation := range conversations {
		id, workspaceID := firstString(conversation["id"]), firstString(conversation["workspace_id"])
		if _, ok := mirrors[workspaceID]; !ok || lineage.canonical[id] != id {
			data.Coverage.MirrorsRemoved++
			continue
		}
		activity := activities[id]
		turns := clipSpans(activity.Turns, data.start, data.end)
		if len(turns) == 0 {
			continue
		}
		thread := concurrencyThread{ID: id, ConversationID: id, Provider: firstString(conversation["provider"]), Model: defaultString(firstString(conversation["model"]), "Unknown model"),
			AgentDepth: integer(conversation["agent_depth"]), Turns: turns, ModelSpans: clipSpans(activity.ModelSpans, data.start, data.end)}
		thread.rootProvider, thread.rootModel = thread.Provider, thread.Model
		// Delegated work runs inside its root's workspace and repository. A run
		// another conversation launched is delegated even at agent_depth 0.
		rootWorkspace := workspaceID
		if root := lineage.roots[id]; root != nil {
			rootWorkspace = firstString(root["workspace_id"])
			if rootID := firstString(root["id"]); rootID != id {
				thread.RootConversationID = rootID
				thread.rootProvider, thread.rootModel = firstString(root["provider"]), defaultString(firstString(root["model"]), "Unknown model")
			}
			if lineage.runs[id] != "" {
				thread.AgentDepth = max(thread.AgentDepth, 1)
			}
		}
		workspace := workspaces[rootWorkspace]
		thread.WorkID, thread.Title, thread.location = rootWorkspace, firstString(workspace["title"]), firstString(workspace["location"])
		for _, mirror := range mirrors[rootWorkspace] {
			if thread.location == "" {
				thread.location = firstString(workspaces[mirror]["location"])
			}
		}
		thread.Repository = firstString(workspace["repository_name"])
		// Adapters can record the same checkout under several workspace IDs;
		// its location is the user's workspace.
		thread.WorkspaceID, thread.WorkspaceName = rootWorkspace, thread.Title
		if thread.location != "" {
			thread.WorkspaceID, thread.WorkspaceName = filepath.Clean(thread.location), filepath.Base(thread.location)
		}
		data.Coverage.UnexplainedWait += activity.Unexplained
		data.Threads = append(data.Threads, thread)
	}
	origins, err := c.conversationOrigins(ctx)
	if err != nil {
		return nil, err
	}
	for i := range data.Threads {
		t := &data.Threads[i]
		o := origins[t.ConversationID]
		t.Channel = firstString(o["channel"])
		t.MachineID = firstString(o["machine_id"])
		t.ExecutionMachineID = firstString(o["execution_machine_id"])
	}
	data.Coverage.Conversations = len(data.Threads)
	return data, nil
}

type parallelismSettings struct {
	gap      int64 // minutes
	counting string
}

// parallelismSettings reads the Parallelism view's saved choices
// (web/src/concurrency-settings.ts normalizeConcurrencySettings).
func (c *Catalog) parallelismSettings(ctx context.Context) parallelismSettings {
	settings := parallelismSettings{gap: 60, counting: "workspace"}
	var text string
	if c.reader().QueryRowContext(ctx, "SELECT value FROM ui_preferences WHERE key=?", parallelismSettingsKey).Scan(&text) != nil {
		return settings
	}
	var saved struct {
		Gap      any `json:"gap"`
		Counting any `json:"counting"`
	}
	if json.Unmarshal([]byte(text), &saved) != nil {
		return settings
	}
	if gap, ok := saved.Gap.(float64); ok && (gap == 0 || gap == 15 || gap == 30 || gap == 60) {
		settings.gap = int64(gap)
	}
	if saved.Counting == "agent" {
		settings.counting = "agent"
	}
	return settings
}

type parallelismUnit struct {
	channel, machineID, executionMachineID string
	id, label, workspace, location         string
	repository, conversation               any
	provider, model                        any
	spans                                  parallelismSpans
}

// parallelismUnits is concurrencySpans(threads, "user", gap, counting): each
// user-managed thread's turns joined across gaps up to gap minutes, then
// united per counting unit. Bridging precedes the union, so a different
// conversation cannot manufacture a continuous work thread.
func parallelismUnits(threads []concurrencyThread, settings parallelismSettings) []parallelismUnit {
	units, index := []parallelismUnit{}, map[string]int{}
	for _, thread := range threads {
		if thread.AgentDepth > 0 {
			continue
		}
		id := thread.WorkspaceID
		if settings.counting == "agent" {
			id = defaultString(thread.RootConversationID, thread.ID)
		}
		at, ok := index[id]
		if !ok {
			unit := parallelismUnit{channel: thread.Channel, machineID: thread.MachineID, executionMachineID: thread.ExecutionMachineID, id: id, label: thread.WorkspaceName, workspace: thread.WorkspaceName, location: thread.location, repository: nilIfEmpty(thread.Repository)}
			if settings.counting == "agent" {
				unit.label, unit.conversation, unit.provider, unit.model = thread.Title, id, thread.rootProvider, thread.rootModel
			}
			at, index[id] = len(units), len(units)
			units = append(units, unit)
		}
		if units[at].channel != thread.Channel {
			units[at].channel = ""
		}
		if units[at].machineID != thread.MachineID {
			units[at].machineID = ""
		}
		if units[at].executionMachineID != thread.ExecutionMachineID {
			units[at].executionMachineID = ""
		}
		units[at].spans = append(units[at].spans, mergeSpans(thread.Turns, settings.gap*60_000)...)
	}
	for i := range units {
		units[i].spans = mergeSpans(units[i].spans, 0)
	}
	return units
}

// localHour is the start of ms's local clock hour. Stepping by real hours from
// it keeps a daylight-saving day's 23 or 25 hours distinct.
func localHour(ms int64) int64 {
	t := time.UnixMilli(ms).In(time.Local)
	return ms - int64(t.Minute())*60_000 - int64(t.Second())*1000 - int64(t.Nanosecond())/1e6
}

type parallelismCell struct {
	hour             int64
	unit             int
	level, repoLevel int
}

type parallelismMeasure struct {
	active                     int64
	elapsed, repo, parallelism float64
}

// parallelismCells sweeps the units' spans. Between consecutive changes in
// who is active, split at local hours, each active unit gets the piece's
// duration, and its share of the piece's wall time: 1/level machine-wide and
// 1/repo_level within its repository. Summed over any rows, the shares are
// the wall time with any of those units active.
func parallelismCells(units []parallelismUnit) map[parallelismCell]*parallelismMeasure {
	type edge struct {
		at         int64
		unit       int
		activating bool
	}
	edges := []edge{}
	repositories := make([]string, len(units))
	for i, unit := range units {
		repositories[i] = firstString(unit.repository)
		for _, span := range unit.spans {
			edges = append(edges, edge{span[0], i, true}, edge{span[1], i, false})
		}
	}
	slices.SortStableFunc(edges, func(a, b edge) int { return cmp.Compare(a.at, b.at) })
	cells := map[parallelismCell]*parallelismMeasure{}
	active, perRepository := []int{}, map[string]int{}
	for i := 0; i < len(edges); {
		at := edges[i].at
		for ; i < len(edges) && edges[i].at == at; i++ {
			unit := edges[i].unit
			if edges[i].activating {
				active = append(active, unit)
				perRepository[repositories[unit]]++
			} else {
				active = slices.DeleteFunc(active, func(other int) bool { return other == unit })
				perRepository[repositories[unit]]--
			}
		}
		if i == len(edges) || len(active) == 0 {
			continue
		}
		level := len(active)
		for from, next := at, edges[i].at; from < next; {
			hour := localHour(from)
			to := min(next, hour+3_600_000)
			piece := float64(to - from)
			for _, unit := range active {
				repoLevel := perRepository[repositories[unit]]
				cell := parallelismCell{hour, unit, level, repoLevel}
				measure := cells[cell]
				if measure == nil {
					measure = &parallelismMeasure{}
					cells[cell] = measure
				}
				measure.active += to - from
				measure.elapsed += piece / float64(level)
				measure.repo += piece / float64(repoLevel)
				if level >= 2 {
					measure.parallelism += piece / float64(level)
				}
			}
			from = to
		}
	}
	return cells
}

type parallelismRow struct {
	parallelismCell
	parallelismMeasure
}

// parallelismTable is the dataset for one setting, compact: rows become maps
// per request, for its window and fields only.
type parallelismTable struct {
	settings parallelismSettings
	units    []parallelismUnit
	rows     []parallelismRow // by hour
}

func newParallelismTable(data *concurrencyData, settings parallelismSettings) *parallelismTable {
	table := &parallelismTable{settings: settings, units: parallelismUnits(data.Threads, settings)}
	for cell, measure := range parallelismCells(table.units) {
		table.rows = append(table.rows, parallelismRow{cell, *measure})
	}
	slices.SortFunc(table.rows, func(a, b parallelismRow) int {
		if a.hour != b.hour {
			return cmp.Compare(a.hour, b.hour)
		}
		if a.unit != b.unit {
			return strings.Compare(table.units[a.unit].id, table.units[b.unit].id)
		}
		if a.level != b.level {
			return a.level - b.level
		}
		return a.repoLevel - b.repoLevel
	})
	return table
}

// maps returns the rows whose hour starts within [from, to], with fields
// (nil: all of them).
func (table *parallelismTable) maps(fields libraryFields, from, to int64) []map[string]any {
	first, _ := slices.BinarySearchFunc(table.rows, from, func(row parallelismRow, at int64) int { return cmp.Compare(row.hour, at) })
	rows := make([]map[string]any, 0, len(table.rows)-first)
	type clock struct{ started, day, hour, week, month string }
	clocks := map[int64]clock{}
	for _, cell := range table.rows[first:] {
		if cell.hour > to {
			break
		}
		at, ok := clocks[cell.hour]
		if !ok {
			local := time.UnixMilli(cell.hour).In(time.Local)
			at = clock{local.Format(time.RFC3339), local.Format("2006-01-02"), local.Format("15"),
				local.AddDate(0, 0, -((int(local.Weekday()) + 6) % 7)).Format("2006-01-02"), local.Format("2006-01")}
			clocks[cell.hour] = at
		}
		unit, band := table.units[cell.unit], "4+"
		if cell.level < 4 {
			band = fmt.Sprint(cell.level)
		}
		values := map[string]any{"started_at": at.started, "day": at.day, "hour_of_day": at.hour, "week": at.week, "month": at.month,
			"counting": table.settings.counting, "gap_minutes": table.settings.gap,
			"unit_id": unit.id, "unit_label": unit.label, "repository_name": unit.repository, "workspace_name": nilIfEmpty(unit.workspace),
			"channel": nilIfEmpty(unit.channel), "machine_id": nilIfEmpty(unit.machineID), "execution_machine_id": nilIfEmpty(unit.executionMachineID),
			"location": nilIfEmpty(unit.location), "conversation_id": unit.conversation, "provider": unit.provider, "model": unit.model,
			"level": int64(cell.level), "level_band": band, "repo_level": int64(cell.repoLevel),
			"unit_ms": cell.active, "elapsed_ms": cell.elapsed, "repo_elapsed_ms": cell.repo, "parallel_elapsed_ms": cell.parallelism,
			"unit_hours": float64(cell.active) / 36e5, "elapsed_hours": cell.elapsed / 36e5, "repo_elapsed_hours": cell.repo / 36e5,
			"parallel_elapsed_hours": cell.parallelism / 36e5}
		if fields != nil {
			for name := range values {
				if !fields[name] {
					delete(values, name)
				}
			}
		}
		values["id"] = fmt.Sprintf("%s|%d|%d|%s", at.started, cell.level, cell.repoLevel, unit.id)
		rows = append(rows, values)
	}
	return rows
}

// parallelismRows are the whole parallelism dataset for settings: one row per
// local hour, unit, machine-wide level and repository level.
func parallelismRows(data *concurrencyData, settings parallelismSettings) []map[string]any {
	return newParallelismTable(data, settings).maps(nil, math.MinInt64, math.MaxInt64)
}

// parallelismWindow bounds the hours a request's AND terms on started_at can
// match: map rows are staged into SQLite per request, and these span 400 days.
// The engine still applies the whole WHERE. Relative values must already be
// resolved against the request's clock; anything else leaves a side open.
func parallelismWindow(where []querytable.WhereTerm) (from, to int64) {
	from, to = math.MinInt64, math.MaxInt64
	for _, term := range where {
		if term.IsGroup() || term.Negated || term.Field != "started_at" || strings.HasPrefix(term.Value, "+") || strings.HasPrefix(term.Value, "-") {
			continue
		}
		// The engine's datetime literals (sqliteParseTime).
		at, err := time.Parse(time.RFC3339Nano, term.Value)
		if err != nil {
			if at, err = time.Parse("2006-01-02", term.Value); err != nil {
				continue
			}
		}
		switch term.Op {
		case ">", ">=":
			from = max(from, at.UnixMilli())
		case "<", "<=":
			to = min(to, at.UnixMilli())
		case "=":
			from, to = max(from, at.UnixMilli()), min(to, at.UnixMilli())
		}
	}
	return from, to
}

// parallelismQueryRows feeds the query-table engine. Preference writes don't
// advance the catalog generation, so the key names the settings.
func (c *Catalog) parallelismQueryRows(ctx context.Context, fields libraryFields, where ...[]querytable.WhereTerm) ([]map[string]any, error) {
	settings := c.parallelismSettings(ctx)
	key := fmt.Sprintf("parallelism:%s:%s:%d:%s", c.clock().In(time.Local).Format("2006-01-02"), time.Local.String(), settings.gap, settings.counting)
	table, err := cachedValue(ctx, c, key, func(ctx context.Context) (*parallelismTable, error) {
		data, err := c.parallelismData(ctx)
		if err != nil {
			return nil, err
		}
		return newParallelismTable(data, settings), nil
	})
	if err != nil {
		return nil, err
	}
	from, to := int64(math.MinInt64), int64(math.MaxInt64)
	for _, terms := range where {
		a, b := parallelismWindow(terms)
		from, to = max(from, a), min(to, b)
	}
	if fields != nil {
		fields = fields.union(fieldSet("machine_id", "execution_machine_id"))
	}
	rows := table.maps(fields, from, to)
	if err := c.attachOriginMachineLabels(ctx, rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// mapRowsWhere is the row filter a map dataset may pre-select with, resolved
// against the request's clock: only parallelism's started_at window uses it.
func (s *Server) mapRowsWhere(dataset string, where []querytable.WhereTerm, clock time.Time) [][]querytable.WhereTerm {
	if dataset != "parallelism" || len(where) == 0 {
		return nil
	}
	schema, err := s.queryTableSchema(dataset)
	if err != nil {
		return nil
	}
	resolved, err := querytable.ResolveRelativeWhere(where, schema, clock)
	if err != nil {
		return nil
	}
	return [][]querytable.WhereTerm{resolved}
}
