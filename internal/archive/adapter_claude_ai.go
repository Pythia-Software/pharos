package archive

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// claudeAIAdapter reads a Claude.ai data export (claude.ai → Settings →
// Export data): conversations.json, the folder holding it, or the ZIP the
// export arrives in. Each conversation is a tree of chat messages;
// model and usage are available only when explicitly recorded in the export.
type claudeAIAdapter struct{ baseAdapter }

// claudeAIExtractor versions the parser; bump it when parsing changes.
const claudeAIExtractor = "claude-ai-v6"

// claudeAIRoot is the parent a conversation's first message names.
const claudeAIRoot = "00000000-0000-4000-8000-000000000000"

func (a *claudeAIAdapter) indexVersion() string { return claudeAIExtractor }

func (a *claudeAIAdapter) Fingerprint() (string, error) {
	inputs, err := claudeExportInputs(a.files(), a.config.Path)
	if err != nil {
		return "", err
	}
	if len(inputs) == 0 {
		return "", errors.New("choose the Claude.ai export: no conversation files or collected export inventories were found")
	}
	var values []string
	for _, input := range inputs {
		if input.retired {
			values = append(values, a.original(input.path)+":"+input.sha256)
			continue
		}
		value, err := a.files().pathFingerprint(input.path, func(string, os.DirEntry) bool { return true })
		if err != nil {
			return "", err
		}
		values = append(values, a.original(input.path)+":"+value)
	}
	return a.indexVersion() + ":" + hashBytes([]byte(strings.Join(values, "\n"))), nil
}

type claudeAIConversation struct {
	Fields      map[string]any `json:"source_fields,omitempty"`
	UUID        string         `json:"uuid"`
	Name        string         `json:"name"`
	Summary     string         `json:"summary"`
	CreatedAt   string         `json:"created_at"`
	UpdatedAt   string         `json:"updated_at"`
	CurrentLeaf string         `json:"current_leaf_message_uuid"`
	Account     struct {
		UUID string `json:"uuid"`
	} `json:"account"`
	Messages []map[string]any `json:"chat_messages"`
}

func (c *claudeAIConversation) UnmarshalJSON(data []byte) error {
	type plain claudeAIConversation
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	if err := decoder.Decode((*plain)(c)); err != nil {
		return err
	}
	decoder = json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	c.Fields = nil
	if err := decoder.Decode(&c.Fields); err != nil {
		return err
	}
	delete(c.Fields, "chat_messages")
	return nil
}

func (a *claudeAIAdapter) Discover(emit func(WorkspaceRecord) error) error {
	inputs, err := claudeExportInputs(a.files(), a.config.Path)
	if err != nil {
		return err
	}
	// Validate every conversation input before publishing any of this source's
	// records. A damaged late shard must not partially replace a previous index.
	seen := map[string]string{}
	for _, input := range inputs {
		if err := verifyClaudeExportInput(a.files(), input); err != nil {
			return err
		}
		if input.conversations {
			check := func(c claudeAIConversation) error {
				key := input.snapshot + "\x00" + c.UUID
				digest := hashBytes([]byte(jsonText(c)))
				if old, ok := seen[key]; ok && old != digest {
					return fmt.Errorf("Claude export has conflicting copies of conversation %s", c.UUID)
				}
				seen[key] = digest
				return nil
			}
			if err := a.readExport(input, nil, check); err != nil {
				return err
			}
		}
		if err := a.readSupplement(input, nil, func(name, digest string) error {
			key := input.snapshot + "\x00supplement\x00" + name
			if previous, ok := seen[key]; ok && previous != digest {
				return fmt.Errorf("Claude export has conflicting copies of %s", name)
			}
			seen[key] = digest
			return nil
		}); err != nil {
			return err
		}
	}
	supplements := map[string]*WorkspaceRecord{}
	var supplementOrder []string
	for _, input := range inputs {
		if input.conversations {
			if err := a.readExport(input, emit, nil); err != nil {
				return err
			}
		}
		if err := a.readSupplement(input, func(record WorkspaceRecord) error {
			key := input.snapshot + "\x00" + record.SourceID
			if previous := supplements[key]; previous != nil {
				known := map[string]bool{}
				for _, message := range previous.Conversations[0].Messages {
					known[message.NativeID] = true
				}
				for _, message := range record.Conversations[0].Messages {
					if !known[message.NativeID] {
						previous.Conversations[0].Messages = append(previous.Conversations[0].Messages, message)
						known[message.NativeID] = true
					}
				}
				previous.Observed = max(previous.Observed, record.Observed)
				previous.Conversations[0].Observed = previous.Observed
				if record.ActivityAt > previous.ActivityAt {
					previous.ActivityAt = record.ActivityAt
				}
				if !strings.HasPrefix(record.Title, "Claude.ai export · ") {
					previous.Title = record.Title
				}
			} else {
				supplements[key] = &record
				supplementOrder = append(supplementOrder, key)
			}
			return nil
		}, nil); err != nil {
			return err
		}
	}
	for _, key := range supplementOrder {
		if err := emit(*supplements[key]); err != nil {
			return err
		}
	}
	return nil
}

func (a *claudeAIAdapter) readExport(input claudeExportInput, emit func(WorkspaceRecord) error, check func(claudeAIConversation) error) error {
	if err := a.context().Err(); err != nil {
		return err
	}
	if input.units != nil {
		for _, unit := range input.units.Conversations {
			conversation, err := input.units.conversation(a.files(), input.objectRoot, unit)
			if err != nil {
				return err
			}
			if emit != nil {
				if err := emit(a.record(conversation, a.original(input.path), unit.Observed)); err != nil {
					return err
				}
			} else if check != nil {
				if err := check(conversation); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if strings.EqualFold(filepath.Ext(input.path), ".zip") {
		if _, err := a.files().Database(input.path); err != nil {
			return err
		}
	}
	source, err := a.files().Open(input.path)
	if err != nil {
		return err
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		return err
	}
	version := input.version
	if version == 0 {
		version = info.ModTime().UnixNano()
	}
	origin := a.original(input.path)
	decode := func(reader io.Reader) error {
		return decodeClaudeConversations(reader, func(conversation claudeAIConversation) error {
			if err := a.context().Err(); err != nil {
				return err
			}
			if emit == nil {
				if check != nil {
					return check(conversation)
				}
				return nil
			}
			return emit(a.record(conversation, origin, version))
		})
	}
	if strings.EqualFold(filepath.Ext(input.path), ".zip") {
		// Materialize only an encoded capture. exportZip otherwise uses ReadAt.
		archive, err := exportZip(source, info.Size())
		if err != nil {
			return fmt.Errorf("Claude ZIP %s: %w", input.path, err)
		}
		entries, err := claudeZipListing(archive)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			content, err := entry.Open()
			if err != nil {
				return err
			}
			err = decode(content)
			closeErr := content.Close()
			if err != nil || closeErr != nil {
				return fmt.Errorf("Claude export %s, %s: %w", input.path, entry.Name, errors.Join(err, closeErr))
			}
		}
		return nil
	}
	if err := decode(source); err != nil {
		return fmt.Errorf("Claude export %s: %w", input.path, err)
	}
	return nil
}

func decodeClaudeConversations(content io.Reader, emit func(claudeAIConversation) error) error {
	decoder := json.NewDecoder(content)
	decoder.UseNumber()
	if token, err := decoder.Token(); err != nil || token != json.Delim('[') {
		return errors.New("Claude.ai conversations must contain an array")
	}
	seen := map[string]bool{}
	for index := 0; decoder.More(); index++ {
		var conversation claudeAIConversation
		if err := decoder.Decode(&conversation); err != nil {
			return fmt.Errorf("conversation %d: %w", index, err)
		}
		if conversation.UUID == "" || seen[conversation.UUID] {
			return fmt.Errorf("conversation %d has a missing or duplicate ID", index)
		}
		seen[conversation.UUID] = true
		if conversation.Messages == nil {
			return fmt.Errorf("conversation %s has no messages list", conversation.UUID)
		}
		messages := map[string]bool{}
		for _, message := range conversation.Messages {
			id := firstString(message["uuid"])
			if id == "" || messages[id] {
				return fmt.Errorf("conversation %s has a missing or duplicate message ID", conversation.UUID)
			}
			messages[id] = true
		}
		if emit != nil {
			if err := emit(conversation); err != nil {
				return err
			}
		}
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim(']') {
		return errors.New("Claude.ai conversations end early")
	}
	return ensureEOF(decoder)
}

func (a *claudeAIAdapter) record(conversation claudeAIConversation, origin string, version int64) WorkspaceRecord {
	selected := claudeAISelected(conversation)
	messages := []MessageRecord{}
	if len(conversation.Fields) > 0 {
		messages = append(messages, MessageRecord{NativeID: conversation.UUID + ":export", Role: "system", Kind: "metadata", Selected: true,
			EvidenceLocator: origin + ":" + conversation.UUID,
			Text:            jsonText(map[string]any{"type": "claude_ai_conversation", "text": conversation.Summary, "details": conversation.Fields}), RawText: jsonText(map[string]any{"claude_ai_conversation": conversation.Fields})})
	}
	toolNames, delegated := map[string]string{}, map[string]bool{}
	for _, message := range conversation.Messages {
		start := len(messages)
		id := firstString(message["uuid"])
		role := "assistant"
		if firstString(message["sender"]) == "human" {
			role = "user"
		}
		parent := firstString(message["parent_message_uuid"])
		if parent == claudeAIRoot {
			parent = ""
		}
		at := iso(message["created_at"])
		base := func(suffix string) MessageRecord {
			return MessageRecord{NativeID: id + suffix, Role: role, CreatedAt: at, PreviousNativeID: parent, EvidenceLocator: fmt.Sprintf("%s:%s:message:%s%s", origin, conversation.UUID, id, suffix), Selected: selected[id]}
		}
		texts := 0
		blocks, _ := message["content"].([]any)
		for index, raw := range blocks {
			block := mapValue(raw)
			if block == nil {
				continue
			}
			// The block is the message's raw text, so nothing in a person's
			// words is read as protocol, such as pasted usage.
			item := base(fmt.Sprintf(":block:%d", index))
			item.RawText = jsonText(block)
			if start := iso(block["start_timestamp"]); start != "" {
				item.CreatedAt = start
			}
			// A result block has an ID of its own; tool_use_id names its call.
			callID := firstString(block["id"], block["tool_use_id"])
			if firstString(block["type"]) == "tool_result" {
				callID = firstString(block["tool_use_id"], block["id"])
			}
			switch firstString(block["type"]) {
			case "text", "voice_note":
				// A voice note holds what the person said, transcribed.
				if text := asString(block["text"]); strings.TrimSpace(text) != "" {
					// The first text is the message's own, by its ID alone,
					// wherever an export puts it among the blocks.
					if texts == 0 {
						raw, created := item.RawText, item.CreatedAt
						item = base("")
						item.RawText, item.CreatedAt = raw, created
					}
					item.Kind, item.Text = "message", text
					messages = append(messages, item)
					texts++
				} else {
					item.Kind, item.Text = "metadata", jsonText(block)
					messages = append(messages, item)
				}
			case "thinking":
				// Claude.ai hides the thinking itself and keeps summaries of it.
				text, format, unavailable := firstString(block["thinking"]), "text", ""
				if strings.TrimSpace(text) == "" {
					summaries, listed := []string{}, reasoningList(block["summaries"])
					for _, summary := range listed {
						if s := firstString(mapValue(summary)["summary"]); strings.TrimSpace(s) != "" {
							summaries = append(summaries, s)
						}
					}
					text, format = strings.Join(summaries, "\n\n"), "summary"
					if text == "" {
						format, unavailable = "", "hidden"
					}
				}
				messages = append(messages, reasoningRecord(item, text, format, unavailable))
			case "tool_use":
				// The enclosing assistant message can be stamped after all its
				// blocks. Only the tool block's own timestamp locates a call.
				item.CreatedAt = claudeAIToolBlockTime(block, false)
				name := defaultString(block["name"], "tool")
				toolNames[callID] = name
				item.Kind = "tool_call"
				if matchesFold(name, "task", "agent", "spawn_agent", "delegate", "workflow") {
					item.Kind = "delegation"
					delegated[callID] = true
				}
				item.CallID, item.Text = callID, jsonText(claudeAIToolPayload(block, name))
				messages = append(messages, item)
			case "tool_result":
				// A missing result timestamp is not the enclosing message's
				// timestamp and does not establish when the call finished.
				item.CreatedAt = claudeAIToolBlockTime(block, true)
				item.Role, item.Kind, item.CallID = "tool", "tool_result", callID
				if delegated[callID] {
					item.Kind = "delegation_result"
				}
				item.Text = jsonText(claudeAIToolPayload(block, firstString(toolNames[callID], block["name"])))
				messages = append(messages, item)
			case "injected_prompt_block":
				// Claude.ai adds the date, memory and tool instructions to the
				// person's turn; they are context, not what the person wrote.
				messages = append(messages, injectedContext(item, defaultString(block["injection_source"], "claude_ai"), firstString(block["prompt"])))
			default:
				messages = append(messages, injectedContext(item, "claude_ai_unknown_block", jsonText(block)))
			case "document", "image":
				item.Kind = "attachment"
				item.Text = jsonText(block)
				messages = append(messages, item)
			case "token_budget":
				item.Kind, item.Text = "metadata", jsonText(block)
				messages = append(messages, item)
			}
		}
		// A message without text blocks keeps its flattened text, as the
		// message's own text. Exports from before content blocks hold only
		// that, which a later export holds as its first text block.
		if text := asString(message["text"]); texts == 0 && strings.TrimSpace(text) != "" {
			item := base("")
			item.Kind, item.Text, item.RawText = "message", text, jsonText(map[string]any{"text": text})
			messages = append(messages, item)
		}
		attachments, _ := message["attachments"].([]any)
		for index, raw := range attachments {
			attachment := mapValue(raw)
			if attachment == nil {
				continue
			}
			item := base(fmt.Sprintf(":attachment:%d", index))
			item.Kind, item.RawText = "attachment", jsonText(attachment)
			value := maps.Clone(attachment)
			value["type"] = "file_attachment"
			item.Text = jsonText(value)
			messages = append(messages, item)
		}
		if files, _ := message["files"].([]any); len(files) > 0 {
			names := []string{}
			for _, raw := range files {
				names = append(names, firstString(mapValue(raw)["file_name"]))
			}
			item := base(":files")
			item.Kind, item.Text, item.RawText = "attachment", jsonText(map[string]any{"type": "files", "file_names": names, "files": files}), jsonText(files)
			messages = append(messages, item)
		}
		// Retain the envelope once, without repeating the full content tree on
		// every block. Never interpret quoted text or tool output as accounting.
		envelope := maps.Clone(message)
		for _, key := range []string{"content", "attachments", "files"} {
			delete(envelope, key)
		}
		if len(messages) == start {
			item := base("")
			item.Kind, item.Text = "metadata", jsonText(map[string]any{"type": "claude_ai_message", "details": envelope})
			messages = append(messages, item)
		}
		var raw map[string]any
		decoder := json.NewDecoder(strings.NewReader(messages[start].RawText))
		decoder.UseNumber()
		_ = decoder.Decode(&raw)
		if raw == nil {
			raw = map[string]any{}
		}
		raw["claude_ai_message"] = envelope
		messages[start].RawText = jsonText(raw)
		if role == "assistant" {
			model := firstString(message["model"], mapValue(message["metadata"])["model"])
			for i := start; i < len(messages); i++ {
				messages[i].Model = model
			}
			usage := mapValue(firstNonNil(message["usage"], mapValue(message["metadata"])["usage"]))
			if normalizeTokenCounts(usage) != nil {
				item := base(":usage")
				item.Role, item.Kind, item.Model = "system", "metadata", model
				item.Text = jsonText(map[string]any{"type": "token_usage", "usage_message_id": id, "usage": usage, "model": model})
				messages = append(messages, item)
			}
		}
	}
	active := []MessageRecord{}
	for _, message := range messages {
		if message.Selected {
			active = append(active, message)
		}
	}
	purpose, outcome := agentPurposeOutcome(active)
	title := defaultString(conversation.Name, "Claude.ai "+short(conversation.UUID))
	created, updated := iso(conversation.CreatedAt), iso(conversation.UpdatedAt)
	return WorkspaceRecord{Observed: version, SourceID: conversation.UUID, SourceKind: "claude-ai", Title: title, Account: a.config.Account, Purpose: purpose, Outcome: outcome, ActivityAt: updated, Metadata: claudeConversationMetadata(conversation),
		Conversations: []ConversationRecord{{NativeID: conversation.UUID, Provider: "claude", Account: a.config.Account, Origin: "user-export", Harness: "claude-ai-export", Coverage: "unknown", Messages: messages, StartedAt: created, EndedAt: updated, Observed: version}}}
}

func claudeAIToolPayload(block map[string]any, name string) map[string]any {
	value := map[string]any{"tool": name}
	for _, key := range []string{"input", "content", "is_error", "structured_content", "display_content", "message", "integration_name", "tool_origin", "input_incomplete"} {
		if v, ok := block[key]; ok {
			value[key] = v
		}
	}
	return value
}

// A result block's start is when the output was observed; its stop is a
// fallback when only that observation is present. A tool-use block's stop
// marks the emitted input, not necessarily the tool's completion.
func claudeAIToolBlockTime(block map[string]any, result bool) string {
	if at := iso(block["start_timestamp"]); at != "" {
		return at
	}
	if result {
		return iso(block["stop_timestamp"])
	}
	return ""
}

// Repair only recognizable retained export blocks. A missing, truncated or
// unrelated raw record does not justify changing an existing timestamp.
func claudeAIRetainedToolTime(message MessageRecord) (string, bool) {
	var block map[string]any
	if json.Unmarshal([]byte(message.RawText), &block) != nil {
		return "", false
	}
	switch firstString(block["type"]) {
	case "tool_use":
		if message.Kind == "tool_call" || message.Kind == "delegation" {
			return claudeAIToolBlockTime(block, false), true
		}
	case "tool_result":
		if message.Kind == "tool_result" || message.Kind == "delegation_result" {
			return claudeAIToolBlockTime(block, true), true
		}
	}
	return "", false
}

func claudeConversationMetadata(c claudeAIConversation) map[string]any {
	metadata := map[string]any{"summary": c.Summary}
	if c.Account.UUID != "" {
		metadata["claude_account_uuid"] = c.Account.UUID
	}
	return metadata
}

// claudeAISelected marks the messages on the conversation's active branch:
// the path from its current leaf, or when the export names none from its
// newest leaf, to the root. Edited and regenerated turns are siblings. An
// export without parent links selects every message.
func claudeAISelected(conversation claudeAIConversation) map[string]bool {
	parents, children := map[string]string{}, map[string]int{}
	linked := false
	for _, message := range conversation.Messages {
		id, parent := firstString(message["uuid"]), firstString(message["parent_message_uuid"])
		parents[id] = parent
		children[parent]++
		linked = linked || parent != ""
	}
	// Exports from before Claude.ai kept a tree name no parents: the
	// conversation is its messages, in order.
	if !linked {
		selected := map[string]bool{}
		for id := range parents {
			selected[id] = true
		}
		return selected
	}
	leaf := conversation.CurrentLeaf
	if _, ok := parents[leaf]; !ok {
		leaf = ""
		var newest time.Time
		for _, message := range conversation.Messages {
			id := firstString(message["uuid"])
			at, _ := parseTime(firstString(message["created_at"]))
			if children[id] == 0 && !at.Before(newest) {
				leaf, newest = id, at
			}
		}
	}
	return branchPath(parents, leaf)
}
