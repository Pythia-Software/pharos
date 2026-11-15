package archive

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/gbdubs/pharos/estimation"
)

const usageEstimationStoreVersion = "chat-estimates-v5-observed-model"

var usageEstimationBatch = 10

// Only scalar estimates are stored. Source transcripts and measured ledgers
// remain authoritative and are never replaced by predictions.
const usageEstimationStoreSQL = `
CREATE TABLE IF NOT EXISTS usage_estimation_inputs (
 conversation_id TEXT PRIMARY KEY REFERENCES conversations(id) ON DELETE CASCADE,
 revision INTEGER NOT NULL DEFAULT 1, built_revision INTEGER NOT NULL DEFAULT 0,
 estimator_key TEXT NOT NULL DEFAULT '', built_at TEXT,
 reported_responses INTEGER NOT NULL DEFAULT 0, unsupported_responses INTEGER NOT NULL DEFAULT 0
) WITHOUT ROWID;

`

func (c *Catalog) ensureUsageEstimationStore() error {
	tx, err := c.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(usageEstimationStoreSQL); err != nil {
		return err
	}
	var installed string
	err = tx.QueryRow("SELECT value FROM meta WHERE key='usage_estimation_store_version'").Scan(&installed)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if installed == usageEstimationStoreVersion {
		return tx.Commit()
	}
	if _, err := tx.Exec("DROP TABLE IF EXISTS usage_estimation_tools; DROP TABLE IF EXISTS usage_estimation_responses"); err != nil {
		return err
	}
	mark := func(ref string) string {
		return `INSERT INTO usage_estimation_inputs(conversation_id) SELECT c.id FROM conversations c
 WHERE c.id=` + ref + ` AND (c.harness='claude-ai-export' OR EXISTS(SELECT 1 FROM usage_estimation_inputs i WHERE i.conversation_id=c.id))
 ON CONFLICT(conversation_id) DO UPDATE SET revision=revision+1;`
	}
	markExisting := func(ref string) string {
		return clearUsageEstimatesSQL(ref) + "UPDATE usage_estimation_inputs SET revision=revision+1 WHERE conversation_id=" + ref + ";"
	}
	markSelection := func(ref string) string {
		ids := `(SELECT id FROM conversations WHERE harness='claude-ai-export' AND account=` + ref + `.account AND native_id=` + ref + `.conversation_uuid)`
		return `DELETE FROM model_requests WHERE is_estimate=1 AND conversation_id IN ` + ids + `;
 DELETE FROM agent_session_usage WHERE is_estimate=1 AND agent_session_id IN (SELECT id FROM agent_sessions WHERE conversation_id IN ` + ids + `);
 UPDATE agent_sessions SET input_tokens=0,uncached_input_tokens=0,cache_read_input_tokens=0,cache_creation_input_tokens=0,
 cache_creation_5m_input_tokens=0,cache_creation_1h_input_tokens=0,output_tokens=0,reasoning_output_tokens=0,unclassified_tokens=0,total_tokens=0,
 is_estimate=0,input_tokens_available=1,usage_estimate_json=NULL,usage_status='unavailable' WHERE is_estimate=1 AND conversation_id IN ` + ids + `;
 UPDATE tool_calls SET duration_ms=NULL,duration_source=NULL,duration_is_estimate=0,duration_estimate_json=NULL WHERE duration_is_estimate=1 AND conversation_id IN ` + ids + `;
 UPDATE usage_estimation_inputs SET revision=revision+1 WHERE conversation_id IN ` + ids + `;`
	}
	// Conversation triggers enroll eligible imports first. Message triggers
	// then touch that indexed row only, keeping native coding ingestion cheap.
	columns := []string{"conversation_id", "native_id", "role", "kind", "model", "effort", "text", "raw_text", "created_at", "parent_native_id", "call_id", "source_order", "selected"}
	changed := make([]string, len(columns))
	for i, name := range columns {
		changed[i] = "OLD." + name + " IS NOT NEW." + name
	}
	for _, t := range []struct{ name, event, body string }{
		{"message_insert", "AFTER INSERT ON messages WHEN EXISTS(SELECT 1 FROM usage_estimation_inputs WHERE conversation_id=NEW.conversation_id)", markExisting("NEW.conversation_id")},
		{"message_delete", "AFTER DELETE ON messages WHEN EXISTS(SELECT 1 FROM usage_estimation_inputs WHERE conversation_id=OLD.conversation_id)", markExisting("OLD.conversation_id")},
		{"message_update", "AFTER UPDATE OF " + strings.Join(columns, ",") + " ON messages WHEN EXISTS(SELECT 1 FROM usage_estimation_inputs WHERE conversation_id=OLD.conversation_id OR conversation_id=NEW.conversation_id) AND (" + strings.Join(changed, " OR ") + ")", markExisting("OLD.conversation_id") + markExisting("NEW.conversation_id")},
		{"reported_request_insert", "AFTER INSERT ON model_requests WHEN NEW.is_estimate=0 AND EXISTS(SELECT 1 FROM usage_estimation_inputs WHERE conversation_id=NEW.conversation_id)", clearUsageEstimatesSQL("NEW.conversation_id") + mark("NEW.conversation_id")},
		{"reported_request_update", "AFTER UPDATE OF output_tokens,input_tokens,total_tokens,is_estimate ON model_requests WHEN NEW.is_estimate=0 AND EXISTS(SELECT 1 FROM usage_estimation_inputs WHERE conversation_id=NEW.conversation_id)", clearUsageEstimatesSQL("NEW.conversation_id") + mark("NEW.conversation_id")},
		{"reported_usage_insert", "AFTER INSERT ON agent_session_usage WHEN NEW.is_estimate=0 AND EXISTS(SELECT 1 FROM usage_estimation_inputs WHERE conversation_id=(SELECT conversation_id FROM agent_sessions WHERE id=NEW.agent_session_id))", clearUsageEstimatesSQL("(SELECT conversation_id FROM agent_sessions WHERE id=NEW.agent_session_id)") + mark("(SELECT conversation_id FROM agent_sessions WHERE id=NEW.agent_session_id)")},
		{"reported_session_update", "AFTER UPDATE OF total_tokens,usage_status,is_estimate ON agent_sessions WHEN NEW.is_estimate=0 AND (NEW.total_tokens>0 OR NEW.usage_status LIKE 'reported%') AND EXISTS(SELECT 1 FROM usage_estimation_inputs WHERE conversation_id=NEW.conversation_id)", markExisting("NEW.conversation_id")},
		{"conversation_insert", "AFTER INSERT ON conversations", mark("NEW.id")},
		{"conversation_update", "AFTER UPDATE OF harness,provider,account,native_id ON conversations WHEN OLD.harness IS NOT NEW.harness OR OLD.provider IS NOT NEW.provider OR OLD.account IS NOT NEW.account OR OLD.native_id IS NOT NEW.native_id", clearUsageEstimatesSQL("NEW.id") + mark("NEW.id")},
		{"selection_insert", "AFTER INSERT ON claude_chat_settings_observations", markSelection("NEW")},
		{"selection_delete", "AFTER DELETE ON claude_chat_settings_observations", markSelection("OLD")},
		{"selection_update", "AFTER UPDATE OF account,organization_uuid,conversation_uuid,observed_at,model ON claude_chat_settings_observations WHEN OLD.account IS NOT NEW.account OR OLD.organization_uuid IS NOT NEW.organization_uuid OR OLD.conversation_uuid IS NOT NEW.conversation_uuid OR OLD.observed_at IS NOT NEW.observed_at OR OLD.model IS NOT NEW.model", markSelection("OLD") + markSelection("NEW")},
	} {
		if _, err := tx.Exec("DROP TRIGGER IF EXISTS usage_estimation_store_" + t.name + "; CREATE TRIGGER usage_estimation_store_" + t.name + " " + t.event + " BEGIN " + t.body + " END"); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO usage_estimation_inputs(conversation_id)
 SELECT id FROM conversations WHERE harness='claude-ai-export'
 ON CONFLICT(conversation_id) DO UPDATE SET revision=revision+1`); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO meta(key,value) VALUES('usage_estimation_store_version',?)
 ON CONFLICT(key) DO UPDATE SET value=excluded.value`, usageEstimationStoreVersion); err != nil {
		return err
	}
	return tx.Commit()
}

// Equal rate groups share a key even after a daily calibration pass. Empty or
// unavailable calibration uses the same published-only key in a chat-only library.
func usageEstimationSnapshotKey(book estimationCalibration) string {
	local := "published"
	if len(book.Groups) > 0 {
		local = hashBytes([]byte(jsonText(book.Groups)))
	}
	return usageEstimationStoreVersion + ":" + usageEstimationIdentity() + ":" + local
}

func (c *Catalog) usageEstimationKey(ctx context.Context) (string, error) {
	book, err := c.loadEstimationCalibration(ctx)
	return usageEstimationSnapshotKey(book), err
}

const pendingUsageEstimationSQL = `SELECT conversation_id FROM usage_estimation_inputs
 WHERE revision<>built_revision OR estimator_key<>? ORDER BY conversation_id LIMIT ?`

// A batch commits one conversation at a time. Cancellation leaves completed
// conversations usable and every uncommitted or superseded conversation pending.
func (c *Catalog) backfillUsageEstimationBatch(ctx context.Context) (bool, error) {
	key, err := c.usageEstimationKey(ctx)
	if err != nil {
		return false, err
	}
	rows, err := queryMapsContext(ctx, c.reader(), pendingUsageEstimationSQL, key, usageEstimationBatch)
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		if err := c.rebuildUsageEstimationConversation(ctx, firstString(row["conversation_id"])); err != nil {
			return false, err
		}
	}
	// Re-read the key: a changed calibration can invalidate earlier rows while
	// the pass ran. The writer guard also checks it for each publication.
	key, err = c.usageEstimationKey(ctx)
	if err != nil {
		return false, err
	}
	var more bool
	err = c.reader().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM usage_estimation_inputs WHERE revision<>built_revision OR estimator_key<>?)`, key).Scan(&more)
	return more, err
}

func (c *Catalog) backfillUsageEstimation(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		more, err := c.backfillUsageEstimationBatch(ctx)
		if err != nil || !more {
			return err
		}
	}
}

func (c *Catalog) rebuildUsageEstimationConversation(ctx context.Context, id string) error {
	tx, err := c.reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var revision int64
	var harness, account, nativeID string
	err = tx.QueryRowContext(ctx, `SELECT i.revision,c.harness,c.account,c.native_id FROM usage_estimation_inputs i JOIN conversations c ON c.id=i.conversation_id WHERE i.conversation_id=?`, id).Scan(&revision, &harness, &account, &nativeID)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	book, err := c.loadEstimationCalibrationFrom(ctx, tx)
	if err != nil {
		return err
	}
	key := usageEstimationSnapshotKey(book)
	manifest, err := bundledEstimation()
	if err != nil {
		return err
	}
	builder := newUsageEstimationBuilder(harness)
	report := &conversationUsageEstimates{Responses: []usageResponseEstimate{}}
	if builder != nil {
		builder.selections, err = claudeConversationSettingsFrom(ctx, tx, account, nativeID)
		if err != nil {
			return err
		}
		// Stream the same columns the reader observes, without retaining raw
		// bodies or a second array of messages. Source order is sorted by the
		// shared builder; SQLite need not sort large transcript bodies.
		rows, err := tx.QueryContext(ctx, "SELECT "+readerMessageColumns+",source_order FROM messages WHERE conversation_id=?", id)
		if err != nil {
			return err
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			return err
		}
		values, pointers := make([]any, len(columns)), make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		for rows.Next() {
			if err := rows.Scan(pointers...); err != nil {
				rows.Close()
				return err
			}
			row := make(map[string]any, len(columns))
			for i, name := range columns {
				if data, ok := values[i].([]byte); ok {
					row[name] = string(data)
				} else {
					row[name] = values[i]
				}
			}
			builder.observe(row)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		report = builder.estimate(manifest, book, hashBytes(estimation.Assumptions))
	}
	if err := tx.Rollback(); err != nil {
		return err
	}
	return c.storeUsageEstimationConversation(ctx, id, revision, key, report)
}

func (c *Catalog) storeUsageEstimationConversation(ctx context.Context, id string, revision int64, key string, report *conversationUsageEstimates) error {
	return c.writeTransaction(ctx, "backfill chat estimates", func(writer *sql.Tx) error {
		var current int64
		err := writer.QueryRowContext(ctx, "SELECT revision FROM usage_estimation_inputs WHERE conversation_id=?", id).Scan(&current)
		if err == sql.ErrNoRows {
			return nil
		}
		if err != nil || current != revision {
			return err // superseded input stays pending
		}
		currentBook, err := c.loadEstimationCalibrationFrom(ctx, writer)
		if err != nil || usageEstimationSnapshotKey(currentBook) != key {
			return err
		}
		if _, err := writer.ExecContext(ctx, clearUsageEstimatesSQL("?"), repeatedEstimateConversationArgs(id)...); err != nil {
			return err
		}
		authoritative, err := authoritativeUsageExists(ctx, writer, id)
		if err != nil {
			return err
		}
		if !authoritative && report.ReportedResponses == 0 {
			if err := publishCanonicalUsageEstimates(ctx, writer, id, key, report); err != nil {
				return err
			}
		}

		_, err = writer.ExecContext(ctx, `UPDATE usage_estimation_inputs SET built_revision=?,estimator_key=?,built_at=?,reported_responses=?,unsupported_responses=? WHERE conversation_id=?`, revision, key, now(), report.ReportedResponses, report.UnsupportedResponses, id)
		return err
	})
}

func (c *Catalog) UsageEstimationStatus(ctx context.Context) (map[string]any, error) {
	key, err := c.usageEstimationKey(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := queryMapsContext(ctx, c.reader(), `SELECT COUNT(*) conversations,
 COALESCE(SUM(CASE WHEN revision=built_revision AND estimator_key=? THEN 1 ELSE 0 END),0) current_conversations,
 COALESCE(SUM(CASE WHEN revision=built_revision AND estimator_key=? THEN reported_responses ELSE 0 END),0) reported_responses,
 COALESCE(SUM(CASE WHEN revision=built_revision AND estimator_key=? THEN unsupported_responses ELSE 0 END),0) unsupported_responses,
 MAX(built_at) last_built_at FROM usage_estimation_inputs`, key, key, key)
	if err != nil {
		return nil, err
	}
	if len(rows) != 1 {
		return nil, fmt.Errorf("missing chat estimation status")
	}
	status := rows[0]
	status["pending_conversations"] = integer(status["conversations"]) - integer(status["current_conversations"])
	status["version"], status["estimator_key"] = usageEstimatorVersion, key
	status["range_kind"] = "sensitivity_not_confidence"
	c.tools.mu.Lock()
	status["calibration_error"] = nilIfEmpty(c.tools.estimationError)
	c.tools.mu.Unlock()
	status["scope"] = "selected_branch_usage_in_conversations_without_reported_usage"
	return status, nil
}
