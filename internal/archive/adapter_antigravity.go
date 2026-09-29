package archive

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Google Antigravity (the app, the agy CLI, and the IDE) keeps each of its
// apps' data in its own directory, ~/.gemini/antigravity, antigravity-cli and
// antigravity-ide. A source is one of those directories. Every conversation
// has a brain/<id> folder whose .system_generated/logs/transcript_full.jsonl
// holds one JSON object per step, never truncated (transcript.jsonl beside it
// may truncate long fields). conversation_summaries.db indexes them with
// titles, workspaces, and subagents' parents; annotations/<id>.pbtxt repeats
// the title. conversations/<id>.db holds the steps again as protobuf, and
// each model request's model and token usage, which are read from it (see
// adapter_antigravity_usage.go). The CLI writes a log per run to
// log/cli-<time>.log, which names each conversation run with -p (print mode).

type antigravityAdapter struct {
	baseAdapter
	installedVersions map[string]installedVersionObservation
	installedVersion  func(string) string
}

// antigravityProducts names the apps by their data directories.
var antigravityProducts = map[string]string{"antigravity": "Antigravity", "antigravity-cli": "Antigravity CLI", "antigravity-ide": "Antigravity IDE"}

// antigravityExtractor versions the parser; bump it when parsing changes so
// every transcript is parsed again.
const antigravityExtractor = "antigravity-v1"

const (
	antigravitySummaries = "conversation_summaries.db"
	antigravityLogs      = ".system_generated/logs"
	antigravityRunLogs   = "log"
)

func (a *antigravityAdapter) partExtractor() string { return antigravityExtractor }

// antigravityDataDir reports whether dir looks like an Antigravity app's data
// directory: it has a brain folder of conversations.
func antigravityDataDir(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "brain"))
	return err == nil && info.IsDir()
}

// transcripts maps each conversation ID to the transcript to read: the full
// one, or the possibly truncated one when that is all there is.
func (a *antigravityAdapter) transcripts() (map[string]string, error) {
	brain := filepath.Join(a.config.Path, "brain")
	entries, err := os.ReadDir(brain)
	if err != nil {
		return nil, err
	}
	found := map[string]string{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		logs := filepath.Join(brain, entry.Name(), filepath.FromSlash(antigravityLogs))
		for _, name := range []string{"transcript_full.jsonl", "transcript.jsonl"} {
			if info, err := os.Stat(filepath.Join(logs, name)); err == nil && info.Mode().IsRegular() {
				found[entry.Name()] = filepath.Join(logs, name)
				break
			}
		}
	}
	return found, nil
}

// captureFiles lists the files the adapter reads besides the summaries
// database: transcripts and title annotations.
func (a *antigravityAdapter) captureFiles() ([]string, error) {
	transcripts, err := a.transcripts()
	if err != nil {
		return nil, err
	}
	files := []string{}
	for _, path := range transcripts {
		files = append(files, path)
	}
	annotations, _ := filepath.Glob(filepath.Join(a.config.Path, "annotations", "*.pbtxt"))
	files = append(files, annotations...)
	files = append(files, a.runLogs()...)
	sort.Strings(files)
	return files, nil
}

// runLogs lists the CLI's per-run logs. The CLI keeps only its latest runs'
// (about 1,300), so a capture keeps the evidence of print mode for the rest.
func (a *antigravityAdapter) runLogs() []string {
	logs, _ := filepath.Glob(filepath.Join(a.config.Path, antigravityRunLogs, "cli-*.log"))
	return logs
}

// antigravityPrintMode matches the line a print-mode run logs once it has a
// conversation. Print mode also adds a "NON-INTERACTIVE mode" section to the
// system prompt, which no transcript keeps.
var antigravityPrintMode = regexp.MustCompile(`Print mode: conversation=([0-9A-Za-z-]+)`)

// antigravityLogCache holds the print-mode conversations of each run log read,
// by path, size and modification time; a run's log never changes once it ends.
var antigravityLogCache sync.Map

type antigravityLogEntry struct {
	size, modified int64
	ids            []string
}

// printModeConversations returns the conversations run with -p, from the run
// logs the CLI (or a capture of it) still has.
func (a *antigravityAdapter) printModeConversations() map[string]bool {
	printed := map[string]bool{}
	for _, path := range a.runLogs() {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		entry, cached := antigravityLogCache.Load(path)
		if !cached || entry.(antigravityLogEntry).size != info.Size() || entry.(antigravityLogEntry).modified != info.ModTime().UnixNano() {
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			found := antigravityLogEntry{size: info.Size(), modified: info.ModTime().UnixNano()}
			for _, match := range antigravityPrintMode.FindAllSubmatch(data, -1) {
				found.ids = append(found.ids, string(match[1]))
			}
			antigravityLogCache.Store(path, found)
			entry = found
		}
		for _, id := range entry.(antigravityLogEntry).ids {
			printed[id] = true
		}
	}
	return printed
}

// databases lists the SQLite databases the adapter reads: the summaries, and
// each conversation's requests.
func (a *antigravityAdapter) databases() ([]string, error) {
	transcripts, err := a.transcripts()
	if err != nil {
		return nil, err
	}
	candidates := []string{filepath.Join(a.config.Path, antigravitySummaries)}
	for id := range transcripts {
		candidates = append(candidates, a.requestsDatabase(id))
	}
	databases := []string{}
	for _, path := range candidates {
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			databases = append(databases, path)
		}
	}
	sort.Strings(databases[min(1, len(databases)):])
	return databases, nil
}

func (a *antigravityAdapter) Fingerprint() (string, error) {
	files, err := a.captureFiles()
	if err != nil {
		return "", err
	}
	rows := []string{}
	databases, err := a.databases()
	if err != nil {
		return "", err
	}
	for _, database := range databases {
		files = append(files, database, database+"-wal")
	}
	for _, file := range files {
		if info, err := os.Stat(file); err == nil {
			relative, _ := filepath.Rel(a.config.Path, file)
			rows = append(rows, fmt.Sprintf("%s:%d:%d", filepath.ToSlash(relative), info.Size(), info.ModTime().UnixNano()))
		}
	}
	sort.Strings(rows)
	return antigravityExtractor + ":" + hashBytes([]byte(strings.Join(rows, "\n"))), nil
}

func (a *antigravityAdapter) Discover(emit func(WorkspaceRecord) error) error {
	return a.discoverParts(nil, func(record *WorkspaceRecord, _ []sourcePart) error {
		if record == nil {
			return nil
		}
		return emit(*record)
	})
}

// antigravitySummary is a conversation's row in conversation_summaries.db,
// and whether a run log shows it was run with -p.
type antigravitySummary struct {
	Title, Parent, Agent, Project, App, Status string
	Workspaces                                 []string
	Depth                                      int
	Print                                      bool `json:",omitempty"`
}

// signal digests what a record takes from the summary, so a retitled
// conversation is parsed again though its transcript is unchanged.
func (s antigravitySummary) signal() string {
	return hashBytes([]byte(jsonText(s)))[:16]
}

var antigravityTitle = regexp.MustCompile(`(?m)^\s*title:\s*"((?:[^"\\]|\\.)*)"`)

// summaries reads the conversation index, and the titles of conversations it
// does not list from their annotations. Either may be missing.
func (a *antigravityAdapter) summaries(ids map[string]string) map[string]antigravitySummary {
	summaries := map[string]antigravitySummary{}
	path := filepath.Join(a.config.Path, antigravitySummaries)
	if _, err := os.Stat(path); err == nil {
		if db, err := openReadOnlySQLite(path); err == nil {
			rows, err := queryMaps(db, "SELECT * FROM conversation_summaries")
			db.Close()
			if err == nil {
				for _, row := range rows {
					id := firstString(row["conversation_id"])
					if id == "" {
						continue
					}
					summary := antigravitySummary{Title: firstString(row["title"]), Parent: firstString(row["parent_conversation_id"]), Agent: firstString(row["agent_name"]),
						Project: firstString(row["project_id"]), App: firstString(row["app_data_dir"]), Status: firstString(row["status"]), Depth: int(integer(row["nesting_depth"]))}
					var workspaces []string
					if json.Unmarshal([]byte(firstString(row["workspace_uris"])), &workspaces) == nil {
						for _, uri := range workspaces {
							summary.Workspaces = append(summary.Workspaces, fileURIPath(uri))
						}
					}
					summaries[id] = summary
				}
			}
		}
	}
	for id := range ids {
		if summaries[id].Title != "" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(a.config.Path, "annotations", id+".pbtxt"))
		if err != nil {
			continue
		}
		if match := antigravityTitle.FindSubmatch(data); match != nil {
			var title string
			if json.Unmarshal([]byte(`"`+string(match[1])+`"`), &title) != nil {
				title = string(match[1])
			}
			summary := summaries[id]
			summary.Title = title
			summaries[id] = summary
		}
	}
	return summaries
}

func fileURIPath(uri string) string {
	if parsed, err := url.Parse(uri); err == nil && parsed.Scheme == "file" {
		return parsed.Path
	}
	return uri
}

// discoverParts emits a record per top-level conversation, with the
// transcripts of the subagents it started, as the Claude adapter does.
func (a *antigravityAdapter) discoverParts(unchanged func([]sourcePart) bool, emit func(*WorkspaceRecord, []sourcePart) error) error {
	transcripts, err := a.transcripts()
	if err != nil {
		return err
	}
	summaries := a.summaries(transcripts)
	for id := range a.printModeConversations() {
		if _, ok := transcripts[id]; ok {
			summary := summaries[id]
			summary.Print = true
			summaries[id] = summary
		}
	}
	root := func(id string) string {
		seen := map[string]bool{}
		for summaries[id].Parent != "" && !seen[id] {
			seen[id] = true
			if _, ok := transcripts[summaries[id].Parent]; !ok {
				break
			}
			id = summaries[id].Parent
		}
		return id
	}
	groups := map[string][]string{}
	for id := range transcripts {
		groups[root(id)] = append(groups[root(id)], id)
	}
	roots := make([]string, 0, len(groups))
	for id := range groups {
		roots = append(roots, id)
	}
	sort.Strings(roots)
	for _, rootID := range roots {
		ids := groups[rootID]
		sort.Slice(ids, func(i, j int) bool {
			if ids[i] == rootID || ids[j] == rootID {
				return ids[i] == rootID
			}
			return ids[i] < ids[j]
		})
		parts, complete := []sourcePart{}, true
		for _, id := range ids {
			if info, err := os.Stat(transcripts[id]); err == nil {
				parts = append(parts, sourcePart{item: a.original(transcripts[id]), size: info.Size(), version: info.ModTime().UnixNano(), signal: summaries[id].signal()})
			} else {
				complete = false
			}
			if part, ok := a.requestsPart(id); ok {
				parts = append(parts, part)
			}
		}
		if unchanged != nil && complete && unchanged(parts) {
			continue
		}
		var combined *WorkspaceRecord
		read := []sourcePart{}
		for _, id := range ids {
			record, part, err := a.parseFile(id, transcripts[id], summaries[id])
			if err != nil {
				return fmt.Errorf("parse %s: %w", transcripts[id], err)
			}
			if part.item == "" {
				continue
			}
			read = append(read, part)
			if part, ok := a.requestsPart(id); ok {
				read = append(read, part)
			}
			if len(record.Conversations) == 0 || len(record.Conversations[0].Messages) == 0 {
				continue
			}
			if combined == nil {
				record.SourceID = rootID
				combined = &record
				continue
			}
			combined.Conversations = append(combined.Conversations, record.Conversations...)
			combined.Observed = max(combined.Observed, record.Observed)
			if record.ActivityAt > combined.ActivityAt {
				combined.ActivityAt = record.ActivityAt
			}
		}
		if combined != nil {
			combined.Metrics = reconciledTokenMetrics(*combined)
		}
		if err := emit(combined, read); err != nil {
			return fmt.Errorf("ingest %s: %w", rootID, err)
		}
	}
	return nil
}

// readJSONLines decodes each line of the first size bytes of r that holds a
// JSON object, numbering it in "_line"; other lines are skipped.
func readJSONLines(r io.Reader, size int64) ([]map[string]any, error) {
	events := []map[string]any{}
	reader := bufio.NewReaderSize(io.LimitReader(r, size), 64*1024)
	for line := 1; ; line++ {
		encoded, err := reader.ReadBytes('\n')
		if len(encoded) == 0 && err == io.EOF {
			return events, nil
		}
		if err != nil && err != io.EOF {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		decoder := json.NewDecoder(bytes.NewReader(encoded))
		decoder.UseNumber()
		var event map[string]any
		if decoder.Decode(&event) == nil && event != nil {
			event["_line"] = int64(line)
			events = append(events, event)
		}
		if err == io.EOF {
			return events, nil
		}
	}
}

// requestsPart is the version of a conversation's requests database, read
// before it is, so a later write is read again next time.
func (a *antigravityAdapter) requestsPart(id string) (sourcePart, bool) {
	path := a.requestsDatabase(id)
	info, err := os.Stat(path)
	if err != nil {
		return sourcePart{}, false
	}
	return sourcePart{item: "sqlite:" + a.original(path), size: info.Size(), version: a.databaseVersion(path)}, true
}

func (a *antigravityAdapter) parseFile(id, path string, summary antigravitySummary) (WorkspaceRecord, sourcePart, error) {
	file, err := os.Open(path)
	if err != nil {
		return WorkspaceRecord{}, sourcePart{}, nil
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return WorkspaceRecord{}, sourcePart{}, nil
	}
	part := sourcePart{item: a.original(path), size: info.Size(), version: info.ModTime().UnixNano(), signal: summary.signal()}
	if parseHook != nil {
		parseHook(path)
	}
	events, err := readJSONLines(file, part.size)
	if err != nil {
		return WorkspaceRecord{}, part, err
	}
	requests, err := a.requests(id)
	if err != nil {
		return WorkspaceRecord{}, part, err
	}
	record := a.record(id, path, summary, events, requests)
	observe(&record, part.version)
	return record, part, nil
}

var (
	antigravityTag   = regexp.MustCompile(`(?s)<([A-Z][A-Z0-9_]*)>\n?(.*?)\n?</([A-Z][A-Z0-9_]*)>`)
	antigravityModel = regexp.MustCompile("setting `Model Selection` from .*? to (.+?)\\. ")
)

// antigravityUserInput splits a USER_INPUT step into what the person wrote,
// inside <USER_REQUEST>, and the blocks the app added around it (the local
// time, setting changes, and the like), keyed by their lowercased tags.
func antigravityUserInput(content string) (string, [][2]string) {
	matches := antigravityTag.FindAllStringSubmatchIndex(content, -1)
	request, found, context := "", false, [][2]string{}
	rest := strings.Builder{}
	previous := 0
	for _, match := range matches {
		open, body, closing := content[match[2]:match[3]], content[match[4]:match[5]], content[match[6]:match[7]]
		if open != closing {
			continue
		}
		rest.WriteString(content[previous:match[0]])
		previous = match[1]
		if open == "USER_REQUEST" {
			if found {
				request += "\n\n"
			}
			request, found = request+body, true
			continue
		}
		context = append(context, [2]string{strings.ToLower(open), body})
	}
	rest.WriteString(content[previous:])
	if !found {
		return content, nil
	}
	if extra := strings.TrimSpace(rest.String()); extra != "" {
		context = append(context, [2]string{"harness", extra})
	}
	return request, context
}

func (a *antigravityAdapter) record(id, path string, summary antigravitySummary, events []map[string]any, requests map[int64]antigravityRequest) WorkspaceRecord {
	origin := a.original(path)
	app := summary.App
	if app == "" {
		app = filepath.Base(a.original(a.config.Path))
	}
	subagent := summary.Parent != ""
	messages := []MessageRecord{}
	// selected is the model setting as the person named it ("Gemini 3.8
	// Flash (High)"); served is the API model that answered a request
	// ("gemini-3.8-flash"), which is what prices are listed under.
	selected, served, started, ended := "", "", "", ""
	requestsDatabase := a.original(a.requestsDatabase(id))
	type pendingCall struct{ id, tool, kind string }
	pending := []pendingCall{}
	for _, event := range events {
		line := integer(event["_line"])
		stepIndex := line - 1
		if index, ok := number(event["step_index"]); ok {
			stepIndex = int64(index)
		}
		step := fmt.Sprintf("step-%d", stepIndex)
		timestamp := iso(event["created_at"])
		if started == "" {
			started = timestamp
		}
		if timestamp != "" {
			ended = timestamp
		}
		request, requested := requests[stepIndex]
		if requested && request.model != "" {
			served = request.model
		}
		base := MessageRecord{NativeID: step, Model: defaultString(served, selected), CreatedAt: timestamp, EvidenceLocator: fmt.Sprintf("%s:%d", origin, line), Selected: true}
		content, stepType, failed := firstString(event["content"]), firstString(event["type"]), firstString(event["status"]) == "ERROR"
		switch {
		case stepType == "USER_INPUT":
			request, context := antigravityUserInput(content)
			for _, block := range context {
				if match := antigravityModel.FindStringSubmatch(block[1]); block[0] == "user_settings_change" && match != nil {
					selected = strings.TrimSpace(match[1])
					base.Model = defaultString(served, selected)
				}
			}
			// Calls still waiting for output when the person spoke again
			// were cancelled; nothing that follows is theirs.
			pending = nil
			item := base
			item.Role, item.Kind, item.Text = "user", "message", strings.TrimSpace(request)
			if subagent {
				item.Role = agentRole
			} else if summary.Print {
				item.Sender = "automation:agy-print"
			}
			media := antigravityMedia(event["media"])
			if item.Text == "" && len(media) > 0 {
				item.Text = "(image attachment)"
			}
			if item.Text != "" {
				messages = append(messages, item)
			}
			if len(media) > 0 {
				attachment := attachmentRecord(base, item.Role, media)
				attachment.NativeID = step + ":attachments"
				messages = append(messages, attachment)
			}
			// What the app added goes with the person's words, after them, so
			// it stays in their turn.
			for index, block := range context {
				item := injectedContext(base, "antigravity_"+block[0], block[1])
				item.NativeID = fmt.Sprintf("%s:context:%d", step, index)
				messages = append(messages, item)
			}
		case stepType == "PLANNER_RESPONSE":
			if text := strings.TrimSpace(content); text != "" {
				item := base
				item.Role, item.Kind, item.Text = "assistant", "message", text
				messages = append(messages, item)
			}
			calls, _ := event["tool_calls"].([]any)
			for index, raw := range calls {
				call := mapValue(raw)
				if call == nil {
					continue
				}
				name := defaultString(call["name"], "tool")
				item := base
				item.NativeID = fmt.Sprintf("%s:call:%d", step, index)
				item.CallID = item.NativeID
				item.Role, item.Kind = "assistant", "tool_call"
				if name == "invoke_subagent" {
					item.Kind = "delegation"
				}
				item.Text = jsonText(map[string]any{"tool": name, "input": mapValueDefault(call["args"])})
				messages = append(messages, item)
				pending = append(pending, pendingCall{item.CallID, name, item.Kind})
			}
			if failed && firstString(event["error"]) != "" {
				item := base
				item.NativeID = step + ":error"
				item.Role, item.Kind, item.Text = "system", "error", firstString(event["error"])
				messages = append(messages, item)
			}
			if requested {
				item := base
				item.NativeID, item.Role, item.Kind = step+":usage", "system", "metadata"
				item.EvidenceLocator = fmt.Sprintf("%s:gen_metadata:%d", requestsDatabase, request.index)
				item.Text = jsonText(map[string]any{"type": "token_usage", "usage": request.tokenUsage(), "usage_message_id": defaultString(request.usage["response_id"], fmt.Sprintf("%s:request:%d", id, request.index)),
					"model_usage": request.usage})
				messages = append(messages, item)
			}
		case len(pending) > 0 && firstString(event["source"]) != "USER_EXPLICIT":
			// Tool calls carry no IDs; the app runs a response's calls in
			// order and records each one's output as the next step.
			call := pending[0]
			pending = pending[1:]
			item := base
			item.CallID, item.Role, item.Kind = call.id, "tool", "tool_result"
			if call.kind == "delegation" {
				item.Kind = "delegation_result"
			}
			payload := map[string]any{"tool": call.tool, "content": content, "is_error": failed}
			if message := firstString(event["error"]); message != "" {
				payload["details"] = map[string]any{"error": message}
			}
			item.Text = jsonText(payload)
			messages = append(messages, item)
		default:
			// Other steps (system notices, checkpoints, and kinds this parser
			// does not know yet) are kept as metadata.
			kept := make(map[string]any, len(event))
			for key, value := range event {
				if key != "_line" {
					kept[key] = value
				}
			}
			item := base
			item.Role, item.Kind, item.Text = "system", "metadata", jsonText(kept)
			if failed {
				item.Kind = "error"
			}
			messages = append(messages, item)
		}
	}
	location := ""
	if len(summary.Workspaces) > 0 {
		location = summary.Workspaces[0]
	}
	purpose, outcome := agentPurposeOutcome(messages)
	product := defaultString(antigravityProducts[app], "Antigravity")
	depth := summary.Depth
	if subagent && depth < 1 {
		depth = 1
	}
	metadata := map[string]any{"app": app}
	if selected != "" {
		metadata["model_selection"] = selected
	}
	for key, value := range map[string]string{"project_id": summary.Project, "status": summary.Status} {
		if value != "" {
			metadata[key] = value
		}
	}
	if len(summary.Workspaces) > 1 {
		metadata["workspaces"] = summary.Workspaces
	}
	installed := a.installedHarnessVersion(app)
	versionFirst, versionLast := "", ""
	versionFirst, versionLast = installedVersionForActivity(installed, started, ended)
	return WorkspaceRecord{SourceID: id, SourceKind: "antigravity", Title: defaultString(summary.Title, product+" "+short(id)), Account: a.config.Account, Purpose: purpose, Outcome: outcome,
		ActivityAt: ended, Location: location, Repository: a.repository(location, ""), Metadata: metadata,
		Conversations: []ConversationRecord{{NativeID: id, Provider: "antigravity", Account: a.config.Account, Model: defaultString(served, selected), ParentNativeID: summary.Parent, AgentDepth: depth,
			AgentNickname: summary.Agent, Origin: origin, Harness: antigravityHarness(app), HarnessVersionFirst: versionFirst, HarnessVersionLast: versionLast, HarnessVersionSource: "installed-app", Coverage: "complete", Messages: messages, StartedAt: started, EndedAt: ended}}}
}

// antigravityMedia lists the media types of a step's attachments; the bytes
// are files the transcript only points to.
func antigravityMedia(value any) []string {
	items, _ := value.([]any)
	types := []string{}
	for _, raw := range items {
		if media := mapValue(raw); media != nil {
			types = append(types, defaultString(firstNonNil(media["mime_type"], media["mimeType"]), "application/octet-stream"))
		}
	}
	return types
}
