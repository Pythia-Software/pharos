package sourcewire

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

// SourceRecoveryContract is the signed envelope carrier. It is not an
// advertisement that graph recovery or typed execution is available.
const SourceRecoveryContract = "source_recovery_v1"

const RecoveryHorizon = 7 * 24 * time.Hour
const RecoveryFutureSkew = 5 * time.Minute

var ErrIssuanceExpired = errors.New("source_issuance_expired")

// CheckRecoveryIssuance must receive the database time sampled after the last
// common authority lock, including on exact receipt replay.
func CheckRecoveryIssuance(issuedAt int64, now time.Time) (time.Time, error) {
	if issuedAt <= 0 || issuedAt > 253402300799 {
		return time.Time{}, ErrInvalid
	}
	issued := time.Unix(issuedAt, 0).UTC()
	if issued.After(now.Add(RecoveryFutureSkew)) {
		return time.Time{}, ErrInvalid
	}
	expires := issued.Add(RecoveryHorizon)
	if !now.Before(expires) {
		return expires, ErrIssuanceExpired
	}
	return expires, nil
}

// ProtectionObservationID commits to the canonical body without its ID and to
// the exact signed installation, library, and contract. UUID version 8 marks
// this SHA-256-derived identifier; it is not a random authority token.
func ProtectionObservationID(v ProtectionObservation, installation, library string) (string, error) {
	if !uuidPattern.MatchString(installation) || library == "" || len(library) > 256 || v.Contract != SourceRecoveryContract {
		return "", ErrInvalid
	}
	payload := struct {
		Version              int            `json:"version"`
		IssuedAt             int64          `json:"issued_at"`
		ExpectedRevision     int64          `json:"expected_revision"`
		Revision             int64          `json:"revision"`
		Token                string         `json:"token"`
		Witness              string         `json:"witness"`
		Dependencies         []string       `json:"dependencies"`
		EvidenceRevision     string         `json:"evidence_revision"`
		Protection           string         `json:"protection"`
		Classification       Classification `json:"classification"`
		ReviewedFlagRevision int64          `json:"reviewed_flag_revision"`
		ManualReviewEvidence string         `json:"manual_review_evidence"`
	}{v.Version, v.IssuedAt, v.ExpectedRevision, v.Revision, v.Token, v.Witness, v.Dependencies, v.EvidenceRevision, v.Protection, v.Classification, v.ReviewedFlagRevision, v.ManualReviewEvidence}
	envelope := struct {
		Contract     string `json:"contract"`
		Installation string `json:"installation_id"`
		Library      string `json:"library_id"`
		Observation  any    `json:"observation"`
	}{v.Contract, installation, library, payload}
	raw, err := json.Marshal(envelope)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write([]byte("pharos-source-protection-id-v1\n"))
	h.Write(raw)
	sum := h.Sum(nil)
	sum[6] = (sum[6] & 0x0f) | 0x80
	sum[8] = (sum[8] & 0x3f) | 0x80
	var b [36]byte
	hex.Encode(b[0:8], sum[0:4])
	b[8] = '-'
	hex.Encode(b[9:13], sum[4:6])
	b[13] = '-'
	hex.Encode(b[14:18], sum[6:8])
	b[18] = '-'
	hex.Encode(b[19:23], sum[8:10])
	b[23] = '-'
	hex.Encode(b[24:36], sum[10:16])
	return string(b[:]), nil
}

// ProfileUploadID pins one issued identity for all page and seal evidence.
func ProfileUploadID(installation, library, snapshot, inventory, profile string, version int, revision, local, issued int64, prior string) (string, error) {
	if !uuidPattern.MatchString(installation) || library == "" || len(library) > 256 || !profileIdentity(snapshot, inventory, profile, version, revision, local, prior) || issued <= 0 || issued > 253402300799 {
		return "", ErrInvalid
	}
	v := struct {
		Contract     string `json:"contract"`
		Installation string `json:"installation_id"`
		Library      string `json:"library_id"`
		Snapshot     string `json:"snapshot_id"`
		Inventory    string `json:"inventory_sha256"`
		Profile      string `json:"profile_id"`
		Version      int    `json:"profile_version"`
		Revision     int64  `json:"profile_revision"`
		Local        int64  `json:"local_revision"`
		Prior        string `json:"expected_prior_sha256"`
		Issued       int64  `json:"issued_at"`
	}{SourceRecoveryContract, installation, library, snapshot, inventory, profile, version, revision, local, prior, issued}
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write([]byte("pharos-source-profile-upload-id-v1\n"))
	h.Write(raw)
	sum := h.Sum(nil)
	sum[6] = (sum[6] & 0x0f) | 0x80
	sum[8] = (sum[8] & 0x3f) | 0x80
	var b [36]byte
	hex.Encode(b[0:8], sum[0:4])
	b[8] = '-'
	hex.Encode(b[9:13], sum[4:6])
	b[13] = '-'
	hex.Encode(b[14:18], sum[6:8])
	b[18] = '-'
	hex.Encode(b[19:23], sum[8:10])
	b[23] = '-'
	hex.Encode(b[24:36], sum[10:16])
	return string(b[:]), nil
}
