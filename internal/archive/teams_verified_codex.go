package archive

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// teamsVerifyCodexFile recomputes the narrow numeric observation from a
// bounded native file at evaluation time. It never returns transcript text.
// Callers must still prove source custody, account assignment, overlap,
// privacy and sealed ownership within their own snapshot and sharing gates.
func teamsVerifyCodexFile(root, origin, nativeID, expectedDigest string) (int64, error) {
	const maxSourceBytes = 32 << 20
	if root == "" || origin == "" || nativeID == "" || expectedDigest == "" || strings.HasSuffix(strings.ToLower(origin), ".gz") {
		return 0, errTeamsSourceUnavailable
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return 0, errTeamsSourceUnavailable
	}
	resolved, err := filepath.EvalSymlinks(origin)
	if err != nil {
		return 0, errTeamsSourceUnavailable
	}
	rel, err := filepath.Rel(resolvedRoot, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return 0, errTeamsSourceUnavailable
	}
	read := func() ([]byte, error) {
		f, e := os.Open(resolved)
		if e != nil {
			return nil, e
		}
		defer f.Close()
		st, e := f.Stat()
		if e != nil || !st.Mode().IsRegular() || st.Size() > maxSourceBytes {
			return nil, errors.New("source unavailable")
		}
		value, e := io.ReadAll(io.LimitReader(f, maxSourceBytes+1))
		if e != nil || len(value) > maxSourceBytes {
			return nil, errors.New("source unavailable")
		}
		return value, nil
	}
	first, err := read()
	if err != nil {
		return 0, errTeamsSourceUnavailable
	}
	second, err := read()
	if err != nil || sha256.Sum256(first) != sha256.Sum256(second) {
		return 0, errTeamsSourceUnavailable
	}
	var quality teamsJSONLQuality
	events, err := readJSONLinesObserved(bytes.NewReader(first), int64(len(first)), quality.observe)
	if err != nil {
		return 0, errTeamsSourceUnavailable
	}
	proof := teamsCodexNativeProof(events, nativeID, quality)
	total, digest, ok := teamsCodexTotalPrefix(proof, events)
	if !ok || digest != expectedDigest {
		return 0, errTeamsSourceUnavailable
	}
	return total, nil
}
