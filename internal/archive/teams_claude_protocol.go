package archive

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"

	"github.com/gbdubs/pharos/internal/teams"
)

var errTeamsClaudeIsolation = errors.New("Claude's effective settings or tool isolation could not be verified")

// teamsClaudeInitValid is defense in depth after a pre-disclosure control
// handshake. Init arrives only after launch and cannot by itself prove that no
// managed hook or routing override ran before the prompt was submitted.
func teamsClaudeInitValid(raw []byte, model string) bool {
	if !teams.ValidUniqueJSON(raw) {
		return false
	}
	var init struct {
		Type, Subtype, Model  string
		Tools                 []string          `json:"tools"`
		MCPServers            []json.RawMessage `json:"mcp_servers"`
		Skills                []string          `json:"skills"`
		SlashCommands         []string          `json:"slash_commands"`
		Agents                []string          `json:"agents"`
		AdditionalDirectories []string          `json:"additional_directories"`
		PermissionMode        string            `json:"permissionMode"`
		APIKeySource          string            `json:"apiKeySource"`
		OutputStyle           string            `json:"output_style"`
		Plugins               []struct{ Name, Path, Source string }
	}
	if json.Unmarshal(raw, &init) != nil || init.Type != "system" || init.Subtype != "init" || init.Model != model || init.PermissionMode != "dontAsk" || init.APIKeySource != "none" || init.OutputStyle != "default" {
		return false
	}
	if !slices.Equal(init.Tools, []string{"StructuredOutput"}) || init.MCPServers == nil || len(init.MCPServers) != 0 || init.Skills == nil || len(init.Skills) != 0 || init.SlashCommands == nil || len(init.SlashCommands) != 0 || init.AdditionalDirectories == nil || len(init.AdditionalDirectories) != 0 {
		return false
	}
	agents := slices.Clone(init.Agents)
	slices.Sort(agents)
	if !slices.Equal(agents, []string{"Explore", "Plan", "claude", "general-purpose"}) {
		return false
	}
	plugins := map[string]bool{"cc-plugin-agents-md": false, "cc-plugin-privacy": false, "cc-plugin-plugin-authoring": false}
	if len(init.Plugins) != len(plugins) {
		return false
	}
	for _, p := range init.Plugins {
		seen, ok := plugins[p.Name]
		if !ok || seen || p.Path != "builtin" || p.Source != p.Name+"@builtin" {
			return false
		}
		plugins[p.Name] = true
	}
	return true
}

// A successful terminal event must contain exactly one structured object;
// the ordinary Ask fallbacks to result text are intentionally unavailable.
// A separate bounded schema validator must still validate the object before
// it can be persisted, reviewed or released.
func teamsClaudeResult(raw []byte, model string) (json.RawMessage, error) {
	if len(raw) > 2<<20 || !teams.ValidUniqueJSON(raw) {
		return nil, errTeamsClaudeIsolation
	}
	var result struct {
		Type, Subtype string
		IsError       *bool           `json:"is_error"`
		Structured    json.RawMessage `json:"structured_output"`
		Denials       json.RawMessage `json:"permission_denials"`
		ModelUsage    map[string]struct {
			Provider          string `json:"provider"`
			WebSearchRequests *int   `json:"webSearchRequests"`
		} `json:"modelUsage"`
		Usage *struct {
			ServerToolUse *struct {
				Search *int `json:"web_search_requests"`
				Fetch  *int `json:"web_fetch_requests"`
			} `json:"server_tool_use"`
		} `json:"usage"`
		Subagents *struct {
			Spawned *int `json:"spawned"`
		} `json:"subagent_stats"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Type != "result" || result.Subtype != "success" || result.IsError == nil || *result.IsError {
		return nil, errTeamsClaudeIsolation
	}
	var denials []json.RawMessage
	if json.Unmarshal(result.Denials, &denials) != nil || denials == nil || len(denials) != 0 {
		return nil, errTeamsClaudeIsolation
	}
	usage, ok := result.ModelUsage[model]
	if !ok || len(result.ModelUsage) != 1 || usage.Provider != "firstParty" || usage.WebSearchRequests == nil || *usage.WebSearchRequests != 0 || result.Usage == nil || result.Usage.ServerToolUse == nil || result.Usage.ServerToolUse.Search == nil || *result.Usage.ServerToolUse.Search != 0 || result.Usage.ServerToolUse.Fetch == nil || *result.Usage.ServerToolUse.Fetch != 0 || result.Subagents == nil || result.Subagents.Spawned == nil || *result.Subagents.Spawned != 0 {
		return nil, errTeamsClaudeIsolation
	}
	value := bytes.TrimSpace(result.Structured)
	if len(value) == 0 || len(value) > 128<<10 || value[0] != '{' || !teams.ValidUniqueJSON(value) {
		return nil, errTeamsClaudeIsolation
	}
	return bytes.Clone(value), nil
}

func teamsClaudeIntermediate(raw []byte, eventType string, expectedID string) (string, json.RawMessage, bool) {
	if !teams.ValidUniqueJSON(raw) {
		return "", nil, false
	}
	var event struct {
		Type    string `json:"type"`
		Message struct {
			Role    string            `json:"role"`
			Content []json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(raw, &event) != nil || event.Type != eventType || event.Message.Role != eventType || event.Message.Content == nil {
		return "", nil, false
	}
	var foundID string
	var foundInput json.RawMessage
	for _, rawContent := range event.Message.Content {
		var part struct {
			Type      string          `json:"type"`
			Name      string          `json:"name"`
			ID        string          `json:"id"`
			ToolUseID string          `json:"tool_use_id"`
			Input     json.RawMessage `json:"input"`
		}
		if json.Unmarshal(rawContent, &part) != nil {
			return "", nil, false
		}
		if eventType == "assistant" {
			switch part.Type {
			case "text", "thinking":
			case "tool_use":
				if part.Name != "StructuredOutput" || part.ID == "" || len(part.ID) > 256 || expectedID != "" || foundID != "" || len(part.Input) == 0 {
					return "", nil, false
				}
				foundID, foundInput = part.ID, bytes.Clone(part.Input)
			default:
				return "", nil, false
			}
		} else if eventType == "user" {
			if part.Type != "tool_result" || expectedID == "" || part.ToolUseID != expectedID {
				return "", nil, false
			}
		} else {
			return "", nil, false
		}
	}
	if eventType == "user" && len(event.Message.Content) != 1 {
		return "", nil, false
	}
	return foundID, foundInput, true
}

func teamsClaudeSameObject(a, b []byte) bool {
	if !teams.ValidUniqueJSON(a) || !teams.ValidUniqueJSON(b) {
		return false
	}
	canonical := func(raw []byte) []byte {
		var value any
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		if d.Decode(&value) != nil {
			return nil
		}
		if _, ok := value.(map[string]any); !ok {
			return nil
		}
		out, _ := json.Marshal(value)
		return out
	}
	x, y := canonical(a), canonical(b)
	return x != nil && bytes.Equal(x, y)
}

// These two observed notifications carry display/progress metadata only.
// Unknown system events cannot be silently accepted across the boundary.
func teamsClaudeSystemNotice(raw []byte, subtype string) bool {
	allowed := map[string]bool{"type": true, "subtype": true, "uuid": true, "session_id": true}
	switch subtype {
	case "ui_invalidate":
		allowed["event"] = true
	case "thinking_tokens":
		allowed["estimated_tokens"] = true
		allowed["estimated_tokens_delta"] = true
	default:
		return false
	}
	v, e := teamsStrictObject(raw, allowed)
	if e != nil || len(v) != len(allowed) || string(v["type"]) != `"system"` {
		return false
	}
	var actual, id, session string
	if json.Unmarshal(v["subtype"], &actual) != nil || actual != subtype || json.Unmarshal(v["uuid"], &id) != nil || id == "" || len(id) > 64 || json.Unmarshal(v["session_id"], &session) != nil || session == "" || len(session) > 64 {
		return false
	}
	if subtype == "ui_invalidate" {
		return string(v["event"]) == `"ui.render"`
	}
	for _, key := range []string{"estimated_tokens", "estimated_tokens_delta"} {
		var n int
		if json.Unmarshal(v[key], &n) != nil || n < 0 || n > 100000000 {
			return false
		}
	}
	return true
}

func teamsClaudeRateLimitNotice(raw []byte) bool {
	v, e := teamsStrictObject(raw, map[string]bool{"type": true, "rate_limit_info": true, "uuid": true, "session_id": true})
	if e != nil || !teamsExactKeys(v, "type", "rate_limit_info", "uuid", "session_id") || string(v["type"]) != `"rate_limit_event"` {
		return false
	}
	var id, session string
	if json.Unmarshal(v["uuid"], &id) != nil || id == "" || len(id) > 64 || json.Unmarshal(v["session_id"], &session) != nil || session == "" || len(session) > 64 {
		return false
	}
	info, e := teamsStrictObject(v["rate_limit_info"], map[string]bool{"status": true, "resetsAt": true, "rateLimitType": true, "overageStatus": true, "overageDisabledReason": true, "isUsingOverage": true, "unifiedWindows": true})
	if e != nil || string(info["status"]) != `"allowed"` {
		return false
	}
	for _, key := range []string{"rateLimitType", "overageStatus", "overageDisabledReason"} {
		if value, ok := info[key]; ok {
			var s string
			if json.Unmarshal(value, &s) != nil || len(s) > 128 {
				return false
			}
		}
	}
	if value, ok := info["isUsingOverage"]; ok && string(value) != "true" && string(value) != "false" {
		return false
	}
	if value, ok := info["resetsAt"]; ok {
		var n int64
		if json.Unmarshal(value, &n) != nil || n < 0 {
			return false
		}
	}
	if value, ok := info["unifiedWindows"]; ok {
		var windows map[string]json.RawMessage
		if json.Unmarshal(value, &windows) != nil || windows == nil || len(windows) > 8 {
			return false
		}
		for name, rawWindow := range windows {
			if len(name) == 0 || len(name) > 64 {
				return false
			}
			window, e := teamsStrictObject(rawWindow, map[string]bool{"utilization": true, "resetsAt": true, "status": true})
			if e != nil || !teamsExactKeys(window, "utilization", "resetsAt", "status") || string(window["status"]) != `"allowed"` {
				return false
			}
			var utilization float64
			var reset int64
			if json.Unmarshal(window["utilization"], &utilization) != nil || utilization < 0 || utilization > 1 || json.Unmarshal(window["resetsAt"], &reset) != nil || reset < 0 {
				return false
			}
		}
	}
	return true
}
