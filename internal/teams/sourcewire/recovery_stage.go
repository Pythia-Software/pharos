package sourcewire

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// RecoveryStageItem is an opaque local proof commitment. It contains no local
// source name, path, text, user-defined label, or query configuration.
type RecoveryStageItem struct {
	Token            string         `json:"token"`
	Witness          string         `json:"witness"`
	EvidenceRevision string         `json:"evidence_revision"`
	Dependencies     []string       `json:"dependencies"`
	OriginRevision   int64          `json:"origin_revision"`
	Protection       string         `json:"protection"`
	FlagFloor        string         `json:"flag_floor"`
	Classification   Classification `json:"classification"`
}

// RecoveryProfileChunk carries one bounded piece of an exact canonical 032
// profile page. The header is repeated on every chunk; items are the opaque
// stage items, and the worker reconstructs the original page before proof.
type RecoveryProfileChunk struct {
	Contract            string  `json:"contract"`
	IssuedAt            int64   `json:"issued_at"`
	UploadID            string  `json:"upload_id"`
	SnapshotID          string  `json:"snapshot_id"`
	InventorySHA256     string  `json:"inventory_sha256"`
	ProfileID           string  `json:"profile_id"`
	ProfileVersion      int     `json:"profile_version"`
	ProfileRevision     int64   `json:"profile_revision"`
	LocalRevision       int64   `json:"local_revision"`
	ExpectedPriorSHA256 string  `json:"expected_prior_sha256"`
	PageIndex           int     `json:"page_index"`
	PreviousPageSHA256  *string `json:"previous_page_sha256"`
	PageSHA256          string  `json:"page_sha256"`
	ChunkIndex          int     `json:"chunk_index"`
	FinalChunk          bool    `json:"final_chunk"`
}

type RecoveryStagePage struct {
	Version            int                   `json:"version"`
	Contract           string                `json:"contract"`
	IssuedAt           int64                 `json:"issued_at"`
	CommandID          string                `json:"command_id"`
	RunID              string                `json:"run_id"`
	Purpose            string                `json:"purpose"`
	SnapshotID         string                `json:"snapshot_id"`
	InventorySHA256    string                `json:"inventory_sha256"`
	ProfileID          string                `json:"profile_id"`
	ProfileVersion     int                   `json:"profile_version"`
	ProfileRevision    int64                 `json:"profile_revision"`
	ProfileSHA256      string                `json:"profile_sha256"`
	Index              int                   `json:"index"`
	PreviousPageSHA256 *string               `json:"previous_page_sha256"`
	Final              bool                  `json:"final"`
	ProfileChunk       *RecoveryProfileChunk `json:"profile_chunk"`
	ProfileSeal        *ProfileSeal          `json:"profile_seal"`
	Items              []RecoveryStageItem   `json:"items"`
}

// DecodeRecoveryStagePage admits only the bounded, canonical proof language;
// it does not prove scope completeness or authorize relaxation or publication.
func DecodeRecoveryStagePage(raw []byte) (RecoveryStagePage, error) {
	var v RecoveryStagePage
	if len(raw) > 240<<10 || canonical(raw, &v) != nil || v.Version != 1 || v.Contract != SourceRecoveryContract || v.IssuedAt <= 0 || v.IssuedAt > 253402300799 || !uuidPattern.MatchString(v.CommandID) || !uuidPattern.MatchString(v.RunID) || !uuidPattern.MatchString(v.SnapshotID) || !digest(v.InventorySHA256, false) || v.Index < 0 || v.Index >= 1024 || v.Items == nil || len(v.Items) > 500 {
		return RecoveryStagePage{}, ErrInvalid
	}
	if v.Index == 0 {
		if v.PreviousPageSHA256 != nil {
			return RecoveryStagePage{}, ErrInvalid
		}
	} else if v.PreviousPageSHA256 == nil || !digest(*v.PreviousPageSHA256, false) {
		return RecoveryStagePage{}, ErrInvalid
	}
	switch v.Purpose {
	case "protection":
		if v.ProfileID != "" || v.ProfileVersion != 0 || v.ProfileRevision != 0 || v.ProfileSHA256 != "" || v.ProfileChunk != nil || v.ProfileSeal != nil {
			return RecoveryStagePage{}, ErrInvalid
		}
	case "profiles":
		if v.ProfileID != "codex_request_usage" || v.ProfileVersion != 1 || v.ProfileRevision < 0 || ((v.ProfileRevision == 0) != (v.ProfileSHA256 == "")) || !digest(v.ProfileSHA256, true) {
			return RecoveryStagePage{}, ErrInvalid
		}
	default:
		return RecoveryStagePage{}, ErrInvalid
	}
	if v.ProfileChunk != nil {
		c := v.ProfileChunk
		if v.Purpose != "profiles" || c.Contract != SourceRecoveryContract || c.IssuedAt <= 0 || c.IssuedAt > 253402300799 || !uuidPattern.MatchString(c.UploadID) || c.SnapshotID != v.SnapshotID || c.InventorySHA256 != v.InventorySHA256 || c.ProfileID != v.ProfileID || c.ProfileVersion != v.ProfileVersion || c.ProfileRevision <= v.ProfileRevision || c.LocalRevision < 1 || c.ExpectedPriorSHA256 != v.ProfileSHA256 || c.PageIndex < 0 || c.PageIndex >= MaxPages || c.ChunkIndex < 0 || c.ChunkIndex >= MaxItemsPerPage || !digest(c.PageSHA256, false) {
			return RecoveryStagePage{}, ErrInvalid
		}
		if c.PageIndex == 0 {
			if c.PreviousPageSHA256 != nil {
				return RecoveryStagePage{}, ErrInvalid
			}
		} else if c.PreviousPageSHA256 == nil || !digest(*c.PreviousPageSHA256, false) {
			return RecoveryStagePage{}, ErrInvalid
		}
		if v.Final != (v.ProfileSeal != nil) || (v.Final && !c.FinalChunk) {
			return RecoveryStagePage{}, ErrInvalid
		}
		if v.ProfileSeal != nil {
			seal := v.ProfileSeal
			if seal.Validate() != nil || seal.Contract != c.Contract || seal.IssuedAt != c.IssuedAt || seal.UploadID != c.UploadID || seal.SnapshotID != c.SnapshotID || seal.InventorySHA256 != c.InventorySHA256 || seal.ProfileID != c.ProfileID || seal.ProfileVersion != c.ProfileVersion || seal.ProfileRevision != c.ProfileRevision || seal.LocalRevision != c.LocalRevision || seal.ExpectedPriorSHA256 != c.ExpectedPriorSHA256 || seal.PageCount != c.PageIndex+1 || seal.FinalPageSHA256 != c.PageSHA256 || !seal.Complete || seal.Coverage != (Coverage{}) {
				return RecoveryStagePage{}, ErrInvalid
			}
		}
	} else if v.ProfileSeal != nil {
		return RecoveryStagePage{}, ErrInvalid
	}
	previousToken := ""
	for _, item := range v.Items {
		if !opaque(item.Token) || !opaque(item.Witness) || !opaque(item.EvidenceRevision) || item.OriginRevision < 0 || item.Dependencies == nil || len(item.Dependencies) > MaxDependenciesPerItem || item.Token <= previousToken {
			return RecoveryStagePage{}, ErrInvalid
		}
		switch item.Protection {
		case "non_private", "explicit_private", "inherited_private", "flagged", "unknown":
		default:
			return RecoveryStagePage{}, ErrInvalid
		}
		switch item.FlagFloor {
		case "", "non_private", "unknown":
		default:
			return RecoveryStagePage{}, ErrInvalid
		}
		c := item.Classification
		switch c.State {
		case "unclassified", "pending", "failed", "stale", "clear":
		default:
			return RecoveryStagePage{}, ErrInvalid
		}
		if c.State == "clear" || c.EvidenceRevision != "" || c.PolicySHA256 != "" {
			if !opaque(c.EvidenceRevision) || !digest(c.PolicySHA256, false) || (c.State != "stale" && c.EvidenceRevision != item.EvidenceRevision) {
				return RecoveryStagePage{}, ErrInvalid
			}
		}
		if v.Purpose == "profiles" && v.ProfileChunk != nil && (len(item.Dependencies) != 1 || item.OriginRevision != 0 || item.Protection != "unknown" || item.FlagFloor != "" || item.Classification != (Classification{State: "unclassified"})) {
			return RecoveryStagePage{}, ErrInvalid
		}
		prior := ""
		for _, dep := range item.Dependencies {
			if !opaque(dep) || dep <= prior || dep == item.Token {
				return RecoveryStagePage{}, ErrInvalid
			}
			prior = dep
		}
		previousToken = item.Token
	}
	return v, nil
}

func RecoveryStagePageDigest(raw []byte) string {
	h := sha256.New()
	h.Write([]byte("pharos-source-recovery-stage-v1\n"))
	h.Write(raw)
	return hex.EncodeToString(h.Sum(nil))
}

func (v RecoveryStagePage) Bytes() ([]byte, string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, "", err
	}
	if _, err = DecodeRecoveryStagePage(b); err != nil {
		return nil, "", err
	}
	return b, RecoveryStagePageDigest(b), nil
}

// RecoveryStageReceipt is an acknowledgement of immutable staged metadata;
// it never proves completeness or authorizes release.
type RecoveryStageReceipt struct {
	Status     string `json:"status"`
	CommandID  string `json:"command_id"`
	RunID      string `json:"run_id"`
	PageSHA256 string `json:"page_sha256"`
}
