package archive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// teamsCodexContentFile reads a complete bounded native rollout twice. It
// rejects partial JSON, identity ambiguity and symlink traversal; its digest
// binds every byte, not merely the usage counters used for numeric reporting.
func teamsCodexContentFile(source SourceConfig, origin, native string) ([]MessageRecord, [32]byte, error) {
	var empty [32]byte
	root, e := filepath.EvalSymlinks(source.Path)
	if e != nil {
		return nil, empty, errTeamsSourceUnavailable
	}
	rel, e := filepath.Rel(source.Path, origin)
	if e != nil || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, empty, errTeamsSourceUnavailable
	}
	path := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" || part == "." || part == ".." {
			return nil, empty, errTeamsSourceUnavailable
		}
		path = filepath.Join(path, part)
		st, err := os.Lstat(path)
		if err != nil || st.Mode()&os.ModeSymlink != 0 {
			return nil, empty, errTeamsSourceUnavailable
		}
	}
	if !strings.HasSuffix(strings.ToLower(path), ".jsonl") {
		return nil, empty, errTeamsSourceUnavailable
	}
	read := func() ([]byte, error) {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil || !st.Mode().IsRegular() || st.Size() > 4<<20 {
			return nil, errors.New("unavailable")
		}
		b, err := io.ReadAll(io.LimitReader(f, (4<<20)+1))
		if err != nil || len(b) > 4<<20 {
			return nil, errors.New("unavailable")
		}
		return b, nil
	}
	first, e := read()
	if e != nil || len(first) == 0 || first[len(first)-1] != '\n' {
		return nil, empty, errTeamsSourceUnavailable
	}
	second, e := read()
	if e != nil || !bytes.Equal(first, second) {
		return nil, empty, errTeamsSourceUnavailable
	}
	return teamsCodexContentBytes(source, path, native, first)
}

// teamsCodexContentBytes applies the same content gates to a verified,
// decoded retained capture. The caller must establish custody separately.
func teamsCodexContentBytes(source SourceConfig, path, native string, first []byte) ([]MessageRecord, [32]byte, error) {
	var empty [32]byte
	messages, err := teamsCodexParseContentBytes(source, path, native, first)
	if err != nil {
		return nil, empty, err
	}
	h := sha256.New()
	h.Write([]byte("pharos-codex-transcript-v1\x00"))
	h.Write(first)
	var digest [32]byte
	copy(digest[:], h.Sum(nil))
	return messages, digest, nil
}

func teamsCodexParseContentBytes(source SourceConfig, path, native string, first []byte) ([]MessageRecord, error) {
	if len(first) == 0 || len(first) > 4<<20 || first[len(first)-1] != '\n' {
		return nil, errTeamsSourceUnavailable
	}
	var quality teamsJSONLQuality
	events, e := readJSONLinesObserved(bytes.NewReader(first), int64(len(first)), quality.observe)
	if e != nil || quality.gap || quality.trailingPartial {
		return nil, errTeamsSourceUnavailable
	}
	proof := teamsCodexNativeProof(events, native, quality)
	if proof.Status != "header_independent_unvalidated" {
		return nil, errTeamsSourceUnavailable
	}
	a := jsonlAdapter{baseAdapter: baseAdapter{config: source}, provider: "codex"}
	record, ok, e := a.codex(path, events)
	if e != nil || !ok || len(record.Conversations) != 1 || record.Conversations[0].NativeID != native {
		return nil, errTeamsSourceUnavailable
	}
	return record.Conversations[0].Messages, nil
}

// Content exposed to a Teams tool must exactly match the source-derived
// messages in this transaction. Extra, missing or changed indexed messages
// make the conversation unavailable rather than choosing a copy.
func teamsCodexIndexedContentMatches(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, id string, source []MessageRecord) (bool, error) {
	expected := make(map[string]MessageRecord, len(source))
	for index, m := range source {
		if m.NativeID == "" {
			return false, nil
		}
		if _, ok := expected[m.NativeID]; ok {
			return false, nil
		}
		m.Role = defaultString(m.Role, "unknown")
		m.Kind = defaultString(m.Kind, "message")
		m.SourceOrder = index
		expected[m.NativeID] = m
	}
	rows, e := q.QueryContext(ctx, `SELECT native_id,role,kind,text,COALESCE(raw_text,''),COALESCE(created_at,''),COALESCE(source_order,0),selected FROM messages WHERE conversation_id=?`, id)
	if e != nil {
		return false, e
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var native, role, kind, text, raw, at string
		var order int
		var selected bool
		if e = rows.Scan(&native, &role, &kind, &text, &raw, &at, &order, &selected); e != nil {
			return false, e
		}
		m, ok := expected[native]
		if !ok || m.Role != role || m.Kind != kind || m.Text != text || m.RawText != raw || m.CreatedAt != at || m.SourceOrder != order || m.Selected != selected {
			return false, nil
		}
		count++
	}
	return count == len(expected), rows.Err()
}
