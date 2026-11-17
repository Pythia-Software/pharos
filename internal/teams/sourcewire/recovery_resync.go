package sourcewire

import "encoding/json"

// RecoveryStart and RecoveryPageRequest are signed library-scoped commands.
// A resync never grants work, disclosure, or publication authority.
type RecoveryStart struct {
	Version  int    `json:"version"`
	Contract string `json:"contract"`
	IssuedAt int64  `json:"issued_at"`
	StartID  string `json:"start_id"`
	Purpose  string `json:"purpose"`
	Restart  bool   `json:"restart"`
}

type RecoveryPageRequest struct {
	Version  int    `json:"version"`
	Contract string `json:"contract"`
	IssuedAt int64  `json:"issued_at"`
	RunID    string `json:"run_id"`
	Family   string `json:"family"`
	Cursor   string `json:"cursor"`
}

func DecodeRecoveryStart(raw []byte) (RecoveryStart, error) {
	var v RecoveryStart
	if len(raw) > 4096 || canonical(raw, &v) != nil || v.Version != 1 || v.Contract != SourceRecoveryContract || v.IssuedAt <= 0 || v.IssuedAt > 253402300799 || !uuidPattern.MatchString(v.StartID) || (v.Purpose != "protection" && v.Purpose != "profiles") {
		return v, ErrInvalid
	}
	return v, nil
}
func DecodeRecoveryPage(raw []byte) (RecoveryPageRequest, error) {
	var v RecoveryPageRequest
	if len(raw) > 8192 || canonical(raw, &v) != nil || v.Version != 1 || v.Contract != SourceRecoveryContract || v.IssuedAt <= 0 || v.IssuedAt > 253402300799 || !uuidPattern.MatchString(v.RunID) || len(v.Cursor) > 4096 {
		return v, ErrInvalid
	}
	switch v.Family {
	case "origins", "inventory", "guards", "witnesses", "dependencies", "profile_evidence", "profile_seals":
	default:
		return v, ErrInvalid
	}
	return v, nil
}

type RecoveryStartReceipt struct {
	Status                string `json:"status"`
	RunID                 string `json:"run_id,omitempty"`
	Generation            int64  `json:"generation"`
	ExpiresAt             int64  `json:"expires_at,omitempty"`
	ServerTime            int64  `json:"server_time"`
	UnacceptedFloor       int64  `json:"unaccepted_floor"`
	EarliestRetry         int64  `json:"earliest_retry,omitempty"`
	Held                  bool   `json:"held"`
	ExternalProofRequired bool   `json:"external_proof_required"`
}
type RecoveryPageReceipt struct {
	Status                string            `json:"status"`
	RunID                 string            `json:"run_id"`
	Generation            int64             `json:"generation"`
	ServerTime            int64             `json:"server_time"`
	UnacceptedFloor       int64             `json:"unaccepted_floor"`
	Held                  bool              `json:"held"`
	ExternalProofRequired bool              `json:"external_proof_required"`
	Family                string            `json:"family"`
	Items                 []json.RawMessage `json:"items"`
	NextCursor            string            `json:"next_cursor,omitempty"`
}
