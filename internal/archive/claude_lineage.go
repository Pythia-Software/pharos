package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
)

// Repair one retained native root group during the explicit message-search
// upgrade. Only delegation envelopes and the first assignment are needed; large tool
// outputs and the rest of the prose are not read. IDs and reconciled usage remain untouched.
func repairStoredClaudeLineage(ctx context.Context, tx *sql.Tx, workspace, root string) error {
	rows, err := queryMapsContext(ctx, tx, `SELECT id,native_id,COALESCE(parent_id,'') parent_id,agent_depth FROM conversations
	 WHERE workspace_id=? AND provider='claude' AND (native_id=? OR instr(native_id,?)=1)`, workspace, root, root+":subagent:")
	if err != nil {
		return err
	}
	record := WorkspaceRecord{}
	ids, prior := map[string]string{}, map[string]int{}
	rootFound := false
	for _, row := range rows {
		native, id := firstString(row["native_id"]), firstString(row["id"])
		messages, err := queryMapsContext(ctx, tx, `SELECT native_id,kind,call_id,text FROM messages WHERE conversation_id=?
		 AND kind IN ('delegation','delegation_result') ORDER BY source_order,created_at,id`, id)
		if err != nil {
			return err
		}
		conversation := ConversationRecord{NativeID: native, Provider: "claude", AgentDepth: int(integer(row["agent_depth"]))}
		if native == root {
			rootFound, conversation.AgentDepth = true, 0
		} else {
			conversation.AgentDepth = max(1, conversation.AgentDepth)
		}
		for _, message := range messages {
			conversation.Messages = append(conversation.Messages, MessageRecord{NativeID: firstString(message["native_id"]), Kind: firstString(message["kind"]), CallID: firstString(message["call_id"]), Text: firstString(message["text"])})
		}
		var assignment MessageRecord
		assignment.Kind = "message"
		if err := tx.QueryRowContext(ctx, `SELECT role,text FROM messages WHERE conversation_id=? AND kind='message' AND +role IN ('user','agent')
		 ORDER BY source_order IS NULL,source_order,created_at,id LIMIT 1`, id).Scan(&assignment.Role, &assignment.Text); err == nil {
			conversation.Messages = append(conversation.Messages, assignment)
		} else if err != sql.ErrNoRows {
			return err
		}
		record.Conversations = append(record.Conversations, conversation)
		ids[native], prior[native] = id, int(integer(row["agent_depth"]))
	}
	if !rootFound {
		return nil
	}
	reconcileClaudeLineage(&record)
	orderedIDs := make([]string, len(record.Conversations))
	for i, conversation := range record.Conversations {
		id := ids[conversation.NativeID]
		orderedIDs[i] = id
		var parent any
		if conversation.ParentNativeID != "" {
			parent = ids[conversation.ParentNativeID]
		}
		if _, err := tx.ExecContext(ctx, `UPDATE conversations SET parent_id=?,agent_depth=? WHERE id=?
		 AND (parent_id IS NOT ? OR agent_depth<>?)`, parent, conversation.AgentDepth, id, parent, conversation.AgentDepth); err != nil {
			return err
		}
		if delta := conversation.AgentDepth - prior[conversation.NativeID]; delta != 0 {
			if _, err := tx.ExecContext(ctx, "UPDATE agent_sessions SET depth=depth+? WHERE conversation_id=?", delta, id); err != nil {
				return err
			}
		}
	}
	return linkClaudeGroupDelegations(tx, record.Conversations, orderedIDs)
}

// A Claude subagent's filename identifies the execution, while the result of
// its launch identifies the conversation that created it. All descendants
// live in the root's subagents directory, so directory nesting is no proof of
// parentage.
type claudeLineageEdge struct {
	parent int
	callID string
}

func claudeLineageEdges(conversations []ConversationRecord) map[int]claudeLineageEdge {
	children := map[string]int{}
	duplicateChildren := map[string]bool{}
	workflows := map[string][]int{}
	for index, conversation := range conversations {
		if conversation.AgentDepth == 0 {
			continue
		}
		if _, workflow, found := strings.Cut(conversation.NativeID, ":subagent:workflows/"); found {
			if run, _, found := strings.Cut(workflow, "/"); found {
				workflows[run] = append(workflows[run], index)
			}
		}
		name := conversation.NativeID
		if slash := strings.LastIndexByte(name, '/'); slash >= 0 {
			name = name[slash+1:]
		} else if colon := strings.LastIndexByte(name, ':'); colon >= 0 {
			name = name[colon+1:]
		}
		if !strings.HasPrefix(name, "agent-") || len(name) <= len("agent-") {
			continue
		}
		agent := strings.TrimPrefix(name, "agent-")
		if _, exists := children[agent]; exists {
			duplicateChildren[agent] = true
		}
		children[agent] = index
	}
	type candidate struct {
		parent int
		call   string
	}
	claims := map[int][]candidate{}
	for parent, conversation := range conversations {
		launches := map[string]int{}
		launchMessages := map[string]MessageRecord{}
		for _, message := range conversation.Messages {
			if message.Kind == "delegation" && message.CallID != "" {
				launches[message.CallID]++
				launchMessages[message.CallID] = message
			}
		}
		type acknowledgement struct {
			agent, run string
			textual    bool
		}
		results := map[string]acknowledgement{}
		resultCounts := map[string]int{}
		for _, message := range conversation.Messages {
			if message.Kind != "delegation_result" || launches[message.CallID] != 1 {
				continue
			}
			resultCounts[message.CallID]++
			var payload map[string]any
			if json.Unmarshal([]byte(message.Text), &payload) == nil && payload["is_error"] != true {
				// The native Agent acknowledgement is retained in toolUseResult
				// details, or an explicit launch acknowledgement corroborated
				// by the child assignment. Report prose is not identity proof.
				details := mapValue(payload["details"])
				ack := acknowledgement{agent: firstString(details["agentId"]), run: firstString(details["runId"])}
				if ack.agent == "" && ack.run == "" {
					ack.agent, ack.textual = claudeTextLaunchAgent(payload["content"]), true
				}
				results[message.CallID] = ack
			}
		}
		for call, result := range results {
			if resultCounts[call] != 1 || duplicateChildren[result.agent] {
				continue
			}
			child, exists := children[result.agent]
			if result.agent == "" && result.run != "" && len(workflows[result.run]) == 1 {
				child, exists = workflows[result.run][0], true
			}
			if !exists || child == parent {
				continue
			}
			if result.textual && !claudeLaunchAssignmentMatches(launchMessages[call], conversations[child]) {
				continue
			}
			claims[child] = append(claims[child], candidate{parent, call})
		}
	}
	edges := map[int]claudeLineageEdge{}
	for child, candidates := range claims {
		if len(candidates) == 1 {
			edges[child] = claudeLineageEdge{candidates[0].parent, candidates[0].call}
		}
	}
	// A corrupt or conflicting transcript can describe a cycle. The whole
	// cycle keeps the conservative root fallback instead of depending on order.
	cycleMembers := map[int]bool{}
	for child := range edges {
		seen := map[int]bool{child: true}
		path := []int{child}
		for cursor := child; ; {
			edge, exists := edges[cursor]
			if !exists {
				break
			}
			if seen[edge.parent] {
				for _, member := range path {
					cycleMembers[member] = true
				}
				break
			}
			seen[edge.parent] = true
			path = append(path, edge.parent)
			cursor = edge.parent
		}
	}
	for member := range cycleMembers {
		delete(edges, member)
	}
	return edges
}

// Some Claude versions retain the native asynchronous launch acknowledgement
// only as a text block. Accept its explicit ID line, never IDs mentioned in
// report prose. This weaker form also requires an exact assignment match.
func claudeTextLaunchAgent(content any) string {
	text := messageText(content)
	if !strings.HasPrefix(text, "Async agent launched successfully.") {
		return ""
	}
	agent, count := "", 0
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, "agentId: ") {
			continue
		}
		id, tail, ok := strings.Cut(strings.TrimPrefix(line, "agentId: "), " ")
		if !ok || !strings.HasPrefix(tail, "(internal ID") || id == "" || strings.ContainsAny(id, "/\\:\t\r") {
			return ""
		}
		agent, count = id, count+1
	}
	if count != 1 {
		return ""
	}
	return agent
}

func claudeLaunchAssignmentMatches(launch MessageRecord, child ConversationRecord) bool {
	var call struct {
		Tool  string `json:"tool"`
		Input struct {
			Prompt string `json:"prompt"`
		} `json:"input"`
	}
	if json.Unmarshal([]byte(launch.Text), &call) != nil || !matchesFold(call.Tool, "agent", "task") || call.Input.Prompt == "" {
		return false
	}
	for _, message := range child.Messages {
		if message.Kind == "message" && (message.Role == "agent" || message.Role == "user") {
			return message.Text == call.Input.Prompt
		}
	}
	return false
}

func reconcileClaudeLineage(record *WorkspaceRecord) {
	conversations := record.Conversations
	if len(conversations) == 0 {
		return
	}
	root := -1
	for index, conversation := range conversations {
		if conversation.AgentDepth == 0 {
			root = index
			break
		}
	}
	if root < 0 {
		return
	}
	edges := claudeLineageEdges(conversations)
	depths := make([]int, len(conversations))
	var depth func(int) int
	depth = func(index int) int {
		if index == root {
			return 0
		}
		if depths[index] != 0 {
			return depths[index]
		}
		depths[index] = 1
		if edge, exists := edges[index]; exists {
			depths[index] = depth(edge.parent) + 1
			conversations[index].ParentNativeID = conversations[edge.parent].NativeID
		} else {
			conversations[index].ParentNativeID = conversations[root].NativeID
		}
		conversations[index].AgentDepth = depths[index]
		return depths[index]
	}
	for index := range conversations {
		depth(index)
	}
	// upsertConversation resolves parent IDs from rows already written in this
	// transaction. A stable depth order writes parent before grandchild even
	// when agent filenames sort in the opposite order.
	for index := 1; index < len(conversations); index++ {
		for cursor := index; cursor > 0 && conversations[cursor].AgentDepth < conversations[cursor-1].AgentDepth; cursor-- {
			conversations[cursor], conversations[cursor-1] = conversations[cursor-1], conversations[cursor]
		}
	}
	record.Conversations = conversations
}

// Called after all conversations and their agent sessions have been written.
// The same unique acknowledgement proof used for conversation lineage binds
// the launch session to the child execution.
func linkClaudeGroupDelegations(tx *sql.Tx, conversations []ConversationRecord, ids []string) error {
	type launch struct{ conversation, call string }
	accepted := map[launch]string{}
	groupIDs := []string{}
	for i, conversation := range conversations {
		if i < len(ids) && ids[i] != "" && conversation.Provider == "claude" {
			groupIDs = append(groupIDs, ids[i])
		}
	}
	for child, edge := range claudeLineageEdges(conversations) {
		if child >= len(ids) || edge.parent >= len(ids) || ids[child] == "" || ids[edge.parent] == "" ||
			conversations[child].Provider != "claude" || conversations[edge.parent].Provider != "claude" ||
			conversations[child].ParentNativeID != conversations[edge.parent].NativeID {
			continue
		}
		accepted[launch{ids[edge.parent], edge.callID}] = ids[child]
	}
	// An unchanged parent's derivation can be skipped during ingest even when
	// another conversation adds a competing claim. Retract rejected links in
	// this group as well as setting accepted ones; preserve links outside it.
	linked, err := queryMaps(tx, `SELECT id,conversation_id,native_id,child_conversation_id FROM agent_sessions
	 WHERE conversation_id IN (SELECT value FROM json_each(?))
	 AND child_conversation_id IN (SELECT value FROM json_each(?))`, jsonText(groupIDs), jsonText(groupIDs))
	if err != nil {
		return err
	}
	for _, session := range linked {
		if accepted[launch{firstString(session["conversation_id"]), firstString(session["native_id"])}] == firstString(session["child_conversation_id"]) {
			continue
		}
		if _, err := tx.Exec(`UPDATE agent_sessions SET child_conversation_id=NULL,
		 usage_status=CASE WHEN usage_status='in-child-conversation' THEN 'unavailable' ELSE usage_status END WHERE id=?`, session["id"]); err != nil {
			return err
		}
	}
	for source, child := range accepted {
		if _, err := tx.Exec(`UPDATE agent_sessions SET usage_status='in-child-conversation',child_conversation_id=?
		 WHERE conversation_id=? AND native_id=? AND total_tokens=0
		 AND (child_conversation_id IS NOT ? OR usage_status<>'in-child-conversation')`, child, source.conversation, source.call, child); err != nil {
			return err
		}
	}
	return nil
}
