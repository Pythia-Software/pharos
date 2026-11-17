package sourcewire

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// Classification is progress for a particular proof and policy, independent
// from the effective protection of the source.
type Classification struct {
	State            string `json:"state"`
	Required         bool   `json:"required"`
	EvidenceRevision string `json:"evidence_revision"`
	PolicySHA256     string `json:"policy_sha256"`
}

type ProtectionObservation struct {
	Version          int            `json:"version"`
	ObservationID    string         `json:"observation_id"`
	ExpectedRevision int64          `json:"expected_revision"`
	Revision         int64          `json:"revision"`
	Token            string         `json:"token"`
	Witness          string         `json:"witness"`
	Dependencies     []string       `json:"dependencies"`
	EvidenceRevision string         `json:"evidence_revision"`
	Protection       string         `json:"protection"`
	Classification   Classification `json:"classification"`
	// Review binds clearance to the exact prior flagged origin and local proof.
	ReviewedFlagRevision int64  `json:"reviewed_flag_revision"`
	ManualReviewEvidence string `json:"manual_review_evidence"`
}

type ProtectionReceipt struct {
	Status            string `json:"status"`
	ObservationID     string `json:"observation_id"`
	Revision          int64  `json:"revision"`
	ObservationSHA256 string `json:"observation_sha256"`
	PrivacyRevision   int64  `json:"privacy_revision"`
	OriginProtection  string `json:"origin_protection"`
	SafetyPending     bool   `json:"safety_pending"`
}

func (v ProtectionObservation) Validate() error {
	if v.Version != 1 || !uuidPattern.MatchString(v.ObservationID) || v.ExpectedRevision < 0 || v.Revision < 1 || v.Revision != v.ExpectedRevision+1 || !opaque(v.Token) || !opaque(v.Witness) || !opaque(v.EvidenceRevision) || v.Dependencies == nil || len(v.Dependencies) > MaxDependenciesPerItem {
		return ErrInvalid
	}
	previous := ""
	for _, d := range v.Dependencies {
		if !opaque(d) || d <= previous || d == v.Token {
			return ErrInvalid
		}
		previous = d
	}
	switch v.Protection {
	case "non_private", "explicit_private", "inherited_private", "flagged", "unknown":
	default:
		return ErrInvalid
	}
	switch v.Classification.State {
	case "unclassified", "pending", "failed", "stale", "clear":
	default:
		return ErrInvalid
	}
	if v.Classification.State == "clear" || v.Classification.EvidenceRevision != "" || v.Classification.PolicySHA256 != "" {
		if !opaque(v.Classification.EvidenceRevision) || !digest(v.Classification.PolicySHA256, false) {
			return ErrInvalid
		}
		if v.Classification.State != "stale" && v.Classification.EvidenceRevision != v.EvidenceRevision {
			return ErrInvalid
		}
	}
	if v.ReviewedFlagRevision < 0 || ((v.ReviewedFlagRevision == 0) != (v.ManualReviewEvidence == "")) || (v.ManualReviewEvidence != "" && !opaque(v.ManualReviewEvidence)) {
		return ErrInvalid
	}
	return nil
}
func DecodeProtectionObservation(raw []byte) (ProtectionObservation, error) {
	var v ProtectionObservation
	if len(raw) > 32768 || canonical(raw, &v) != nil || v.Validate() != nil {
		return ProtectionObservation{}, ErrInvalid
	}
	return v, nil
}
func (v ProtectionObservation) Bytes() ([]byte, string, error) {
	if v.Validate() != nil {
		return nil, "", ErrInvalid
	}
	b, e := json.Marshal(v)
	if e != nil {
		return nil, "", e
	}
	return b, ProtectionDigest(b), nil
}
func ProtectionDigest(raw []byte) string {
	h := sha256.New()
	h.Write([]byte("pharos-source-protection-v1\n"))
	h.Write(raw)
	return hex.EncodeToString(h.Sum(nil))
}
