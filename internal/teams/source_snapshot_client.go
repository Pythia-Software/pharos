package teams

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/gbdubs/pharos/internal/teams/sourcewire"
)

// SourcePageReceipt proves only durable staging; it does not grant ownership,
// authorize a query or imply that the inventory was sealed.
type SourcePageReceipt struct {
	SnapshotID string `json:"snapshot_id"`
	Index      int    `json:"index"`
	PageSHA256 string `json:"page_sha256"`
}

// StageSourcePage sends the exact canonical page. Callers must persist the
// identity/exposure fence and recheck privacy and policy under the sharing
// gate before calling it. A transport error is uncertain; retry exact bytes.
func (c *DeviceClient) StageSourcePage(ctx context.Context, workID string, raw []byte) (SourcePageReceipt, error) {
	var out SourcePageReceipt
	if !validDeviceID(workID) {
		return out, errors.New("invalid work identity")
	}
	raw = bytes.Clone(raw)
	page, err := sourcewire.DecodePage(raw)
	if err != nil {
		return out, err
	}
	path := fmt.Sprintf("/v1/device/work/%s/source-snapshots/%s/pages/%d", workID, page.SnapshotID, page.Index)
	if err = c.exchange(ctx, http.MethodPut, path, raw, &out); err != nil {
		return SourcePageReceipt{}, err
	}
	if out.SnapshotID != page.SnapshotID || out.Index != page.Index || out.PageSHA256 != sourcewire.SHA256(raw) {
		return SourcePageReceipt{}, errors.New("Teams source page receipt does not match")
	}
	return out, nil
}
