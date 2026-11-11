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
		// JSON in an assistant answer is prose, not a protocol envelope.
		raw, value := map[string]any{}, map[string]any{}
		if strings.TrimSpace(m.RawText) != "" || m.Kind == "metadata" {
			raw, value = tokenObject(m)
		}
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
		rows, err := queryMaps(tx, `SELECT id,native_id,kind,model FROM agent_sessions WHERE conversation_id=?`, ids[i])
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
			fallback := before
			if native == "main" && session["kind"] == "subagent" {
				// The embedded summary can be mixed or unknown; it is not
				// the conversation's last observed model.
				fallback = c.Model
			}
			model := sessionSummaryModel(summary, c.Messages, fallback)
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
	// Conversation summaries retain their latest-model policy. Reconcile the
	// complete embedded executions after replay has restored all evidence.
	return refreshLinkedSessionModels(tx, ids)
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

// A replay can reconstruct counters without the request's model. Preserve its
// previous effective allocation only if subtracting the explicit request models
// leaves exactly those counters in the same hour, effort and attribution. A
// derived summary is never a new allocation source, even on a later replay.
func preserveReplayedUsageModels(tx *sql.Tx, sessionID string, usage map[usageBucket]tokenCounts, startedAt string) (map[usageBucket]tokenCounts, error) {
	missing := false
	for bucket := range usage {
		missing = missing || bucket.model == ""
	}
	if !missing {
		return usage, nil
	}
	rows, err := queryMaps(tx, `SELECT u.*,u.model AS usage_model,a.model AS session_model FROM agent_session_usage u
		JOIN agent_sessions a ON a.id=u.agent_session_id WHERE a.id=? AND u.total_tokens>0`, sessionID)
	if err != nil {
		return nil, err
	}
	prior := map[usageBucket]tokenCounts{}
	for _, row := range rows {
		bucket := usageBucket{hour: firstString(row["usage_hour"]), model: ledgerModel(row), effort: firstString(row["effort"]), attribution: firstString(row["attribution"])}
		if prior[bucket] == nil {
			prior[bucket] = tokenCounts{}
		}
		for _, field := range usageTokenFields {
			prior[bucket][field] += float64(integer(row[field]))
		}
	}
	resolved := map[usageBucket]tokenCounts{}
	normalized := map[usageBucket]tokenCounts{}
	for bucket, counts := range usage {
		bucket.hour = defaultString(bucket.hour, usageHour(startedAt))
		if normalized[bucket] == nil {
			normalized[bucket] = tokenCounts{}
		}
		addTokenCounts(normalized[bucket], counts)
	}
	for bucket, counts := range normalized {
		if bucket.model == "" {
			continue
		}
		resolved[bucket] = counts
		// Keep negative residuals: an overdrawn category must fail the exact
		// comparison below, rather than manufacturing a balanced allocation.
		for _, field := range usageTokenFields {
			if prior[bucket] != nil {
				prior[bucket][field] -= counts[field]
			}
		}
	}
	for bucket, counts := range normalized {
		if bucket.model != "" {
			continue
		}
		total, valid := tokenCounts{}, true
		for old, remaining := range prior {
			if old.hour != bucket.hour || old.effort != bucket.effort || old.attribution != bucket.attribution {
				continue
			}
			for _, field := range usageTokenFields {
				if field == "uncached_input_tokens" || field == "unclassified_tokens" {
					continue // Derived again by the session writer.
				}
				valid = valid && remaining[field] >= 0
				total[field] += remaining[field]
			}
		}
		for field, value := range total {
			valid = valid && value == counts[field]
		}
		valid = valid && total["total_tokens"] == counts["total_tokens"]
		if valid {
			for old, remaining := range prior {
				if old.hour == bucket.hour && old.effort == bucket.effort && old.attribution == bucket.attribution && remaining["total_tokens"] > 0 {
					if resolved[old] == nil {
						resolved[old] = tokenCounts{}
					}
					addTokenCounts(resolved[old], remaining)
				}
			}
		} else {
			bucket.model = "Unknown model"
			if resolved[bucket] == nil {
				resolved[bucket] = tokenCounts{}
			}
			addTokenCounts(resolved[bucket], counts)
		}
	}
	return resolved, nil
}
