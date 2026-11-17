package archive

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

// teamsRecordNativeEvidence is fed only by trusted adapter observations, in
// the same writer transaction as the indexed record. It does not attest an
// account or authorize reporting. Until the scoped source resolver and send
// barrier consume this revision, no production work may use these proofs.
func teamsRecordNativeEvidence(tx *sql.Tx, host string, config SourceConfig, view *captureView, record WorkspaceRecord, digest string) error {
	if !isCodexSource(config.Kind) || record.SourceKind != "codex" || host == "" || config.Name == "" {
		return nil
	}
	hasProof := false
	for _, conversation := range record.Conversations {
		if conversation.teamsNativeProof != nil {
			hasProof = true
			break
		}
	}
	if !hasProof {
		return nil
	}
	root := config.Path
	if view != nil && view.manifest != nil {
		root = view.manifest.Source.Path
	}
	if root == "" {
		return errors.New("source provenance is unavailable")
	}
	provenance, err := json.Marshal(struct {
		Domain, Host, Name, Kind, Account, Root string
		Options                                 map[string]any
	}{"pharos-teams-source-binding-v1", host, config.Name, config.Kind, config.Account, root, config.Options})
	if err != nil {
		return err
	}
	sum := sha256.Sum256(provenance)
	fingerprint := hex.EncodeToString(sum[:])
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return err
	}
	binding := base64.RawURLEncoding.EncodeToString(random[:])
	result, err := tx.Exec(`INSERT INTO teams_source_bindings(binding_id,provenance_sha256,host_id,source_name,source_kind,provider,account_label,source_root,created_at) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(provenance_sha256) DO NOTHING`, binding, fingerprint, host, config.Name, config.Kind, "codex", config.Account, root, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if err = tx.QueryRow(`SELECT binding_id FROM teams_source_bindings WHERE provenance_sha256=?`, fingerprint).Scan(&binding); err != nil {
		return err
	}
	changed := inserted > 0
	for _, conversation := range record.Conversations {
		proof := conversation.teamsNativeProof
		if proof == nil || proof.Provider != "codex" || proof.NativeID != conversation.NativeID || conversation.Provider != "codex" {
			continue
		}
		var conversationID string
		err = tx.QueryRow(`SELECT id FROM conversations WHERE provider=? AND account=? AND native_id=?`, conversation.Provider, conversation.Account, conversation.NativeID).Scan(&conversationID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		result, err = tx.Exec(`INSERT INTO teams_native_source_proofs(binding_id,conversation_id,provider,native_id,status,trailing_partial,indexed_digest,total_tokens,total_digest) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(binding_id,conversation_id) DO UPDATE SET provider=excluded.provider,native_id=excluded.native_id,status=excluded.status,trailing_partial=excluded.trailing_partial,indexed_digest=excluded.indexed_digest,total_tokens=excluded.total_tokens,total_digest=excluded.total_digest WHERE teams_native_source_proofs.provider<>excluded.provider OR teams_native_source_proofs.native_id<>excluded.native_id OR teams_native_source_proofs.status<>excluded.status OR teams_native_source_proofs.trailing_partial<>excluded.trailing_partial OR teams_native_source_proofs.indexed_digest<>excluded.indexed_digest OR teams_native_source_proofs.total_tokens IS NOT excluded.total_tokens OR teams_native_source_proofs.total_digest<>excluded.total_digest`, binding, conversationID, proof.Provider, proof.NativeID, proof.Status, proof.TrailingPartial, digest, proof.TotalTokens, proof.TotalDigest)
		if err != nil {
			return err
		}
		n, e := result.RowsAffected()
		if e != nil {
			return e
		}
		changed = changed || n > 0
	}
	if changed {
		_, err = tx.Exec(`UPDATE teams_source_revision SET revision=revision+1 WHERE id=1`)
		return err
	}
	return nil
}

// requestTeamsSourceProof is a local explicit setup action, not a send grant.
// It asks the existing source index to revisit Codex records with the audited
// observer; no ordinary library indexes this extra evidence by default.
func (c *Catalog) requestTeamsSourceProof(ctx context.Context, descriptor LibraryDescriptor, expectedSettings int64) error {
	lease, err := c.teamsSharingLease()
	if err != nil {
		return err
	}
	defer lease.Close()
	return c.writeTransaction(ctx, "teams-request-source-proof", func(tx *sql.Tx) error {
		settings, e := readTeamsSettings(ctx, tx)
		if e != nil {
			return e
		}
		if settings.LibraryID != descriptor.ID || settings.InstanceID != descriptor.Instance || settings.OrgID == "" || settings.InstallationID == "" || settings.SettingsRevision != expectedSettings {
			return errTeamsConsentStale
		}
		var requested bool
		if e = tx.QueryRowContext(ctx, `SELECT proof_requested FROM teams_source_revision WHERE id=1`).Scan(&requested); e != nil {
			return e
		}
		if requested {
			return nil
		}
		if _, e = tx.ExecContext(ctx, `UPDATE teams_source_revision SET proof_requested=1,revision=revision+1 WHERE id=1`); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE teams_local_settings SET settings_revision=settings_revision+1,local_revision=local_revision+1 WHERE id=1`); e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `UPDATE source_states SET fingerprint=NULL,index_version=NULL WHERE lower(kind)='codex'`)
		return e
	})
}
