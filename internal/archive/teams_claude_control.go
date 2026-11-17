package archive

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"slices"

	"github.com/gbdubs/pharos/internal/teams"
)

// The control handshake is sent on the same process before any source-derived
// prompt. Every known field that can change routing, hooks or loaded context
// is fail-closed; a new CLI surface requires review before it can be used.
func teamsClaudeControlPayload(raw []byte, requestID string) (map[string]json.RawMessage, bool) {
	if len(raw) > 2<<20 || !teams.ValidUniqueJSON(raw) {
		return nil, false
	}
	top, e := teamsStrictObject(raw, map[string]bool{"type": true, "response": true})
	if e != nil || string(top["type"]) != `"control_response"` {
		return nil, false
	}
	r, e := teamsStrictObject(top["response"], map[string]bool{"subtype": true, "request_id": true, "response": true, "pending_permission_requests": true, "pending_user_dialog_requests": true})
	if e != nil || string(r["subtype"]) != `"success"` {
		return nil, false
	}
	var id string
	if json.Unmarshal(r["request_id"], &id) != nil || id != requestID {
		return nil, false
	}
	for _, k := range []string{"pending_permission_requests", "pending_user_dialog_requests"} {
		if b, ok := r[k]; ok && !bytes.Equal(b, []byte("[]")) {
			return nil, false
		}
	}
	var data map[string]json.RawMessage
	if json.Unmarshal(r["response"], &data) != nil || data == nil {
		return nil, false
	}
	return data, true
}

func teamsExactKeys(v map[string]json.RawMessage, keys ...string) bool {
	if len(v) != len(keys) {
		return false
	}
	for _, key := range keys {
		if _, ok := v[key]; !ok {
			return false
		}
	}
	return true
}

func teamsClaudeSafetySettings(raw json.RawMessage) bool {
	v, e := teamsStrictObject(raw, map[string]bool{"disableClaudeAiConnectors": true, "disableBundledSkills": true, "disableAllHooks": true, "disableWorkflows": true, "disableSkillShellExecution": true, "autoMemoryEnabled": true})
	if e != nil || !teamsExactKeys(v, "disableClaudeAiConnectors", "disableBundledSkills", "disableAllHooks", "disableWorkflows", "disableSkillShellExecution", "autoMemoryEnabled") {
		return false
	}
	for _, k := range []string{"disableClaudeAiConnectors", "disableBundledSkills", "disableAllHooks", "disableWorkflows", "disableSkillShellExecution"} {
		if string(v[k]) != "true" {
			return false
		}
	}
	return string(v["autoMemoryEnabled"]) == "false"
}

func teamsClaudeSettingsValid(raw []byte, requestID, model, effort string) bool {
	p, ok := teamsClaudeControlPayload(raw, requestID)
	if !ok || !teamsExactKeys(p, "effective", "sources", "applied") || !teamsClaudeSafetySettings(p["effective"]) {
		return false
	}
	var sources []json.RawMessage
	if json.Unmarshal(p["sources"], &sources) != nil || len(sources) != 1 {
		return false
	}
	s, e := teamsStrictObject(sources[0], map[string]bool{"source": true, "settings": true})
	if e != nil || !teamsExactKeys(s, "source", "settings") || string(s["source"]) != `"flagSettings"` || !teamsClaudeSafetySettings(s["settings"]) {
		return false
	}
	allowed := map[string]bool{"model": true, "effort": true, "advisor": true, "ultracode": true, "ultracodeRequested": true, "ultracodeAvailable": true, "ultrathinkAvailable": true, "outsideFileToolReadsBlocked": true, "outsideFileToolWritesBlocked": true, "outsideBlocksCountLaunchFoldersOnly": true, "launchEnvLeftOwnFilesInPlace": true}
	a, e := teamsStrictObject(p["applied"], allowed)
	if e != nil || len(a) != len(allowed) {
		return false
	}
	var actualModel, actualEffort string
	if json.Unmarshal(a["model"], &actualModel) != nil || json.Unmarshal(a["effort"], &actualEffort) != nil || actualModel != model || actualEffort != effort || string(a["advisor"]) != "null" {
		return false
	}
	for _, k := range []string{"ultracode", "ultracodeRequested", "ultracodeAvailable", "ultrathinkAvailable"} {
		if string(a[k]) != "false" {
			return false
		}
	}
	for _, k := range []string{"outsideFileToolReadsBlocked", "outsideFileToolWritesBlocked", "outsideBlocksCountLaunchFoldersOnly", "launchEnvLeftOwnFilesInPlace"} {
		if string(a[k]) != "false" && string(a[k]) != "true" {
			return false
		}
	}
	return true
}

func teamsClaudeHooksValid(raw []byte, requestID string) bool {
	p, ok := teamsClaudeControlPayload(raw, requestID)
	if !ok || !teamsExactKeys(p, "events", "hooks", "eventCatalog", "policy", "safeMode") || string(p["events"]) != "[]" || string(p["hooks"]) != "[]" {
		return false
	}
	var catalog []json.RawMessage
	if json.Unmarshal(p["eventCatalog"], &catalog) != nil || catalog == nil {
		return false
	}
	policy, e := teamsStrictObject(p["policy"], map[string]bool{"disabledByPolicy": true, "managedOnly": true, "pluginOnly": true, "allDisabled": true, "policyHookCount": true})
	if e != nil || !teamsExactKeys(policy, "disabledByPolicy", "managedOnly", "pluginOnly", "allDisabled", "policyHookCount") {
		return false
	}
	if string(policy["disabledByPolicy"]) != "false" || string(policy["managedOnly"]) != "false" || string(policy["pluginOnly"]) != "false" || string(policy["allDisabled"]) != "true" || string(policy["policyHookCount"]) != "0" {
		return false
	}
	safe, e := teamsStrictObject(p["safeMode"], map[string]bool{"managedHooksStillApply": true, "exitHint": true})
	return e == nil && teamsExactKeys(safe, "managedHooksStillApply", "exitHint") && string(safe["managedHooksStillApply"]) == "false"
}

// Account material stays in memory and is never put in a result or log.
func teamsClaudeAccountDigest(account json.RawMessage) ([32]byte, bool) {
	var empty [32]byte
	v, e := teamsStrictObject(account, map[string]bool{"email": true, "organization": true, "subscriptionType": true, "apiProvider": true})
	if e != nil || !teamsExactKeys(v, "email", "organization", "subscriptionType", "apiProvider") || string(v["apiProvider"]) != `"firstParty"` {
		return empty, false
	}
	var fields []string
	for _, k := range []string{"email", "organization", "subscriptionType", "apiProvider"} {
		var s string
		if json.Unmarshal(v[k], &s) != nil || s == "" || len(s) > 1024 {
			return empty, false
		}
		fields = append(fields, s)
	}
	b, _ := json.Marshal(fields)
	return sha256.Sum256(b), true
}

func teamsClaudeAgentsValid(raw json.RawMessage) bool {
	var entries []struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(raw, &entries) != nil || len(entries) != 4 {
		return false
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name)
	}
	slices.Sort(names)
	return slices.Equal(names, []string{"Explore", "Plan", "claude", "general-purpose"})
}

// Initialization validates the exact reviewed binary protocol and obtains an
// in-memory account fingerprint for the member's model-account consent. No
// prompt or library data is needed for this handshake.
func teamsClaudeInitialize(raw []byte, requestID string) ([32]byte, bool) {
	var empty [32]byte
	var envelope struct {
		Response struct {
			Permissions []json.RawMessage `json:"pending_permission_requests"`
			Dialogs     []json.RawMessage `json:"pending_user_dialog_requests"`
		} `json:"response"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.Response.Permissions == nil || envelope.Response.Dialogs == nil || len(envelope.Response.Permissions) != 0 || len(envelope.Response.Dialogs) != 0 {
		return empty, false
	}
	p, ok := teamsClaudeControlPayload(raw, requestID)
	if !ok {
		return empty, false
	}
	allowed := []string{"agents", "output_style", "available_output_styles", "account", "pid", "claude_code_version", "current_permission_mode", "hook_on_failure_supported", "hooks_when_unanswered_kept", "other_hook_ask_outranks_host", "feedback_mode", "analytics_disabled", "remote_control_auto_enable", "remote_control_auto_connect_default", "remote_control_available", "remote_control_auto_on_by_default", "ide_rc_auto_enable_gate", "fast_mode_state", "fast_mode_disabled_reason", "session_state", "capabilities"}
	// The pinned CLI can also report model metadata and the user's output
	// style directory during initialize. It must report no commands, and the
	// active output style remains exactly default below.
	if _, exists := p["commands"]; exists {
		allowed = append(allowed, "commands", "models", "user_output_styles_dir")
		var commands []json.RawMessage
		var models []json.RawMessage
		var directory string
		if json.Unmarshal(p["commands"], &commands) != nil || commands == nil || len(commands) != 0 || json.Unmarshal(p["models"], &models) != nil || models == nil || len(models) > 64 || json.Unmarshal(p["user_output_styles_dir"], &directory) != nil || len(directory) > 4096 {
			return empty, false
		}
		for _, model := range models {
			if !teams.ValidUniqueJSON(model) || len(model) > 32768 {
				return empty, false
			}
		}
	}
	if !teamsExactKeys(p, allowed...) || string(p["claude_code_version"]) != `"2.1.326"` || string(p["current_permission_mode"]) != `"dontAsk"` || string(p["output_style"]) != `"default"` || string(p["session_state"]) != `"idle"` || string(p["analytics_disabled"]) != "true" || !teamsClaudeAgentsValid(p["agents"]) {
		return empty, false
	}
	for _, k := range []string{"remote_control_auto_enable", "remote_control_auto_connect_default", "remote_control_auto_on_by_default", "ide_rc_auto_enable_gate"} {
		if string(p[k]) != "false" {
			return empty, false
		}
	}
	return teamsClaudeAccountDigest(p["account"])
}

func teamsClaudeInitializePID(raw []byte, requestID string) int {
	p, ok := teamsClaudeControlPayload(raw, requestID)
	if !ok {
		return 0
	}
	var pid int
	if json.Unmarshal(p["pid"], &pid) != nil || pid <= 0 {
		return 0
	}
	return pid
}

// Auth status is a separate read-only account check; it carries no source
// data. Its stable identity is kept locally as a digest, never sent or logged.
func teamsClaudeAuthDigest(raw []byte) ([32]byte, bool) {
	var empty [32]byte
	allowed := map[string]bool{"loggedIn": true, "authMethod": true, "apiProvider": true, "analyticsDisabled": true, "projectsDirectory": true, "configDirectory": true, "loginExpiresAt": true, "consoleProfileSignInOffered": true, "email": true, "orgId": true, "orgName": true, "subscriptionType": true, "storedClaudeAiLogin": true, "planTier": true}
	v, e := teamsStrictObject(raw, allowed)
	if e != nil || !teams.ValidUniqueJSON(raw) || string(v["loggedIn"]) != "true" || string(v["authMethod"]) != `"claude.ai"` || string(v["apiProvider"]) != `"firstParty"` || string(v["storedClaudeAiLogin"]) != "true" {
		return empty, false
	}
	var fields []string
	for _, key := range []string{"authMethod", "apiProvider", "email", "orgId"} {
		var value string
		if json.Unmarshal(v[key], &value) != nil || value == "" || len(value) > 1024 {
			return empty, false
		}
		fields = append(fields, value)
	}
	b, _ := json.Marshal(fields)
	return sha256.Sum256(b), true
}
