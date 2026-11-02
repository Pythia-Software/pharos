package archive

import (
	"encoding/json"
	"strconv"
)

// Progress contains only Pharos-authored labels. Never forward CLI text,
// reasoning, tool arguments, results, or diagnostics into the activity feed.
type askProgress struct {
	Message   string `json:"message"`
	ElapsedMs int64  `json:"elapsedMs"`
}

const askActivityLimit = 40

func (p askProvider) reportProgress(message string) {
	if p.progress != nil {
		p.progress(message)
	}
}

func askToolActivity(tool string) string {
	switch tool {
	case "browse_site":
		return "Browsing available views"
	case "site_link":
		return "Checking a destination link"
	case "list_repositories":
		return "Reading repositories"
	case "search_conversations", "search_work":
		return "Searching archived work"
	case "list_metrics", "query_statistics":
		return "Reading usage statistics"
	default:
		return "Reading Pharos data"
	}
}

type askCLIEvents struct {
	provider askProvider
	calls    map[string]string
}

func (e *askCLIEvents) tool(id, name string, done, failed bool) {
	if e.calls == nil {
		e.calls = map[string]string{}
	}
	if done {
		if previous := e.calls[id]; previous != "" {
			name = previous
		}
		delete(e.calls, id)
	} else {
		if previous, exists := e.calls[id]; exists && (previous == name || name == "") {
			return // Claude repeats partial tool starts in its full message.
		}
		// Bound metadata even if the CLI sends an unexpected transcript.
		if len(e.calls) >= askActivityLimit {
			return
		}
		e.calls[id] = name
	}
	message := askToolActivity(name)
	if failed {
		message += " — failed"
	} else if done {
		message += " — done"
	} else {
		message += "…"
	}
	e.provider.reportProgress(message)
}

func (e *askCLIEvents) read(raw []byte) {
	if e.provider.ID == "Antigravity" {
		e.readAGY(raw)
		return
	}
	var event struct {
		Type, Subtype string
		Item          struct{ ID, Type, Server, Tool, Status string }
		Event         struct {
			Type         string
			ContentBlock struct{ Type, ID, Name string } `json:"content_block"`
		}
		Message struct {
			Content []struct {
				Type, ID, Name string
				ToolUseID      string `json:"tool_use_id"`
				IsError        bool   `json:"is_error"`
			}
		}
	}
	if json.Unmarshal(raw, &event) != nil {
		return
	}
	p := e.provider
	if p.ID == "Codex" {
		switch event.Type {
		case "thread.started":
			p.reportProgress("Connected to Codex…")
		case "turn.started":
			p.reportProgress("Processing your question…")
		case "item.started", "item.completed":
			item := event.Item
			switch item.Type {
			case "mcp_tool_call":
				if item.Server == "pharos" {
					e.tool(item.ID, item.Tool, event.Type == "item.completed", item.Status == "failed")
				}
			case "reasoning":
				p.reportProgress("Thinking…")
			case "agent_message":
				p.reportProgress("Preparing the response…")
			}
		}
		return
	}
	if p.ID != "Claude" {
		return
	}
	toolStart := func(id, name string) {
		const prefix = "mcp__pharos__"
		if len(name) > len(prefix) && name[:len(prefix)] == prefix {
			e.tool(id, name[len(prefix):], false, false)
		}
	}
	switch event.Type {
	case "system":
		if event.Subtype == "init" {
			p.reportProgress("Connected to Claude…")
		} else if event.Subtype == "api_retry" {
			p.reportProgress("Claude is retrying the connection…")
		}
	case "stream_event":
		if event.Event.Type == "content_block_start" {
			block := event.Event.ContentBlock
			switch block.Type {
			case "tool_use":
				toolStart(block.ID, block.Name)
			case "thinking":
				p.reportProgress("Thinking…")
			case "text":
				p.reportProgress("Preparing the response…")
			}
		}
	case "assistant", "user":
		for _, block := range event.Message.Content {
			if block.Type == "tool_use" {
				toolStart(block.ID, block.Name)
			} else if block.Type == "tool_result" && e.calls[block.ToolUseID] != "" {
				e.tool(block.ToolUseID, "", true, block.IsError)
			}
		}
	}
}

// AGY uses step_update instead of the other CLIs' item/message events. The
// outer tool is call_mcp_tool; its routing parameters identify the Pharos tool.
// All other arguments, tool output and text deltas stay outside the UI feed.
func (e *askCLIEvents) readAGY(raw []byte) {
	var event struct {
		Event string
		Step  struct {
			Index    *int   `json:"step_index"`
			Type     string `json:"step_type"`
			State    string
			ToolName string `json:"tool_name"`
			ToolInfo struct {
				Name       string
				Parameters struct{ ServerName, ToolName string }
				Error      json.RawMessage
			} `json:"tool_info"`
		} `json:"step_update"`
	}
	if json.Unmarshal(raw, &event) != nil || event.Event != "step_update" {
		return
	}
	step := event.Step
	if step.State != "ACTIVE" && step.State != "DONE" {
		return
	}
	switch step.Type {
	case "user_input":
		e.provider.reportProgress("Processing your question…")
	case "agent_response":
		e.provider.reportProgress("Preparing the response…")
	case "checkpoint":
		e.provider.reportProgress("Finalizing the response…")
	case "tool":
		if step.Index == nil || *step.Index < 0 {
			return
		}
		tool := step.ToolName
		if tool == "" {
			tool = step.ToolInfo.Name
		}
		if tool == "finish" {
			e.provider.reportProgress("Preparing the response…")
			return
		}
		if tool != "call_mcp_tool" {
			return
		}
		route := step.ToolInfo.Parameters
		if route.ServerName != "" && route.ServerName != "pharos-ask" {
			return
		}
		failed := len(step.ToolInfo.Error) > 0 && string(step.ToolInfo.Error) != "null"
		e.tool(strconv.Itoa(*step.Index), route.ToolName, step.State == "DONE", failed)
	}
}
