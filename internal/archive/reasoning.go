package archive

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

const reasoningExtractionVersion = "1"

func reasoningList(value any) []any {
	items, _ := value.([]any)
	return items
}

// reasoningBlock reads only protocol-defined reasoning fields. It never walks
// arbitrary JSON: a tool result or a quoted user payload is not agent thought.
func reasoningBlock(block map[string]any) (text, format, unavailable string) {
	switch firstString(block["type"]) {
	case "thinking":
		text, format = firstString(block["thinking"], block["text"]), "text"
	case "reasoning_text", "analysis":
		text, format = firstString(block["text"]), "text"
	case "summary_text":
		text, format = firstString(block["text"]), "summary"
	case "agent_reasoning":
		text, format = firstString(block["text"], block["message"]), "summary"
	case "agent_reasoning_raw_content":
		text, format = firstString(block["text"], block["message"]), "text"
	case "redacted_thinking":
		return "", "", "redacted"
	case "reasoning":
		for _, field := range []struct{ key, format string }{{"content", "text"}, {"summary", "summary"}} {
			parts := []string{}
			format := field.format
			for _, value := range reasoningList(block[field.key]) {
				part := mapValue(value)
				if matchesFold(firstString(part["type"]), "reasoning_text", "text", "summary_text") {
					if s := firstString(part["text"]); strings.TrimSpace(s) != "" {
						parts = append(parts, s)
						if firstString(part["type"]) == "summary_text" {
							format = "summary"
						}
					}
				}
			}
			if len(parts) > 0 {
				return strings.Join(parts, "\n\n"), format, ""
			}
		}
		if text := firstString(block["text"]); strings.TrimSpace(text) != "" {
			// Codex exec exposes a reasoning summary as a completed text item.
			return text, "summary", ""
		}
		if firstString(block["encrypted_content"]) != "" {
			return "", "", "encrypted"
		}
		format = "text"
	default:
		return "", "", ""
	}
	if strings.TrimSpace(text) == "" {
		return "", "", "empty"
	}
	return text, format, ""
}

func reasoningRecord(base MessageRecord, text, format, unavailable string) MessageRecord {
	base.accounting = nil
	base.Role, base.Kind, base.Text, base.ReasoningFormat = "assistant", "reasoning", text, format
	if unavailable != "" {
		base.Kind = "metadata"
		base.ReasoningFormat = ""
		base.Text = jsonText(map[string]any{"type": "reasoning_unavailable", "reason": unavailable})
	}
	return base
}

// normalizeReasoningMessage leaves ordinary prose alone, including prose that
// happens to quote JSON. Only assistant protocol envelopes are split. The first
// resulting record keeps the old ID and the accounting envelope; later blocks
// receive deterministic IDs and no extra copy of the request's usage.
func normalizeReasoningMessage(message MessageRecord) []MessageRecord {
	if matchesFold(message.Kind, "thinking", "analysis", "reasoning") {
		if strings.TrimSpace(message.Text) == "" {
			return []MessageRecord{reasoningRecord(message, "", "", "empty")}
		}
		message.Kind = "reasoning"
		if message.ReasoningFormat == "" {
			message.ReasoningFormat = "text"
		}
		return []MessageRecord{message}
	}
	if message.Role != "assistant" || (message.Kind != "metadata" && message.Kind != "message" && message.Kind != "") {
		return []MessageRecord{message}
	}
	// A normal reply can itself contain JSON examples. Only a retained source
	// envelope (metadata, or a body explicitly backed by the same raw event)
	// establishes that its thinking fields came from the provider protocol.
	if message.Kind != "metadata" && message.RawText != message.Text {
		return []MessageRecord{message}
	}
	var outer map[string]any
	decoder := json.NewDecoder(strings.NewReader(message.Text))
	decoder.UseNumber()
	if decoder.Decode(&outer) != nil || outer == nil {
		return []MessageRecord{message}
	}
	value := outer
	if matchesFold(firstString(value["type"]), "response_item", "event_msg") {
		value = mapValueDefault(value["payload"])
	}
	if text, format, unavailable := reasoningBlock(value); format != "" || unavailable != "" {
		item := reasoningRecord(message, text, format, unavailable)
		item.RawText = defaultString(message.RawText, message.Text)
		return []MessageRecord{item}
	}
	body := mapValue(value["message"])
	if body == nil {
		body = value
	}
	role := firstString(body["role"], value["role"])
	if role != "assistant" && !(role == "" && firstString(value["type"]) == "assistant") {
		return []MessageRecord{message}
	}
	if firstString(body["channel"], value["channel"]) == "analysis" {
		text := messageText(body["content"])
		item := reasoningRecord(message, text, "text", "")
		if strings.TrimSpace(text) == "" {
			item = reasoningRecord(message, "", "", "empty")
		}
		item.RawText = defaultString(message.RawText, message.Text)
		return []MessageRecord{item}
	}
	blocks := reasoningList(body["content"])
	found := false
	for _, raw := range blocks {
		_, format, unavailable := reasoningBlock(mapValue(raw))
		found = found || format != "" || unavailable != ""
	}
	if !found {
		return []MessageRecord{message}
	}
	result := []MessageRecord{}
	for index, raw := range blocks {
		block := mapValue(raw)
		item := message
		item.accounting, item.RawText, item.ReasoningFormat = nil, "", ""
		item.NativeID = fmt.Sprintf("%s:reasoning-block:%d", message.NativeID, index)
		if len(blocks) > 1 {
			item.EvidenceLocator = fmt.Sprintf("%s:block:%d", message.EvidenceLocator, index)
		}
		if text, format, unavailable := reasoningBlock(block); format != "" || unavailable != "" {
			item = reasoningRecord(item, text, format, unavailable)
		} else {
			switch firstString(block["type"]) {
			case "text", "input_text", "output_text":
				item.Kind, item.Text = "message", firstString(block["text"])
			case "tool_use":
				name := firstString(block["name"])
				item.Kind, item.CallID = "tool_call", firstString(block["id"])
				if matchesFold(name, "task", "agent", "spawn_agent", "delegate", "workflow") {
					item.Kind = "delegation"
				}
				item.Text = jsonText(map[string]any{"tool": name, "input": block["input"]})
			case "tool_result":
				item.Role, item.Kind, item.CallID = "tool", "tool_result", firstString(block["tool_use_id"])
				item.Text = jsonText(map[string]any{"content": block["content"], "is_error": block["is_error"]})
			default:
				item.Kind, item.Text = "metadata", jsonText(raw)
			}
		}
		if len(result) == 0 {
			item.NativeID = message.NativeID
			item.RawText = defaultString(message.RawText, message.Text)
		}
		result = append(result, item)
	}
	return result
}

func normalizeReasoningMessages(messages []MessageRecord) []MessageRecord {
	result := make([]MessageRecord, 0, len(messages))
	for _, message := range messages {
		result = append(result, normalizeReasoningMessage(message)...)
	}
	return recoverRawReasoning(result)
}

func reasoningBlockBase(value string) string {
	if at := strings.LastIndex(value, ":block:"); at >= 0 {
		if _, err := strconv.Atoi(value[at+len(":block:"):]); err == nil {
			return value[:at]
		}
	}
	return value
}

// Older adapters sometimes retained thinking only in raw_text on a reply or
// accounting row. Recover those blocks without re-emitting the reply/tools or
// copying their usage. Native block IDs make a later source reindex agree with
// this recovery; existing canonical blocks always win over the envelope copy.
func recoverRawReasoning(messages []MessageRecord) []MessageRecord {
	known := map[string]bool{}
	positions := map[string]int{}
	type siblingPosition struct{ position, block int }
	groups := map[string][]siblingPosition{}
	for position, message := range messages {
		known[message.NativeID] = true
		positions[message.NativeID] = position
		base := reasoningBlockBase(message.NativeID)
		if base != message.NativeID {
			index, _ := strconv.Atoi(strings.TrimPrefix(message.NativeID, base+":block:"))
			groups[base] = append(groups[base], siblingPosition{position, index})
		}
	}
	before := map[int][]MessageRecord{}
	for position, message := range messages {
		if message.RawText == "" || message.Kind == "reasoning" || message.Role == "user" || message.Role == "tool" ||
			message.Kind == "tool_result" || message.Kind == "delegation_result" || message.Kind == "context" {
			continue
		}
		var value map[string]any
		decoder := json.NewDecoder(strings.NewReader(message.RawText))
		decoder.UseNumber()
		if decoder.Decode(&value) != nil {
			continue
		}
		if matchesFold(firstString(value["type"]), "response_item", "event_msg") {
			value = mapValueDefault(value["payload"])
		}
		if message.Kind == "metadata" && matchesFold(firstString(value["type"]), "reasoning", "agent_reasoning", "agent_reasoning_raw_content") {
			text, format, unavailable := reasoningBlock(value)
			messages[position] = reasoningRecord(message, text, format, unavailable)
			continue
		}
		body := mapValue(value["message"])
		if body == nil {
			body = value
		}
		role := firstString(body["role"])
		if role != "assistant" && !(role == "" && firstString(value["type"]) == "assistant") {
			continue
		}
		if firstString(body["channel"]) == "analysis" {
			if message.Kind == "message" && message.Text == messageText(body["content"]) {
				messages[position].Kind, messages[position].ReasoningFormat = "reasoning", "text"
				continue
			}
			if message.Kind == "metadata" {
				text := messageText(body["content"])
				unavailable := ""
				if strings.TrimSpace(text) == "" {
					unavailable = "empty"
				}
				messages[position] = reasoningRecord(message, text, "text", unavailable)
				continue
			}
		}
		blocks := reasoningList(body["content"])
		base := reasoningBlockBase(strings.TrimSuffix(message.NativeID, ":usage"))
		firstReply := len(blocks)
		for i, raw := range blocks {
			if matchesFold(firstString(mapValue(raw)["type"]), "text", "input_text", "output_text") {
				firstReply = i
				break
			}
		}
		siblings := append([]siblingPosition(nil), groups[base]...)
		if position, ok := positions[base]; ok {
			siblings = append(siblings, siblingPosition{position, firstReply})
		}
		for index, raw := range blocks {
			text, format, unavailable := reasoningBlock(mapValue(raw))
			if format == "" && unavailable == "" {
				continue
			}
			nativeID := fmt.Sprintf("%s:block:%d", base, index)
			if known[nativeID] || known[fmt.Sprintf("%s:reasoning-block:%d", base, index)] {
				continue
			}
			// The first normalized block retains the event's unsuffixed ID.
			// Match its unavailable marker, since an old reply with that same
			// ID can legitimately be missing the first reasoning block.
			if first, ok := positions[base]; ok && index == 0 && unavailable != "" &&
				messages[first].Kind == "metadata" && messages[first].Text == reasoningRecord(message, text, format, unavailable).Text {
				continue
			}
			item := reasoningRecord(message, text, format, unavailable)
			item.NativeID, item.RawText = nativeID, ""
			item.EvidenceLocator = fmt.Sprintf("%s:block:%d", reasoningBlockBase(message.EvidenceLocator), index)
			// Place the recovered block among its event's existing siblings.
			slot, last, next := position, -1, len(messages)+1
			for _, sibling := range siblings {
				last = max(last, sibling.position)
				if sibling.block > index {
					next = min(next, sibling.position)
				}
			}
			if next <= len(messages) {
				slot = next
			} else if last >= 0 {
				slot = last + 1
			}
			before[slot] = append(before[slot], item)
			known[nativeID] = true
		}
	}
	if len(before) == 0 {
		return messages
	}
	result := make([]MessageRecord, 0, len(messages)+len(before))
	for i, message := range messages {
		result = append(result, before[i]...)
		result = append(result, message)
	}
	return append(result, before[len(messages)]...)
}

func normalizeRecordReasoning(record WorkspaceRecord) WorkspaceRecord {
	// Do not mutate the adapter's slices: source digests and callers may reuse it.
	record.Conversations = append([]ConversationRecord(nil), record.Conversations...)
	for i := range record.Conversations {
		record.Conversations[i].Messages = normalizeHandbackMessages(normalizeReasoningMessages(record.Conversations[i].Messages))
	}
	return record
}
