package archive

import "maps"

// messageAgentSessionSQL follows the same ownership rule as the reader and
// Session Shape: an embedded helper owns its recorded messages; otherwise the
// conversation's main session owns them. Conflicting helper ownership is
// unknown, rather than assigning the message to an arbitrary session.
func messageAgentSessionSQL(message, conversation string) string {
	owners := ` FROM agent_session_messages session_message JOIN agent_sessions session_owner ON session_owner.id=session_message.agent_session_id
 WHERE session_message.message_id=` + message + ` AND session_owner.native_id<>'main'`
	return `(CASE WHEN NULLIF(` + message + `,'') IS NULL THEN NULL WHEN EXISTS(SELECT 1` + owners + `) THEN
 (SELECT CASE WHEN COUNT(DISTINCT session_owner.id)=1 THEN MIN(session_owner.id) END` + owners + `)
 ELSE (SELECT main_session.id FROM agent_sessions main_session WHERE main_session.conversation_id=` + conversation + ` AND main_session.native_id='main') END)`
}

// Expose recorded session identity as a normal application source column. The
// expression is projected only when requested, leaving unrelated queries cheap.
func withAgentSessionIdentity(dataset string, table sqlDataset) sqlDataset {
	if table.columns["agent_session_id"] != "" {
		return table
	}
	field := map[string]string{
		"writing_messages": "id", "tool_calls": "call_message_id", "skill_usages": "evidence_message_id",
		"compactions": "message_id", "agent_events": "message_id", "agent_event_messages": "message_id",
		"agent_event_rates": "message_id", "message_labels": "message_id",
	}[dataset]
	if field == "" || table.columns[field] == "" || table.columns["conversation_id"] == "" {
		return table
	}
	identity := messageAgentSessionSQL(table.columns[field], table.columns["conversation_id"])
	// Tool and skill ledgers already record the execution directly. Retain that
	// stronger evidence even when a source did not save message membership.
	if dataset == "tool_calls" {
		identity = "COALESCE(NULLIF(t.agent_session_id,'')," + identity + ")"
	} else if dataset == "skill_usages" {
		identity = "COALESCE(NULLIF(s.agent_session_id,'')," + identity + ")"
	}
	table.columns = maps.Clone(table.columns)
	table.columns["agent_session_id"] = identity
	return table
}
