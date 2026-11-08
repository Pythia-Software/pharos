package archive

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
)

const claudeWorkflowRule = "claude-workflow-run-ack/v1"

// This key names a set of executions, not one agent. A Workflow can launch
// several agents; joining their launch handles would merge independent work.
func claudeWorkflowKey(account, root, run string) string {
	return "claude-workflow:" + account + "|" + root + "|" + run
}

func claudeWorkflowOf(native string) (root, run string, ok bool) {
	root, rest, ok := strings.Cut(native, ":subagent:workflows/")
	if !ok || root == "" {
		return "", "", false
	}
	run, agent, ok := strings.Cut(rest, "/")
	// Only immediate members of this run, never an agent's nested descendants.
	if !ok || !workflowIDPart(run) || !strings.HasPrefix(agent, "agent-") || !workflowIDPart(strings.TrimPrefix(agent, "agent-")) {
		return "", "", false
	}
	return root, run, true
}

func workflowIDPart(value string) bool {
	return value != "" && strings.IndexFunc(value, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_')
	}) < 0
}

// Read only the Workflow tool's structured acknowledgement, never its script,
// report prose, or journal text printed by another tool. The root session ID
// scopes the run even when its launcher is itself a subagent. The retained
// transcript directory must agree with both session and run.
func claudeWorkflowAcknowledgement(native, text string) string {
	var payload map[string]any
	if json.Unmarshal([]byte(text), &payload) != nil || truthy(payload["is_error"]) || truthy(payload["error"]) ||
		firstString(payload["tool"]) != "Workflow" {
		return ""
	}
	details := mapValue(payload["details"])
	run := firstString(details["runId"])
	if !workflowIDPart(run) || firstString(details["status"]) != "async_launched" || firstString(details["taskType"]) != "local_workflow" {
		return ""
	}
	root := claudeRootNative(native)
	dir := strings.ReplaceAll(firstString(details["transcriptDir"]), "\\", "/")
	if !strings.HasSuffix(dir, "/"+root+"/subagents/workflows/"+run) {
		return ""
	}
	return run
}

// Shared by extraction, its partial index and retained-evidence invalidation.
// row qualifies columns for a table or an OLD/NEW trigger row; empty leaves
// them unqualified. CASE guards old rows whose text is not JSON.
func claudeWorkflowCallSQL(row string) string {
	if row != "" {
		row += "."
	}
	return row + `kind IN ('tool_call','delegation') AND ` + row + `call_id<>'' AND CASE WHEN json_valid(` + row + `text) THEN json_extract(` + row + `text,'$.tool') END='Workflow'`
}

func conversationWorkflowEvidence(conversation ConversationRecord) []EvidenceRecord {
	if conversation.Provider != "claude" {
		return nil
	}
	launches := map[string]int{}
	results := map[string][]MessageRecord{}
	for _, message := range conversation.Messages {
		if message.CallID == "" {
			continue
		}
		switch message.Kind {
		case "tool_call", "delegation":
			var call struct {
				Tool string `json:"tool"`
			}
			if json.Unmarshal([]byte(message.Text), &call) == nil && call.Tool == "Workflow" {
				launches[message.CallID]++
			}
		case "tool_result", "delegation_result":
			results[message.CallID] = append(results[message.CallID], message)
		}
	}
	claims := []EvidenceRecord{}
	for _, call := range slices.Sorted(mapsKeysAny(launches)) {
		if launches[call] != 1 || len(results[call]) != 1 {
			continue
		}
		result := results[call][0]
		if run := claudeWorkflowAcknowledgement(conversation.NativeID, result.Text); run != "" {
			claims = append(claims, EvidenceRecord{Kind: evidenceWorkflowLaunch, Subject: "launch:" + call,
				Object: claudeWorkflowKey(conversation.Account, claudeRootNative(conversation.NativeID), run),
				Origin: evidenceDerived.String(), Authority: authorityVerified, Rule: claudeWorkflowRule, IDKind: idKindCallID,
				Locator: "message:" + result.NativeID, Detail: map[string]any{"run_id": run, "call_id": call}})
		}
	}
	return claims
}

// Read the launch and its matching results by call ID. Used by the legacy
// resolver, dependency walks and retained-row upgrades; no source files needed.
func retainedWorkflowEvidence(ctx context.Context, q queryer, scope []string) (map[string][]EvidenceRecord, error) {
	where, args := executionScope("m.conversation_id", scope)
	rows, err := queryMapsContext(ctx, q, `SELECT m.conversation_id,c.native_id,c.account,m.native_id message_native,m.kind,m.call_id,
	 CASE WHEN m.kind IN ('tool_call','delegation') THEN '{"tool":"Workflow"}' ELSE m.text END text
	 FROM (SELECT conversation_id,call_id FROM messages WHERE `+claudeWorkflowCallSQL("")+`) d
	 JOIN conversations c ON c.id=d.conversation_id AND c.provider='claude'
	 JOIN messages m ON m.conversation_id=d.conversation_id AND m.call_id=d.call_id AND m.call_id<>''
	 WHERE m.kind IN ('tool_call','delegation','tool_result','delegation_result')`+where+` ORDER BY m.conversation_id,m.id`, args...)
	if err != nil {
		return nil, err
	}
	conversations := map[string]ConversationRecord{}
	for _, row := range rows {
		id := firstString(row["conversation_id"])
		cv := conversations[id]
		cv.Provider, cv.Account, cv.NativeID = "claude", firstString(row["account"]), firstString(row["native_id"])
		cv.Messages = append(cv.Messages, MessageRecord{NativeID: firstString(row["message_native"]), Kind: firstString(row["kind"]),
			CallID: firstString(row["call_id"]), Text: firstString(row["text"])})
		conversations[id] = cv
	}
	out := map[string][]EvidenceRecord{}
	for id, conversation := range conversations {
		out[id] = conversationWorkflowEvidence(conversation)
	}
	return out, nil
}

func workflowParentAppender(e *executionEvidence) func(owner, key string, locator sourceRef, strength evidenceStrength) {
	owners := map[string]executionStream{}
	members := map[string][]executionStream{}
	for _, stream := range e.Streams {
		if stream.Ref.Kind != sourceConversation || stream.Provider != "claude" {
			continue
		}
		owners[stream.Ref.ID] = stream
		if root, run, ok := claudeWorkflowOf(stream.NativeID); ok {
			key := claudeWorkflowKey(stream.Account, root, run)
			members[key] = append(members[key], stream)
		}
	}
	return func(owner, key string, locator sourceRef, strength evidenceStrength) {
		parent, ok := owners[owner]
		if !ok {
			return
		}
		for _, child := range members[key] {
			if child.Ref.ID == owner || child.Account != parent.Account || claudeRootNative(child.NativeID) != claudeRootNative(parent.NativeID) {
				continue
			}
			e.Claims = append(e.Claims, executionClaim{Kind: claimTranscriptParent, Subject: child.Ref,
				Object: sourceRef{sourceConversation, owner}, Strength: min(strength, evidenceDerived), Namespace: "claude/" + child.Account,
				IDKind: idKindCallID, Rule: claudeWorkflowRule, Locator: locator})
		}
	}
}

// A late child, corrected acknowledgement, or removed result changes every
// dependent view of this run, including when no stored parent links exist.
func dependencyWorkflowLaunches(ctx context.Context, q queryer, out map[string]*dependencyConversation, ids []string) error {
	claims, err := retainedWorkflowEvidence(ctx, q, ids)
	if err != nil {
		return err
	}
	for id, evidence := range claims {
		if cv := out[id]; cv != nil {
			for _, claim := range evidence {
				cv.objects = append(cv.objects, claim.Object)
				cv.rows = append(cv.rows, "workflow\x1f"+jsonText(claim))
			}
		}
	}
	return nil
}
