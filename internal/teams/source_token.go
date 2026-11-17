package teams

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"math"
	"unicode/utf8"
)

// SourceToken produces an org/person-scoped opaque identity. The caller must
// establish that all three provider-specific identity parts are trustworthy;
// missing or ambiguous identity cannot certify ownership or content safety.
func SourceToken(key []byte, provider, account, native string) (string, error) {
	if len(key) != 32 {
		return "", errors.New("source token key must be 32 bytes")
	}
	fields := []string{"pharos-source-v1", provider, account, native}
	mac := hmac.New(sha256.New, key)
	for _, field := range fields {
		if field == "" || !utf8.ValidString(field) || uint64(len(field)) > math.MaxUint32 {
			return "", errors.New("source identity must contain valid nonempty UTF-8 values")
		}
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(field)))
		mac.Write(n[:])
		mac.Write([]byte(field))
	}
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}
