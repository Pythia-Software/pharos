package archive

import (
	"strings"
	"testing"
)

const syntheticClaudeInit = `{"type":"system","subtype":"init","model":"synthetic-model","tools":["StructuredOutput"],"mcp_servers":[],"skills":[],"slash_commands":[],"agents":["claude","Explore","general-purpose","Plan"],"additional_directories":[],"permissionMode":"dontAsk","apiKeySource":"none","output_style":"default","plugins":[{"name":"cc-plugin-agents-md","path":"builtin","source":"cc-plugin-agents-md@builtin"},{"name":"cc-plugin-privacy","path":"builtin","source":"cc-plugin-privacy@builtin"},{"name":"cc-plugin-plugin-authoring","path":"builtin","source":"cc-plugin-plugin-authoring@builtin"}]}`

func TestTeamsClaudeInitRejectsAmbientCapabilities(t *testing.T) {
	if !teamsClaudeInitValid([]byte(syntheticClaudeInit), "synthetic-model") {
		t.Fatal("reviewed synthetic init rejected")
	}
	for _, bad := range []string{
		strings.Replace(syntheticClaudeInit, `"tools":["StructuredOutput"]`, `"tools":["StructuredOutput","Bash"]`, 1),
		strings.Replace(syntheticClaudeInit, `"mcp_servers":[]`, `"mcp_servers":[{"name":"other"}]`, 1),
		strings.Replace(syntheticClaudeInit, `"skills":[]`, `"skills":["ambient"]`, 1),
		strings.Replace(syntheticClaudeInit, `"apiKeySource":"none"`, `"apiKeySource":"environment"`, 1),
		strings.Replace(syntheticClaudeInit, `"path":"builtin"`, `"path":"/synthetic/unreviewed"`, 1),
		strings.Replace(syntheticClaudeInit, `"model":"synthetic-model"`, `"model":"synthetic-model","model":"other"`, 1),
	} {
		if teamsClaudeInitValid([]byte(bad), "synthetic-model") {
			t.Fatalf("unsafe init accepted: %s", bad)
		}
	}
}

func TestTeamsClaudeResultRequiresSuccessfulStructuredObject(t *testing.T) {
	good := `{"type":"result","subtype":"success","is_error":false,"permission_denials":[],"modelUsage":{"synthetic-model":{"provider":"firstParty","webSearchRequests":0}},"usage":{"server_tool_use":{"web_search_requests":0,"web_fetch_requests":0}},"subagent_stats":{"spawned":0},"structured_output":{"answer":"synthetic"}}`
	result, err := teamsClaudeResult([]byte(good), "synthetic-model")
	if err != nil || string(result) != `{"answer":"synthetic"}` {
		t.Fatalf("result %s %v", result, err)
	}
	for _, bad := range []string{
		strings.Replace(good, `"is_error":false`, `"is_error":true`, 1),
		strings.Replace(good, `"permission_denials":[]`, `"permission_denials":[{}]`, 1),
		strings.Replace(good, `"structured_output":{"answer":"synthetic"}`, `"structured_output":"synthetic"`, 1),
		strings.Replace(good, `"answer":"synthetic"`, `"answer":"synthetic","answer":"other"`, 1),
	} {
		if _, err = teamsClaudeResult([]byte(bad), "synthetic-model"); err == nil {
			t.Fatalf("unsafe result accepted: %s", bad)
		}
	}
}

func TestTeamsClaudeAcceptsOnlyReviewedSystemNotices(t *testing.T) {
	background := `{"type":"system","subtype":"background_tasks_changed","tasks":[],"uuid":"synthetic-event","session_id":"synthetic-session"}`
	if !teamsClaudeSystemNotice([]byte(background), "background_tasks_changed") || teamsClaudeSystemNotice([]byte(strings.Replace(background, `"tasks":[]`, `"tasks":[{}]`, 1)), "background_tasks_changed") {
		t.Fatal("background task notice was not fail closed")
	}
	ui := `{"type":"system","subtype":"ui_invalidate","event":"ui.render","uuid":"synthetic-id","session_id":"synthetic-session"}`
	thinking := `{"type":"system","subtype":"thinking_tokens","estimated_tokens":50,"estimated_tokens_delta":10,"uuid":"synthetic-id","session_id":"synthetic-session"}`
	if !teamsClaudeSystemNotice([]byte(ui), "ui_invalidate") || !teamsClaudeSystemNotice([]byte(thinking), "thinking_tokens") {
		t.Fatal("reviewed notice rejected")
	}
	if teamsClaudeSystemNotice([]byte(strings.Replace(ui, `"event":"ui.render"`, `"event":"execute"`, 1)), "ui_invalidate") || teamsClaudeSystemNotice([]byte(strings.Replace(thinking, `"estimated_tokens":50`, `"estimated_tokens":-1`, 1)), "thinking_tokens") {
		t.Fatal("unreviewed notice accepted")
	}
}

func TestTeamsClaudeRateLimitNoticeIsStrict(t *testing.T) {
	good := `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","resetsAt":10,"rateLimitType":"seven_day","overageStatus":"rejected","overageDisabledReason":"org_level_disabled","isUsingOverage":false,"unifiedWindows":{"seven_day":{"utilization":0.63,"resetsAt":10,"status":"allowed"}}},"uuid":"synthetic","session_id":"synthetic"}`
	if !teamsClaudeRateLimitNotice([]byte(good)) {
		t.Fatal("reviewed rate limit notice rejected")
	}
	if teamsClaudeRateLimitNotice([]byte(strings.Replace(good, `"status":"allowed"`, `"status":"denied"`, 1))) || teamsClaudeRateLimitNotice([]byte(strings.Replace(good, `"resetsAt":10`, `"resetsAt":10,"routing":"unknown"`, 1))) {
		t.Fatal("unsafe notice accepted")
	}
}
