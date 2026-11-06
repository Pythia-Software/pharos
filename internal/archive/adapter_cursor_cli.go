package archive

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// cursorCLIAdapter reads the Cursor CLI's (cursor-agent) chats from its
// folder, ~/.cursor. Each chat is a database, chats/<md5 of its folder>/
// <chat>/store.db, holding content-addressed blobs (blobs, keyed by the
// sha256 of their data) and the chat's metadata (meta, hex-encoded JSON). A
// message is a JSON blob; a root is a protobuf blob listing the chat's
// messages in order, and meta names the latest. Each turn writes a new root,
// so a message only an older root holds was rewound or compacted away. The
// metadata also holds the chat's encryption key, which is neither read nor
// captured (cursorCLICaptureTables). The agent-transcripts the CLI writes
// beside its projects are text copies without tool results, so are not read.
type cursorCLIAdapter struct {
	baseAdapter
}

// cursorCLIExtractor versions the parser; bump it when parsing changes.
const cursorCLIExtractor = "cursor-cli-v1"

func (a *cursorCLIAdapter) partExtractor() string { return cursorCLIExtractor }

// cursorCLICaptureTables are what a capture keeps of a chat: its blobs, and
// of its metadata only what is read (cursorCLIMetaKeys), so never the
// encryption key, however it is written. Metadata that is not hex-encoded
// JSON is left out; so are tables a later release adds.
var cursorCLICaptureTables = map[string]captureTable{
	"blobs": {},
	"meta": {
		columns: `key, lower(hex(json_object(` + cursorCLIMetaColumns() + `)))`,
		where:   `json_valid(CAST(unhex(value) AS TEXT))`,
	},
}

// cursorCLIMetaKeys are the keys of a chat's metadata that are read.
// subagentInfo is a Task subagent's lineage: parentAgentId,
// rootParentAgentId, toolCallId and typeName.
var cursorCLIMetaKeys = []string{"agentId", "latestRootBlobId", "name", "createdAt", "mode", "lastUsedModel", "subagentInfo"}

func cursorCLIMetaColumns() string {
	columns := []string{}
	for _, key := range cursorCLIMetaKeys {
		columns = append(columns, fmt.Sprintf(`'%s', json_extract(CAST(unhex(value) AS TEXT), '$.%s')`, key, key))
	}
	return strings.Join(columns, ", ")
}

// cursorCLIChat is a chat's storage: its database and the meta.json beside
// it ("" when absent).
type cursorCLIChat struct{ store, meta string }

// chats lists the chats in the folder's chats directory. A folder of chats
// that cannot be listed is reported with the chats of the others.
func (a *cursorCLIAdapter) chats() ([]cursorCLIChat, error) {
	root := filepath.Join(a.config.Path, "chats")
	folders, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		if _, err := os.Stat(a.config.Path); err != nil {
			return nil, err
		}
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	chats, failures := []cursorCLIChat{}, []error{}
	for _, folder := range folders {
		if !folder.IsDir() {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(root, folder.Name()))
		if err != nil {
			failures = append(failures, err)
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			dir := filepath.Join(root, folder.Name(), entry.Name())
			chat := cursorCLIChat{store: filepath.Join(dir, "store.db")}
			if info, err := a.files().Stat(chat.store); err != nil && !errors.Is(err, os.ErrNotExist) {
				failures = append(failures, err)
				continue
			} else if err != nil || !info.Mode().IsRegular() {
				continue
			}
			if info, err := a.files().Stat(filepath.Join(dir, "meta.json")); err == nil && info.Mode().IsRegular() {
				chat.meta = filepath.Join(dir, "meta.json")
			}
			chats = append(chats, chat)
		}
	}
	sort.Slice(chats, func(i, j int) bool { return chats[i].store < chats[j].store })
	return chats, errors.Join(failures...)
}

// captureInputs are what a capture copies: each chat's database (see
// cursorCLICaptureTables) and its meta.json; and what could not be listed.
func (a *cursorCLIAdapter) captureInputs() (databases, files []string, err error) {
	chats, err := a.chats()
	for _, chat := range chats {
		databases = append(databases, chat.store)
		if chat.meta != "" {
			files = append(files, chat.meta)
		}
	}
	return databases, files, err
}

func (a *cursorCLIAdapter) Fingerprint() (string, error) {
	databases, files, err := a.captureInputs()
	if err != nil && databases == nil {
		return "", err
	}
	rows := []string{}
	for _, database := range databases {
		rows = append(rows, fmt.Sprintf("%s:%d", a.original(database), a.databaseVersion(database)))
	}
	for _, file := range files {
		if info, err := a.files().Stat(file); err == nil {
			rows = append(rows, fmt.Sprintf("%s:%d:%d", a.original(file), info.Size(), info.ModTime().UnixNano()))
		}
	}
	return cursorCLIExtractor + ":" + hashBytes([]byte(strings.Join(rows, "\n"))), nil
}

func (a *cursorCLIAdapter) Discover(emit func(WorkspaceRecord) error) error {
	return a.discoverParts(nil, func(record *WorkspaceRecord, _ []sourcePart) error {
		if record == nil {
			return nil
		}
		return emit(*record)
	})
}

func (a *cursorCLIAdapter) discoverParts(unchanged func([]sourcePart) bool, emit func(*WorkspaceRecord, []sourcePart) error) error {
	plan, err := a.planParts()
	if err != nil {
		return err
	}
	return plan.discover(a.context(), unchanged, emit)
}

// open opens a chat's database for reading in one transaction.
func (a *cursorCLIAdapter) open(store string) (*sql.DB, *sql.Tx, error) {
	readable, err := a.files().Database(store)
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

// cursorCLIMeta reads a chat's metadata: what is read of the first row of
// meta that is hex-encoded JSON. It is read as a capture copies it
// (cursorCLICaptureTables), so a key written twice is read the same live and
// captured.
func cursorCLIMeta(tx *sql.Tx) (map[string]any, error) {
	var value string
	if err := tx.QueryRow(`SELECT json_object(` + cursorCLIMetaColumns() + `) FROM meta WHERE ` + cursorCLICaptureTables["meta"].where + ` ORDER BY key LIMIT 1`).Scan(&value); errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("the chat has no metadata")
	} else if err != nil {
		return nil, err
	}
	meta := map[string]any{}
	if err := json.Unmarshal([]byte(value), &meta); err != nil {
		return nil, fmt.Errorf("chat metadata: %w", err)
	}
	return meta, nil
}

// planParts plans one part per chat. A new root is written with each change
// to the chat, so the latest root's ID is the change signal.
func (a *cursorCLIAdapter) planParts() (*sourceWorkPlan, error) {
	chats, err := a.chats()
	if err != nil && chats == nil {
		return nil, err
	}
	plan := &sourceWorkPlan{continueOnError: true}
	failures := []error{err}
	for _, chat := range chats {
		signal, err := a.signal(chat.store)
		if err != nil {
			failures = append(failures, fmt.Errorf("Cursor CLI chat %s: %w", a.original(chat.store), err))
			continue
		}
		size := int64(0)
		if info, err := a.files().Stat(chat.store); err == nil {
			size = info.Size()
		}
		if chat.meta != "" {
			if info, err := a.files().Stat(chat.meta); err == nil {
				signal += fmt.Sprintf(":%d:%d", info.Size(), info.ModTime().UnixNano())
			}
		}
		parts := []sourcePart{{item: a.original(chat.store), size: size, version: a.version(chat), signal: signal}}
		plan.groups = append(plan.groups, plannedPartGroup{parts: parts, read: func(emit partEmitter) error {
			record, err := a.read(chat)
			if err != nil {
				return fmt.Errorf("Cursor CLI chat %s: %w", a.original(chat.store), err)
			}
			return emit(record, parts)
		}})
	}
	plan.discoveryError = errors.Join(failures...)
	return plan, nil
}

// version is when a chat last changed: its database or its meta.json.
func (a *cursorCLIAdapter) version(chat cursorCLIChat) int64 {
	version := a.databaseVersion(chat.store)
	if chat.meta != "" {
		if info, err := a.files().Stat(chat.meta); err == nil {
			version = max(version, info.ModTime().UnixNano())
		}
	}
	return version
}

// signal is a chat's latest root.
func (a *cursorCLIAdapter) signal(store string) (string, error) {
	db, tx, err := a.open(store)
	if err != nil {
		return "", err
	}
	defer db.Close()
	defer tx.Rollback()
	meta, err := cursorCLIMeta(tx)
	if err != nil {
		return "", err
	}
	return firstString(meta["latestRootBlobId"]), nil
}

// readGroup reads, as indexing does, the chat whose part parts begin with;
// nothing when that chat is gone.
func (a *cursorCLIAdapter) readGroup(parts []sourcePart) (*WorkspaceRecord, []sourcePart, error) {
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

// cursorCLIRoot is what a root holds: its messages' blob IDs in order, the
// context meter, and the folder the chat was run in.
type cursorCLIRoot struct {
	messages            []string
	turns               []string
	contextUsed, window int64
	folder              string
}

// cursorCLIParseRoot reads a root (protoFields): field 1 lists its
// messages' blob IDs, 5 the context meter (tokens used, then the window),
// 8 its turns' blob IDs (cursorCLITurnTimes) and 9 the folder's URI. A blob that is not a root, as it lists no message
// or anything but message IDs, is reported as nil.
func cursorCLIParseRoot(data []byte) *cursorCLIRoot {
	fields := protoFields(data)
	root := &cursorCLIRoot{}
	for _, value := range fields[1] {
		id, ok := value.([]byte)
		if !ok || len(id) != 32 {
			return nil
		}
		root.messages = append(root.messages, hex.EncodeToString(id))
	}
	if len(root.messages) == 0 {
		return nil
	}
	if meter, ok := protoLast(fields[5]).([]byte); ok {
		values := protoFields(meter)
		used, _ := protoLast(values[1]).(uint64)
		window, _ := protoLast(values[2]).(uint64)
		root.contextUsed, root.window = int64(used), int64(window)
	}
	if folder, ok := protoLast(fields[9]).([]byte); ok {
		root.folder = string(folder)
	}
	for _, value := range fields[8] {
		if id, ok := value.([]byte); ok && len(id) == 32 {
			root.turns = append(root.turns, hex.EncodeToString(id))
		}
	}
	return root
}

// cursorCLITimes are when a chat's turns record their parts happened: each
// prompt and reply by its text, in order for a text sent again, and each
// call's start and end by its ID.
type cursorCLITimes struct {
	prompts, replies map[string][]string
	calls            map[string][2]string
}

// next is the first time not yet taken for text in queue.
func (t cursorCLITimes) next(queue map[string][]string, text string) string {
	key := strings.TrimSpace(text)
	list := queue[key]
	if len(list) == 0 {
		return ""
	}
	queue[key] = list[1:]
	return list[0]
}

// cursorCLITurnTimes reads the turns roots list. A turn blob's field 1
// holds the prompt's blob ID (1) and its steps' (2). The prompt's blob holds
// its text (1) and when it was sent (25, in ms); a step is a reply {1 text,
// 2 started}, a tool call {57 call ID, 59 started, 60 ended}, or thinking.
func cursorCLITurnTimes(blobs map[string][]byte, roots []*cursorCLIRoot) cursorCLITimes {
	times := cursorCLITimes{prompts: map[string][]string{}, replies: map[string][]string{}, calls: map[string][2]string{}}
	at := func(values []any) string {
		if ms, ok := protoLast(values).(uint64); ok && ms > 0 {
			return cursorCLITime(int64(ms))
		}
		return ""
	}
	blob := func(value any) map[int][]any {
		id, _ := value.([]byte)
		return protoFields(blobs[hex.EncodeToString(id)])
	}
	seen := map[string]bool{}
	for _, root := range roots {
		for _, id := range root.turns {
			if seen[id] {
				continue
			}
			seen[id] = true
			turn := protoFields(protoBytes(protoFields(blobs[id])[1]))
			prompt := blob(protoLast(turn[1]))
			if text, sent := strings.TrimSpace(string(protoBytes(prompt[1]))), at(prompt[25]); text != "" && sent != "" {
				times.prompts[text] = append(times.prompts[text], sent)
			}
			for _, value := range turn[2] {
				step := blob(value)
				if reply := protoFields(protoBytes(step[1])); len(reply) > 0 {
					if text, started := strings.TrimSpace(string(protoBytes(reply[1]))), at(reply[2]); text != "" && started != "" {
						times.replies[text] = append(times.replies[text], started)
					}
				} else if call := protoFields(protoBytes(step[2])); len(call) > 0 {
					if id := string(protoBytes(call[57])); id != "" {
						times.calls[id] = [2]string{at(call[59]), at(call[60])}
					}
				}
			}
		}
	}
	return times
}

// cursorCLIBlobID is the ID of a blob holding data: its sha256.
func cursorCLIBlobID(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// read reads a chat in one transaction.
func (a *cursorCLIAdapter) read(chat cursorCLIChat) (*WorkspaceRecord, error) {
	db, tx, err := a.open(chat.store)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	defer tx.Rollback()
	// Live, the version is read once the transaction has begun reading, so
	// it is no older than what was read.
	meta, err := cursorCLIMeta(tx)
	if err != nil {
		return nil, err
	}
	version := a.version(chat)
	blobs, damaged := map[string][]byte{}, false
	rows, err := tx.QueryContext(a.context(), `SELECT id, data FROM blobs`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var data []byte
		if err := rows.Scan(&id, &data); err != nil {
			rows.Close()
			return nil, err
		}
		// A blob that is not what its ID names, as one cut short, is read
		// as gone; it may have been a root.
		if strings.EqualFold(id, cursorCLIBlobID(data)) {
			blobs[strings.ToLower(id)] = data
		} else {
			damaged = true
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	extra := map[string]any{}
	if chat.meta != "" {
		if value, _, err := a.files().ReadJSONVersion(chat.meta); err == nil {
			extra = mapValueDefault(value)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	return a.record(chat, meta, extra, blobs, damaged, version), nil
}

// cursorCLIMessage is a message as a chat's roots place it.
type cursorCLIMessage struct {
	id       string // its native ID: its blob's, with which occurrence of it
	blob     string
	selected bool
	at       string // when the last prompt before it in its root was sent
}

// cursorCLIUserText is the text of a user message.
func cursorCLIUserText(value map[string]any) string {
	if text, ok := value["content"].(string); ok {
		return text
	}
	texts := []string{}
	parts, _ := sliceValue(value["content"])
	for _, part := range parts {
		if text := firstString(mapValueDefault(part)["text"]); strings.TrimSpace(text) != "" {
			texts = append(texts, text)
		}
	}
	return strings.Join(texts, "\n\n")
}

// record builds a chat's record from its metadata, its meta.json and blobs;
// damaged is whether a blob could not be read.
func (a *cursorCLIAdapter) record(chat cursorCLIChat, meta, extra map[string]any, blobs map[string][]byte, damaged bool, version int64) *WorkspaceRecord {
	isMessage := func(id string) bool { data := blobs[id]; return len(data) > 0 && data[0] == '{' }
	latest := strings.ToLower(firstString(meta["latestRootBlobId"]))
	root := cursorCLIParseRoot(blobs[latest])
	older := []*cursorCLIRoot{}
	for id, data := range blobs {
		if id == latest || isMessage(id) {
			continue
		}
		if candidate := cursorCLIParseRoot(data); candidate != nil {
			older = append(older, candidate)
		}
	}
	sort.SliceStable(older, func(i, j int) bool {
		if len(older[i].messages) != len(older[j].messages) {
			return len(older[i].messages) < len(older[j].messages)
		}
		return strings.Join(older[i].messages, "") < strings.Join(older[j].messages, "")
	})
	// Without its latest root, or a root it may have had, a chat's messages
	// are not all known: what was indexed of it is kept, and the longest root
	// left stands for the latest.
	lost := latest != "" && root == nil || damaged
	if root == nil && latest != "" && len(older) > 0 {
		root, older = older[len(older)-1], older[:len(older)-1]
	} else if root == nil {
		root = &cursorCLIRoot{}
	}
	// A message no root lists was listed by a root since deleted, whose
	// place for it is lost: what was indexed of the chat is kept.
	listed := map[string]bool{}
	for _, candidate := range append([]*cursorCLIRoot{root}, older...) {
		for _, blob := range candidate.messages {
			listed[blob] = true
		}
	}
	for id := range blobs {
		if isMessage(id) && !listed[id] {
			lost = true
		}
	}
	times := cursorCLITurnTimes(blobs, append([]*cursorCLIRoot{root}, older...))
	// When each prompt was sent, from the time the CLI stamps on it.
	started := cursorCLITime(firstNonNil(meta["createdAt"], extra["createdAtMs"]))
	sent := map[string]string{}
	sentAt := func(blob string) string {
		if at, ok := sent[blob]; ok {
			return at
		}
		var value map[string]any
		if isMessage(blob) && json.Unmarshal(blobs[blob], &value) == nil && firstString(value["role"]) == "user" {
			_, sent[blob] = cursorCLIPrompt(cursorCLIUserText(value))
		}
		return sent[blob]
	}
	// The latest root's messages, in order; a message repeated in it is told
	// apart by its occurrence. Then those only older roots hold, from the
	// shortest root: rewound or compacted away, so no longer selected. Each
	// is timed by the prompts before it in its own root.
	messages, held := []cursorCLIMessage{}, map[string]int{}
	place := func(blob string, occurrence int, selected bool, at string) {
		held[blob] = occurrence
		id := blob[:min(16, len(blob))]
		if occurrence > 1 {
			id += "#" + strconv.Itoa(occurrence)
		}
		messages = append(messages, cursorCLIMessage{id: id, blob: blob, selected: selected, at: at})
	}
	latestAt := started
	for _, blob := range root.messages {
		latestAt = max(latestAt, sentAt(blob))
		place(blob, held[blob]+1, true, latestAt)
	}
	// An older root's message whose blob is gone or unreadable is unread, as
	// one the latest root lists is.
	for _, candidate := range older {
		occurrences, at := map[string]int{}, started
		for _, blob := range candidate.messages {
			at = max(at, sentAt(blob))
			if occurrences[blob]++; occurrences[blob] > held[blob] {
				place(blob, occurrences[blob], false, at)
			}
		}
	}

	agent := defaultString(meta["agentId"], filepath.Base(filepath.Dir(chat.store)))
	origin := a.original(chat.store)
	conversation := ConversationRecord{NativeID: agent, Provider: "cursor", Account: a.config.Account, Origin: origin, Coverage: "complete", Harness: "cursor-cli", StartedAt: started}
	// A Task subagent is a chat of its own, naming the chat that started it;
	// its prompts are that agent's. One started by a subagent is at least
	// two levels down.
	lineage := mapValueDefault(cursorJSON(meta["subagentInfo"]))
	parent := firstString(lineage["parentAgentId"])
	if parent != "" {
		conversation.ParentNativeID, conversation.AgentDepth, conversation.AgentPath = parent, 1, firstString(lineage["typeName"])
		if top := firstString(lineage["rootParentAgentId"]); top != "" && top != parent {
			conversation.AgentDepth = 2
		}
	}
	context := map[string]any{}
	if root.contextUsed > 0 {
		context["context_tokens_used"] = root.contextUsed
	}
	if root.window > 0 {
		context["context_token_limit"] = root.window
	}
	if len(context) > 0 {
		conversation.NativeContext = context
	}
	// model is the conversation's, from what is selected; current is the
	// one each reply ran on, unknown again where what was rewound begins.
	// floor is the latest time placed so far, so a part the turns record no
	// time for is never placed before one they do. at is the latest selected
	// message's, which the chat ends no earlier than.
	at, model, current, floor := started, "", "", ""
	add := func(item MessageRecord) {
		item.CreatedAt = max(item.CreatedAt, floor)
		floor = item.CreatedAt
		if item.Selected {
			at = max(at, item.CreatedAt)
		}
		conversation.Messages = append(conversation.Messages, item)
	}
	for n, message := range messages {
		if !message.selected && (n == 0 || messages[n-1].selected) {
			current, floor = "", ""
		}
		data := blobs[message.blob]
		var value map[string]any
		if len(data) == 0 || json.Unmarshal(data, &value) != nil {
			conversation.Coverage = "partial"
			conversation.Unread = append(conversation.Unread, message.id)
			continue
		}
		if message.selected {
			at = max(at, message.at)
		}
		base := MessageRecord{NativeID: message.id, CreatedAt: message.at, EvidenceLocator: origin + "#" + message.blob, Selected: message.selected,
			// The blob's ID stands in for its raw text, so text quoting usage
			// JSON is never read as token usage.
			RawText: jsonText(map[string]any{"type": "cursor_cli_message", "id": message.blob})}
		parts, _ := value["content"].([]any)
		if text, ok := value["content"].(string); ok {
			parts = []any{map[string]any{"type": "text", "text": text}}
		}
		switch firstString(value["role"]) {
		case "user":
			// The CLI's own context (the system, folder and shell) is sent
			// as a user message.
			text := cursorCLIUserText(value)
			if strings.HasPrefix(strings.TrimSpace(text), "<user_info>") || strings.TrimSpace(text) == "" {
				continue
			}
			// A compaction's summary is sent as a user message the CLI wrote.
			if mapValueDefault(mapValueDefault(value["providerOptions"])["cursor"])["isSummary"] == true {
				item := base
				item.Role, item.Kind = "system", "metadata"
				item.Text = jsonText(map[string]any{"type": "compaction", "summary": text})
				add(item)
				continue
			}
			prompt, sent := cursorCLIPrompt(text)
			if recorded := times.next(times.prompts, prompt); recorded != "" {
				base.CreatedAt = recorded
			} else if sent != "" {
				base.CreatedAt = sent
			}
			item := base
			item.Role, item.Kind, item.Text = "user", "message", prompt
			if parent != "" {
				item.Role, item.Sender = agentRole, "agent:"+parent
			}
			add(item)
		case "assistant":
			for index, part := range parts {
				part := mapValueDefault(part)
				if name := firstString(mapValueDefault(mapValueDefault(part["providerOptions"])["cursor"])["modelName"]); name != "" {
					if current = name; message.selected {
						model = name
					}
				}
				item := base
				item.NativeID, item.Model = message.id+":"+strconv.Itoa(index), current
				switch firstString(part["type"]) {
				case "reasoning":
					if text := firstString(part["text"]); strings.TrimSpace(text) != "" {
						add(reasoningRecord(item, text, "text", ""))
					} else if firstString(part["signature"]) != "" {
						add(reasoningRecord(item, "", "", "encrypted"))
					}
				case "text":
					if text := firstString(part["text"]); strings.TrimSpace(text) != "" {
						item.Role, item.Kind, item.Text = "assistant", "message", text
						item.CreatedAt = defaultString(times.next(times.replies, text), item.CreatedAt)
						add(item)
					}
				case "tool-call":
					name := firstString(part["toolName"])
					item.CallID, item.Role, item.Kind = firstString(part["toolCallId"]), "assistant", "tool_call"
					item.CreatedAt = defaultString(times.calls[item.CallID][0], item.CreatedAt)
					if matchesFold(name, "task") {
						item.Kind = "delegation"
					}
					item.Text = jsonText(map[string]any{"tool": name, "input": part["args"]})
					add(item)
				}
			}
		case "tool":
			high := mapValueDefault(mapValueDefault(mapValueDefault(value["providerOptions"])["cursor"])["highLevelToolCallResult"])
			outcome := mapValueDefault(high["output"])
			for index, part := range parts {
				part := mapValueDefault(part)
				if firstString(part["type"]) != "tool-result" {
					continue
				}
				name := firstString(part["toolName"])
				item := base
				item.NativeID, item.CallID, item.Role, item.Kind = message.id+":"+strconv.Itoa(index), firstString(part["toolCallId"]), "tool", "tool_result"
				item.CreatedAt = defaultString(times.calls[item.CallID][1], item.CreatedAt)
				if matchesFold(name, "task") {
					item.Kind = "delegation_result"
				}
				// The CLI flags a failed call on the result or on its outcome.
				payload := map[string]any{"tool": name, "content": part["result"], "is_error": part["isError"] == true || len(parts) == 1 && (high["isError"] == true || outcome["error"] != nil)}
				// What the CLI ran, when the message holds one result.
				if len(parts) == 1 {
					details := map[string]any{}
					success, failure := mapValue(outcome["success"]), mapValue(outcome["failure"])
					if failure != nil {
						payload["is_error"] = true
						if exit, ok := number(failure["exitCode"]); ok {
							details["exit_code"] = exit
						}
					} else if success != nil && matchesFold(name, "shell") {
						details["exit_code"] = 0
					}
					if ran, ok := number(firstNonNil(success["localExecutionTimeMs"], failure["localExecutionTimeMs"])); ok {
						details["duration_ms"] = ran
					}
					if len(details) > 0 {
						payload["details"] = details
					}
				}
				item.Text = jsonText(payload)
				add(item)
			}
		}
	}
	if lost {
		// Unread names nothing, so every message the read lacks is kept.
		conversation.Coverage, conversation.Unread = "partial", nil
	}
	if conversation.Model = model; conversation.Model == "" {
		conversation.Model = firstString(mapValueDefault(meta)["lastUsedModel"])
		if conversation.Model == "default" {
			conversation.Model = ""
		}
	}
	conversation.EndedAt = max(at, cursorCLITime(extra["updatedAtMs"]))
	location := firstString(extra["cwd"])
	if location == "" && strings.HasPrefix(root.folder, "file://") {
		location, _ = vscodeWorkspaceLocation(map[string]any{"folder": root.folder})
	}
	selected := []MessageRecord{}
	for _, message := range conversation.Messages {
		if message.Selected {
			selected = append(selected, message)
		}
	}
	purpose, outcome := agentPurposeOutcome(selected)
	title := firstString(meta["name"])
	if !meaningfulWorkspaceTitle(title, agent) {
		title = firstString(extra["title"])
	}
	if !meaningfulWorkspaceTitle(title, agent) {
		title = defaultString(purpose, "Cursor "+short(agent))
	}
	metadata := map[string]any{}
	if mode := firstString(meta["mode"]); mode != "" {
		metadata["mode"] = mode
	}
	var repository map[string]any
	if location != "" {
		repository = a.repository(location, "")
	}
	record := &WorkspaceRecord{SourceID: agent, SourceKind: "cursor-cli", Title: title, Account: a.config.Account, Purpose: purpose, Outcome: outcome,
		ActivityAt: conversation.EndedAt, Location: location, Repository: repository, Metadata: metadata}
	if len(conversation.Messages) > 0 || conversation.Coverage == "partial" {
		record.Conversations = []ConversationRecord{conversation}
	}
	observe(record, version)
	return record
}

// cursorCLIPrompt is a prompt's text without what the CLI wraps it in, and
// when it was sent, to the minute, from the time the CLI adds.
var cursorCLIStamp = regexp.MustCompile(`(?s)^\s*<timestamp>(.*?)</timestamp>\s*`)
var cursorCLIQuery = regexp.MustCompile(`(?s)<user_query>\s*(.*)\s*</user_query>`)
var cursorCLIZone = regexp.MustCompile(`^(.*?)\s*\(UTC(?:([+-])(\d{1,2})(?::(\d{2}))?)?\)$`)

func cursorCLIPrompt(text string) (prompt, sent string) {
	if match := cursorCLIStamp.FindStringSubmatch(text); match != nil {
		text = text[len(match[0]):]
		if zone := cursorCLIZone.FindStringSubmatch(strings.TrimSpace(match[1])); zone != nil {
			offset := 0
			if zone[2] != "" {
				hours, _ := strconv.Atoi(zone[3])
				minutes, _ := strconv.Atoi(zone[4])
				offset = hours*3600 + minutes*60
				if zone[2] == "-" {
					offset = -offset
				}
			}
			if at, err := time.ParseInLocation("Monday, Jan 2, 2006, 3:04 PM", zone[1], time.FixedZone("", offset)); err == nil {
				sent = at.UTC().Format("2006-01-02T15:04:05.000Z")
			}
		}
	}
	if match := cursorCLIQuery.FindStringSubmatch(text); match != nil {
		return strings.TrimSpace(match[1]), sent
	}
	return strings.TrimSpace(text), sent
}

// cursorCLITime is a time the CLI keeps in milliseconds, as a number or as
// text.
func cursorCLITime(value any) string {
	if text, ok := value.(string); ok {
		if ms, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64); err == nil {
			value = ms
		}
	}
	return iso(value)
}
