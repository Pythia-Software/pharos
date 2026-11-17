package teams

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"strconv"
	"unicode/utf8"
)

var errInvalidSourceIdentity = errors.New("invalid source identity")

func sourceIdentityMAC(key []byte, fields ...[]byte) (string, error) {
	if len(key) != 32 {
		return "", errInvalidSourceIdentity
	}
	mac := hmac.New(sha256.New, key)
	for _, field := range fields {
		if len(field) > 1<<20 {
			return "", errInvalidSourceIdentity
		}
		var size [4]byte
		binary.BigEndian.PutUint32(size[:], uint32(len(field)))
		mac.Write(size[:])
		mac.Write(field)
	}
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

// SourceWorkNamespace is an attested group name, not a provider credential or
// a claim that Teams verified which account produced the source.
func SourceWorkNamespace(key []byte, provider string) (string, error) {
	if provider != "codex" && provider != "claude" {
		return "", errInvalidSourceIdentity
	}
	return sourceIdentityMAC(key, []byte("pharos-account-namespace-v1"), []byte(provider), []byte("reserved"), []byte("work"))
}

// SourceWitness is deliberately independent of account assignment, so
// conflicting user attestations of the same native unit can be withheld.
func SourceWitness(key []byte, provider, unit, nativeID string) (string, error) {
	if !((provider == "codex" && (unit == "codex_thread" || unit == "codex_transcript" || unit == "content_component")) || (provider == "claude" && unit == "content_component")) || nativeID == "" || len(nativeID) > 512 || !utf8.ValidString(nativeID) {
		return "", errInvalidSourceIdentity
	}
	return sourceIdentityMAC(key, []byte("pharos-source-witness-v1"), []byte(provider), []byte(unit), []byte(nativeID))
}

// SourceEvidenceRevision binds only the specific approved intrinsic profile.
// It does not certify content equivalence, account identity, or authorization.
func SourceEvidenceRevision(key []byte, sourceToken, profile string, version int, intrinsicDigest []byte) (string, error) {
	if decoded, err := base64.RawURLEncoding.Strict().DecodeString(sourceToken); err != nil || len(decoded) != sha256.Size || base64.RawURLEncoding.EncodeToString(decoded) != sourceToken {
		return "", errInvalidSourceIdentity
	}
	if !((profile == "codex_thread_usage" || profile == "codex_request_usage" || profile == "claude_reply_usage" || profile == "codex_transcript") && version == 1) || len(intrinsicDigest) != sha256.Size {
		return "", errInvalidSourceIdentity
	}
	return sourceIdentityMAC(key, []byte("pharos-source-evidence-v1"), []byte(sourceToken), []byte(profile), []byte(strconv.Itoa(version)), intrinsicDigest)
}

// SourceProfileIdentity derives a non-owning, profile-specific child identity
// from the exact existing thread token and witness. The keyed domains prevent
// a child from being substituted for its ownership/protection anchor.
func SourceProfileIdentity(key []byte, anchorToken, anchorWitness, profile string, version int) (string, string, error) {
	valid := func(value string) bool {
		b, e := base64.RawURLEncoding.Strict().DecodeString(value)
		return e == nil && len(b) == sha256.Size && base64.RawURLEncoding.EncodeToString(b) == value
	}
	if profile != "codex_request_usage" || version != 1 || !valid(anchorToken) || !valid(anchorWitness) {
		return "", "", errInvalidSourceIdentity
	}
	token, err := sourceIdentityMAC(key, []byte("pharos-source-profile-token-v1"), []byte(profile), []byte(strconv.Itoa(version)), []byte(anchorToken))
	if err != nil {
		return "", "", err
	}
	witness, err := sourceIdentityMAC(key, []byte("pharos-source-profile-witness-v1"), []byte(profile), []byte(strconv.Itoa(version)), []byte(anchorWitness))
	if err != nil {
		return "", "", err
	}
	if token == anchorToken || witness == anchorWitness {
		return "", "", errInvalidSourceIdentity
	}
	return token, witness, nil
}
