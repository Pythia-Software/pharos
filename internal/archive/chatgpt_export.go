package archive

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const chatGPTInventoryName = "chatgpt-export.json"
const chatGPTInventoryFormat = "pharos-chatgpt-export-v1"

// These account preferences and contact records are not conversation evidence.
// Keep the same exclusion for an explicitly selected ZIP and a folder capture.
func chatGPTExcluded(name string) bool {
	switch path.Base(name) {
	case "user.json", "user_settings.json", "ads.json":
		return true
	}
	return false
}

type chatGPTInventory struct {
	Format   string              `json:"format"`
	Files    []string            `json:"files"`
	SHA256   map[string]string   `json:"sha256"`
	Excluded map[string][]string `json:"excluded,omitempty"`
}

type chatGPTBundle struct {
	assets  map[string]map[string]any
	library []map[string]any
	shares  []map[string]any
}

func chatGPTReadJSON(read func(string) ([]byte, error), name string, target any) error {
	content, err := read(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(content, target); err != nil {
		return fmt.Errorf("ChatGPT %s: %w", name, err)
	}
	return nil
}

func chatGPTBundleFrom(read func(string) ([]byte, error), names map[string]int64, origin string, zipped bool) (*chatGPTBundle, error) {
	b := &chatGPTBundle{assets: map[string]map[string]any{}}
	filenames := map[string]string{}
	for _, item := range []struct {
		name  string
		value any
	}{{"conversation_asset_file_names.json", &filenames}, {"library_files.json", &b.library}, {"shared_conversations.json", &b.shares}} {
		if err := chatGPTReadJSON(read, item.name, item.value); err != nil {
			return nil, err
		}
	}
	for name, size := range names {
		if !claudeExportPath(name) || chatGPTExcluded(name) || path.Ext(name) != ".dat" {
			continue
		}
		asset := map[string]any{"type": "chatgpt_asset", "name": defaultString(filenames[name], name), "size_bytes": size, "available": true, "source_file": origin, "member": name}
		if !zipped {
			asset["source_file"], asset["member"] = filepath.Join(origin, filepath.FromSlash(name)), ""
		}
		b.assets[strings.TrimSuffix(name, ".dat")] = asset
	}
	for _, f := range b.library {
		id := firstString(f["file_id"])
		asset := b.assets[id]
		// Inline Library IDs contain '~'; the exporter escapes it and appends
		// a digest. Only an unambiguous member may satisfy this identity.
		if asset == nil && strings.HasPrefix(id, "file-inline-") {
			prefix := strings.ReplaceAll(id, "~", "_") + "-"
			for key, candidate := range b.assets {
				if strings.HasPrefix(key, prefix) {
					if asset != nil {
						asset = nil
						break
					}
					asset = candidate
				}
			}
		}
		if asset == nil {
			asset = map[string]any{"type": "chatgpt_asset", "available": false}
		}
		asset = cloneAnyMap(asset)
		asset["name"], asset["mime_type"], asset["library_file_id"] = defaultString(f["file_name"], defaultString(asset["name"], id)), firstString(f["mime_type"]), id
		asset["library_metadata"] = f
		b.assets[id] = asset
	}
	return b, nil
}

func cloneAnyMap(value map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range value {
		out[k] = v
	}
	return out
}

func (b *chatGPTBundle) asset(pointer string) map[string]any {
	if b == nil {
		return nil
	}
	_, id, ok := strings.Cut(pointer, "://")
	if !ok {
		id = pointer
	}
	return b.assets[id]
}

func (b *chatGPTBundle) resolve(value map[string]any) map[string]any {
	if b == nil {
		return value
	}
	var find func(any) string
	find = func(v any) string {
		if m := mapValue(v); m != nil {
			for _, key := range []string{"asset_pointer", "library_file_id", "id"} {
				if p := firstString(m[key]); b.asset(p) != nil {
					return p
				}
			}
			for _, key := range []string{"audio_asset_pointer", "video_container_asset_pointer"} {
				if p := find(m[key]); p != "" {
					return p
				}
			}
		}
		return ""
	}
	if asset := b.asset(find(value)); asset != nil {
		out := cloneAnyMap(asset)
		out["source_attachment"] = value
		if firstString(out["mime_type"]) == "" {
			switch kind := firstString(value["content_type"]); {
			case kind == "image_asset_pointer":
				out["media_kind"] = "image"
			case strings.Contains(kind, "audio"):
				out["media_kind"] = "audio"
			}
		}
		return out
	}
	out := cloneAnyMap(value)
	out["type"], out["available"] = "chatgpt_asset", false
	out["name"] = defaultString(value["name"], defaultString(value["content_type"], "Export attachment"))
	return out
}

func (a *chatGPTAdapter) folderBundle(file string) (*chatGPTBundle, error) {
	dir := filepath.Dir(file)
	entries, err := a.files().ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := map[string]int64{}
	for _, entry := range entries {
		if !entry.IsDir() {
			if info, err := a.files().Info(filepath.Join(dir, entry.Name()), entry); err == nil {
				names[entry.Name()] = info.Size()
			}
		}
	}
	read := func(name string) ([]byte, error) { return chatGPTSidecar(a.files(), filepath.Join(dir, name)) }
	return chatGPTBundleFrom(read, names, filepath.Dir(a.original(file)), false)
}

func chatGPTZIPBundle(z *zip.Reader, origin string) (*chatGPTBundle, error) {
	names := map[string]int64{}
	members := map[string]*zip.File{}
	for _, f := range z.File {
		names[f.Name] = int64(f.UncompressedSize64)
		members[f.Name] = f
	}
	read := func(name string) ([]byte, error) {
		f := members[name]
		if f == nil {
			return nil, os.ErrNotExist
		}
		if f.UncompressedSize64 > uint64(claudeExportLimits.Manifest) {
			return nil, errors.New("ChatGPT sidecar exceeds size limit")
		}
		in, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer in.Close()
		return io.ReadAll(io.LimitReader(in, claudeExportLimits.Manifest+1))
	}
	return chatGPTBundleFrom(read, names, origin, true)
}

// Validate archive paths, sizes and every CRC before publishing any records.
// Manifests are evidence, never instructions to fetch a URL or open another path.
func verifyChatGPTZIP(ctx context.Context, z *zip.Reader) error { return checkChatGPTZIP(ctx, z, true) }

func checkChatGPTZIP(ctx context.Context, z *zip.Reader, readAll bool) error {
	if len(z.File) > claudeExportLimits.Entries {
		return errors.New("ChatGPT ZIP has too many members")
	}
	names := map[string]int64{}
	seenNames := map[string]bool{}
	var total uint64
	for _, f := range z.File {
		if !claudeExportPath(strings.TrimSuffix(f.Name, "/")) || seenNames[f.Name] || f.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("unsafe or duplicate ChatGPT ZIP member %q", f.Name)
		}
		seenNames[f.Name] = true
		if f.FileInfo().IsDir() {
			continue
		}
		total += f.UncompressedSize64
		if f.UncompressedSize64 > uint64(claudeExportLimits.Member) || total > uint64(claudeExportLimits.Expanded) {
			return errors.New("ChatGPT ZIP exceeds size limits")
		}
		n := int64(f.UncompressedSize64)
		if readAll {
			in, err := f.Open()
			if err != nil {
				return err
			}
			n, err = io.Copy(io.Discard, claudeContextReader{ctx, io.LimitReader(in, claudeExportLimits.Member+1)})
			closeErr := in.Close()
			if err != nil || closeErr != nil || n != int64(f.UncompressedSize64) {
				return fmt.Errorf("ChatGPT member %s failed verification: %w", f.Name, errors.Join(err, closeErr))
			}
		}
		names[f.Name] = n
	}
	if len(chatGPTConversationFiles(slices.Collect(maps.Keys(names)))) == 0 {
		return errors.New("ChatGPT ZIP contains no conversations")
	}
	for _, f := range z.File {
		if f.Name != "export_manifest.json" {
			continue
		}
		if f.UncompressedSize64 > uint64(claudeExportLimits.Manifest) {
			return errors.New("ChatGPT manifest exceeds size limit")
		}
		in, err := f.Open()
		if err != nil {
			return err
		}
		var manifest struct {
			ExportFiles []struct {
				Path string `json:"path"`
				Size int64  `json:"size_bytes"`
			} `json:"export_files"`
		}
		err = json.NewDecoder(in).Decode(&manifest)
		in.Close()
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, item := range manifest.ExportFiles {
			if !claudeExportPath(item.Path) || seen[item.Path] {
				return errors.New("invalid ChatGPT export inventory")
			}
			seen[item.Path] = true
			// Original collections must satisfy their entire producer inventory.
			// A filtered snapshot may intentionally omit account files, as its
			// retained receipt records; discovery verifies its snapshot hash.
			if chatGPTExcluded(item.Path) {
				if _, exists := names[item.Path]; !exists && !readAll {
					continue
				}
			}
			if n, ok := names[item.Path]; !ok || n != item.Size {
				return fmt.Errorf("ChatGPT inventory member %s is missing or has a different size", item.Path)
			}
		}
	}
	return nil
}

// The URL names a stored attachment, never a user-supplied filesystem path.
// Captured evidence is preferred so playback works after the download is gone.
func (s *Server) chatGPTAsset(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/chatgpt-assets/")
	rows, err := queryMaps(s.Catalog.reader(), `SELECT m.text,c.workspace_id FROM messages m JOIN conversations c ON c.id=m.conversation_id WHERE m.id=? AND m.kind='attachment'`, id)
	if err != nil || len(rows) != 1 {
		http.NotFound(w, r)
		return
	}
	var asset map[string]any
	if json.Unmarshal([]byte(firstString(rows[0]["text"])), &asset) != nil || asset["type"] != "chatgpt_asset" || asset["available"] != true {
		http.NotFound(w, r)
		return
	}
	original, member := firstString(asset["source_file"]), firstString(asset["member"])
	if original == "" || member != "" && !claudeExportPath(member) {
		http.NotFound(w, r)
		return
	}
	read := func(open func(string) (*sourceFile, error), file string) ([]byte, error) {
		in, err := open(file)
		if err != nil {
			return nil, err
		}
		defer in.Close()
		if member == "" {
			return io.ReadAll(claudeContextReader{r.Context(), io.LimitReader(in, (64<<20)+1)})
		}
		info, err := in.Stat()
		if err != nil {
			return nil, err
		}
		z, err := exportZip(in, info.Size())
		if err != nil {
			return nil, err
		}
		for _, f := range z.File {
			if f.Name == member {
				if f.UncompressedSize64 > 64<<20 {
					return nil, errors.New("asset too large for preview")
				}
				v, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer v.Close()
				return io.ReadAll(claudeContextReader{r.Context(), io.LimitReader(v, (64<<20)+1)})
			}
		}
		return nil, os.ErrNotExist
	}
	var data []byte
	sightings, _ := queryMaps(s.Catalog.reader(), `SELECT host_id,source_name FROM workspace_sightings WHERE workspace_id=?`, rows[0]["workspace_id"])
	for _, row := range sightings {
		host, name := firstString(row["host_id"]), firstString(row["source_name"])
		if !validCaptureName(host) || !validCaptureName(name) {
			continue
		}
		file, view, e := capturedFileFor(s.Config().CaptureRoot, host, name, original)
		if e != nil {
			continue
		}
		manifest := view.manifest
		if manifest.Host.ID != host || manifest.Source.Name != name || manifest.Source.Kind != "chatgpt" && manifest.Source.Kind != "chatgpt-export" {
			continue
		}
		data, err = read(sourceFiles{view: view, ctx: r.Context()}.Open, file)
		if err == nil {
			break
		}
	}
	if data == nil || err != nil {
		for _, source := range s.Config().Sources {
			if source.Kind != "chatgpt" && source.Kind != "chatgpt-export" {
				continue
			}
			if source.Path == original || capturePathIsWithin(original, source.Path) {
				data, err = read(func(file string) (*sourceFile, error) {
					rootPath := source.Path
					if info, e := os.Stat(rootPath); e != nil {
						return nil, e
					} else if !info.IsDir() {
						rootPath = filepath.Dir(rootPath)
					}
					rel, e := filepath.Rel(rootPath, file)
					if e != nil || !filepath.IsLocal(rel) {
						return nil, os.ErrPermission
					}
					root, e := os.OpenRoot(rootPath)
					if e != nil {
						return nil, e
					}
					defer root.Close()
					input, e := root.Open(rel)
					if e != nil {
						return nil, e
					}
					info, e := input.Stat()
					if e != nil {
						input.Close()
						return nil, e
					}
					if !info.Mode().IsRegular() {
						input.Close()
						return nil, os.ErrPermission
					}
					return &sourceFile{Reader: input, file: input, info: info}, nil
				}, original)
				if err == nil {
					break
				}
			}
		}
	}
	if data == nil || err != nil || len(data) > 64<<20 {
		http.NotFound(w, r)
		return
	}
	contentType := http.DetectContentType(data)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	if !strings.HasPrefix(contentType, "image/") && !strings.HasPrefix(contentType, "audio/") && !strings.HasPrefix(contentType, "text/plain") {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	disposition := "attachment"
	if strings.HasPrefix(contentType, "image/") || strings.HasPrefix(contentType, "audio/") {
		disposition = "inline"
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": filepath.Base(firstString(asset["name"]))}))
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
}

// All selected export evidence is retained for folder inputs too. A folder with
// a producer inventory is limited to its declared members; older exports use
// their known sidecars and exporter-shaped payload names.
func chatGPTCaptureFiles(files sourceFiles, root string) ([]string, error) {
	info, err := files.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return []string{root}, nil
	}
	if data, err := chatGPTSidecar(files, filepath.Join(root, chatGPTInventoryName)); err == nil {
		var inventory chatGPTInventory
		if json.Unmarshal(data, &inventory) != nil || inventory.Format != chatGPTInventoryFormat {
			return nil, errors.New("invalid ChatGPT collection receipt")
		}
		out := []string{filepath.Join(root, chatGPTInventoryName)}
		for _, name := range inventory.Files {
			if !claudeExportPath(name) {
				return nil, errors.New("unsafe ChatGPT snapshot path")
			}
			out = append(out, filepath.Join(root, filepath.FromSlash(name)))
		}
		return out, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	names := []string{}
	if data, err := chatGPTSidecar(files, filepath.Join(root, "export_manifest.json")); err == nil {
		var manifest struct {
			Files []struct {
				Path string `json:"path"`
			} `json:"export_files"`
		}
		if json.Unmarshal(data, &manifest) != nil {
			return nil, errors.New("invalid ChatGPT inventory")
		}
		names = append(names, "export_manifest.json")
		for _, item := range manifest.Files {
			if !claudeExportPath(item.Path) {
				return nil, errors.New("unsafe ChatGPT inventory member")
			}
			names = append(names, item.Path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	} else {
		entries, err := files.ReadDir(root)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if len(chatGPTConversationFiles([]string{name})) > 0 || slices.Contains([]string{"library_files.json", "conversation_asset_file_names.json", "conversation_summaries.json", "shared_conversations.json", "sectioned_conversations.json"}, name) || strings.HasPrefix(name, "file_") && strings.HasSuffix(name, ".dat") || strings.HasPrefix(name, "file-inline-") && strings.HasSuffix(name, ".dat") {
				names = append(names, name)
			}
		}
	}
	out := []string{}
	for _, name := range names {
		if !chatGPTExcluded(name) {
			out = append(out, filepath.Join(root, filepath.FromSlash(name)))
		}
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

// Snapshot hashes describe the filtered bytes actually retained in the library.
func chatGPTFileHash(files sourceFiles, file string) (string, error) {
	in, err := files.Open(file)
	if err != nil {
		return "", err
	}
	defer in.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, claudeContextReader{files.ctx, in}); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func chatGPTSidecar(files sourceFiles, file string) ([]byte, error) {
	in, err := files.Open(file)
	if err != nil {
		return nil, err
	}
	defer in.Close()
	data, err := io.ReadAll(io.LimitReader(in, claudeExportLimits.Manifest+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > claudeExportLimits.Manifest {
		return nil, errors.New("ChatGPT sidecar exceeds size limit")
	}
	return data, nil
}
