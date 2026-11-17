package archive

// The checkpoint codec is deliberately independent of activation and proof
// admission. A valid record is only a commitment, never non-private evidence.
import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"

	"github.com/gbdubs/pharos/internal/teams"
)

var errTeamsProtectionContinuity = errors.New("protection continuity is unproved")

// Tuple instance is domain-hashed, so the host record contains no library or
// source identifiers. Sequence is separately monotone across host CAS writes.
type teamsCheckpointTuple struct {
	Generation int64  `json:"generation"`
	Instance   string `json:"instance"`
	Revision   int64  `json:"revision"`
	CommitID   string `json:"commit_id"`
	Digest     string `json:"digest"`
}

type teamsCheckpointGuardCover struct {
	Kind       string `json:"kind"`
	Scope      string `json:"scope"`
	Commitment string `json:"commitment"`
}

type teamsCheckpointRecord struct {
	Version             int                         `json:"version"`
	Scope               string                      `json:"scope"`
	KeyID               string                      `json:"key_id"`
	Sequence            int64                       `json:"sequence"`
	Phase               string                      `json:"phase"`
	Current             *teamsCheckpointTuple       `json:"current"`
	Previous            *teamsCheckpointTuple       `json:"previous"`
	PreviousUnactivated string                      `json:"previous_unactivated"`
	Next                *teamsCheckpointTuple       `json:"next"`
	Operation           string                      `json:"operation"`
	Transition          string                      `json:"transition"`
	Cover               []teamsCheckpointGuardCover `json:"cover"`
}

func teamsCheckpointScope(applicationID, libraryID string) string {
	sum := sha256.Sum256([]byte("pharos-teams-protection-checkpoint-v1\x00" + applicationID + "\x00" + libraryID))
	return hex.EncodeToString(sum[:])
}
func teamsCheckpointInstance(libraryID, instanceID string) string {
	sum := sha256.Sum256([]byte("pharos-teams-checkpoint-instance-v1\x00" + libraryID + "\x00" + instanceID))
	return hex.EncodeToString(sum[:])
}
func teamsCheckpointHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	b, err := hex.DecodeString(s)
	return err == nil && hex.EncodeToString(b) == s
}
func teamsCheckpointTupleValid(t *teamsCheckpointTuple) bool {
	return t != nil && t.Generation > 0 && t.Generation < math.MaxInt64 && t.Revision >= 0 && t.Revision < math.MaxInt64 && teamsCheckpointHex(t.Instance, 64) && teamsCheckpointHex(t.CommitID, 32) && teamsCheckpointHex(t.Digest, 64)
}
func (r teamsCheckpointRecord) valid() bool {
	if r.Version != 1 || !teamsCheckpointHex(r.Scope, 64) || !teamsCheckpointHex(r.KeyID, 64) || r.Sequence < 1 || r.Sequence >= math.MaxInt64 || r.Cover == nil || len(r.Cover) > 32 {
		return false
	}
	for _, c := range r.Cover {
		if (c.Kind != "positive" && c.Kind != "unknown" && c.Kind != "reconstructible") || (c.Scope != "identity" && c.Scope != "library") || !teamsCheckpointHex(c.Commitment, 64) {
			return false
		}
	}
	switch r.Phase {
	case "unactivated":
		return r.Current == nil && r.Previous == nil && r.Next == nil && teamsCheckpointHex(r.PreviousUnactivated, 64) && r.Operation == "" && r.Transition == "" && len(r.Cover) == 0
	case "held":
		return teamsCheckpointTupleValid(r.Current) && r.Current.Generation < r.Sequence && r.Previous == nil && r.PreviousUnactivated == "" && r.Next == nil && r.Operation == "" && r.Transition == "continuity_hold" && len(r.Cover) == 1 && r.Cover[0].Kind == "unknown" && r.Cover[0].Scope == "library"
	case "current":
		return teamsCheckpointTupleValid(r.Current) && r.Current.Generation <= r.Sequence && r.Previous == nil && r.PreviousUnactivated == "" && r.Next == nil && r.Operation == "" && r.Transition == "" && len(r.Cover) == 0
	case "pending":
		if r.Current != nil || !teamsCheckpointTupleValid(r.Next) || r.Next.Generation != r.Sequence || !teamsCheckpointHex(r.Operation, 32) {
			return false
		}
		if (r.Transition == "tightening" && len(r.Cover) == 0) || (r.Transition == "no_tightening" && len(r.Cover) != 0) || (r.Transition != "tightening" && r.Transition != "no_tightening") {
			return false
		}
		if r.Previous == nil {
			return teamsCheckpointHex(r.PreviousUnactivated, 64)
		}
		return r.PreviousUnactivated == "" && teamsCheckpointTupleValid(r.Previous) && r.Previous.Generation < r.Sequence && r.Next.Generation > r.Previous.Generation && r.Next.CommitID != r.Previous.CommitID
	default:
		return false
	}
}

func teamsParseCheckpoint(raw []byte) (teamsCheckpointRecord, error) {
	var r teamsCheckpointRecord
	if len(raw) == 0 || len(raw) > 8192 || !teams.ValidUniqueJSON(raw) {
		return r, errTeamsProtectionContinuity
	}
	fields, err := teamsStrictObject(raw, map[string]bool{"version": true, "scope": true, "key_id": true, "sequence": true, "phase": true, "current": true, "previous": true, "previous_unactivated": true, "next": true, "operation": true, "transition": true, "cover": true})
	if err != nil || len(fields) != 12 {
		return r, errTeamsProtectionContinuity
	}
	tupleFields := map[string]bool{"generation": true, "instance": true, "revision": true, "commit_id": true, "digest": true}
	for _, key := range []string{"current", "previous", "next"} {
		if !bytes.Equal(fields[key], []byte("null")) {
			object, e := teamsStrictObject(fields[key], tupleFields)
			if e != nil || len(object) != 5 {
				return r, errTeamsProtectionContinuity
			}
		}
	}
	var covers []json.RawMessage
	if err = json.Unmarshal(fields["cover"], &covers); err != nil || covers == nil || len(covers) > 32 {
		return r, errTeamsProtectionContinuity
	}
	for _, rawCover := range covers {
		object, e := teamsStrictObject(rawCover, map[string]bool{"kind": true, "scope": true, "commitment": true})
		if e != nil || len(object) != 3 {
			return r, errTeamsProtectionContinuity
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&r); err != nil || !r.valid() {
		return teamsCheckpointRecord{}, errTeamsProtectionContinuity
	}
	return r, nil
}
func teamsEncodeCheckpoint(r teamsCheckpointRecord) ([]byte, error) {
	if !r.valid() {
		return nil, errTeamsProtectionContinuity
	}
	return json.Marshal(r)
}

type teamsCheckpointDisposition uint8

const (
	teamsCheckpointUnproved teamsCheckpointDisposition = iota
	teamsCheckpointCurrent
	teamsCheckpointDefer
	teamsCheckpointFinalize
	teamsCheckpointRecover
	teamsCheckpointUnactivated
)

// Classify a pinned catalog whose actual projection was independently checked.
// Only the holder of the real SQLite writer may reconcile a pending record.
// Classification itself never mutates the host or catalog.
func teamsClassifyCheckpoint(r teamsCheckpointRecord, scope string, catalog *teamsCheckpointTuple, unactivatedDigest string, projectionMatches, writerHeld bool) teamsCheckpointDisposition {
	if !r.valid() || r.Scope != scope || !projectionMatches {
		return teamsCheckpointUnproved
	}
	same := func(a, b *teamsCheckpointTuple) bool { return a != nil && b != nil && *a == *b }
	switch r.Phase {
	case "unactivated":
		if catalog == nil && unactivatedDigest == r.PreviousUnactivated {
			return teamsCheckpointUnactivated
		}
	case "current":
		if same(r.Current, catalog) {
			return teamsCheckpointCurrent
		}
	case "pending":
		previous := same(r.Previous, catalog) || (r.Previous == nil && catalog == nil && unactivatedDigest == r.PreviousUnactivated)
		if previous || same(r.Next, catalog) {
			if !writerHeld {
				return teamsCheckpointDefer
			}
			if same(r.Next, catalog) {
				return teamsCheckpointFinalize
			}
			return teamsCheckpointRecover
		}
	}
	return teamsCheckpointUnproved
}
