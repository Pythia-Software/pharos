package archive

import (
	"context"
	"database/sql"
	"encoding/hex"
	"sort"
	"time"

	"github.com/gbdubs/pharos/internal/teams"
	"github.com/gbdubs/pharos/internal/teams/sourcewire"
)

// A caller must construct this roster under the capture lease from a current
// manifest and two matching confined source walks. It is deliberately not a
// public authority or a substitute for the local protection baseline.
type teamsCapturedRosterEntry struct{ captureSHA, capturePath string }

// teamsMetadataInventoryIn reads only provenance, digests and privacy metadata.
// It never reads message text or opens a provider transcript. It is not called
// by the worker until manifest, filesystem and baseline gates are integrated.
func teamsMetadataInventoryIn(ctx context.Context, tx *sql.Tx, cfg Config, d LibraryDescriptor, credential *teams.EnrollmentCredential, roster map[string]teamsCapturedRosterEntry) (teamsV2Inventory, error) {
	var out teamsV2Inventory
	if credential == nil || credential.LibraryID != d.ID || len(cfg.Sources) != 1 || !cfg.Sources[0].Enabled || !isCodexSource(cfg.Sources[0].Kind) || len(roster) == 0 || len(roster) > 500 {
		return out, errTeamsSourceUnavailable
	}
	source := cfg.Sources[0]
	key := credential.SourceKey()
	defer clear(key)
	if len(key) != 32 {
		return out, errTeamsNotEnrolled
	}
	account, err := teams.SourceWorkNamespace(key, "codex")
	if err != nil {
		return out, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT revision FROM teams_source_revision WHERE id=1`).Scan(&out.SourceRevision); err != nil {
		return out, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT revision FROM teams_privacy_revision WHERE id=1`).Scan(&out.PrivacyRevision); err != nil {
		return out, err
	}
	generation, err := readCacheGenerations(ctx, tx)
	if err != nil {
		return out, err
	}
	out.Generation = generation.all
	// Every conversation and every tracked source record/item must be in the
	// bounded roster. Extra providers, stale parts, and ambiguous links hold it.
	var conversations, records, parts, links, sourceOK, sources, proofs, profiles, custody, evidence, bindings, unscoped int
	for _, q := range []struct {
		sql  string
		args []any
		dest *int
	}{
		{`SELECT count(*) FROM conversations`, nil, &conversations},
		{`SELECT count(*) FROM source_record_states`, nil, &records},
		{`SELECT count(*) FROM source_item_states`, nil, &parts},
		{`SELECT count(*) FROM conversation_identity_links`, nil, &links},
		{`SELECT count(*) FROM teams_protection_assertions WHERE active=1 AND (provider='' OR account='' OR native_id='')`, nil, &unscoped},
		{`SELECT count(*) FROM source_states`, nil, &sources},
		{`SELECT count(*) FROM teams_native_source_proofs`, nil, &proofs},
		{`SELECT count(*) FROM teams_codex_request_profiles`, nil, &profiles},
		{`SELECT count(*) FROM teams_codex_capture_custody`, nil, &custody},
		{`SELECT count(*) FROM teams_codex_transcript_evidence_v1`, nil, &evidence},
		{`SELECT count(*) FROM teams_source_bindings WHERE superseded_at=''`, nil, &bindings},
		{`SELECT count(*) FROM source_states WHERE host_id=? AND source_name=? AND coverage='complete' AND pending_count=0 AND COALESCE(error,'')=''`, []any{currentHost().ID, source.Name}, &sourceOK},
	} {
		if err = tx.QueryRowContext(ctx, q.sql, q.args...).Scan(q.dest); err != nil {
			return out, err
		}
	}
	if conversations != len(roster) || records != len(roster) || parts != len(roster) || links != 0 || unscoped != 0 || sourceOK != 1 || sources != 1 || proofs != len(roster) || profiles != len(roster) || custody != len(roster) || evidence != len(roster) || bindings != 1 {
		return out, errTeamsSourceUnavailable
	}
	rows, err := tx.QueryContext(ctx, `SELECT c.id,c.native_id,p.origin,p.total_digest,p.total_tokens,b.binding_id,b.provenance_sha256,b.source_root,m.revision,e.transcript_sha256,e.review_input,e.capture_sha256,e.capture_revision,e.indexed_digest
 FROM conversations c JOIN teams_native_source_proofs p ON p.conversation_id=c.id JOIN teams_source_bindings b ON b.binding_id=p.binding_id AND b.superseded_at=''
 JOIN teams_source_mapping_current mc ON mc.binding_id=b.binding_id JOIN teams_source_mapping_revisions m ON m.revision=mc.revision AND m.library_id=? AND m.instance_id=? AND m.origin=? AND m.org_id=? AND m.person_id=? AND m.installation_id=? AND m.mode='work'
 JOIN teams_codex_transcript_evidence_v1 e ON e.binding_id=p.binding_id AND e.conversation_id=p.conversation_id AND e.origin=p.origin AND e.content_matched=1 AND e.gates_version=1
 WHERE c.provider='codex' AND c.account=? AND p.provider='codex' AND p.native_id=c.native_id AND p.status='header_independent_unvalidated' AND p.trailing_partial=0 AND b.host_id=? AND b.source_name=? LIMIT ?`, d.ID, d.Instance, credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, source.Account, currentHost().ID, source.Name, len(roster)+1)
	if err != nil {
		return out, err
	}
	type row struct {
		id, native, origin, totalDigest, binding, provenance, root, transcript, review, capture, revision, indexed string
		total                                                                                                      sql.NullInt64
		mapping                                                                                                    int64
	}
	var found []row
	for rows.Next() {
		var r row
		if err = rows.Scan(&r.id, &r.native, &r.origin, &r.totalDigest, &r.total, &r.binding, &r.provenance, &r.root, &r.mapping, &r.transcript, &r.review, &r.capture, &r.revision, &r.indexed); err != nil {
			rows.Close()
			return out, err
		}
		found = append(found, r)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return out, err
	}
	rows.Close()
	if len(found) != len(roster) {
		return out, errTeamsSourceUnavailable
	}
	out.Items = make([]sourcewire.Item, 0, len(found)*3)
	out.values = map[string]int64{}
	out.exposures = map[string]int64{}
	seen := map[string]bool{}
	for _, r := range found {
		file, ok := roster[r.origin]
		if !ok || seen[r.origin] || r.native == "" || len(r.native) > 512 || r.mapping < 1 || !r.total.Valid || !validRecordHash(r.totalDigest) || !validRecordHash(r.transcript) || !teamsCurrentCodexBinding(cfg, currentHost().ID, source.Name, r.root, r.provenance) {
			return teamsV2Inventory{}, errTeamsSourceUnavailable
		}
		seen[r.origin] = true
		custody, e := teamsReadCodexCaptureCustody(tx, r.binding, r.id, r.origin)
		if e != nil || custody.CaptureSHA256 != r.capture || custody.CaptureSHA256 != file.captureSHA || custody.CapturePath != file.capturePath || custody.CaptureRevision != r.revision || custody.IndexedDigest != r.indexed {
			return teamsV2Inventory{}, errTeamsSourceUnavailable
		}
		var itemCurrent bool
		if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM source_item_states WHERE host_id=? AND source_name=? AND item=?)`, currentHost().ID, source.Name, r.origin).Scan(&itemCurrent); e != nil || !itemCurrent {
			return teamsV2Inventory{}, errTeamsSourceUnavailable
		}
		privacy, e := readTeamsPrivacy(ctx, tx, r.id)
		if e != nil {
			return teamsV2Inventory{}, e
		}
		protection := "unknown"
		if privacy.UserPrivate {
			protection = "private"
		} else if privacy.ClassifierState == "clear" && privacy.ClassifierReviewed && privacy.ClassifierInput == r.review && len(r.review) == len(teamsManualReviewPrefix)+64 {
			protection = "public"
		}
		var explicit, held int
		if e = tx.QueryRowContext(ctx, `SELECT COALESCE(sum(CASE WHEN kind='explicit_private' THEN 1 ELSE 0 END),0),COALESCE(sum(CASE WHEN kind<>'explicit_private' THEN 1 ELSE 0 END),0) FROM teams_protection_assertions WHERE active=1 AND (conversation_id=? OR (provider='codex' AND account=? AND native_id=?))`, r.id, source.Account, r.native).Scan(&explicit, &held); e != nil {
			return teamsV2Inventory{}, e
		}
		if privacy.UserPrivate || explicit > 0 {
			protection = "private"
		} else if held > 0 {
			protection = "unknown"
		}
		if len(r.review) != len(teamsManualReviewPrefix)+64 || r.review[:len(teamsManualReviewPrefix)] != teamsManualReviewPrefix || !validRecordHash(r.review[len(teamsManualReviewPrefix):]) {
			return teamsV2Inventory{}, errTeamsSourceUnavailable
		}
		thread, e := teams.SourceToken(key, "codex", account, r.native)
		if e != nil {
			return teamsV2Inventory{}, e
		}
		witness, e := teams.SourceWitness(key, "codex", "codex_thread", r.native)
		if e != nil {
			return teamsV2Inventory{}, e
		}
		totalDigest, _ := hex.DecodeString(r.totalDigest)
		totalRevision, e := teams.SourceEvidenceRevision(key, thread, "codex_thread_usage", 1, totalDigest)
		if e != nil {
			return teamsV2Inventory{}, e
		}
		content, e := teams.SourceToken(key, "codex-content", account, r.native)
		if e != nil {
			return teamsV2Inventory{}, e
		}
		contentWitness, e := teams.SourceWitness(key, "codex", "codex_transcript", r.native)
		if e != nil {
			return teamsV2Inventory{}, e
		}
		contentDigest, _ := hex.DecodeString(r.transcript)
		contentRevision, e := teams.SourceEvidenceRevision(key, content, "codex_transcript", 1, contentDigest)
		if e != nil {
			return teamsV2Inventory{}, e
		}
		component, e := teams.SourceToken(key, "codex-component", account, r.native)
		if e != nil {
			return teamsV2Inventory{}, e
		}
		componentWitness, e := teams.SourceWitness(key, "codex", "content_component", r.native)
		if e != nil {
			return teamsV2Inventory{}, e
		}
		out.Items = append(out.Items, sourcewire.Item{Kind: "codex_thread", Token: thread, Witness: witness, Protection: protection, Profile: &sourcewire.Profile{ID: "codex_thread_usage", Version: 1}, EvidenceRevision: totalRevision, Dependencies: []string{}}, sourcewire.Item{Kind: "content_evidence", Token: content, Witness: contentWitness, Protection: protection, Profile: &sourcewire.Profile{ID: "codex_transcript", Version: 1}, EvidenceRevision: contentRevision, Dependencies: []string{thread}}, sourcewire.Item{Kind: "content_component", Token: component, Witness: componentWitness, Protection: protection, Dependencies: []string{content}})
		out.values[thread] = r.total.Int64
		out.exposures[r.binding] = r.mapping
	}
	sort.Slice(out.Items, func(i, j int) bool { return out.Items[i].Token < out.Items[j].Token })
	for i := 1; i < len(out.Items); i++ {
		if out.Items[i-1].Token == out.Items[i].Token {
			return teamsV2Inventory{}, errTeamsSourceUnavailable
		}
	}
	out.Complete = true
	out.ObservedAt = time.Now().UTC()
	return out, nil
}
