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
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gbdubs/pharos/internal/platform/filelock"
)

type claudeManifest struct {
	Version    string               `json:"version"`
	CreatedAt  string               `json:"created_at"`
	TotalFiles *int                 `json:"total_files"`
	DataFiles  []claudeManifestPart `json:"data_files"`
}

type claudeManifestPart struct {
	Filename string `json:"filename"`
	Category string `json:"category"`
	Part     int    `json:"part"`
	URL      string `json:"export_url,omitempty"`
	SHA256   string `json:"sha256,omitempty"`
	Size     *int64 `json:"size_bytes,omitempty"`
}

type claudeImportPart struct {
	Retirement     string `json:"retirement_proof,omitempty"`
	OriginalHash   string `json:"original_sha256,omitempty"`
	ID             string `json:"id"`
	Filename       string `json:"filename"`
	Category       string `json:"category"`
	Part           int    `json:"part"`
	Parent         string `json:"parent,omitempty"`
	State          string `json:"state"`
	Error          string `json:"error,omitempty"`
	Download       bool   `json:"download_available"`
	ExpectedHash   string `json:"expected_sha256,omitempty"`
	ExpectedSize   *int64 `json:"expected_size,omitempty"`
	SHA256         string `json:"sha256,omitempty"`
	Size           int64  `json:"size"`
	Conversations  int    `json:"conversations"`
	Messages       int    `json:"messages"`
	IsConversation bool   `json:"is_conversation"`
	Manifest       bool   `json:"manifest,omitempty"`
	Storage        string `json:"storage,omitempty"`
	SourceModified string `json:"source_modified_at,omitempty"`
	Units          string `json:"source_units,omitempty"`
	UnitsSHA256    string `json:"source_units_sha256,omitempty"`
	UnitsError     string `json:"source_units_error,omitempty"`
	Retired        bool   `json:"native_retired,omitempty"`
	// Hint belongs to the user's explicitly selected input, never a URL.
	Hint string `json:"input_path,omitempty"`
}

type claudeImportJob struct {
	Groups       []claudeSupplementObservation `json:"supplement_groups,omitempty"`
	Error        string                        `json:"error,omitempty"`
	Version      int                           `json:"version"`
	ID           string                        `json:"id"`
	Name         string                        `json:"name"`
	State        string                        `json:"state"`
	CreatedAt    string                        `json:"created_at"`
	ExportedAt   string                        `json:"exported_at,omitempty"`
	UpdatedAt    string                        `json:"updated_at"`
	ManifestHash string                        `json:"manifest_sha256,omitempty"`
	Completeness string                        `json:"completeness"`
	Directories  []string                      `json:"directories"`
	Parts        []claudeImportPart            `json:"parts"`
	Warnings     []string                      `json:"warnings"`
	Source       *ProbeAcceptResult            `json:"source,omitempty"`
}

type claudeImportRequest struct {
	Paths []string `json:"paths"`
	Name  string   `json:"name"`
	ID    string   `json:"id"`
}

func claudeImportRoot(config Config, name string) string {
	return filepath.Join(config.CaptureRoot, ".claude-imports", currentHost().ID, name)
}

func claudeImportJobDir(config Config, name, id string) string {
	return filepath.Join(claudeImportRoot(config, name), "snapshots", id)
}

func claudeImportSecrets(config Config, id string) string {
	// The library can travel to another Mac. Download capabilities stay on
	// the importing host and are deliberately outside its backup/capture tree.
	return filepath.Join(pharosSupportDir(), "claude-import-links", hashBytes([]byte(config.CaptureRoot)), id+".json")
}

func claudeImportName(name string) (string, error) {
	if name == "" {
		name = "claude-ai-imports"
	}
	if !sourceNameValid.MatchString(name) || name == "." || name == ".." {
		return "", errors.New("choose a source name using letters, digits, '.', '_' and '-'")
	}
	return name, nil
}

type claudeImportLease struct {
	file    *os.File
	capture *captureSession
}

func (lease *claudeImportLease) Close() error {
	err := lease.file.Close()
	return errors.Join(err, lease.capture.Close())
}

func claudeImportLock(config Config, name string) (io.Closer, error) {
	if !config.Library || config.CaptureRoot == "" {
		return nil, errNotLibrary(config)
	}
	if currentHost().Fallback {
		return nil, errHostUnknown
	}
	// A watcher can outlive an eject. Recheck the library for each actual
	// collection, then use capture's existing volume/host guard and writer lock.
	if err := config.checkLibrary(volumeIdentity); err != nil {
		return nil, err
	}
	session, err := beginCapture(config)
	if err != nil {
		return nil, err
	}
	accepted := false
	defer func() {
		if !accepted {
			session.Close()
		}
	}()
	root := claudeImportRoot(config, name)
	lockPath, err := claudeOwnedPath(config.CaptureRoot, path.Join(".claude-imports", currentHost().ID, name, ".import.lock"))
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := filelock.Lock(file, filelock.Exclusive, filelock.Try); err != nil {
		file.Close()
		return nil, fmt.Errorf("another Claude export collection is running, or its lock is unavailable: %w", err)
	}
	accepted = true
	return &claudeImportLease{file: file, capture: session}, nil
}

func readClaudeManifest(name string) (claudeManifest, []byte, bool, error) {
	var manifest claudeManifest
	file, err := os.Open(name)
	if err != nil {
		return manifest, nil, false, err
	}
	defer file.Close()
	// Large conversation arrays are streamed later, never buffered here.
	var first [512]byte
	n, err := file.Read(first[:])
	if err != nil && err != io.EOF {
		return manifest, nil, false, err
	}
	if !strings.HasPrefix(strings.TrimSpace(string(first[:n])), "{") {
		return manifest, nil, false, nil
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return manifest, nil, false, err
	}
	content, err := io.ReadAll(io.LimitReader(file, claudeExportLimits.Manifest+1))
	if err != nil {
		return manifest, nil, false, err
	}
	if int64(len(content)) > claudeExportLimits.Manifest {
		return manifest, nil, false, errors.New("Claude manifest exceeds the size limit")
	}
	var shape map[string]json.RawMessage
	if err := json.Unmarshal(content, &shape); err != nil {
		return manifest, nil, false, err
	}
	if _, ok := shape["data_files"]; !ok {
		return manifest, nil, false, nil
	}
	if err := json.Unmarshal(content, &manifest); err != nil {
		return manifest, nil, true, errors.New("Claude manifest has invalid fields")
	}
	if manifest.DataFiles == nil || len(manifest.DataFiles) == 0 || len(manifest.DataFiles) > claudeExportLimits.Parts {
		return manifest, nil, true, errors.New("Claude manifest has an empty or excessive data_files list")
	}
	if manifest.TotalFiles != nil && *manifest.TotalFiles != len(manifest.DataFiles) {
		return manifest, nil, true, errors.New("Claude manifest total_files does not match data_files")
	}
	if manifest.CreatedAt != "" {
		if _, err := time.Parse(time.RFC3339Nano, manifest.CreatedAt); err != nil {
			return manifest, nil, true, errors.New("Claude manifest has an invalid created_at timestamp")
		}
	}
	return manifest, content, true, nil
}

func (job *claudeImportJob) addManifest(manifest claudeManifest, parent, directory string, links map[string]string) error {
	if strings.Count(parent, "/") > claudeExportLimits.Depth {
		return errors.New("Claude manifest hierarchy exceeds the depth limit")
	}
	if manifest.Version != "" && manifest.Version != "1.0" {
		job.Warnings = append(job.Warnings, "This export uses an unrecognized manifest version; known fields are being preserved.")
	}
	var additions []claudeImportPart
	addedLinks := map[string]string{}
	seen := map[string]bool{}
	existing := map[string]claudeImportPart{}
	for _, part := range job.Parts {
		existing[part.ID] = part
	}
	for _, entry := range manifest.DataFiles {
		if !claudeExportPath(entry.Filename) || entry.Part < 0 || entry.Category == "" || entry.Size != nil && *entry.Size < 0 {
			return errors.New("Claude manifest contains an unsafe path or invalid part metadata")
		}
		if entry.SHA256 != "" {
			decoded, err := hex.DecodeString(entry.SHA256)
			if err != nil || len(decoded) != sha256.Size {
				return errors.New("Claude manifest contains an invalid SHA-256")
			}
		}
		logical := path.Join(parent, entry.Filename)
		id := hashBytes([]byte(logical))
		if seen[id] {
			return fmt.Errorf("Claude manifest repeats a part: %s", entry.Filename)
		}
		seen[id] = true
		link := safeClaudeDownloadURL(entry.URL)
		if claudeExcludedCategory(entry.Category) || claudeAccountRecord(entry.Filename) {
			// Account records are never downloaded, so their link is not kept.
			link = ""
		} else if entry.URL != "" && link == "" {
			job.Warnings = append(job.Warnings, "A download link uses an unsupported address; download that part from Claude and add the file here.")
		}
		if link != "" {
			addedLinks[id] = link
		}
		if prior, ok := existing[id]; ok {
			// Repairing a collected child manifest must reuse its children.
			// A changed manifest belongs to a separate export snapshot.
			sameSize := prior.ExpectedSize == nil && entry.Size == nil || prior.ExpectedSize != nil && entry.Size != nil && *prior.ExpectedSize == *entry.Size
			if prior.Filename != entry.Filename || prior.Category != entry.Category || prior.Part != entry.Part || prior.Parent != parent || prior.ExpectedHash != entry.SHA256 || !sameSize {
				return fmt.Errorf("Claude manifest changes an existing part: %s", entry.Filename)
			}
			continue
		}
		if len(job.Parts)+len(additions) >= claudeExportLimits.Parts {
			return errors.New("Claude manifest hierarchy exceeds the part limit")
		}
		additions = append(additions, claudeImportPart{ID: id, Filename: entry.Filename, Category: entry.Category, Part: entry.Part, Parent: parent, State: "missing", ExpectedHash: entry.SHA256, ExpectedSize: entry.Size, Download: link != "", Hint: filepath.Join(directory, filepath.FromSlash(entry.Filename))})
	}
	job.Parts = append(job.Parts, additions...)
	for id, link := range addedLinks {
		links[id] = link
	}
	return nil
}

func saveClaudeLinks(config Config, id string, links map[string]string) error {
	name := claudeImportSecrets(config, id)
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		return err
	}
	return writeFileDurably(name, []byte(jsonText(links)))
}

func loadClaudeLinks(config Config, id string) map[string]string {
	links := map[string]string{}
	content, err := os.ReadFile(claudeImportSecrets(config, id))
	if err == nil {
		_ = json.Unmarshal(content, &links)
	}
	return links
}

func saveClaudeJob(config Config, job *claudeImportJob) error {
	job.UpdatedAt = now()
	dir := claudeImportJobDir(config, job.Name, job.ID)
	return writeFileDurably(filepath.Join(dir, "job.json"), []byte(jsonText(job)))
}

func loadClaudeJob(config Config, name, id string) (*claudeImportJob, error) {
	decoded, err := hex.DecodeString(id)
	if err != nil || len(decoded) != sha256.Size {
		return nil, errors.New("invalid Claude import job ID")
	}
	var job claudeImportJob
	if err := claudeReadJSON(sourceFiles{}, filepath.Join(claudeImportJobDir(config, name, id), "job.json"), &job); err != nil {
		return nil, err
	}
	if (job.Version != 1 && job.Version != 2) || job.ID != id || job.Name != name || len(job.Parts) > claudeExportLimits.Parts {
		return nil, errors.New("invalid or unsupported Claude import job")
	}
	for _, part := range job.Parts {
		decoded, decodeErr := hex.DecodeString(part.ID)
		if decodeErr != nil || len(decoded) != sha256.Size {
			return nil, errors.New("Claude import job has an invalid part ID")
		}
		if part.Storage != "" && part.Storage != path.Join("parts", part.ID+strings.ToLower(filepath.Ext(part.Filename))) {
			return nil, errors.New("Claude import job has an unexpected storage path")
		}
		if part.Units != "" && part.Units != path.Join("units", part.ID+".json") {
			return nil, errors.New("Claude import job has an unexpected source-unit path")
		}
		if part.Retired && (job.Version != 2 || part.Units == "" || !validRecordHash(part.UnitsSHA256)) {
			return nil, errors.New("retired Claude part lacks a versioned source-unit receipt")
		}
		if !claudeExportPath(part.Filename) || part.Storage != "" && !claudeExportPath(part.Storage) {
			return nil, errors.New("Claude import job contains an unsafe path")
		}
	}
	return &job, nil
}

func claudeImportPaths(paths []string) ([]string, error) {
	if len(paths) == 0 || len(paths) > claudeExportLimits.Parts {
		return nil, errors.New("choose a Claude manifest, export folder or downloaded files")
	}
	var result []string
	for _, name := range paths {
		name = expandPath(strings.TrimSpace(name))
		if !filepath.IsAbs(name) {
			return nil, errors.New("Claude export paths must be absolute or begin with ~/")
		}
		info, err := os.Lstat(name)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() && !info.IsDir() {
			return nil, fmt.Errorf("choose a readable regular file or folder: %s", name)
		}
		if !info.IsDir() {
			result = append(result, name)
			continue
		}
		err = filepath.WalkDir(name, func(file string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			rel, _ := filepath.Rel(name, file)
			if strings.Count(filepath.ToSlash(rel), "/") > claudeExportLimits.Depth {
				return errors.New("Claude export folder exceeds the depth limit")
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return errors.New("Claude export folders cannot contain symbolic links")
			}
			if !entry.IsDir() && (strings.EqualFold(filepath.Ext(file), ".zip") || strings.EqualFold(filepath.Ext(file), ".json")) {
				result = append(result, file)
				if len(result) > claudeExportLimits.Parts {
					return errors.New("Claude export folder exceeds the file limit")
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(result)
	return result, nil
}

// collectClaudeExport is shared by the UI and CLI. It journals each part under
// one source-wide lock, so a crash can leave an orphan temporary file but cannot
// publish a partially written archive or a receipt for an unverified transfer.
func collectClaudeExport(ctx context.Context, config Config, request claudeImportRequest) (result *claudeImportJob, failure error) {
	name, err := claudeImportName(request.Name)
	if err != nil {
		return nil, err
	}
	lock, err := claudeImportLock(config, name)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	var job *claudeImportJob
	defer func() {
		if failure != nil && job != nil {
			job.Error = failure.Error()
			if errors.Is(failure, context.Canceled) {
				job.State = "interrupted"
			} else {
				job.State = "failed"
			}
			_ = saveClaudeJob(config, job)
		}
	}()
	links := map[string]string{}
	var selected, explicit []string
	for _, input := range request.Paths {
		name := expandPath(strings.TrimSpace(input))
		if info, err := os.Lstat(name); err == nil && info.Mode().IsRegular() {
			explicit = append(explicit, name)
		}
	}
	if len(request.Paths) > 0 {
		selected, err = claudeImportPaths(request.Paths)
		if err != nil {
			return nil, err
		}
	}
	if request.ID != "" {
		job, err = loadClaudeJob(config, name, request.ID)
		if err != nil {
			return nil, err
		}
		links = loadClaudeLinks(config, job.ID)
	} else {
		if len(selected) == 0 {
			return nil, errors.New("choose a Claude manifest, export folder or downloaded files")
		}
		var manifests []string
		manifestFiles := map[string]struct {
			manifest claudeManifest
			content  []byte
		}{}
		children := map[string]bool{}
		isExplicit := map[string]bool{}
		for _, file := range explicit {
			isExplicit[file] = true
		}
		var rootManifest claudeManifest
		var raw []byte
		for _, file := range selected {
			if !strings.EqualFold(filepath.Ext(file), ".json") {
				continue
			}
			manifest, content, ok, err := readClaudeManifest(file)
			if err != nil {
				if isExplicit[file] || strings.HasPrefix(strings.ToLower(filepath.Base(file)), "manifest") {
					return nil, fmt.Errorf("Claude manifest %s: %w", filepath.Base(file), err)
				}
				continue
			}
			if ok {
				manifests = append(manifests, file)
				manifestFiles[file] = struct {
					manifest claudeManifest
					content  []byte
				}{manifest, content}
				for _, part := range manifest.DataFiles {
					if claudeExportPath(part.Filename) {
						child := filepath.Join(filepath.Dir(file), filepath.FromSlash(part.Filename))
						if child != file {
							children[child] = true
						}
					}
				}
			}
		}
		if len(manifests) > 0 {
			roots := manifests[:0]
			for _, file := range manifests {
				if !children[file] {
					roots = append(roots, file)
				}
			}
			manifests = roots
			if len(manifests) == 0 {
				return nil, errors.New("Claude manifest hierarchy contains a cycle with no top-level manifest")
			}
			rootManifest, raw = manifestFiles[manifests[0]].manifest, manifestFiles[manifests[0]].content
		}
		if len(manifests) > 1 {
			return nil, errors.New("this folder has multiple manifests; choose the top-level manifest to keep export snapshots separate")
		}
		job = &claudeImportJob{Version: 1, Name: name, CreatedAt: now(), Parts: []claudeImportPart{}, Directories: []string{}, Warnings: []string{}, Completeness: "unknown"}
		if len(manifests) == 1 {
			job.ID, job.ManifestHash, job.ExportedAt, job.Completeness = hashBytes(raw), hashBytes(raw), rootManifest.CreatedAt, "manifest"
			original := strings.TrimSuffix(claudeImportSecrets(config, job.ID), ".json") + ".manifest.json"
			if err := os.MkdirAll(filepath.Dir(original), 0700); err != nil {
				return nil, err
			}
			if err := writeFileDurably(original, raw); err != nil {
				return nil, err
			}
			if err := job.addManifest(rootManifest, "", filepath.Dir(manifests[0]), links); err != nil {
				return nil, err
			}
		} else {
			// Explicit files constitute a set, not a claim of a full account export.
			identity := []string{}
			for _, file := range selected {
				if !strings.EqualFold(filepath.Ext(file), ".zip") && !claudeConversationName(filepath.Base(file)) {
					continue
				}
				if strings.EqualFold(filepath.Ext(file), ".zip") && !isExplicit[file] && !claudeLooseExportZIP(file) {
					continue
				}
				info, err := os.Stat(file)
				if err != nil {
					return nil, err
				}
				id := hashBytes([]byte(file))
				identity = append(identity, fmt.Sprintf("%s:%d:%d", file, info.Size(), info.ModTime().UnixNano()))
				job.Parts = append(job.Parts, claudeImportPart{ID: id, Filename: filepath.Base(file), Category: "archive", State: "missing", Hint: file})
			}
			if len(job.Parts) == 0 {
				return nil, errors.New("choose the Claude.ai export: no manifest, ZIP or conversation files were found")
			}
			job.ID = hashBytes([]byte(strings.Join(identity, "\n")))
			job.Warnings = append(job.Warnings, "No manifest was supplied, so full export completeness cannot be verified.")
		}
		if prior, e := loadClaudeJob(config, name, job.ID); e == nil {
			job = prior
			for id, link := range loadClaudeLinks(config, job.ID) {
				links[id] = link
			}
		} else if !os.IsNotExist(e) {
			return nil, e
		}
	}
	for _, file := range selected {
		dir := filepath.Dir(file)
		found := false
		for _, known := range job.Directories {
			found = found || known == dir
		}
		if !found {
			job.Directories = append(job.Directories, dir)
		}
	}
	dir := claudeImportJobDir(config, name, job.ID)
	if err := os.MkdirAll(filepath.Join(dir, "parts"), 0700); err != nil {
		return nil, err
	}
	// An interrupted collection can leave a temporary copy, possibly one not
	// yet stripped of account records. The source lock makes this safe.
	if leftovers, err := filepath.Glob(filepath.Join(dir, "parts", ".collect-*")); err == nil {
		for _, leftover := range leftovers {
			if err := os.Remove(leftover); err != nil && !os.IsNotExist(err) {
				return nil, err
			}
		}
	}
	if err := saveClaudeLinks(config, job.ID, links); err != nil {
		return nil, err
	}
	job.State, job.Error = "collecting", ""
	if err := saveClaudeJob(config, job); err != nil {
		return nil, err
	}
	for index := 0; index < len(job.Parts); index++ {
		if err := ctx.Err(); err != nil {
			job.State = "interrupted"
			_ = saveClaudeJob(config, job)
			return job, err
		}
		part := &job.Parts[index]
		part.Download = links[part.ID] != ""
		if claudeExcludedCategory(part.Category) || claudeAccountRecord(part.Filename) || part.State == claudePartExcluded {
			// Account and sign-in records are never downloaded or kept. A
			// copy collected before this rule is removed.
			if part.Storage != "" {
				if err := os.Remove(filepath.Join(dir, filepath.FromSlash(part.Storage))); err != nil && !os.IsNotExist(err) {
					return job, err
				}
			}
			part.State, part.Error, part.Storage, part.SHA256, part.Size, part.Download = claudePartExcluded, claudeExcludedReason, "", "", 0, false
			delete(links, part.ID)
			if err := saveClaudeLinks(config, job.ID, links); err != nil {
				return job, err
			}
			if err := saveClaudeJob(config, job); err != nil {
				return job, err
			}
			continue
		}
		if part.State == "collected" {
			if err := verifyClaudeStoredPart(ctx, dir, *part); err == nil {
				priorUnits, priorHash := part.Units, part.UnitsSHA256
				part.UnitsError = ""
				if err := retainClaudeSourceUnits(ctx, config, job, part); err != nil {
					part.UnitsError = err.Error()
				}
				// A verified retry must not temporarily erase/rewrite completed group
				// metadata, changing the source fingerprint despite identical evidence.
				if priorUnits != part.Units || priorHash != part.UnitsSHA256 {
					if err := publishClaudeInventory(config, job); err != nil {
						return job, err
					}
				}
				if err := saveClaudeJob(config, job); err != nil {
					return job, err
				}
				continue
			}
			part.State, part.Error = "damaged", "The collected file changed; supply that part again."
		}
		candidates := claudePartCandidates(*job, *part, explicit)
		if len(candidates) > 1 {
			part.State, part.Error = "ambiguous", "Several files match this part. Add the intended file explicitly."
		} else if len(candidates) == 1 {
			err := collectClaudePart(ctx, config, job, index, candidates[0], links)
			if err != nil {
				job.Parts[index].State, job.Parts[index].Error = "failed", err.Error()
			}
		} else if part.State != "damaged" {
			part.State, part.Error = "missing", "Download this part in your signed-in browser, then check the folder again."
		}
		if job.Parts[index].State == "collected" {
			job.Parts[index].UnitsError = ""
			if err := retainClaudeSourceUnits(ctx, config, job, &job.Parts[index]); err != nil {
				job.Parts[index].UnitsError = err.Error()
			}
		}
		if err := saveClaudeLinks(config, job.ID, links); err != nil {
			return job, err
		}
		if err := publishClaudeInventory(config, job); err != nil {
			return job, err
		}
		if err := saveClaudeJob(config, job); err != nil {
			return job, err
		}
	}
	if err := ctx.Err(); err != nil {
		return job, err
	}
	job.State = "collected"
	for _, part := range job.Parts {
		if part.State != "collected" && part.State != claudePartExcluded {
			job.State = "incomplete"
		}
	}
	if err := prepareClaudeSupplementObservations(config, job); err != nil {
		return job, err
	}
	if err := publishClaudeInventory(config, job); err != nil {
		return job, err
	}
	indexable := false
	for _, part := range job.Parts {
		indexable = indexable || part.State == "collected" && part.IsConversation
	}
	if indexable {
		result, err := ImportSource(config, sourceImport{Kind: "claude-ai", Name: name, Path: claudeImportRoot(config, name)})
		if err != nil {
			return job, err
		}
		job.Source = &result
	}
	if err := saveClaudeJob(config, job); err != nil {
		return job, err
	}
	if err := publishClaudeSupplementHeads(config, job); err != nil {
		return job, err
	}
	return job, nil
}

func claudePartCandidates(job claudeImportJob, part claudeImportPart, selected []string) []string {
	// An explicit matching file disambiguates an old browser '(1)' copy.
	var explicit []string
	match := func(name string) bool {
		base, wanted := filepath.Base(name), filepath.Base(part.Filename)
		if base == wanted {
			return true
		}
		// Browsers number repeated downloads: Chrome "name (1).zip",
		// Firefox "name(1).zip".
		ext := filepath.Ext(wanted)
		rest, ok := strings.CutPrefix(base, strings.TrimSuffix(wanted, ext))
		if !ok || !strings.HasSuffix(rest, ")"+ext) {
			return false
		}
		number := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(rest, " "), "("), ")"+ext)
		return strings.HasPrefix(strings.TrimPrefix(rest, " "), "(") && number != "" && strings.Trim(number, "0123456789") == ""
	}
	for _, file := range selected {
		if match(file) {
			ready := true
			for _, suffix := range []string{".part", ".crdownload", ".download"} {
				if _, err := os.Stat(file + suffix); !os.IsNotExist(err) {
					ready = false
				}
			}
			if ready {
				explicit = append(explicit, file)
			}
		}
	}
	if len(explicit) > 0 {
		return explicit
	}
	seen := map[string]bool{}
	var candidates, linkWindow []string
	exported, exportedErr := time.Parse(time.RFC3339Nano, job.ExportedAt)
	consider := func(file string) {
		if seen[file] {
			return
		}
		info, err := os.Lstat(file)
		if err != nil || !info.Mode().IsRegular() {
			return
		}
		// Browser partials are never complete, including Firefox's final-name
		// placeholder while its sibling .part is still being written.
		for _, suffix := range []string{".part", ".crdownload", ".download"} {
			if _, err := os.Stat(file + suffix); !os.IsNotExist(err) {
				return
			}
		}
		if exportedErr == nil && info.ModTime().Before(exported) {
			return
		}
		seen[file] = true
		candidates = append(candidates, file)
		if exportedErr == nil && !info.ModTime().After(exported.Add(claudeDownloadWindow)) {
			linkWindow = append(linkWindow, file)
		}
	}
	consider(part.Hint)
	for _, dir := range job.Directories {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() && match(entry.Name()) {
				consider(filepath.Join(dir, entry.Name()))
			}
		}
	}
	// Repeated exports reuse filenames, so a later export's "(1)" copies also
	// postdate this one. Its links expire a day after creation: prefer files
	// that arrived while they worked, but keep a lone later copy usable.
	if len(candidates) > 1 && len(linkWindow) > 0 {
		return linkWindow
	}
	return candidates
}

// Claude's export links expire 24 hours after creation; an hour of slack
// covers clock skew and a transfer that finishes just after expiry.
var claudeDownloadWindow = 25 * time.Hour

type claudeContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r claudeContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func verifyClaudeStoredPart(ctx context.Context, dir string, part claudeImportPart) error {
	if part.Retired {
		if filepath.Base(filepath.Dir(dir)) != "snapshots" || !validRecordHash(filepath.Base(dir)) {
			return errors.New("invalid retired Claude snapshot root")
		}
		root := filepath.Dir(filepath.Dir(dir))
		asset := claudeExportAsset{SHA256: part.SHA256, UnitsSHA256: part.UnitsSHA256, Retirement: part.Retirement}
		if _, err := readClaudeRetirementProof(sourceFiles{ctx: ctx}, root, filepath.Base(dir), part.ID, asset); err != nil {
			return err
		}
		units, err := readClaudeSourceUnits(sourceFiles{ctx: ctx}, filepath.Join(dir, part.Units), part.SHA256, part.UnitsSHA256)
		if err != nil {
			return err
		}
		return verifyClaudeUnitClosure(ctx, root, units)
	}
	name, err := claudeOwnedPath(dir, part.Storage)
	if err != nil {
		return err
	}
	file, err := os.Open(name)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, claudeContextReader{ctx, io.LimitReader(file, part.Size+1)})
	if err != nil {
		return err
	}
	if n != part.Size || hex.EncodeToString(hash.Sum(nil)) != part.SHA256 {
		return errors.New("collected file no longer matches its receipt")
	}
	return nil
}

func collectClaudePart(ctx context.Context, config Config, job *claudeImportJob, index int, from string, links map[string]string) error {
	part := job.Parts[index]
	part.Retired = false
	part.Retirement = ""
	part.Conversations, part.Messages, part.IsConversation, part.Manifest = 0, 0, false, false
	dir := claudeImportJobDir(config, job.Name, job.ID)
	source, err := openClaudeImportInput(job.Directories, from)
	if err != nil {
		return err
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > claudeExportLimits.Archive {
		return errors.New("Claude part is not a regular file or exceeds the archive limit")
	}
	retained, err := claudeImportRetainedBytes(claudeImportRoot(config, job.Name))
	if err != nil {
		return err
	}
	if info.Size() > claudeImportBudget-retained {
		return errors.New("Claude collection exceeds its retained-file budget; move older snapshots out of this collection before adding more")
	}
	if err := ensureFree(dir, info.Size()); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Join(dir, "parts"), ".collect-")
	if err != nil {
		return err
	}
	defer func() { temporary.Close(); os.Remove(temporary.Name()) }()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(temporary, hash), claudeContextReader{ctx, io.LimitReader(source, info.Size()+1)})
	if err != nil {
		return err
	}
	after, err := source.Stat()
	if err != nil {
		return err
	}
	if n != info.Size() || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return errors.New("download changed during collection; wait for it to finish")
	}
	part.SHA256, part.Size, part.SourceModified = hex.EncodeToString(hash.Sum(nil)), n, formatTime(info.ModTime())
	if part.OriginalHash != "" && part.OriginalHash != part.SHA256 {
		return errors.New("this download differs from the previously collected part; use the original file or collect the new export as a separate snapshot")
	}
	part.OriginalHash = part.SHA256
	if part.ExpectedSize != nil && n != *part.ExpectedSize || part.ExpectedHash != "" && !strings.EqualFold(part.SHA256, part.ExpectedHash) {
		return errors.New("download does not match the manifest size or SHA-256")
	}
	var expandedJob *claudeImportJob
	expandedLinks := map[string]string{}
	extension := strings.ToLower(filepath.Ext(part.Filename))
	if extension == ".zip" {
		part.IsConversation, err = verifyClaudeZIP(ctx, temporary, n)
		if err != nil {
			return err
		}
		archive, err := zip.NewReader(temporary, n)
		if err != nil {
			return err
		}
		entries, err := claudeZipListing(archive)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			content, err := entry.Open()
			if err != nil {
				return err
			}
			err = decodeClaudeConversations(claudeContextReader{ctx, content}, func(c claudeAIConversation) error { part.Conversations++; part.Messages += len(c.Messages); return nil })
			closeErr := content.Close()
			if err != nil || closeErr != nil {
				return errors.Join(err, closeErr)
			}
		}
	} else if extension == ".json" {
		manifest, originalManifest, isManifest, err := readClaudeManifest(temporary.Name())
		if err != nil {
			return err
		}
		if isManifest {
			original := strings.TrimSuffix(claudeImportSecrets(config, job.ID), ".json") + "-" + part.ID + ".manifest.json"
			if err := writeFileDurably(original, originalManifest); err != nil {
				return err
			}
			if part.OriginalHash == job.ManifestHash {
				return errors.New("Claude manifest hierarchy contains a cycle")
			}
			for _, ancestor := range job.Parts {
				if ancestor.Manifest && strings.HasPrefix(part.Parent, path.Join(ancestor.Parent, ancestor.Filename)) && ancestor.OriginalHash == part.OriginalHash {
					return errors.New("Claude manifest hierarchy contains a cycle")
				}
			}
			// Parent paths include the manifest's filename to bound and retain
			// the hierarchy. Cyclic references eventually hit the depth bound.
			parent := path.Join(part.Parent, part.Filename)
			// Publish the expanded hierarchy only after the sanitized manifest
			// has been committed. A failed write must leave a retryable parent.
			expanded := *job
			expanded.Parts = append([]claudeImportPart(nil), job.Parts...)
			expanded.Warnings = append([]string(nil), job.Warnings...)
			if err := expanded.addManifest(manifest, parent, filepath.Dir(from), expandedLinks); err != nil {
				return err
			}
			expandedJob = &expanded
			part.Manifest = true
			for i := range manifest.DataFiles {
				manifest.DataFiles[i].URL = ""
			}
			content := []byte(jsonText(manifest))
			if err := temporary.Truncate(0); err != nil {
				return err
			}
			if _, err := temporary.Seek(0, io.SeekStart); err != nil {
				return err
			}
			if _, err := temporary.Write(content); err != nil {
				return err
			}
			part.SHA256, part.Size = hashBytes(content), int64(len(content))
		} else if claudeConversationName(part.Filename) || part.Category == "conversations" {
			if _, err := temporary.Seek(0, io.SeekStart); err != nil {
				return err
			}
			if err := decodeClaudeConversations(claudeContextReader{ctx, temporary}, func(c claudeAIConversation) error { part.Conversations++; part.Messages += len(c.Messages); return nil }); err != nil {
				return err
			}
			part.IsConversation = true
		}
	} else {
		return errors.New("Claude export parts must be ZIP or JSON files")
	}
	if part.Category == "conversations" && !part.IsConversation {
		return errors.New("this conversation part has no conversation data")
	}
	if extension == ".zip" {
		// Older single-ZIP exports carry account records beside the
		// conversations; the kept copy leaves them out.
		filtered, kept, err := withoutClaudeAccountRecords(ctx, temporary, n, filepath.Join(dir, "parts"))
		if err != nil {
			return err
		}
		if filtered != nil {
			original := temporary
			original.Close()
			os.Remove(original.Name())
			temporary = filtered
			if kept == 0 {
				part.State, part.Error, part.SHA256, part.Size = claudePartExcluded, claudeExcludedReason, "", 0
				job.Parts[index] = part
				return nil
			}
			hash := sha256.New()
			if _, err := temporary.Seek(0, io.SeekStart); err != nil {
				return err
			}
			size, err := io.Copy(hash, claudeContextReader{ctx, temporary})
			if err != nil {
				return err
			}
			part.SHA256, part.Size = hex.EncodeToString(hash.Sum(nil)), size
		}
	}
	if err := syncToDrive(temporary); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	part.Storage = path.Join("parts", part.ID+extension)
	if err := os.Rename(temporary.Name(), filepath.Join(dir, filepath.FromSlash(part.Storage))); err != nil {
		return err
	}
	if expandedJob != nil {
		*job = *expandedJob
		for id, link := range expandedLinks {
			links[id] = link
		}
	}
	part.State, part.Error = "collected", ""
	job.Parts[index] = part
	return nil
}

// Claude's light_metadata category holds the account's name, email address,
// phone number and sign-in history. Pharos never downloads or keeps it.
const claudePartExcluded = "excluded"
const claudeExcludedReason = "Not kept: Pharos does not keep your Claude account details or sign-in history."

func claudeExcludedCategory(category string) bool { return category == "light_metadata" }

func claudeAccountRecord(name string) bool {
	base := path.Base(name)
	return base == "users.json" || base == "login_history.json"
}

// withoutClaudeAccountRecords returns a copy of a verified ZIP without account
// records, or nil when it has none. Members are copied raw, unrecompressed.
// kept counts the remaining files; zero means nothing is worth keeping.
func withoutClaudeAccountRecords(ctx context.Context, source *os.File, size int64, dir string) (*os.File, int, error) {
	archive, err := zip.NewReader(source, size)
	if err != nil {
		return nil, 0, err
	}
	found := false
	for _, entry := range archive.File {
		found = found || claudeAccountRecord(entry.Name)
	}
	if !found {
		return nil, 0, nil
	}
	filtered, err := os.CreateTemp(dir, ".collect-")
	if err != nil {
		return nil, 0, err
	}
	kept := 0
	writer := zip.NewWriter(filtered)
	for _, entry := range archive.File {
		if claudeAccountRecord(entry.Name) {
			continue
		}
		if !entry.FileInfo().IsDir() {
			kept++
		}
		header := entry.FileHeader
		raw, err := entry.OpenRaw()
		if err == nil {
			var target io.Writer
			if target, err = writer.CreateRaw(&header); err == nil {
				_, err = io.Copy(target, claudeContextReader{ctx, raw})
			}
		}
		if err != nil {
			filtered.Close()
			os.Remove(filtered.Name())
			return nil, 0, err
		}
	}
	if err := writer.Close(); err != nil {
		filtered.Close()
		os.Remove(filtered.Name())
		return nil, 0, err
	}
	return filtered, kept, nil
}

func publishClaudeInventory(config Config, job *claudeImportJob) error {
	groups := job.Groups
	// Intermediate collection receipts can have a different available closure
	// than the last completed observation. Publish complete metadata only once
	// its membership has been recomputed at collection completion.
	if job.State != "collected" {
		groups = nil
	}
	inventory := claudeExportInventory{Groups: groups, Format: claudeInventoryFormat, CreatedAt: job.ExportedAt, Files: []claudeExportAsset{}, Missing: []string{}}
	if job.Version == 2 {
		inventory.Format = claudeObjectInventoryFormat
	}
	for _, part := range job.Parts {
		if part.State == claudePartExcluded {
			continue
		}
		if part.State != "collected" {
			inventory.Missing = append(inventory.Missing, part.Filename)
			continue
		}
		inventory.Files = append(inventory.Files, claudeExportAsset{Path: part.Storage, Filename: part.Filename, Category: part.Category, SHA256: part.SHA256, Size: part.Size, Conversations: part.IsConversation, SourceModified: part.SourceModified, Units: part.Units, UnitsSHA256: part.UnitsSHA256, Retired: part.Retired, Retirement: part.Retirement})
	}
	name := filepath.Join(claudeImportJobDir(config, job.Name, job.ID), claudeInventoryName)
	content := []byte(jsonText(inventory))
	if previous, err := os.ReadFile(name); err == nil && bytes.Equal(previous, content) {
		return nil
	}
	return writeFileDurably(name, content)
}

// OpenRoot confines manifest-relative paths even when a path component is
// replaced by a symlink between inventory and opening it. Explicitly selected
// folders are the roots of the grant; browser download URLs are never paths.
func openClaudeImportInput(directories []string, name string) (*os.File, error) {
	for _, dir := range directories {
		rel, err := filepath.Rel(dir, name)
		if err != nil || !filepath.IsLocal(rel) {
			continue
		}
		root, err := os.OpenRoot(dir)
		if err != nil {
			return nil, err
		}
		file, err := root.Open(rel)
		root.Close()
		return file, err
	}
	return nil, errors.New("Claude part is outside the selected input folders")
}

var claudeImportBudget = int64(64 << 30)

func claudeImportRetainedBytes(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(name string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("Claude collection contains a symbolic link")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	return total, err
}

// A directory can be Downloads. Only manifest-listed files, explicit files or
// recognized export layouts are collected; an unrelated ZIP beside them is not
// permission to retain unrelated data in this library.
func claudeLooseExportZIP(name string) bool {
	archive, err := zip.OpenReader(name)
	if err != nil {
		return strings.HasPrefix(filepath.Base(name), "conversations-")
	}
	defer archive.Close()
	for _, entry := range archive.File {
		if claudeConversationName(entry.Name) || entry.Name == "users.json" || entry.Name == "memories.json" || strings.HasPrefix(entry.Name, "projects/") && strings.HasSuffix(entry.Name, ".json") || strings.HasPrefix(entry.Name, "memories/") && strings.HasSuffix(entry.Name, ".json") || strings.HasPrefix(entry.Name, "artifacts/") || strings.HasPrefix(entry.Name, "claude_code_cloud/") {
			return true
		}
	}
	return false
}
