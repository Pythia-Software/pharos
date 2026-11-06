package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// opencodeAdapter reads OpenCode sessions: ~/.local/share/opencode (or
// $XDG_DATA_HOME/opencode), or one of its databases. OpenCode keeps every
// session in opencode.db, or opencode-<channel>.db for other release
// channels, in its v2 schema: session_v2 rows, their session_message rows
// in seq order, and session_part rows for messages whose content is not
// inline. A subagent is a session whose parent_id names the session that
// started it. The same database holds OpenCode's credentials and accounts,
// which are neither read nor captured (opencodeCaptureTables).
type opencodeAdapter struct {
	baseAdapter
	// selected, when set, limits reading to these sessions, in groups whose
	// root is among them (snapshotSessions).
	selected map[string]bool
}

// opencodeExtractor versions the parser; bump it when parsing changes.
const opencodeExtractor = "opencode-v1"

func (a *opencodeAdapter) partExtractor() string { return opencodeExtractor }

// opencodeCaptureTables are the tables a capture keeps; every other table,
// including any a later release adds, is left out, since OpenCode keeps
// credentials, accounts and settings in the same database. The 1.x schema's
// session, message and part are kept too: a capture of such a database is
// reported as unread, as it is live, and holds its sessions for a reader.
// blob and blob_ref hold the media a part names as media:<hash>.
var opencodeCaptureTables = map[string]bool{"session_v2": true, "session_message": true, "session_part": true, "blob": true, "blob_ref": true,
	"project": true, "session": true, "message": true, "part": true}

// databases lists OpenCode's databases: the source itself when it is one,
// otherwise each opencode*.db in it.
func (a *opencodeAdapter) databases() ([]string, error) {
	info, err := a.files().Stat(a.config.Path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return []string{a.config.Path}, nil
	}
	entries, err := a.files().ReadDir(a.config.Path)
	if err != nil {
		return nil, err
	}
	databases := []string{}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, "opencode") && strings.HasSuffix(name, ".db") && entry.Type().IsRegular() {
			databases = append(databases, filepath.Join(a.config.Path, name))
		}
	}
	sort.Strings(databases)
	return databases, nil
}

func (a *opencodeAdapter) Fingerprint() (string, error) {
	databases, err := a.databases()
	if err != nil {
		return "", err
	}
	rows := []string{}
	for _, database := range databases {
		relative, _ := filepath.Rel(a.config.Path, database)
		rows = append(rows, fmt.Sprintf("%s:%d", filepath.ToSlash(relative), a.databaseVersion(database)))
	}
	return opencodeExtractor + ":" + hashBytes([]byte(strings.Join(rows, "\n"))), nil
}

func (a *opencodeAdapter) Discover(emit func(WorkspaceRecord) error) error {
	return a.discoverParts(nil, func(record *WorkspaceRecord, _ []sourcePart) error {
		if record == nil {
			return nil
		}
		return emit(*record)
	})
}

func (a *opencodeAdapter) discoverParts(unchanged func([]sourcePart) bool, emit func(*WorkspaceRecord, []sourcePart) error) error {
	defer a.shareSQLiteCopies()()
	plan, err := a.planParts()
	if err != nil {
		return err
	}
	return plan.discover(a.context(), unchanged, emit)
}

// opencodeSession is a session_v2 row's identity and how far it has changed.
type opencodeSession struct {
	id, parent string
	// fork is the session a fork copied its start from.
	fork string
	// watermark is the latest time_updated of the session and its rows, and
	// rows their count, which falls when an undo deletes messages.
	watermark, rows int64
	// usage is the session's cost and token totals, which usage no message
	// records, such as title generation's, raises without time_updated.
	usage string
}

// open opens a database for reading in one transaction.
func (a *opencodeAdapter) open(database string) (*sql.DB, *sql.Tx, error) {
	db, err := a.files().SQLite(database)
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

// sessions lists a database's sessions, and as unread the sessions it holds
// only in the 1.x schema.
func (a *opencodeAdapter) sessions(database string) (sessions []opencodeSession, unread error, err error) {
	db, tx, err := a.open(database)
	if err != nil {
		return nil, nil, err
	}
	defer db.Close()
	defer tx.Rollback()
	tables, err := tableNames(tx)
	if err != nil {
		return nil, nil, err
	}
	if !tables["session_v2"] || !tables["session_message"] {
		if tables["session"] && tables["message"] {
			return nil, fmt.Errorf("OpenCode database %s uses the schema of releases before 2.0, which Pharos does not read yet", a.original(database)), nil
		}
		return nil, nil, nil
	}
	if tables["session"] && tables["message"] {
		if unread, err = a.legacySessions(tx, database); err != nil {
			return nil, nil, err
		}
	}
	parts := "0, 0"
	if tables["session_part"] {
		parts = "(SELECT COALESCE(MAX(time_updated),0) FROM session_part p WHERE p.session_id=s.id), (SELECT COUNT(*) FROM session_part p WHERE p.session_id=s.id)"
	}
	fork := "''"
	if columns, err := tableColumns(tx, "session_v2"); err != nil {
		return nil, nil, err
	} else if columns["fork_session_id"] {
		fork = "COALESCE(s.fork_session_id,'')"
	}
	rows, err := tx.QueryContext(a.context(), `SELECT s.id, COALESCE(s.parent_id,''), `+fork+`, s.time_updated,
		(SELECT COALESCE(MAX(time_updated),0) FROM session_message m WHERE m.session_id=s.id),
		(SELECT COUNT(*) FROM session_message m WHERE m.session_id=s.id), `+parts+`,
		COALESCE(s.cost,0)||':'||COALESCE(s.tokens_input,0)||':'||COALESCE(s.tokens_output,0)||':'||COALESCE(s.tokens_reasoning,0)||':'||COALESCE(s.tokens_cache_read,0)||':'||COALESCE(s.tokens_cache_write,0)
		FROM session_v2 s ORDER BY s.id`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	sessions = []opencodeSession{}
	for rows.Next() {
		var session opencodeSession
		var updated, messagesUpdated, messages, partsUpdated, partCount int64
		if err := rows.Scan(&session.id, &session.parent, &session.fork, &updated, &messagesUpdated, &messages, &partsUpdated, &partCount, &session.usage); err != nil {
			return nil, nil, err
		}
		session.watermark, session.rows = max(updated, messagesUpdated, partsUpdated), messages+partCount
		sessions = append(sessions, session)
	}
	return sessions, unread, rows.Err()
}

// legacySessions reports the sessions a 1.x release wrote to a database
// after OpenCode moved its sessions to the 2.x schema, which OpenCode moves
// once, under the same IDs, and never again. A session deleted since in 2.x
// stays in the 1.x tables, so only those newer than every session that was
// moved are counted.
func (a *opencodeAdapter) legacySessions(tx *sql.Tx, database string) (error, error) {
	columns, err := tableColumns(tx, "session")
	if err != nil {
		return nil, err
	}
	query := `SELECT COUNT(*) FROM session s WHERE NOT EXISTS (SELECT 1 FROM session_v2 v WHERE v.id=s.id)`
	if columns["time_created"] {
		query += ` AND s.time_created > COALESCE((SELECT MAX(l.time_created) FROM session l JOIN session_v2 v ON v.id=l.id),-1)`
	}
	var count int64
	if err := tx.QueryRowContext(a.context(), query).Scan(&count); err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, nil
	}
	sessions := "sessions"
	if count == 1 {
		sessions = "session"
	}
	return fmt.Errorf("OpenCode database %s holds %d %s only in the schema of releases before 2.0, written after OpenCode moved its sessions to 2.x, which Pharos does not read yet", a.original(database), count, sessions), nil
}

// planParts plans one group per session that started its own work, holding
// it and the subagents it started; each session is one part.
func (a *opencodeAdapter) planParts() (*sourceWorkPlan, error) {
	databases, err := a.databases()
	if err != nil {
		return nil, err
	}
	plan := &sourceWorkPlan{continueOnError: true}
	var failures []error
	for _, database := range databases {
		sessions, unread, err := a.sessions(database)
		if err != nil || unread != nil {
			failures = append(failures, errors.Join(unread, err))
		}
		if err != nil {
			continue
		}
		byID := map[string]opencodeSession{}
		for _, session := range sessions {
			byID[session.id] = session
		}
		root := func(id string) string {
			seen := map[string]bool{}
			for byID[id].parent != "" && !seen[id] {
				seen[id] = true
				if _, ok := byID[byID[id].parent]; !ok {
					break
				}
				id = byID[id].parent
			}
			return id
		}
		groups := map[string][]opencodeSession{}
		roots := []string{}
		for _, session := range sessions {
			top := root(session.id)
			if groups[top] == nil {
				roots = append(roots, top)
			}
			groups[top] = append(groups[top], session)
		}
		for _, top := range roots {
			members := groups[top]
			if a.selected != nil {
				if !a.selected[top] {
					continue
				}
				members = slices.DeleteFunc(slices.Clone(members), func(member opencodeSession) bool { return !a.selected[member.id] })
			}
			// The root first, then its subagents a level at a time, each
			// level by ID, so a subagent follows the one that started it.
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
				signal := fmt.Sprintf("%d:%d:%s", member.watermark, member.rows, member.usage)
				// A fork's copies are read by what its source still holds.
				if source, ok := byID[member.fork]; ok && member.fork != "" {
					signal += fmt.Sprintf(":fork:%d:%d", source.watermark, source.rows)
				}
				parts = append(parts, sourcePart{item: a.original(database) + "#" + member.id, size: member.rows, version: member.watermark * 1e6, signal: signal})
			}
			database, members := database, members
			plan.groups = append(plan.groups, plannedPartGroup{parts: parts, read: func(emit partEmitter) error {
				record, err := a.read(database, members)
				if err != nil {
					return err
				}
				return emit(record, parts)
			}})
		}
	}
	plan.discoveryError = errors.Join(failures...)
	return plan, nil
}

// snapshotSessions lists the sessions in a database snapshot. A superseded
// snapshot's that no newer one holds were deleted in OpenCode, and are read
// from it alone: a group whose root was deleted, without its subagents a
// newer snapshot still holds, which are newer there.
func (a *opencodeAdapter) snapshotSessions(ctx context.Context, file string) (map[string]bool, error) {
	db, err := openReadOnlySQLite(file)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	tables, err := tableNames(db)
	if err != nil || !tables["session_v2"] {
		return map[string]bool{}, err
	}
	rows, err := queryMapsContext(ctx, db, `SELECT id FROM session_v2`)
	ids := map[string]bool{}
	for _, row := range rows {
		ids[firstString(row["id"])] = true
	}
	return ids, err
}

func (a *opencodeAdapter) olderReader(file string, view *captureView, selected map[string]bool) Adapter {
	config := a.config
	config.Path = file
	return &opencodeAdapter{baseAdapter: baseAdapter{config: config, capability: a.capability, view: view}, selected: selected}
}

// readGroup reads, as indexing does, the group whose parts begin as parts
// do; nothing when that session is gone.
func (a *opencodeAdapter) readGroup(parts []sourcePart) (*WorkspaceRecord, []sourcePart, error) {
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

// read reads a session and its subagents in one transaction.
func (a *opencodeAdapter) read(database string, members []opencodeSession) (*WorkspaceRecord, error) {
	db, tx, err := a.open(database)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	defer tx.Rollback()
	tables, err := tableNames(tx)
	if err != nil {
		return nil, err
	}
	// Live, the version is read once the transaction has begun reading, so
	// it is no older than what was read.
	version := a.databaseVersion(database)
	origin := a.original(database)
	var record *WorkspaceRecord
	depth := map[string]int{}
	for _, member := range members {
		conversation, session, err := a.conversation(tx, tables, origin, member.id)
		if err != nil {
			return nil, err
		}
		if conversation == nil {
			continue
		}
		if member.parent != "" {
			depth[member.id] = depth[member.parent] + 1
			conversation.ParentNativeID, conversation.AgentDepth = member.parent, max(depth[member.id], 1)
			conversation.AgentNickname, conversation.AgentPath = firstString(session["title"]), firstString(session["agent"])
			// A subagent's prompts are its parent's; a command run from its
			// prompt is still the person's.
			for index := range conversation.Messages {
				if message := &conversation.Messages[index]; message.Role == "user" && message.Kind != "tool_call" {
					message.Role, message.Sender = agentRole, "agent:"+member.parent
				}
			}
		}
		if record == nil {
			purpose, outcome := agentPurposeOutcome(conversation.Messages)
			title := firstString(session["title"])
			if !meaningfulWorkspaceTitle(title, member.id) {
				title = defaultString(purpose, "OpenCode "+short(member.id))
			}
			directory := firstString(session["directory"])
			record = &WorkspaceRecord{SourceID: member.id, SourceKind: "opencode", Title: title, Account: a.config.Account, Purpose: purpose, Outcome: outcome, ActivityAt: iso(session["time_updated"]), Location: directory, Repository: a.repository(directory, ""), Metadata: map[string]any{}}
			if cost, ok := number(session["cost"]); ok && cost > 0 {
				record.Metadata["reported_cost_usd"] = cost
			}
			if fork := firstString(session["fork_session_id"]); fork != "" {
				record.Metadata["forked_from"] = fork
			}
		}
		record.Conversations = append(record.Conversations, *conversation)
	}
	if record == nil {
		return nil, nil
	}
	observe(record, version)
	return record, nil
}

// conversation reads one session: its row, then its messages in seq order.
func (a *opencodeAdapter) conversation(tx *sql.Tx, tables map[string]bool, origin, id string) (*ConversationRecord, map[string]any, error) {
	found, err := queryMapsContext(a.context(), tx, `SELECT * FROM session_v2 WHERE id=?`, id)
	if err != nil || len(found) == 0 {
		return nil, nil, err
	}
	session := found[0]
	model := mapValueDefault(opencodeJSON(session["model"]))
	conversation := &ConversationRecord{NativeID: id, Provider: "opencode", Account: a.config.Account, Origin: origin, Coverage: "complete", Harness: "opencode",
		HarnessVersionFirst: firstString(session["version"]), HarnessVersionLast: firstString(session["version"]), Model: firstString(model["id"]),
		StartedAt: iso(session["time_created"]), EndedAt: iso(session["time_updated"])}
	if provider := firstString(model["providerID"]); provider != "" {
		conversation.NativeContext = map[string]any{"model_provider": provider}
	}
	rows, err := tx.QueryContext(a.context(), `SELECT id, type, seq, time_created, data FROM session_message WHERE session_id=? ORDER BY seq`, id)
	if err != nil {
		return nil, nil, err
	}
	type row struct {
		id, kind     string
		seq, created int64
		data         map[string]any
	}
	messages := []row{}
	for rows.Next() {
		var r row
		var data string
		if err := rows.Scan(&r.id, &r.kind, &r.seq, &r.created, &data); err != nil {
			rows.Close()
			return nil, nil, err
		}
		if err := json.Unmarshal([]byte(data), &r.data); err != nil {
			rows.Close()
			return nil, nil, fmt.Errorf("OpenCode message %s: %w", r.id, err)
		}
		messages = append(messages, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	locator := func(message string) string { return fmt.Sprintf("%s#%s:%s", origin, id, message) }
	// A fork starts with a copy of its source's messages up to the cut, each
	// with the source's data, usage and tool runs included, under the ID
	// <fork event>_<seq> and the source's seq; the fork's totals start at
	// zero. The source counts a copy while it holds the message at that seq,
	// so the copy is left to it; seqs are never reused, so one an undo or
	// deleting the source removed is the fork's alone. That stays as the
	// fork's history, without its usage, which the source may have been
	// indexed with.
	inherited, held := "", map[int64]bool{}
	if source := firstString(session["fork_session_id"]); source != "" {
		for _, message := range messages {
			if suffix := "_" + strconv.FormatInt(message.seq, 10); strings.HasSuffix(message.id, suffix) && len(message.id) > len(suffix) {
				inherited = strings.TrimSuffix(message.id, suffix)
				break
			}
		}
	}
	// A deleted fork read from an older snapshot whose source a newer one
	// holds can't tell what the source holds now, so it keeps its copies.
	if source := firstString(session["fork_session_id"]); inherited != "" && (a.selected == nil || a.selected[source]) {
		rows, err := tx.QueryContext(a.context(), `SELECT seq FROM session_message WHERE session_id=?`, source)
		if err != nil {
			return nil, nil, err
		}
		for rows.Next() {
			var seq int64
			if err := rows.Scan(&seq); err != nil {
				rows.Close()
				return nil, nil, err
			}
			held[seq] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, nil, err
		}
	}
	copied := func(message row) bool {
		return inherited != "" && message.id == inherited+"_"+strconv.FormatInt(message.seq, 10)
	}
	messages = slices.DeleteFunc(messages, func(message row) bool { return copied(message) && held[message.seq] })
	// What the session totals beyond its messages and compactions: requests
	// whose messages an undo deleted, title generation, and a summary made
	// ahead that no compaction took, which no message records.
	sums := map[string]float64{}
	for _, message := range messages {
		data := message.data
		created := iso(firstNonNil(mapValueDefault(data["time"])["created"], message.created))
		// The message's type and ID stand in for its raw text, so text quoting
		// usage JSON is never read as token usage.
		base := MessageRecord{NativeID: message.id, Model: conversation.Model, CreatedAt: created, EvidenceLocator: locator(message.id), Selected: true,
			RawText: jsonText(map[string]any{"type": message.kind, "id": message.id})}
		add := func(item MessageRecord) { conversation.Messages = append(conversation.Messages, item) }
		text := func(fields ...string) string {
			for _, field := range fields {
				if value := firstString(data[field]); strings.TrimSpace(value) != "" {
					return value
				}
			}
			return ""
		}
		switch message.kind {
		case "user":
			item := base
			item.Role, item.Kind, item.Text = "user", "message", firstString(data["text"])
			if strings.TrimSpace(item.Text) != "" {
				add(item)
			}
			if files, _ := data["files"].([]any); len(files) > 0 {
				attachment := base
				attachment.NativeID, attachment.Role, attachment.Kind, attachment.Text = message.id+":files", "user", "attachment", jsonText(files)
				add(attachment)
			}
		case "synthetic", "system", "skill":
			// Text OpenCode added to the prompt: a synthetic message, a system
			// note, or a skill's instructions.
			if body := text("text", "description"); body != "" {
				source := "opencode_" + message.kind
				if name := firstString(data["skill"], data["name"]); message.kind == "skill" && name != "" {
					source += ":" + name
				}
				add(injectedContext(base, source, body))
			}
		case "shell":
			// A command the person ran from the prompt, which no request asked
			// for, and what it printed.
			callID := message.id + ":shell"
			call := base
			call.CallID, call.Role, call.Kind = callID, "user", "tool_call"
			call.Text = jsonText(map[string]any{"tool": "shell", "input": map[string]any{"command": data["command"]}})
			add(call)
			if status := firstString(data["status"]); status != "running" && status != "pending" {
				result := base
				result.NativeID, result.CallID, result.Role, result.Kind = "result:"+callID, callID, "tool", "tool_result"
				exit, exited := number(data["exit"])
				payload := map[string]any{"tool": "shell", "content": data["output"], "is_error": status == "error" || (exited && exit != 0)}
				if exited {
					payload["details"] = map[string]any{"exit_code": exit}
				}
				result.Text = jsonText(payload)
				add(result)
			}
		case "compaction":
			// A compaction that ended is a request of its own, with its
			// model and usage, which the session's totals hold too; a copy
			// in a fork is its source's.
			if tokens := mapValue(data["tokens"]); tokens != nil && !copied(message) {
				item := base
				item.Model = defaultString(firstString(mapValueDefault(data["model"])["id"]), conversation.Model)
				add(opencodeUsage(item, message.id, tokens, sums))
			}
			switch firstString(data["status"]) {
			case "completed":
				// Only a compaction that completed replaces the context later
				// requests read: the boundary where token accounting resets
				// carried context. One that failed left the context as it was.
				item := base
				item.Role, item.Kind = "system", "metadata"
				item.Text = jsonText(map[string]any{"type": "compaction", "summary": firstNonNil(data["summary"], mapValueDefault(data["native"])["messages"])})
				add(item)
			case "failed":
				item := base
				item.NativeID, item.Role, item.Kind = message.id+":error", "system", "error"
				failure := mapValueDefault(data["error"])
				item.Text = "Compaction failed: " + firstString(failure["message"], failure["type"], "unknown error")
				add(item)
			}
		case "assistant":
			model := mapValueDefault(data["model"])
			base.Model = defaultString(firstString(model["id"]), conversation.Model)
			// A message whose parts are in session_part holds them there,
			// whatever its own content says, an empty list included.
			content, _ := data["content"].([]any)
			if tables["session_part"] {
				parts, err := opencodeParts(tx, a, message.id)
				if err != nil {
					return nil, nil, err
				}
				if len(parts) > 0 {
					content = parts
				}
			}
			for index, raw := range content {
				part := mapValueDefault(raw)
				item := base
				item.NativeID = fmt.Sprintf("%s:block:%d", message.id, index)
				if at := iso(mapValueDefault(part["time"])["created"]); at != "" {
					item.CreatedAt = at
				}
				switch firstString(part["type"]) {
				case "text":
					if text := firstString(part["text"]); strings.TrimSpace(text) != "" {
						item.Role, item.Kind, item.Text = "assistant", "message", text
						add(item)
					}
				case "reasoning":
					if text := firstString(part["text"]); strings.TrimSpace(text) != "" {
						add(reasoningRecord(item, text, "text", ""))
					}
				case "tool":
					a.tool(item, part, add)
				}
			}
			if failure := mapValue(data["error"]); failure != nil {
				item := base
				item.NativeID, item.Role, item.Kind = message.id+":error", "system", "error"
				item.Text = firstString(mapValueDefault(failure["data"])["message"], failure["message"], failure["name"], jsonText(failure))
				add(item)
			}
			if tokens := mapValue(data["tokens"]); tokens != nil && !copied(message) {
				add(opencodeUsage(base, message.id, tokens, sums))
			}
		}
	}
	totals := opencodeTokenUsage(map[string]any{"input": session["tokens_input"], "output": session["tokens_output"], "reasoning": session["tokens_reasoning"], "cache": map[string]any{"read": session["tokens_cache_read"], "write": session["tokens_cache_write"]}})
	residual, positive := map[string]any{}, false
	for key, total := range totals {
		left := max(total-sums[key], 0)
		residual[key] = left
		positive = positive || left > 0
	}
	if positive {
		conversation.Messages = append(conversation.Messages, MessageRecord{NativeID: "usage:session", Role: "system", Kind: "metadata", Model: conversation.Model, CreatedAt: conversation.EndedAt, EvidenceLocator: locator("session"), Selected: true,
			Text: jsonText(map[string]any{"type": "token_usage", "usage": residual, "source": "session_residual"})})
	}
	if len(conversation.Messages) == 0 {
		return nil, session, nil
	}
	return conversation, session, nil
}

// opencodeUsage is a request's usage record, which sums counts toward the
// session's totals.
func opencodeUsage(base MessageRecord, id string, tokens map[string]any, sums map[string]float64) MessageRecord {
	usage := opencodeTokenUsage(tokens)
	for key, value := range usage {
		sums[key] += value
	}
	base.NativeID, base.Role, base.Kind, base.RawText = id+":usage", "system", "metadata", ""
	base.Text = jsonText(map[string]any{"type": "token_usage", "usage": usage, "usage_message_id": id})
	return base
}

// tool adds a tool part's call and, once it has run, its result.
func (a *opencodeAdapter) tool(base MessageRecord, part map[string]any, add func(MessageRecord)) {
	name := defaultString(part["name"], "tool")
	callID := defaultString(part["id"], base.NativeID)
	state := mapValueDefault(part["state"])
	delegated := matchesFold(name, "task", "subagent", "agent")
	call := base
	call.CallID, call.Role, call.Kind = callID, "assistant", "tool_call"
	if delegated {
		call.Kind = "delegation"
	}
	call.Text = jsonText(map[string]any{"tool": name, "input": state["input"]})
	add(call)
	status := firstString(state["status"])
	if status != "completed" && status != "error" {
		return
	}
	times := mapValueDefault(part["time"])
	result := base
	result.NativeID, result.CallID, result.Role, result.Kind = "result:"+callID, callID, "tool", "tool_result"
	if delegated {
		result.Kind = "delegation_result"
	}
	if at := iso(times["completed"]); at != "" {
		result.CreatedAt = at
	}
	metadata := mapValueDefault(state["metadata"])
	details := map[string]any{}
	if exit, ok := number(metadata["exit"]); ok {
		details["exit_code"] = exit
	}
	if metadata["truncated"] == true {
		details["truncated"] = true
	}
	if started, ok := number(firstNonNil(times["ran"], times["created"])); ok {
		if completed, ok := number(times["completed"]); ok && completed >= started {
			details["duration_ms"] = completed - started
		}
	}
	if failure := firstString(state["error"]); failure != "" {
		details["error"] = failure
	}
	// A delegation's result names the session it ran in, the subagent's
	// conversation.
	if child := firstString(metadata["sessionID"]); delegated && child != "" {
		details["agent_id"] = child
	}
	content := firstNonNil(state["content"], state["output"])
	// A failure, such as a permission OpenCode or the person refused, can
	// hold no content but its error.
	if failure := mapValue(state["error"]); content == nil && failure != nil {
		content = firstString(failure["message"], failure["type"])
	}
	payload := map[string]any{"tool": name, "content": content, "is_error": status == "error"}
	if len(details) > 0 {
		payload["details"] = details
	}
	result.Text = jsonText(payload)
	add(result)
}

// opencodeTokenUsage is OpenCode's usage in the shape token accounting
// reads. OpenCode's input leaves out cache reads and writes, and its output
// leaves out reasoning.
func opencodeTokenUsage(tokens map[string]any) map[string]float64 {
	value := func(v any) float64 {
		n, _ := number(v)
		return n
	}
	cache := mapValueDefault(tokens["cache"])
	reasoning := value(tokens["reasoning"])
	return map[string]float64{
		"input_tokens":                value(tokens["input"]),
		"cache_read_input_tokens":     value(cache["read"]),
		"cache_creation_input_tokens": value(cache["write"]),
		"output_tokens":               value(tokens["output"]) + reasoning,
		"reasoning_output_tokens":     reasoning,
	}
}

// opencodeParts reads a message's content from session_part, by ordinal.
func opencodeParts(tx *sql.Tx, a *opencodeAdapter, message string) ([]any, error) {
	rows, err := tx.QueryContext(a.context(), `SELECT data FROM session_part WHERE message_id=? ORDER BY ordinal`, message)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	parts := []any{}
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var part map[string]any
		if err := json.Unmarshal([]byte(data), &part); err != nil {
			return nil, fmt.Errorf("OpenCode part of %s: %w", message, err)
		}
		parts = append(parts, part)
	}
	return parts, rows.Err()
}

// opencodeJSON decodes a JSON column, or nil.
func opencodeJSON(value any) any {
	text, _ := value.(string)
	var decoded any
	if text == "" || json.Unmarshal([]byte(text), &decoded) != nil {
		return nil
	}
	return decoded
}

// opencodeProbeStats measures a database for the probe: its sessions and
// when they were first and last active.
func opencodeProbeStats(database string) (probeStats, error) {
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
	if err != nil || !tables["session_v2"] {
		return stats, err
	}
	var oldest, newest sql.NullInt64
	if err := db.QueryRow(`SELECT COUNT(*), MIN(time_created), MAX(time_updated) FROM session_v2`).Scan(&stats.sessions, &oldest, &newest); err != nil {
		return stats, err
	}
	stats.files, stats.bytes = 1, info.Size()
	if oldest.Valid {
		stats.oldest, stats.newest = time.UnixMilli(oldest.Int64), time.UnixMilli(newest.Int64)
	}
	return stats, nil
}
