package archive

import (
	"database/sql"
	"strings"
)

// executionModel excludes labels which describe missing evidence or an error
// envelope. This is summary policy, never permission to move token counts.
func executionModel(model string) bool {
	model = strings.TrimSpace(model)
	return model != "" && !placeholderModel(model)
}

// advanceExecutionModel keeps an error's model on that message, without letting
// it become the inherited model for later events.
func advanceExecutionModel(current, reported string) string {
	if executionModel(reported) || !executionModel(current) && reported != "" {
		return reported
	}
	return current
}

// sessionSummaryModel prefers the last real assistant/request in source order.
// Without ordered requests, a single real usage model is recoverable; a mixed
// aggregate has no last model. Configured models are only a final fallback.
func sessionSummaryModel(summary agentSessionSummary, messages []MessageRecord, fallback string) string {
	latest, last := "", -1
	for _, index := range summary.messageIndexes {
		m := messages[index]
		raw, value := tokenObject(m)
		typ := firstString(value["type"], raw["type"])
		if matchesFold(typ, "result", "cost-state", "turn.completed") || m.Kind == "result" || firstString(value["advisor_of"], raw["advisor_of"]) != "" {
			continue
		}
		model := firstString(mapValue(value["message"])["model"], mapValue(value["response"])["model"], value["model"], m.Model)
		request := m.Role != "user" && (m.Role == "assistant" || typ == "assistant" || typ == "token_usage" && firstString(value["usage_scope"]) != "aggregate" || typ == "token_usage_record")
		if request && executionModel(model) && index > last {
			latest, last = model, index
		}
	}
	if latest != "" {
		return latest
	}
	models := map[string]bool{}
	for bucket, counts := range summary.usage {
		if executionModel(bucket.model) && counts["total_tokens"] > 0 {
			models[bucket.model] = true
		}
	}
	if len(models) == 1 {
		for model := range models {
			return model
		}
	}
	if len(models) > 1 {
		return "Unknown model"
	}
	return fallback
}

func conversationSummaryModel(c ConversationRecord) string {
	return sessionSummaryModel(agentSessionSummaries(c.Messages)[0], c.Messages, c.Model)
}

// repairModelSummaries runs after accounting replay so recovered labels cannot
// feed back into allocation, including when all original transcripts (or even
// retained message envelopes) are gone.
func repairModelSummaries(tx *sql.Tx, workspace string, conversations []ConversationRecord, ids []string) error {
	for i, c := range conversations {
		summaries := map[string]agentSessionSummary{}
		for _, summary := range agentSessionSummaries(c.Messages) {
			summaries[summary.nativeID] = summary
		}
		rows, err := queryMaps(tx, `SELECT id,native_id,model FROM agent_sessions WHERE conversation_id=?`, ids[i])
		if err != nil {
			return err
		}
		for _, session := range rows {
			native := firstString(session["native_id"])
			summary := summaries[native]
			sessionID := firstString(session["id"])
			// The persisted ledger also covers legacy facts preserved by replay.
			usage, err := queryMaps(tx, `SELECT model,SUM(total_tokens) total_tokens FROM agent_session_usage WHERE agent_session_id=? GROUP BY model`, sessionID)
			if err != nil {
				return err
			}
			summary.usage = map[usageBucket]tokenCounts{}
			for _, row := range usage {
				summary.usage[usageBucket{model: firstString(row["model"])}] = tokenCounts{"total_tokens": float64(integer(row["total_tokens"]))}
			}
			before := firstString(session["model"])
			model := sessionSummaryModel(summary, c.Messages, before)
			if before != model {
				if err := preserveSummaryUsageModel(tx, sessionID, before); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(`UPDATE agent_sessions SET model=? WHERE id=? AND COALESCE(model,'')<>?`, nilIfEmpty(model), sessionID, model); err != nil {
				return err
			}
			if native == "main" {
				if _, err := tx.Exec(`UPDATE conversations SET model=? WHERE id=? AND workspace_id=? AND COALESCE(model,'')<>?`, nilIfEmpty(model), ids[i], workspace, model); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// Empty legacy usage models use the session label as a pricing fallback.
// Freeze that old effective model before changing a label. Merge overlapping
// buckets without changing any token category, so unknown usage stays unknown.
func preserveSummaryUsageModel(tx *sql.Tx, sessionID, model string) error {
	model = defaultString(strings.TrimSpace(model), "Unknown model")
	updates := make([]string, 0, len(usageTokenFields))
	for _, field := range usageTokenFields {
		updates = append(updates, field+"=agent_session_usage."+field+"+excluded."+field)
	}
	fields := strings.Join(usageTokenFields, ",")
	_, err := tx.Exec(`INSERT INTO agent_session_usage(agent_session_id,usage_hour,model,effort,attribution,`+fields+`)
		SELECT agent_session_id,usage_hour,?,effort,attribution,`+fields+` FROM agent_session_usage
		WHERE agent_session_id=? AND TRIM(model)='' AND total_tokens>0
		ON CONFLICT(agent_session_id,usage_hour,model,effort,attribution) DO UPDATE SET `+strings.Join(updates, ","), model, sessionID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`DELETE FROM agent_session_usage WHERE agent_session_id=? AND TRIM(model)='' AND total_tokens>0`, sessionID)
	return err
}
