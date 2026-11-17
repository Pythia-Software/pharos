package teams

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gbdubs/pharos/internal/teams/sourcewire"
)

// ObserveSourceProtection signs exact canonical bytes. It makes no ownership
// or source-inventory claim; callers persist these bytes for replay.
func (c *DeviceClient) ObserveSourceProtection(ctx context.Context, library string, raw []byte) (sourcewire.ProtectionReceipt, error) {
	var out sourcewire.ProtectionReceipt
	v, err := sourcewire.DecodeProtectionObservation(raw)
	if err != nil {
		return out, err
	}
	path, err := LibraryPath(library, "source-protection")
	if err != nil {
		return out, err
	}
	path += "/" + v.ObservationID
	var response json.RawMessage
	if err = c.exchange(ctx, http.MethodPut, path, raw, &response); err != nil {
		return out, err
	}
	if sourceResponse(response, &out, "status", "observation_id", "revision", "observation_sha256", "privacy_revision", "origin_protection", "safety_pending") != nil || out.ObservationID != v.ObservationID || out.ObservationSHA256 != sourcewire.ProtectionDigest(raw) {
		return sourcewire.ProtectionReceipt{}, errors.New("invalid Teams protection receipt")
	}
	switch out.Status {
	case "accepted":
		if out.Revision != v.Revision || out.PrivacyRevision < 1 || out.OriginProtection != v.Protection {
			return sourcewire.ProtectionReceipt{}, errors.New("invalid Teams protection receipt")
		}
	case "held":
		if out.Revision != 0 || out.PrivacyRevision != 0 || out.OriginProtection != "" || !out.SafetyPending {
			return sourcewire.ProtectionReceipt{}, errors.New("invalid Teams protection hold")
		}
	default:
		return sourcewire.ProtectionReceipt{}, errors.New("invalid Teams protection status")
	}

	return out, nil
}
