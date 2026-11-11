package archive

import (
	"context"
	"database/sql"
	"strings"
)

func recordPayloadDigest(digest, version string) string {
	payload := strings.TrimPrefix(digest, version+":")
	if len(payload) > 65 && payload[64] == ':' && validRecordHash(payload[:64]) {
		payload = payload[65:]
	}
	return version + ":" + payload
}

// A raw export may change without changing parsed content (for example JSON
// whitespace). Advance its evidence and parts atomically, leaving all parsed
// messages, search and derived rows alone. Authority still rejects older or
// conflicting same-version inputs.
func (c *Catalog) acceptUnchangedEvidence(ctx context.Context, adapter Adapter, record WorkspaceRecord, parts []sourcePart, tracker *partTracker, digest, payload, version string) (bool, error) {
	host, source := sourceHost(adapter).ID, adapter.Config().Name
	tx, err := c.beginWrite(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var current string
	if err = tx.QueryRow(`SELECT digest FROM source_record_states WHERE host_id=? AND source_name=? AND source_kind=? AND source_account=? AND source_id=?`, host, source, record.SourceKind, record.Account, record.SourceID).Scan(&current); err != nil {
		return false, err
	}
	if recordPayloadDigest(current, version) != payload {
		return false, nil
	}
	workspace, err := storedWorkspaceID(tx, record)
	if err != nil {
		return false, err
	}
	// A verified move keeps parser checkpoints while removing the workspace.
	// The ordinary writer must reapply delivery suppression before associating
	// new evidence; there is no workspace here for copy_evidence to reference.
	if workspace == "" {
		return false, nil
	}
	authority, err := resolveAuthority(tx, record, workspace, host)
	if err != nil {
		return false, err
	}
	if err = guardCapture(tx, record, workspace, host, &authority); err != nil {
		return false, err
	}
	if authority.older {
		return false, nil
	}
	ids := make([]string, len(authority.conversations))
	for i, claim := range authority.conversations {
		ids[i] = claim.id
	}
	if err = recordCopyVersions(tx, host, workspace, ids, record, true); err != nil {
		return false, err
	}
	if _, err = tx.Exec(`UPDATE source_record_states SET digest=?,updated_at=? WHERE host_id=? AND source_name=? AND source_kind=? AND source_account=? AND source_id=?`, digest, now(), host, source, record.SourceKind, record.Account, record.SourceID); err != nil {
		return false, err
	}
	if tracker != nil {
		if err = tracker.record(tx, parts); err != nil {
			return false, err
		}
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// A repeated immutable payload can advance source ordering without reparsing
// it. Match the currently associated identity and version in the transaction;
// another payload/writer's authority must never be advanced by this receipt.
func (c *Catalog) acceptRepeatedObservation(ctx context.Context, adapter Adapter, parts []sourcePart, tracker *partTracker) error {
	host := sourceHost(adapter).ID
	revision := ""
	if view := captureViewOf(adapter); view != nil {
		revision = view.evidenceRevision
	}
	pending := append([]sourcePart{}, tracker.pending...)
	return c.writeTransaction(ctx, "accept repeated source observation", func(tx *sql.Tx) error {
		tracker.pending = append(tracker.pending[:0], pending...)
		for _, part := range parts {
			if !part.evidenceObservation || part.version <= 0 {
				continue
			}
			rows, err := queryMaps(tx, `SELECT workspace_id,conversation_id,origin,version FROM copy_evidence WHERE host_id=? AND identity=? AND version<?`, host, part.signal, part.version)
			if err != nil {
				return err
			}
			for _, row := range rows {
				result, err := tx.Exec(`UPDATE copy_versions SET version=?,written_at=? WHERE host_id=? AND workspace_id=? AND conversation_id=? AND origin=? AND version=?`, part.version, now(), host, row["workspace_id"], row["conversation_id"], row["origin"], row["version"])
				if err != nil {
					return err
				}
				changed, err := result.RowsAffected()
				if err != nil {
					return err
				}
				if changed == 0 {
					continue
				}
				if _, err := tx.Exec(`UPDATE copy_evidence SET version=?,revision=CASE WHEN ?='' THEN revision ELSE ? END WHERE host_id=? AND workspace_id=? AND conversation_id=? AND origin=? AND identity=? AND version=?`, part.version, revision, revision, host, row["workspace_id"], row["conversation_id"], row["origin"], part.signal, row["version"]); err != nil {
					return err
				}
			}
		}
		return tracker.record(tx, parts)
	})
}
