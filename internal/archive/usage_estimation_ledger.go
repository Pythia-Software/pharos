package archive

import (
	"context"
	"database/sql"
	"math"
)

// Estimates occupy ordinary accounting rows. Only predictions can be cleared;
// reported counters, including a reported zero, are never replaced here.
func clearUsageEstimatesSQL(conversation string) string {
	where := "1=1"
	if conversation != "" {
		where = "conversation_id=" + conversation
	}
	return `DELETE FROM model_requests WHERE ` + where + ` AND is_estimate=1;
 DELETE FROM agent_session_usage WHERE is_estimate=1 AND agent_session_id IN (SELECT id FROM agent_sessions WHERE ` + where + `);
 UPDATE agent_sessions SET input_tokens=0,uncached_input_tokens=0,cache_read_input_tokens=0,cache_creation_input_tokens=0,
 cache_creation_5m_input_tokens=0,cache_creation_1h_input_tokens=0,output_tokens=0,reasoning_output_tokens=0,unclassified_tokens=0,total_tokens=0,
 is_estimate=0,input_tokens_available=1,usage_estimate_json=NULL,usage_status='unavailable'
 WHERE ` + where + ` AND is_estimate=1;
 UPDATE tool_calls SET duration_ms=NULL,duration_source=NULL,duration_is_estimate=0,duration_estimate_json=NULL
 WHERE ` + where + ` AND duration_is_estimate=1;`
}

func repeatedEstimateConversationArgs(id string) []any { return []any{id, id, id, id} }

func authoritativeUsageExists(ctx context.Context, tx estimationReader, conversation string) (bool, error) {
	var exists bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM model_requests WHERE conversation_id=? AND is_estimate=0)
 OR EXISTS(SELECT 1 FROM agent_sessions WHERE conversation_id=? AND is_estimate=0 AND (total_tokens>0 OR usage_status LIKE 'reported%'))
 OR EXISTS(SELECT 1 FROM agent_session_usage u JOIN agent_sessions a ON a.id=u.agent_session_id WHERE a.conversation_id=? AND u.is_estimate=0)`, conversation, conversation, conversation).Scan(&exists)
	return exists, err
}

func publishCanonicalUsageEstimates(ctx context.Context, tx *sql.Tx, conversation, key string, report *conversationUsageEstimates) error {
	var session, workspace, started string
	err := tx.QueryRowContext(ctx, `SELECT a.id,c.workspace_id,COALESCE(c.started_at,'') FROM conversations c
 JOIN agent_sessions a ON a.conversation_id=c.id AND a.native_id='main' WHERE c.id=?`, conversation).Scan(&session, &workspace, &started)
	if err == sql.ErrNoRows {
		return nil // an unsupported session structure cannot receive invented ownership
	}
	if err != nil {
		return err
	}
	var outputTotal, thinkingTotal int64
	for sequence, response := range report.Responses {
		output := int64(math.Round(response.Output.Expected))
		thinking := min(output, int64(math.Round(response.Thinking.Expected)))
		stamp := response.UsageAt
		if stamp == "" {
			var at sql.NullString
			if err := tx.QueryRowContext(ctx, "SELECT created_at FROM messages WHERE id=?", response.MessageID).Scan(&at); err != nil {
				return err
			}
			stamp = defaultString(at.String, started)
		}
		requestID := stableID("model-request", conversation, "estimate:"+response.NativeID)
		payload := map[string]any{"estimator_key": key, "scope": "output_only", "input_tokens_available": false, "estimate": response}
		if _, err := tx.ExecContext(ctx, `INSERT INTO model_requests(id,conversation_id,agent_session_id,native_id,sequence,requested_at,model,effort,speed,speed_source,
 output_tokens,reasoning_output_tokens,total_tokens,source_message_id,final_message_id,is_estimate,input_tokens_available,usage_estimate_json)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,1,0,?)`, requestID, conversation, session, response.NativeID, sequence, nilIfEmpty(stamp), nilIfEmpty(response.Model), nilIfEmpty(response.Effort), response.Speed, response.SpeedSource,
			output, thinking, output, response.MessageID, response.MessageID, jsonText(payload)); err != nil {
			return err
		}
		// The estimated row can carry a saved selection as an explicit pricing
		// assumption; source messages and measured requests remain unchanged.
		model := defaultString(response.Model, "Unknown model")
		usageMetadata := map[string]any{"estimator_key": key, "scope": "output_only"}
		if response.ModelSource != "" {
			usageMetadata["model_source"] = response.ModelSource
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_session_usage(agent_session_id,usage_hour,model,effort,attribution,output_tokens,reasoning_output_tokens,total_tokens,is_estimate,input_tokens_available,usage_estimate_json)
 VALUES(?,?,?,?,'estimated-output',?,?,?,1,0,?)
 ON CONFLICT(agent_session_id,usage_hour,model,effort,attribution) DO UPDATE SET output_tokens=output_tokens+excluded.output_tokens,
 reasoning_output_tokens=reasoning_output_tokens+excluded.reasoning_output_tokens,total_tokens=total_tokens+excluded.total_tokens,
 usage_estimate_json=CASE WHEN json_extract(excluded.usage_estimate_json,'$.model_source') IS NOT NULL THEN excluded.usage_estimate_json ELSE usage_estimate_json END`, session, usageHour(stamp), model, response.Effort, output, thinking, output, jsonText(usageMetadata)); err != nil {
			return err
		}
		for _, tool := range response.Tools {
			if tool.Status != "estimated" || tool.Seconds == nil {
				continue
			}
			if _, err := tx.ExecContext(ctx, `UPDATE tool_calls SET duration_ms=?,duration_source='estimated',duration_is_estimate=1,duration_estimate_json=?
 WHERE conversation_id=? AND call_message_id=? AND duration_ms IS NULL`, int64(math.Round(tool.Seconds.Expected*1000)), jsonText(tool), conversation, tool.MessageID); err != nil {
				return err
			}
		}
		outputTotal += output
		thinkingTotal += thinking
	}
	if len(report.Responses) == 0 {
		return nil
	}
	_, err = tx.ExecContext(ctx, `UPDATE agent_sessions SET output_tokens=?,reasoning_output_tokens=?,total_tokens=?,is_estimate=1,input_tokens_available=0,
 usage_status='estimated-output',usage_estimate_json=? WHERE id=? AND (usage_status='unavailable' OR is_estimate=1)`, outputTotal, thinkingTotal, outputTotal, jsonText(map[string]any{"estimator_key": key, "scope": "output_only"}), session)
	return err
}

// Missing input remains NULL at read boundaries, while accounting sums the
// available output and never mistakes reasoning (an output subset) for extra tokens.
var usageInputFields = []string{"input_tokens", "uncached_input_tokens", "cache_read_input_tokens", "cache_creation_input_tokens", "cache_creation_5m_input_tokens", "cache_creation_1h_input_tokens"}

func estimateFlag(value any) bool {
	if flag, ok := value.(bool); ok {
		return flag
	}
	return integer(value) != 0
}
func (b priceBook) ledgerCost(entry map[string]any, model, day string, tokens map[string]int64) usageCost {
	result := b.cost(model, day, tokens)
	if estimateFlag(entry["is_estimate"]) && result.cost != nil {
		result.status = "partial"
	}
	return result
}
