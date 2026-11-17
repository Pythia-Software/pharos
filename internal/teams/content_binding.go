package teams

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
)

type ContentProcessing struct {
	Execution string `json:"execution"`
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	Region    string `json:"region,omitempty"`
	Route     string `json:"route,omitempty"`
}
type ContentEvidence struct {
	Token              string   `json:"token"`
	Kind               string   `json:"kind"`
	ProfileID          string   `json:"profile_id"`
	ProfileVersion     int      `json:"profile_version"`
	EvidenceRevision   string   `json:"evidence_revision"`
	Protection         string   `json:"protection"`
	Dependencies       []string `json:"dependencies"`
	EffectiveProtected bool     `json:"effective_protected"`
	Selected           bool     `json:"selected"`
}
type ContentBinding struct {
	WorkID             string            `json:"work_id"`
	LibraryID          string            `json:"library_id"`
	InstallationID     string            `json:"installation_id"`
	SnapshotID         string            `json:"snapshot_id"`
	InventorySHA256    string            `json:"inventory_sha256"`
	ConfigSHA256       string            `json:"config_sha256"`
	Processing         ContentProcessing `json:"processing"`
	DecisionID         string            `json:"decision_id"`
	InventoryRevision  int64             `json:"inventory_revision"`
	CatalogRevision    int64             `json:"catalog_revision"`
	EvaluationRevision int64             `json:"evaluation_revision"`
	LocalRevision      int64             `json:"local_revision"`
	PrivacyRevision    int64             `json:"privacy_revision"`
	OwnershipRevision  int64             `json:"ownership_revision"`
	Components         []ContentEvidence `json:"components"`
	SHA256             string            `json:"sha256"`
}

// VerifyContentBinding verifies both the canonical service digest and the
// exact expected locally sealed closure. It never treats a server page alone
// as authority to read data absent from the local snapshot.
func VerifyContentBinding(binding ContentBinding, expected []ContentEvidence, digest string) error {
	invalid := errors.New("Teams content binding did not match the sealed local closure")
	if digest == "" || len(binding.Components) == 0 || len(binding.Components) > 10000 || len(expected) != len(binding.Components) || binding.Processing.Execution != "local" || binding.Processing.Provider == "" || binding.Processing.Model == "" || binding.DecisionID == "" || binding.InventoryRevision < 1 || binding.LocalRevision < 1 || binding.CatalogRevision < binding.LocalRevision || binding.EvaluationRevision < 1 || binding.PrivacyRevision < 0 || binding.OwnershipRevision < 0 {
		return invalid
	}
	for i, item := range binding.Components {
		if item.Token == "" || (i > 0 && binding.Components[i-1].Token >= item.Token) || item.Protection != "public" || item.EffectiveProtected || item.Dependencies == nil || !sort.StringsAreSorted(item.Dependencies) {
			return invalid
		}
		a, e := json.Marshal(item)
		if e != nil {
			return invalid
		}
		b, e := json.Marshal(expected[i])
		if e != nil || string(a) != string(b) {
			return invalid
		}
	}
	binding.SHA256 = ""
	raw, e := json.Marshal(binding)
	if e != nil {
		return invalid
	}
	sum := sha256.Sum256(append([]byte("pharos-content-binding-v1\x00"), raw...))
	if hex.EncodeToString(sum[:]) != digest {
		return invalid
	}
	return nil
}
