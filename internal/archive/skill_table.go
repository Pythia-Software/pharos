package archive

import "strings"

func mcpMethod(name string, server any) any {
	if strings.HasPrefix(strings.ToLower(name), "mcp__") && server != nil {
		characters := []rune(name)
		start := min(len(characters), len([]rune(asString(server)))+7)
		return string(characters[start:])
	}
	switch name {
	case "read_mcp_resource", "list_mcp_resources", "list_mcp_resource_templates":
		return name
	}
	return nil
}

func mcpMethodSQL(prefix string) string {
	name, server := prefix+"tool_name", prefix+"mcp_server"
	return "CASE WHEN lower(" + name + ") GLOB 'mcp__*' AND " + server + " IS NOT NULL THEN substr(" + name + ",length(" + server + ")+8) WHEN " + name + " IN ('read_mcp_resource','list_mcp_resources','list_mcp_resource_templates') THEN " + name + " END"
}

var skillUsageDataset = sqlDataset{
	from: `FROM skill_usages s JOIN workspaces w ON w.id=s.workspace_id
		JOIN conversations c ON c.id=s.conversation_id LEFT JOIN repositories r ON r.id=w.repository_id
		LEFT JOIN tool_calls t ON t.id=s.tool_call_id LEFT JOIN agent_sessions a ON a.id=s.agent_session_id`,
	base:    "s.workspace_id NOT IN (SELECT workspace_id FROM tool_mirror_workspaces)",
	lookups: map[string]bool{"id": true, "conversation_id": true, "workspace_id": true, "tool_call_id": true},
	columns: map[string]string{
		"id": "s.id", "workspace_id": "s.workspace_id", "conversation_id": "s.conversation_id", "tool_call_id": "s.tool_call_id",
		"evidence_message_id": "s.evidence_message_id", "body_message_id": "s.body_message_id",
		"skill_name": "s.skill_name", "skill_path": "s.skill_path", "evidence_type": "s.evidence_type", "status": "s.status",
		"created_at": "s.created_at", "day": "date(s.created_at,'localtime')", "week": "date(s.created_at,'localtime','-6 days','weekday 1')", "month": "strftime('%Y-%m',s.created_at,'localtime')",
		"content_bytes": "s.content_bytes", "provider": "c.provider", "source_kind": "w.source_kind", "model": "COALESCE(t.model,c.model)",
		"repository_name": "r.display_name", "title": "w.title", "tool_name": "t.tool_name", "mcp_server": "t.mcp_server", "mcp_method": mcpMethodSQL("t."),
		"session_kind": "COALESCE(a.kind,'root')", "agent_depth": "COALESCE(a.depth,0)",
	},
}
