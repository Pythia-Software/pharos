package teams

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/gbdubs/pharos/internal/teams/sourcewire"
)

type ProfilePageReceipt = sourcewire.ProfilePageReceipt
type ProfileSealReceipt = sourcewire.ProfileSealReceipt

func profilePath(workID, snapshot, id string, version int) (string, error) {
	if !validDeviceID(workID) || !validDeviceID(snapshot) || id != "codex_request_usage" || version != 1 {
		return "", errors.New("invalid Teams source profile")
	}
	return fmt.Sprintf("/v1/device/work/%s/source-snapshots/%s/profiles/%s/%d", workID, snapshot, id, version), nil
}
func (c *DeviceClient) StageSourceProfilePage(ctx context.Context, workID string, raw []byte) (ProfilePageReceipt, error) {
	var out ProfilePageReceipt
	page, err := sourcewire.DecodeProfilePage(raw)
	if err != nil {
		return out, err
	}
	path, err := profilePath(workID, page.SnapshotID, page.ProfileID, page.ProfileVersion)
	if err != nil {
		return out, err
	}
	var response json.RawMessage
	if err = c.exchange(ctx, http.MethodPut, fmt.Sprintf("%s/pages/%d", path, page.Index), raw, &response); err != nil {
		return out, err
	}
	if sourceResponse(response, &out, "status", "snapshot_id", "profile_id", "profile_version", "profile_revision", "index", "page_sha256") != nil || out.SnapshotID != page.SnapshotID || out.ProfileID != page.ProfileID || out.ProfileVersion != page.ProfileVersion || out.ProfileRevision != page.ProfileRevision || out.Index != page.Index || (out.Status != "accepted" && out.Status != "held") || out.PageSHA256 != sourcewire.ProfilePageDigest(raw) {
		return ProfilePageReceipt{}, errors.New("invalid Teams profile page receipt")
	}
	return out, nil
}
func (c *DeviceClient) SealSourceProfile(ctx context.Context, workID string, raw []byte) (ProfileSealReceipt, error) {
	var out ProfileSealReceipt
	seal, err := sourcewire.DecodeProfileSeal(raw)
	if err != nil {
		return out, err
	}
	path, err := profilePath(workID, seal.SnapshotID, seal.ProfileID, seal.ProfileVersion)
	if err != nil {
		return out, err
	}
	var response json.RawMessage
	if err = c.exchange(ctx, http.MethodPut, path+"/seal", raw, &response); err != nil {
		return out, err
	}
	if sourceResponse(response, &out, "status", "snapshot_id", "profile_id", "profile_version", "profile_revision", "seal_sha256", "complete", "item_count") != nil || out.SnapshotID != seal.SnapshotID || out.ProfileID != seal.ProfileID || out.ProfileVersion != seal.ProfileVersion || out.ProfileRevision != seal.ProfileRevision || ((out.Status == "accepted" && (out.SealSHA256 != sourcewire.ProfileSealDigest(raw) || out.Complete != seal.Complete || out.ItemCount != seal.ItemCount)) || (out.Status == "held" && (out.SealSHA256 != "" || out.Complete || out.ItemCount != 0)) || (out.Status != "accepted" && out.Status != "held")) {
		return ProfileSealReceipt{}, errors.New("invalid Teams profile seal receipt")
	}
	return out, nil
}
