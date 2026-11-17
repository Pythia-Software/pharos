package archive

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// teamsPersistCodexTranscriptEvidence runs after indexed messages and custody
// are written. No transcript bytes or message text are stored in this table.
func teamsPersistCodexTranscriptEvidence(ctx context.Context, tx *sql.Tx, selected *capturedFile, binding, conversation, origin, indexed string) (bool, error) {
	remove := func() (bool, error) {
		res, err := tx.Exec(`DELETE FROM teams_codex_transcript_evidence_v1 WHERE binding_id=? AND conversation_id=? AND origin=?`, binding, conversation, origin)
		if err != nil {
			return false, err
		}
		n, err := res.RowsAffected()
		return n > 0, err
	}
	if selected == nil || selected.TeamsAcquisition == nil || selected.teamsTranscript == nil || !validRecordHash(selected.teamsTranscript.Digest) {
		return remove()
	}
	matched, err := teamsCodexIndexedContentMatches(ctx, tx, conversation, selected.teamsTranscript.Messages)
	if err != nil {
		return false, err
	}
	if !matched {
		return remove()
	}
	review, err := teamsManualReviewInput(ctx, tx, conversation)
	if errors.Is(err, errTeamsSourceUnavailable) {
		return remove()
	}
	if err != nil {
		return false, err
	}
	if !strings.HasPrefix(review, teamsManualReviewPrefix) || !validRecordHash(strings.TrimPrefix(review, teamsManualReviewPrefix)) {
		return remove()
	}
	var capture, revision string
	err = tx.QueryRow(`SELECT capture_sha256,capture_revision FROM teams_codex_capture_custody WHERE binding_id=? AND conversation_id=? AND origin=? AND indexed_digest=?`, binding, conversation, origin, indexed).Scan(&capture, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return remove()
	}
	if err != nil {
		return false, err
	}
	if capture != selected.SHA256 || revision == "" {
		return remove()
	}
	res, err := tx.Exec(`INSERT INTO teams_codex_transcript_evidence_v1(binding_id,conversation_id,origin,indexed_digest,capture_sha256,capture_revision,transcript_sha256,review_input,content_matched,gates_version) VALUES(?,?,?,?,?,?,?,?,1,1) ON CONFLICT(binding_id,conversation_id,origin) DO UPDATE SET indexed_digest=excluded.indexed_digest,capture_sha256=excluded.capture_sha256,capture_revision=excluded.capture_revision,transcript_sha256=excluded.transcript_sha256,review_input=excluded.review_input,content_matched=1,gates_version=1 WHERE indexed_digest<>excluded.indexed_digest OR capture_sha256<>excluded.capture_sha256 OR capture_revision<>excluded.capture_revision OR transcript_sha256<>excluded.transcript_sha256 OR review_input<>excluded.review_input OR content_matched<>1 OR gates_version<>1`, binding, conversation, origin, indexed, capture, revision, selected.teamsTranscript.Digest, review)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// A fresh acquisition of the exact already-indexed bytes can refresh dormant
// transcript metadata without advancing source time or rewriting indexed rows.
// A conflicting or genuinely older observation stays behind the normal guard.
func (c *Catalog) teamsAttachUnchangedTranscript(ctx context.Context, host string, config SourceConfig, view *captureView, record WorkspaceRecord, captures map[string]*capturedFile, tracker *partTracker, parts []sourcePart) (bool, error) {
	if view == nil || view.manifest == nil || tracker == nil || len(record.Conversations) != 1 || len(parts) != 1 {
		return false, nil
	}
	conversation := record.Conversations[0]
	selected := captures[conversation.Origin]
	if selected == nil || selected.TeamsAcquisition == nil || selected.TeamsAcquisitionRoot == nil || selected.teamsTranscript == nil || parts[0].item != conversation.Origin {
		return false, nil
	}
	sealed := view.manifest.Source
	if sealed.TeamsProvenanceAmbiguous || !validRecordHash(sealed.TeamsOptionsSHA256) || sealed.Name != config.Name || sealed.Kind != config.Kind || defaultString(sealed.Account, "local") != config.Account || sealed.Path == "" || !isCodexSource(config.Kind) {
		return false, nil
	}
	provenance, err := teamsSourceBindingFingerprint(host, config.Name, config.Kind, config.Account, sealed.Path, sealed.TeamsOptionsSHA256)
	if err != nil {
		return false, err
	}
	attached := false
	err = c.writeTransaction(ctx, "teams-attach-unchanged-transcript", func(tx *sql.Tx) error {
		var requested bool
		if err := tx.QueryRowContext(ctx, `SELECT proof_requested FROM teams_source_revision WHERE id=1`).Scan(&requested); err != nil {
			return err
		}
		if !requested {
			return nil
		}
		var version int64
		if err := tx.QueryRow(`SELECT version FROM source_item_states WHERE host_id=? AND source_name=? AND item=?`, host, config.Name, parts[0].item).Scan(&version); errors.Is(err, sql.ErrNoRows) {
			return nil
		} else if err != nil {
			return err
		}
		if version != parts[0].version {
			return nil
		}
		var binding, id, indexed string
		rows, err := tx.QueryContext(ctx, `SELECT b.binding_id,c.id,x.indexed_digest FROM teams_source_bindings b JOIN teams_codex_capture_custody x ON x.binding_id=b.binding_id JOIN conversations c ON c.id=x.conversation_id WHERE b.host_id=? AND b.source_name=? AND b.source_root=? AND b.account_label=? AND lower(b.source_kind)='codex' AND b.provenance_sha256=? AND b.superseded_at='' AND x.origin=? AND c.provider='codex' AND c.account=? AND c.native_id=? LIMIT 2`, host, config.Name, sealed.Path, config.Account, provenance, conversation.Origin, conversation.Account, conversation.NativeID)
		if err != nil {
			return err
		}
		count := 0
		for rows.Next() {
			count++
			if err = rows.Scan(&binding, &id, &indexed); err != nil {
				rows.Close()
				return err
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if count != 1 {
			return nil
		}
		custody, err := teamsReadCodexCaptureCustody(tx, binding, id, conversation.Origin)
		if errors.Is(err, errTeamsSourceUnavailable) {
			return nil
		}
		if err != nil {
			return err
		}
		if custody.IndexedDigest != indexed || custody.CaptureSHA256 != selected.SHA256 {
			return nil
		}
		matched, err := teamsCodexIndexedContentMatches(ctx, tx, id, selected.teamsTranscript.Messages)
		if err != nil || !matched {
			return err
		}
		custodyChanged, err := teamsPersistCodexCaptureCustody(tx, view, record, selected, binding, id, conversation.Origin, indexed, provenance)
		if err != nil {
			return err
		}
		changed, err := teamsPersistCodexTranscriptEvidence(ctx, tx, selected, binding, id, conversation.Origin, indexed)
		if err != nil {
			return err
		}
		// A declined attachment can still update custody or invalidate stale
		// evidence. Commit those changes with a source revision before returning.
		if changed || custodyChanged {
			if _, err = tx.Exec(`UPDATE teams_source_revision SET revision=revision+1 WHERE id=1`); err != nil {
				return err
			}
		}
		var exists bool
		if err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM teams_codex_transcript_evidence_v1 WHERE binding_id=? AND conversation_id=? AND origin=?)`, binding, id, conversation.Origin).Scan(&exists); err != nil || !exists {
			return err
		}
		if err = tracker.record(tx, parts); err != nil {
			return err
		}
		attached = true
		return nil
	})
	return attached, err
}
