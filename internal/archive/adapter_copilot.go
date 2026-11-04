package archive

import (
	"bufio"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// copilotAdapter reads GitHub Copilot CLI sessions: ~/.copilot, or its
// session-state folder. Each session is a folder holding events.jsonl, the
// event log, and workspace.yaml, its name and place; older releases wrote a
// flat <session>.jsonl. A subagent's events are in its parent's log, marked
// with its agentId. session-store.db is an index Copilot rebuilds from the
// logs, and holds the same usage rows more than once, so it is not read.
type copilotAdapter struct{ baseAdapter }

// copilotExtractor versions the parser; bump it when parsing changes.
const copilotExtractor = "copilot-v1"

func (a *copilotAdapter) partExtractor() string { return copilotExtractor }

// sessionsDir is the folder holding the sessions.
func (a *copilotAdapter) sessionsDir() string {
	if info, err := os.Stat(filepath.Join(a.config.Path, "session-state")); err == nil && info.IsDir() {
		return filepath.Join(a.config.Path, "session-state")
	}
	return a.config.Path
}

// copilotSession is one session's event log, when present its workspace.yaml,
// and the flat log an older release wrote for it before it had a folder.
type copilotSession struct{ id, events, workspace, flat string }

// sessions lists the sessions. A session with both a folder and a flat log
// is read from both: the folder's log need not repeat what the flat one holds.
func (a *copilotAdapter) sessions() ([]copilotSession, error) {
	dir := a.sessionsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	byID := map[string]copilotSession{}
	for _, entry := range entries {
		if err := a.context().Err(); err != nil {
			return nil, err
		}
		name := entry.Name()
		if entry.IsDir() {
			events := filepath.Join(dir, name, "events.jsonl")
			if info, err := os.Stat(events); err != nil || !info.Mode().IsRegular() {
				continue
			}
			session := copilotSession{id: name, events: events}
			if info, err := os.Stat(filepath.Join(dir, name, "workspace.yaml")); err == nil && info.Mode().IsRegular() {
				session.workspace = filepath.Join(dir, name, "workspace.yaml")
			}
			byID[name] = session
		}
	}
	for _, entry := range entries {
		if id, ok := strings.CutSuffix(entry.Name(), ".jsonl"); ok && entry.Type().IsRegular() {
			flat := filepath.Join(dir, entry.Name())
			if session, found := byID[id]; found {
				session.flat = flat
				byID[id] = session
			} else {
				byID[id] = copilotSession{id: id, events: flat}
			}
		}
	}
	sessions := make([]copilotSession, 0, len(byID))
	for _, session := range byID {
		sessions = append(sessions, session)
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].id < sessions[j].id })
	return sessions, nil
}

// captureFiles lists every file the adapter reads.
func (a *copilotAdapter) captureFiles() ([]string, error) {
	sessions, err := a.sessions()
	if err != nil {
		return nil, err
	}
	files := []string{}
	for _, session := range sessions {
		for _, file := range []string{session.flat, session.events, session.workspace} {
			if file != "" {
				files = append(files, file)
			}
		}
	}
	sort.Strings(files)
	return files, nil
}

func (a *copilotAdapter) Fingerprint() (string, error) {
	files, err := a.captureFiles()
	if err != nil {
		return "", err
	}
	rows := []string{}
	for _, file := range files {
		// Through the capture view: an encoded log at its original size.
		if info, err := a.files().Stat(file); err == nil {
			relative, _ := filepath.Rel(a.config.Path, file)
			rows = append(rows, fmt.Sprintf("%s:%d:%d", filepath.ToSlash(relative), info.Size(), info.ModTime().UnixNano()))
		}
	}
	sort.Strings(rows)
	return copilotExtractor + ":" + hashBytes([]byte(strings.Join(rows, "\n"))), nil
}

func (a *copilotAdapter) Discover(emit func(WorkspaceRecord) error) error {
	return a.discoverParts(nil, func(record *WorkspaceRecord, _ []sourcePart) error {
		if record == nil {
			return nil
		}
		return emit(*record)
	})
}

func (a *copilotAdapter) discoverParts(unchanged func([]sourcePart) bool, emit func(*WorkspaceRecord, []sourcePart) error) error {
	plan, err := a.planParts()
	if err != nil {
		return err
	}
	return plan.discover(a.context(), unchanged, emit)
}

func (a *copilotAdapter) planParts() (*sourceWorkPlan, error) {
	sessions, err := a.sessions()
	if err != nil {
		return nil, err
	}
	plan := &sourceWorkPlan{continueOnError: true}
	var failures []error
	for _, session := range sessions {
		parts := []sourcePart{}
		for _, file := range []string{session.flat, session.events, session.workspace} {
			if file == "" {
				continue
			}
			info, err := a.files().Stat(file)
			if err != nil {
				if !errors.Is(err, os.ErrNotExist) {
					failures = append(failures, fmt.Errorf("stat %s: %w", file, err))
				}
				parts = nil
				break
			}
			parts = append(parts, sourcePart{item: a.original(file), size: info.Size(), version: info.ModTime().UnixNano()})
		}
		if len(parts) == 0 {
			continue
		}
		plan.groups = append(plan.groups, plannedPartGroup{parts: parts, read: func(emit partEmitter) error {
			record, read, err := a.parseSession(session)
			if err != nil {
				return fmt.Errorf("parse %s: %w", session.events, err)
			}
			return emit(record, read)
		}})
	}
	plan.discoveryError = errors.Join(failures...)
	return plan, nil
}

// parseSession reads a session, reporting the version of each file as it
// was opened, so a log appended meanwhile is parsed again next time.
func (a *copilotAdapter) parseSession(session copilotSession) (*WorkspaceRecord, []sourcePart, error) {
	read := []sourcePart{}
	workspace := map[string]string{}
	if session.workspace != "" {
		data, part, err := a.readWorkspace(session.workspace)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, nil, err
		}
		if err == nil {
			workspace = data
			read = append(read, part)
		}
	}
	// The flat log first, then the folder's events it does not already hold.
	events, seen, version := []map[string]any{}, map[string]bool{}, int64(0)
	logs := []sourcePart{}
	for _, path := range []string{session.flat, session.events} {
		if path == "" {
			continue
		}
		read, part, err := a.readLog(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, nil, err
		}
		logs = append(logs, part)
		version = max(version, part.version)
		for _, event := range read {
			if id := firstString(event["id"]); id != "" {
				if seen[id] {
					continue
				}
				seen[id] = true
			}
			event["_origin"] = part.item
			events = append(events, event)
		}
	}
	if len(logs) == 0 {
		return nil, read, nil
	}
	read = append(logs, read...)
	record := a.record(session, events, workspace)
	if record == nil {
		return nil, read, nil
	}
	// The logs alone version the session. A capture can copy a log before
	// an append and workspace.yaml after it; taking the newer yaml's time
	// would let that older log replace the newer one.
	observe(record, version)
	return record, read, nil
}

// readLog reads an event log and its part.
func (a *copilotAdapter) readLog(path string) ([]map[string]any, sourcePart, error) {
	file, err := a.files().OpenContent(path)
	if err != nil {
		return nil, sourcePart{}, err
	}
	defer file.Close()
	info, _ := file.Stat()
	events, err := readJSONLines(file, file.readLimit())
	return events, sourcePart{item: a.original(path), size: info.Size(), version: info.ModTime().UnixNano()}, err
}

func (a *copilotAdapter) readWorkspace(path string) (map[string]string, sourcePart, error) {
	file, err := a.files().OpenContent(path)
	if err != nil {
		return nil, sourcePart{}, err
	}
	defer file.Close()
	info, _ := file.Stat()
	values, err := copilotWorkspaceYAML(file)
	return values, sourcePart{item: a.original(path), size: info.Size(), version: info.ModTime().UnixNano(), metadata: true}, err
}

// copilotWorkspaceYAML reads workspace.yaml's top-level scalars, the only
// shape Copilot writes there: plain, quoted, or a block (| or >) whose
// indented lines follow.
func copilotWorkspaceYAML(file *sourceFile) (map[string]string, error) {
	values := map[string]string{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	block, folded, lines := "", false, []string{}
	finish := func() {
		if block != "" {
			separator := "\n"
			if folded {
				separator = " "
			}
			values[block] = strings.Join(lines, separator)
		}
		block, lines = "", nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if block != "" && (line == "" || line[0] == ' ' || line[0] == '\t') {
			lines = append(lines, strings.TrimSpace(line))
			continue
		}
		finish()
		if line == "" || line[0] == '#' || line[0] == ' ' || line[0] == '\t' {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch {
		case value != "" && (value[0] == '|' || value[0] == '>'):
			block, folded = key, value[0] == '>'
			continue
		case strings.HasPrefix(value, `"`):
			if unquoted, err := strconv.Unquote(value); err == nil {
				value = unquoted
			}
		case strings.HasPrefix(value, "'") && strings.HasSuffix(value, "'") && len(value) >= 2:
			value = strings.ReplaceAll(value[1:len(value)-1], "''", "'")
		}
		values[key] = value
	}
	finish()
	return values, scanner.Err()
}

// copilotAgent is one conversation in a session's log: the session's own,
// or a subagent's.
type copilotAgent struct {
	conversation ConversationRecord
	model        string
	effort       string
}

func (a *copilotAdapter) record(session copilotSession, events []map[string]any, workspace map[string]string) *WorkspaceRecord {
	origin := a.original(session.events)
	sessionID, cwd, branch, version := session.id, workspace["cwd"], workspace["branch"], ""
	main := &copilotAgent{}
	agents := map[string]*copilotAgent{"": main}
	order := []string{""}
	// Each call's tool, whether it delegates, and where its record is.
	type call struct {
		tool      string
		delegated bool
		index     int
		agent     string
		started   time.Time
	}
	calls := map[string]*call{}
	// A receipt comes before the assistant message it paid for, and shares
	// its apiCallId; naming the receipt after the message links the
	// message's tool calls to it.
	paidFor := map[string]string{}
	for _, event := range events {
		if firstString(event["type"]) == "assistant.message" {
			data := mapValueDefault(event["data"])
			if api, id := firstString(data["apiCallId"]), firstString(data["messageId"]); api != "" && id != "" {
				paidFor[firstString(event["agentId"])+"\x00"+api] = id
			}
		}
	}
	// Receipts by request, under its API and its provider ID alike: a
	// request reported again updates its one record, as token accounting
	// reads it. beforeShutdown is their usage per model at the last shutdown.
	type receipt struct {
		agent   *copilotAgent
		index   int
		model   string
		request string
		usage   map[string]any
	}
	receipts, byRequest := []*receipt{}, map[string]*receipt{}
	receipted := func() map[string]map[string]float64 {
		sums := map[string]map[string]float64{}
		for _, r := range receipts {
			if sums[r.model] == nil {
				sums[r.model] = map[string]float64{}
			}
			for key, value := range r.usage {
				n, _ := number(value)
				sums[r.model][key] += n
			}
		}
		return sums
	}
	beforeShutdown := map[string]map[string]float64{}
	var lastShutdown map[string]any
	shutdownAt, shutdownLocator := "", ""
	started, ended := "", ""
	for _, event := range events {
		data := mapValueDefault(event["data"])
		kind := firstString(event["type"])
		timestamp := iso(event["timestamp"])
		if timestamp != "" {
			if started == "" {
				started = timestamp
			}
			ended = timestamp
		}
		agentID := firstString(event["agentId"])
		agent := agents[agentID]
		if agent == nil {
			// A subagent's events can precede its start event.
			agent = &copilotAgent{conversation: ConversationRecord{NativeID: agentID, AgentDepth: 1}}
			agents[agentID] = agent
			order = append(order, agentID)
		}
		line, log := integer(event["_line"]), defaultString(firstString(event["_origin"]), origin)
		eventID := firstString(event["id"])
		if eventID == "" {
			// Both of a session's logs have a line 2.
			eventID = fmt.Sprintf("line-%d", line)
			if log != origin {
				eventID = "flat:" + eventID
			}
		}
		// The event's type and ID stand in for its raw text, so text quoting
		// usage JSON is never read as token usage.
		base := MessageRecord{NativeID: eventID, Model: agent.model, Effort: agent.effort, CreatedAt: timestamp, EvidenceLocator: fmt.Sprintf("%s:%d", log, line), Selected: true,
			RawText: jsonText(map[string]any{"type": kind, "id": eventID})}
		add := func(message MessageRecord) int {
			agent.conversation.Messages = append(agent.conversation.Messages, message)
			return len(agent.conversation.Messages) - 1
		}
		switch kind {
		case "session.start":
			sessionID = defaultString(data["sessionId"], sessionID)
			context := mapValueDefault(data["context"])
			cwd = firstString(cwd, context["cwd"])
			branch = firstString(branch, context["branch"])
			version = firstString(data["copilotVersion"], version)
		case "session.resume":
			context := mapValueDefault(data["context"])
			cwd = firstString(cwd, context["cwd"])
			branch = firstString(branch, context["branch"])
			agent.effort = updatedEffort(agent.effort, data)
		case "subagent.started":
			agent.conversation.AgentNickname = firstString(data["agentDisplayName"], data["agentName"])
			agent.conversation.AgentPath = firstString(data["agentName"], data["agentType"])
			agent.model = firstString(data["model"], agent.model)
		case "subagent.configured":
			agent.model = firstString(data["model"], agent.model)
			agent.effort = updatedEffort(agent.effort, data)
		case "user.message":
			agent.effort = updatedEffort(agent.effort, mapValueDefault(data["responsesReasoning"]))
			agent.model = firstString(mapValueDefault(data["responsesReasoning"])["model"], agent.model)
			base.Model, base.Effort = agent.model, agent.effort
			item := base
			item.NativeID = defaultString(data["messageId"], eventID)
			item.Role, item.Kind, item.Text = "user", "message", firstString(data["content"])
			if agentID != "" {
				item.Role = agentRole
			}
			if source := firstString(data["source"]); strings.HasPrefix(source, "agent-") {
				item.Sender = "agent:" + strings.TrimPrefix(source, "agent-")
			}
			if strings.TrimSpace(item.Text) != "" {
				add(item)
			}
		case "system.message":
			// The system prompt Copilot sends with each request.
			add(injectedContext(base, "copilot_system_prompt", firstString(data["content"])))
		case "assistant.message":
			agent.model = firstString(data["model"], agent.model)
			base.Model = agent.model
			id := defaultString(data["messageId"], eventID)
			if text := firstString(data["reasoningText"]); strings.TrimSpace(text) != "" {
				format := "text"
				// OpenAI's Responses API returns summaries of its reasoning.
				if strings.HasPrefix(firstString(mapValueDefault(data["reasoningBlocks"])["provider"]), "openai") {
					format = "summary"
				}
				item := reasoningRecord(base, text, format, "")
				item.NativeID = id + ":reasoning"
				add(item)
			} else if firstString(data["reasoningOpaque"], data["encryptedContent"]) != "" {
				item := reasoningRecord(base, "", "", "encrypted")
				item.NativeID = id + ":reasoning"
				add(item)
			}
			if text := firstString(data["content"]); strings.TrimSpace(text) != "" {
				item := base
				item.NativeID, item.Role, item.Kind, item.Text = id, "assistant", "message", text
				add(item)
			}
			requests, _ := data["toolRequests"].([]any)
			for index, raw := range requests {
				request := mapValue(raw)
				if request == nil {
					continue
				}
				name := defaultString(request["name"], "tool")
				callID := defaultString(request["toolCallId"], fmt.Sprintf("%s:call:%d", id, index))
				item := base
				item.NativeID, item.CallID, item.Role, item.Kind = fmt.Sprintf("%s:block:%d", id, index), callID, "assistant", "tool_call"
				delegated := matchesFold(name, "task", "agent", "spawn_agent", "delegate", "workflow")
				if delegated {
					item.Kind = "delegation"
				}
				item.Text = jsonText(map[string]any{"tool": name, "input": request["arguments"]})
				calls[callID] = &call{tool: name, delegated: delegated, index: add(item), agent: agentID}
			}
		case "tool.execution_start":
			// The call's own start, which its result's time is measured from.
			if c := calls[firstString(data["toolCallId"])]; c != nil {
				if at, ok := parseTime(timestamp); ok {
					c.started = at
				}
				if c.agent == agentID && timestamp != "" {
					agent.conversation.Messages[c.index].CreatedAt = timestamp
				}
			}
		case "tool.execution_complete":
			callID := firstString(data["toolCallId"])
			c := calls[callID]
			if c == nil {
				c = &call{tool: "tool"}
			}
			item := base
			item.NativeID, item.CallID, item.Role, item.Kind = "result:"+callID, callID, "tool", "tool_result"
			if c.delegated {
				item.Kind = "delegation_result"
			}
			result := mapValueDefault(data["result"])
			payload := map[string]any{"tool": c.tool, "content": firstNonNil(result["content"], result["detailedContent"]), "is_error": data["success"] == false}
			details := map[string]any{}
			if failure := mapValue(data["error"]); failure != nil {
				details["error"] = firstString(failure["message"], failure["code"])
			} else if failure := firstString(data["error"]); failure != "" {
				details["error"] = failure
			}
			if shell := mapValue(data["shellExecution"]); shell != nil && shell["exitCode"] != nil {
				details["exit_code"] = shell["exitCode"]
			}
			if at, ok := parseTime(timestamp); ok && !c.started.IsZero() {
				details["duration_ms"] = at.Sub(c.started).Milliseconds()
			}
			if len(details) > 0 {
				payload["details"] = details
			}
			item.Text = jsonText(payload)
			add(item)
		case "session.usage_record":
			usage := mapValueDefault(data["usage"])
			agent.model = firstString(usage["model"], agent.model)
			agent.effort = updatedEffort(agent.effort, usage)
			api := firstString(usage["apiCallId"])
			keys := []string{}
			for _, id := range []string{api, firstString(usage["providerCallId"])} {
				if id != "" {
					keys = append(keys, agentID+"\x00"+id)
				}
			}
			var r *receipt
			for _, key := range keys {
				if r = byRequest[key]; r != nil {
					break
				}
			}
			if r == nil {
				item := base
				item.RawText, item.Role, item.Kind = "", "system", "metadata"
				item.Model, item.Effort = agent.model, agent.effort
				r = &receipt{agent: agent, index: add(item), model: agent.model, usage: map[string]any{}}
				receipts = append(receipts, r)
			}
			for _, key := range keys {
				byRequest[key] = r
			}
			// A later report of a request supersedes the fields it carries:
			// its model, the message it paid for, and its tokens.
			stored := &r.agent.conversation.Messages[r.index]
			if message := paidFor[agentID+"\x00"+api]; message != "" {
				stored.NativeID = message + ":usage"
			}
			r.request = firstString(api, r.request, usage["providerCallId"])
			if model := firstString(usage["model"]); model != "" {
				r.model, stored.Model = model, model
			}
			maps.Copy(r.usage, copilotTokenUsage(usage))
			stored.Text = jsonText(map[string]any{"type": "token_usage", "usage": r.usage, "usage_message_id": defaultString(r.request, eventID)})
		case "session.shutdown":
			// Each shutdown totals the whole session so far, receipts included.
			lastShutdown, shutdownAt, shutdownLocator = data, timestamp, base.EvidenceLocator
			beforeShutdown = receipted()
		case "session.error", "assistant.error", "error":
			item := base
			item.Role, item.Kind, item.Text = "system", "error", firstString(data["message"], data["error"], jsonText(data))
			add(item)
		}
	}
	// Releases that wrote no receipts still totalled each model at shutdown,
	// and a session resumed after an upgrade holds both. What the last
	// shutdown totals beyond the receipts before it is usage no receipt
	// records. Its ID names no line, so a later total replaces it rather
	// than adding to it.
	if lastShutdown != nil {
		metrics := mapValueDefault(lastShutdown["modelMetrics"])
		models := make([]string, 0, len(metrics))
		for model := range metrics {
			models = append(models, model)
		}
		sort.Strings(models)
		for _, model := range models {
			residual, positive := map[string]any{}, false
			for key, value := range copilotTokenUsage(mapValueDefault(mapValueDefault(metrics[model])["usage"])) {
				total, _ := number(value)
				left := max(total-beforeShutdown[model][key], 0)
				residual[key] = left
				positive = positive || left > 0
			}
			if !positive {
				continue
			}
			item := MessageRecord{NativeID: "usage:shutdown:" + model, Role: "system", Kind: "metadata", Model: model, CreatedAt: shutdownAt, EvidenceLocator: shutdownLocator, Selected: true,
				Text: jsonText(map[string]any{"type": "token_usage", "usage": residual, "source": "shutdown_residual"})}
			main.conversation.Messages = append(main.conversation.Messages, item)
		}
	}
	if len(main.conversation.Messages) == 0 {
		return nil
	}
	purpose, outcome := agentPurposeOutcome(main.conversation.Messages)
	title := firstString(workspace["name"], workspace["summary"])
	if !meaningfulWorkspaceTitle(title, sessionID) {
		title = defaultString(purpose, "Copilot "+short(sessionID))
	}
	client := workspace["client_name"]
	metadata := map[string]any{"branch": branch}
	if client != "" {
		metadata["client_name"] = client
	}
	if workspace["user_named"] == "true" {
		metadata["title_kind"] = "custom"
	}
	record := &WorkspaceRecord{SourceID: sessionID, SourceKind: "copilot", Title: title, Account: a.config.Account, Purpose: purpose, Outcome: outcome, ActivityAt: ended, Location: cwd, Repository: a.repository(firstString(workspace["git_root"], cwd), workspace["repository"]), Metadata: metadata}
	for _, id := range order {
		agent := agents[id]
		if len(agent.conversation.Messages) == 0 {
			continue
		}
		conversation := agent.conversation
		conversation.Provider, conversation.Account, conversation.Origin, conversation.Coverage = "copilot", a.config.Account, origin, "complete"
		conversation.Harness, conversation.HarnessVersionFirst, conversation.HarnessVersionLast = "copilot-cli", version, version
		conversation.Model, conversation.Effort = agent.model, agent.effort
		if client != "" {
			conversation.NativeContext = map[string]any{"client_name": client}
		}
		if id == "" {
			conversation.NativeID = sessionID
		} else {
			conversation.ParentNativeID = sessionID
		}
		conversation.StartedAt, conversation.EndedAt = conversation.Messages[0].CreatedAt, conversation.Messages[len(conversation.Messages)-1].CreatedAt
		record.Conversations = append(record.Conversations, conversation)
	}
	record.Conversations[0].StartedAt, record.Conversations[0].EndedAt = started, ended
	return record
}

// copilotTokenUsage is a Copilot usage record in the shape token accounting
// reads: inputTokens already counts the cache reads and writes.
func copilotTokenUsage(usage map[string]any) map[string]any {
	out := map[string]any{}
	for key, source := range map[string]string{"input_tokens": "inputTokens", "output_tokens": "outputTokens", "cached_input_tokens": "cacheReadTokens", "cache_write_input_tokens": "cacheWriteTokens", "reasoning_output_tokens": "reasoningTokens"} {
		if value, ok := usage[source]; ok {
			out[key] = value
		}
	}
	return out
}
