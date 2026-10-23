package archive

import (
	"encoding/json"
	"strings"
)

// normalizeHandbackMessages exposes an acknowledged SubagentHandback report as
// the child's final assistant message. The original call and result remain in
// place for tool reads and accounting. The native ID ties the derived message
// to its call so repeated normalization cannot create another copy.
func normalizeHandbackMessages(messages []MessageRecord) []MessageRecord {
	known := make(map[string]bool, len(messages))
	for _, message := range messages {
		known[message.NativeID] = true
	}
	pending := make(map[string]MessageRecord)
	var normalized []MessageRecord
	for _, message := range messages {
		normalized = append(normalized, message)
		if message.Kind == "tool_call" && message.Role == "assistant" && message.CallID != "" && message.NativeID != "" {
			delete(pending, message.CallID)
			if report, ok := handbackReport(message.Text); ok {
				candidate := message
				candidate.Text = report
				pending[message.CallID] = candidate
			}
			continue
		}
		if message.Kind != "tool_result" || message.CallID == "" {
			continue
		}
		call, ok := pending[message.CallID]
		delete(pending, message.CallID)
		if !ok || !successfulHandbackResult(message.Text) {
			continue
		}
		id := call.NativeID + ":handback"
		if known[id] {
			continue
		}
		known[id] = true
		normalized = append(normalized, MessageRecord{
			NativeID: id, Role: "assistant", Kind: "message", Text: call.Text,
			Model: call.Model, Effort: call.Effort, CreatedAt: message.CreatedAt,
			ParentNativeID: call.ParentNativeID, PreviousNativeID: call.PreviousNativeID,
			EvidenceLocator: call.EvidenceLocator + ":input.message", Selected: call.Selected,
		})
	}
	return normalized
}

func handbackReport(text string) (string, bool) {
	var call struct {
		Tool  string `json:"tool"`
		Input struct {
			Message string `json:"message"`
		} `json:"input"`
	}
	if json.Unmarshal([]byte(text), &call) != nil || call.Tool != "SubagentHandback" || strings.TrimSpace(call.Input.Message) == "" {
		return "", false
	}
	return call.Input.Message, true
}

func successfulHandbackResult(text string) bool {
	var result struct {
		Content json.RawMessage `json:"content"`
		IsError bool            `json:"is_error"`
	}
	if json.Unmarshal([]byte(text), &result) != nil || result.IsError || len(result.Content) == 0 || string(result.Content) == "null" {
		return false
	}
	var content string
	if json.Unmarshal(result.Content, &content) == nil {
		return handbackAcknowledged(content)
	}
	var blocks []any
	if json.Unmarshal(result.Content, &blocks) != nil {
		return false
	}
	acknowledged := false
	for _, raw := range blocks {
		var content string
		switch block := raw.(type) {
		case string:
			content = block
		case map[string]any:
			if block["is_error"] == true || firstString(block["type"]) == "error" {
				return false
			}
			content = firstString(block["text"], block["content"])
		}
		if content != "" {
			if !handbackAcknowledged(content) {
				return false
			}
			acknowledged = true
		}
	}
	return acknowledged
}

func handbackAcknowledged(content string) bool {
	var result map[string]any
	if json.Unmarshal([]byte(content), &result) == nil && result != nil {
		return result["success"] == true && result["error"] == nil && result["is_error"] != true
	}
	// Older acknowledgements used plain text. Accept the known positive
	// responses, rather than any sentence containing "delivered" (including
	// "undelivered" or a report quoting a failed delivery).
	switch strings.ToLower(strings.TrimSpace(content)) {
	case "report delivered to your caller.", "report delivered successfully.":
		return true
	}
	return false
}
