package archive

import "database/sql"

const teamsCodexRequestTriggerSQL = `-- Any changed indexed request message or ledger invalidates the local profile.
-- Ingest may rebuild it only after both representations agree in one commit.
CREATE TRIGGER IF NOT EXISTS teams_codex_profiles_deleted AFTER DELETE ON teams_codex_request_profiles
BEGIN UPDATE teams_source_revision SET revision=revision+1 WHERE id=1; END;
CREATE TRIGGER IF NOT EXISTS teams_codex_message_insert AFTER INSERT ON messages
BEGIN DELETE FROM teams_codex_request_profiles WHERE conversation_id=new.conversation_id; END;
CREATE TRIGGER IF NOT EXISTS teams_codex_message_update AFTER UPDATE ON messages
WHEN (old.conversation_id,old.native_id,old.role,old.kind,old.model,old.effort,old.text,old.raw_text,old.source_order,old.created_at,old.selected)
 IS NOT (new.conversation_id,new.native_id,new.role,new.kind,new.model,new.effort,new.text,new.raw_text,new.source_order,new.created_at,new.selected)
BEGIN DELETE FROM teams_codex_request_profiles WHERE conversation_id=old.conversation_id OR conversation_id=new.conversation_id; END;
CREATE TRIGGER IF NOT EXISTS teams_codex_message_delete AFTER DELETE ON messages
BEGIN DELETE FROM teams_codex_request_profiles WHERE conversation_id=old.conversation_id; END;
CREATE TRIGGER IF NOT EXISTS teams_codex_session_insert AFTER INSERT ON agent_sessions
BEGIN DELETE FROM teams_codex_request_profiles WHERE conversation_id=new.conversation_id; END;
CREATE TRIGGER IF NOT EXISTS teams_codex_session_delete BEFORE DELETE ON agent_sessions
BEGIN DELETE FROM teams_codex_request_profiles WHERE conversation_id=old.conversation_id; END;
CREATE TRIGGER IF NOT EXISTS teams_codex_session_update AFTER UPDATE ON agent_sessions
WHEN (old.id,old.conversation_id,old.native_id,old.total_tokens,old.is_estimate) IS NOT (new.id,new.conversation_id,new.native_id,new.total_tokens,new.is_estimate)
BEGIN DELETE FROM teams_codex_request_profiles WHERE conversation_id=old.conversation_id OR conversation_id=new.conversation_id; END;
CREATE TRIGGER IF NOT EXISTS teams_codex_usage_insert AFTER INSERT ON agent_session_usage
BEGIN DELETE FROM teams_codex_request_profiles WHERE conversation_id=(SELECT conversation_id FROM agent_sessions WHERE id=new.agent_session_id); END;
CREATE TRIGGER IF NOT EXISTS teams_codex_usage_update AFTER UPDATE ON agent_session_usage
WHEN (old.agent_session_id,old.usage_hour,old.model,old.effort,old.attribution,old.input_tokens,old.uncached_input_tokens,old.cache_read_input_tokens,old.cache_creation_input_tokens,old.cache_creation_5m_input_tokens,old.cache_creation_1h_input_tokens,old.output_tokens,old.reasoning_output_tokens,old.unclassified_tokens,old.total_tokens,old.is_estimate,old.input_tokens_available)
 IS NOT (new.agent_session_id,new.usage_hour,new.model,new.effort,new.attribution,new.input_tokens,new.uncached_input_tokens,new.cache_read_input_tokens,new.cache_creation_input_tokens,new.cache_creation_5m_input_tokens,new.cache_creation_1h_input_tokens,new.output_tokens,new.reasoning_output_tokens,new.unclassified_tokens,new.total_tokens,new.is_estimate,new.input_tokens_available)
BEGIN DELETE FROM teams_codex_request_profiles WHERE conversation_id IN (SELECT conversation_id FROM agent_sessions WHERE id IN (old.agent_session_id,new.agent_session_id)); END;
CREATE TRIGGER IF NOT EXISTS teams_codex_usage_delete AFTER DELETE ON agent_session_usage
BEGIN DELETE FROM teams_codex_request_profiles WHERE conversation_id=(SELECT conversation_id FROM agent_sessions WHERE id=old.agent_session_id); END;
`

// Install after legacy column and usage-ledger migrations. Revisit prior opt-in
// Codex sources once; ordinary libraries do not acquire additional proof.
func (c *Catalog) ensureTeamsCodexRequestSchema() error {
	tx, err := c.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(teamsCodexRequestTriggerSQL); err != nil {
		return err
	}
	result, err := tx.Exec(`INSERT OR IGNORE INTO meta(key,value) VALUES('teams_codex_request_profile_reindex','1')`)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n > 0 {
		var requested bool
		if err = tx.QueryRow(`SELECT proof_requested FROM teams_source_revision WHERE id=1`).Scan(&requested); err != nil && err != sql.ErrNoRows {
			return err
		}
		if requested {
			if _, err = tx.Exec(`INSERT INTO sync_generations(host_id,source_name,generation) SELECT host_id,source_name,1 FROM source_states WHERE lower(kind)='codex' ON CONFLICT(host_id,source_name) DO UPDATE SET generation=sync_generations.generation+1`); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
