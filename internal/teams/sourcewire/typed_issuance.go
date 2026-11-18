package sourcewire

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// TypedBindingRequest is distinct from the legacy source binding wire. The
// ID is derived from the remaining fields and authenticated scope.
type TypedBindingRequest struct {
	BindingID          string `json:"binding_id"`
	SnapshotID         string `json:"snapshot_id"`
	InventoryRevision  int64  `json:"inventory_revision"`
	InventorySHA256    string `json:"inventory_sha256"`
	LocalRevision      int64  `json:"local_revision"`
	CatalogRevision    int64  `json:"catalog_revision"`
	EvaluationRevision int64  `json:"evaluation_revision"`
	ConfigSHA256       string `json:"config_sha256"`
	OwnershipRevision  int64  `json:"ownership_revision"`
	PrivacyRevision    int64  `json:"privacy_revision"`
	IssuedAt           int64  `json:"issued_at"`
}

func TypedBindingID(org, person, installation, library, work string, r TypedBindingRequest) (string, error) {
	if !uuidPattern.MatchString(org) || !uuidPattern.MatchString(person) || !uuidPattern.MatchString(installation) || !uuidPattern.MatchString(work) || !uuidPattern.MatchString(r.SnapshotID) || library == "" || len(library) > 128 || !digest(r.InventorySHA256, false) || !digest(r.ConfigSHA256, false) || r.InventoryRevision < 1 || r.LocalRevision < 1 || r.CatalogRevision < r.LocalRevision || r.EvaluationRevision < 1 || r.OwnershipRevision < 1 || r.PrivacyRevision < 0 || r.IssuedAt <= 0 || r.IssuedAt > 253402300799 {
		return "", ErrInvalid
	}
	v := struct {
		Org               string `json:"org_id"`
		Person            string `json:"person_id"`
		Installation      string `json:"installation_id"`
		Library           string `json:"library_id"`
		Work              string `json:"work_id"`
		Snapshot          string `json:"snapshot_id"`
		InventoryRevision int64  `json:"inventory_revision"`
		InventoryHash     string `json:"inventory_sha256"`
		Local             int64  `json:"local_revision"`
		Catalog           int64  `json:"catalog_revision"`
		Evaluation        int64  `json:"evaluation_revision"`
		Config            string `json:"config_sha256"`
		Ownership         int64  `json:"ownership_revision"`
		Privacy           int64  `json:"privacy_revision"`
		Issued            int64  `json:"issued_at"`
	}{org, person, installation, library, work, r.SnapshotID, r.InventoryRevision, r.InventorySHA256, r.LocalRevision, r.CatalogRevision, r.EvaluationRevision, r.ConfigSHA256, r.OwnershipRevision, r.PrivacyRevision, r.IssuedAt}
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, _ = h.Write([]byte("pharos-typed-source-binding-id-v2\n"))
	_, _ = h.Write(raw)
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
