package archive

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// claudeAIAdapter reads a Claude.ai data export (claude.ai → Settings →
// Export data): conversations.json, the folder holding it, or the ZIP the
// export arrives in. Each conversation is a tree of chat messages;
// the export names no model and records no token usage.
type claudeAIAdapter struct{ baseAdapter }

// claudeAIExtractor versions the parser; bump it when parsing changes.
const claudeAIExtractor = "claude-ai-v1"

// claudeAIRoot is the parent a conversation's first message names.
const claudeAIRoot = "00000000-0000-4000-8000-000000000000"

func (a *claudeAIAdapter) indexVersion() string { return claudeAIExtractor }

func (a *claudeAIAdapter) exportFile() string {
	info, err := os.Stat(a.config.Path)
	if err == nil && info.IsDir() {
		return filepath.Join(a.config.Path, "conversations.json")
	}
	return a.config.Path
}

func (a *claudeAIAdapter) Fingerprint() (string, error) {
	file := a.exportFile()
	if _, err := os.Stat(file); err != nil {
		return "", fmt.Errorf("Claude.ai export is missing %s", file)
	}
	fingerprint, err := pathFingerprint(file, func(string, os.DirEntry) bool { return true })
	return a.indexVersion() + ":" + fingerprint, err
}

type claudeAIConversation struct {
	UUID        string           `json:"uuid"`
	Name        string           `json:"name"`
	Summary     string           `json:"summary"`
	CreatedAt   string           `json:"created_at"`
	UpdatedAt   string           `json:"updated_at"`
	CurrentLeaf string           `json:"current_leaf_message_uuid"`
	Messages    []map[string]any `json:"chat_messages"`
}

func (a *claudeAIAdapter) Discover(emit func(WorkspaceRecord) error) error {
	file := a.exportFile()
	source, err := a.files().Open(file)
	if err != nil {
		return fmt.Errorf("cannot read Claude.ai export %s: %w", file, err)
	}
	defer source.Close()
	info, _ := source.Stat()
	version := info.ModTime().UnixNano()
	var content io.Reader = source
	if strings.EqualFold(filepath.Ext(file), ".zip") {
		if content, err = claudeAIZipConversations(source, info.Size()); err != nil {
			return fmt.Errorf("cannot read Claude.ai export %s: %w", file, err)
		}
	}
	// Conversations are decoded one at a time; an export can be large.
	decoder := json.NewDecoder(content)
	decoder.UseNumber()
	if token, err := decoder.Token(); err != nil || token != json.Delim('[') {
		return fmt.Errorf("Claude.ai export %s must contain an array", file)
	}
	for index := 0; decoder.More(); index++ {
		var conversation claudeAIConversation
		if err := decoder.Decode(&conversation); err != nil {
			return fmt.Errorf("Claude.ai export %s conversation %d: %w", file, index, err)
		}
		if conversation.UUID == "" {
			return fmt.Errorf("Claude.ai export %s conversation %d has no ID", file, index)
		}
		// An empty conversation is an empty list; a missing one is damage,
		// and would replace the conversation with nothing.
		if conversation.Messages == nil {
			return fmt.Errorf("Claude.ai export %s conversation %s has no messages list", file, conversation.UUID)
		}
		if err := emit(a.record(conversation, a.original(file), version)); err != nil {
			return err
		}
	}
	// More also stops at the end of the input or a read error, so only the
	// closing bracket shows the whole export was read.
	if token, err := decoder.Token(); err != nil || token != json.Delim(']') {
		return fmt.Errorf("Claude.ai export %s ends early", file)
	}
	if err := ensureEOF(decoder); err != nil {
		return fmt.Errorf("Claude.ai export %s: %w", file, err)
	}
	return nil
}

// claudeAIZipConversations opens conversations.json inside the export's ZIP.
// A capture may store the ZIP compressed, so it is read whole when the file
// is not available for random access.
func claudeAIZipConversations(source *sourceFile, size int64) (io.Reader, error) {
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
	archive, err := zip.NewReader(at, size)
	if err != nil {
		return nil, err
	}
	for _, entry := range archive.File {
		if entry.Name == "conversations.json" {
			return entry.Open()
		}
	}
	return nil, errors.New("the ZIP holds no conversations.json")
}

func (a *claudeAIAdapter) record(conversation claudeAIConversation, origin string, version int64) WorkspaceRecord {
	selected := claudeAISelected(conversation)
	messages := []MessageRecord{}
	toolNames, delegated := map[string]string{}, map[string]bool{}
	for _, message := range conversation.Messages {
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
			callID := firstString(block["id"], block["tool_use_id"])
			switch blockType := firstString(block["type"]); blockType {
			case "text":
				if text := firstString(block["text"]); strings.TrimSpace(text) != "" {
					item.Kind, item.Text = "message", text
					messages = append(messages, item)
					texts++
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
				name := defaultString(block["name"], "tool")
				toolNames[callID] = name
				item.Kind = "tool_call"
				if matchesFold(name, "task", "agent", "spawn_agent", "delegate", "workflow") {
					item.Kind = "delegation"
					delegated[callID] = true
				}
				item.CallID, item.Text = callID, jsonText(map[string]any{"tool": name, "input": mapValueDefault(block["input"])})
				messages = append(messages, item)
			case "tool_result":
				item.Role, item.Kind, item.CallID = "tool", "tool_result", callID
				if delegated[callID] {
					item.Kind = "delegation_result"
				}
				item.Text = jsonText(map[string]any{"tool": toolNames[callID], "content": block["content"], "is_error": block["is_error"]})
				messages = append(messages, item)
			case "injected_prompt_block":
				// Claude.ai adds the date, memory and tool instructions to the
				// person's turn; they are context, not what the person wrote.
				messages = append(messages, injectedContext(item, defaultString(block["injection_source"], "claude_ai"), firstString(block["prompt"])))
			case "document", "image":
				item.Kind = "attachment"
				item.Text = jsonText(map[string]any{"type": blockType, "title": block["title"], "file_uuid": block["file_uuid"]})
				messages = append(messages, item)
			}
		}
		// A message without text blocks keeps its flattened text.
		if text := firstString(message["text"]); texts == 0 && strings.TrimSpace(text) != "" {
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
			item.Text = jsonText(map[string]any{"type": "file_attachment", "file_name": attachment["file_name"], "file_type": attachment["file_type"], "file_size": attachment["file_size"], "extracted_content": attachment["extracted_content"]})
			messages = append(messages, item)
		}
		if files, _ := message["files"].([]any); len(files) > 0 {
			names := []string{}
			for _, raw := range files {
				names = append(names, firstString(mapValue(raw)["file_name"]))
			}
			item := base(":files")
			item.Kind, item.Text, item.RawText = "attachment", jsonText(map[string]any{"type": "files", "file_names": names}), jsonText(files)
			messages = append(messages, item)
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
	return WorkspaceRecord{Observed: version, SourceID: conversation.UUID, SourceKind: "claude-ai", Title: title, Account: a.config.Account, Purpose: purpose, Outcome: outcome, ActivityAt: updated, Metadata: map[string]any{"summary": conversation.Summary},
		Conversations: []ConversationRecord{{NativeID: conversation.UUID, Provider: "claude", Account: a.config.Account, Origin: "user-export", Harness: "claude-ai-export", Coverage: "complete", Messages: messages, StartedAt: created, EndedAt: updated, Observed: version}}}
}

// claudeAISelected marks the messages on the conversation's active branch:
// the path from its current leaf, or when the export names none from its
// newest leaf, to the root. Edited and regenerated turns are siblings.
func claudeAISelected(conversation claudeAIConversation) map[string]bool {
	parents, children := map[string]string{}, map[string]int{}
	for _, message := range conversation.Messages {
		id, parent := firstString(message["uuid"]), firstString(message["parent_message_uuid"])
		parents[id] = parent
		children[parent]++
	}
	leaf := conversation.CurrentLeaf
	if _, ok := parents[leaf]; !ok {
		leaf = ""
		newest := ""
		for _, message := range conversation.Messages {
			id, at := firstString(message["uuid"]), firstString(message["created_at"])
			if children[id] == 0 && at >= newest {
				leaf, newest = id, at
			}
		}
	}
	return claudeAIPath(parents, leaf)
}

func claudeAIPath(parents map[string]string, leaf string) map[string]bool {
	selected := map[string]bool{}
	for id := leaf; id != "" && !selected[id]; id = parents[id] {
		if _, ok := parents[id]; !ok {
			break
		}
		selected[id] = true
	}
	return selected
}
