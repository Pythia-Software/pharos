package sourcewire

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"hash"
	"io"
	"strconv"
)

const MaxAssignmentsPerPage = 1000

type Assignment struct {
	Token    string `json:"token"`
	Decision string `json:"decision"`
}
type AssignmentHeader struct {
	BindingID         string `json:"binding_id"`
	SnapshotID        string `json:"snapshot_id"`
	InventorySHA256   string `json:"inventory_sha256"`
	OwnershipRevision int64  `json:"ownership_revision"`
	PrivacyRevision   int64  `json:"privacy_revision"`
}
type AssignmentHasher struct {
	h        hash.Hash
	previous string
	count    int
	finished bool
}

func NewAssignmentHasher(v AssignmentHeader) (*AssignmentHasher, error) {
	if !uuidPattern.MatchString(v.BindingID) || !uuidPattern.MatchString(v.SnapshotID) || !digest(v.InventorySHA256, false) || v.OwnershipRevision < 0 || v.PrivacyRevision < 0 {
		return nil, ErrInvalid
	}
	h := sha256.New()
	io.WriteString(h, "pharos-source-assignments-v2\n{\"binding_id\":\""+v.BindingID+"\",\"snapshot_id\":\""+v.SnapshotID+"\",\"inventory_sha256\":\""+v.InventorySHA256+"\",\"ownership_revision\":"+strconv.FormatInt(v.OwnershipRevision, 10)+",\"privacy_revision\":"+strconv.FormatInt(v.PrivacyRevision, 10)+",\"items\":[")
	return &AssignmentHasher{h: h}, nil
}
func (h *AssignmentHasher) Add(item Assignment) error {
	if h == nil || h.finished || !opaque(item.Token) || item.Token <= h.previous || h.count >= MaxPages*MaxItemsPerPage || (item.Decision != "owned" && item.Decision != "excluded" && item.Decision != "unavailable") {
		return ErrInvalid
	}
	b, e := json.Marshal(item)
	if e != nil {
		return ErrInvalid
	}
	if h.count > 0 {
		io.WriteString(h.h, ",")
	}
	_, _ = h.h.Write(b)
	h.previous = item.Token
	h.count++
	return nil
}
func (h *AssignmentHasher) Finish() (string, int, error) {
	if h == nil || h.finished {
		return "", 0, ErrInvalid
	}
	h.finished = true
	io.WriteString(h.h, "]}")
	return hex.EncodeToString(h.h.Sum(nil)), h.count, nil
}
