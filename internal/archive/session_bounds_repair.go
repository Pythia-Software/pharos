package archive

import (
	"context"
	"database/sql"
	"encoding/json"
)

var sessionBoundsRepairBatchSize = 100

type sessionBoundsRepairResult struct {
	Examined   int            `json:"examined"`
	Eligible   int            `json:"eligible"`
	WithTokens int            `json:"eligible_with_tokens"`
	Updated    int            `json:"updated"`
	Skipped    map[string]int `json:"skipped"`
}

// Unlike RepairUsageAttribution, this only fills supported, wholly missing
// root timing. No usage, request, pricing, identity or version stamp is rebuilt.
// It revalidates each batch under the catalog writer and commits at most 100
// sessions at a time. Preview uses read transactions and never initializes data.
func (c *Catalog) RepairSessionBounds(ctx context.Context, apply bool) (sessionBoundsRepairResult, error) {
	result := sessionBoundsRepairResult{Skipped: map[string]int{}}
	after := ""
	for {
		last, count := after, 0
		batch := sessionBoundsRepairResult{Skipped: map[string]int{}}
		visit := func(tx *sql.Tx) error {
			last, count = after, 0
			batch = sessionBoundsRepairResult{Skipped: map[string]int{}}
			rows, err := queryMapsContext(ctx, tx, `SELECT s.id session_id,s.total_tokens,c.*,
			 a.installation_id,a.attempt_id,a.started_at attempt_start,a.completed_at attempt_end,a.transcripts_json,
			 (SELECT COUNT(*) FROM tl1_attempts a2 WHERE a2.conversation_native_id=c.native_id) attempt_matches,
			 EXISTS(SELECT 1 FROM messages m WHERE m.conversation_id=c.id AND COALESCE(m.created_at,'')<>'') timed_messages
			 FROM agent_sessions s JOIN conversations c ON c.id=s.conversation_id
			 LEFT JOIN tl1_attempts a ON a.conversation_native_id=c.native_id
			 WHERE s.id>? AND s.native_id='main' AND s.kind='root' AND s.depth=0
			 AND s.parent_id IS NULL AND s.child_conversation_id IS NULL
			 AND c.agent_depth=0 AND c.parent_id IS NULL
			 AND COALESCE(s.started_at,'')='' AND COALESCE(s.ended_at,'')=''
			 AND s.time_bounds_source='unavailable'
			 ORDER BY s.id LIMIT ?`, after, sessionBoundsRepairBatchSize)
			if err != nil {
				return err
			}
			seen := map[string]bool{}
			for _, row := range rows {
				last = firstString(row["session_id"])
				if seen[last] {
					continue
				}
				seen[last], count = true, count+1
				batch.Examined++
				bounds, reason := repairableSessionBounds(row)
				if reason != "" {
					batch.Skipped[reason]++
					continue
				}
				batch.Eligible++
				if integer(row["total_tokens"]) > 0 {
					batch.WithTokens++
				}
				if !apply {
					continue
				}
				if _, err := tx.ExecContext(ctx, `UPDATE conversations SET session_bounds_json=? WHERE id=?`, sessionBoundsJSON(bounds), row["id"]); err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `UPDATE agent_sessions SET started_at=?,ended_at=?,time_bounds_source=? WHERE id=?`, bounds.StartedAt, nilIfEmpty(bounds.EndedAt), bounds.Source, last); err != nil {
					return err
				}
				batch.Updated++
			}
			return nil
		}
		var err error
		if apply {
			err = c.writeTransaction(ctx, "session-bounds-repair", visit)
		} else {
			var tx *sql.Tx
			tx, err = c.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
			if err == nil {
				err = visit(tx)
				_ = tx.Rollback()
			}
		}
		if err != nil {
			return result, err
		}
		result.Examined += batch.Examined
		result.Eligible += batch.Eligible
		result.WithTokens += batch.WithTokens
		result.Updated += batch.Updated
		for reason, n := range batch.Skipped {
			result.Skipped[reason] += n
		}
		if count == 0 {
			return result, nil
		}
		after = last
		if apply {
			c.boundWAL(ctx, walSizeLimit)
		}
	}
}

func repairableSessionBounds(row map[string]any) (*SessionTimeBounds, string) {
	if integer(row["timed_messages"]) != 0 {
		return nil, "timed-messages"
	}
	c := ConversationRecord{Coverage: firstString(row["coverage"]), SessionBounds: decodeSessionBounds(row["session_bounds_json"])}
	if bounds := rootSessionEnvelope(c); bounds != nil {
		return bounds, ""
	}
	if integer(row["attempt_matches"]) != 1 {
		return nil, "missing-or-ambiguous-attempt"
	}
	var names []string
	if json.Unmarshal([]byte(firstString(row["transcripts_json"])), &names) != nil || len(names) != 1 || names[0] == "" {
		return nil, "multiple-or-missing-transcripts"
	}
	if firstString(row["native_id"]) != tl1ConversationNativeID(firstString(row["installation_id"]), firstString(row["attempt_id"]), names[0]) {
		return nil, "transcript-identity-mismatch"
	}
	bounds := attemptSessionBounds(firstString(row["attempt_start"]), firstString(row["attempt_end"]))
	if bounds == nil || bounds.StartedAt != iso(row["started_at"]) ||
		// A historical start-as-end is only tolerated when completion is
		// absent. It supplies a start, never a completed zero-duration run.
		(bounds.EndedAt != iso(row["ended_at"]) && !(firstString(row["attempt_end"]) == "" && iso(row["ended_at"]) == bounds.StartedAt)) {
		return nil, "invalid-or-mismatching-bounds"
	}
	c.SessionBounds = bounds
	if rootSessionEnvelope(c) == nil {
		return nil, "partial-transcript"
	}
	return bounds, ""
}
