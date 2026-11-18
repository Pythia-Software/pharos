package teams

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gbdubs/pharos/internal/teams/sourcewire"
)

var ErrInvalidRecoveryPage = errors.New("invalid Teams recovery page receipt")
var ErrInvalidRecoveryStart = errors.New("invalid Teams recovery start receipt")

// Recovery responses are observations for reconciliation only; neither a
// completed page nor a historical receipt grants permission to run a query.
func decodeRecoveryResponse(raw []byte, out any, required ...string) error {
	if !validUniqueJSON(raw) {
		return errSourceBinding
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return errSourceBinding
	}
	for _, k := range required {
		if v, ok := fields[k]; !ok || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return errSourceBinding
		}
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil {
		return errSourceBinding
	}
	return nil
}
func (c *DeviceClient) StartSourceRecovery(ctx context.Context, library string, raw []byte) (sourcewire.RecoveryStartReceipt, error) {
	var out sourcewire.RecoveryStartReceipt
	if _, err := sourcewire.DecodeRecoveryStart(raw); err != nil {
		return out, err
	}
	path, err := LibraryPath(library, "source-recovery")
	if err != nil {
		return out, err
	}
	var response json.RawMessage
	if err = c.exchange(ctx, http.MethodPost, path+"/start", raw, &response); err != nil {
		if errors.Is(err, ErrInvalidDeviceResponse) || errors.Is(err, ErrDeviceResponseTooLarge) {
			return out, ErrInvalidRecoveryStart
		}
		return out, err
	}
	if decodeRecoveryResponse(response, &out, "status", "generation", "server_time", "unaccepted_floor", "held", "external_proof_required") != nil || out.Generation < 1 || out.ServerTime <= 0 || out.UnacceptedFloor < 0 || out.EarliestRetry < 0 || !out.ExternalProofRequired || (out.Status != "proof_required" && out.Status != "held" && out.Status != "restart_required" && out.Status != "capacity_blocked" && out.Status != "restart_exhausted" && out.Status != "busy" && out.Status != "cleanup_pending") || (out.RunID != "" && (!validDeviceID(out.RunID) || out.ExpiresAt <= out.ServerTime)) || (out.Status == "held" && !out.Held) {
		return sourcewire.RecoveryStartReceipt{}, ErrInvalidRecoveryStart
	}
	return out, nil
}
func (c *DeviceClient) PageSourceRecovery(ctx context.Context, library string, raw []byte) (sourcewire.RecoveryPageReceipt, error) {
	var out sourcewire.RecoveryPageReceipt
	req, err := sourcewire.DecodeRecoveryPage(raw)
	if err != nil {
		return out, err
	}
	path, err := LibraryPath(library, "source-recovery")
	if err != nil {
		return out, err
	}
	var response json.RawMessage
	if err = c.exchange(ctx, http.MethodPost, path+"/"+req.RunID+"/pages", raw, &response); err != nil {
		if errors.Is(err, ErrInvalidDeviceResponse) || errors.Is(err, ErrDeviceResponseTooLarge) {
			return out, ErrInvalidRecoveryPage
		}
		return out, err
	}
	if decodeRecoveryResponse(response, &out, "status", "run_id", "generation", "server_time", "unaccepted_floor", "held", "external_proof_required", "family", "items") != nil || out.RunID != req.RunID || out.Family != req.Family || out.Generation < 1 || out.ServerTime <= 0 || out.UnacceptedFloor < 0 || !out.ExternalProofRequired || out.Items == nil || len(out.Items) > sourcewire.MaxItemsPerPage || len(out.NextCursor) > 4096 || (out.Status != "proof_required" && out.Status != "held" && out.Status != "capacity_blocked") || (out.Status == "held" && !out.Held) {
		return sourcewire.RecoveryPageReceipt{}, ErrInvalidRecoveryPage
	}
	return out, nil
}

// StageSourceRecovery submits the caller's already-journaled exact bytes. An
// accepted staging receipt is not a release or execution authorization.
func (c *DeviceClient) StageSourceRecovery(ctx context.Context, library string, raw []byte) (sourcewire.RecoveryStageReceipt, error) {
	var out sourcewire.RecoveryStageReceipt
	page, err := sourcewire.DecodeRecoveryStagePage(raw)
	if err != nil {
		return out, err
	}
	path, err := LibraryPath(library, "source-recovery")
	if err != nil {
		return out, err
	}
	var response json.RawMessage
	if err = c.exchange(ctx, http.MethodPost, path+"/"+page.RunID+"/stage", raw, &response); err != nil {
		return out, err
	}
	if decodeRecoveryResponse(response, &out, "status", "command_id", "run_id", "page_sha256") != nil || out.Status != "accepted" || out.CommandID != page.CommandID || out.RunID != page.RunID || out.PageSHA256 != sourcewire.RecoveryStagePageDigest(raw) {
		return sourcewire.RecoveryStageReceipt{}, errors.New("invalid Teams recovery stage receipt")
	}
	return out, nil
}
