package archive

import (
	"context"
	"database/sql"
	"strings"
)

func recordReasoningState(tx *sql.Tx, conversation string, messages []MessageRecord) error {
	count, bytes, summaries, unavailable := 0, 0, 0, 0
	for _, message := range messages {
		if message.Kind == "reasoning" || message.Kind == "thinking" || message.Kind == "analysis" {
			count++
			bytes += len(message.Text)
			if message.ReasoningFormat == "summary" {
				summaries++
			}
		} else if message.Kind == "metadata" && strings.HasPrefix(message.Text, `{"reason":`) {
			if value, ok := parseReaderJSON(message.Text); ok && firstString(mapValue(value)["type"]) == "reasoning_unavailable" {
				unavailable++
			}
		}
	}
	_, err := tx.Exec(`INSERT INTO reasoning_extraction_state(conversation_id,version,retained_count,retained_bytes,summary_count,unavailable_count)
	 VALUES(?,?,?,?,?,?) ON CONFLICT(conversation_id) DO UPDATE SET version=excluded.version,
	 retained_count=excluded.retained_count,retained_bytes=excluded.retained_bytes,summary_count=excluded.summary_count,unavailable_count=excluded.unavailable_count
	 WHERE (reasoning_extraction_state.version,reasoning_extraction_state.retained_count,reasoning_extraction_state.retained_bytes,reasoning_extraction_state.summary_count,reasoning_extraction_state.unavailable_count)
	 IS NOT (excluded.version,excluded.retained_count,excluded.retained_bytes,excluded.summary_count,excluded.unavailable_count)`,
		conversation, reasoningExtractionVersion, count, bytes, summaries, unavailable)
	return err
}

func (c *Catalog) pendingReasoning(ctx context.Context) (int64, error) {
	var count int64
	err := c.reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM conversations c LEFT JOIN reasoning_extraction_state s ON s.conversation_id=c.id
	 WHERE s.conversation_id IS NULL OR s.version<>?`, reasoningExtractionVersion).Scan(&count)
	return count, err
}

// Keep lightweight ordering/provenance for every row, but never load unrelated
// user prose or large tool results just to look for a thinking envelope.
func storedReasoningMessages(ctx context.Context, tx *sql.Tx, id string) ([]MessageRecord, error) {
	eligible := `m.kind IN ('thinking','reasoning','analysis') OR (m.role='assistant' AND m.kind='metadata') OR
	 (m.raw_text IS NOT NULL AND m.raw_text<>'' AND m.role NOT IN ('user','tool','agent') AND m.kind NOT IN ('tool_result','delegation_result','context'))`
	rows, err := queryMapsContext(ctx, tx, `SELECT m.native_id,m.role,m.kind,m.model,m.effort,m.created_at,
	 m.parent_native_id,m.previous_native_id,m.call_id,m.evidence_locator,m.source_order,m.selected,m.sender,m.reasoning_format,
	 CASE WHEN (`+eligible+`) THEN m.text ELSE '' END text,
	 CASE WHEN (`+eligible+`) THEN m.raw_text ELSE NULL END raw_text
	 FROM messages m WHERE m.conversation_id=? ORDER BY m.source_order IS NULL,m.source_order,m.created_at,m.id`, id)
	if err != nil {
		return nil, err
	}
	return storedMessageRecords(rows), nil
}

// BackfillReasoning commits one conversation and its completion marker together.
// Cancellation rolls back the current conversation; completed ones need not be
// read again. No query or catalog open runs this migration synchronously.
func (c *Catalog) BackfillReasoning(ctx context.Context, progress func(int, int)) (int, error) {
	var done int
	var after int64
	total, err := c.pendingReasoning(ctx)
	if err != nil {
		return 0, err
	}
	for {
		if err := ctx.Err(); err != nil {
			return done, err
		}
		var id, workspace string
		err := c.reader().QueryRowContext(ctx, `SELECT c.id,c.workspace_id,c.rowid FROM conversations c
		 LEFT JOIN reasoning_extraction_state s ON s.conversation_id=c.id
		 WHERE c.rowid>? AND (s.conversation_id IS NULL OR s.version<>?) ORDER BY c.rowid LIMIT 1`, after, reasoningExtractionVersion).Scan(&id, &workspace, &after)
		if err == sql.ErrNoRows {
			return done, nil
		}
		if err != nil {
			return done, err
		}
		err = c.writeTransaction(ctx, "reasoning-upgrade", func(tx *sql.Tx) error {
			// An ingest may have normalized this conversation since selection.
			var version string
			if err := tx.QueryRowContext(ctx, "SELECT version FROM reasoning_extraction_state WHERE conversation_id=?", id).Scan(&version); err == nil && version == reasoningExtractionVersion {
				return nil
			} else if err != nil && err != sql.ErrNoRows {
				return err
			}
			messages, err := storedReasoningMessages(ctx, tx, id)
			if err != nil {
				return err
			}
			normalized := normalizeReasoningMessages(messages)
			changed := len(messages) != len(normalized)
			for i, message := range normalized {
				if !changed && message != messages[i] {
					changed = true
				}
			}
			if changed {
				originals := map[string]MessageRecord{}
				for _, message := range messages {
					originals[message.NativeID] = message
				}
				orders := map[string]int{}
				toolsChanged := false
				for i, message := range normalized {
					if err := ctx.Err(); err != nil {
						return err
					}
					orders[message.NativeID] = i
					normalized[i].SourceOrder = i
					if original, exists := originals[message.NativeID]; exists && message == original {
						continue
					}
					toolsChanged = toolsChanged || message.Kind == "tool_call" || message.Kind == "tool_result" || message.Kind == "delegation" || message.Kind == "delegation_result"
					message.SourceOrder = i
					messageID, textChanged, err := upsertMessage(tx, id, message)
					if err != nil {
						return err
					}
					if textChanged {
						if err := replaceMessageFTS(tx, messageID); err != nil {
							return err
						}
					}
				}
				if _, err := tx.ExecContext(ctx, `UPDATE messages SET source_order=orders.value FROM json_each(?) orders
				 WHERE conversation_id=? AND native_id=orders.key AND source_order IS NOT orders.value`, jsonText(orders), id); err != nil {
					return err
				}
				if err := upsertConversationDocument(tx, id); err != nil {
					return err
				}
				var conversation ConversationRecord
				if err := tx.QueryRowContext(ctx, `SELECT provider,COALESCE(model,''),COALESCE(effort,''),COALESCE(started_at,''),agent_depth FROM conversations WHERE id=?`, id).
					Scan(&conversation.Provider, &conversation.Model, &conversation.Effort, &conversation.StartedAt, &conversation.AgentDepth); err != nil {
					return err
				}
				// Session assignment needs complete delegation and accounting bodies;
				// the lightweight extraction projection intentionally omits these.
				conversation.Messages, err = storedMessages(ctx, tx, id)
				if err != nil {
					return err
				}
				if err := refreshAgentSessionMembership(tx, workspace, id, conversation); err != nil {
					return err
				}
				if toolsChanged {
					if err := replaceToolLedger(tx, workspace, id, conversation); err != nil {
						return err
					}
				}
				if _, err := tx.Exec("DELETE FROM conversation_derivation_inputs WHERE conversation_id=?", id); err != nil {
					return err
				}
			}
			return recordReasoningState(tx, id, normalized)
		})
		if err != nil {
			return done, err
		}
		done++
		if progress != nil {
			progress(done, int(total))
		}
	}
}

// Counts come from the compact extraction ledger, not scans of message bodies.
// Complete here means retained envelopes have been normalized, never that a
// provider supplied its full reasoning or that old source history is present.
func (c *Catalog) reasoningCoverage(ctx context.Context) (map[string]any, error) {
	rows, err := queryMapsContext(ctx, c.reader(), `SELECT w.source_kind source,c.provider,
	 COUNT(*) conversations,
	 SUM(CASE WHEN s.version=? THEN 0 ELSE 1 END) pending_conversations,
	 COALESCE(SUM(CASE WHEN s.version=? THEN s.retained_count ELSE 0 END),0) retained_records,
	 COALESCE(SUM(CASE WHEN s.version=? THEN s.retained_bytes ELSE 0 END),0) retained_bytes,
	 COALESCE(SUM(CASE WHEN s.version=? THEN s.summary_count ELSE 0 END),0) summary_records,
	 COALESCE(SUM(CASE WHEN s.version=? THEN s.unavailable_count ELSE 0 END),0) unavailable_records
	 FROM conversations c JOIN workspaces w ON w.id=c.workspace_id
	 LEFT JOIN reasoning_extraction_state s ON s.conversation_id=c.id GROUP BY w.source_kind,c.provider`,
		reasoningExtractionVersion, reasoningExtractionVersion, reasoningExtractionVersion, reasoningExtractionVersion, reasoningExtractionVersion)
	if err != nil {
		return nil, err
	}
	pending, retained := int64(0), int64(0)
	for _, row := range rows {
		pending += integer(row["pending_conversations"])
		retained += integer(row["retained_records"])
		row["status"] = "normalized_retained_text"
		if integer(row["pending_conversations"]) > 0 {
			row["status"] = "backfill_pending"
		}
		if firstString(row["source"]) == "antigravity" {
			row["retention"] = "unsupported"
		} else {
			row["retention"] = "source_dependent"
		}
	}
	return map[string]any{"version": reasoningExtractionVersion, "scope": "catalog", "normalization_complete": pending == 0,
		"pending_conversations": pending, "retained_records": retained, "sources": rows,
		"source_history": "unknown", "detail": "Only saved plaintext reasoning and summaries. Redacted/encrypted content is unavailable. Older discarded events require reindexing available sources or captures."}, nil
}
