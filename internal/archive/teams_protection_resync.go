package archive

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"

	"github.com/gbdubs/pharos/internal/teams"
	"github.com/gbdubs/pharos/internal/teams/sourcewire"
)

type teamsRecoveryOrigin struct {
	Token            string                    `json:"token"`
	Witness          string                    `json:"witness"`
	Revision         int64                     `json:"revision"`
	EvidenceRevision string                    `json:"evidence_revision"`
	Protection       string                    `json:"protection"`
	FlagFloor        string                    `json:"flag_floor"`
	Classification   sourcewire.Classification `json:"classification"`
}

// This first lane requires an entire first-and-final authenticated page: a
// later page could contain a conflicting witness even when a token matched.
// The caller must obtain both receipts through the signed device client and
// must not treat this metadata as a non-private decision.
func teamsOpaqueRecoveryValue(v string) bool {
	raw, err := base64.RawURLEncoding.Strict().DecodeString(v)
	return err == nil && len(raw) == 32 && base64.RawURLEncoding.EncodeToString(raw) == v
}

func teamsOriginRevisionFromRecoveryPage(start sourcewire.RecoveryStartReceipt, request sourcewire.RecoveryPageRequest, page sourcewire.RecoveryPageReceipt, token, witness string) (int64, string, error) {
	encoded, err := json.Marshal(request)
	if err != nil {
		return 0, "", err
	}
	if _, err = sourcewire.DecodeRecoveryPage(encoded); err != nil {
		return 0, "", errTeamsConsentStale
	}
	if (start.Status != "proof_required" && start.Status != "held") || !start.ExternalProofRequired || start.Generation < 1 || start.RunID != request.RunID || request.Family != "origins" || page.Family != "origins" || page.RunID != start.RunID || page.Generation != start.Generation || (page.Status != "proof_required" && page.Status != "held") || !page.ExternalProofRequired || page.Items == nil || len(page.Items) > 500 || page.ServerTime < start.ServerTime || page.ServerTime >= start.ExpiresAt || page.UnacceptedFloor < start.UnacceptedFloor || !teamsOpaqueRecoveryValue(token) || !teamsOpaqueRecoveryValue(witness) {
		return 0, "", errTeamsConsentStale
	}
	if request.Cursor != "" || page.NextCursor != "" {
		return 0, "", errors.New("source origin scan is incomplete")
	}
	var found *teamsRecoveryOrigin
	previousToken, previousWitness := "", ""
	for _, raw := range page.Items {
		if !teams.ValidUniqueJSON(raw) {
			return 0, "", errTeamsConsentStale
		}
		allowed := map[string]bool{"token": true, "witness": true, "revision": true, "evidence_revision": true, "protection": true, "flag_floor": true, "classification": true}
		fields, e := teamsStrictObject(raw, allowed)
		if e != nil || len(fields) != len(allowed) {
			return 0, "", errTeamsConsentStale
		}
		classFields, e := teamsStrictObject(fields["classification"], map[string]bool{"state": true, "required": true, "evidence_revision": true, "policy_sha256": true})
		if e != nil || len(classFields) != 4 {
			return 0, "", errTeamsConsentStale
		}
		var value teamsRecoveryOrigin
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&value) != nil || value.Revision < 1 || value.Token == "" || value.Witness == "" {
			return 0, "", errTeamsConsentStale
		}
		if !teamsOpaqueRecoveryValue(value.Token) || !teamsOpaqueRecoveryValue(value.Witness) || !teamsOpaqueRecoveryValue(value.EvidenceRevision) || (value.Token < previousToken || (value.Token == previousToken && value.Witness <= previousWitness)) {
			return 0, "", errTeamsConsentStale
		}
		switch value.Protection {
		case "non_private", "explicit_private", "inherited_private", "flagged", "unknown":
		default:
			return 0, "", errTeamsConsentStale
		}
		switch value.FlagFloor {
		case "", "non_private", "unknown":
		default:
			return 0, "", errTeamsConsentStale
		}
		switch value.Classification.State {
		case "unclassified", "pending", "failed", "stale", "clear":
		default:
			return 0, "", errTeamsConsentStale
		}
		if value.Classification.State == "clear" || value.Classification.EvidenceRevision != "" || value.Classification.PolicySHA256 != "" {
			if !teamsOpaqueRecoveryValue(value.Classification.EvidenceRevision) || !validRecordHash(value.Classification.PolicySHA256) || (value.Classification.State != "stale" && value.Classification.EvidenceRevision != value.EvidenceRevision) {
				return 0, "", errTeamsConsentStale
			}
		}
		previousToken, previousWitness = value.Token, value.Witness
		// A different witness for this token, or this witness under another
		// token, is a conflict rather than proof of a new origin.
		if (value.Token == token) != (value.Witness == witness) {
			return 0, "", errTeamsConsentStale
		}
		if value.Token == token && value.Witness == witness {
			if found != nil {
				return 0, "", errTeamsConsentStale
			}
			copy := value
			found = &copy
		}
	}
	if found != nil {
		return found.Revision, found.Protection, nil
	}
	return 0, "", nil
}
