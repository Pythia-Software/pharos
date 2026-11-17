package sourcewire

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"hash"
	"io"
	"strconv"
)

// InventoryHasher binds canonical intrinsic inventory, coverage and completeness
// independently of upload pages, work IDs, evaluations and local freshness.
// Items must be supplied once each in ascending token order.
type InventoryHasher struct {
	h            hash.Hash
	previous     string
	count, bytes int
	finished     bool
}

func NewInventoryHasher(complete bool, coverage Coverage) (*InventoryHasher, error) {
	for _, count := range []int{coverage.IdentityAmbiguous, coverage.OverlapUnresolved, coverage.CaptureGap, coverage.UnsupportedMeasure, coverage.MappingConflict} {
		if count < 0 || count > 1000000 {
			return nil, ErrInvalid
		}
	}
	c, err := json.Marshal(coverage)
	if err != nil {
		return nil, ErrInvalid
	}
	prefix := "pharos-source-inventory-v2\n{\"inventory_complete\":" + strconv.FormatBool(complete) + ",\"coverage\":" + string(c) + ",\"items\":["
	h := sha256.New()
	io.WriteString(h, prefix)
	return &InventoryHasher{h: h, bytes: len(prefix) + 2}, nil
}
func (v *InventoryHasher) Add(item Item) error {
	if v == nil || v.finished || !validItem(item) || item.Token <= v.previous || v.count >= MaxPages*MaxItemsPerPage {
		return ErrInvalid
	}
	b, err := json.Marshal(item)
	extra := len(b)
	if v.count > 0 {
		extra++
	}
	if err != nil || v.bytes+extra > MaxSnapshotBytes {
		return ErrInvalid
	}
	if v.count > 0 {
		io.WriteString(v.h, ",")
	}
	_, _ = v.h.Write(b)
	v.previous = item.Token
	v.count++
	v.bytes += extra
	return nil
}
func (v *InventoryHasher) Finish() (string, int, error) {
	if v == nil || v.finished {
		return "", 0, ErrInvalid
	}
	v.finished = true
	io.WriteString(v.h, "]}")
	return hex.EncodeToString(v.h.Sum(nil)), v.count, nil
}
