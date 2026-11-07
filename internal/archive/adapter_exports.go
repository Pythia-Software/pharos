package archive

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// chatGPTAdapter reads a ChatGPT data export (ChatGPT → Settings → Data
// controls → Export data): the ZIP it arrives in, the folder it unzips to,
// or one of its conversation files. An export holds conversations.json, or
// numbered shards conversations-000.json, conversations-001.json, and so on.
// Each conversation is a tree of messages; the export records no token
// usage.
type chatGPTAdapter struct{ baseAdapter }

// chatGPTExtractor versions the parser; bump it when parsing changes.
const chatGPTExtractor = "chatgpt-v2"

func (a *chatGPTAdapter) indexVersion() string { return chatGPTExtractor }

// chatGPTShard names an export's numbered conversation files.
var chatGPTShard = regexp.MustCompile(`^conversations-\d+\.json$`)

// chatGPTConversationFiles picks an export's conversation files from the
// names it holds: its shards, in order, else conversations.json.
func chatGPTConversationFiles(names []string) []string {
	shards, whole := []string{}, []string{}
	for _, name := range names {
		switch base := path.Base(filepath.ToSlash(name)); {
		case chatGPTShard.MatchString(base):
			shards = append(shards, name)
		case base == "conversations.json":
			whole = append(whole, name)
		}
	}
	if len(shards) > 0 {
		slices.Sort(shards)
		return shards
	}
	return whole
}

// exportFiles are the files the source names: a folder's conversation
// files, or the file itself (a ZIP, or one conversation file).
func (a *chatGPTAdapter) exportFiles() ([]string, error) {
	info, err := a.files().Stat(a.config.Path)
	if err != nil || !info.IsDir() {
		return []string{a.config.Path}, nil
	}
	entries, err := a.files().ReadDir(a.config.Path)
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, entry := range entries {
		if !entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	files := chatGPTConversationFiles(names)
	if len(files) == 0 {
		return nil, fmt.Errorf("ChatGPT export %s holds no conversations.json", a.config.Path)
	}
	for index, name := range files {
		files[index] = filepath.Join(a.config.Path, name)
	}
	return files, nil
}

func (a *chatGPTAdapter) Fingerprint() (string, error) {
	files, err := a.exportFiles()
	if err != nil {
		return "", err
	}
	rows := []string{}
	for _, file := range files {
		if _, err := a.files().Stat(file); err != nil {
			return "", fmt.Errorf("ChatGPT export is missing %s", file)
		}
		fingerprint, err := a.files().pathFingerprint(file, func(string, os.DirEntry) bool { return true })
		if err != nil {
			return "", err
		}
		rows = append(rows, filepath.Base(file)+":"+fingerprint)
	}
	return a.indexVersion() + ":" + hashBytes([]byte(strings.Join(rows, "\n"))), nil
}

func (a *chatGPTAdapter) Discover(emit func(WorkspaceRecord) error) error {
	files, err := a.exportFiles()
	if err != nil {
		return err
	}
	// A file that can't be read is reported, and the others are read.
	failures := []error{}
	for _, file := range files {
		if err := a.discoverFile(file, emit); errors.Is(err, errChatGPTEmit) {
			return err
		} else if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// errChatGPTEmit marks a failure to index what was read, which stops the
// read.
var errChatGPTEmit = errors.New("ChatGPT export not indexed")

func (a *chatGPTAdapter) discoverFile(file string, emit func(WorkspaceRecord) error) error {
	source, err := a.files().Open(file)
	if err != nil {
		return fmt.Errorf("cannot read ChatGPT export %s: %w", file, err)
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		return fmt.Errorf("cannot read ChatGPT export %s: %w", file, err)
	}
	version, origin := info.ModTime().UnixNano(), a.original(file)
	if !strings.EqualFold(filepath.Ext(file), ".zip") {
		return a.decode(source, file, origin, version, emit)
	}
	archive, err := exportZip(source, info.Size())
	if err != nil {
		return fmt.Errorf("cannot read ChatGPT export %s: %w", file, err)
	}
	entries := map[string]*zip.File{}
	names := []string{}
	for _, entry := range archive.File {
		entries[entry.Name] = entry
		names = append(names, entry.Name)
	}
	chosen := chatGPTConversationFiles(names)
	if len(chosen) == 0 {
		return fmt.Errorf("ChatGPT export %s: the ZIP holds no conversations.json", file)
	}
	failures := []error{}
	for _, name := range chosen {
		content, err := entries[name].Open()
		if err != nil {
			failures = append(failures, fmt.Errorf("ChatGPT export %s: %s: %w", file, name, err))
			continue
		}
		err = a.decode(content, file+"/"+name, origin+"/"+name, version, emit)
		content.Close()
		if errors.Is(err, errChatGPTEmit) {
			return err
		} else if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// exportZip opens an export's ZIP. A capture may store the ZIP compressed, so
// it is read whole when the file is not available for random access.
func exportZip(source *sourceFile, size int64) (*zip.Reader, error) {
	var at io.ReaderAt
	if source.file != nil && source.decoder == nil {
		at = source.file
	} else {
		data, err := io.ReadAll(source)
		if err != nil {
			return nil, err
		}
		at, size = bytes.NewReader(data), int64(len(data))
	}
	return zip.NewReader(at, size)
}

type chatGPTConversation struct {
	ID             string                 `json:"id"`
	ConversationID string                 `json:"conversation_id"`
	Title          string                 `json:"title"`
	CreateTime     any                    `json:"create_time"`
	UpdateTime     any                    `json:"update_time"`
	CurrentNode    string                 `json:"current_node"`
	DefaultModel   string                 `json:"default_model_slug"`
	Mapping        map[string]chatGPTNode `json:"mapping"`
}

type chatGPTNode struct {
	Parent   string         `json:"parent"`
	Children []string       `json:"children"`
	Message  map[string]any `json:"message"`
}

// decode reads one conversation file, a conversation at a time: an export
// can be large. A conversation not in the shape ChatGPT writes is reported,
// and the others are read.
func (a *chatGPTAdapter) decode(content io.Reader, file, origin string, version int64, emit func(WorkspaceRecord) error) error {
	decoder := json.NewDecoder(content)
	decoder.UseNumber()
	if token, err := decoder.Token(); err != nil || token != json.Delim('[') {
		return fmt.Errorf("ChatGPT export %s must contain an array", file)
	}
	failures := []error{}
	for index := 0; decoder.More(); index++ {
		var conversation chatGPTConversation
		if err := decoder.Decode(&conversation); err != nil {
			// A value of the wrong type is read past; anything else
			// leaves nothing after it to read.
			var mistyped *json.UnmarshalTypeError
			if failures = append(failures, fmt.Errorf("ChatGPT export %s conversation %d: %w", file, index, err)); errors.As(err, &mistyped) {
				continue
			}
			return errors.Join(failures...)
		}
		if defaultString(conversation.ConversationID, conversation.ID) == "" {
			failures = append(failures, fmt.Errorf("ChatGPT export %s conversation %d has no ID", file, index))
			continue
		}
		if conversation.Mapping == nil {
			failures = append(failures, fmt.Errorf("ChatGPT export %s conversation %d has no mapping", file, index))
			continue
		}
		if err := emit(a.record(conversation, origin, version)); err != nil {
			return fmt.Errorf("%w: %w", errChatGPTEmit, err)
		}
	}
	// More also stops at the end of the input or a read error, so only the
	// closing bracket shows the whole file was read.
	if token, err := decoder.Token(); err != nil || token != json.Delim(']') {
		return errors.Join(append(failures, fmt.Errorf("ChatGPT export %s ends early", file))...)
	}
	if err := ensureEOF(decoder); err != nil {
		failures = append(failures, fmt.Errorf("ChatGPT export %s: %w", file, err))
	}
	return errors.Join(failures...)
}

// record reads a conversation's tree: each node in order, after its parent,
// with the branch ending at its current node selected. Regenerated and
// edited turns are siblings, kept unselected.
func (a *chatGPTAdapter) record(conversation chatGPTConversation, origin string, version int64) WorkspaceRecord {
	nativeID := defaultString(conversation.ConversationID, conversation.ID)
	children := chatGPTChildren(conversation.Mapping)
	selected := chatGPTSelected(conversation, children)
	messages := []MessageRecord{}
	// read is the message each node, or the nearest node above it, was
	// read as; calls and tools are the call each call or reply node
	// belongs to, and its tool; replies marks the replies.
	read, calls, tools, kinds := map[string]string{}, map[string]string{}, map[string]string{}, map[string]string{}
	replies := map[string]bool{}
	// onward is the reply a reply runs on into: of the tool's replies
	// after it, the one on the current branch, else the first. Another is
	// an alternative to it, which no branch holds both of.
	onward := func(parent string) string {
		first := ""
		for _, child := range children[parent] {
			if author := mapValue(mapValue(conversation.Mapping[child].Message)["author"]); firstString(author["role"]) != "tool" {
				continue
			}
			if selected[child] {
				return child
			}
			first = defaultString(first, child)
		}
		return first
	}
	unread := []string{}
	for _, nodeID := range chatGPTOrder(conversation.Mapping, children) {
		node := conversation.Mapping[nodeID]
		read[nodeID] = read[node.Parent]
		message := node.Message
		if message == nil {
			continue
		}
		metadata, content, author := mapValue(message["metadata"]), mapValue(message["content"]), mapValue(message["author"])
		if content == nil {
			// A message without content is damage: what was indexed from it
			// is kept.
			unread = append(unread, defaultString(message["id"], nodeID))
			continue
		}
		id, role, contentType := defaultString(message["id"], nodeID), defaultString(author["role"], "unknown"), firstString(content["content_type"])
		kinds[nodeID] = contentType
		// A message's own metadata says how fast it was served.
		inference, _ := inferenceSetting(message)
		base := func(suffix string) MessageRecord {
			return withInference(MessageRecord{NativeID: id + suffix, Role: role, Model: firstString(metadata["model_slug"]), Effort: recordedEffort(metadata), CreatedAt: iso(message["create_time"]),
				PreviousNativeID: read[node.Parent], EvidenceLocator: fmt.Sprintf("%s:mapping:%s%s", origin, nodeID, suffix), Selected: selected[nodeID]}, inference)
		}
		own := false
		add := func(item MessageRecord) {
			messages = append(messages, item)
			if !own {
				read[nodeID], own = item.NativeID, true
			}
		}
		item := base("")
		item.RawText = accountingRaw(message)
		switch contentType {
		case "user_editable_context":
			// The person's custom instructions, which ChatGPT hides and
			// adds to the conversation: context, not what they wrote.
			if text := strings.TrimSpace(firstString(content["user_profile"]) + "\n\n" + firstString(content["user_instructions"])); text != "" {
				add(injectedContext(item, "chatgpt_custom_instructions", text))
				continue
			}
		case "model_editable_context":
			if text := strings.TrimSpace(firstString(content["model_set_context"])); text != "" {
				add(injectedContext(item, "chatgpt_memory", text))
				continue
			}
		}
		recipient := firstString(message["recipient"])
		text, attachments := chatGPTContent(content)
		switch {
		case contentType == "thoughts":
			// Each thought is read as its text, or when it holds none as
			// its summary, in order: so a record holding any summary is
			// one.
			thoughts, format := []string{}, "text"
			for _, raw := range reasoningList(content["thoughts"]) {
				thought := mapValue(raw)
				if text := strings.TrimSpace(firstString(thought["content"])); text != "" {
					thoughts = append(thoughts, text)
				} else if summary := strings.TrimSpace(firstString(thought["summary"])); summary != "" {
					thoughts, format = append(thoughts, summary), "summary"
				}
			}
			if len(thoughts) == 0 {
				add(reasoningRecord(item, "", "", "hidden"))
			} else {
				add(reasoningRecord(item, strings.Join(thoughts, "\n\n"), format, ""))
			}
		case contentType == "reasoning_recap":
			// "Thought for 12 seconds": the reasoning it follows is already
			// read; alone, it says only that the model reasoned.
			if kinds[node.Parent] != "thoughts" {
				add(reasoningRecord(item, "", "", "hidden"))
			}
		case role == "assistant" && recipient != "" && recipient != "all":
			// A message to a tool (python, browser, dalle.text2im, ...) is
			// the call; the tool's reply follows it.
			item.Kind, item.CallID, item.Text = "tool_call", item.NativeID, jsonText(map[string]any{"tool": recipient, "input": text})
			if language := firstString(content["language"]); language != "" {
				item.Text = jsonText(map[string]any{"tool": recipient, "input": text, "language": language})
			}
			add(item)
			calls[nodeID], tools[nodeID] = item.NativeID, recipient
		case role == "tool" && calls[node.Parent] == "" && contentType == "text" && firstString(metadata["initial_text"], metadata["finished_text"]) != "":
			// o1's reasoning, in older exports: a summary of it, from a
			// tool ("Thinking", then "Thought for 27 seconds") that no call
			// asked.
			if text == "" {
				add(reasoningRecord(item, "", "", "hidden"))
			} else {
				add(reasoningRecord(item, text, "summary", ""))
			}
		case role == "tool":
			// The tool replies to the call before it. A reply can run on
			// into more, such as each page a browser click quotes: each
			// replies to the call, and the one it runs on into names the
			// reply it continues.
			result := map[string]any{"tool": defaultString(author["name"], tools[node.Parent]), "content": text}
			item.CallID = calls[node.Parent]
			if item.CallID != "" && replies[node.Parent] && onward(node.Parent) == nodeID {
				result["continues"] = read[node.Parent]
			}
			switch contentType {
			case "system_error":
				result["is_error"] = true
			case "tether_quote":
				result["url"], result["title"] = content["url"], content["title"]
			}
			// Python's exception can be told only in how its run ended.
			if run := mapValue(metadata["aggregate_result"]); run != nil && (run["status"] == "failed_with_in_kernel_exception" || mapValue(run["in_kernel_exception"]) != nil) {
				result["is_error"] = true
			}
			item.Kind, item.Text = "tool_result", jsonText(result)
			add(item)
			if item.CallID != "" {
				calls[nodeID], tools[nodeID], replies[nodeID] = item.CallID, firstString(result["tool"]), true
			}
		default:
			// Whatever text a node holds is read, under the node's ID, as
			// the adapter before chatgpt-v2 read it: so a read holds every
			// message an older one indexed, and replaces it.
			if text == "" {
				text = messageText(content["parts"])
			}
			if text == "" {
				break
			}
			if metadata["is_visually_hidden_from_conversation"] == true {
				// What ChatGPT hides from the person, such as its system
				// prompt, is context, not their conversation.
				add(injectedContext(item, "chatgpt_hidden", text))
				break
			}
			item.Kind, item.Text = "message", text
			add(item)
		}
		// A node read as nothing above that holds text, such as a recap
		// after the thoughts it sums up, keeps its ID, as the adapter
		// before chatgpt-v2 read it.
		if text := messageText(content["parts"]); !own && text != "" {
			add(injectedContext(item, "chatgpt_"+defaultString(contentType, "message"), text))
		}
		for index, attachment := range attachments {
			attached := base(fmt.Sprintf(":part:%d", index))
			attached.Kind, attached.Text, attached.RawText = "attachment", jsonText(attachment), jsonText(attachment)
			add(attached)
		}
	}
	active, model := []MessageRecord{}, ""
	for _, message := range messages {
		if message.Selected {
			active = append(active, message)
			if message.Role == "assistant" && message.Model != "" {
				model = message.Model
			}
		}
	}
	purpose, outcome := agentPurposeOutcome(active)
	title := defaultString(conversation.Title, "ChatGPT "+short(nativeID))
	created, updated := iso(conversation.CreateTime), iso(conversation.UpdateTime)
	coverage := "complete"
	if len(unread) > 0 {
		coverage = "partial"
	}
	return WorkspaceRecord{Observed: version, SourceID: nativeID, SourceKind: "chatgpt", Title: title, Account: a.config.Account, Purpose: purpose, Outcome: outcome, ActivityAt: updated, Metadata: map[string]any{},
		Conversations: []ConversationRecord{{NativeID: nativeID, Provider: "chatgpt", Model: defaultString(model, conversation.DefaultModel), Account: a.config.Account, Origin: "user-export", Harness: "chatgpt-export", Coverage: coverage, Unread: unread, Messages: messages, StartedAt: created, EndedAt: updated, Observed: version}}}
}

// chatGPTContent is a message's text, and the files (images, audio) its
// parts point to.
func chatGPTContent(content map[string]any) (string, []map[string]any) {
	texts, attachments := []string{}, []map[string]any{}
	if text := firstString(content["text"], content["result"]); text != "" {
		texts = append(texts, text)
	}
	parts, _ := content["parts"].([]any)
	for _, raw := range parts {
		switch part := raw.(type) {
		case string:
			if part != "" {
				texts = append(texts, part)
			}
		case map[string]any:
			// Text in the shapes the adapter before chatgpt-v2 read too.
			if kind := firstString(part["type"]); kind == "text" || kind == "input_text" || kind == "output_text" {
				texts = append(texts, firstString(part["text"]))
				continue
			}
			if part["content_type"] == "audio_transcription" {
				if text := firstString(part["text"]); text != "" {
					texts = append(texts, text)
				}
				continue
			}
			// An image, a recording or a video frame, whole: its pointers
			// may be nested.
			attachments = append(attachments, part)
		}
	}
	return strings.Join(texts, "\n"), attachments
}

// chatGPTSelected marks the nodes on the conversation's current branch: the
// path from its current node, or when the export names none from its newest
// last message (one with no message after it), to the root.
func chatGPTSelected(conversation chatGPTConversation, children map[string][]string) map[string]bool {
	parents := map[string]string{}
	for id, node := range conversation.Mapping {
		parents[id] = node.Parent
	}
	leaf := conversation.CurrentNode
	if _, ok := conversation.Mapping[leaf]; !ok {
		// below reports whether a message follows a node.
		known := map[string]bool{}
		var below func(string) bool
		below = func(id string) bool {
			if found, ok := known[id]; ok {
				return found
			}
			known[id] = false
			for _, child := range children[id] {
				if conversation.Mapping[child].Message != nil || below(child) {
					known[id] = true
					break
				}
			}
			return known[id]
		}
		// latest is the latest time on a node's path, so a message without
		// one takes its branch's.
		times := map[string]string{}
		var latest func(string) string
		latest = func(id string) string {
			if at, ok := times[id]; ok {
				return at
			}
			times[id] = ""
			node, ok := conversation.Mapping[id]
			if !ok {
				return ""
			}
			times[id] = max(iso(node.Message["create_time"]), latest(node.Parent))
			return times[id]
		}
		leaf = ""
		newest := ""
		for _, id := range slices.Sorted(maps.Keys(conversation.Mapping)) {
			at := latest(id)
			if conversation.Mapping[id].Message != nil && !below(id) && (leaf == "" || at >= newest) {
				leaf, newest = id, at
			}
		}
	}
	return branchPath(parents, leaf)
}

// branchPath is the path from leaf up its parents to the root.
func branchPath(parents map[string]string, leaf string) map[string]bool {
	selected := map[string]bool{}
	for id := leaf; id != "" && !selected[id]; id = parents[id] {
		if _, ok := parents[id]; !ok {
			break
		}
		selected[id] = true
	}
	return selected
}

// chatGPTChildren are each node's children: the nodes that name it as their
// parent, since an export may not list them. They are in the order the
// parent lists them, then by time.
func chatGPTChildren(mapping map[string]chatGPTNode) map[string][]string {
	children := map[string][]string{}
	for _, id := range slices.Sorted(maps.Keys(mapping)) {
		children[mapping[id].Parent] = append(children[mapping[id].Parent], id)
	}
	for parent, ids := range children {
		listed := map[string]int{}
		for index, child := range mapping[parent].Children {
			listed[child] = index + 1
		}
		slices.SortStableFunc(ids, func(a, b string) int {
			if listed[a] != listed[b] {
				if listed[a] == 0 || listed[b] == 0 {
					return listed[b] - listed[a]
				}
				return listed[a] - listed[b]
			}
			return strings.Compare(iso(mapping[a].Message["create_time"]), iso(mapping[b].Message["create_time"]))
		})
	}
	return children
}

// chatGPTOrder lists a conversation's nodes each after its parent, and any
// the tree doesn't reach last.
func chatGPTOrder(mapping map[string]chatGPTNode, children map[string][]string) []string {
	order, seen := []string{}, map[string]bool{}
	var visit func(string)
	visit = func(id string) {
		if seen[id] {
			return
		}
		seen[id] = true
		order = append(order, id)
		for _, child := range children[id] {
			visit(child)
		}
	}
	ids := slices.Sorted(maps.Keys(mapping))
	for _, id := range ids {
		if _, ok := mapping[mapping[id].Parent]; !ok {
			visit(id)
		}
	}
	for _, id := range ids {
		visit(id)
	}
	return order
}
