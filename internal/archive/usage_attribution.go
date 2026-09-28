package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
)

const usageAttributionVersion = "1"

func claudeChildren(conversations []ConversationRecord, root ConversationRecord) []ConversationRecord {
	if root.Provider != "claude" || root.AgentDepth != 0 {
		return nil
	}
	children := []ConversationRecord{}
	for _, child := range conversations {
		if child.AgentDepth > 0 && (child.ParentNativeID == root.NativeID || strings.HasPrefix(child.NativeID, root.NativeID+":subagent:")) {
			children = append(children, child)
		}
	}
	return children
}

func claudeGroupRemainder(conversations []ConversationRecord, root ConversationRecord) map[string]tokenCounts {
	if root.Provider != "claude" || root.AgentDepth != 0 {
		return nil
	}
	claims := map[string]tokenCounts{}
	for _, message := range root.Messages {
		raw, value := tokenObject(message)
		if firstString(value["type"], raw["type"]) != "cost-state" {
			continue
		}
		for model, usage := range mapValueDefault(value["modelUsage"]) {
			if counts := normalizeTokenCounts(mapValue(usage)); counts != nil {
				claims[model] = counts
			}
		}
	}
	if len(claims) == 0 {
		return nil
	}
	requests := map[string]tokenCounts{}
	hasRequests := false
	for _, conversation := range append([]ConversationRecord{root}, claudeChildren(conversations, root)...) {
		modelRequests, _ := buildToolLedger(conversation.Messages, conversation.Model)
		for _, request := range modelRequests {
			if request.Counts["total_tokens"] <= 0 {
				continue
			}
			hasRequests = true
			if requests[request.Model] == nil {
				requests[request.Model] = tokenCounts{}
			}
			addTokenCounts(requests[request.Model], request.Counts)
		}
	}
	if !hasRequests {
		return nil
	}
	remainders := map[string]tokenCounts{}
	for model, claim := range claims {
		if remainder := tokenRemainder(claim, requests[model]); remainder["total_tokens"] > 0 {
			remainders[model] = remainder
		}
	}
	return remainders
}

func applyClaudeGroupUsage(tx *sql.Tx, workspaceID string, conversations []ConversationRecord, ids []string, authority []conversationAuthority) error {
	for index, root := range conversations {
		if root.Provider != "claude" || root.AgentDepth != 0 || index >= len(ids) || ids[index] == "" ||
			(authority != nil && (index >= len(authority) || !authority[index].writer)) {
			continue
		}
		remainders := claudeGroupRemainder(conversations, root)
		rootID := ids[index]
		if remainders != nil {
			if err := replaceAgentSessionsWithCostState(tx, workspaceID, rootID, root, true); err != nil {
				return err
			}
			sessionID := stableID("agent-session", rootID, "main")
			for model, counts := range remainders {
				if err := insertSessionRemainder(tx, sessionID, usageHour(root.StartedAt), model, counts); err != nil {
					return err
				}
			}
		}
		if err := linkClaudeDelegations(tx, rootID, root, conversations, ids); err != nil {
			return err
		}
	}
	return nil
}

func insertSessionRemainder(tx *sql.Tx, sessionID, hour, model string, counts tokenCounts) error {
	if counts["total_tokens"] <= 0 {
		return nil
	}
	_, err := tx.Exec(`INSERT INTO agent_session_usage(agent_session_id,usage_hour,model,attribution,input_tokens,uncached_input_tokens,
		cache_read_input_tokens,cache_creation_input_tokens,cache_creation_5m_input_tokens,cache_creation_1h_input_tokens,
		output_tokens,reasoning_output_tokens,unclassified_tokens,total_tokens) VALUES(?,?,?,'session-remainder',?,?,?,?,?,?,?,?,?,?)`,
		sessionID, hour, model, integer(counts["input_tokens"]), integer(max(counts["input_tokens"]-counts["cache_read_input_tokens"]-counts["cache_creation_input_tokens"], 0)),
		integer(counts["cache_read_input_tokens"]), integer(counts["cache_creation_input_tokens"]), integer(counts["cache_creation_5m_input_tokens"]),
		integer(counts["cache_creation_1h_input_tokens"]), integer(counts["output_tokens"]), integer(counts["reasoning_output_tokens"]),
		integer(max(counts["total_tokens"]-counts["input_tokens"]-counts["output_tokens"], 0)), integer(counts["total_tokens"]))
	if err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE agent_sessions SET input_tokens=input_tokens+?,uncached_input_tokens=uncached_input_tokens+?,
		cache_read_input_tokens=cache_read_input_tokens+?,cache_creation_input_tokens=cache_creation_input_tokens+?,
		cache_creation_5m_input_tokens=cache_creation_5m_input_tokens+?,cache_creation_1h_input_tokens=cache_creation_1h_input_tokens+?,
		output_tokens=output_tokens+?,reasoning_output_tokens=reasoning_output_tokens+?,unclassified_tokens=unclassified_tokens+?,
		total_tokens=total_tokens+?,usage_status=CASE WHEN unclassified_tokens+?>0 THEN 'reported-partial' ELSE 'reported-reconciled' END WHERE id=?`,
		integer(counts["input_tokens"]), integer(max(counts["input_tokens"]-counts["cache_read_input_tokens"]-counts["cache_creation_input_tokens"], 0)),
		integer(counts["cache_read_input_tokens"]), integer(counts["cache_creation_input_tokens"]), integer(counts["cache_creation_5m_input_tokens"]),
		integer(counts["cache_creation_1h_input_tokens"]), integer(counts["output_tokens"]), integer(counts["reasoning_output_tokens"]),
		integer(max(counts["total_tokens"]-counts["input_tokens"]-counts["output_tokens"], 0)), integer(counts["total_tokens"]),
		integer(max(counts["total_tokens"]-counts["input_tokens"]-counts["output_tokens"], 0)), sessionID)
	return err
}

func linkClaudeDelegations(tx *sql.Tx, rootID string, root ConversationRecord, conversations []ConversationRecord, ids []string) error {
	for _, message := range root.Messages {
		if message.Kind != "delegation_result" || message.CallID == "" {
			continue
		}
		var payload map[string]any
		if json.Unmarshal([]byte(message.Text), &payload) != nil {
			continue
		}
		details := mapValueDefault(payload["details"])
		agentID, runID := firstString(details["agentId"]), firstString(details["runId"])
		for index, child := range conversations {
			if index >= len(ids) || ids[index] == "" || child.AgentDepth == 0 || child.ParentNativeID != root.NativeID {
				continue
			}
			if agentID != "" && !strings.HasSuffix(child.NativeID, "agent-"+agentID) || agentID == "" && (runID == "" || !strings.Contains(child.NativeID, "/"+runID+"/")) {
				continue
			}
			if _, err := tx.Exec(`UPDATE agent_sessions SET usage_status='in-child-conversation',child_conversation_id=?
				WHERE conversation_id=? AND native_id=? AND total_tokens=0`, ids[index], rootID, message.CallID); err != nil {
				return err
			}
			break
		}
	}
	return nil
}

// RepairUsageAttribution replays stored messages one workspace at a time.
// The version row commits with the corrected sessions and metrics.
func (c *Catalog) RepairUsageAttribution(ctx context.Context, progress func(int, int)) (int, error) {
	return c.repairUsageAttribution(ctx, progress, walSizeLimit)
}

func (c *Catalog) repairUsageAttribution(ctx context.Context, progress func(int, int), walLimit int64) (int, error) {
	rows, err := queryMapsContext(ctx, c.DB, `SELECT w.id FROM workspaces w WHERE EXISTS
		(SELECT 1 FROM conversations c WHERE c.workspace_id=w.id AND c.provider='claude')
		AND NOT EXISTS (SELECT 1 FROM usage_attribution_state s WHERE s.workspace_id=w.id AND s.version=?)`, usageAttributionVersion)
	if err != nil {
		return 0, err
	}
	for index, row := range rows {
		if err := ctx.Err(); err != nil {
			return index, err
		}
		workspaceID := firstString(row["id"])
		items, err := queryMapsContext(ctx, c.DB, `SELECT id,native_id,parent_id,provider,model,agent_depth,started_at FROM conversations
			WHERE workspace_id=? ORDER BY agent_depth,native_id`, workspaceID)
		if err != nil {
			return index, err
		}
		conversations, ids := make([]ConversationRecord, 0, len(items)), make([]string, 0, len(items))
		for _, item := range items {
			messages, err := storedMessages(ctx, c.DB, item["id"])
			if err != nil {
				return index, err
			}
			ids = append(ids, firstString(item["id"]))
			nativeID := firstString(item["native_id"])
			parentNativeID := ""
			if before, _, ok := strings.Cut(nativeID, ":subagent:"); ok {
				parentNativeID = before
			}
			conversations = append(conversations, ConversationRecord{NativeID: nativeID, ParentNativeID: parentNativeID, Provider: firstString(item["provider"]), Model: firstString(item["model"]), AgentDepth: int(integer(item["agent_depth"])), StartedAt: firstString(item["started_at"]), Messages: messages})
		}
		tx, err := c.DB.BeginTx(ctx, nil)
		if err != nil {
			return index, err
		}
		for j, conversation := range conversations {
			if err = replaceAgentSessions(tx, workspaceID, ids[j], conversation); err != nil {
				break
			}
		}
		if err == nil {
			err = applyClaudeGroupUsage(tx, workspaceID, conversations, ids, nil)
		}
		if err == nil {
			if metrics := reconciledTokenMetrics(WorkspaceRecord{Conversations: conversations}); len(metrics) > 0 {
				_, err = tx.Exec("DELETE FROM metrics WHERE workspace_id=? AND unit='tokens'", workspaceID)
				if err == nil {
					err = replaceMetrics(tx, workspaceID, metrics)
				}
			}
		}
		if err == nil {
			_, err = tx.Exec(`INSERT INTO usage_attribution_state(workspace_id,version) VALUES(?,?)
				ON CONFLICT(workspace_id) DO UPDATE SET version=excluded.version`, workspaceID, usageAttributionVersion)
		}
		if err != nil {
			tx.Rollback()
			return index, err
		}
		if err = tx.Commit(); err != nil {
			return index, err
		}
		c.boundWAL(walLimit)
		if progress != nil {
			progress(index+1, len(rows))
		}
	}
	return len(rows), nil
}
