package archive

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"io"
	"time"
)

// teamsPersistCodexCaptureCustody records only an unambiguous retained capture
// selected by trusted indexing. It is local provenance, not sharing authority.
// Live-only and old captures without this evidence remain unavailable to Q3.
// The parser and this verification read the same frozen capture view. Hash the
// decoded retained bytes before entering the catalog writer; the capture is
// held by its reader lease and is never a provider-file read. Capture writes
// replace files by rename. capture_revision records this verification view,
// not a requirement that later unrelated capture revisions stay unchanged.
const teamsMaxCustodyCaptureBytes = 64 << 20

func teamsVerifiedCodexCapture(view *captureView, record WorkspaceRecord, origin string) *capturedFile {
	if view == nil || view.manifest == nil || view.evidenceRevision == "" || !validRecordHash(record.EvidenceIdentity) {
		return nil
	}
	var selected *capturedFile
	var rel string
	for _, f := range view.manifest.Files {
		if f == nil || (f.Path != origin && plainRollout(f.Path) != origin) || f.Captured == "" {
			continue
		}
		mapped, ok := view.captured(f.Path)
		candidate, within := view.rel(mapped)
		if !ok || !within || candidate != f.Captured {
			continue
		}
		if selected != nil {
			return nil
		}
		selected, rel = f, candidate
	}
	if selected == nil || selected.Size < 0 || selected.Size > teamsMaxCustodyCaptureBytes || !validRecordHash(selected.SHA256) {
		return nil
	}
	if _, err := time.Parse(time.RFC3339Nano, selected.CapturedAt); err != nil {
		return nil
	}

	f, err := view.open(rel)
	if err != nil {
		return nil
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, selected.Size+1))
	if err != nil || n != selected.Size || hex.EncodeToString(h.Sum(nil)) != selected.SHA256 {
		return nil
	}
	return selected
}

func teamsPersistCodexCaptureCustody(tx *sql.Tx, view *captureView, record WorkspaceRecord, selected *capturedFile, binding, conversation, origin, indexedDigest, provenance string) (bool, error) {
	var evidence string
	err := tx.QueryRow(`SELECT evidence_sha256 FROM teams_codex_request_profiles WHERE binding_id=? AND conversation_id=? AND origin=? AND indexed_digest=?`, binding, conversation, origin, indexedDigest).Scan(&evidence)
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}
	if err == sql.ErrNoRows || selected == nil {
		result, e := tx.Exec(`DELETE FROM teams_codex_capture_custody WHERE binding_id=? AND conversation_id=? AND origin=?`, binding, conversation, origin)
		if e != nil {
			return false, e
		}
		n, e := result.RowsAffected()
		return n > 0, e
	}
	result, err := tx.Exec(`INSERT INTO teams_codex_capture_custody(binding_id,conversation_id,origin,indexed_digest,evidence_sha256,capture_sha256,captured_at,capture_path,provenance_sha256,evidence_identity,capture_revision) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(binding_id,conversation_id,origin) DO UPDATE SET indexed_digest=excluded.indexed_digest,evidence_sha256=excluded.evidence_sha256,capture_sha256=excluded.capture_sha256,captured_at=excluded.captured_at,capture_path=excluded.capture_path,provenance_sha256=excluded.provenance_sha256,evidence_identity=excluded.evidence_identity,capture_revision=excluded.capture_revision WHERE indexed_digest<>excluded.indexed_digest OR evidence_sha256<>excluded.evidence_sha256 OR capture_sha256<>excluded.capture_sha256 OR captured_at<>excluded.captured_at OR capture_path<>excluded.capture_path OR provenance_sha256<>excluded.provenance_sha256 OR evidence_identity<>excluded.evidence_identity OR capture_revision<>excluded.capture_revision`, binding, conversation, origin, indexedDigest, evidence, selected.SHA256, selected.CapturedAt, selected.Captured, provenance, record.EvidenceIdentity, view.evidenceRevision)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n > 0, err
}

// teamsReadCodexCaptureCustody reads metadata only; a caller must establish
// issued assignment and effect-specific eligibility independently before any
// fact payload is read. The caller owns the transaction and its lifetime.
type teamsCodexCaptureCustody struct {
	IndexedDigest, EvidenceSHA256, CaptureSHA256, CapturedAt         string
	CapturePath, CaptureRevision, ProvenanceSHA256, EvidenceIdentity string
}

func teamsReadCodexCaptureCustody(tx *sql.Tx, binding, conversation, origin string) (teamsCodexCaptureCustody, error) {
	var v teamsCodexCaptureCustody
	err := tx.QueryRow(`SELECT x.indexed_digest,x.evidence_sha256,x.capture_sha256,x.captured_at,x.capture_path,x.capture_revision,x.provenance_sha256,x.evidence_identity
 FROM teams_codex_capture_custody x
 JOIN teams_codex_request_profiles q USING(binding_id,conversation_id,origin)
 JOIN teams_native_source_proofs p USING(binding_id,conversation_id,origin)
 JOIN teams_source_bindings b USING(binding_id)
 JOIN conversations c ON c.id=x.conversation_id
 WHERE x.binding_id=? AND x.conversation_id=? AND x.origin=?
 AND b.superseded_at='' AND b.provenance_sha256=x.provenance_sha256
 AND p.provider='codex' AND p.native_id=c.native_id AND p.status='header_independent_unvalidated' AND p.trailing_partial=0
 AND q.indexed_digest=x.indexed_digest AND p.indexed_digest=x.indexed_digest AND q.evidence_sha256=x.evidence_sha256
 AND EXISTS(SELECT 1 FROM source_record_states sr WHERE sr.host_id=b.host_id AND sr.source_name=b.source_name AND sr.source_kind='codex' AND sr.source_account=c.account AND sr.source_id=c.native_id AND sr.digest=x.indexed_digest)`, binding, conversation, origin).Scan(&v.IndexedDigest, &v.EvidenceSHA256, &v.CaptureSHA256, &v.CapturedAt, &v.CapturePath, &v.CaptureRevision, &v.ProvenanceSHA256, &v.EvidenceIdentity)
	if err == sql.ErrNoRows {
		return teamsCodexCaptureCustody{}, errTeamsSourceUnavailable
	}
	if err != nil {
		return teamsCodexCaptureCustody{}, err
	}
	if !validRecordHash(v.EvidenceSHA256) || !validRecordHash(v.CaptureSHA256) || !validRecordHash(v.ProvenanceSHA256) || !validRecordHash(v.EvidenceIdentity) || v.CapturePath == "" || v.CaptureRevision == "" {
		return teamsCodexCaptureCustody{}, errTeamsSourceUnavailable
	}
	if _, err = time.Parse(time.RFC3339Nano, v.CapturedAt); err != nil {
		return teamsCodexCaptureCustody{}, errTeamsSourceUnavailable
	}
	return v, nil
}
