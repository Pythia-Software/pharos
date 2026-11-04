package archive

// Session-shape definitions are adapted from AgentsView (MIT License,
// Copyright (c) 2026 Kenn Software LLC; https://github.com/kenn-io/agentsview):
// its length, duration and autonomy bands, the 300 s active-gap and 1800 s
// turn-cycle caps, retry runs, edit churn clusters, the mid-task compaction
// overlap and the runaway-loop and context-pressure thresholds. The
// verify-separated retry is adapted from CodeBurn (MIT License, Copyright (c)
// 2026 AgentSeal; https://github.com/getagentseal/codeburn), and the tool-call
// bands and nearest-rank percentile from Splitrail (MIT License, Copyright (c)
// 2025 Piebald LLC; https://github.com/Piebald-AI/splitrail). See
// THIRD_PARTY_NOTICES.md and docs/session-shape.md.

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gbdubs/pharos/internal/querytable"
)

const (
	// sessionShapeVersion names the derivation stored in session_shape_facts.
	// Changing it marks every conversation for recomputation.
	sessionShapeVersion = "5"
	sessionShapeBatch   = 200
	// A gap between consecutive messages longer than this is idle time.
	sessionShapeActiveGap = 300.0
	// A turn cycle longer than this is a person away, not the loop's speed.
	sessionShapeTurnCycleCap = 1800.0
	// A run of tool calls without typed human input this long is a long run.
	sessionShapeLongRun = 150
	// A peak context above this share of the window is context pressure.
	sessionShapePressure = 0.9
	// Retries need this many identical calls in a row.
	sessionShapeRetryRun = 3
	// Churn: this many edits of one file within fewer than this many messages.
	sessionShapeChurnEdits, sessionShapeChurnSpan = 3, 10
	// Mid-task compaction: tool calls looked at before and after a compaction,
	// and the distinct tool names both windows must share.
	sessionShapeCompactBefore, sessionShapeCompactAfter, sessionShapeCompactShared = 10, 5, 2
)

// sessionShapeSignals are the forensic signals with evidence, in the order the
// reader's strip and the docs list them. forensic_events sums their counts.
var sessionShapeSignals = []string{"failed_calls", "failure_streak", "identical_retries", "thrash_retries", "churned_files",
	"mid_task_compactions", "context_pressure", "corrections", "interrupts", "rejections", "hook_blocks",
	"unverified_handbacks", "ended_on_failure", "long_unprompted_runs"}

// shapeMessage is one of a session's own messages, metadata excluded.
type shapeMessage struct {
	id, role, kind string
	at             int64 // Unix ms; valid only when timed
	timed          bool
	blank          bool // user prose with no text, which Human Words skips
	// User prose with authorship: whether it holds typed text, and whether
	// that typed text has a correction cue.
	authored, typed, correction bool
	typedWords                  int64
}

// shapeCall is one of a session's own tool calls, in ledger order.
type shapeCall struct {
	message                                         string // the call's message ID
	position                                        int    // the call message's index in the session's messages
	tool, category, commandCategory, status, errors string
	file                                            string
	input                                           string // hash of the call's tool and input
	interrupted, testFailure                        bool
}

type shapeRequest struct {
	at    int64 // Unix ms of the request's first event
	input int64 // input tokens, cached included: the context occupied
	model string
}

// sessionShapeInput is everything one session's shape is derived from.
type sessionShapeInput struct {
	messages []shapeMessage
	// compactions are the message positions compaction markers precede, with
	// the markers' IDs.
	compactions []shapeCompaction
	calls       []shapeCall
	// toolMessages counts the session's tool-call messages, so a session with
	// calls but no ledger rows reads as missing tool data, not as no calls.
	toolMessages int
	requests     []shapeRequest
	window       int64 // reported context window; 0 when none was reported
	model        string
}

type shapeCompaction struct {
	id       string
	position int
}

// sessionShapeFacts are one session's stored facts. A nil pointer is a value
// without enough evidence, never zero.
type sessionShapeFacts struct {
	StartedAt              string   `json:"started_at,omitempty"`
	LastMessageAt          string   `json:"last_message_at,omitempty"`
	Messages               int64    `json:"messages"`
	TypedHumanMessages     *int64   `json:"typed_human_messages"`
	TypedWords             *int64   `json:"typed_words"`
	ToolCalls              *int64   `json:"tool_calls"`
	WallSeconds            *float64 `json:"wall_seconds"`
	ActiveSeconds          *float64 `json:"active_seconds"`
	TurnCycles             *int64   `json:"turn_cycles"`
	TurnCycleP50           *float64 `json:"turn_cycle_p50_seconds"`
	TurnCycleP90           *float64 `json:"turn_cycle_p90_seconds"`
	FirstResponse          *float64 `json:"first_response_seconds"`
	MaxUnpromptedToolCalls *int64   `json:"max_unprompted_tool_calls"`
	FilesEdited            *int64   `json:"files_edited"`
	Compactions            int64    `json:"compactions"`
	FailedCalls            *int64   `json:"failed_calls"`
	Rejections             *int64   `json:"rejections"`
	Interrupts             *int64   `json:"interrupts"`
	HookBlocks             *int64   `json:"hook_blocks"`
	FailureStreakMax       *int64   `json:"failure_streak_max"`
	IdenticalRetries       *int64   `json:"identical_retries"`
	ThrashRetries          *int64   `json:"thrash_retries"`
	EditTurns              *int64   `json:"edit_turns"`
	OneShotEditTurns       *int64   `json:"one_shot_edit_turns"`
	ChurnedFiles           *int64   `json:"churned_files"`
	MidTaskCompactions     *int64   `json:"mid_task_compactions"`
	ContextPeakTokens      *int64   `json:"context_peak_tokens"`
	ContextWindow          *int64   `json:"context_window"`
	ContextPeakRatio       *float64 `json:"context_peak_ratio"`
	ContextPressure        *int64   `json:"context_pressure"`
	Corrections            *int64   `json:"corrections"`
	VerifiedHandbacks      *int64   `json:"verified_handbacks"`
	EditHandbacks          *int64   `json:"edit_handbacks"`
	EndedOnFailure         *int64   `json:"ended_on_failure"`
	LongUnpromptedRuns     *int64   `json:"long_unprompted_runs"`
	HasToolData            bool     `json:"has_tool_data"`
	HasTimestamps          bool     `json:"has_timestamps"`
	HasAuthorship          bool     `json:"has_authorship"`
	HasContextWindow       bool     `json:"has_context_window"`
}

// sessionShapeEvidence lists, per signal, the messages that triggered it.
type sessionShapeEvidence map[string][]string

func shapeCount(n int) *int64 {
	value := int64(n)
	return &value
}

func shapeSeconds(value float64) *float64 { return &value }

// nearestRank is the p-quantile of sorted values by nearest rank, as
// Splitrail's percentile.
func nearestRank(sorted []float64, p float64) float64 {
	return sorted[min(len(sorted)-1, max(0, int(math.Ceil(p*float64(len(sorted))))-1))]
}

// verifies reports a call that checks work: a test, build or lint command
// (command_category test or build; package scripts named test, lint, check or
// typecheck are test), or any command the ledger saw fail a test. Read-only
// commands (read, search, list, version control) and everything else are not.
func (call shapeCall) verifies() bool {
	return call.category == "command" && (call.commandCategory == "test" || call.commandCategory == "build" || call.testFailure)
}

// edits reports an edit-family call that was not refused or failed.
func (call shapeCall) edits() bool {
	return call.category == "edit" && call.file != "" && call.status != "error"
}

// outcome sorts a call: a real failure, or one of the cases a person or the
// harness caused (rejected, interrupted, blocked by a hook), or "".
func (call shapeCall) outcome() string {
	switch {
	case call.errors == "user_rejected":
		return "rejections"
	case call.errors == "hook_blocked":
		return "hook_blocks"
	case call.errors == "interrupted" || call.interrupted:
		return "interrupts"
	case call.status == "error":
		return "failed_calls"
	}
	return ""
}

// deriveSessionShape computes one session's facts and evidence. It is pure:
// the tests drive it directly, and readSessionShapeInput feeds it.
func deriveSessionShape(in sessionShapeInput) (sessionShapeFacts, sessionShapeEvidence) {
	facts, evidence := sessionShapeFacts{Messages: int64(len(in.messages)), Compactions: int64(len(in.compactions))}, sessionShapeEvidence{}
	note := func(signal string, ids ...string) {
		for _, id := range ids {
			if id != "" {
				evidence[signal] = append(evidence[signal], id)
			}
		}
	}

	// Timing. Messages arrive in timestamp order; untimed ones sort last.
	var timed []shapeMessage
	users, authored := 0, 0
	for _, message := range in.messages {
		if message.timed {
			timed = append(timed, message)
		}
		// Human Words classifies the user prose it can place in time.
		if message.role == "user" && message.kind == "message" && message.timed && !message.blank {
			users++
			if message.authored {
				authored++
			}
		}
	}
	if len(timed) > 0 {
		facts.HasTimestamps = true
		facts.StartedAt = time.UnixMilli(timed[0].at).UTC().Format(timeLayout)
		facts.LastMessageAt = time.UnixMilli(timed[len(timed)-1].at).UTC().Format(timeLayout)
		active := 0.0
		for i := 1; i < len(timed); i++ {
			if gap := float64(timed[i].at-timed[i-1].at) / 1000; gap > 0 && gap <= sessionShapeActiveGap {
				active += gap
			}
		}
		facts.WallSeconds, facts.ActiveSeconds = shapeSeconds(float64(timed[len(timed)-1].at-timed[0].at)/1000), shapeSeconds(active)
	}

	// Authorship. Turns start at each typed human message; pasted, quoted,
	// re-sent, template, harness and automated text does not start one.
	// Until every such message is classified (a new one waits for Human
	// Words), an unclassified prompt would hide a turn, so nothing that
	// depends on authorship is known.
	facts.HasAuthorship = authored == users
	turnAt := make([]int, len(in.messages)) // each message's turn
	typedAt := []int{}                      // positions of typed human messages
	for i, message := range in.messages {
		if facts.HasAuthorship && message.typed {
			typedAt = append(typedAt, i)
		}
		turnAt[i] = len(typedAt)
	}
	if facts.HasAuthorship {
		facts.TypedHumanMessages = shapeCount(len(typedAt))
		words := int64(0)
		for _, i := range typedAt {
			words += in.messages[i].typedWords
		}
		facts.TypedWords = &words
		corrections := 0
		for _, i := range typedAt {
			if in.messages[i].correction {
				corrections++
				note("corrections", in.messages[i].id)
			}
		}
		facts.Corrections = shapeCount(corrections)
		if facts.HasTimestamps {
			cycles := []float64{}
			for n, i := range typedAt {
				prompt := in.messages[i]
				if !prompt.timed {
					continue
				}
				for _, next := range in.messages[i+1:] {
					if next.role == "user" && next.kind == "message" {
						break
					}
					if next.role != "assistant" {
						continue
					}
					if gap := float64(next.at-prompt.at) / 1000; next.timed && gap > 0 && gap <= sessionShapeTurnCycleCap {
						cycles = append(cycles, gap)
						if n == 0 {
							facts.FirstResponse = shapeSeconds(gap)
						}
					}
					break
				}
			}
			facts.TurnCycles = shapeCount(len(cycles))
			if len(cycles) > 0 {
				slices.Sort(cycles)
				facts.TurnCycleP50, facts.TurnCycleP90 = shapeSeconds(nearestRank(cycles, 0.5)), shapeSeconds(nearestRank(cycles, 0.9))
			}
		}
	}

	// Context: the peak request input over the window the reader's context
	// rail uses (contextWindow in reader-model.js): the reported one, else a
	// Claude model's 200K, or 1M when the model says [1m] or context grew past
	// 200K. Other models' windows are unknown.
	peak, peakAt := int64(0), int64(0)
	claude := strings.Contains(strings.ToLower(in.model), "claude") || claudeModelPattern.MatchString(in.model)
	oneMillion := strings.Contains(strings.ToLower(in.model), "[1m]")
	for _, request := range in.requests {
		if request.input > peak {
			peak, peakAt = request.input, request.at
		}
		claude = claude || claudeModelPattern.MatchString(request.model)
		oneMillion = oneMillion || strings.Contains(strings.ToLower(request.model), "[1m]")
	}
	window := in.window
	if window == 0 && claude {
		window = 200_000
		if oneMillion || peak > 200_000 {
			window = 1_000_000
		}
	}
	if peak > 0 {
		facts.ContextPeakTokens = &peak
		if window > 0 {
			facts.HasContextWindow = true
			facts.ContextWindow, facts.ContextPeakRatio = &window, shapeSeconds(float64(peak)/float64(window))
			pressure := 0
			if *facts.ContextPeakRatio > sessionShapePressure {
				pressure = 1
				// The first of the session's messages at or after the peak request.
				for _, message := range in.messages {
					if message.timed && message.at >= peakAt && message.role == "assistant" {
						note("context_pressure", message.id)
						break
					}
				}
			}
			facts.ContextPressure = shapeCount(pressure)
		}
	}

	// Tool data: present when the ledger holds the session's calls, or when
	// the session made none.
	facts.HasToolData = len(in.calls) > 0 || in.toolMessages == 0
	if !facts.HasToolData {
		return facts, evidence
	}
	calls := in.calls
	facts.ToolCalls = shapeCount(len(calls))
	outcomes := map[string]int{"failed_calls": 0, "rejections": 0, "interrupts": 0, "hook_blocks": 0}
	streak, longest, longestEnd := 0, 0, -1
	for i, call := range calls {
		kind := call.outcome()
		if kind != "" {
			outcomes[kind]++
			note(kind, call.message)
		}
		if kind == "failed_calls" {
			if streak++; streak > longest {
				longest, longestEnd = streak, i
			}
		} else {
			streak = 0
		}
	}
	facts.FailedCalls, facts.Rejections = shapeCount(outcomes["failed_calls"]), shapeCount(outcomes["rejections"])
	facts.Interrupts, facts.HookBlocks = shapeCount(outcomes["interrupts"]), shapeCount(outcomes["hook_blocks"])
	facts.FailureStreakMax = shapeCount(longest)
	if longest >= 2 {
		for _, call := range calls[longestEnd-longest+1 : longestEnd+1] {
			note("failure_streak", call.message)
		}
	}

	// Identical retries: runs of 3+ consecutive calls with the same tool and
	// byte-identical input; each run adds its length less one.
	retries := 0
	for start, i := 0, 1; i <= len(calls); i++ {
		if i < len(calls) && calls[i].tool == calls[start].tool && calls[i].input == calls[start].input {
			continue
		}
		if n := i - start; n >= sessionShapeRetryRun {
			retries += n - 1
			for _, call := range calls[start+1 : i] {
				note("identical_retries", call.message)
			}
		}
		start = i
	}
	facts.IdenticalRetries = shapeCount(retries)

	// Files edited, and churn: 3+ edits of one file whose message positions
	// span fewer than 10, extended over the edits that follow while the span
	// stays under 10 (AgentsView's churnCluster). One per file.
	files, order := map[string][]shapeCall{}, []string{}
	for _, call := range calls {
		if call.edits() {
			if files[call.file] == nil {
				order = append(order, call.file)
			}
			files[call.file] = append(files[call.file], call)
		}
	}
	facts.FilesEdited = shapeCount(len(order))
	churned := 0
	for _, file := range order {
		edits := files[file]
		for i := 0; i+sessionShapeChurnEdits <= len(edits); i++ {
			lo, hi := edits[i].position, edits[i].position
			for _, edit := range edits[i+1 : i+sessionShapeChurnEdits] {
				lo, hi = min(lo, edit.position), max(hi, edit.position)
			}
			if hi-lo >= sessionShapeChurnSpan {
				continue
			}
			end := i + sessionShapeChurnEdits - 1
			for end+1 < len(edits) && max(hi, edits[end+1].position)-min(lo, edits[end+1].position) < sessionShapeChurnSpan {
				lo, hi, end = min(lo, edits[end+1].position), max(hi, edits[end+1].position), end+1
			}
			churned++
			for _, edit := range edits[i : end+1] {
				note("churned_files", edit.message)
			}
			break
		}
	}
	facts.ChurnedFiles = shapeCount(churned)

	// Mid-task compactions: a compaction where 2+ distinct tool names appear
	// both among the 10 calls before it and the 5 after it.
	midTask := 0
	for _, compaction := range in.compactions {
		split, _ := slices.BinarySearchFunc(calls, compaction.position, func(call shapeCall, position int) int { return cmp.Compare(call.position, position) })
		before, after := map[string]bool{}, map[string]bool{}
		for _, call := range calls[max(0, split-sessionShapeCompactBefore):split] {
			before[call.tool] = true
		}
		for _, call := range calls[split:min(len(calls), split+sessionShapeCompactAfter)] {
			if before[call.tool] {
				after[call.tool] = true
			}
		}
		if len(after) >= sessionShapeCompactShared {
			midTask++
			note("mid_task_compactions", compaction.id)
		}
	}
	facts.MidTaskCompactions = shapeCount(midTask)

	if !facts.HasAuthorship {
		return facts, evidence
	}
	// Autonomy and long runs: tool calls per typed human message.
	runs, longRuns, run, previousTurn := 0, 0, 0, -1
	for _, call := range calls {
		if turn := turnAt[call.position]; turn != previousTurn {
			run, previousTurn = 0, turn
		}
		if run++; run > runs {
			runs = run
		}
		if run == sessionShapeLongRun {
			longRuns++
			note("long_unprompted_runs", call.message)
		}
	}
	facts.MaxUnpromptedToolCalls, facts.LongUnpromptedRuns = shapeCount(runs), shapeCount(longRuns)

	// Thrash: within one turn, an edit of F, then a verification, then another
	// edit of F (CodeBurn's countRetries, with Pharos's command categories).
	// Each turn with an edit is an edit turn; one without thrash is one-shot.
	type turnState struct {
		lastEdit           map[string]int
		lastVerify         int
		edited, thrashed   bool
		lastEditAt         int
		verifiedAfterEdits bool
	}
	turns := map[int]*turnState{}
	state := func(turn int) *turnState {
		if turns[turn] == nil {
			turns[turn] = &turnState{lastEdit: map[string]int{}, lastVerify: -1, lastEditAt: -1}
		}
		return turns[turn]
	}
	thrash := 0
	for i, call := range calls {
		turn := state(turnAt[call.position])
		if call.verifies() {
			turn.lastVerify = i
			if turn.edited {
				turn.verifiedAfterEdits = true
			}
		}
		if call.edits() {
			if previous, ok := turn.lastEdit[call.file]; ok && turn.lastVerify > previous {
				thrash++
				turn.thrashed = true
				note("thrash_retries", call.message)
			}
			turn.lastEdit[call.file], turn.edited, turn.lastEditAt, turn.verifiedAfterEdits = i, true, i, false
		}
	}
	facts.ThrashRetries = shapeCount(thrash)
	editTurns, oneShot := 0, 0
	for _, turn := range turns {
		if turn.edited {
			editTurns++
			if !turn.thrashed {
				oneShot++
			}
		}
	}
	facts.EditTurns, facts.OneShotEditTurns = shapeCount(editTurns), shapeCount(oneShot)

	// Handbacks: the last assistant message of a turn that edited (prose if
	// there is any), verified when a verification ran after its last edit.
	handback := map[int]string{}
	prose := map[int]bool{}
	for i, message := range in.messages {
		if message.role != "assistant" || prose[turnAt[i]] && message.kind != "message" {
			continue
		}
		handback[turnAt[i]], prose[turnAt[i]] = message.id, prose[turnAt[i]] || message.kind == "message"
	}
	verified, handbacks := 0, 0
	for number, turn := range turns {
		if !turn.edited || handback[number] == "" {
			continue
		}
		handbacks++
		if turn.verifiedAfterEdits {
			verified++
		} else {
			note("unverified_handbacks", handback[number])
		}
	}
	facts.VerifiedHandbacks, facts.EditHandbacks = shapeCount(verified), shapeCount(handbacks)

	// Ended on a failed call (experimental): the last call failed and nothing
	// human-typed follows it.
	ended := 0
	if len(calls) > 0 && calls[len(calls)-1].outcome() == "failed_calls" && turnAt[calls[len(calls)-1].position] == len(typedAt) {
		last := calls[len(calls)-1]
		ended = 1
		note("ended_on_failure", last.message)
	}
	facts.EndedOnFailure = shapeCount(ended)
	return facts, evidence
}

var (
	claudeModelPattern  = regexp.MustCompile(`(?i)claude|opus|sonnet|haiku|fable`)
	contextWindowFigure = regexp.MustCompile(`"model_context_window":\s*(\d+)`)
	lastUsageInput      = regexp.MustCompile(`^"last_token_usage":\s*\{[^}]*"input_tokens":\s*(\d+)`)
)

// readSessionShapeInput reads one conversation's inputs in q's snapshot. Like
// Parallelism, messages owned by an embedded sub-agent belong to it, not to
// the conversation; the index supplies created_at, and role and kind precede
// the message text.
func readSessionShapeInput(ctx context.Context, q queryer, id string) (sessionShapeInput, bool, error) {
	in := sessionShapeInput{}
	conversation, err := queryMapsContext(ctx, q, `SELECT COALESCE(NULLIF(c.model,''),(SELECT a.model FROM agent_sessions a WHERE a.conversation_id=c.id AND a.native_id='main' LIMIT 1)) model
 FROM conversations c WHERE c.id=?`, id)
	if err != nil || len(conversation) == 0 {
		return in, false, err
	}
	in.model = firstString(conversation[0]["model"])
	owned := map[string]bool{}
	rows, err := queryMapsContext(ctx, q, `SELECT am.message_id FROM agent_sessions a JOIN agent_session_messages am ON am.agent_session_id=a.id
 WHERE a.conversation_id=? AND a.native_id<>'main'`, id)
	if err != nil {
		return in, false, err
	}
	for _, row := range rows {
		owned[firstString(row["message_id"])] = true
	}
	// Compaction markers are the Library's (compaction_count); a reported
	// context window is Codex's model_context_window. A Codex token_count
	// checkpoint's last_token_usage is the context its request occupied, as on
	// the reader's context rail; its model_requests row holds a delta of
	// cumulative usage instead, which can span earlier requests.
	rows, err = queryMapsContext(ctx, q, `SELECT m.id,m.role,m.kind,m.created_at,
 m.role='user' AND m.kind='message' AND trim(m.text,' '||char(9,10,13))='' blank,
 m.kind='metadata' AND (m.text LIKE '%"subtype":"compact_boundary"%' OR m.text LIKE '%"compaction_trigger":"%') compaction,
 CASE WHEN m.kind='metadata' AND instr(m.text,'"model_context_window"')>0 THEN substr(m.text,instr(m.text,'"model_context_window"'),40) END window,
 CASE WHEN m.kind='metadata' AND instr(m.text,'"last_token_usage"')>0 THEN substr(m.text,instr(m.text,'"last_token_usage"'),400) END last_usage
 FROM messages m INDEXED BY messages_conversation_idx WHERE m.conversation_id=? ORDER BY m.created_at IS NULL,m.created_at,m.rowid`, id)
	if err != nil {
		return in, false, err
	}
	positions := map[string]int{}
	checkpoints, checkpointed := []shapeRequest{}, false
	for _, row := range rows {
		messageID := firstString(row["id"])
		if owned[messageID] {
			continue
		}
		if match := contextWindowFigure.FindStringSubmatch(firstString(row["window"])); match != nil {
			if window, err := strconv.ParseInt(match[1], 10, 64); err == nil {
				in.window = max(in.window, window)
			}
		}
		if match := lastUsageInput.FindStringSubmatch(firstString(row["last_usage"])); match != nil {
			checkpointed = true
			if input, err := strconv.ParseInt(match[1], 10, 64); err == nil && input > 0 {
				checkpoint := shapeRequest{input: input}
				if at, ok := parseTime(firstString(row["created_at"])); ok {
					checkpoint.at = at.UnixMilli()
				}
				checkpoints = append(checkpoints, checkpoint)
			}
		}
		kind := firstString(row["kind"])
		if kind == "metadata" {
			if integer(row["compaction"]) != 0 {
				in.compactions = append(in.compactions, shapeCompaction{id: messageID, position: len(in.messages)})
			}
			continue
		}
		message := shapeMessage{id: messageID, role: firstString(row["role"]), kind: kind, blank: integer(row["blank"]) != 0}
		if at, ok := parseTime(firstString(row["created_at"])); ok {
			message.at, message.timed = at.UnixMilli(), true
		}
		if kind == "tool_call" || kind == "delegation" {
			in.toolMessages++
		}
		positions[messageID] = len(in.messages)
		in.messages = append(in.messages, message)
	}
	rows, err = queryMapsContext(ctx, q, `SELECT ma.message_id,ma.typed_words>0 typed,COALESCE(ma.typed_words,0) typed_words,`+authoredCueSQL(correctionCuePhrases)+` correction
 FROM message_authorship ma JOIN messages m ON m.id=ma.message_id WHERE ma.conversation_id=?`, id)
	if err != nil {
		return in, false, err
	}
	for _, row := range rows {
		if at, ok := positions[firstString(row["message_id"])]; ok {
			message := &in.messages[at]
			message.authored, message.typed, message.correction = true, integer(row["typed"]) != 0, integer(row["correction"]) != 0
			message.typedWords = integer(row["typed_words"])
		}
	}
	rows, err = queryMapsContext(ctx, q, `SELECT t.call_message_id,t.tool_name,t.tool_category,COALESCE(t.command_category,'') command_category,t.status,
 COALESCE(t.error_type,'') error_type,t.interrupted,t.test_failure,COALESCE(t.file_path,'') file_path,m.content_hash
 FROM tool_calls t LEFT JOIN messages m ON m.id=t.call_message_id WHERE t.conversation_id=? ORDER BY t.sequence`, id)
	if err != nil {
		return in, false, err
	}
	for _, row := range rows {
		message := firstString(row["call_message_id"])
		position, ok := positions[message]
		if !ok {
			// An embedded sub-agent's call, or one whose message is gone.
			continue
		}
		in.calls = append(in.calls, shapeCall{message: message, position: position, tool: firstString(row["tool_name"]), category: firstString(row["tool_category"]),
			commandCategory: firstString(row["command_category"]), status: firstString(row["status"]), errors: firstString(row["error_type"]),
			file: firstString(row["file_path"]), input: firstString(row["content_hash"]), interrupted: integer(row["interrupted"]) != 0, testFailure: integer(row["test_failure"]) != 0})
	}
	// The ledger orders calls as their messages appear; timestamps can disagree.
	slices.SortStableFunc(in.calls, func(a, b shapeCall) int { return cmp.Compare(a.position, b.position) })
	rows, err = queryMapsContext(ctx, q, `SELECT mr.requested_at,mr.input_tokens,COALESCE(mr.model,'') model,mr.native_id IS NULL anonymous FROM model_requests mr
 WHERE mr.conversation_id=? AND COALESCE(mr.agent_session_id,'') NOT IN (SELECT id FROM agent_sessions WHERE conversation_id=? AND native_id<>'main')
 ORDER BY mr.sequence`, id, id)
	if err != nil {
		return in, false, err
	}
	in.requests = checkpoints
	for _, row := range rows {
		if checkpointed && integer(row["anonymous"]) != 0 {
			// A checkpoint's usage delta; its checkpoint is the sample.
			continue
		}
		request := shapeRequest{input: integer(row["input_tokens"]), model: firstString(row["model"])}
		if checkpointed {
			// Codex request rows, native or not, can hold cumulative usage;
			// with checkpoints, even ones reporting no input, a row names
			// only its model.
			request.input = 0
		}
		if at, ok := parseTime(firstString(row["requested_at"])); ok {
			request.at = at.UnixMilli()
		}
		in.requests = append(in.requests, request)
	}
	slices.SortStableFunc(in.requests, func(a, b shapeRequest) int { return cmp.Compare(a.at, b.at) })
	return in, true, nil
}

// ensureSessionShape marks every conversation dirty when the derivation
// changes, so the stored facts are rebuilt (incrementally) under it.
func (c *Catalog) ensureSessionShape() error {
	var version string
	err := c.DB.QueryRow("SELECT value FROM meta WHERE key='session_shape_version'").Scan(&version)
	if err != nil && err != sql.ErrNoRows || version == sessionShapeVersion {
		return err
	}
	tx, err := c.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, statement := range []string{"DELETE FROM session_shape_facts", "DELETE FROM session_shape_evidence",
		"INSERT INTO session_shape_dirty SELECT id,'' FROM conversations WHERE true ON CONFLICT(conversation_id) DO NOTHING",
		"INSERT INTO meta(key,value) VALUES('session_shape_version','" + sessionShapeVersion + "') ON CONFLICT(key) DO UPDATE SET value=excluded.value",
	} {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type sessionShapeDerived struct {
	id, revision string
	facts        *sessionShapeFacts // nil: the conversation is gone
	evidence     sessionShapeEvidence
}

func deriveSessionShapeOf(ctx context.Context, q queryer, id string) (*sessionShapeFacts, sessionShapeEvidence, error) {
	in, found, err := readSessionShapeInput(ctx, q, id)
	if err != nil || !found {
		return nil, nil, err
	}
	facts, evidence := deriveSessionShape(in)
	return &facts, evidence, nil
}

func deriveSessionShapeBatch(ctx context.Context, q queryer, limit int) ([]sessionShapeDerived, error) {
	dirty, err := queryMapsContext(ctx, q, "SELECT conversation_id,revision FROM session_shape_dirty LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	result := make([]sessionShapeDerived, 0, len(dirty))
	for _, row := range dirty {
		derived := sessionShapeDerived{id: firstString(row["conversation_id"]), revision: asString(row["revision"])}
		if derived.facts, derived.evidence, err = deriveSessionShapeOf(ctx, q, derived.id); err != nil {
			return nil, err
		}
		result = append(result, derived)
	}
	return result, nil
}

// Publishing an old derivation is harmless: its conversation stays dirty
// unless the token it read is still current.
func (c *Catalog) publishSessionShape(ctx context.Context, batch []sessionShapeDerived) error {
	return c.writeTransaction(ctx, "session-shape", func(tx *sql.Tx) error {
		for _, derived := range batch {
			for _, statement := range []string{"DELETE FROM session_shape_facts WHERE conversation_id=?", "DELETE FROM session_shape_evidence WHERE conversation_id=?"} {
				if _, err := tx.ExecContext(ctx, statement, derived.id); err != nil {
					return err
				}
			}
			if derived.facts != nil {
				encoded, err := json.Marshal(derived.facts)
				if err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, "INSERT INTO session_shape_facts(conversation_id,facts) VALUES(?,?)", derived.id, string(encoded)); err != nil {
					return err
				}
				for signal, ids := range derived.evidence {
					encoded, err := json.Marshal(ids)
					if err != nil {
						return err
					}
					if _, err := tx.ExecContext(ctx, "INSERT INTO session_shape_evidence(conversation_id,signal,message_ids) VALUES(?,?,?)", derived.id, signal, string(encoded)); err != nil {
						return err
					}
				}
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM session_shape_dirty WHERE conversation_id=? AND revision=?", derived.id, derived.revision); err != nil {
				return err
			}
		}
		return nil
	})
}

// refreshSessionShape stores up to limit dirty conversations' facts, in short
// read snapshots and write transactions, and reports how many. Unless asked to
// wait, it leaves the work to a caller already doing it.
func (c *Catalog) refreshSessionShape(ctx context.Context, limit int, wait bool) (int, error) {
	if wait {
		c.sessionShapeMu.Lock()
	} else if !c.sessionShapeMu.TryLock() {
		return 0, nil
	}
	defer c.sessionShapeMu.Unlock()
	done := 0
	for done < limit {
		tx, err := c.reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return done, err
		}
		batch, err := deriveSessionShapeBatch(ctx, tx, min(limit-done, sessionShapeBatch))
		_ = tx.Rollback()
		if err != nil || len(batch) == 0 {
			return done, err
		}
		if err := c.publishSessionShape(ctx, batch); err != nil {
			return done, err
		}
		done += len(batch)
	}
	return done, nil
}

// sessionShapeAllFacts returns every conversation's facts: stored facts for
// clean conversations and fresh ones for dirty conversations. Storing the
// dirty ones is left to maintainLibrary, so a page never writes.
func (c *Catalog) sessionShapeAllFacts(ctx context.Context) (map[string]sessionShapeFacts, error) {
	tx, err := c.reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	dirty, err := deriveSessionShapeBatch(ctx, tx, -1)
	if err != nil {
		return nil, err
	}
	result, skip := map[string]sessionShapeFacts{}, map[string]bool{}
	for _, derived := range dirty {
		skip[derived.id] = true
		if derived.facts != nil {
			result[derived.id] = *derived.facts
		}
	}
	rows, err := tx.QueryContext(ctx, "SELECT conversation_id,facts FROM session_shape_facts")
	if err != nil {
		return nil, err
	}
	unreadable := []string{}
	for rows.Next() {
		var id, text string
		if err := rows.Scan(&id, &text); err != nil {
			rows.Close()
			return nil, err
		}
		if skip[id] {
			continue
		}
		var facts sessionShapeFacts
		if json.Unmarshal([]byte(text), &facts) != nil {
			unreadable = append(unreadable, id)
			continue
		}
		result[id] = facts
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Stored facts are disposable; derive any damaged ones again.
	for _, id := range unreadable {
		facts, _, err := deriveSessionShapeOf(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if facts != nil {
			result[id] = *facts
		}
	}
	return result, nil
}

// sessionShapeOf returns one conversation's facts and evidence: stored, or
// derived afresh while the conversation is dirty or its stored copy is
// damaged. Facts are nil when the conversation is gone.
func (c *Catalog) sessionShapeOf(ctx context.Context, id string) (*sessionShapeFacts, sessionShapeEvidence, error) {
	tx, err := c.reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	var dirty bool
	var text sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM session_shape_dirty WHERE conversation_id=?1),
 (SELECT facts FROM session_shape_facts WHERE conversation_id=?1)`, id).Scan(&dirty, &text); err != nil {
		return nil, nil, err
	}
	var facts sessionShapeFacts
	if dirty || !text.Valid || json.Unmarshal([]byte(text.String), &facts) != nil {
		return deriveSessionShapeOf(ctx, tx, id)
	}
	rows, err := queryMapsContext(ctx, tx, "SELECT signal,message_ids FROM session_shape_evidence WHERE conversation_id=?", id)
	if err != nil {
		return nil, nil, err
	}
	evidence := sessionShapeEvidence{}
	for _, row := range rows {
		var ids []string
		if json.Unmarshal([]byte(firstString(row["message_ids"])), &ids) != nil {
			return deriveSessionShapeOf(ctx, tx, id)
		}
		evidence[firstString(row["signal"])] = ids
	}
	return &facts, evidence, nil
}

// Bands are fixed, ordered labels, so a share of sessions in each band is
// comparable across weeks.
func lengthBand(messages int64) string {
	switch {
	case messages <= 5:
		return "1-5"
	case messages <= 15:
		return "6-15"
	case messages <= 30:
		return "16-30"
	case messages <= 60:
		return "31-60"
	case messages <= 120:
		return "61-120"
	}
	return "121+"
}

func activeBand(seconds float64) string {
	switch minutes := seconds / 60; {
	case minutes < 5:
		return "<5m"
	case minutes < 15:
		return "5-15m"
	case minutes < 30:
		return "15-30m"
	case minutes < 60:
		return "30-60m"
	case minutes < 120:
		return "1-2h"
	}
	return "2h+"
}

func toolBand(calls int64) string {
	switch {
	case calls == 0:
		return "0"
	case calls <= 5:
		return "1-5"
	case calls <= 20:
		return "6-20"
	case calls <= 50:
		return "21-50"
	case calls <= 100:
		return "51-100"
	}
	return "101+"
}

// autonomyBand uses AgentsView's AutonomyBucket edges.
func autonomyBand(ratio float64) string {
	switch {
	case ratio < 0.5:
		return "<0.5"
	case ratio < 1:
		return "0.5-1"
	case ratio < 2:
		return "1-2"
	case ratio < 5:
		return "2-5"
	case ratio < 10:
		return "5-10"
	}
	return "10+"
}

func sessionArchetype(typed int64) string {
	switch {
	case typed == 0:
		return "automated"
	case typed <= 5:
		return "quick"
	case typed <= 15:
		return "standard"
	case typed <= 50:
		return "deep"
	}
	return "marathon"
}

func pointerValue[T int64 | float64](value *T) any {
	if value == nil {
		return nil
	}
	return *value
}

// sessionShapeRecord is one session's row before a request selects fields.
// started is its Unix ms, for windowing; the rows are sorted by it.
type sessionShapeRecord struct {
	started int64
	values  map[string]any
}

// sessionShapeTable is the dataset, kept until the catalog changes.
type sessionShapeTable struct {
	records []sessionShapeRecord
}

// sessionShapeRows is the session_shape dataset: one row per agent session.
// Mirrors are removed as Parallelism removes them, by workspace
// (suppressMirrors) and by identity link (usageConversationRoots' canonical
// conversation), so a session is counted once; each row carries its root.
func (c *Catalog) sessionShapeTable(ctx context.Context) (*sessionShapeTable, error) {
	now := c.clock().In(time.Local)
	return cachedValue(ctx, c, "session-shape:"+now.Format("2006-01-02")+":"+time.Local.String(), func(ctx context.Context) (*sessionShapeTable, error) {
		facts, err := c.sessionShapeAllFacts(ctx)
		if err != nil {
			return nil, err
		}
		return c.newSessionShapeTable(ctx, facts)
	})
}

func (c *Catalog) newSessionShapeTable(ctx context.Context, facts map[string]sessionShapeFacts) (*sessionShapeTable, error) {
	conversations, err := queryMapsContext(ctx, c.reader(), `SELECT c.id,c.workspace_id,c.provider,c.harness,c.agent_depth,
 COALESCE(NULLIF(c.model,''),(SELECT a.model FROM agent_sessions a WHERE a.conversation_id=c.id AND a.native_id='main' LIMIT 1)) model,
 COALESCE(NULLIF(c.effort,''),(SELECT a.effort FROM agent_sessions a WHERE a.conversation_id=c.id AND a.native_id='main' LIMIT 1)) effort,
 w.title,w.source_kind,w.activity_at,r.display_name repository_name
 FROM conversations c JOIN workspaces w ON w.id=c.workspace_id LEFT JOIN repositories r ON r.id=w.repository_id
 WHERE `+currentGroupConversation("c")+` ORDER BY c.id`)
	if err != nil {
		return nil, err
	}
	candidates, seen := []map[string]any{}, map[string]bool{}
	for _, conversation := range conversations {
		if id := firstString(conversation["workspace_id"]); !seen[id] {
			seen[id] = true
			candidates = append(candidates, map[string]any{"id": id, "source_kind": conversation["source_kind"], "activity_at": conversation["activity_at"]})
		}
	}
	kept, keptRows := map[string]bool{}, []map[string]any{}
	for _, row := range c.suppressMirrors(candidates) {
		kept[firstString(row["id"])] = true
		keptRows = append(keptRows, map[string]any{"workspace_id": row["id"]})
	}
	lineage, err := c.usageConversationRoots(keptRows)
	if err != nil {
		return nil, err
	}
	// Delegated runs this session launched: embedded and linked sub-agent
	// sessions, and headless runs its shell commands started.
	subagents := map[string]int64{}
	rows, err := queryMapsContext(ctx, c.reader(), `SELECT conversation_id id,COUNT(*) n FROM agent_sessions WHERE native_id<>'main' GROUP BY conversation_id
 UNION ALL SELECT parent_id,COUNT(*) FROM conversation_launches GROUP BY parent_id`)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		subagents[firstString(row["id"])] += integer(row["n"])
	}
	// Tokens and cost are Usage's, priced the same way, summed per session.
	usage, err := c.localUsageBaseRows(ctx, false)
	if err != nil {
		return nil, err
	}
	type tokens struct {
		input, output int64
		cost          any
		status        string
	}
	spent := map[string]*tokens{}
	for _, row := range usage {
		id := firstString(row["conversation_id"])
		total := spent[id]
		if total == nil {
			total = &tokens{}
			spent[id] = total
		}
		total.input += integer(row["input_tokens"])
		total.output += integer(row["output_tokens"])
		if cost, ok := number(row["cost_usd"]); ok {
			total.cost = addCost(total.cost, &cost)
		}
		total.status = worsePriceStatus(total.status, firstString(row["price_status"]))
	}
	origins, err := c.conversationOrigins(ctx)
	if err != nil {
		return nil, err
	}
	table := &sessionShapeTable{}
	for _, conversation := range conversations {
		id := firstString(conversation["id"])
		shape, ok := facts[id]
		if !kept[firstString(conversation["workspace_id"])] || lineage.canonical[id] != id || !ok || shape.Messages == 0 {
			continue
		}
		at, _ := parseTime(shape.StartedAt)
		row := sessionShapeRow(shape)
		depth := integer(conversation["agent_depth"])
		if lineage.runs[id] != "" {
			depth = max(depth, 1)
		}
		kind := "root"
		if depth > 0 {
			kind = "subagent"
		}
		model := firstString(conversation["model"])
		for field, value := range map[string]any{"id": id, "conversation_id": id, "workspace_id": conversation["workspace_id"], "title": conversation["title"],
			"repository_name": conversation["repository_name"], "provider": conversation["provider"], "harness": conversation["harness"],
			"model": nilIfEmpty(model), "model_family": nilIfEmpty(modelFamily(defaultString(nilIfEmpty(model), "Unknown model"))), "effort": nilIfEmpty(firstString(conversation["effort"])),
			"agent_depth": depth, "agent_kind": kind, "subagents": subagents[id], "input_tokens": int64(0), "output_tokens": int64(0), "cost_usd": nil, "price_status": nil} {
			row[field] = value
		}
		if root := lineage.roots[id]; root != nil {
			row["root_conversation_id"] = root["id"]
		}
		if total := spent[id]; total != nil {
			row["input_tokens"], row["output_tokens"], row["cost_usd"], row["price_status"] = total.input, total.output, total.cost, nilIfEmpty(total.status)
		}
		if !at.IsZero() {
			local := at.In(time.Local)
			midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.Local)
			row["started_at"], row["day"], row["month"] = shape.StartedAt, midnight.Format("2006-01-02"), midnight.Format("2006-01")
			row["last_message_at"] = nilIfEmpty(shape.LastMessageAt)
			row["week"] = midnight.AddDate(0, 0, -((int(midnight.Weekday()) + 6) % 7)).Format("2006-01-02")
		}
		copyOrigin(row, origins[id])
		table.records = append(table.records, sessionShapeRecord{started: at.UnixMilli(), values: row})
	}
	slices.SortStableFunc(table.records, func(a, b sessionShapeRecord) int { return cmp.Compare(a.started, b.started) })
	return table, nil
}

// sessionShapeRow turns stored facts into the dataset's measures, bands and
// coverage.
func sessionShapeRow(shape sessionShapeFacts) map[string]any {
	row := map[string]any{"messages": shape.Messages, "compactions": shape.Compactions, "length_band": lengthBand(shape.Messages),
		"has_tool_data": shape.HasToolData, "has_timestamps": shape.HasTimestamps, "has_authorship": shape.HasAuthorship, "has_context_window": shape.HasContextWindow,
		"active_band": nil, "tool_band": nil, "autonomy_band": nil, "archetype": nil, "autonomy": nil, "autonomy_prompts": nil,
		"idle_seconds": nil, "active_hours": nil, "unverified_handbacks": nil, "forensic_events": nil}
	for field, value := range map[string]any{
		"typed_human_messages": pointerValue(shape.TypedHumanMessages), "typed_words": pointerValue(shape.TypedWords), "tool_calls": pointerValue(shape.ToolCalls),
		"wall_seconds": pointerValue(shape.WallSeconds), "active_seconds": pointerValue(shape.ActiveSeconds),
		"turn_cycles": pointerValue(shape.TurnCycles), "turn_cycle_p50_seconds": pointerValue(shape.TurnCycleP50),
		"turn_cycle_p90_seconds": pointerValue(shape.TurnCycleP90), "first_response_seconds": pointerValue(shape.FirstResponse),
		"max_unprompted_tool_calls": pointerValue(shape.MaxUnpromptedToolCalls), "files_edited": pointerValue(shape.FilesEdited),
		"failed_calls": pointerValue(shape.FailedCalls), "rejections": pointerValue(shape.Rejections), "interrupts": pointerValue(shape.Interrupts),
		"hook_blocks": pointerValue(shape.HookBlocks), "failure_streak_max": pointerValue(shape.FailureStreakMax),
		"identical_retries": pointerValue(shape.IdenticalRetries), "thrash_retries": pointerValue(shape.ThrashRetries),
		"edit_turns": pointerValue(shape.EditTurns), "one_shot_edit_turns": pointerValue(shape.OneShotEditTurns),
		"churned_files": pointerValue(shape.ChurnedFiles), "mid_task_compactions": pointerValue(shape.MidTaskCompactions),
		"context_peak_tokens": pointerValue(shape.ContextPeakTokens), "context_window": pointerValue(shape.ContextWindow),
		"context_peak_ratio": pointerValue(shape.ContextPeakRatio), "context_pressure": pointerValue(shape.ContextPressure),
		"corrections": pointerValue(shape.Corrections), "verified_handbacks": pointerValue(shape.VerifiedHandbacks),
		"edit_handbacks": pointerValue(shape.EditHandbacks), "ended_on_failure": pointerValue(shape.EndedOnFailure),
		"long_unprompted_runs": pointerValue(shape.LongUnpromptedRuns),
	} {
		row[field] = value
	}
	if shape.ActiveSeconds != nil {
		row["active_band"], row["active_hours"] = activeBand(*shape.ActiveSeconds), *shape.ActiveSeconds/3600
		row["idle_seconds"] = *shape.WallSeconds - *shape.ActiveSeconds
	}
	if shape.ToolCalls != nil {
		row["tool_band"] = toolBand(*shape.ToolCalls)
	}
	if shape.TypedHumanMessages != nil {
		row["archetype"] = sessionArchetype(*shape.TypedHumanMessages)
		if shape.ToolCalls != nil {
			prompts := max(1, *shape.TypedHumanMessages)
			ratio := float64(*shape.ToolCalls) / float64(prompts)
			row["autonomy"], row["autonomy_prompts"], row["autonomy_band"] = ratio, prompts, autonomyBand(ratio)
		}
	}
	if shape.EditHandbacks != nil {
		row["unverified_handbacks"] = *shape.EditHandbacks - *shape.VerifiedHandbacks
	}
	// Every count with evidence, failure_streak_max aside (its calls are
	// failed calls already).
	events, known := int64(0), false
	for _, signal := range sessionShapeSignals {
		if value, ok := row[signal].(int64); ok {
			events, known = events+value, true
		}
	}
	if known {
		row["forensic_events"] = events
	}
	return row
}

// maps returns the records started within [from, to], with fields (nil: all
// of them). Records without a start fall outside any bounded window.
func (table *sessionShapeTable) maps(fields libraryFields, from, to int64) []map[string]any {
	rows := []map[string]any{}
	for _, record := range table.records {
		if from != math.MinInt64 && record.started < from || to != math.MaxInt64 && record.started > to {
			continue
		}
		row := make(map[string]any, len(record.values))
		for name, value := range record.values {
			if fields == nil || fields[name] {
				row[name] = value
			}
		}
		row["id"] = record.values["id"]
		rows = append(rows, row)
	}
	return rows
}

// sessionShapeQueryRows feeds the query-table engine, windowed by any AND
// terms on started_at.
func (c *Catalog) sessionShapeQueryRows(ctx context.Context, fields libraryFields, where ...[]querytable.WhereTerm) ([]map[string]any, error) {
	table, err := c.sessionShapeTable(ctx)
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
