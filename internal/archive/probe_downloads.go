package archive

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// DownloadExport is a possible export, not an imported source. An empty Kind
// means the conversation file did not identify its provider.
type DownloadExport struct {
	Kind string `json:"kind,omitempty"`
	Path string `json:"path"`
}

type DownloadScan struct {
	Exports    []DownloadExport `json:"exports"`
	Problem    string           `json:"problem,omitempty"`
	Incomplete bool             `json:"incomplete,omitempty"`
}

// downloadConversationKind inspects only the first bounded record. It does not
// import data, and leaves empty or unfamiliar arrays for the person to identify.
func downloadConversationKind(reader io.Reader) string {
	decoder := json.NewDecoder(io.LimitReader(reader, 4<<20))
	if token, err := decoder.Token(); err != nil || token != json.Delim('[') || !decoder.More() {
		return ""
	}
	var record map[string]json.RawMessage
	if decoder.Decode(&record) != nil {
		return ""
	}
	if _, ok := record["mapping"]; ok {
		return "chatgpt"
	}
	if _, ok := record["chat_messages"]; ok {
		return "claude-ai"
	}
	return ""
}

func downloadExportZip(file string) (string, bool) {
	archive, err := zip.OpenReader(file)
	if err != nil {
		return "", false
	}
	defer archive.Close()
	for _, member := range archive.File {
		if !claudeConversationName(path.Base(member.Name)) {
			continue
		}
		reader, err := member.Open()
		if err != nil {
			continue
		}
		kind := downloadConversationKind(reader)
		reader.Close()
		return kind, true
	}
	return "", false
}

func downloadExportFolder(dir string) (string, bool) {
	folder, err := os.Open(dir)
	if err != nil {
		return "", false
	}
	defer folder.Close()
	entries, _ := folder.ReadDir(512)
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(strings.ToLower(name), "manifest") && strings.EqualFold(filepath.Ext(name), ".json") {
			_, _, found, err := readClaudeManifest(filepath.Join(dir, name))
			if found && err == nil {
				return "claude-ai", true
			}
		}
		if !claudeConversationName(name) {
			continue
		}
		file, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		kind := downloadConversationKind(file)
		file.Close()
		return kind, true
	}
	return "", false
}

// A guided scan explicitly requested by the person may read Downloads. Limit
// it to top-level files and one level of unzipped folders; never follow links.
func scanDownloadExports(home string) DownloadScan {
	result := DownloadScan{Exports: []DownloadExport{}}
	dir := filepath.Join(home, "Downloads")
	folder, err := os.Open(dir)
	if errors.Is(err, os.ErrNotExist) {
		return result
	}
	if err != nil {
		result.Problem = "Pharos could not read Downloads: " + err.Error()
		return result
	}
	defer folder.Close()
	entries, err := folder.ReadDir(5001)
	if err != nil && err != io.EOF {
		result.Problem = "Pharos could not list Downloads: " + err.Error()
		return result
	}
	if len(entries) > 5000 {
		entries = entries[:5000]
		result.Incomplete = true
	}
	deadline := time.Now().Add(probeWalkBudget)
	for _, entry := range entries {
		if time.Now().After(deadline) {
			result.Incomplete = true
			break
		}
		file := filepath.Join(dir, entry.Name())
		kind, found := "", false
		switch {
		case entry.IsDir():
			kind, found = downloadExportFolder(file)
		case entry.Type().IsRegular() && strings.EqualFold(filepath.Ext(entry.Name()), ".zip"):
			kind, found = downloadExportZip(file)
		case entry.Type().IsRegular() && strings.HasPrefix(strings.ToLower(entry.Name()), "manifest") && strings.EqualFold(filepath.Ext(entry.Name()), ".json"):
			_, _, found, err = readClaudeManifest(file)
			found = found && err == nil
			kind = "claude-ai"
		case entry.Type().IsRegular() && claudeConversationName(entry.Name()):
			f, err := os.Open(file)
			if err == nil {
				kind, found = downloadConversationKind(f), true
				f.Close()
			}
		}
		if found {
			result.Exports = append(result.Exports, DownloadExport{Kind: kind, Path: tildeWithin(home, file)})
		}
	}
	return result
}
