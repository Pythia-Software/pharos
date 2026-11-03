package archive

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/gbdubs/pharos/internal/querytable"
)

// Rhythm is when you and your agents work, by local clock hour, and from it
// the Habits page's streaks, calendar, punchcard, workday shape and
// automation reliability (docs/habits-dashboard.md). It has four actors:
//
//   - you: your engaged time, estimated from the messages you typed. Each
//     typed message is preceded by the time its words take to type (40 words
//     a minute, one to ten minutes); messages at most the engaged gap apart
//     join one engaged block, and the time between them belongs to the next
//     message. A block ends shortly after its last message.
//   - with_you: agent turns you started, while you are engaged.
//   - unattended: agent turns you started, while you are not.
//   - automated: agent turns a script, schedule or another agent started.
//
// Agent time is Parallelism's (parallelismData): each top-level
// conversation's turns, from a prompt to its last response or tool result,
// with delegated runs inside their root's turns. A turn is attributed by the
// user message that started it, through authorship (message_authorship): a
// heartbeat or automation sender is scheduled, a headless run scripted, TL1
// orchestrated, another session's message an agent's. Authorship is often
// recorded on a mirror (Conductor's copy), so messages find their thread by
// the lineage's canonical conversation, and a turn takes the message sent at
// most rhythmMatchBefore before it (or rhythmMatchAfter after: the copies'
// clocks differ). A turn without one has an unknown initiator and counts as
// yours.
const (
	rhythmTypingMs     = 1500 // per typed word: 40 words a minute
	rhythmLeadMinMs    = 60_000
	rhythmLeadMaxMs    = 10 * 60_000
	rhythmTailMs       = 2 * 60_000
	rhythmMatchBefore  = 120_000
	rhythmMatchAfter   = 5_000
	rhythmFocusMs      = 25 * 60_000
	habitsSettingsKey  = "pharos-habits-v1"
	habitsMaxVacations = 1000
)

var rhythmEngagedGaps = []int64{5, 10, 15, 30}

const (
	actorYou        = "you"
	actorWithYou    = "with_you"
	actorUnattended = "unattended"
	actorAutomated  = "automated"
)

// rhythmKinds maps authorship's automated rules to an initiator and a label.
// The first rule a message holds, in this order, decides.
var rhythmKinds = []struct{ rule, initiator, label string }{
	{reasonAutomation.rule, "scheduled", ""},
	{reasonTL1.rule, "orchestrated", "TL1"},
	{reasonLaunched.rule, "scripted", "Launched by a session"},
	{reasonClaudePrint.rule, "scripted", "claude -p"},
	{reasonCodexExec.rule, "scripted", "codex exec"},
	{reasonAgyPrint.rule, "scripted", "agy -p"},
	{reasonUnrecordedClaude.rule, "scripted", "Claude SDK"},
	{reasonAgentSession.rule, "agent", "Another agent session"},
	{reasonSubAgent.rule, "delegated", "Sub-agent"},
}

type rhythmMessage struct {
	at                    int64
	conversation          string
	typed                 int64
	initiator, automation string
}

// rhythmMessages reads classified user messages since cutoff, with the
// initiator each implies.
func (c *Catalog) rhythmMessages(ctx context.Context, cutoff string) ([]rhythmMessage, error) {
	rows, err := c.reader().QueryContext(ctx, `SELECT ma.message_id,ma.conversation_id,ma.sent_at,ma.typed_words,ma.automated_words,COALESCE(ma.rules,'')
		FROM message_authorship ma WHERE ma.sent_at>=? ORDER BY ma.sent_at`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	messages, ids := []rhythmMessage{}, map[string]int{}
	for rows.Next() {
		var id, conversation, sent, rules string
		var typed, automated int64
		if err := rows.Scan(&id, &conversation, &sent, &typed, &automated, &rules); err != nil {
			return nil, err
		}
		at, ok := parseTime(sent)
		if !ok {
			continue
		}
		message := rhythmMessage{at: at.UnixMilli(), conversation: conversation, typed: typed, initiator: "human"}
		if typed == 0 && automated > 0 {
			var held []string
			_ = json.Unmarshal([]byte(rules), &held)
			for _, kind := range rhythmKinds {
				if slices.Contains(held, kind.rule) {
					message.initiator, message.automation = kind.initiator, kind.label
					break
				}
			}
		}
		ids[id] = len(messages)
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// A scheduled message names its automation in its span's reason.
	spans, err := c.reader().QueryContext(ctx, `SELECT s.message_id,s.reason FROM message_authorship_spans s
		JOIN message_authorship ma ON ma.message_id=s.message_id WHERE s.rule=? AND ma.sent_at>=?`, reasonAutomation.rule, cutoff)
	if err != nil {
		return nil, err
	}
	defer spans.Close()
	for spans.Next() {
		var id, reason string
		if err := spans.Scan(&id, &reason); err != nil {
			return nil, err
		}
		if at, ok := ids[id]; ok && messages[at].initiator == "scheduled" {
			messages[at].automation = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(reason, reasonAutomation.prefix), reasonAutomation.suffix))
		}
	}
	for i := range messages {
		if messages[i].initiator == "scheduled" && messages[i].automation == "" {
			messages[i].automation = "Automation"
		}
	}
	return messages, spans.Err()
}

// rhythmBlock is one engaged block: a run of typed messages at most the
// engaged gap apart.
type rhythmBlock struct {
	start, end int64
	messages   int
}

type rhythmPiece struct {
	span         [2]int64
	conversation string
}

// rhythmEngagement joins typed messages into engaged blocks, and splits each
// block into pieces belonging to the message they lead up to (the last piece,
// the tail, to the block's last message). Pieces never overlap.
func rhythmEngagement(typed []rhythmMessage, gap int64) ([]rhythmBlock, []rhythmPiece) {
	blocks, pieces := []rhythmBlock{}, []rhythmPiece{}
	tail := min(int64(rhythmTailMs), gap)
	var last *rhythmMessage
	closeBlock := func() {
		if last == nil {
			return
		}
		block := &blocks[len(blocks)-1]
		pieces = append(pieces, rhythmPiece{[2]int64{last.at, last.at + tail}, last.conversation})
		block.end = last.at + tail
	}
	for i := range typed {
		message := &typed[i]
		lead := min(max(message.typed*rhythmTypingMs, rhythmLeadMinMs), rhythmLeadMaxMs)
		start := message.at - lead
		// Messages join when they were sent within the gap; a typing estimate
		// never bridges a longer one.
		if last != nil && message.at-last.at <= gap {
			// The bridged gap is spent on the next message.
			start = last.at
			blocks[len(blocks)-1].messages++
		} else {
			if last != nil {
				// A long estimate starts no earlier than the last block ends.
				start = max(start, last.at+tail)
			}
			closeBlock()
			blocks = append(blocks, rhythmBlock{start: start, messages: 1})
		}
		if message.at > start {
			pieces = append(pieces, rhythmPiece{[2]int64{start, message.at}, message.conversation})
		}
		last = message
	}
	closeBlock()
	return blocks, pieces
}

// intersectSpans returns the parts of span inside and outside blocks (sorted,
// disjoint).
func intersectSpans(span [2]int64, blocks []rhythmBlock) (inside, outside parallelismSpans) {
	at := span[0]
	first, _ := slices.BinarySearchFunc(blocks, span[0], func(block rhythmBlock, t int64) int { return cmp.Compare(block.end, t) })
	for _, block := range blocks[first:] {
		if block.start >= span[1] {
			break
		}
		from, to := max(block.start, span[0]), min(block.end, span[1])
		if from >= to {
			continue
		}
		if from > at {
			outside = append(outside, [2]int64{at, from})
		}
		inside = append(inside, [2]int64{from, to})
		at = to
	}
	if at < span[1] {
		outside = append(outside, [2]int64{at, span[1]})
	}
	return inside, outside
}

type rhythmUnit struct {
	parallelismUnit
	actor, initiator, automation string
	starts                       []int64 // turns started, or typed messages sent
	typedWords                   []int64 // with starts, for you
}

type rhythmRow struct {
	hour               int64
	unit               int
	active             int64
	elapsed, parallel  float64
	peak               int
	starts, typedWords int64
}

// rhythmTable is the dataset under one engaged gap: units, their hourly
// rows, and the engaged blocks.
type rhythmTable struct {
	gap      int64 // minutes
	units    []rhythmUnit
	rows     []rhythmRow // by hour
	blocks   []rhythmBlock
	start    int64
	coverage struct {
		Turns, UnmatchedTurns, TypedMessages, Messages int
	}
}

// rhythmTurns splits the thread's occupancy back into turns. Its spans are
// merged where one turn's last response touches the next prompt, so each
// prompt inside a span (past the first's match window) starts a turn of its
// own, as parallelismTimeline began it.
func rhythmTurns(spans parallelismSpans, prompts []rhythmMessage) parallelismSpans {
	var turns parallelismSpans
	for _, span := range spans {
		start := span[0]
		first, _ := slices.BinarySearchFunc(prompts, span[0]+rhythmMatchAfter+1, func(message rhythmMessage, at int64) int { return cmp.Compare(message.at, at) })
		for _, prompt := range prompts[first:] {
			if prompt.at >= span[1] {
				break
			}
			if prompt.at-start > rhythmMatchAfter {
				turns = append(turns, [2]int64{start, prompt.at})
				start = prompt.at
			}
		}
		turns = append(turns, [2]int64{start, span[1]})
	}
	return turns
}

func newRhythmTable(data *concurrencyData, messages []rhythmMessage, gap int64) *rhythmTable {
	table := &rhythmTable{gap: gap, start: data.start}
	table.coverage.Messages = len(messages)
	threads := map[string]*concurrencyThread{}
	for i := range data.Threads {
		threads[data.Threads[i].ID] = &data.Threads[i]
	}
	canonical := func(id string) string {
		if to := data.canonical[id]; to != "" {
			return to
		}
		return id
	}
	// The thread a conversation's messages count toward: its canonical
	// conversation, and that conversation's root.
	threadOf := func(conversation string) *concurrencyThread {
		thread := threads[canonical(conversation)]
		if thread != nil && thread.RootConversationID != "" && threads[thread.RootConversationID] != nil {
			return threads[thread.RootConversationID]
		}
		return thread
	}
	byThread, typed := map[string][]rhythmMessage{}, []rhythmMessage{}
	for _, message := range messages {
		if message.at < data.start || message.at >= data.end {
			continue
		}
		byThread[canonical(message.conversation)] = append(byThread[canonical(message.conversation)], message)
		if message.initiator == "human" && message.typed > 0 {
			typed = append(typed, message)
		}
	}
	table.coverage.TypedMessages = len(typed)
	blocks, pieces := rhythmEngagement(typed, gap*60_000)
	table.blocks = blocks

	index := map[string]int{}
	unitFor := func(actor, initiator, automation, conversation string, thread *concurrencyThread) int {
		key := actor + "\x00" + initiator + "\x00" + automation + "\x00" + conversation
		if at, ok := index[key]; ok {
			return at
		}
		unit := rhythmUnit{actor: actor, initiator: initiator, automation: automation}
		unit.id, unit.conversation = conversation, conversation
		if thread != nil {
			unit.label, unit.workspace, unit.location, unit.repository = thread.Title, thread.WorkspaceName, thread.location, nilIfEmpty(thread.Repository)
			unit.provider, unit.model = thread.rootProvider, thread.rootModel
			unit.channel, unit.machineID, unit.executionMachineID = thread.Channel, thread.MachineID, thread.ExecutionMachineID
		}
		index[key] = len(table.units)
		table.units = append(table.units, unit)
		return index[key]
	}

	// Your engaged time, in the conversation each piece leads up to.
	for _, piece := range pieces {
		thread := threadOf(piece.conversation)
		conversation := piece.conversation
		if thread != nil {
			conversation = defaultString(thread.RootConversationID, thread.ID)
		}
		at := unitFor(actorYou, "human", "", conversation, thread)
		table.units[at].spans = append(table.units[at].spans, piece.span)
	}
	for _, message := range typed {
		thread := threadOf(message.conversation)
		conversation := message.conversation
		if thread != nil {
			conversation = defaultString(thread.RootConversationID, thread.ID)
		}
		at := unitFor(actorYou, "human", "", conversation, thread)
		table.units[at].starts = append(table.units[at].starts, message.at)
		table.units[at].typedWords = append(table.units[at].typedWords, message.typed)
	}

	// Agent turns, by who started them and whether you were engaged.
	for i := range data.Threads {
		thread := &data.Threads[i]
		if thread.AgentDepth > 0 {
			continue
		}
		root := defaultString(thread.RootConversationID, thread.ID)
		own := byThread[thread.ID]
		for _, turn := range rhythmTurns(thread.Turns, own) {
			table.coverage.Turns++
			initiator, automation := "unknown", ""
			// The message nearest the turn's start, within the match window.
			first, _ := slices.BinarySearchFunc(own, turn[0]-rhythmMatchBefore, func(message rhythmMessage, at int64) int { return cmp.Compare(message.at, at) })
			best := int64(math.MaxInt64)
			for _, message := range own[first:] {
				if message.at > turn[0]+rhythmMatchAfter {
					break
				}
				if distance := abs64(message.at - turn[0]); distance < best {
					best, initiator, automation = distance, message.initiator, message.automation
				}
			}
			if initiator == "unknown" {
				table.coverage.UnmatchedTurns++
			}
			if initiator != "human" && initiator != "unknown" {
				at := unitFor(actorAutomated, initiator, automation, root, thread)
				table.units[at].spans = append(table.units[at].spans, turn)
				table.units[at].starts = append(table.units[at].starts, turn[0])
				continue
			}
			inside, outside := intersectSpans(turn, blocks)
			for _, part := range []struct {
				actor string
				spans parallelismSpans
			}{{actorWithYou, inside}, {actorUnattended, outside}} {
				if len(part.spans) == 0 {
					continue
				}
				at := unitFor(part.actor, initiator, "", root, thread)
				table.units[at].spans = append(table.units[at].spans, part.spans...)
			}
			// The turn starts with you or without you.
			actor := actorUnattended
			if len(inside) > 0 && inside[0][0] == turn[0] {
				actor = actorWithYou
			}
			at := unitFor(actor, initiator, "", root, thread)
			table.units[at].starts = append(table.units[at].starts, turn[0])
		}
	}

	// Sweep agents and you separately: agents at once is a count of agents,
	// and your pieces never overlap.
	rows := map[[2]int64]*rhythmRow{}
	for _, group := range []bool{true, false} {
		members, base := []int{}, []parallelismUnit{}
		for i := range table.units {
			if (table.units[i].actor == actorYou) == group {
				table.units[i].spans = mergeSpans(table.units[i].spans, 0)
				members = append(members, i)
				// The sweep keeps repository levels too; they are not used here.
				unit := table.units[i].parallelismUnit
				unit.repository = nil
				base = append(base, unit)
			}
		}
		for cell, measure := range parallelismCells(base) {
			unit := members[cell.unit]
			key := [2]int64{cell.hour, int64(unit)}
			row := rows[key]
			if row == nil {
				row = &rhythmRow{hour: cell.hour, unit: unit}
				rows[key] = row
			}
			row.active += measure.active
			row.elapsed += measure.elapsed
			row.parallel += measure.parallelism
			row.peak = max(row.peak, cell.level)
		}
	}
	for i, unit := range table.units {
		for j, at := range unit.starts {
			key := [2]int64{localHour(at), int64(i)}
			row := rows[key]
			if row == nil {
				row = &rhythmRow{hour: key[0], unit: i}
				rows[key] = row
			}
			row.starts++
			if unit.typedWords != nil {
				row.typedWords += unit.typedWords[j]
			}
		}
	}
	for _, row := range rows {
		table.rows = append(table.rows, *row)
	}
	slices.SortFunc(table.rows, func(a, b rhythmRow) int {
		if a.hour != b.hour {
			return cmp.Compare(a.hour, b.hour)
		}
		return cmp.Compare(a.unit, b.unit)
	})
	return table
}

func abs64(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}

// localClock names a local hour's calendar fields.
type localClock struct{ started, day, hour, week, month string }

func newLocalClock(hour int64) localClock {
	local := time.UnixMilli(hour).In(time.Local)
	return localClock{local.Format(time.RFC3339), local.Format("2006-01-02"), local.Format("15"),
		local.AddDate(0, 0, -((int(local.Weekday()) + 6) % 7)).Format("2006-01-02"), local.Format("2006-01")}
}

// maps returns the rows whose hour starts within [from, to], with fields
// (nil: all of them).
func (table *rhythmTable) maps(fields libraryFields, from, to int64) []map[string]any {
	first, _ := slices.BinarySearchFunc(table.rows, from, func(row rhythmRow, at int64) int { return cmp.Compare(row.hour, at) })
	rows := make([]map[string]any, 0, len(table.rows)-first)
	clocks := map[int64]localClock{}
	for _, row := range table.rows[first:] {
		if row.hour > to {
			break
		}
		at, ok := clocks[row.hour]
		if !ok {
			at = newLocalClock(row.hour)
			clocks[row.hour] = at
		}
		unit := table.units[row.unit]
		var automation any
		if unit.automation != "" {
			automation = unit.automation
		}
		var peak any
		if unit.actor != actorYou && row.peak > 0 {
			peak = int64(row.peak)
		}
		values := map[string]any{"started_at": at.started, "day": at.day, "hour_of_day": at.hour, "week": at.week, "month": at.month,
			"engaged_gap_minutes": table.gap, "actor": unit.actor, "initiator": unit.initiator, "automation": automation,
			"unit_id": unit.id, "unit_label": nilIfEmpty(unit.label), "repository_name": unit.repository, "workspace_name": nilIfEmpty(unit.workspace),
			"conversation_id": unit.conversation, "provider": unit.provider, "model": unit.model,
			"channel": nilIfEmpty(unit.channel), "machine_id": nilIfEmpty(unit.machineID), "execution_machine_id": nilIfEmpty(unit.executionMachineID),
			"active_ms": row.active, "elapsed_ms": row.elapsed, "parallel_elapsed_ms": row.parallel, "peak_level": peak,
			"active_hours": float64(row.active) / 36e5, "elapsed_hours": row.elapsed / 36e5, "parallel_elapsed_hours": row.parallel / 36e5,
			"messages": nil, "typed_words": nil, "turns": nil}
		if unit.actor == actorYou {
			values["messages"], values["typed_words"] = row.starts, row.typedWords
		} else {
			values["turns"] = row.starts
		}
		if fields != nil {
			for name := range values {
				if !fields[name] {
					delete(values, name)
				}
			}
		}
		values["id"] = fmt.Sprintf("%s|%s|%s|%s|%s", at.started, unit.actor, unit.initiator, unit.automation, unit.id)
		rows = append(rows, values)
	}
	return rows
}

// habitsSettings are the Habits page's saved choices: what makes an active
// day, the engaged gap, when a day starts, and vacation days.
type habitsSettings struct {
	Vacations    []string `json:"vacations"`
	YouMessages  int64    `json:"youMessages"`
	AgentMinutes int64    `json:"agentMinutes"`
	EngagedGap   int64    `json:"engagedGap"`
	DayStart     int      `json:"dayStart"`
}

var defaultHabitsSettings = habitsSettings{Vacations: []string{}, YouMessages: 1, AgentMinutes: 10, EngagedGap: 15, DayStart: 0}

// normalizeHabitsSettings keeps what is valid of a saved or linked value
// (web/src/habits-model.ts normalizeHabitsSettings).
func normalizeHabitsSettings(text string) habitsSettings {
	settings := defaultHabitsSettings
	var saved map[string]any
	if json.Unmarshal([]byte(text), &saved) != nil {
		return settings
	}
	whole := func(key string, low, high float64) (int64, bool) {
		value, ok := saved[key].(float64)
		return int64(value), ok && value == math.Trunc(value) && value >= low && value <= high
	}
	if value, ok := whole("youMessages", 1, 1000); ok {
		settings.YouMessages = value
	}
	if value, ok := whole("agentMinutes", 1, 24*60); ok {
		settings.AgentMinutes = value
	}
	if value, ok := whole("engagedGap", 0, 60); ok && slices.Contains(rhythmEngagedGaps, value) {
		settings.EngagedGap = value
	}
	if value, ok := whole("dayStart", 0, 6); ok {
		settings.DayStart = int(value)
	}
	if days, ok := saved["vacations"].([]any); ok {
		seen := map[string]bool{}
		for _, day := range days {
			text, _ := day.(string)
			if _, err := time.Parse("2006-01-02", text); err == nil && !seen[text] && len(settings.Vacations) < habitsMaxVacations {
				seen[text] = true
				settings.Vacations = append(settings.Vacations, text)
			}
		}
		slices.Sort(settings.Vacations)
	}
	return settings
}

type habitsPreferencesKey struct{}

// withHabitsPreferences applies a link's settings to a request in place of
// the saved ones, as Parallelism's do.
func withHabitsPreferences(ctx context.Context, encoded string) (context.Context, error) {
	if encoded == "" {
		return ctx, nil
	}
	if len(encoded) > 64*1024 || !json.Valid([]byte(encoded)) {
		return ctx, fmt.Errorf("invalid Habits settings")
	}
	return context.WithValue(ctx, habitsPreferencesKey{}, normalizeHabitsSettings(encoded)), nil
}

func (c *Catalog) habitsSettings(ctx context.Context) habitsSettings {
	if linked, ok := ctx.Value(habitsPreferencesKey{}).(habitsSettings); ok {
		return linked
	}
	var text string
	if c.reader().QueryRowContext(ctx, "SELECT value FROM ui_preferences WHERE key=?", habitsSettingsKey).Scan(&text) != nil {
		return defaultHabitsSettings
	}
	return normalizeHabitsSettings(text)
}

// rhythm is the table under the engaged gap, kept until the catalog changes.
func (c *Catalog) rhythm(ctx context.Context, gap int64) (*rhythmTable, error) {
	now := c.clock().In(time.Local)
	key := fmt.Sprintf("rhythm:%s:%s:%d", now.Format("2006-01-02"), time.Local.String(), gap)
	return cachedValue(ctx, c, key, func(ctx context.Context) (*rhythmTable, error) {
		data, err := c.parallelismData(ctx)
		if err != nil {
			return nil, err
		}
		messages, err := c.rhythmMessages(ctx, time.UnixMilli(data.start).UTC().Format(time.RFC3339))
		if err != nil {
			return nil, err
		}
		return newRhythmTable(data, messages, gap), nil
	})
}

// rhythmQueryRows feeds the query-table engine.
func (c *Catalog) rhythmQueryRows(ctx context.Context, fields libraryFields, where ...[]querytable.WhereTerm) ([]map[string]any, error) {
	table, err := c.rhythm(ctx, c.habitsSettings(ctx).EngagedGap)
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
