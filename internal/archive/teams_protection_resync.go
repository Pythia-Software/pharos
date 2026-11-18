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
	return teamsOriginRevisionFromCompleteScan(start, request, page, token, witness, 500)
}

// teamsOriginRevisionFromCompleteScan is used only after a single complete
// page or a fully validated bounded cursor chain has been assembled.
func teamsOriginRevisionFromCompleteScan(start sourcewire.RecoveryStartReceipt, request sourcewire.RecoveryPageRequest, page sourcewire.RecoveryPageReceipt, token, witness string, limit int) (int64, string, error) {
	return teamsValidateRecoveryOriginItems(start, request, page, token, witness, limit, true)
}

func teamsValidateRecoveryOriginItems(start sourcewire.RecoveryStartReceipt, request sourcewire.RecoveryPageRequest, page sourcewire.RecoveryPageReceipt, token, witness string, limit int, lookup bool) (int64, string, error) {
	encoded, err := json.Marshal(request)
	if err != nil {
		return 0, "", err
	}
	if _, err = sourcewire.DecodeRecoveryPage(encoded); err != nil {
		return 0, "", errTeamsConsentStale
	}
	if (start.Status != "proof_required" && start.Status != "held") || !start.ExternalProofRequired || start.Generation < 1 || start.RunID != request.RunID || request.Family != "origins" || page.Family != "origins" || page.RunID != start.RunID || page.Generation != start.Generation || (page.Status != "proof_required" && page.Status != "held") || !page.ExternalProofRequired || page.Items == nil || len(page.Items) > limit || page.ServerTime < start.ServerTime || page.ServerTime >= start.ExpiresAt || page.UnacceptedFloor < start.UnacceptedFloor || (lookup && (!teamsOpaqueRecoveryValue(token) || !teamsOpaqueRecoveryValue(witness))) {
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
		if lookup && (value.Token == token) != (value.Witness == witness) {
			return 0, "", errTeamsConsentStale
		}
		if lookup && value.Token == token && value.Witness == witness {
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

const teamsRecoveryMaxOriginPages = 4
const teamsRecoveryMaxOriginBytes = 2 << 20

// Every request/receipt is retained separately by the caller. This validator
// accepts only a complete, monotone cursor chain for one authenticated run;
// a partial prefix or suffix can never establish absence.
type teamsRecoveryOriginScan struct {
	start     sourcewire.RecoveryStartReceipt
	last      sourcewire.RecoveryPageReceipt
	byToken   map[string]teamsRecoveryOrigin
	byWitness map[string]teamsRecoveryOrigin
	held      bool
}

func (s *teamsRecoveryOriginScan) lookup(token, witness string) (int64, string, error) {
	if s == nil || !teamsOpaqueRecoveryValue(token) || !teamsOpaqueRecoveryValue(witness) {
		return 0, "", errTeamsConsentStale
	}
	t, tok := s.byToken[token]
	w, wit := s.byWitness[witness]
	if tok != wit || (tok && (t.Witness != witness || w.Token != token)) {
		return 0, "", errTeamsConsentStale
	}
	if tok {
		return t.Revision, t.Protection, nil
	}
	return 0, "", nil
}

// Validate and index the complete bounded chain once, before any candidate
// lookup or source observation can use it.
func teamsBuildRecoveryOriginScan(start sourcewire.RecoveryStartReceipt, requests []sourcewire.RecoveryPageRequest, pages []sourcewire.RecoveryPageReceipt) (*teamsRecoveryOriginScan, error) {
	if start.ServerTime <= 0 || start.ExpiresAt <= start.ServerTime || start.UnacceptedFloor < 0 || (start.Status == "held" && !start.Held) {
		return nil, errTeamsConsentStale
	}
	if len(requests) == 0 || len(requests) != len(pages) || len(pages) > teamsRecoveryMaxOriginPages {
		return nil, errTeamsSourceUnavailable
	}
	combined := pages[len(pages)-1]
	combined.Items = []json.RawMessage{}
	seen := map[string]bool{}
	cursor := ""
	previousTime, previousFloor := start.ServerTime, start.UnacceptedFloor
	total := 0
	held := start.Held || start.Status == "held"
	for i, req := range requests {
		raw, e := json.Marshal(req)
		if e != nil {
			return nil, e
		}
		if _, e = sourcewire.DecodeRecoveryPage(raw); e != nil || req.RunID != start.RunID || req.Family != "origins" || req.Cursor != cursor || (i > 0 && req.Cursor == "") || seen[req.Cursor] {
			return nil, errTeamsConsentStale
		}
		seen[req.Cursor] = true
		p := pages[i]
		if p.RunID != start.RunID || p.Generation != start.Generation || p.ServerTime <= 0 || p.UnacceptedFloor < 0 || (p.Status == "held" && !p.Held) || p.Family != "origins" || (p.Status != "proof_required" && p.Status != "held") || !p.ExternalProofRequired || p.Items == nil || len(p.Items) > 500 || p.ServerTime < previousTime || p.ServerTime >= start.ExpiresAt || p.UnacceptedFloor < previousFloor || (p.NextCursor != "" && len(p.Items) == 0) || (i < len(pages)-1 && p.NextCursor == "") || (i == len(pages)-1 && p.NextCursor != "") || (p.NextCursor != "" && seen[p.NextCursor]) {
			return nil, errTeamsConsentStale
		}
		for _, item := range p.Items {
			if len(item) > teamsRecoveryMaxOriginBytes-total {
				return nil, errTeamsSourceUnavailable
			}
			total += len(item)
			combined.Items = append(combined.Items, item)
		}
		held = held || p.Held || p.Status == "held"
		cursor, previousTime, previousFloor = p.NextCursor, p.ServerTime, p.UnacceptedFloor
	}
	if _, _, e := teamsValidateRecoveryOriginItems(start, requests[0], combined, "", "", teamsRecoveryMaxOriginPages*500, false); e != nil {
		return nil, e
	}
	scan := &teamsRecoveryOriginScan{start: start, last: pages[len(pages)-1], byToken: map[string]teamsRecoveryOrigin{}, byWitness: map[string]teamsRecoveryOrigin{}, held: held}
	for _, raw := range combined.Items {
		var v teamsRecoveryOrigin
		if json.Unmarshal(raw, &v) != nil {
			return nil, errTeamsConsentStale
		}
		if _, ok := scan.byToken[v.Token]; ok {
			return nil, errTeamsConsentStale
		}
		if _, ok := scan.byWitness[v.Witness]; ok {
			return nil, errTeamsConsentStale
		}
		scan.byToken[v.Token] = v
		scan.byWitness[v.Witness] = v
	}
	return scan, nil
}
func teamsOriginRevisionFromRecoveryScan(start sourcewire.RecoveryStartReceipt, requests []sourcewire.RecoveryPageRequest, pages []sourcewire.RecoveryPageReceipt, token, witness string) (int64, string, error) {
	scan, err := teamsBuildRecoveryOriginScan(start, requests, pages)
	if err != nil {
		return 0, "", err
	}
	return scan.lookup(token, witness)
}
