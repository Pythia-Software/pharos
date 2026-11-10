package archive

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The inventory is a derived, credential-free receipt. Claude's original
// manifest contains expiring download capabilities and is never a capture input.
const claudeInventoryName = "claude-export.json"
const claudeInventoryFormat = "pharos-claude-export-v1"

var claudeExportLimits = struct {
	Manifest, Member, Expanded, Archive int64
	Entries, Parts, Depth               int
}{8 << 20, 1 << 30, 32 << 30, 16 << 30, 100000, 10000, 12}

type claudeExportAsset struct {
	Path          string `json:"path"`
	Filename      string `json:"filename"`
	Category      string `json:"category"`
	SHA256        string `json:"sha256"`
	Size          int64  `json:"size"`
	Conversations bool   `json:"conversations"`
	// SourceModified is the downloaded file's time, which orders a snapshot
	// collected without a manifest; the stored copy's time is collection time.
	SourceModified string `json:"source_modified_at,omitempty"`
}

type claudeExportInventory struct {
	Format    string              `json:"format"`
	CreatedAt string              `json:"created_at,omitempty"`
	Account   string              `json:"account_uuid,omitempty"`
	Files     []claudeExportAsset `json:"files"`
	Missing   []string            `json:"missing"`
}

type claudeExportInput struct {
	path          string
	conversations bool
	version       int64
	snapshot      string
	sha256        string
}

// Export paths use slash semantics, independently of the indexing host. This
// validates names without extracting ZIPs or interpreting artifact source paths.
func claudeExportPath(name string) bool {
	return name != "" && name != "." && !strings.ContainsAny(name, "\\\x00:") &&
		!strings.HasPrefix(name, "/") && path.Clean(name) == name && name != ".." && !strings.HasPrefix(name, "../")
}

func claudeConversationName(name string) bool {
	base := path.Base(name)
	if base == "conversations.json" {
		return true
	}
	if !strings.HasPrefix(base, "conversations-") || !strings.HasSuffix(base, ".json") {
		return false
	}
	number := strings.TrimSuffix(strings.TrimPrefix(base, "conversations-"), ".json")
	if number == "" {
		return false
	}
	for _, digit := range number {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

func claudeReadJSON(files sourceFiles, name string, target any) error {
	file, err := files.Open(name)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() > claudeExportLimits.Manifest {
		return fmt.Errorf("Claude export inventory exceeds %d bytes", claudeExportLimits.Manifest)
	}
	decoder := json.NewDecoder(io.LimitReader(file, claudeExportLimits.Manifest+1))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return ensureEOF(decoder)
}

// claudeExportInputs is shared by live discovery and capture planning. Managed
// collections enumerate receipts, so a missing or new file is never silently
// treated as part of a different export snapshot.
func claudeExportInputs(files sourceFiles, root string) ([]claudeExportInput, error) {
	info, err := files.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return []claudeExportInput{{path: root, conversations: true}}, nil
	}
	var inventories, candidates []string
	count := 0
	err = files.WalkDir(root, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := files.context().Err(); err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, name)
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("Claude export contains a symbolic link: %s", rel)
		}
		if entry.IsDir() {
			if strings.Count(filepath.ToSlash(rel), "/") > claudeExportLimits.Depth {
				return fmt.Errorf("Claude export exceeds the directory depth limit")
			}
			return nil
		}
		count++
		if count > claudeExportLimits.Parts {
			return fmt.Errorf("Claude export exceeds the file limit")
		}
		if entry.Name() == claudeInventoryName {
			inventories = append(inventories, name)
		} else if claudeConversationName(entry.Name()) || strings.EqualFold(filepath.Ext(name), ".zip") {
			candidates = append(candidates, name)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var inputs []claudeExportInput
	for _, name := range inventories {
		var inventory claudeExportInventory
		if err := claudeReadJSON(files, name, &inventory); err != nil {
			return nil, fmt.Errorf("Claude export inventory %s: %w", name, err)
		}
		if inventory.Format != claudeInventoryFormat {
			return nil, fmt.Errorf("unsupported Claude export inventory format")
		}
		if len(inventory.Files) > claudeExportLimits.Parts {
			return nil, fmt.Errorf("Claude export inventory exceeds the part limit")
		}
		version := int64(0)
		if inventory.CreatedAt != "" {
			at, err := time.Parse(time.RFC3339Nano, inventory.CreatedAt)
			if err != nil {
				return nil, fmt.Errorf("Claude export has an invalid creation timestamp")
			}
			version = at.UnixNano()
		}
		inputs = append(inputs, claudeExportInput{path: name})
		seen := map[string]bool{}
		for _, asset := range inventory.Files {
			if !claudeExportPath(asset.Path) || seen[asset.Path] {
				return nil, fmt.Errorf("Claude export inventory has an unsafe or repeated path")
			}
			seen[asset.Path] = true
			local := filepath.Join(filepath.Dir(name), filepath.FromSlash(asset.Path))
			info, err := files.Stat(local)
			if err != nil || !info.Mode().IsRegular() || info.Size() != asset.Size {
				return nil, fmt.Errorf("Claude export part %s is missing or its size changed", asset.Filename)
			}
			partVersion := version
			if partVersion == 0 && asset.SourceModified != "" {
				if at, err := time.Parse(time.RFC3339Nano, asset.SourceModified); err == nil {
					partVersion = at.UnixNano()
				}
			}
			inputs = append(inputs, claudeExportInput{path: local, conversations: asset.Conversations, version: partVersion, snapshot: name, sha256: asset.SHA256})
		}
	}
	if len(inventories) == 0 {
		for _, name := range candidates {
			// Standalone category ZIPs are preserved too. Discovery decides
			// which members actually contain conversations.
			inputs = append(inputs, claudeExportInput{path: name, conversations: true, snapshot: root})
		}
	}
	sort.Slice(inputs, func(i, j int) bool {
		if inputs[i].version != inputs[j].version {
			return inputs[i].version < inputs[j].version
		}
		return inputs[i].path < inputs[j].path
	})
	return inputs, nil
}

func verifyClaudeExportInput(files sourceFiles, input claudeExportInput) error {
	if input.sha256 == "" {
		return nil
	}
	file, err := files.Open(input.path)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, claudeContextReader{files.context(), file}); err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != input.sha256 {
		return fmt.Errorf("Claude export part %s no longer matches its collection receipt", filepath.Base(input.path))
	}
	return nil
}

func claudeZipListing(archive *zip.Reader) ([]*zip.File, error) {
	if len(archive.File) > claudeExportLimits.Entries {
		return nil, errors.New("Claude ZIP exceeds the entry limit")
	}
	var expanded uint64
	seen := map[string]bool{}
	var conversations []*zip.File
	for _, entry := range archive.File {
		name := strings.TrimSuffix(entry.Name, "/")
		if !claudeExportPath(name) || seen[name] || entry.Mode()&os.ModeSymlink != 0 || !entry.FileInfo().IsDir() && !entry.Mode().IsRegular() {
			return nil, fmt.Errorf("Claude ZIP has an unsafe, duplicate or non-regular entry: %s", entry.Name)
		}
		seen[name] = true
		if entry.UncompressedSize64 > uint64(claudeExportLimits.Member) {
			return nil, fmt.Errorf("Claude ZIP member %s exceeds the size limit", entry.Name)
		}
		expanded += entry.UncompressedSize64
		if expanded > uint64(claudeExportLimits.Expanded) {
			return nil, errors.New("Claude ZIP exceeds the expanded size limit")
		}
		if !entry.FileInfo().IsDir() && claudeConversationName(entry.Name) {
			conversations = append(conversations, entry)
		}
	}
	sort.Slice(conversations, func(i, j int) bool { return conversations[i].Name < conversations[j].Name })
	return conversations, nil
}

type claudeFileDigest struct {
	SHA256 string
	Size   int64
}

// Verification reads each member to EOF, including CRC checks. Nested artifact
// inventories refer to files inside this ZIP and never trigger network requests.
func verifyClaudeZIP(ctx context.Context, file *os.File, size int64) (bool, error) {
	archive, err := zip.NewReader(file, size)
	if err != nil {
		return false, err
	}
	conversations, err := claudeZipListing(archive)
	if err != nil {
		return false, err
	}
	digests := map[string]claudeFileDigest{}
	manifests := map[string][]byte{}
	for _, entry := range archive.File {
		if entry.FileInfo().IsDir() {
			continue
		}
		reader, err := entry.Open()
		if err != nil {
			return false, err
		}
		hash := sha256.New()
		var target io.Writer = hash
		var content strings.Builder
		isManifest := strings.HasSuffix(entry.Name, ".files.json")
		if isManifest {
			if entry.UncompressedSize64 > uint64(claudeExportLimits.Manifest) {
				reader.Close()
				return false, errors.New("Claude artifact inventory exceeds the size limit")
			}
			target = io.MultiWriter(hash, &content)
		}
		n, readErr := io.Copy(target, claudeContextReader{ctx, io.LimitReader(reader, claudeExportLimits.Member+1)})
		closeErr := reader.Close()
		if readErr != nil || closeErr != nil || n != int64(entry.UncompressedSize64) {
			return false, fmt.Errorf("Claude ZIP member %s failed integrity verification: %w", entry.Name, errors.Join(readErr, closeErr, errors.New("member could not be verified")))
		}
		digests[entry.Name] = claudeFileDigest{hex.EncodeToString(hash.Sum(nil)), n}
		if isManifest {
			manifests[entry.Name] = []byte(content.String())
		}
	}
	for name, content := range manifests {
		var inventory struct {
			Files []struct {
				Path   string `json:"exported_as"`
				SHA256 string `json:"sha256"`
				Size   *int64 `json:"size_bytes"`
			} `json:"files"`
		}
		if err := json.Unmarshal(content, &inventory); err != nil || inventory.Files == nil {
			return false, fmt.Errorf("invalid Claude artifact inventory %s", name)
		}
		seen := map[string]bool{}
		for _, item := range inventory.Files {
			if !claudeExportPath(item.Path) || seen[item.Path] {
				return false, fmt.Errorf("unsafe or repeated path in Claude artifact inventory %s", name)
			}
			seen[item.Path] = true
			// A version.files.json sits beside its version/ directory.
			target := path.Join(strings.TrimSuffix(name, ".files.json"), item.Path)
			digest, exists := digests[target]
			if !exists || item.Size != nil && digest.Size != *item.Size || item.SHA256 != "" && !strings.EqualFold(digest.SHA256, item.SHA256) {
				return false, fmt.Errorf("Claude artifact file %s is missing or does not match its inventory", target)
			}
		}
	}
	return len(conversations) > 0, nil
}
