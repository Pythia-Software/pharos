package archive

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// clineAdapter reads Cline sessions: ~/.cline/data (or $CLINE_DATA_DIR, or
// $CLINE_DIR/data), or its sessions folder. Each session is a folder named
// for it, holding <id>.json, its manifest (title, place, totals), and
// <id>.messages.json, its messages as Anthropic content blocks; the terminal
// assistant message of each request carries the request's metrics. Cline
// keeps provider credentials under settings and an index in db; neither is
// read or captured, and nor is a session's compacted context
// (<id>.compaction.json), which repeats its messages.
type clineAdapter struct{ baseAdapter }

// clineExtractor versions the parser; bump it when parsing changes.
const clineExtractor = "cline-v1"

func (a *clineAdapter) partExtractor() string { return clineExtractor }

// sessionsDir is the folder holding the sessions.
func (a *clineAdapter) sessionsDir() string {
	if info, err := os.Stat(filepath.Join(a.config.Path, "sessions")); err == nil && info.IsDir() {
		return filepath.Join(a.config.Path, "sessions")
	}
	return a.config.Path
}

// clineSession is one session's manifest and messages.
type clineSession struct{ id, manifest, messages string }

func (a *clineAdapter) sessions() ([]clineSession, error) {
	dir := a.sessionsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	sessions := []clineSession{}
	for _, entry := range entries {
		if err := a.context().Err(); err != nil {
			return nil, err
		}
		if !entry.IsDir() {
			continue
		}
		id := entry.Name()
		messages := filepath.Join(dir, id, id+".messages.json")
		if info, err := os.Stat(messages); err != nil || !info.Mode().IsRegular() {
			continue
		}
		session := clineSession{id: id, messages: messages}
		if info, err := os.Stat(filepath.Join(dir, id, id+".json")); err == nil && info.Mode().IsRegular() {
			session.manifest = filepath.Join(dir, id, id+".json")
		}
		sessions = append(sessions, session)
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].id < sessions[j].id })
	return sessions, nil
}

// captureFiles lists every file the adapter reads.
func (a *clineAdapter) captureFiles() ([]string, error) {
	sessions, err := a.sessions()
	if err != nil {
		return nil, err
	}
	files := []string{}
	for _, session := range sessions {
		for _, file := range []string{session.manifest, session.messages} {
			if file != "" {
				files = append(files, file)
			}
		}
	}
	sort.Strings(files)
	return files, nil
}

func (a *clineAdapter) Fingerprint() (string, error) {
	files, err := a.captureFiles()
	if err != nil {
		return "", err
	}
	rows := []string{}
	for _, file := range files {
		if info, err := a.files().Stat(file); err == nil {
			relative, _ := filepath.Rel(a.config.Path, file)
			rows = append(rows, fmt.Sprintf("%s:%d:%d", filepath.ToSlash(relative), info.Size(), info.ModTime().UnixNano()))
		}
	}
	return clineExtractor + ":" + hashBytes([]byte(strings.Join(rows, "\n"))), nil
}

func (a *clineAdapter) Discover(emit func(WorkspaceRecord) error) error {
	return a.discoverParts(nil, func(record *WorkspaceRecord, _ []sourcePart) error {
		if record == nil {
			return nil
		}
		return emit(*record)
	})
}

func (a *clineAdapter) discoverParts(unchanged func([]sourcePart) bool, emit func(*WorkspaceRecord, []sourcePart) error) error {
	plan, err := a.planParts()
	if err != nil {
		return err
	}
	return plan.discover(a.context(), unchanged, emit)
}

// planParts plans one group per session: its messages, which version it,
// and its manifest, which Cline rewrites with its totals and title.
func (a *clineAdapter) planParts() (*sourceWorkPlan, error) {
	sessions, err := a.sessions()
	if err != nil {
		return nil, err
	}
	plan := &sourceWorkPlan{continueOnError: true}
	var failures []error
	for _, session := range sessions {
		parts := []sourcePart{}
		for _, file := range []string{session.messages, session.manifest} {
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
			parts = append(parts, sourcePart{item: a.original(file), size: info.Size(), version: info.ModTime().UnixNano(), metadata: file == session.manifest})
		}
		if len(parts) == 0 {
			continue
		}
		plan.groups = append(plan.groups, plannedPartGroup{parts: parts, read: func(emit partEmitter) error {
			record, read, err := a.parseSession(session)
			if err != nil {
				return fmt.Errorf("parse %s: %w", session.messages, err)
			}
			return emit(record, read)
		}})
	}
	plan.discoveryError = errors.Join(failures...)
	return plan, nil
}

// readJSON reads a session file and its part, as it was opened.
func (a *clineAdapter) readJSON(path string, metadata bool) (map[string]any, sourcePart, error) {
	info, err := a.files().Stat(path)
	if err != nil {
		return nil, sourcePart{}, err
	}
	value, version, err := a.files().ReadJSONVersion(path)
	if err != nil {
		return nil, sourcePart{}, err
	}
	object := mapValue(value)
	if object == nil {
		return nil, sourcePart{}, fmt.Errorf("%s is not a JSON object", path)
	}
	return object, sourcePart{item: a.original(path), size: info.Size(), version: version, metadata: metadata}, nil
}

func (a *clineAdapter) parseSession(session clineSession) (*WorkspaceRecord, []sourcePart, error) {
	read := []sourcePart{}
	manifest := map[string]any{}
	if session.manifest != "" {
		value, part, err := a.readJSON(session.manifest, true)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, nil, err
		}
		if err == nil {
			manifest = value
			read = append(read, part)
		}
	}
	transcript, part, err := a.readJSON(session.messages, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, read, nil
	} else if err != nil {
		return nil, nil, err
	}
	read = append([]sourcePart{part}, read...)
	// A file without its list of messages, as one cut short or from
	// another release, would replace the session with nothing.
	if _, ok := transcript["messages"].([]any); !ok {
		return nil, nil, errors.New("no list of messages")
	}
	record := a.record(session, manifest, transcript)
	if record == nil {
		return nil, read, nil
	}
	// The messages alone version the session; the manifest, rewritten with
	// its totals, is metadata (sourcePart.metadata).
	observe(record, part.version)
	return record, read, nil
}

var (
	// clineUserInput is the wrapper Cline puts around what a person typed,
	// naming the mode (plan or act) they sent it in.
	clineUserInput = regexp.MustCompile(`(?s)^\s*<user_input(?:\s+mode="([^"]*)")?>(.*)</user_input>\s*$`)
	// clineModeNotice is Cline's note, inside the wrapper, that the person
	// switched modes before sending.
	clineModeNotice = regexp.MustCompile(`(?s)<mode_notice>(.*?)</mode_notice>\s*`)
)

func (a *clineAdapter) record(session clineSession, manifest, transcript map[string]any) *WorkspaceRecord {
	origin := a.original(session.messages)
	meta := mapValueDefault(manifest["metadata"])
	sessionID := defaultString(firstString(transcript["sessionId"], manifest["session_id"]), session.id)
	version := firstString(mapValueDefault(meta["sessionHistoryOrigin"])["version"], mapValueDefault(transcript["origin"])["version"])
	model := firstString(meta["modelId"], manifest["model"])
	messages := []MessageRecord{}
	if prompt := firstString(transcript["system_prompt"]); strings.TrimSpace(prompt) != "" {
		messages = append(messages, injectedContext(MessageRecord{NativeID: "system_prompt", EvidenceLocator: origin, Selected: true, CreatedAt: iso(manifest["started_at"])}, "cline_system_prompt", prompt))
	}
	toolNames, delegated := map[string]string{}, map[string]bool{}
	started, ended := iso(manifest["started_at"]), iso(manifest["ended_at"])
	list, _ := transcript["messages"].([]any)
	for index, raw := range list {
		message := mapValue(raw)
		if message == nil {
			continue
		}
		id := defaultString(message["id"], fmt.Sprintf("message-%d", index))
		role := firstString(message["role"])
		created := iso(message["ts"])
		if created != "" {
			started = defaultString(started, created)
			ended = created
		}
		if info := mapValue(message["modelInfo"]); info != nil {
			model = firstString(info["id"], model)
		}
		base := MessageRecord{Role: role, Model: model, CreatedAt: created, EvidenceLocator: fmt.Sprintf("%s#%s", origin, id), Selected: true}
		blocks, _ := message["content"].([]any)
		if text, _ := message["content"].(string); text != "" {
			blocks = []any{map[string]any{"type": "text", "text": text}}
		}
		texts := 0
		for blockIndex, rawBlock := range blocks {
			block := mapValue(rawBlock)
			if block == nil {
				continue
			}
			// The block is the message's raw text, so nothing in a person's
			// words is read as protocol, such as pasted usage.
			item := base
			item.NativeID, item.RawText = fmt.Sprintf("%s:block:%d", id, blockIndex), jsonText(block)
			callID := firstString(block["id"], block["tool_use_id"])
			switch kind := firstString(block["type"]); kind {
			case "text":
				text := firstString(block["text"])
				if role == "user" {
					var notices []string
					text, notices = clineUserText(text)
					for n, notice := range notices {
						note := item
						note.NativeID = fmt.Sprintf("%s:notice:%d", item.NativeID, n)
						messages = append(messages, injectedContext(note, "cline_mode_notice", notice))
					}
				}
				if strings.TrimSpace(text) == "" {
					continue
				}
				// The first text is the message's own, by its ID alone.
				if texts == 0 {
					item.NativeID = id
				}
				item.Kind, item.Text = "message", text
				messages = append(messages, item)
				texts++
			case "thinking":
				messages = append(messages, reasoningRecord(item, firstString(block["thinking"]), "text", ""))
			case "redacted_thinking":
				messages = append(messages, reasoningRecord(item, "", "", "encrypted"))
			case "tool_use":
				name := defaultString(block["name"], "tool")
				toolNames[callID] = name
				item.Kind = "tool_call"
				if matchesFold(name, "task", "agent", "spawn_agent", "delegate", "workflow", "team_run_task") {
					item.Kind = "delegation"
					delegated[callID] = true
				}
				item.CallID, item.Text = callID, jsonText(map[string]any{"tool": name, "input": clineToolInput(block["input"])})
				messages = append(messages, item)
			case "tool_result":
				item.Role, item.Kind, item.CallID = "tool", "tool_result", callID
				if delegated[callID] {
					item.Kind = "delegation_result"
				}
				item.Text = jsonText(map[string]any{"tool": defaultString(toolNames[callID], firstString(block["name"])), "content": block["content"], "is_error": block["is_error"] == true})
				messages = append(messages, item)
			case "image", "document", "file":
				item.Kind, item.Text = "attachment", jsonText(map[string]any{"type": kind})
				messages = append(messages, item)
			}
		}
		// The request's metrics, on the message that ended it.
		if metrics := mapValue(message["metrics"]); metrics != nil {
			usage := clineTokenUsage(metrics)
			if len(usage) > 0 {
				item := base
				item.NativeID, item.Role, item.Kind, item.RawText = id+":usage", "system", "metadata", ""
				item.Text = jsonText(map[string]any{"type": "token_usage", "usage": usage, "usage_message_id": id})
				messages = append(messages, item)
			}
		}
	}
	// The system prompt alone is no session to keep.
	if len(messages) == 0 || len(messages) == 1 && messages[0].NativeID == "system_prompt" {
		return nil
	}
	purpose, outcome := agentPurposeOutcome(messages)
	title := firstString(meta["title"])
	if !meaningfulWorkspaceTitle(title, sessionID) {
		title = defaultString(purpose, "Cline "+short(sessionID))
	}
	cwd := firstString(manifest["cwd"], manifest["workspace_root"])
	metadata := map[string]any{}
	if branch := firstString(mapValueDefault(meta["git"])["branch"]); branch != "" {
		metadata["branch"] = branch
	}
	if client := firstString(manifest["source"], mapValueDefault(transcript["origin"])["source"]); client != "" {
		metadata["client_name"] = client
	}
	if cost, ok := number(meta["totalCost"]); ok && cost > 0 {
		metadata["reported_cost_usd"] = cost
	}
	conversation := ConversationRecord{NativeID: sessionID, Provider: "cline", Account: a.config.Account, Origin: origin, Coverage: "complete", Harness: "cline",
		HarnessVersionFirst: version, HarnessVersionLast: version, Model: model, StartedAt: started, EndedAt: ended, Messages: messages}
	if provider := firstString(manifest["provider"]); provider != "" {
		conversation.NativeContext = map[string]any{"model_provider": provider}
	}
	return &WorkspaceRecord{SourceID: sessionID, SourceKind: "cline", Title: title, Account: a.config.Account, Purpose: purpose, Outcome: outcome, ActivityAt: ended,
		Location: cwd, Repository: a.repository(cwd, firstString(mapValueDefault(meta["git"])["url"])), Metadata: metadata, Conversations: []ConversationRecord{conversation}}
}

// clineUserText is what a person typed, without Cline's wrapper, and the
// mode notices Cline added inside it.
func clineUserText(text string) (string, []string) {
	match := clineUserInput.FindStringSubmatch(text)
	if match == nil {
		return text, nil
	}
	inner := match[2]
	notices := []string{}
	for _, notice := range clineModeNotice.FindAllStringSubmatch(inner, -1) {
		notices = append(notices, strings.TrimSpace(notice[1]))
	}
	return strings.TrimSpace(clineModeNotice.ReplaceAllString(inner, "")), notices
}

// clineToolInput is a tool call's input, which Cline keeps as JSON text.
func clineToolInput(input any) any {
	if text, ok := input.(string); ok {
		var value any
		if json.Unmarshal([]byte(text), &value) == nil {
			return value
		}
	}
	return input
}

// clineTokenUsage maps a request's metrics. Cline's input includes the
// cached input it reads and writes, which Pharos's totals count once.
func clineTokenUsage(metrics map[string]any) map[string]any {
	out := map[string]any{}
	for key, source := range map[string]string{"input_tokens": "inputTokens", "output_tokens": "outputTokens", "cached_input_tokens": "cacheReadTokens", "cache_write_input_tokens": "cacheWriteTokens"} {
		if value, ok := metrics[source]; ok {
			out[key] = value
		}
	}
	return out
}
