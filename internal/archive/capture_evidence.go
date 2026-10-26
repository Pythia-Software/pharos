package archive

import (
	"context"
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
