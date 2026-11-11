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
const claudeObjectInventoryFormat = "pharos-claude-export-v2"

var claudeExportLimits = struct {
	Manifest, Member, Expanded, Archive int64
	Entries, Parts, Depth               int
}{8 << 20, 1 << 30, 32 << 30, 16 << 30, 100000, 10000, 12}

type claudeExportAsset struct {
	Retirement    string `json:"retirement_proof,omitempty"`
	Path          string `json:"path"`
	Filename      string `json:"filename"`
	Category      string `json:"category"`
	SHA256        string `json:"sha256"`
	Size          int64  `json:"size"`
	Conversations bool   `json:"conversations"`
	// SourceModified is the downloaded file's time, which orders a snapshot
	// collected without a manifest; the stored copy's time is collection time.
	SourceModified string `json:"source_modified_at,omitempty"`
	Units          string `json:"source_units,omitempty"`
	UnitsSHA256    string `json:"source_units_sha256,omitempty"`
	Retired        bool   `json:"native_retired,omitempty"`
}

type claudeExportInventory struct {
	Format    string              `json:"format"`
	CreatedAt string              `json:"created_at,omitempty"`
	Account   string              `json:"account_uuid,omitempty"`
	Files     []claudeExportAsset `json:"files"`
	Missing   []string            `json:"missing"`
}

type claudeExportInput struct {
	path            string
	member          string // Known supplementary member, for an unzipped export.
	conversations   bool
	version         int64
	snapshot        string
	sha256          string
	units           *claudeSourceUnits
	objectRoot      string
	retired         bool
	objectInventory bool
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
	// A managed collection keeps its inventories in known places. Anything
	// else is a person's folder, which may hold unrelated files: only its own
	// conversation files and known supplementary directories are read.
	// ZIPs there are not read, since they can carry account records that
	// collection would strip.
	inventories, err := claudeInventories(files, root)
	if err != nil {
		return nil, err
	}
	var candidates []string
	var inputs []claudeExportInput
	objectInputs := map[string]bool{}
	if len(inventories) == 0 {
		entries, err := files.ReadDir(root)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.Type().IsRegular() && claudeConversationName(entry.Name()) {
				candidates = append(candidates, filepath.Join(root, entry.Name()))
			}
			if entry.IsDir() && claudeSupplementDirectory(entry.Name()) {
				err := files.WalkDir(filepath.Join(root, entry.Name()), func(name string, item fs.DirEntry, walkErr error) error {
					if walkErr != nil {
						return walkErr
					}
					if err := files.context().Err(); err != nil {
						return err
					}
					if item.Type()&os.ModeSymlink != 0 {
						return fmt.Errorf("Claude export contains a symbolic link: %s", name)
					}
					if item.IsDir() {
						return nil
					}
					if !item.Type().IsRegular() {
						return fmt.Errorf("Claude export contains a non-regular file: %s", name)
					}
					rel, err := filepath.Rel(root, name)
					if err != nil {
						return err
					}
					inputs = append(inputs, claudeExportInput{path: name, member: filepath.ToSlash(rel), snapshot: root})
					if len(inputs) > claudeExportLimits.Entries {
						return errors.New("Claude export exceeds the entry limit")
					}
					return nil
				})
				if err != nil {
					return nil, err
				}
			}
		}
		if len(candidates) > claudeExportLimits.Parts {
			return nil, fmt.Errorf("Claude export exceeds the file limit")
		}
	}
	for _, name := range inventories {
		var inventory claudeExportInventory
		if err := claudeReadJSON(files, name, &inventory); err != nil {
			return nil, fmt.Errorf("Claude export inventory %s: %w", name, err)
		}
		if inventory.Format != claudeInventoryFormat && inventory.Format != claudeObjectInventoryFormat {
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
		inputs = append(inputs, claudeExportInput{path: name, objectInventory: inventory.Format == claudeObjectInventoryFormat})
		seen := map[string]bool{}
		for _, asset := range inventory.Files {
			if !claudeExportPath(asset.Path) || seen[asset.Path] {
				return nil, fmt.Errorf("Claude export inventory has an unsafe or repeated path")
			}
			seen[asset.Path] = true
			local := filepath.Join(filepath.Dir(name), filepath.FromSlash(asset.Path))
			for dir := local; dir != filepath.Dir(name) && dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
				if link, err := os.Lstat(dir); err == nil && link.Mode()&os.ModeSymlink != 0 {
					return nil, fmt.Errorf("Claude export contains a symbolic link: %s", asset.Path)
				}
			}
			if asset.Retired {
				if inventory.Format != claudeObjectInventoryFormat || asset.Units == "" || !validRecordHash(asset.UnitsSHA256) {
					return nil, errors.New("retired Claude input lacks its versioned replacement proof")
				}
				part := strings.TrimSuffix(filepath.Base(asset.Path), filepath.Ext(asset.Path))
				proofPath, err := readClaudeRetirementProof(files, root, filepath.Base(filepath.Dir(name)), part, asset)
				if err != nil {
					return nil, err
				}
				inputs = append(inputs, claudeExportInput{path: proofPath})
			} else {
				info, err := files.Stat(local)
				if err != nil || !info.Mode().IsRegular() || info.Size() != asset.Size {
					return nil, fmt.Errorf("Claude export part %s is missing or its size changed", asset.Filename)
				}
			}
			partVersion := version
			if partVersion == 0 && asset.SourceModified != "" {
				if at, err := time.Parse(time.RFC3339Nano, asset.SourceModified); err == nil {
					partVersion = at.UnixNano()
				}
			}
			input := claudeExportInput{path: local, conversations: asset.Conversations, version: partVersion, snapshot: name, sha256: asset.SHA256, retired: asset.Retired}
			// A standalone copied snapshot still replays its authoritative native
			// part without requiring the parent collection's optional object pool.
			useUnits := asset.Units != ""
			if filepath.Dir(name) == root && !asset.Retired {
				if _, err := files.Stat(filepath.Join(root, "objects")); errors.Is(err, fs.ErrNotExist) {
					useUnits = false
				} else if err != nil {
					return nil, err
				}
			}
			if useUnits {
				if !claudeExportPath(asset.Units) || !strings.HasPrefix(asset.Units, "units/") {
					return nil, errors.New("unsafe Claude source-unit receipt path")
				}
				unitPath, err := claudeOwnedPath(filepath.Dir(name), asset.Units)
				if err != nil {
					return nil, err
				}
				units, err := readClaudeSourceUnits(files, unitPath, asset.SHA256, asset.UnitsSHA256)
				if err != nil {
					return nil, err
				}
				input.units, input.objectRoot = units, root
				inputs = append(inputs, claudeExportInput{path: unitPath})
				for _, pack := range units.packs() {
					if objectInputs[pack] {
						continue
					}
					objectInputs[pack] = true
					packPath := filepath.Join(root, filepath.FromSlash(pack))
					info, err := files.Stat(packPath)
					if err != nil || !info.Mode().IsRegular() {
						return nil, errors.New("Claude source object pack is unavailable")
					}
					inputs = append(inputs, claudeExportInput{path: packPath})
				}
			}
			inputs = append(inputs, input)
		}
	}
	if len(inventories) == 0 {
		for _, name := range candidates {
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

// claudeInventories finds a managed collection's inventories: the root's own,
// or one per snapshot. Pharos writes these trees, so a link there is refused.
func claudeInventories(files sourceFiles, root string) ([]string, error) {
	var inventories []string
	check := func(dir string) error {
		entries, err := files.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.Name() != claudeInventoryName {
				continue
			}
			if !entry.Type().IsRegular() {
				return fmt.Errorf("Claude export inventory %s is not a regular file", filepath.Join(dir, entry.Name()))
			}
			inventories = append(inventories, filepath.Join(dir, entry.Name()))
		}
		return nil
	}
	if err := check(root); err != nil {
		return nil, err
	}
	snapshots := filepath.Join(root, "snapshots")
	entries, err := files.ReadDir(snapshots)
	if errors.Is(err, fs.ErrNotExist) {
		return inventories, nil
	} else if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if err := files.context().Err(); err != nil {
			return nil, err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("Claude export contains a symbolic link: %s", filepath.Join("snapshots", entry.Name()))
		}
		if entry.IsDir() {
			if err := check(filepath.Join(snapshots, entry.Name())); err != nil {
				return nil, err
			}
		}
	}
	if len(inventories) > claudeExportLimits.Parts {
		return nil, fmt.Errorf("Claude export exceeds the inventory limit")
	}
	return inventories, nil
}

func verifyClaudeExportInput(files sourceFiles, input claudeExportInput) error {
	if input.retired {
		return nil
	}
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
