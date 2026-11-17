// Package sourcewire defines the versioned, opaque source snapshot protocol.
// It contains no database or client implementation and never handles raw IDs.
package sourcewire

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
)

const (
	MaxItemsPerPage        = 1000
	MaxPageBytes           = 512 << 10
	MaxPages               = 100
	MaxSnapshotBytes       = 32 << 20
	MaxDependenciesPerItem = 128
)

var (
	ErrInvalid  = errors.New("invalid source snapshot")
	uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

type Profile struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
}
type Item struct {
	Kind             string   `json:"kind"`
	Token            string   `json:"token"`
	Witness          string   `json:"witness"`
	Profile          *Profile `json:"evidence_profile"`
	EvidenceRevision string   `json:"evidence_revision"`
	Protection       string   `json:"protection"`
	Dependencies     []string `json:"dependencies"`
}
type Page struct {
	SnapshotID          string  `json:"snapshot_id"`
	LocalRevision       int64   `json:"local_revision"`
	InventoryRevision   int64   `json:"inventory_revision"`
	InventorySHA256     string  `json:"inventory_sha256"`
	EvaluationRevision  int64   `json:"evaluation_revision"`
	ExpectedPriorSHA256 string  `json:"expected_prior_sha256"`
	Index               int     `json:"index"`
	PreviousPageSHA256  *string `json:"previous_page_sha256"`
	Items               []Item  `json:"items"`
}
type Coverage struct {
	IdentityAmbiguous  int `json:"identity_ambiguous"`
	OverlapUnresolved  int `json:"overlap_unresolved"`
	CaptureGap         int `json:"capture_gap"`
	UnsupportedMeasure int `json:"unsupported_measure"`
	MappingConflict    int `json:"mapping_conflict"`
}
type Seal struct {
	SnapshotID          string   `json:"snapshot_id"`
	LocalRevision       int64    `json:"local_revision"`
	InventoryRevision   int64    `json:"inventory_revision"`
	InventorySHA256     string   `json:"inventory_sha256"`
	EvaluationRevision  int64    `json:"evaluation_revision"`
	ExpectedPriorSHA256 string   `json:"expected_prior_sha256"`
	PageCount           int      `json:"page_count"`
	ItemCount           int      `json:"item_count"`
	FinalPageSHA256     string   `json:"final_page_sha256"`
	InventoryComplete   bool     `json:"inventory_complete"`
	Coverage            Coverage `json:"coverage"`
}

func opaque(s string) bool {
	b, e := base64.RawURLEncoding.Strict().DecodeString(s)
	return e == nil && len(b) == sha256.Size && base64.RawURLEncoding.EncodeToString(b) == s
}
func digest(s string, empty bool) bool {
	if empty && s == "" {
		return true
	}
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == sha256.Size && hex.EncodeToString(b) == s
}
func validItem(v Item) bool {
	if !opaque(v.Token) || !opaque(v.Witness) || v.Dependencies == nil || len(v.Dependencies) > MaxDependenciesPerItem {
		return false
	}
	switch v.Protection {
	case "public", "private", "unknown", "unavailable":
	default:
		return false
	}
	switch v.Kind {
	case "codex_thread":
		if !(v.Protection == "unavailable" && v.Profile == nil && v.EvidenceRevision == "") && (v.Profile == nil || v.Profile.ID != "codex_thread_usage" || v.Profile.Version != 1 || !opaque(v.EvidenceRevision)) {
			return false
		}
	case "claude_reply":
		if !(v.Protection == "unavailable" && v.Profile == nil && v.EvidenceRevision == "") && (v.Profile == nil || v.Profile.ID != "claude_reply_usage" || v.Profile.Version != 1 || !opaque(v.EvidenceRevision)) {
			return false
		}
	case "content_component":
		if v.Profile != nil || v.EvidenceRevision != "" {
			return false
		}
	case "content_evidence":
		if len(v.Dependencies) != 1 || v.Profile == nil || v.Profile.ID != "codex_transcript" || v.Profile.Version != 1 || !opaque(v.EvidenceRevision) {
			return false
		}
	default:
		return false
	}
	previous := ""
	for _, dependency := range v.Dependencies {
		if !opaque(dependency) || dependency <= previous || dependency == v.Token {
			return false
		}
		previous = dependency
	}
	return true
}
func (p Page) Validate() error {
	if !uuidPattern.MatchString(p.SnapshotID) || p.LocalRevision < 1 || p.InventoryRevision < 1 || !digest(p.InventorySHA256, false) || p.EvaluationRevision < 1 || !digest(p.ExpectedPriorSHA256, true) || p.Index < 0 || p.Index >= MaxPages || p.Items == nil || len(p.Items) > MaxItemsPerPage {
		return ErrInvalid
	}
	if p.Index == 0 {
		if p.PreviousPageSHA256 != nil {
			return ErrInvalid
		}
	} else if p.PreviousPageSHA256 == nil || !digest(*p.PreviousPageSHA256, false) {
		return ErrInvalid
	}
	previous := ""
	for _, item := range p.Items {
		if !validItem(item) || item.Token <= previous {
			return ErrInvalid
		}
		previous = item.Token
	}
	return nil
}
func (s Seal) Validate() error {
	if !uuidPattern.MatchString(s.SnapshotID) || s.LocalRevision < 1 || s.InventoryRevision < 1 || !digest(s.InventorySHA256, false) || s.EvaluationRevision < 1 || !digest(s.ExpectedPriorSHA256, true) || s.PageCount < 1 || s.PageCount > MaxPages || s.ItemCount < 0 || s.ItemCount > s.PageCount*MaxItemsPerPage || !digest(s.FinalPageSHA256, false) {
		return ErrInvalid
	}
	for _, count := range []int{s.Coverage.IdentityAmbiguous, s.Coverage.OverlapUnresolved, s.Coverage.CaptureGap, s.Coverage.UnsupportedMeasure, s.Coverage.MappingConflict} {
		if count < 0 || count > 1000000 {
			return ErrInvalid
		}
	}
	return nil
}
func canonical(raw []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		return ErrInvalid
	}
	if _, e := d.Token(); e != io.EOF {
		return ErrInvalid
	}
	b, e := json.Marshal(v)
	if e != nil || !bytes.Equal(b, raw) {
		return ErrInvalid
	}
	return nil
}
func DecodePage(raw []byte) (Page, error) {
	var p Page
	if len(raw) > MaxPageBytes || canonical(raw, &p) != nil || p.Validate() != nil {
		return Page{}, ErrInvalid
	}
	return p, nil
}
func DecodeSeal(raw []byte) (Seal, error) {
	var s Seal
	if len(raw) > 4096 || canonical(raw, &s) != nil || s.Validate() != nil {
		return Seal{}, ErrInvalid
	}
	return s, nil
}
func SHA256(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
