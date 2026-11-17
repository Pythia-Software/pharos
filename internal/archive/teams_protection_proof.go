package archive

import "database/sql"

// Install after the legacy identity/privacy migrations. Retained flags include
// reviewed v1 flags: classifier progress is not release of an assertion.
func (c *Catalog) ensureTeamsProtectionProofSchema() error {
	tx, err := c.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// A persisted baseline cannot prove continuity after an offline restore.
	// Reconciliation must re-establish it for this open before any use.
	if _, err = tx.Exec(`UPDATE teams_protection_baseline SET state='held',generation=generation+1 WHERE id=1 AND state='current'`); err != nil {
		return err
	}
	marker, err := tx.Exec(`INSERT OR IGNORE INTO meta(key,value) VALUES('teams_protection_legacy_assertions_v1','1')`)
	if err != nil {
		return err
	}
	newMigration, err := marker.RowsAffected()
	if err != nil {
		return err
	}
	if newMigration > 0 {
		_, err = tx.Exec(`INSERT INTO teams_protection_assertions(conversation_id,provider,account,native_id,kind,evidence,active,created_revision)
 SELECT p.conversation_id,p.provider,p.account,p.native_id,'legacy_hold',p.classifier_input,1,r.revision FROM teams_conversation_privacy p CROSS JOIN teams_privacy_revision r WHERE r.id=1 AND p.classifier_state<>'flagged' AND NOT (p.classifier_state='unclassified' AND p.classifier_input='' AND p.classifier_reviewed=0) AND NOT EXISTS(SELECT 1 FROM teams_protection_assertions a WHERE a.conversation_id=p.conversation_id AND a.provider=p.provider AND a.account=p.account AND a.native_id=p.native_id AND a.kind='legacy_hold' AND a.evidence=p.classifier_input AND a.active=1)`)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT INTO teams_protection_assertions(conversation_id,provider,account,native_id,kind,evidence,active,created_revision)
 SELECT p.conversation_id,p.provider,p.account,p.native_id,'explicit_private','legacy',1,r.revision FROM teams_conversation_privacy p CROSS JOIN teams_privacy_revision r WHERE p.user_private=1 AND r.id=1 AND NOT EXISTS(SELECT 1 FROM teams_protection_assertions a WHERE a.conversation_id=p.conversation_id AND a.provider=p.provider AND a.account=p.account AND a.native_id=p.native_id AND a.kind='explicit_private' AND a.active=1);
 INSERT INTO teams_protection_assertions(conversation_id,provider,account,native_id,kind,evidence,active,created_revision)
 SELECT p.conversation_id,p.provider,p.account,p.native_id,'positive_flag',p.classifier_input,1,r.revision FROM teams_conversation_privacy p CROSS JOIN teams_privacy_revision r WHERE p.classifier_state='flagged' AND r.id=1 AND NOT EXISTS(SELECT 1 FROM teams_protection_assertions a WHERE a.conversation_id=p.conversation_id AND a.provider=p.provider AND a.account=p.account AND a.native_id=p.native_id AND a.kind='positive_flag' AND a.evidence=p.classifier_input AND a.active=1)`)
		if err != nil {
			return err
		}
	}
	// Recreate only the triggers owned here; other proof guards may be added
	// independently and must survive initialization.
	if _, err = tx.Exec(`DROP TRIGGER IF EXISTS teams_protection_privacy_insert;
DROP TRIGGER IF EXISTS teams_protection_privacy_update;
DROP TRIGGER IF EXISTS teams_protection_privacy_delete;
DROP TRIGGER IF EXISTS teams_protection_source_revision;
DROP TRIGGER IF EXISTS teams_protection_mapping_insert;
DROP TRIGGER IF EXISTS teams_protection_mapping_update;
DROP TRIGGER IF EXISTS teams_protection_mapping_delete;
DROP TRIGGER IF EXISTS teams_protection_settings_update;
DROP TRIGGER IF EXISTS teams_protection_link_insert;
DROP TRIGGER IF EXISTS teams_protection_link_update;
DROP TRIGGER IF EXISTS teams_protection_link_delete;`); err != nil {
		return err
	}
	_, err = tx.Exec(` CREATE TRIGGER IF NOT EXISTS teams_protection_privacy_insert AFTER INSERT ON teams_conversation_privacy BEGIN
 INSERT OR IGNORE INTO teams_protection_assertions(conversation_id,provider,account,native_id,kind,evidence,active,created_revision) SELECT new.conversation_id,new.provider,new.account,new.native_id,'explicit_private','explicit',1,revision FROM teams_privacy_revision WHERE id=1 AND new.user_private=1 AND NOT EXISTS(SELECT 1 FROM teams_protection_assertions WHERE conversation_id=new.conversation_id AND provider=new.provider AND account=new.account AND native_id=new.native_id AND kind='explicit_private' AND active=1);
 INSERT OR IGNORE INTO teams_protection_assertions(conversation_id,provider,account,native_id,kind,evidence,active,created_revision) SELECT new.conversation_id,new.provider,new.account,new.native_id,'positive_flag',new.classifier_input,1,revision FROM teams_privacy_revision WHERE id=1 AND new.classifier_state='flagged' AND NOT EXISTS(SELECT 1 FROM teams_protection_assertions WHERE conversation_id=new.conversation_id AND provider=new.provider AND account=new.account AND native_id=new.native_id AND kind='positive_flag' AND evidence=new.classifier_input AND active=1);
 UPDATE teams_protection_baseline SET state='held',generation=generation+1 WHERE id=1;
 END;
 CREATE TRIGGER IF NOT EXISTS teams_protection_privacy_update AFTER UPDATE ON teams_conversation_privacy BEGIN
 INSERT OR IGNORE INTO teams_protection_assertions(conversation_id,provider,account,native_id,kind,evidence,active,created_revision) SELECT new.conversation_id,new.provider,new.account,new.native_id,'explicit_private','explicit',1,revision FROM teams_privacy_revision WHERE id=1 AND new.user_private=1 AND NOT EXISTS(SELECT 1 FROM teams_protection_assertions WHERE conversation_id=new.conversation_id AND provider=new.provider AND account=new.account AND native_id=new.native_id AND kind='explicit_private' AND active=1);
 INSERT OR IGNORE INTO teams_protection_assertions(conversation_id,provider,account,native_id,kind,evidence,active,created_revision) SELECT old.conversation_id,old.provider,old.account,old.native_id,'positive_flag',old.classifier_input,1,revision FROM teams_privacy_revision WHERE id=1 AND old.classifier_state='flagged' AND NOT EXISTS(SELECT 1 FROM teams_protection_assertions WHERE conversation_id=old.conversation_id AND provider=old.provider AND account=old.account AND native_id=old.native_id AND kind='positive_flag' AND evidence=old.classifier_input AND active=1);
 INSERT OR IGNORE INTO teams_protection_assertions(conversation_id,provider,account,native_id,kind,evidence,active,created_revision) SELECT new.conversation_id,new.provider,new.account,new.native_id,'positive_flag',new.classifier_input,1,revision FROM teams_privacy_revision WHERE id=1 AND new.classifier_state='flagged' AND NOT EXISTS(SELECT 1 FROM teams_protection_assertions WHERE conversation_id=new.conversation_id AND provider=new.provider AND account=new.account AND native_id=new.native_id AND kind='positive_flag' AND evidence=new.classifier_input AND active=1);
 UPDATE teams_protection_baseline SET state='held',generation=generation+1 WHERE id=1;
 END;
 CREATE TRIGGER IF NOT EXISTS teams_protection_privacy_delete AFTER DELETE ON teams_conversation_privacy BEGIN
 INSERT OR IGNORE INTO teams_protection_assertions(conversation_id,provider,account,native_id,kind,evidence,active,created_revision) SELECT old.conversation_id,old.provider,old.account,old.native_id,'positive_flag',old.classifier_input,1,revision FROM teams_privacy_revision WHERE id=1 AND old.classifier_state='flagged' AND NOT EXISTS(SELECT 1 FROM teams_protection_assertions WHERE conversation_id=old.conversation_id AND provider=old.provider AND account=old.account AND native_id=old.native_id AND kind='positive_flag' AND evidence=old.classifier_input AND active=1);
 UPDATE teams_protection_baseline SET state='held',generation=generation+1 WHERE id=1;
 END;
 CREATE TRIGGER IF NOT EXISTS teams_protection_source_revision AFTER UPDATE OF revision ON teams_source_revision WHEN new.revision<>old.revision BEGIN UPDATE teams_protection_baseline SET state='held',generation=generation+1 WHERE id=1; END;
 CREATE TRIGGER IF NOT EXISTS teams_protection_mapping_insert AFTER INSERT ON teams_source_mapping_current BEGIN UPDATE teams_protection_baseline SET state='held',generation=generation+1 WHERE id=1; END;
 CREATE TRIGGER IF NOT EXISTS teams_protection_mapping_update AFTER UPDATE ON teams_source_mapping_current BEGIN UPDATE teams_protection_baseline SET state='held',generation=generation+1 WHERE id=1; END;
 CREATE TRIGGER IF NOT EXISTS teams_protection_mapping_delete AFTER DELETE ON teams_source_mapping_current BEGIN UPDATE teams_protection_baseline SET state='held',generation=generation+1 WHERE id=1; END;
 CREATE TRIGGER IF NOT EXISTS teams_protection_settings_update AFTER UPDATE ON teams_local_settings WHEN (old.library_id,old.instance_id,old.org_id,old.installation_id,old.key_fingerprint,old.enabled,old.metrics_opt_out,old.acknowledged_policy,old.settings_revision) IS NOT (new.library_id,new.instance_id,new.org_id,new.installation_id,new.key_fingerprint,new.enabled,new.metrics_opt_out,new.acknowledged_policy,new.settings_revision) BEGIN UPDATE teams_protection_baseline SET state='held',generation=generation+1 WHERE id=1; END;
 CREATE TRIGGER IF NOT EXISTS teams_protection_link_insert AFTER INSERT ON conversation_identity_links BEGIN UPDATE teams_protection_baseline SET state='held',generation=generation+1 WHERE id=1; END;
 CREATE TRIGGER IF NOT EXISTS teams_protection_link_update AFTER UPDATE ON conversation_identity_links BEGIN UPDATE teams_protection_baseline SET state='held',generation=generation+1 WHERE id=1; END;
 CREATE TRIGGER IF NOT EXISTS teams_protection_link_delete AFTER DELETE ON conversation_identity_links BEGIN UPDATE teams_protection_baseline SET state='held',generation=generation+1 WHERE id=1; END;`)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// Retire exactly the local explicit assertion; classifier and lineage guards
// remain untouched. A retirement is durable even on a false-to-false action.
// Revisions are observations of the global privacy revision at the time of
// ledger mutation, not action ordering. IDs provide durable ledger ordering.
func teamsRetireExplicitProtection(tx *sql.Tx, p teamsPrivacy) error {
	result, err := tx.Exec(`INSERT INTO teams_protection_actions(conversation_id,provider,account,native_id,kind,observed_revision) VALUES(?,?,?,?,'explicit_unmark',?)`, p.ConversationID, p.provider, p.account, p.native, p.Revision)
	if err != nil {
		return err
	}
	actionID, err := result.LastInsertId()
	if err != nil {
		return err
	}
	result, err = tx.Exec(`UPDATE teams_protection_assertions SET active=0,retired_revision=?,retirement_evidence='explicit_unmark',retired_by_action_id=? WHERE conversation_id=? AND provider=? AND account=? AND native_id=? AND kind='explicit_private' AND active=1`, p.Revision, actionID, p.ConversationID, p.provider, p.account, p.native)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE teams_protection_actions SET retired_count=? WHERE action_id=?`, n, actionID); err != nil {
		return err
	}
	if n > 0 {
		_, err = tx.Exec(`UPDATE teams_protection_baseline SET state='held',generation=generation+1 WHERE id=1`)
	}
	return err
}
