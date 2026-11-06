package archive

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// vscodeCopilotAdapter reads GitHub Copilot Chat sessions from a VS Code user
// folder (~/Library/Application Support/Code/User, or the Insiders, VSCodium
// or Positron one). VS Code keeps each session in its window's workspace
// storage, workspaceStorage/<hash>/chatSessions/<id>.jsonl, beside the
// workspace.json naming the folder it was opened on; a window without a
// folder keeps its sessions in globalStorage/emptyWindowChatSessions. A
// .jsonl session is a journal of operations on the session object; an older
// release wrote the object whole, as <id>.json. Only these files are read or
// captured: the rest of the user folder holds settings, extension state and
// other extensions' secrets.
type vscodeCopilotAdapter struct{ baseAdapter }

// vscodeCopilotExtractor versions the parser; bump it when parsing changes.
const vscodeCopilotExtractor = "vscode-copilot-v1"

func (a *vscodeCopilotAdapter) partExtractor() string { return vscodeCopilotExtractor }

// vscodeSession is one session's file and the workspace.json of the window
// it belongs to ("" for a window without a folder).
type vscodeSession struct{ id, file, workspace string }

// sessions lists the sessions, and the folders that could not be listed: a
// window's failure leaves the others' sessions to read.
func (a *vscodeCopilotAdapter) sessions() ([]vscodeSession, []error, error) {
	type folder struct{ dir, workspace string }
	folders := []folder{}
	var failures []error
	storage := filepath.Join(a.config.Path, "workspaceStorage")
	entries, err := a.files().ReadDir(storage)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		failures = append(failures, err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			folders = append(folders, folder{filepath.Join(storage, entry.Name(), "chatSessions"), filepath.Join(storage, entry.Name(), "workspace.json")})
		}
	}
	folders = append(folders, folder{filepath.Join(a.config.Path, "globalStorage", "emptyWindowChatSessions"), ""})
	found := map[string]vscodeSession{}
	versions := map[string]int64{}
	for _, folder := range folders {
		if err := a.context().Err(); err != nil {
			return nil, nil, err
		}
		entries, err := a.files().ReadDir(folder.dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			failures = append(failures, err)
			continue
		}
		workspace := ""
		if folder.workspace != "" {
			if info, err := a.files().Stat(folder.workspace); err == nil && info.Mode().IsRegular() {
				workspace = folder.workspace
			}
		}
		// A journal replaces the .json an older release wrote for the session.
		files := map[string]string{}
		for _, entry := range entries {
			name := entry.Name()
			id, journal := strings.CutSuffix(name, ".jsonl")
			if !journal {
				id, _ = strings.CutSuffix(name, ".json")
			}
			if id == name || id == "" || !entry.Type().IsRegular() {
				continue
			}
			if journal || files[id] == "" {
				files[id] = filepath.Join(folder.dir, name)
			}
		}
		for id, file := range files {
			info, err := a.files().Stat(file)
			if errors.Is(err, os.ErrNotExist) {
				continue
			} else if err != nil {
				// A copy that cannot be looked up may be the newest.
				failures = append(failures, err)
				continue
			}
			// The same session in two windows' storage is read from the copy
			// written last.
			if _, seen := found[id]; seen && info.ModTime().UnixNano() <= versions[id] {
				continue
			}
			found[id], versions[id] = vscodeSession{id: id, file: file, workspace: workspace}, info.ModTime().UnixNano()
		}
	}
	sessions := make([]vscodeSession, 0, len(found))
	for _, session := range found {
		sessions = append(sessions, session)
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].id < sessions[j].id })
	return sessions, failures, nil
}

// captureFiles lists every file the adapter reads. A capture missing a
// window's sessions is not made.
func (a *vscodeCopilotAdapter) captureFiles() ([]string, error) {
	sessions, failures, err := a.sessions()
	if err == nil {
		err = errors.Join(failures...)
	}
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	files := []string{}
	for _, session := range sessions {
		for _, file := range []string{session.file, session.workspace} {
			if file != "" && !seen[file] {
				seen[file] = true
				files = append(files, file)
			}
		}
	}
	sort.Strings(files)
	return files, nil
}

func (a *vscodeCopilotAdapter) Fingerprint() (string, error) {
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
	return vscodeCopilotExtractor + ":" + hashBytes([]byte(strings.Join(rows, "\n"))), nil
}

func (a *vscodeCopilotAdapter) Discover(emit func(WorkspaceRecord) error) error {
	return a.discoverParts(nil, func(record *WorkspaceRecord, _ []sourcePart) error {
		if record == nil {
			return nil
		}
		return emit(*record)
	})
}

func (a *vscodeCopilotAdapter) discoverParts(unchanged func([]sourcePart) bool, emit func(*WorkspaceRecord, []sourcePart) error) error {
	plan, err := a.planParts()
	if err != nil {
		return err
	}
	return plan.discover(a.context(), unchanged, emit)
}

// planParts plans one group per session: its file, which versions it, and
// its window's workspace.json, which names where it ran.
func (a *vscodeCopilotAdapter) planParts() (*sourceWorkPlan, error) {
	sessions, failures, err := a.sessions()
	if err != nil {
		return nil, err
	}
	plan := &sourceWorkPlan{continueOnError: true}
	for _, session := range sessions {
		parts := []sourcePart{}
		for _, file := range []string{session.file, session.workspace} {
			if file == "" {
				continue
			}
			info, err := a.files().Stat(file)
			if err != nil {
				if !errors.Is(err, os.ErrNotExist) {
					failures = append(failures, fmt.Errorf("stat %s: %w", file, err))
				}
				if file == session.file {
					parts = nil
					break
				}
				continue
			}
			parts = append(parts, sourcePart{item: a.original(file), size: info.Size(), version: info.ModTime().UnixNano(), metadata: file == session.workspace})
		}
		if len(parts) == 0 {
			continue
		}
		plan.groups = append(plan.groups, plannedPartGroup{parts: parts, read: func(emit partEmitter) error {
			record, read, digest, err := a.parseSession(session)
			if err != nil {
				return fmt.Errorf("parse %s: %w", session.file, err)
			}
			// The copy read may be older than one read before, in a window
			// that is gone or cannot be listed; its evidence makes ingest keep
			// the newer (guardCapture), as it does for a capture. It is the
			// identity a retained read of the same session file has
			// (retainedRecordIdentity), so a source moved between live and
			// retained reads reads the same version again, and workspace.json
			// is no part of it, so a change of folder alone is read.
			if record != nil && a.view == nil {
				values := []string{}
				for _, part := range read {
					if !part.metadata {
						values = append(values, part.item, part.signal)
					}
				}
				for _, part := range read {
					if !part.metadata {
						values = append(values, part.item, digest)
					}
				}
				record.EvidenceIdentity = hashBytes([]byte(jsonText(values)))
			}
			return emit(record, read)
		}})
	}
	plan.discoveryError = errors.Join(failures...)
	return plan, nil
}

// parseSession reads a session, and the SHA-256 of its file's bytes.
func (a *vscodeCopilotAdapter) parseSession(session vscodeSession) (*WorkspaceRecord, []sourcePart, string, error) {
	read := []sourcePart{}
	file, err := a.files().Open(session.file)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, "", nil
	} else if err != nil {
		return nil, nil, "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, nil, "", err
	}
	hash := sha256.New()
	reader := io.TeeReader(file, hash)
	var value map[string]any
	if strings.HasSuffix(session.file, ".jsonl") {
		value, err = vscodeReplay(reader)
	} else {
		var decoded any
		if decoded, err = decodeJSONValue(reader); err == nil {
			if value = mapValue(decoded); value == nil {
				err = errors.New("not a JSON object")
			}
		}
	}
	if err == nil {
		_, err = io.Copy(io.Discard, reader)
	}
	if err != nil {
		return nil, nil, "", err
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	// A session cut short or from another release would replace what was
	// indexed with nothing.
	if _, ok := value["requests"].([]any); !ok {
		return nil, nil, "", errors.New("no list of requests")
	}
	read = append(read, sourcePart{item: a.original(session.file), size: info.Size(), version: info.ModTime().UnixNano()})
	location, remote := "", ""
	if session.workspace != "" {
		manifest, version, err := a.files().ReadJSONVersion(session.workspace)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, nil, "", err
		}
		if err == nil {
			location, remote = vscodeWorkspaceLocation(mapValueDefault(manifest))
			if stat, err := a.files().Stat(session.workspace); err == nil {
				read = append(read, sourcePart{item: a.original(session.workspace), size: stat.Size(), version: version, metadata: true})
			}
		}
	}
	record := a.record(session, value, location, remote)
	if record == nil {
		return nil, read, digest, nil
	}
	// The session file alone versions the session; workspace.json is
	// metadata (sourcePart.metadata).
	observe(record, info.ModTime().UnixNano())
	return record, read, digest, nil
}

// vscodeDeleted marks a value a journal deletes.
var vscodeDeleted = &struct{}{}

// vscodeReplay rebuilds a session from its journal. The first line is the
// session as it was (kind 0); each later line sets the value at a key path
// (1), cuts the array there to an index and appends to it (2), or deletes the
// value (3). An operation on a path that is not there is skipped, as is an
// unknown kind. A last line without its newline is being written and is left
// for the next read.
func vscodeReplay(reader io.Reader) (map[string]any, error) {
	lines := bufio.NewReader(reader)
	var state any
	for number := 1; ; number++ {
		line, err := lines.ReadBytes('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		complete := err == nil
		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			decoder := json.NewDecoder(bytes.NewReader(trimmed))
			decoder.UseNumber()
			var operation map[string]any
			if decodeErr := decoder.Decode(&operation); decodeErr != nil {
				if !complete && number > 1 {
					break
				}
				return nil, fmt.Errorf("line %d: %w", number, decodeErr)
			}
			kind, _ := number64(operation["kind"])
			if number == 1 {
				if kind != 0 {
					return nil, errors.New("the journal does not begin with the session")
				}
				state = operation["v"]
			} else {
				keys, _ := operation["k"].([]any)
				switch kind {
				case 1:
					value := operation["v"]
					state, _ = vscodeApply(state, keys, func(any) (any, bool) { return value, true })
				case 2:
					values, _ := operation["v"].([]any)
					index, indexed := number64(operation["i"])
					state, _ = vscodeApply(state, keys, func(current any) (any, bool) {
						list, ok := current.([]any)
						if !ok && current != nil {
							return current, false
						}
						if indexed && index >= 0 && int(index) < len(list) {
							list = list[:index]
						}
						return append(list, values...), true
					})
				case 3:
					state, _ = vscodeApply(state, keys, func(any) (any, bool) { return vscodeDeleted, true })
				}
			}
		}
		if !complete {
			break
		}
	}
	session := mapValue(state)
	if session == nil {
		return nil, errors.New("the journal holds no session")
	}
	return session, nil
}

// vscodeApply applies change to the value at path under node and returns
// node as changed, or false if the path is not there.
func vscodeApply(node any, path []any, change func(any) (any, bool)) (any, bool) {
	if len(path) == 0 {
		return change(node)
	}
	switch value := node.(type) {
	case map[string]any:
		key, ok := path[0].(string)
		if !ok {
			return node, false
		}
		child, exists := value[key]
		if !exists && len(path) > 1 {
			return node, false
		}
		updated, ok := vscodeApply(child, path[1:], change)
		if !ok {
			return node, false
		}
		if updated == any(vscodeDeleted) {
			delete(value, key)
		} else {
			value[key] = updated
		}
		return value, true
	case []any:
		index, ok := number64(path[0])
		// An index past the end would pad the array with nothing.
		if !ok || index < 0 || int(index) > len(value) || int(index) == len(value) && len(path) > 1 {
			return node, false
		}
		var child any
		if int(index) < len(value) {
			child = value[index]
		}
		updated, ok := vscodeApply(child, path[1:], change)
		if !ok {
			return node, false
		}
		switch {
		case updated == any(vscodeDeleted):
			if int(index) < len(value) {
				value[index] = nil
			}
		case int(index) == len(value):
			value = append(value, updated)
		default:
			value[index] = updated
		}
		return value, true
	}
	return node, false
}

// number64 is a whole number from JSON.
func number64(value any) (int64, bool) {
	n, ok := number(value)
	if !ok || n != float64(int64(n)) {
		return 0, false
	}
	return int64(n), true
}

// vscodeWorkspaceLocation is the folder a window was opened on, from its
// workspace.json: a folder URI, or a .code-workspace file's. A remote
// window's (SSH, WSL, a container) is a path on that remote host.
func vscodeWorkspaceLocation(manifest map[string]any) (location, remote string) {
	raw := firstString(manifest["folder"])
	workspaceFile := raw == ""
	if workspaceFile {
		raw = firstString(manifest["workspace"])
	}
	scheme, rest, _ := strings.Cut(raw, "://")
	// The remote's name is escaped (ssh-remote%2Bhost), which a URL's host may
	// not be, so the URI is split by hand.
	host, location, _ := strings.Cut(rest, "/")
	location, err := url.PathUnescape("/" + location)
	if err != nil || location == "/" {
		return "", ""
	}
	if workspaceFile {
		location = path.Dir(location)
	}
	switch scheme {
	case "file":
		return filepath.FromSlash(location), ""
	case "vscode-remote":
		if remote, err := url.PathUnescape(host); err == nil {
			return location, remote
		}
	}
	return "", ""
}

var (
	// vscodeCallSuffix is what Copilot adds to a tool call's ID in its own
	// record of the model's rounds.
	vscodeCallSuffix = regexp.MustCompile(`__vscode-\d+$`)
	// vscodeEmptyFence is the empty code block VS Code shows an edit in.
	vscodeEmptyFence = regexp.MustCompile("\n?```\\s*\n\\s*```\n?")
)

// vscodeRoundCall is what the model's rounds say of a tool call.
type vscodeRoundCall struct {
	name, round, at string
	input           any
	result          any
	answered        bool
}

// vscodeToolCall is one tool call in a response: the first place it was
// shown, with what it was last shown as.
type vscodeToolCall struct {
	position int
	item     map[string]any
}

func (a *vscodeCopilotAdapter) record(session vscodeSession, value map[string]any, location, remote string) *WorkspaceRecord {
	sessionID := defaultString(value["sessionId"], session.id)
	// The session is known by its ID in the user folder, not by the file it
	// was read from: a newer copy in another window, or a journal replacing
	// its .json, is the same copy of it.
	origin := filepath.Join(a.original(a.config.Path), "chatSessions", sessionID)
	locator := a.original(session.file)
	started, ended := iso(value["creationDate"]), iso(value["lastMessageDate"])
	messages := []MessageRecord{}
	children := map[string]*vscodeSubagent{}
	childOrder := []string{}
	subagent := func(id string) *vscodeSubagent {
		if children[id] == nil {
			children[id] = &vscodeSubagent{conversation: ConversationRecord{NativeID: id, ParentNativeID: sessionID, AgentDepth: 1}}
			childOrder = append(childOrder, id)
		}
		return children[id]
	}
	model, versionFirst, version := "", "", ""
	requests, _ := value["requests"].([]any)
	compactions := vscodeCompactions(requests)
	for index, raw := range requests {
		request := mapValue(raw)
		if request == nil {
			continue
		}
		id := defaultString(request["requestId"], fmt.Sprintf("request-%d", index))
		created := iso(request["timestamp"])
		started = defaultString(started, created)
		agent := mapValueDefault(request["agent"])
		if v := firstString(agent["extensionVersion"]); v != "" {
			versionFirst, version = defaultString(versionFirst, v), v
		}
		result := mapValueDefault(request["result"])
		meta := mapValueDefault(result["metadata"])
		requestModel := firstString(meta["resolvedModel"])
		if requestModel == "" {
			// A model Copilot chose itself (auto) is known only once it ran.
			if requested := strings.TrimPrefix(firstString(request["modelId"]), "copilot/"); requested != "auto" {
				requestModel = requested
			}
		}
		model = defaultString(requestModel, model)
		completed := iso(mapValueDefault(request["modelState"])["completedAt"])
		if completed == "" {
			if at, ok := number(request["timestamp"]); ok {
				if elapsed, ok := number(firstNonNil(request["elapsedMs"], mapValueDefault(result["timings"])["totalElapsed"])); ok {
					completed = iso(at + elapsed)
				}
			}
		}
		for _, at := range []string{created, completed} {
			if at != "" && at > ended {
				ended = at
			}
		}
		// The request's ID stands in for each record's raw text, so JSON in
		// a reply, a thought or a subagent's answer is never read as usage.
		items, _ := request["response"].([]any)
		// An undone request stays in the session, disabled until the next is
		// sent: whole, or after an undo stop in its response (isHidden in an
		// older release). Its records are kept, not selected.
		removed, cut := vscodeDisabled(request, items)
		base := MessageRecord{Model: requestModel, CreatedAt: created, EvidenceLocator: fmt.Sprintf("%s#%s", locator, id), Selected: !removed,
			RawText: jsonText(map[string]any{"type": "vscode_request", "id": id})}
		// at is the base of what the response shows at a position.
		at := func(position int) MessageRecord {
			item := base
			item.Selected = item.Selected && (cut < 0 || position < cut)
			return item
		}
		// A compaction made for this request from an earlier one's rounds
		// replaced what came before its prompt.
		for _, compaction := range compactions[id] {
			if compaction.before {
				messages = append(messages, vscodeCompactionRecord(base, id, compaction))
			}
		}
		prompt := mapValueDefault(request["message"])
		if text := firstString(prompt["text"]); strings.TrimSpace(text) != "" {
			// The prompt as sent is its raw text.
			item := base
			item.NativeID, item.Role, item.Kind, item.Text, item.RawText = id, "user", "message", text, jsonText(prompt)
			// VS Code sends some requests itself, such as a background
			// terminal's notice that its command completed.
			if request["isSystemInitiated"] == true {
				source := "vscode_system_initiated"
				if firstString(request["terminalExecutionId"]) != "" {
					source = "vscode_terminal_notification"
				}
				item = injectedContext(item, source, text)
			}
			messages = append(messages, item)
		}
		messages = append(messages, vscodeAttachments(base, id, request)...)
		// The rounds name each call's input and hold what it returned.
		rounds := map[string]*vscodeRoundCall{}
		toolResults := mapValueDefault(meta["toolCallResults"])
		roundList, _ := meta["toolCallRounds"].([]any)
		for _, rawRound := range roundList {
			round := mapValueDefault(rawRound)
			roundID := firstString(round["id"])
			calls, _ := round["toolCalls"].([]any)
			for _, rawCall := range calls {
				call := mapValueDefault(rawCall)
				full := firstString(call["id"])
				if full == "" {
					continue
				}
				entry := &vscodeRoundCall{name: firstString(call["name"]), round: roundID, at: iso(round["timestamp"]), input: clineToolInput(call["arguments"])}
				entry.result, entry.answered = toolResults[full]
				rounds[vscodeCallSuffix.ReplaceAllString(full, "")] = entry
			}
		}
		tools := map[string]*vscodeToolCall{}
		for position, rawItem := range items {
			item := mapValue(rawItem)
			if kind := firstString(item["kind"]); item == nil || kind != "toolInvocationSerialized" && kind != "toolInvocation" {
				continue
			}
			callID := firstString(item["toolCallId"])
			if callID == "" {
				continue
			}
			// A call is shown again as it changes; a hidden placeholder
			// sharing a visible call's ID does not replace it.
			if existing := tools[callID]; existing != nil {
				if firstString(item["presentation"]) != "hidden" || firstString(existing.item["presentation"]) == "hidden" {
					existing.item = item
				}
				continue
			}
			tools[callID] = &vscodeToolCall{position: position, item: item}
		}
		// A compaction after one of this request's rounds follows the last of
		// its calls shown; one after a round with none, the response.
		boundaries := map[int][]vscodeCompaction{}
		trailing := []vscodeCompaction{}
		for _, compaction := range compactions[id] {
			if compaction.before {
				continue
			}
			last := -1
			for callID, call := range tools {
				if entry := rounds[callID]; entry != nil && entry.round == compaction.round && call.position > last {
					last = call.position
				}
			}
			if last < 0 {
				trailing = append(trailing, compaction)
			} else {
				boundaries[last] = append(boundaries[last], compaction)
			}
		}
		var prose strings.Builder
		proseAt := -1
		var thought strings.Builder
		thoughtAt, thoughtID := -1, ""
		flushProse := func() {
			text := strings.TrimSpace(vscodeEmptyFence.ReplaceAllString(prose.String(), "\n"))
			if text != "" {
				item := at(proseAt)
				item.NativeID, item.Role, item.Kind, item.Text = fmt.Sprintf("%s:block:%d", id, proseAt), "assistant", "message", text
				messages = append(messages, item)
			}
			prose.Reset()
			proseAt = -1
		}
		flushThought := func() {
			if text := thought.String(); strings.TrimSpace(text) != "" {
				item := at(thoughtAt)
				item.NativeID = fmt.Sprintf("%s:block:%d", id, thoughtAt)
				messages = append(messages, reasoningRecord(item, text, "text", ""))
			}
			thought.Reset()
			thoughtAt, thoughtID = -1, ""
		}
		for position, rawItem := range items {
			// What an undo stop disabled is a block of its own.
			if position == cut {
				flushProse()
				flushThought()
			}
			item := mapValue(rawItem)
			if item == nil {
				continue
			}
			switch kind := firstString(item["kind"]); kind {
			// Markdown VS Code flagged as vulnerable is still the reply.
			case "", "markdownContent", "markdownVuln":
				text := vscodeText(firstNonNil(item["value"], mapValueDefault(item["content"])["value"]))
				if text == "" {
					continue
				}
				flushThought()
				if proseAt < 0 {
					proseAt = position
				}
				prose.WriteString(text)
			case "inlineReference":
				name := vscodeInlineReference(item)
				if name != "" && name != "." && name != "/" {
					flushThought()
					if proseAt < 0 {
						proseAt = position
					}
					prose.WriteString("`" + name + "`")
				}
			case "thinking":
				flushProse()
				text := vscodeText(item["value"])
				// An empty item ends a thought.
				if text == "" {
					flushThought()
					continue
				}
				if thoughtAt >= 0 && firstString(item["id"]) != thoughtID {
					flushThought()
				}
				if thoughtAt < 0 {
					thoughtAt, thoughtID = position, firstString(item["id"])
				}
				thought.WriteString(text)
			case "toolInvocationSerialized", "toolInvocation":
				flushProse()
				flushThought()
				callID := firstString(item["toolCallId"])
				call := tools[callID]
				if call == nil || call.position != position {
					continue
				}
				calls, started := vscodeToolMessages(at(position), id, position, call.item, rounds[callID], sessionID)
				// A subagent's own calls are shown in its parent's response.
				if parent := firstString(call.item["subAgentInvocationId"]); parent != "" {
					child := subagent(parent)
					child.conversation.Messages = append(child.conversation.Messages, calls...)
				} else {
					messages = append(messages, calls...)
				}
				if started != nil {
					child := subagent(callID)
					child.conversation.AgentNickname, child.prompt, child.reply = started.conversation.AgentNickname, started.prompt, started.reply
					child.conversation.Model, child.parent = started.conversation.Model, firstString(call.item["subAgentInvocationId"])
				}
				for _, compaction := range boundaries[position] {
					messages = append(messages, vscodeCompactionRecord(at(position), id, compaction))
				}
			}
		}
		flushProse()
		flushThought()
		for _, compaction := range trailing {
			messages = append(messages, vscodeCompactionRecord(at(len(items)), id, compaction))
		}
		// What a request used was spent, undone or not.
		for index, usage := range vscodeTokenUsage(request, requestModel) {
			item := base
			item.Selected = true
			item.NativeID, item.Role, item.Kind, item.Model, item.RawText = id+":usage", "system", "metadata", usage.model, ""
			// The first record, of the model that served the request, pays for
			// its calls; another model's is its own.
			usageID := id
			if index > 0 {
				usageID = id + ":" + usage.model
				item.NativeID = usageID + ":usage"
			}
			item.CreatedAt = defaultString(completed, created)
			payload := map[string]any{"type": "token_usage", "usage": usage.counts, "usage_message_id": usageID}
			if usage.scope != "" {
				payload["input_scope"] = usage.scope
			}
			item.Text = jsonText(payload)
			messages = append(messages, item)
		}
	}
	if len(messages) == 0 {
		return nil
	}
	// The session's purpose and outcome are what it still shows.
	shown := []MessageRecord{}
	for _, message := range messages {
		if message.Selected {
			shown = append(shown, message)
		}
	}
	purpose, outcome := agentPurposeOutcome(shown)
	title := firstString(value["customTitle"])
	if !meaningfulWorkspaceTitle(title, sessionID) {
		title = defaultString(purpose, "VS Code Copilot "+short(sessionID))
	}
	metadata := map[string]any{}
	if variant := a.variant(); variant != "" {
		metadata["client_name"] = variant
	}
	repository := map[string]any(nil)
	if remote != "" {
		metadata["remote"] = remote
		repository = repositoryFromName(location, "")
	} else if location != "" {
		repository = a.repository(location, "")
	}
	conversation := ConversationRecord{NativeID: sessionID, Provider: "vscode-copilot", Account: a.config.Account, Origin: origin, Coverage: "complete", Harness: "vscode-copilot",
		HarnessVersionFirst: versionFirst, HarnessVersionLast: version, Model: model, StartedAt: started, EndedAt: ended, Messages: messages}
	record := &WorkspaceRecord{SourceID: sessionID, SourceKind: "vscode-copilot", Title: title, Account: a.config.Account, Purpose: purpose, Outcome: outcome, ActivityAt: ended,
		Location: location, Repository: repository, Metadata: metadata, Conversations: []ConversationRecord{conversation}}
	depth := func(id string) int {
		levels := 1
		for seen := map[string]bool{id: true}; children[id] != nil && children[id].parent != "" && !seen[children[id].parent]; levels++ {
			id = children[id].parent
			seen[id] = true
		}
		return levels
	}
	for _, childID := range childOrder {
		agent := children[childID]
		child := &agent.conversation
		// A subagent started by another is that one's child, and its prompt
		// is that one's.
		if agent.parent != "" {
			child.ParentNativeID, child.AgentDepth = agent.parent, depth(childID)
			if agent.prompt != nil {
				agent.prompt.Sender = "agent:" + agent.parent
			}
		}
		if agent.prompt != nil {
			child.Messages = append([]MessageRecord{*agent.prompt}, child.Messages...)
		}
		if agent.reply != nil {
			child.Messages = append(child.Messages, *agent.reply)
		}
		if len(child.Messages) == 0 {
			continue
		}
		child.Provider, child.Account, child.Origin, child.Coverage = "vscode-copilot", a.config.Account, origin, "complete"
		child.Harness, child.HarnessVersionFirst, child.HarnessVersionLast = "vscode-copilot", versionFirst, version
		child.StartedAt, child.EndedAt = child.Messages[0].CreatedAt, child.Messages[len(child.Messages)-1].CreatedAt
		// A subagent may run on another model than the request that started
		// it; its calls are that model's.
		if child.Model != "" {
			for index := range child.Messages {
				child.Messages[index].Model = child.Model
			}
		}
		record.Conversations = append(record.Conversations, *child)
	}
	return record
}

// variant names the VS Code build whose user folder this is (Code, Code -
// Insiders, VSCodium, Positron).
func (a *vscodeCopilotAdapter) variant() string {
	if filepath.Base(a.config.Path) != "User" {
		return ""
	}
	return filepath.Base(filepath.Dir(a.config.Path))
}

// vscodeSubagent is a subagent's conversation: the prompt it was given, its
// own calls, and what it returned.
type vscodeSubagent struct {
	conversation  ConversationRecord
	prompt, reply *MessageRecord
	// parent is the subagent that started this one, when subagents may
	// start their own; otherwise the session.
	parent string
}

// vscodeToolMessages are a tool call and its result. A subagent's call also
// starts the subagent: its prompt and what it returned.
func vscodeToolMessages(base MessageRecord, requestID string, position int, item map[string]any, round *vscodeRoundCall, sessionID string) ([]MessageRecord, *vscodeSubagent) {
	callID := firstString(item["toolCallId"])
	name := vscodeToolName(item)
	data := mapValueDefault(item["toolSpecificData"])
	var input any
	if round != nil {
		input = round.input
		if round.at != "" {
			base.CreatedAt = round.at
		}
	}
	terminal := firstString(data["kind"]) == "terminal"
	subagent := firstString(data["kind"]) == "subagent"
	if input == nil {
		switch {
		case terminal:
			if command := firstString(mapValueDefault(data["commandLine"])["original"], mapValueDefault(data["confirmation"])["commandLine"]); command != "" {
				input = map[string]any{"command": command}
			}
		case subagent:
			input = map[string]any{"agentName": data["agentName"], "description": data["description"], "prompt": data["prompt"]}
		}
	}
	// A call the rounds do not name (one inside a subagent, or still
	// running) keeps its input in the tool's data (an MCP tool's rawInput),
	// and its output and failure in the details of its result.
	resultDetails := mapValueDefault(item["resultDetails"])
	if raw := data["rawInput"]; input == nil && raw != nil {
		input = raw
	}
	call := base
	call.NativeID, call.Role, call.Kind, call.CallID = fmt.Sprintf("%s:block:%d", requestID, position), "assistant", "tool_call", callID
	if subagent {
		call.Kind = "delegation"
	}
	call.Text = jsonText(map[string]any{"tool": name, "input": input})
	messages := []MessageRecord{call}
	var content any
	details := map[string]any{}
	if round != nil && round.answered {
		content = vscodeResultText(round.result)
	}
	if terminal {
		state := mapValueDefault(data["terminalCommandState"])
		if code, ok := number(state["exitCode"]); ok {
			details["exit_code"] = code
		}
		if duration, ok := number(state["duration"]); ok && duration > 0 {
			details["duration_ms"] = duration
		}
		if content == nil || content == "" {
			if output := firstString(mapValueDefault(data["terminalCommandOutput"])["text"]); output != "" {
				content = output
			}
		}
	}
	if subagent && (content == nil || content == "") {
		if text := vscodeText(data["result"]); text != "" {
			content = text
		}
	}
	if content == nil || content == "" {
		if output := vscodeResultDetailsText(resultDetails["output"]); output != "" {
			content = output
		}
	}
	// VS Code saves every call as complete; one the person denied or skipped
	// (or whose result they did not approve) did not succeed, whatever it
	// was shown as.
	declined, actor := vscodeDeclined(item)
	if declined != "" {
		details["confirmation"] = declined
		note := "This tool call was " + declined
		switch actor {
		case "user":
			details["status"], note = "declined", "The user "+declined+" this tool call"
		case "":
			// Without an actor saved it was most likely the person, who
			// may also have stopped the request.
			details["status"] = "declined"
		default:
			// A hook or VS Code's risk assessment decided, not the person.
			details["blocked_by"], note = actor, "A "+strings.ReplaceAll(actor, "_", " ")+" "+declined+" this tool call"
		}
		if content == nil || content == "" {
			content = note
		}
	}
	// A finished call whose output was not kept still finished; what VS Code
	// showed for it is its result.
	if (content == nil || content == "") && item["isComplete"] == true {
		if shown := vscodeText(firstNonNil(mapValueDefault(item["pastTenseMessage"])["value"], item["pastTenseMessage"])); strings.TrimSpace(shown) != "" {
			content = shown
		}
	}
	if content != nil && content != "" {
		result := base
		result.NativeID, result.Role, result.Kind, result.CallID = call.NativeID+":result", "tool", "tool_result", callID
		if subagent {
			result.Kind = "delegation_result"
		}
		// A declined call is no tool's error: its status says what became
		// of it.
		payload := map[string]any{"tool": name, "content": content, "is_error": resultDetails["isError"] == true}
		if len(details) > 0 {
			payload["details"] = details
		}
		result.Text = jsonText(payload)
		messages = append(messages, result)
	}
	if !subagent || callID == "" {
		return messages, nil
	}
	started := &vscodeSubagent{conversation: ConversationRecord{AgentNickname: firstString(data["agentName"]), Model: vscodeModelID(firstString(data["modelName"]))}}
	if text := vscodeText(data["prompt"]); strings.TrimSpace(text) != "" {
		prompt := base
		prompt.NativeID, prompt.Role, prompt.Kind, prompt.Sender, prompt.Text, prompt.Model = callID+":prompt", agentRole, "message", "agent:"+sessionID, text, ""
		started.prompt = &prompt
	}
	if text := vscodeText(data["result"]); strings.TrimSpace(text) != "" {
		reply := base
		reply.NativeID, reply.Role, reply.Kind, reply.Text, reply.Model = callID+":reply", "assistant", "message", text, ""
		started.reply = &reply
	}
	return messages, started
}

// vscodeModelID is the model ID for the name VS Code shows for it, as a
// subagent's modelName records ("Claude Haiku 4.5" is claude-haiku-4.5).
func vscodeModelID(name string) string {
	return strings.ToLower(strings.Join(strings.Fields(name), "-"))
}

// vscodeDeclined is how a call's saved confirmation turned it down
// ("denied", "skipped"), or "" if it ran: false or ToolConfirmKind Denied (0)
// or Skipped (5). A release before 1.104 saved a boolean; none means
// confirmed. The actor is who decided: "user", "hook" or "risk_assessment"
// as a newer release saves it (source), or "" when not saved. A release
// that saved none hid a call a preToolUse hook denied, so a hidden denied
// call is a hook's.
func vscodeDeclined(item map[string]any) (string, string) {
	confirmed := item["isConfirmed"]
	decision := ""
	switch kind, ok := number64(mapValueDefault(confirmed)["type"]); {
	case confirmed == false, ok && kind == 0:
		decision = "denied"
	case ok && kind == 5:
		decision = "skipped"
	default:
		return "", ""
	}
	switch source := firstString(mapValueDefault(confirmed)["source"]); source {
	case "user", "hook":
		return decision, source
	case "riskAssessment":
		return decision, "risk_assessment"
	case "":
		if decision == "denied" && firstString(item["presentation"]) == "hidden" {
			return decision, "hook"
		}
	}
	return decision, ""
}

// vscodeMCPPrefixUnsafe is what VS Code replaces with _ in a server's name
// to make the prefix of its tools' IDs.
var vscodeMCPPrefixUnsafe = regexp.MustCompile(`[^a-z0-9_.-]+`)

// vscodeToolName is a call's tool. VS Code names an MCP server's tool
// mcp_<server>_<tool>, from the name the server announced or else its
// label, and saves the server in the call's source; it is named as other
// harnesses name MCP tools, mcp__<server>__<tool>.
func vscodeToolName(item map[string]any) string {
	id := defaultString(item["toolId"], "tool")
	source := mapValueDefault(item["source"])
	server := firstString(source["label"], source["serverLabel"])
	rest, prefixed := strings.CutPrefix(id, "mcp_")
	if firstString(source["type"]) != "mcp" || server == "" || !prefixed {
		return id
	}
	tool := rest
	// VS Code also replaces dots throughout the ID.
	prefix := vscodeMCPPrefixUnsafe.ReplaceAllString(strings.ToLower(firstString(source["serverLabel"], source["label"])), "_")
	prefix = strings.ReplaceAll(prefix, ".", "_")
	// The prefix is cut to at most 13 characters, shorter beside a second
	// server of the same name's number.
	for n := len(prefix); n > 0; n-- {
		if after, ok := strings.CutPrefix(rest, prefix[:n]); ok {
			if after, ok = strings.CutPrefix(strings.TrimLeft(after, "0123456789"), "_"); ok && after != "" {
				tool = after
				break
			}
		}
	}
	return "mcp__" + server + "__" + tool
}

// vscodeDisabled reports whether an undone request is disabled whole, and
// otherwise the position in its response from which an undo stop disabled
// it (-1 for none). VS Code cuts the response at the stop, or just before
// the code fence opening the block the stop precedes (not a standalone
// block's, which has none).
func vscodeDisabled(request map[string]any, items []any) (bool, int) {
	disablement := mapValue(request["shouldBeRemovedOnSend"])
	if request["isHidden"] == true {
		return true, -1
	}
	if disablement == nil {
		return false, -1
	}
	stop := firstString(disablement["afterUndoStop"])
	if stop == "" {
		return true, -1
	}
	kind := func(index int) string {
		if index < 0 || index >= len(items) {
			return "none"
		}
		return firstString(mapValueDefault(items[index])["kind"])
	}
	for index, raw := range items {
		item := mapValueDefault(raw)
		if firstString(item["kind"]) != "undoStop" || firstString(item["id"]) != stop {
			continue
		}
		if kind(index+1) == "codeblockUri" && mapValueDefault(items[index+1])["isStandalone"] != true && (kind(index-1) == "" || kind(index-1) == "markdownContent") {
			index--
		}
		return false, index
	}
	return false, -1
}

// vscodeAttachments are what was attached to a request
// (variableData.variables): pasted code, a file, a selection, a symbol, an
// image, text a command or setting supplied. A reference to a tool attaches
// nothing; an image's bytes are not kept.
func vscodeAttachments(base MessageRecord, requestID string, request map[string]any) []MessageRecord {
	variables, _ := mapValueDefault(request["variableData"])["variables"].([]any)
	records := []MessageRecord{}
	for index, raw := range variables {
		variable := mapValue(raw)
		kind := firstString(variable["kind"])
		if variable == nil || kind == "tool" || kind == "toolset" || kind == "implicit" && variable["enabled"] == false {
			continue
		}
		value := variable["value"]
		text, place := "", ""
		switch kind {
		case "paste":
			text = defaultString(variable["code"], asString(value))
			place = vscodeURIText(mapValueDefault(mapValueDefault(variable["copiedFrom"])["uri"]))
			if place == "" {
				place = firstString(variable["fileName"])
			}
		case "image":
		case "element":
			text = firstString(variable["innerText"])
		default:
			if s, ok := value.(string); ok {
				text = s
			} else if uri := mapValue(value); uri != nil {
				if location := mapValue(uri["uri"]); location != nil {
					uri = location
				}
				place = vscodeURIText(uri)
			}
		}
		if place == "" {
			place = vscodeURIText(mapValueDefault(variable["uri"]))
		}
		attachment := map[string]any{"type": kind, "name": firstString(variable["name"])}
		if text != "" {
			attachment["text"] = text
		}
		if place != "" {
			attachment["path"] = place
		}
		if location := mapValue(value); location != nil && mapValue(location["range"]) != nil {
			attachment["range"] = location["range"]
		} else if copied := mapValueDefault(variable["copiedFrom"]); copied["range"] != nil {
			attachment["range"] = copied["range"]
		}
		item := base
		item.NativeID, item.Role, item.Kind, item.Text = fmt.Sprintf("%s:attachment:%d", requestID, index), "user", "attachment", jsonText(attachment)
		records = append(records, item)
	}
	return records
}

// vscodeCompaction is a summary Copilot replaced a session's history with,
// up to and including one of the model's rounds. before marks one whose
// round is an earlier request's: it replaced what came before the request.
type vscodeCompaction struct {
	round, summary string
	before         bool
}

// vscodeCompactions are each request's compactions, by request ID. Copilot
// keeps them in a request's result (metadata.summaries, or summary in an
// older release), naming the round they follow, which may be an earlier
// request's.
func vscodeCompactions(requests []any) map[string][]vscodeCompaction {
	owner := map[string]string{}
	ids := make([]string, len(requests))
	for index, raw := range requests {
		request := mapValueDefault(raw)
		ids[index] = defaultString(request["requestId"], fmt.Sprintf("request-%d", index))
		rounds, _ := mapValueDefault(mapValueDefault(request["result"])["metadata"])["toolCallRounds"].([]any)
		for _, round := range rounds {
			if roundID := firstString(mapValueDefault(round)["id"]); roundID != "" && owner[roundID] == "" {
				owner[roundID] = ids[index]
			}
		}
	}
	found := map[string][]vscodeCompaction{}
	// A round's later summary supersedes its earlier.
	add := func(requestID, roundID, summary string) {
		if strings.TrimSpace(summary) == "" {
			return
		}
		for index, existing := range found[requestID] {
			if existing.round == roundID {
				found[requestID][index].summary = summary
				return
			}
		}
		found[requestID] = append(found[requestID], vscodeCompaction{round: roundID, summary: summary, before: owner[roundID] != requestID})
	}
	for index, raw := range requests {
		meta := mapValueDefault(mapValueDefault(mapValueDefault(raw)["result"])["metadata"])
		summaries, _ := meta["summaries"].([]any)
		if summaries == nil && meta["summary"] != nil {
			summaries = []any{meta["summary"]}
		}
		for _, rawSummary := range summaries {
			summary := mapValueDefault(rawSummary)
			add(ids[index], firstString(summary["toolCallRoundId"]), firstString(summary["text"]))
		}
	}
	return found
}

// vscodeCompactionRecord marks where a compaction replaced what came before.
func vscodeCompactionRecord(base MessageRecord, requestID string, compaction vscodeCompaction) MessageRecord {
	base.NativeID, base.Role, base.Kind = fmt.Sprintf("%s:compaction:%s", requestID, compaction.round), "system", "metadata"
	base.Text = jsonText(map[string]any{"type": "compaction", "summary": compaction.summary, "round_id": compaction.round})
	return base
}

// vscodeResultDetailsText is the text of a result's saved output parts:
// embedded text, and the place of a referenced resource.
func vscodeResultDetailsText(value any) string {
	parts, _ := value.([]any)
	texts := []string{}
	for _, raw := range parts {
		part := mapValueDefault(raw)
		switch firstString(part["type"]) {
		case "embed":
			if part["isText"] == true {
				texts = append(texts, asString(part["value"]))
			}
		case "ref":
			if uri := vscodeURIText(mapValueDefault(part["uri"])); uri != "" {
				texts = append(texts, uri)
			}
		}
	}
	return strings.Join(texts, "\n")
}

// vscodeInlineReference is how a reply names what it refers to: the
// reference's name, a symbol's name, or the file of a URI or a Location
// (a web address whole), as VS Code writes it as text.
func vscodeInlineReference(item map[string]any) string {
	if name := firstString(item["name"]); name != "" {
		return name
	}
	reference := mapValueDefault(item["inlineReference"])
	if uri := mapValue(reference["uri"]); uri != nil {
		reference = uri
	} else if name := firstString(reference["name"]); name != "" {
		return name
	}
	if scheme := firstString(reference["scheme"]); scheme == "http" || scheme == "https" {
		return vscodeURIText(reference)
	}
	return path.Base(firstString(reference["path"]))
}

// vscodeURIText is a serialized URI as text.
func vscodeURIText(uri map[string]any) string {
	if external := firstString(uri["external"]); external != "" {
		return external
	}
	scheme, location := firstString(uri["scheme"]), firstString(uri["path"])
	if scheme == "" || scheme == "file" {
		return location
	}
	text := scheme + "://" + firstString(uri["authority"]) + location
	if query := firstString(uri["query"]); query != "" {
		text += "?" + query
	}
	return text
}

// vscodeText is text VS Code keeps as a string or a list of strings.
func vscodeText(value any) string {
	switch text := value.(type) {
	case string:
		return text
	case []any:
		var joined strings.Builder
		for _, part := range text {
			if piece, ok := part.(string); ok {
				joined.WriteString(piece)
			}
		}
		return joined.String()
	}
	return ""
}

// vscodeResultText is the text of a tool result as Copilot kept it: parts
// holding text, or a rendered prompt tree whose leaves hold it.
func vscodeResultText(value any) string {
	var text strings.Builder
	var walk func(any)
	walk = func(node any) {
		switch item := node.(type) {
		case []any:
			for _, child := range item {
				walk(child)
			}
		case map[string]any:
			for _, key := range []string{"text", "value"} {
				if leaf, ok := item[key].(string); ok {
					text.WriteString(leaf)
				}
			}
			for _, key := range []string{"content", "value", "node", "children"} {
				switch item[key].(type) {
				case []any, map[string]any:
					walk(item[key])
				}
			}
		}
	}
	walk(value)
	return text.String()
}

// vscodeUsage is one model's usage in a request.
type vscodeUsage struct {
	model, scope string
	counts       map[string]any
}

// vscodeTokenUsage is a request's usage, over all its model rounds when
// Copilot reports each model's totals (modelTotals): one record per model,
// the served model's first. Otherwise the output is the rounds'
// completionTokens and the input only the last round's promptTokens, named
// by scope "last_round": a lower bound, never a sum of these alternatives.
func vscodeTokenUsage(request map[string]any, served string) []vscodeUsage {
	meta := mapValueDefault(mapValueDefault(request["result"])["metadata"])
	// The request's response.usage.modelTotals: a list of each model's
	// {model, inputTokens, cachedTokens, outputTokens}.
	totals, _ := request["modelTotals"].([]any)
	usages := []vscodeUsage{}
	for _, raw := range totals {
		total := mapValueDefault(raw)
		if name, counts := firstString(total["model"]), vscodeModelTotals(total); name != "" && len(counts) > 0 {
			usages = append(usages, vscodeUsage{model: name, counts: counts})
		}
	}
	sort.SliceStable(usages, func(i, j int) bool { return usages[i].model == served && usages[j].model != served })
	if len(usages) > 0 {
		return usages
	}
	counts := map[string]any{}
	if output, ok := number(firstNonNil(request["completionTokens"], meta["outputTokens"])); ok {
		counts["output_tokens"] = output
	}
	scope := ""
	if input, ok := number(firstNonNil(meta["promptTokens"], request["promptTokens"])); ok {
		counts["input_tokens"], scope = input, "last_round"
	}
	if len(counts) == 0 {
		return nil
	}
	return []vscodeUsage{{model: served, scope: scope, counts: counts}}
}

// vscodeModelTotals maps one model's totals: its input includes the cached
// tokens it read.
func vscodeModelTotals(totals map[string]any) map[string]any {
	usage := map[string]any{}
	for key, source := range map[string]string{"input_tokens": "inputTokens", "output_tokens": "outputTokens", "cached_input_tokens": "cachedTokens"} {
		if value, ok := number(totals[source]); ok {
			usage[key] = value
		}
	}
	return usage
}
