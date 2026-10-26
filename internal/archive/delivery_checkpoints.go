package archive

import (
	"context"
	"database/sql"
)

// Reuse the normalized input fingerprint recorded during ingestion. It covers
// metadata, aliases and instructions as well as messages, without reconstructing
// inputs from derived SQL rows or loading the source's transcripts during cleanup.
func conversationDeliveryDigest(workspaceID string, c ConversationRecord) string {
	return hashBytes([]byte(workspaceID + ":" + conversationDerivationVersion + ":" + jsonText(c)))
}

func workspaceDeliveryDigest(record WorkspaceRecord) string {
	record.Conversations = nil
	return hashBytes([]byte(jsonText(record)))
}

func deliveredWorkspaceCurrent(tx *sql.Tx, record WorkspaceRecord, digest string) (bool, error) {
	var prior string
	err := tx.QueryRow(`SELECT digest FROM library_workspace_delivery_checkpoints
 WHERE source_kind=? AND source_account=? AND source_id=?`, record.SourceKind, record.Account, record.SourceID).Scan(&prior)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil && digest == prior, err
}

// A newly changed conversation can return to pending. An unchanged one cannot
// return merely because another session in the same source database changed.
func suppressDeliveredConversations(tx *sql.Tx, record WorkspaceRecord) (WorkspaceRecord, bool, error) {
	var checkpoints int
	if err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM library_delivery_checkpoints)").Scan(&checkpoints); err != nil {
		return record, false, err
	}
	if checkpoints == 0 {
		return record, false, nil
	}
	workspaceID, err := storedWorkspaceID(tx, record)
	if err != nil {
		return record, false, err
	}
	if workspaceID == "" {
		workspaceID = stableID("workspace", record.SourceKind, record.Account, record.SourceID)
	}
	kept := make([]ConversationRecord, 0, len(record.Conversations))
	removed := false
	for _, conversation := range record.Conversations {
		var digest string
		err := tx.QueryRow(`SELECT digest FROM library_delivery_checkpoints d WHERE provider=? AND account=? AND native_id=?
 AND NOT EXISTS(SELECT 1 FROM conversations c WHERE c.provider=d.provider AND c.account=d.account AND c.native_id=d.native_id)`, conversation.Provider, conversation.Account, conversation.NativeID).Scan(&digest)
		if err != nil && err != sql.ErrNoRows {
			return record, false, err
		}
		if err == nil && digest == conversationDeliveryDigest(workspaceID, conversation) {
			removed = true
			continue
		}
		kept = append(kept, conversation)
	}
	if removed {
		record.Conversations = kept
		record.Metrics = nil
	}
	return record, removed, nil
}

// The caller attached the intact source as prior inside the clean catalog's
// transaction. Missing fingerprints remain eligible for recapture; incomplete
// evidence must never be interpreted as proof that a revision was delivered.
func retainDeliveryCheckpoints(ctx context.Context, tx *sql.Tx, receipt libraryTransferReceipt) error {
	_, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO library_delivery_checkpoints(provider,account,native_id,digest,workspace_id,destination_instance,delivery_id)
 SELECT c.provider,c.account,c.native_id,i.fingerprint,c.workspace_id,?,? FROM prior.conversations c
 JOIN prior.conversation_derivation_inputs i ON i.conversation_id=c.id`, receipt.Target, receipt.ID)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT OR REPLACE INTO library_workspace_delivery_checkpoints(source_kind,source_account,source_id,digest,destination_instance,delivery_id)
 SELECT w.source_kind,w.source_account,w.source_id,i.fingerprint,?,? FROM prior.workspaces w
 JOIN prior.workspace_delivery_inputs i ON i.workspace_id=w.id`, receipt.Target, receipt.ID)
	return err
}
