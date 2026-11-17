package archive

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
)

// Key ID binds cover matching to the same protected host key across credential rotation.
func teamsCheckpointKeyID(key []byte) (string, error) {
	if len(key) != 32 {
		return "", errTeamsProtectionContinuity
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("pharos-teams-guard-key-id-v1"))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// A caller may choose identity scope only after it proves the complete alias,
// custody, import and dependency closure. Otherwise a library hold is the
// conservative cover. This helper neither provisions a key nor releases a
// guard; both require the checkpoint protocol and reconciliation.
func teamsCheckpointGuard(key []byte, kind string, closureProved bool, identity ...string) (teamsCheckpointGuardCover, error) {
	if len(key) != 32 || (kind != "positive" && kind != "unknown" && kind != "reconstructible") {
		return teamsCheckpointGuardCover{}, errTeamsProtectionContinuity
	}
	scope := "library"
	if closureProved && len(identity) > 0 {
		scope = "identity"
		for _, part := range identity {
			if part == "" || len(part) > 1024 {
				scope = "library"
				break
			}
		}
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("pharos-teams-guard-cover-v1\x00" + scope + "\x00"))
	if scope == "identity" {
		for _, part := range identity {
			var size [4]byte
			binary.BigEndian.PutUint32(size[:], uint32(len(part)))
			mac.Write(size[:])
			mac.Write([]byte(part))
		}
	}
	return teamsCheckpointGuardCover{Kind: kind, Scope: scope, Commitment: hex.EncodeToString(mac.Sum(nil))}, nil
}

// An unscoped or mismatched identity cannot bypass a library cover. Opaque
// positives remain held until a separately reviewed exact release exists.
func teamsCheckpointGuardMatches(guard teamsCheckpointGuardCover, key []byte, expectedKeyID string, closureProved bool, identity ...string) bool {
	keyID, err := teamsCheckpointKeyID(key)
	if err != nil || !hmac.Equal([]byte(keyID), []byte(expectedKeyID)) {
		return true
	}
	if guard.Scope == "library" || guard.Scope != "identity" || !closureProved || len(key) != 32 {
		return true
	}
	expected, err := teamsCheckpointGuard(key, guard.Kind, true, identity...)
	// An incomplete later closure is unavailable, never evidence of a mismatch.
	if err != nil || expected.Scope != "identity" {
		return true
	}
	return hmac.Equal([]byte(guard.Commitment), []byte(expected.Commitment))
}
