package sourcewire

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
)

var profileIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// Profile coverage is attached to an existing ownership/protection anchor.
// Items never introduce ownership and contain no raw source records or values.
type ProfileItem struct {
	Token            string   `json:"token"`
	Witness          string   `json:"witness"`
	Dependencies     []string `json:"dependencies"`
	EvidenceRevision string   `json:"evidence_revision"`
}
type ProfilePage struct {
	SnapshotID          string        `json:"snapshot_id"`
	InventorySHA256     string        `json:"inventory_sha256"`
	ProfileID           string        `json:"profile_id"`
	ProfileVersion      int           `json:"profile_version"`
	ProfileRevision     int64         `json:"profile_revision"`
	LocalRevision       int64         `json:"local_revision"`
	ExpectedPriorSHA256 string        `json:"expected_prior_sha256"`
	Index               int           `json:"index"`
	PreviousPageSHA256  *string       `json:"previous_page_sha256"`
	Items               []ProfileItem `json:"items"`
}
type ProfileSeal struct {
	SnapshotID          string   `json:"snapshot_id"`
	InventorySHA256     string   `json:"inventory_sha256"`
	ProfileID           string   `json:"profile_id"`
	ProfileVersion      int      `json:"profile_version"`
	ProfileRevision     int64    `json:"profile_revision"`
	LocalRevision       int64    `json:"local_revision"`
	ExpectedPriorSHA256 string   `json:"expected_prior_sha256"`
	PageCount           int      `json:"page_count"`
	ItemCount           int      `json:"item_count"`
	FinalPageSHA256     string   `json:"final_page_sha256"`
	Complete            bool     `json:"complete"`
	Coverage            Coverage `json:"coverage"`
}

// Receipts acknowledge canonical page/seal bytes; held is nonacceptance.
type ProfilePageReceipt struct {
	Status          string `json:"status"`
	SnapshotID      string `json:"snapshot_id"`
	ProfileID       string `json:"profile_id"`
	ProfileVersion  int    `json:"profile_version"`
	ProfileRevision int64  `json:"profile_revision"`
	Index           int    `json:"index"`
	PageSHA256      string `json:"page_sha256"`
}
type ProfileSealReceipt struct {
	Status          string `json:"status"`
	SnapshotID      string `json:"snapshot_id"`
	ProfileID       string `json:"profile_id"`
	ProfileVersion  int    `json:"profile_version"`
	ProfileRevision int64  `json:"profile_revision"`
	SealSHA256      string `json:"seal_sha256"`
	Complete        bool   `json:"complete"`
	ItemCount       int    `json:"item_count"`
}
type ProfileReference struct {
	ProfileID           string `json:"profile_id"`
	ProfileVersion      int    `json:"profile_version"`
	ProfileRevision     int64  `json:"profile_revision"`
	ProfileSHA256       string `json:"profile_sha256"`
	AssignmentPageCount int    `json:"assignment_page_count"`
	AssignmentItemCount int    `json:"assignment_item_count"`
	AssignmentSHA256    string `json:"assignment_sha256"`
}

// TypedBindingEnvelope is a contract value, not an issued binding. A legacy
// binding and its assignment cannot satisfy this domain-separated envelope.
type TypedBindingEnvelope struct {
	Version            int                `json:"version"`
	BindingID          string             `json:"binding_id"`
	WorkID             string             `json:"work_id"`
	InstallationID     string             `json:"installation_id"`
	LibraryID          string             `json:"library_id"`
	SnapshotID         string             `json:"snapshot_id"`
	InventorySHA256    string             `json:"inventory_sha256"`
	ConfigSHA256       string             `json:"config_sha256"`
	WindowSHA256       string             `json:"window_sha256"`
	LocalRevision      int64              `json:"local_revision"`
	CatalogRevision    int64              `json:"catalog_revision"`
	EvaluationRevision int64              `json:"evaluation_revision"`
	OwnershipRevision  int64              `json:"ownership_revision"`
	PrivacyRevision    int64              `json:"privacy_revision"`
	Profiles           []ProfileReference `json:"profiles"`
}

func profileIdentity(snapshot, inventory, id string, version int, revision, local int64, prior string) bool {
	return uuidPattern.MatchString(snapshot) && digest(inventory, false) && profileIDPattern.MatchString(id) && version >= 1 && version <= 65535 && revision >= 1 && local >= 1 && digest(prior, true)
}
func (v ProfileItem) Validate() error {
	if !opaque(v.Token) || !opaque(v.Witness) || !opaque(v.EvidenceRevision) || len(v.Dependencies) != 1 || !opaque(v.Dependencies[0]) || v.Dependencies[0] == v.Token {
		return ErrInvalid
	}
	return nil
}
func (p ProfilePage) Validate() error {
	if !profileIdentity(p.SnapshotID, p.InventorySHA256, p.ProfileID, p.ProfileVersion, p.ProfileRevision, p.LocalRevision, p.ExpectedPriorSHA256) || p.Index < 0 || p.Index >= MaxPages || p.Items == nil || len(p.Items) > MaxItemsPerPage {
		return ErrInvalid
	}
	if p.Index == 0 {
		if p.PreviousPageSHA256 != nil {
			return ErrInvalid
		}
	} else if p.PreviousPageSHA256 == nil || !digest(*p.PreviousPageSHA256, false) {
		return ErrInvalid
	}
	prev := ""
	for _, v := range p.Items {
		if v.Validate() != nil || v.Token <= prev {
			return ErrInvalid
		}
		prev = v.Token
	}
	return nil
}
func (s ProfileSeal) Validate() error {
	if !profileIdentity(s.SnapshotID, s.InventorySHA256, s.ProfileID, s.ProfileVersion, s.ProfileRevision, s.LocalRevision, s.ExpectedPriorSHA256) || s.PageCount < 1 || s.PageCount > MaxPages || s.ItemCount < 0 || s.ItemCount > s.PageCount*MaxItemsPerPage || !digest(s.FinalPageSHA256, false) {
		return ErrInvalid
	}
	for _, n := range []int{s.Coverage.IdentityAmbiguous, s.Coverage.OverlapUnresolved, s.Coverage.CaptureGap, s.Coverage.UnsupportedMeasure, s.Coverage.MappingConflict} {
		if n < 0 || n > 1000000 {
			return ErrInvalid
		}
	}
	return nil
}
func (v TypedBindingEnvelope) Validate() error {
	if v.Version != 2 || !uuidPattern.MatchString(v.BindingID) || !uuidPattern.MatchString(v.WorkID) || !uuidPattern.MatchString(v.InstallationID) || !uuidPattern.MatchString(v.SnapshotID) || v.LibraryID == "" || len(v.LibraryID) > 128 || !digest(v.InventorySHA256, false) || !digest(v.ConfigSHA256, false) || !digest(v.WindowSHA256, false) || v.LocalRevision < 1 || v.CatalogRevision < 1 || v.EvaluationRevision < 1 || v.OwnershipRevision < 1 || v.PrivacyRevision < 0 || v.Profiles == nil || len(v.Profiles) < 1 || len(v.Profiles) > 128 {
		return ErrInvalid
	}
	var prior ProfileReference
	for _, p := range v.Profiles {
		if !profileIDPattern.MatchString(p.ProfileID) || p.ProfileVersion < 1 || p.ProfileVersion > 65535 || p.ProfileRevision < 1 || !digest(p.ProfileSHA256, false) || p.AssignmentPageCount < 1 || p.AssignmentPageCount > MaxPages || p.AssignmentItemCount < 0 || p.AssignmentItemCount > p.AssignmentPageCount*MaxItemsPerPage || !digest(p.AssignmentSHA256, false) || p.ProfileID < prior.ProfileID || (p.ProfileID == prior.ProfileID && p.ProfileVersion <= prior.ProfileVersion) {
			return ErrInvalid
		}
		prior = p
	}
	return nil
}
func DecodeProfilePage(raw []byte) (ProfilePage, error) {
	var v ProfilePage
	if len(raw) > MaxPageBytes || canonical(raw, &v) != nil || v.Validate() != nil {
		return ProfilePage{}, ErrInvalid
	}
	return v, nil
}
func DecodeProfileSeal(raw []byte) (ProfileSeal, error) {
	var v ProfileSeal
	if len(raw) > 4096 || canonical(raw, &v) != nil || v.Validate() != nil {
		return ProfileSeal{}, ErrInvalid
	}
	return v, nil
}
func (p ProfilePage) Bytes() ([]byte, string, error) {
	if p.Validate() != nil {
		return nil, "", ErrInvalid
	}
	raw, e := json.Marshal(p)
	if e != nil {
		return nil, "", e
	}
	return raw, ProfilePageDigest(raw), nil
}
func (s ProfileSeal) Bytes() ([]byte, string, error) {
	if s.Validate() != nil {
		return nil, "", ErrInvalid
	}
	raw, e := json.Marshal(s)
	if e != nil {
		return nil, "", e
	}
	return raw, ProfileSealDigest(raw), nil
}
func (v TypedBindingEnvelope) Bytes() ([]byte, string, error) {
	if v.Validate() != nil {
		return nil, "", ErrInvalid
	}
	raw, e := json.Marshal(v)
	if e != nil {
		return nil, "", e
	}
	return raw, domainDigest("pharos-typed-source-binding-v2\n", raw), nil
}
func domainDigest(domain string, raw []byte) string {
	h := sha256.New()
	h.Write([]byte(domain))
	h.Write(raw)
	return hex.EncodeToString(h.Sum(nil))
}
func ProfilePageDigest(raw []byte) string {
	return domainDigest("pharos-source-profile-page-v1\n", raw)
}
func ProfileSealDigest(raw []byte) string {
	return domainDigest("pharos-source-profile-seal-v1\n", raw)
}

// TypedAssignmentPage is a page within a separately hashed typed binding.
// Its status values do not grant authority until every page is verified.
type TypedAssignment struct {
	Token          string         `json:"token"`
	Decision       string         `json:"decision"`
	Protection     string         `json:"protection"`
	Classification Classification `json:"classification"`
}

// Assignment pages identify one profile; the envelope carries the commitment.
type AssignmentProfileIdentity struct {
	ProfileID       string `json:"profile_id"`
	ProfileVersion  int    `json:"profile_version"`
	ProfileRevision int64  `json:"profile_revision"`
	ProfileSHA256   string `json:"profile_sha256"`
}
type TypedAssignmentPage struct {
	BindingSHA256      string                    `json:"binding_sha256"`
	Profile            AssignmentProfileIdentity `json:"profile"`
	Index              int                       `json:"index"`
	PageCount          int                       `json:"page_count"`
	PreviousPageSHA256 *string                   `json:"previous_page_sha256"`
	Items              []TypedAssignment         `json:"items"`
}

func (v TypedAssignmentPage) Validate() error {
	p := v.Profile
	if !digest(v.BindingSHA256, false) || !profileIDPattern.MatchString(p.ProfileID) || p.ProfileVersion < 1 || p.ProfileVersion > 65535 || p.ProfileRevision < 1 || !digest(p.ProfileSHA256, false) || v.PageCount < 1 || v.PageCount > MaxPages || v.Index < 0 || v.Index >= v.PageCount || v.Items == nil || len(v.Items) > MaxItemsPerPage {
		return ErrInvalid
	}
	if (v.Index == 0 && v.PreviousPageSHA256 != nil) || (v.Index > 0 && (v.PreviousPageSHA256 == nil || !digest(*v.PreviousPageSHA256, false))) {
		return ErrInvalid
	}
	previous := ""
	for _, item := range v.Items {
		if !opaque(item.Token) || item.Token <= previous {
			return ErrInvalid
		}
		previous = item.Token
		switch item.Decision {
		case "owned", "excluded", "unavailable":
		default:
			return ErrInvalid
		}
		switch item.Protection {
		case "non_private", "explicit_private", "inherited_private", "flagged", "unknown":
		default:
			return ErrInvalid
		}
		switch item.Classification.State {
		case "unclassified", "pending", "failed", "stale", "clear":
		default:
			return ErrInvalid
		}
		if (item.Classification.Required || item.Classification.PolicySHA256 != "") && !digest(item.Classification.PolicySHA256, false) {
			return ErrInvalid
		}
		if item.Classification.State == "clear" || item.Classification.EvidenceRevision != "" {
			if !opaque(item.Classification.EvidenceRevision) || !digest(item.Classification.PolicySHA256, false) {
				return ErrInvalid
			}
		}
	}
	return nil
}
func (v TypedAssignmentPage) Bytes() ([]byte, string, error) {
	if v.Validate() != nil {
		return nil, "", ErrInvalid
	}
	raw, e := json.Marshal(v)
	if e != nil {
		return nil, "", e
	}
	// The commitment excludes binding_sha256 to avoid a hash cycle: the
	// envelope commits to this chain, while each transmitted page names the
	// final envelope digest. Profile indices are independent and start at zero.
	commitment, e := json.Marshal(struct {
		Profile            AssignmentProfileIdentity `json:"profile"`
		Index              int                       `json:"index"`
		PageCount          int                       `json:"page_count"`
		PreviousPageSHA256 *string                   `json:"previous_page_sha256"`
		Items              []TypedAssignment         `json:"items"`
	}{v.Profile, v.Index, v.PageCount, v.PreviousPageSHA256, v.Items})
	if e != nil {
		return nil, "", e
	}
	return raw, domainDigest("pharos-typed-source-assignment-page-v2\n", commitment), nil
}

// VerifyTypedAssignmentPages validates the complete ordered set for one
// profile before any assignment can authorize a binding.
func VerifyTypedAssignmentPages(bindingSHA256 string, ref ProfileReference, pages []TypedAssignmentPage) error {
	if !digest(bindingSHA256, false) || ref.AssignmentPageCount < 1 || ref.AssignmentPageCount > MaxPages || ref.AssignmentItemCount < 0 || ref.AssignmentItemCount > ref.AssignmentPageCount*MaxItemsPerPage || !digest(ref.AssignmentSHA256, false) || len(pages) != ref.AssignmentPageCount {
		return ErrInvalid
	}
	var previous string
	total := 0
	lastToken := ""
	for i, page := range pages {
		if page.BindingSHA256 != bindingSHA256 || page.Profile != (AssignmentProfileIdentity{ref.ProfileID, ref.ProfileVersion, ref.ProfileRevision, ref.ProfileSHA256}) || page.Index != i || page.PageCount != ref.AssignmentPageCount {
			return ErrInvalid
		}
		if i > 0 && (page.PreviousPageSHA256 == nil || *page.PreviousPageSHA256 != previous) {
			return ErrInvalid
		}
		_, hash, err := page.Bytes()
		if err != nil {
			return err
		}
		previous = hash
		for _, item := range page.Items {
			if item.Token <= lastToken {
				return ErrInvalid
			}
			lastToken = item.Token
			total++
		}
	}
	if previous != ref.AssignmentSHA256 || total != ref.AssignmentItemCount {
		return ErrInvalid
	}
	return nil
}
