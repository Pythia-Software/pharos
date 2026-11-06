package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

// zedAdapter reads the threads of Zed's own agent: Zed's data folder
// (~/Library/Application Support/Zed), or its threads database. Zed keeps
// every thread in threads/threads.db, one row a thread, its content one JSON
// document (data), compressed with zstd when data_type says so. A subagent's
// thread names the thread that started it (parent_id). Threads with external
// agents (Claude Code, Codex and others through ACP) are kept by those agents
// themselves, which Pharos reads as their own sources.
type zedAdapter struct {
	baseAdapter
	// selected, when set, limits reading to the groups holding these threads
	// (snapshotSessions).
	selected map[string]bool
}

// zedExtractor versions the parser; bump it when parsing changes.
const zedExtractor = "zed-v1"

func (a *zedAdapter) partExtractor() string { return zedExtractor }

// zedCaptureTables are the tables a capture keeps: the threads, not tables a
// later release adds.
var zedCaptureTables = map[string]captureTable{"threads": {}}

// zedUserDir is Zed's data folder, under the home folder.
const zedUserDir = "Library/Application Support/Zed"

// zedDatabase is where Zed keeps its threads, under its data folder.
const zedDatabase = "threads/threads.db"

// database is Zed's threads database: the source itself when it is a file.
func (a *zedAdapter) database() (string, error) {
	info, err := os.Stat(a.config.Path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return a.config.Path, nil
	}
	database := filepath.Join(a.config.Path, filepath.FromSlash(zedDatabase))
	if _, err := os.Stat(database); errors.Is(err, os.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	return database, nil
}

func (a *zedAdapter) Fingerprint() (string, error) {
	database, err := a.database()
	if err != nil {
		return "", err
	}
	version := int64(0)
	if database != "" {
		version = a.databaseVersion(database)
	}
	return fmt.Sprintf("%s:%d", zedExtractor, version), nil
}

func (a *zedAdapter) Discover(emit func(WorkspaceRecord) error) error {
	return a.discoverParts(nil, func(record *WorkspaceRecord, _ []sourcePart) error {
		if record == nil {
			return nil
		}
		return emit(*record)
	})
}

func (a *zedAdapter) discoverParts(unchanged func([]sourcePart) bool, emit func(*WorkspaceRecord, []sourcePart) error) error {
	defer a.shareSQLiteCopies()()
	plan, err := a.planParts()
	if err != nil {
		return err
	}
	return plan.discover(a.context(), unchanged, emit)
}

// zedThread is a thread's row: its identity and how far it has changed.
type zedThread struct {
	id, parent, updated, dataType, summary string
	size                                   int64
}

// open opens the database for reading in one transaction.
func (a *zedAdapter) open(database string) (*sql.DB, *sql.Tx, error) {
	readable, err := a.files().Database(database)
	if err != nil {
		return nil, nil, err
	}
	db, err := openSourceSQLite(a.context(), readable)
	if err != nil {
		return nil, nil, err
	}
	tx, err := db.BeginTx(a.context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	return db, tx, nil
}

// zedParentColumn is the column naming a thread's parent, in releases that
// have one.
func zedParentColumn(tx interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, ctx context.Context) (string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT name FROM pragma_table_info('threads')`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return "", err
		}
		if name == "parent_id" {
			return "COALESCE(parent_id, '')", nil
		}
	}
	return "''", rows.Err()
}

// threads lists the database's threads.
func (a *zedAdapter) threads(database string) ([]zedThread, error) {
	db, tx, err := a.open(database)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	defer tx.Rollback()
	tables, err := tableNames(tx)
	if err != nil || !tables["threads"] {
		return nil, err
	}
	parent, err := zedParentColumn(tx, a.context())
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(a.context(), `SELECT id, `+parent+`, updated_at, data_type, length(data), summary FROM threads ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	threads := []zedThread{}
	for rows.Next() {
		var thread zedThread
		if err := rows.Scan(&thread.id, &thread.parent, &thread.updated, &thread.dataType, &thread.size, &thread.summary); err != nil {
			return nil, err
		}
		threads = append(threads, thread)
	}
	return threads, rows.Err()
}

// zedTime is a time Zed keeps as RFC 3339 text, as Unix nanoseconds.
func zedTime(value string) int64 {
	at, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return 0
	}
	return at.UnixNano()
}

// planParts plans one group per thread that started its own work, holding it
// and the subagents it started; each thread is one part. Zed rewrites a
// thread's row whenever it is saved, with the time it was saved.
func (a *zedAdapter) planParts() (*sourceWorkPlan, error) {
	database, err := a.database()
	if err != nil {
		return nil, err
	}
	plan := &sourceWorkPlan{continueOnError: true}
	if database == "" {
		return plan, nil
	}
	threads, err := a.threads(database)
	if err != nil {
		return nil, err
	}
	byID := map[string]zedThread{}
	for _, thread := range threads {
		byID[thread.id] = thread
	}
	// A group is the work its top thread started, and keeps that thread's
	// workspace once Zed deletes it: a subagent still names it.
	root := func(id string) string {
		seen := map[string]bool{}
		for !seen[id] {
			seen[id] = true
			thread, ok := byID[id]
			if !ok || thread.parent == "" {
				break
			}
			id = thread.parent
		}
		return id
	}
	groups := map[string][]zedThread{}
	roots := []string{}
	for _, thread := range threads {
		top := root(thread.id)
		if groups[top] == nil {
			roots = append(roots, top)
		}
		groups[top] = append(groups[top], thread)
	}
	for _, top := range roots {
		members := groups[top]
		// A selection reads each group holding one of its threads whole: the
		// group's workspace and each thread's indexed version keep an older
		// snapshot from rolling back what is newer.
		if a.selected != nil && !slices.ContainsFunc(members, func(member zedThread) bool { return a.selected[member.id] }) {
			continue
		}
		// The root first, then its subagents a level at a time, each level
		// by ID, so a subagent follows the one that started it.
		levels := map[string]int{}
		for _, member := range members {
			id, seen := member.id, map[string]bool{}
			for id != top && !seen[id] {
				seen[id] = true
				id = byID[id].parent
				levels[member.id]++
			}
		}
		sort.SliceStable(members, func(i, j int) bool { return levels[members[i].id] < levels[members[j].id] })
		parts := make([]sourcePart, 0, len(members))
		for _, member := range members {
			// Renaming a thread leaves its time as it was, and can leave its
			// size; its title is also in summary.
			parts = append(parts, sourcePart{item: a.original(database) + "#" + member.id, size: member.size, version: zedTime(member.updated),
				signal: fmt.Sprintf("%s:%s:%d:%s:%s", member.updated, member.dataType, member.size, member.parent, hashBytes([]byte(member.summary))[:16])})
		}
		plan.groups = append(plan.groups, plannedPartGroup{parts: parts, read: func(emit partEmitter) error {
			record, damaged, err := a.read(database, top, members)
			if err != nil {
				return fmt.Errorf("Zed thread %s: %w", top, err)
			}
			// A thread that can't be read leaves the others in its group:
			// they are indexed, and it is reported and read again next time.
			read := parts
			if len(damaged) > 0 {
				read = slices.DeleteFunc(slices.Clone(parts), func(part sourcePart) bool {
					return damaged[strings.TrimPrefix(part.item, a.original(database)+"#")] != nil
				})
			}
			if record != nil {
				if err := emit(record, read); err != nil {
					return err
				}
			}
			failures := []error{}
			for _, member := range members {
				if err := damaged[member.id]; err != nil {
					failures = append(failures, fmt.Errorf("Zed thread %s: %w", member.id, err))
				}
			}
			return errors.Join(failures...)
		}})
	}
	return plan, nil
}

// snapshotSessions lists the threads in a database snapshot. A superseded
// snapshot's that no newer one holds were deleted in Zed, and are read from
// it alone.
func (a *zedAdapter) snapshotSessions(ctx context.Context, file string) (map[string]bool, error) {
	db, err := openReadOnlySQLite(file)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	tables, err := tableNames(db)
	if err != nil || !tables["threads"] {
		return map[string]bool{}, err
	}
	rows, err := queryMapsContext(ctx, db, `SELECT id FROM threads`)
	ids := map[string]bool{}
	for _, row := range rows {
		ids[firstString(row["id"])] = true
	}
	return ids, err
}

func (a *zedAdapter) olderReader(file string, view *captureView, selected map[string]bool) Adapter {
	config := a.config
	config.Path = file
	return &zedAdapter{baseAdapter: baseAdapter{config: config, capability: a.capability, view: view}, selected: selected}
}

// readGroup reads, as indexing does, the group whose parts begin as parts
// do; nothing when that thread is gone.
func (a *zedAdapter) readGroup(parts []sourcePart) (*WorkspaceRecord, []sourcePart, error) {
	defer a.shareSQLiteCopies()()
	plan, err := a.planParts()
	if err != nil || len(parts) == 0 {
		return nil, nil, err
	}
	for _, group := range plan.groups {
		if group.parts[0].item != parts[0].item {
			continue
		}
		var record *WorkspaceRecord
		var read []sourcePart
		err := group.read(func(value *WorkspaceRecord, parts []sourcePart) error { record, read = value, parts; return nil })
		return record, read, err
	}
	return nil, nil, plan.discoveryError
}

// zedDecoder decodes zstd-compressed threads.
var zedDecoder, _ = zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(256<<20))

// zedThreadData decodes a thread's data.
func zedThreadData(dataType string, data []byte) (map[string]any, error) {
	switch strings.ToLower(dataType) {
	case "zstd":
		decoded, err := zedDecoder.DecodeAll(data, nil)
		if err != nil {
			return nil, err
		}
		data = decoded
	case "json":
	default:
		return nil, fmt.Errorf("data of type %q", dataType)
	}
	value := map[string]any{}
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	return value, nil
}

// read reads a group's threads in one transaction: the work its top thread
// started, and the error of each thread that can't be read.
func (a *zedAdapter) read(database, top string, members []zedThread) (*WorkspaceRecord, map[string]error, error) {
	db, tx, err := a.open(database)
	if err != nil {
		return nil, nil, err
	}
	defer db.Close()
	defer tx.Rollback()
	// Live, the version is read once the transaction has begun reading, so
	// it is no older than what was read.
	version := a.databaseVersion(database)
	origin := a.original(database)
	var record *WorkspaceRecord
	depth := map[string]int{}
	damaged := map[string]error{}
	for _, member := range members {
		if member.parent != "" {
			depth[member.id] = depth[member.parent] + 1
		}
		found, err := queryMapsContext(a.context(), tx, `SELECT * FROM threads WHERE id=?`, member.id)
		if err != nil {
			return nil, nil, err
		}
		if len(found) == 0 {
			continue
		}
		row := found[0]
		data, _ := row["data"].([]byte)
		if text, ok := row["data"].(string); ok {
			data = []byte(text)
		}
		thread, err := zedThreadData(firstString(row["data_type"]), data)
		if err != nil {
			damaged[member.id] = err
			continue
		}
		conversation := a.conversation(origin, member.id, row, thread)
		if member.parent != "" {
			conversation.ParentNativeID, conversation.AgentDepth = member.parent, max(depth[member.id], 1)
			conversation.AgentNickname = firstString(thread["title"], row["summary"])
			// A subagent's prompts are its parent's.
			for index := range conversation.Messages {
				if message := &conversation.Messages[index]; message.Role == "user" {
					message.Role, message.Sender = agentRole, "agent:"+member.parent
				}
			}
		}
		if record == nil {
			purpose, outcome := agentPurposeOutcome(conversation.Messages)
			title := firstString(thread["title"], row["summary"])
			if !meaningfulWorkspaceTitle(title, member.id) {
				title = defaultString(purpose, "Zed "+short(member.id))
			}
			// The thread that started the work names it; once Zed deletes
			// it, its subagents leave the name and purpose to the copy
			// indexed before.
			if member.id != top {
				title, purpose, outcome = "Zed "+short(top), "", ""
			}
			location := zedLocation(row, thread)
			var repository map[string]any
			if location != "" {
				repository = a.repository(location, "")
			}
			record = &WorkspaceRecord{SourceID: top, SourceKind: "zed", Title: title, Account: a.config.Account, Purpose: purpose, Outcome: outcome,
				ActivityAt: conversation.EndedAt, Location: location, Repository: repository, Metadata: map[string]any{}}
			if profile := firstString(thread["profile"]); profile != "" {
				record.Metadata["profile"] = profile
			}
		}
		record.Conversations = append(record.Conversations, conversation)
	}
	if record == nil {
		return nil, damaged, nil
	}
	observe(record, version)
	return record, damaged, nil
}

// zedLocation is the folder a thread was run in: the first of the folders the
// row names, else the first worktree of the project when it began.
func zedLocation(row, thread map[string]any) string {
	for _, folder := range strings.Split(firstString(row["folder_paths"]), "\n") {
		if folder = strings.TrimSpace(folder); folder != "" {
			return folder
		}
	}
	for _, worktree := range sliceOf(mapValueDefault(thread["initial_project_snapshot"])["worktree_snapshots"]) {
		if path := firstString(mapValueDefault(worktree)["worktree_path"]); path != "" {
			return path
		}
	}
	return ""
}

func sliceOf(value any) []any {
	items, _ := sliceValue(value)
	return items
}

// conversation reads a thread's messages: the agent's own format (a message
// is {"User": …}, {"Agent": …}, {"Compaction": …} or "Resume"), or that of
// releases before it, whose messages have a role.
func (a *zedAdapter) conversation(origin, id string, row, thread map[string]any) ConversationRecord {
	started := iso(firstNonNil(row["created_at"], row["updated_at"]))
	model := mapValueDefault(thread["model"])
	conversation := ConversationRecord{NativeID: id, Provider: "zed", Account: a.config.Account, Origin: origin, Coverage: "complete", Harness: "zed",
		Model: firstString(model["model"]), Effort: firstString(thread["thinking_effort"]), StartedAt: started, EndedAt: iso(firstNonNil(row["updated_at"], thread["updated_at"]))}
	if provider := firstString(model["provider"]); provider != "" {
		conversation.NativeContext = map[string]any{"model_provider": provider}
	}
	// Zed keeps no time per message: each takes the thread's start.
	base := MessageRecord{Model: conversation.Model, CreatedAt: started, Selected: true}
	add := func(item MessageRecord) { conversation.Messages = append(conversation.Messages, item) }
	locator := func(native string) string { return fmt.Sprintf("%s#%s:%s", origin, id, native) }
	seen := map[string]int{}
	usage := thread["request_token_usage"]
	sums := map[string]float64{}
	addUsage := func(native string, tokens any) {
		counts := zedTokenUsage(tokens)
		positive := false
		for key, value := range counts {
			sums[key] += value
			positive = positive || value > 0
		}
		if !positive {
			return
		}
		item := base
		item.NativeID, item.Role, item.Kind, item.EvidenceLocator = native+":usage", "system", "metadata", locator(native)
		item.Text = jsonText(map[string]any{"type": "token_usage", "usage": counts, "usage_message_id": native})
		add(item)
	}
	messages := sliceOf(thread["messages"])
	legacy := false
	for _, message := range messages {
		if _, ok := mapValueDefault(message)["role"]; ok {
			legacy = true
			break
		}
	}
	if legacy {
		a.legacyMessages(messages, firstString(thread["version"]), usage, base, locator, add, addUsage, seen)
	} else {
		// A turn's usage follows its last message, under the prompt that
		// started it.
		turn, replies := "", 0
		closeTurn := func() {
			if turn != "" {
				addUsage(turn, mapValueDefault(usage)[turn])
			}
		}
		for index, raw := range messages {
			message := mapValueDefault(raw)
			if user := mapValue(message["User"]); user != nil {
				closeTurn()
				turn, replies = defaultString(user["id"], "user:"+strconv.Itoa(index)), 0
				item := base
				item.NativeID, item.EvidenceLocator = turn, locator(turn)
				item.RawText = jsonText(map[string]any{"type": "zed_user_message", "id": turn})
				item.Role, item.Kind, item.Text = "user", "message", zedUserText(user["content"])
				if strings.TrimSpace(item.Text) != "" {
					add(item)
				}
				continue
			}
			// A compaction is a boundary, with the summary that stands for
			// what came before it (unless the provider kept its own).
			if compaction, ok := message["Compaction"]; ok {
				native := defaultString(turn, "start") + "/compaction:" + strconv.Itoa(index)
				item := base
				item.NativeID, item.Role, item.Kind, item.EvidenceLocator = native, "system", "metadata", locator(native)
				value := map[string]any{"type": "compaction"}
				if summary := firstString(mapValueDefault(compaction)["Summary"]); summary != "" {
					value["summary"] = summary
				}
				item.Text = jsonText(value)
				add(item)
				continue
			}
			agent := mapValue(message["Agent"])
			if agent == nil {
				continue // "Resume": the person continued past a tool limit
			}
			replies++
			native := defaultString(turn, "start") + "/reply:" + strconv.Itoa(replies)
			item := base
			item.EvidenceLocator = locator(native)
			item.RawText = jsonText(map[string]any{"type": "zed_agent_message", "id": native})
			results := mapValueDefault(agent["tool_results"])
			for part, raw := range sliceOf(agent["content"]) {
				content := mapValueDefault(raw)
				record := item
				record.NativeID = native + ":" + strconv.Itoa(part)
				if text := firstString(content["Text"]); strings.TrimSpace(text) != "" {
					record.Role, record.Kind, record.Text = "assistant", "message", text
					add(record)
				} else if thinking := mapValue(content["Thinking"]); thinking != nil {
					if text := firstString(thinking["text"]); strings.TrimSpace(text) != "" {
						add(reasoningRecord(record, text, "text", ""))
					} else if firstString(thinking["signature"]) != "" {
						add(reasoningRecord(record, "", "", "encrypted"))
					}
				} else if _, ok := content["RedactedThinking"]; ok {
					add(reasoningRecord(record, "", "", "redacted"))
				} else if use := mapValue(content["ToolUse"]); use != nil {
					call := firstString(use["id"])
					a.tool(record, call, firstString(use["name"]), zedToolInput(firstNonNil(use["input"], use["raw_input"])), mapValue(results[call]), add, seen)
				}
			}
		}
		closeTurn()
	}
	// What the thread totals beyond its turns: Zed keeps each turn's last
	// request, and the thread's sum of every request.
	residual, positive := map[string]any{}, false
	for key, total := range zedTokenUsage(thread["cumulative_token_usage"]) {
		left := max(total-sums[key], 0)
		residual[key] = left
		positive = positive || left > 0
	}
	if positive {
		item := base
		item.NativeID, item.Role, item.Kind, item.CreatedAt, item.EvidenceLocator = "usage:thread", "system", "metadata", conversation.EndedAt, locator("usage")
		item.Text = jsonText(map[string]any{"type": "token_usage", "usage": residual, "source": "session_residual"})
		add(item)
	}
	return conversation
}

// legacyMessages reads the messages of releases before Zed's agent: each with
// an ID, a role, its segments, and its tool calls with their results; a
// request's usage is kept by the message's position.
func (a *zedAdapter) legacyMessages(messages []any, version string, usage any, base MessageRecord, locator func(string) string, add func(MessageRecord), addUsage func(string, any), seen map[string]int) {
	requests := sliceOf(usage)
	// A call's ID names its result only in its own message: a provider can
	// give a later call the same ID.
	resultsOf := func(message map[string]any) map[string]map[string]any {
		results := map[string]map[string]any{}
		for _, raw := range sliceOf(message["tool_results"]) {
			result := mapValueDefault(raw)
			results[firstString(result["tool_use_id"])] = result
		}
		return results
	}
	for index, raw := range messages {
		message := mapValueDefault(raw)
		native := "message:" + defaultString(message["id"], strconv.Itoa(index))
		item := base
		item.EvidenceLocator = locator(native)
		item.RawText = jsonText(map[string]any{"type": "zed_message", "id": native})
		role := firstString(message["role"])
		results := resultsOf(message)
		// Release 0.1.0 keeps a call's results on the user message after
		// it, which Zed moves back onto the call's message when it reads
		// the thread.
		if version == "0.1.0" && index+1 < len(messages) {
			maps.Copy(results, resultsOf(mapValueDefault(messages[index+1])))
		}
		segments := sliceOf(message["segments"])
		// The oldest threads keep a message's text whole.
		if text, ok := message["text"].(string); ok && len(segments) == 0 {
			segments = []any{map[string]any{"type": "text", "text": text}}
		}
		for part, raw := range segments {
			segment := mapValueDefault(raw)
			record := item
			record.NativeID = native + ":" + strconv.Itoa(part)
			switch firstString(segment["type"]) {
			case "text":
				if text := firstString(segment["text"]); strings.TrimSpace(text) != "" && (role == "user" || role == "assistant") {
					record.Role, record.Kind, record.Text = role, "message", text
					add(record)
				}
			case "thinking":
				if text := firstString(segment["text"]); strings.TrimSpace(text) != "" {
					add(reasoningRecord(record, text, "text", ""))
				}
			case "RedactedThinking", "redacted_thinking":
				add(reasoningRecord(record, "", "", "redacted"))
			}
		}
		for part, raw := range sliceOf(message["tool_uses"]) {
			use := mapValueDefault(raw)
			record := item
			record.NativeID = native + ":tool:" + strconv.Itoa(part)
			call := firstString(use["id"])
			a.tool(record, call, firstString(use["name"]), use["input"], results[call], add, seen)
		}
		if index < len(requests) && role == "assistant" {
			addUsage(native, requests[index])
		}
	}
}

// zedExit is a failed terminal command's header, as Zed reports it to the
// model, after the command it names.
var zedExit = regexp.MustCompile(`^failed with exit code (-?\d+)\.`)

// zedHeader is a failed or timed-out command's header when its command is
// not known.
var zedHeader = regexp.MustCompile(`(?s)Command ".*" (failed with exit code -?\d+\.|timed out\.)`)

// zedTerminalOutcome is how a terminal command ended, from what Zed reports
// to the model (which it saves as a result that did not fail): notes, then
// a header saying how it ended, then the output in a fenced block. Only what
// comes before that block is Zed's own; the output may say anything. Nothing
// when the result does not say.
func zedTerminalOutcome(content string, input any) map[string]any {
	header := content
	if strings.HasPrefix(content, "```") {
		header = ""
	} else if fence := strings.Index(content, "\n```"); fence >= 0 {
		header = content[:fence]
	}
	starts := func(prefix string) bool {
		return strings.HasPrefix(header, prefix) || strings.Contains(header, "\n"+prefix)
	}
	// What follows the command a header names. Zed names it as it was
	// given, so a fence in the command is not where the output begins.
	ending := ""
	// Zed names the command untrimmed.
	if command, _ := mapValueDefault(input)["command"].(string); command != "" {
		lead := `Command "` + command + `" `
		if at := strings.Index(content, lead); at >= 0 && at < len(header) {
			ending = content[at+len(lead):]
		}
	} else if match := zedHeader.FindStringSubmatch(header); match != nil {
		ending = match[1]
	}
	switch {
	case starts("The user stopped this command."):
		return map[string]any{"interrupted": true}
	case strings.HasPrefix(ending, "timed out."):
		// Zed times a command out only at the limit its call set.
		if limit, ok := number(mapValueDefault(input)["timeout_ms"]); ok {
			return map[string]any{"timedOutAfterMs": limit}
		}
		return map[string]any{"status": "failed"}
	case starts("Command terminated unexpectedly."), starts("Command failed or was interrupted."):
		return map[string]any{"status": "failed"}
	}
	if match := zedExit.FindStringSubmatch(ending); match != nil {
		exit, _ := strconv.Atoi(match[1])
		return map[string]any{"exit_code": exit}
	}
	// Otherwise the output is all there is, or there was none.
	if header != content || starts("Command executed successfully.") {
		return map[string]any{"exit_code": 0}
	}
	return nil
}

// tool records a tool call and its result. A call's ID is its message's
// alone, so a later call reusing one is told apart by its occurrence (seen
// counts each ID in the thread), as one call in the tool ledger.
func (a *zedAdapter) tool(base MessageRecord, call, name string, input any, result map[string]any, add func(MessageRecord), seen map[string]int) {
	if call != "" {
		if seen[call]++; seen[call] > 1 {
			call += "#" + strconv.Itoa(seen[call])
		}
	}
	delegated := matchesFold(name, "spawn_agent")
	record := base
	record.CallID, record.Role, record.Kind = call, "assistant", "tool_call"
	if delegated {
		record.Kind = "delegation"
	}
	record.Text = jsonText(map[string]any{"tool": name, "input": input})
	add(record)
	if result == nil {
		return
	}
	outcome := base
	outcome.NativeID, outcome.CallID, outcome.Role, outcome.Kind = base.NativeID+":result", call, "tool", "tool_result"
	if delegated {
		outcome.Kind = "delegation_result"
	}
	content := zedToolContent(result["content"])
	failed := result["is_error"] == true
	payload := map[string]any{"tool": defaultString(result["tool_name"], name), "content": content, "is_error": failed}
	if matchesFold(name, "terminal") && !failed {
		if details := zedTerminalOutcome(content, input); details != nil {
			payload["details"] = details
		}
	}
	outcome.Text = jsonText(payload)
	add(outcome)
}

// zedUserText is a prompt's text: its text parts, and the names of what it
// mentions; what a mention brought in is context, not the prompt.
func zedUserText(content any) string {
	texts := []string{}
	for _, raw := range sliceOf(content) {
		part := mapValueDefault(raw)
		if text := firstString(part["Text"]); strings.TrimSpace(text) != "" {
			texts = append(texts, text)
		}
	}
	return strings.Join(texts, "\n\n")
}

// zedToolContent is a tool result's text: Zed writes it as text, as
// {"Text": …} or {"type": "text", "text": …}, or as a list of those.
func zedToolContent(content any) string {
	if text, ok := content.(string); ok {
		return text
	}
	if list, ok := content.([]any); ok {
		texts := []string{}
		for _, item := range list {
			if text := zedToolContent(item); text != "" {
				texts = append(texts, text)
			}
		}
		return strings.Join(texts, "\n")
	}
	value := mapValueDefault(content)
	return firstString(value["Text"], value["text"])
}

// zedToolInput is a call's input, which a release tags with its type:
// {"type": "json", "value": …}, or "text". Other input is as it was given,
// which may itself have a type and a value.
func zedToolInput(input any) any {
	if value := mapValue(input); len(value) == 2 && (value["type"] == "json" || value["type"] == "text") {
		if inner, ok := value["value"]; ok {
			return inner
		}
	}
	return input
}

// zedTokenUsage is a request's usage, in the shape token accounting reads,
// which Zed's already is.
func zedTokenUsage(tokens any) map[string]float64 {
	value := mapValueDefault(tokens)
	counts := map[string]float64{}
	for _, key := range []string{"input_tokens", "output_tokens", "cache_creation_input_tokens", "cache_read_input_tokens"} {
		count, _ := number(value[key])
		counts[key] = count
	}
	return counts
}

// zedProbeStats measures the threads database for the probe: its threads and
// when they were first and last active.
func zedProbeStats(database string) (probeStats, error) {
	stats := probeStats{}
	info, err := os.Stat(database)
	if err != nil {
		return stats, err
	}
	db, err := openSourceSQLite(context.Background(), database)
	if err != nil {
		return stats, err
	}
	defer db.Close()
	tables, err := tableNames(db)
	if err != nil || !tables["threads"] {
		return stats, err
	}
	var oldest, newest sql.NullString
	if err := db.QueryRow(`SELECT COUNT(*), MIN(updated_at), MAX(updated_at) FROM threads`).Scan(&stats.sessions, &oldest, &newest); err != nil {
		return stats, err
	}
	stats.files, stats.bytes = 1, info.Size()
	if at := zedTime(oldest.String); at > 0 {
		stats.oldest = time.Unix(0, at)
	}
	if at := zedTime(newest.String); at > 0 {
		stats.newest = time.Unix(0, at)
	}
	return stats, nil
}
