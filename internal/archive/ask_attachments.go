package archive

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// File bytes stay in private temporary files. Ask stores only paths and
// identities; none of this context enters the general archive index.
type askAttachment struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	URL      string `json:"url,omitempty"`
	Selector string `json:"selector,omitempty"`
	Path     string `json:"path,omitempty"`
	MIME     string `json:"mime,omitempty"`
	Size     int64  `json:"size,omitempty"`
	// Offset is the chip's position in the plain question, in UTF-16 units.
	Offset int `json:"offset,omitempty"`
}

const askAttachmentCount = 8
const askAttachmentFileBytes = 20 << 20
const askAttachmentBytes = 50 << 20

func (s *Server) askAttachmentRoot() string {
	root, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		root = os.TempDir()
	}
	digest := sha256.Sum256([]byte(s.Config().CatalogPath + "\x00" + currentHost().ID))
	return filepath.Join(root, "pharos-ask-files-"+hex.EncodeToString(digest[:12]))
}

func (s *Server) validateAskAttachments(items []askAttachment) error {
	if len(items) > askAttachmentCount {
		return fmt.Errorf("attach at most %d items", askAttachmentCount)
	}
	seen, size := map[string]bool{}, int64(0)
	for _, a := range items {
		if !askID.MatchString(a.ID) || seen[a.ID] || strings.TrimSpace(a.Name) == "" || len(a.Name) > 1200 || a.Offset < 0 || a.Offset > 12000 {
			return fmt.Errorf("invalid attachment identity or name")
		}
		seen[a.ID] = true
		switch a.Kind {
		case "url", "element":
			if !askSafeURL(a.URL) || a.Path != "" || a.MIME != "" || a.Size != 0 || len(a.Selector) > 4000 || (a.Kind == "element" && strings.TrimSpace(a.Selector) == "") || (a.Kind == "url" && a.Selector != "") {
				return fmt.Errorf("invalid page or element attachment")
			}
		case "file":
			if strings.ContainsAny(a.Name, "/\\") || a.Name == "." || a.Name == ".." || a.Path != filepath.Join(s.askAttachmentRoot(), a.ID, a.Name) || a.URL != "" || a.Selector != "" || len(a.MIME) > 150 || a.Size < 0 || a.Size > askAttachmentFileBytes {
				return fmt.Errorf("invalid temporary file attachment; attach the original file again")
			}
			size += a.Size
		default:
			return fmt.Errorf("unsupported attachment kind")
		}
	}
	if size > askAttachmentBytes {
		return fmt.Errorf("files must total at most 50 MiB")
	}
	return nil
}

// Interleave references with the user's question so a moved chip changes the
// prompt the agent receives. Offsets at a surrogate boundary round down; old
// messages without offsets keep their attachments at the start.
func askAttachmentContent(text string, items []askAttachment) []any {
	positions, units := map[int]int{}, 0
	for index, r := range text {
		positions[units] = index
		units++
		if r > 0xffff {
			units++
		}
	}
	positions[units] = len(text)
	ordered := append([]askAttachment(nil), items...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Offset < ordered[j].Offset })
	parts, previous := []any{}, 0
	for _, item := range ordered {
		offset := max(0, min(units, item.Offset))
		for offset > 0 {
			if _, ok := positions[offset]; ok {
				break
			}
			offset--
		}
		next := positions[offset]
		if next > previous {
			parts = append(parts, text[previous:next])
		}
		parts = append(parts, map[string]string{"attachmentID": item.ID})
		previous = next
	}
	if previous < len(text) {
		parts = append(parts, text[previous:])
	}
	return parts
}

func askFilesAvailable(items []askAttachment) error {
	for _, item := range items {
		if item.Kind != "file" {
			continue
		}
		info, err := os.Lstat(item.Path)
		if err != nil || !info.Mode().IsRegular() || info.Size() != item.Size {
			return fmt.Errorf("%s is no longer available in temporary storage; attach the original file again", item.Name)
		}
	}
	return nil
}

func (s *Server) askUpload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	r.Body = http.MaxBytesReader(w, r.Body, askAttachmentFileBytes+(1<<20))
	reader, err := r.MultipartReader()
	if err != nil {
		writeError(w, fmt.Errorf("choose a file to attach"), 400)
		return
	}
	part, err := reader.NextPart()
	if err != nil || part.FormName() != "file" || part.FileName() == "" {
		writeError(w, fmt.Errorf("choose a file to attach"), 400)
		return
	}
	defer part.Close()
	name := part.FileName()
	if len(name) > 300 || strings.ContainsAny(name, "/\\") || name == "." || name == ".." {
		writeError(w, fmt.Errorf("invalid file name"), 400)
		return
	}
	root := s.askAttachmentRoot()
	if err = os.MkdirAll(root, 0700); err != nil {
		writeError(w, err, 500)
		return
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || resolved != root {
		writeError(w, fmt.Errorf("temporary attachment directory is unavailable"), 500)
		return
	}
	dir, err := os.MkdirTemp(root, "file-")
	if err != nil {
		writeError(w, err, 500)
		return
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(dir)
		}
	}()
	path := filepath.Join(dir, name)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		writeError(w, err, 500)
		return
	}
	size, copyErr := io.Copy(file, io.LimitReader(part, askAttachmentFileBytes+1))
	closeErr := file.Close()
	if copyErr != nil || size > askAttachmentFileBytes {
		writeError(w, fmt.Errorf("files must be at most 20 MiB each"), 400)
		return
	}
	if closeErr != nil {
		writeError(w, closeErr, 500)
		return
	}
	kind := mime.TypeByExtension(filepath.Ext(name))
	if kind == "" {
		kind = "application/octet-stream"
	}
	keep = true
	writeJSON(w, askAttachment{ID: filepath.Base(dir), Kind: "file", Name: name, Path: path, MIME: kind, Size: size}, 200)
}

var askReadAttachmentTool = map[string]any{
	"name":        "read_attached_file",
	"description": "Read an original file the user attached to this Ask conversation, using its exact temporary path. No import or conversion has been performed. UTF-8 files return text, images return original image bytes, and other files (including PDFs) return original bytes as embedded resources. Only explicitly attached paths are allowed.",
	"inputSchema": map[string]any{"type": "object", "required": []string{"path"}, "properties": map[string]any{"path": map[string]string{"type": "string"}}, "additionalProperties": false},
	"annotations": map[string]any{"readOnlyHint": true, "destructiveHint": false, "openWorldHint": false},
}

// This capability is scoped to one Ask invocation's private endpoint manifest.
// Outside agents never receive it through the general library MCP connection.
func (m *mcpServer) askFileScope() []askAttachment {
	if m.service == "" {
		return nil
	}
	data, err := os.ReadFile(m.service)
	if err != nil {
		return nil
	}
	var scope struct {
		Attachments []askAttachment `json:"attachments"`
	}
	if json.Unmarshal(data, &scope) != nil {
		return nil
	}
	return scope.Attachments
}

func readAskAttachment(request map[string]any, scope []askAttachment) map[string]any {
	path := firstString(mapValue(mapValue(request["params"])["arguments"])["path"])
	for _, item := range scope {
		if item.Kind != "file" || item.Path != path {
			continue
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() != item.Size {
			return mcpToolError(request, "This temporary file is unavailable or changed. Ask the user to attach it again.")
		}
		file, err := os.Open(path)
		if err != nil {
			return mcpToolError(request, err.Error())
		}
		defer file.Close()
		data, err := io.ReadAll(io.LimitReader(file, askAttachmentFileBytes+1))
		if err != nil || len(data) > askAttachmentFileBytes {
			return mcpToolError(request, "The attached file could not be read within the 20 MiB limit.")
		}
		var content map[string]any
		if item.MIME == "image/png" || item.MIME == "image/jpeg" || item.MIME == "image/gif" || item.MIME == "image/webp" {
			content = map[string]any{"type": "image", "mimeType": item.MIME, "data": base64.StdEncoding.EncodeToString(data)}
		} else if utf8.Valid(data) && !strings.ContainsRune(string(data), 0) {
			content = map[string]any{"type": "text", "text": string(data)}
		} else {
			content = map[string]any{"type": "resource", "resource": map[string]any{"uri": (&url.URL{Scheme: "file", Path: path}).String(), "mimeType": item.MIME, "blob": base64.StdEncoding.EncodeToString(data)}}
		}
		return mcpResult(request, map[string]any{"content": []map[string]any{content}})
	}
	return mcpToolError(request, "Only file paths explicitly attached to this conversation may be read.")
}
