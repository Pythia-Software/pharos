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
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

const chatGPTImportSource = "chatgpt-imports"

var chatGPTDownloadName = regexp.MustCompile(`(?i)^[a-f0-9]{32,64}-\d{4}-\d{2}-\d{2}-\d{2}-\d{2}-\d{2}-[a-f0-9]{32}( \(\d+\))?\.zip$`)

type chatGPTImportJob struct {
	ID        string           `json:"id"`
	Path      string           `json:"path"`
	Filename  string           `json:"filename"`
	State     string           `json:"state"`
	Message   string           `json:"message,omitempty"`
	Phase     string           `json:"phase,omitempty"`
	UpdatedAt string           `json:"updated_at"`
	Snapshot  string           `json:"snapshot,omitempty"`
	Collected int              `json:"collected"`
	Expected  int              `json:"expected"`
	Excluded  []string         `json:"excluded,omitempty"`
	Ingestion *claudeIngestion `json:"ingestion,omitempty"`
	Revision  int              `json:"revision"`
}

type chatGPTImportState struct {
	Enabled bool               `json:"enabled"`
	Jobs    []chatGPTImportJob `json:"exports"`
}

type chatGPTImports struct {
	mu      sync.Mutex
	run     sync.Mutex
	dir     string
	config  func() Config
	index   func(context.Context, string, ProgressFunc) error
	collect func(context.Context, Config, string) (string, []string, error)
	working string
	stop    context.CancelFunc
	problem string
}

func chatGPTImportRoot(config Config) string {
	return filepath.Join(config.CaptureRoot, ".chatgpt-imports", currentHost().ID, chatGPTImportSource)
}
func (d *chatGPTImports) statePath() string {
	return filepath.Join(pharosSupportDir(), "chatgpt-imports", hashBytes([]byte(d.config().CaptureRoot)), "state.json")
}
func (d *chatGPTImports) load() chatGPTImportState {
	state := chatGPTImportState{Jobs: []chatGPTImportJob{}}
	data, err := os.ReadFile(d.statePath())
	if err == nil {
		_ = json.Unmarshal(data, &state)
	}
	return state
}
func (d *chatGPTImports) save(state chatGPTImportState) error {
	// Keep completed identities so old downloads cannot be rediscovered after
	// falling out of the recent UI list. The journal contains no transcript text.
	slices.SortStableFunc(state.Jobs, func(a, b chatGPTImportJob) int { return strings.Compare(b.UpdatedAt, a.UpdatedAt) })
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return writeFileAtomic(d.statePath(), data, 0600)
}

func (d *chatGPTImports) enqueue(state *chatGPTImportState, file string) error {
	info, err := os.Lstat(file)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || !strings.EqualFold(filepath.Ext(file), ".zip") {
		return errors.New("choose a completed ChatGPT export ZIP")
	}
	id := hashBytes([]byte(fmt.Sprintf("%s|%d|%d", file, info.Size(), info.ModTime().UnixNano())))
	for _, job := range state.Jobs {
		if job.ID == id {
			return nil
		}
	}
	state.Jobs = append(state.Jobs, chatGPTImportJob{ID: id, Path: file, Filename: filepath.Base(file), State: "fetching", Phase: "queued", Expected: 1, UpdatedAt: formatTime(time.Now())})
	return nil
}

func (d *chatGPTImports) act(body map[string]any) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	state := d.load()
	if enabled, ok := body["enabled"].(bool); ok {
		state.Enabled = enabled
	}
	if value := firstString(body["path"]); value != "" {
		if !strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "~/") {
			return errors.New("enter an absolute export path or one starting with ~/")
		}
		if err := d.enqueue(&state, expandPath(value)); err != nil {
			return err
		}
	}
	if action := firstString(body["action"]); action != "" && action != "check" {
		found := false
		for i := range state.Jobs {
			job := &state.Jobs[i]
			if job.ID != firstString(body["id"]) {
				continue
			}
			found = true
			switch action {
			case "stop":
				job.State, job.Message = "stopped", "Stopped. Verified files are kept."
				if d.working == job.ID && d.stop != nil {
					d.stop()
				}
			case "retry":
				job.State, job.Message, job.Phase = "fetching", "", "queued"
			default:
				return errors.New("unknown export action")
			}
			job.Revision++
			job.UpdatedAt = formatTime(time.Now())
		}
		if !found {
			return errors.New("export no longer in the recent list")
		}
	}
	return d.save(state)
}

// Check discovers only narrowly named completed exports, then advances one job.
// Decisions and progress are published without holding the mutex during IO.
func (d *chatGPTImports) Check(ctx context.Context) (bool, error) {
	d.run.Lock()
	defer d.run.Unlock()
	d.mu.Lock()
	state := d.load()
	d.problem = ""
	if state.Enabled {
		entries, err := os.ReadDir(d.dir)
		if err != nil {
			d.problem = "Pharos cannot read Downloads: " + err.Error()
		} else {
			for _, entry := range entries {
				if chatGPTDownloadName.MatchString(entry.Name()) {
					if err := d.enqueue(&state, filepath.Join(d.dir, entry.Name())); err != nil {
						d.problem = err.Error()
					}
				}
			}
		}
	}
	if !state.Enabled && len(state.Jobs) == 0 {
		d.mu.Unlock()
		return false, nil
	}
	if err := d.save(state); err != nil {
		d.mu.Unlock()
		return false, err
	}
	var job *chatGPTImportJob
	for _, item := range state.Jobs {
		if item.State == "fetching" {
			copy := item
			job = &copy
			break
		}
	}
	if job == nil {
		d.mu.Unlock()
		return false, nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	d.working, d.stop = job.ID, cancel
	d.mu.Unlock()
	progress := func(stage string, workspaces, conversations, messages, skipped, processed int) {
		d.mu.Lock()
		defer d.mu.Unlock()
		state := d.load()
		for i := range state.Jobs {
			j := &state.Jobs[i]
			if j.ID == job.ID && j.Revision == job.Revision {
				j.Ingestion = &claudeIngestion{Stage: stage, Workspaces: workspaces, Conversations: conversations, Messages: messages, Processed: processed, SkippedCurrent: skipped}
				_ = d.save(state)
			}
		}
	}
	err := d.advance(ctx, job, progress)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.working, d.stop = "", nil
	state = d.load()
	more := false
	for i := range state.Jobs {
		j := &state.Jobs[i]
		if j.ID == job.ID && j.Revision == job.Revision {
			if err != nil {
				if errors.Is(err, errClaudeImportBusy) {
					job.State, job.Phase, job.Message = "fetching", "queued", "Waiting for the library's capture or index to finish."
				} else {
					job.State, job.Message = "paused", err.Error()
				}
			}
			job.UpdatedAt = formatTime(time.Now())
			if j.Ingestion != nil {
				job.Ingestion = j.Ingestion
			}
			*j = *job
		}
		more = more || j.State == "fetching"
	}
	return more, errors.Join(err, d.save(state))
}

func (d *chatGPTImports) publish(job *chatGPTImportJob) {
	d.mu.Lock()
	defer d.mu.Unlock()
	state := d.load()
	for i := range state.Jobs {
		if state.Jobs[i].ID == job.ID && state.Jobs[i].Revision == job.Revision {
			state.Jobs[i] = *job
			_ = d.save(state)
		}
	}
}

func (d *chatGPTImports) advance(ctx context.Context, job *chatGPTImportJob, progress ProgressFunc) error {
	var snapshotErr error
	if job.Snapshot != "" {
		snapshotErr = verifyChatGPTSnapshot(ctx, d.config(), job.Snapshot)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if job.Snapshot == "" || snapshotErr != nil {
		job.Collected, job.Ingestion = 0, nil
		job.Phase = "collecting"
		d.publish(job)
		collect := d.collect
		if collect == nil {
			collect = collectChatGPTExport
		}
		file, excluded, err := collect(ctx, d.config(), job.Path)
		if err != nil {
			if snapshotErr != nil {
				return errors.Join(snapshotErr, fmt.Errorf("recollect original ChatGPT ZIP: %w", err))
			}
			return err
		}
		job.Snapshot, job.Excluded, job.Collected = file, excluded, 1
	}
	job.Phase = "indexing"
	job.Message = ""
	d.publish(job)
	if err := d.index(ctx, chatGPTImportSource, progress); err != nil {
		return err
	}
	job.State, job.Phase = "imported", "complete"
	return nil
}

// A saved path alone is not proof that collection is still usable. Recheck
// the receipt and actual retained bytes before an index-only retry.
func verifyChatGPTSnapshot(ctx context.Context, config Config, file string) error {
	root := chatGPTImportRoot(config)
	name := filepath.Base(file)
	if filepath.Dir(file) != root || filepath.Ext(name) != ".zip" {
		return errors.New("invalid saved ChatGPT snapshot path")
	}
	files := sourceFiles{ctx: ctx}
	data, err := chatGPTSidecar(files, filepath.Join(root, chatGPTInventoryName))
	if err != nil {
		return err
	}
	var inventory chatGPTInventory
	if err := json.Unmarshal(data, &inventory); err != nil {
		return err
	}
	expected := inventory.SHA256[name]
	if inventory.Format != chatGPTInventoryFormat || !slices.Contains(inventory.Files, name) || !validExportHash(expected) {
		return errors.New("saved ChatGPT snapshot has no valid collection receipt")
	}
	actual, err := chatGPTFileHash(files, file)
	if err != nil {
		return err
	}
	if actual != expected {
		return errors.New("saved ChatGPT snapshot failed its retained checksum")
	}
	return nil
}

// Collect into an immutable ZIP snapshot and a credential-free receipt. The
// original download is never modified; publication follows complete validation.
func collectChatGPTExport(ctx context.Context, config Config, from string) (string, []string, error) {
	lease, err := claudeImportLock(config, chatGPTImportSource)
	if err != nil {
		return "", nil, err
	}
	defer lease.Close()
	root := chatGPTImportRoot(config)
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", nil, err
	}
	input, err := openClaudeImportInput([]string{filepath.Dir(from)}, from)
	if err != nil {
		return "", nil, err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return "", nil, err
	}
	if info.Size() > claudeExportLimits.Archive {
		return "", nil, errors.New("ChatGPT ZIP exceeds archive size limit")
	}
	temp, err := os.CreateTemp(root, ".collect-")
	if err != nil {
		return "", nil, err
	}
	defer func() { temp.Close(); os.Remove(temp.Name()) }()
	hash := sha256.New()
	copied, err := io.Copy(io.MultiWriter(temp, hash), claudeContextReader{ctx, io.LimitReader(input, info.Size()+1)})
	if err != nil {
		return "", nil, err
	}
	if copied != info.Size() {
		return "", nil, errors.New("the export changed while being collected; wait for the download to finish")
	}
	after, err := input.Stat()
	if err != nil || after.Size() != info.Size() || after.ModTime() != info.ModTime() {
		return "", nil, errors.New("the export changed while being collected; try again after the download finishes")
	}
	z, err := zip.NewReader(temp, info.Size())
	if err != nil {
		return "", nil, err
	}
	if err := verifyChatGPTZIP(ctx, z); err != nil {
		return "", nil, err
	}
	// Parse all conversations before replacing any existing catalog history.
	for _, f := range z.File {
		if len(chatGPTConversationFiles([]string{f.Name})) == 0 {
			continue
		}
		in, err := f.Open()
		if err != nil {
			return "", nil, err
		}
		adapter := &chatGPTAdapter{baseAdapter: baseAdapter{ctx: ctx}}
		err = adapter.decode(in, f.Name, f.Name, 0, func(WorkspaceRecord) error { return ctx.Err() })
		in.Close()
		if err != nil {
			return "", nil, err
		}
	}
	excluded := []string{}
	filtered, err := os.CreateTemp(root, ".filtered-")
	if err != nil {
		return "", nil, err
	}
	defer func() { filtered.Close(); os.Remove(filtered.Name()) }()
	filteredHash := sha256.New()
	writer := zip.NewWriter(io.MultiWriter(filtered, filteredHash))
	for _, f := range z.File {
		if chatGPTExcluded(f.Name) {
			excluded = append(excluded, f.Name)
			continue
		}
		if err := ctx.Err(); err != nil {
			writer.Close()
			return "", nil, err
		}
		if err := writer.Copy(f); err != nil {
			writer.Close()
			return "", nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return "", nil, err
	}
	if err := syncToDrive(filtered); err != nil {
		return "", nil, err
	}
	if err := filtered.Close(); err != nil {
		return "", nil, err
	}
	name := hex.EncodeToString(hash.Sum(nil)) + ".zip"
	destination := filepath.Join(root, name)
	// Re-downloading an existing export must not promote its observation time
	// above a newer snapshot. Only publish missing or corrupt retained bytes.
	retainedHash, retainedErr := chatGPTFileHash(sourceFiles{ctx: ctx}, destination)
	if retainedErr != nil && !errors.Is(retainedErr, os.ErrNotExist) {
		return "", nil, retainedErr
	}
	if retainedHash != hex.EncodeToString(filteredHash.Sum(nil)) {
		if err := os.Rename(filtered.Name(), destination); err != nil {
			return "", nil, err
		}
		if err := os.Chtimes(destination, info.ModTime(), info.ModTime()); err != nil {
			return "", nil, err
		}
	}
	inventory := chatGPTInventory{Format: chatGPTInventoryFormat, Files: []string{}}
	if data, err := os.ReadFile(filepath.Join(root, chatGPTInventoryName)); err == nil {
		if err := json.Unmarshal(data, &inventory); err != nil {
			return "", nil, err
		}
	}
	if inventory.Format != chatGPTInventoryFormat {
		return "", nil, errors.New("invalid ChatGPT collection receipt")
	}
	if inventory.SHA256 == nil {
		inventory.SHA256 = map[string]string{}
	}
	if inventory.Excluded == nil {
		inventory.Excluded = map[string][]string{}
	}
	for _, entry := range inventory.Files {
		if !claudeExportPath(entry) || filepath.Ext(entry) != ".zip" {
			return "", nil, errors.New("invalid ChatGPT snapshot path")
		}
	}
	inventory.SHA256[name] = hex.EncodeToString(filteredHash.Sum(nil))
	slices.Sort(excluded)
	inventory.Excluded[name] = excluded
	if !slices.Contains(inventory.Files, name) {
		inventory.Files = append(inventory.Files, name)
	}
	slices.SortFunc(inventory.Files, func(a, b string) int {
		ai, ae := os.Stat(filepath.Join(root, a))
		bi, be := os.Stat(filepath.Join(root, b))
		if ae == nil && be == nil && !ai.ModTime().Equal(bi.ModTime()) {
			return ai.ModTime().Compare(bi.ModTime())
		}
		return strings.Compare(a, b)
	})
	data, _ := json.Marshal(inventory)
	receipt := filepath.Join(root, chatGPTInventoryName)
	if previous, err := os.ReadFile(receipt); err != nil || !bytes.Equal(previous, data) {
		if err := writeFileAtomic(receipt, data, 0600); err != nil {
			return "", nil, err
		}
	}
	_, err = ImportSource(config, sourceImport{Kind: "chatgpt", Name: chatGPTImportSource, Path: root})
	return destination, excluded, err
}

func (s *Server) newChatGPTImports() *chatGPTImports {
	return &chatGPTImports{dir: claudeDownloadsFolder(), config: s.Config,
		collect: func(ctx context.Context, config Config, file string) (string, []string, error) {
			if !s.ingestMu.TryLock() {
				return "", nil, errClaudeImportBusy
			}
			defer s.ingestMu.Unlock()
			ctx, op := s.begin(ctx, OperationSpec{Kind: "capture.import", Variant: "chatgpt", Trigger: triggerUser})
			op.Step("Collecting and verifying ChatGPT export", "", -1)
			snapshot, excluded, err := collectChatGPTExport(ctx, config, file)
			op.Finish(err)
			return snapshot, excluded, err
		}, index: func(ctx context.Context, name string, progress ProgressFunc) error {
			if !s.ingestMu.TryLock() {
				return errClaudeImportBusy
			}
			defer s.ingestMu.Unlock()
			if err := s.reloadSources(); err != nil {
				return err
			}
			ctx, op := s.begin(ctx, OperationSpec{Kind: "capture.import", Variant: "chatgpt", Trigger: triggerUser})
			err := s.backgroundCaptureProgress(ctx, op, "chatgpt", []string{name}, progress)
			op.Finish(err)
			return err
		}}
}

func (s *Server) chatGPTImportsLoop(ctx context.Context) {
	wait := claudeDownloadsStartDelay
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		case <-s.chatGPTWatchWake:
		}
		wait = claudeDownloadsIdleInterval
		if !s.Config().Library {
			continue
		}
		more, _ := s.chatGPTImports.Check(ctx)
		if more {
			wait = claudeDownloadsActiveInterval
		}
	}
}

func (s *Server) chatGPTImportsDocument() map[string]any {
	d := s.chatGPTImports
	d.mu.Lock()
	state := d.load()
	problem := d.problem
	d.mu.Unlock()
	library := claudeImportLibrary{Recent: []claudeImportWorkspace{}}
	err := s.Catalog.reader().QueryRow(`SELECT COUNT(DISTINCT w.id),COUNT(DISTINCT c.id) FROM workspace_sightings s JOIN workspaces w ON w.id=s.workspace_id LEFT JOIN conversations c ON c.workspace_id=w.id WHERE s.host_id=? AND s.source_name=?`, currentHost().ID, chatGPTImportSource).Scan(&library.Workspaces, &library.Conversations)
	if err != nil {
		library.Error = "Library status is temporarily unavailable."
	} else {
		rows, _ := queryMaps(s.Catalog.reader(), `SELECT w.id,w.title FROM workspace_sightings s JOIN workspaces w ON w.id=s.workspace_id WHERE s.host_id=? AND s.source_name=? ORDER BY w.activity_at DESC,w.id LIMIT 5`, currentHost().ID, chatGPTImportSource)
		for _, r := range rows {
			library.Recent = append(library.Recent, claudeImportWorkspace{ID: firstString(r["id"]), Title: firstString(r["title"]), URL: "/work/" + firstString(r["id"])})
		}
	}
	return map[string]any{"enabled": state.Enabled, "available": true, "folder": d.dir, "exports": state.Jobs, "problem": problem, "library": library}
}

func (s *Server) postChatGPTImports(w http.ResponseWriter, body map[string]any) {
	if !s.Config().Library {
		writeError(w, errNotLibrary(s.Config()), 400)
		return
	}
	if err := s.chatGPTImports.act(body); err != nil {
		writeError(w, err, 400)
		return
	}
	select {
	case s.chatGPTWatchWake <- struct{}{}:
	default:
	}
	writeJSON(w, s.chatGPTImportsDocument(), 200)
}
