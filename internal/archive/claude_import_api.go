package archive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type claudeImportRun struct {
	mu      sync.Mutex
	Running bool   `json:"running"`
	ID      string `json:"id,omitempty"`
	Name    string `json:"name,omitempty"`
	Error   string `json:"error,omitempty"`
	cancel  context.CancelFunc
}

// Browser downloads deliberately use Claude's existing signed-in session.
// This service never follows URLs from an export or imports browser cookies.
// Only Claude's export download shape is accepted, so a planted manifest
// cannot open other claude.ai pages in a signed-in browser.
var claudeDownloadPath = regexp.MustCompile(`^/export/[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}/download/[A-Za-z0-9_-]{1,128}$`)

func safeClaudeDownloadURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" || parsed.Fragment != "" || parsed.RawQuery != "" || parsed.Opaque != "" {
		return ""
	}
	if strings.ToLower(parsed.Host) != "claude.ai" || !claudeDownloadPath.MatchString(parsed.EscapedPath()) {
		return ""
	}
	return parsed.String()
}

type claudeImportSummary struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	State     string `json:"state"`
	UpdatedAt string `json:"updated_at"`
	Collected int    `json:"collected"`
	Expected  int    `json:"expected"`
}

func listClaudeImports(config Config) ([]claudeImportSummary, error) {
	root := filepath.Join(config.CaptureRoot, ".claude-imports", currentHost().ID)
	names, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return []claudeImportSummary{}, nil
	}
	if err != nil {
		return nil, err
	}
	jobs := []claudeImportSummary{}
	for _, name := range names {
		if !name.IsDir() {
			continue
		}
		if _, err := claudeImportName(name.Name()); err != nil {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(root, name.Name(), "snapshots"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			job, err := loadClaudeJob(config, name.Name(), entry.Name())
			if err != nil {
				// A snapshot interrupted before its first save, or damaged,
				// does not hide the others.
				continue
			}
			summary := claudeImportSummary{ID: job.ID, Name: job.Name, State: job.State, UpdatedAt: job.UpdatedAt}
			for _, part := range job.Parts {
				if part.State != claudePartExcluded {
					summary.Expected++
				}
				if part.State == "collected" {
					summary.Collected++
				}
			}
			jobs = append(jobs, summary)
		}
	}
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].UpdatedAt == jobs[j].UpdatedAt {
			return jobs[i].ID < jobs[j].ID
		}
		return jobs[i].UpdatedAt > jobs[j].UpdatedAt
	})
	return jobs, nil
}

func (s *Server) getClaudeImports(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	name, err := claudeImportName(r.URL.Query().Get("name"))
	if err != nil {
		writeError(w, err, 400)
		return
	}
	if id := r.URL.Query().Get("id"); id != "" {
		job, err := loadClaudeJob(s.Config(), name, id)
		if err != nil {
			writeError(w, err, 400)
			return
		}
		writeJSON(w, job, 200)
		return
	}
	jobs, err := listClaudeImports(s.Config())
	if err != nil {
		writeError(w, err, 500)
		return
	}
	s.claudeImports.mu.Lock()
	run := map[string]any{"running": s.claudeImports.Running, "id": s.claudeImports.ID, "name": s.claudeImports.Name, "error": s.claudeImports.Error}
	s.claudeImports.mu.Unlock()
	more := len(jobs) > 30
	if more {
		jobs = jobs[:30]
	}
	writeJSON(w, map[string]any{"jobs": jobs, "run": run, "more": more, "watch": s.claudeWatchDocument()}, 200)
}

func (s *Server) postClaudeImports(w http.ResponseWriter, r *http.Request, body map[string]any) {
	w.Header().Set("Cache-Control", "no-store")
	config := s.Config()
	if !config.Library {
		writeError(w, errNotLibrary(config), 400)
		return
	}
	if r.URL.Path == "/api/claude-imports/cancel" {
		s.claudeImports.mu.Lock()
		if s.claudeImports.cancel != nil {
			s.claudeImports.cancel()
		}
		s.claudeImports.mu.Unlock()
		writeJSON(w, map[string]any{"cancelled": true}, 200)
		return
	}
	var request claudeImportRequest
	content, _ := json.Marshal(body)
	if err := json.Unmarshal(content, &request); err != nil {
		writeError(w, err, 400)
		return
	}
	name, err := claudeImportName(request.Name)
	if err != nil {
		writeError(w, err, 400)
		return
	}
	request.Name = name
	if r.URL.Path == "/api/claude-imports/links" {
		job, err := loadClaudeJob(config, name, request.ID)
		if err != nil {
			writeError(w, err, 400)
			return
		}
		allowed := map[string]bool{}
		for _, part := range job.Parts {
			if part.State != "collected" && part.State != claudePartExcluded && !claudeExcludedCategory(part.Category) {
				allowed[part.ID] = true
			}
		}
		links := loadClaudeLinks(config, request.ID)
		for id, link := range links {
			if !allowed[id] || safeClaudeDownloadURL(link) == "" {
				delete(links, id)
			}
		}
		writeJSON(w, map[string]any{"links": links}, 200)
		return
	}
	if request.ID == "" && len(request.Paths) == 0 {
		writeError(w, errors.New("choose a Claude export path"), 400)
		return
	}
	s.claudeImports.mu.Lock()
	if s.claudeImports.Running {
		s.claudeImports.mu.Unlock()
		writeError(w, errors.New("a Claude export is already being collected"), 409)
		return
	}
	if !s.acquireManualIngest() {
		s.claudeImports.mu.Unlock()
		writeError(w, errors.New("a capture or index is already running"), 409)
		return
	}
	panelCtx, cancel := context.WithCancel(s.life.ctx)
	s.claudeImports.Running, s.claudeImports.ID, s.claudeImports.Name, s.claudeImports.Error, s.claudeImports.cancel = true, request.ID, name, "", cancel
	s.claudeImports.mu.Unlock()
	started := s.start(OperationSpec{Kind: "capture.import", Variant: "claude-ai", Trigger: triggerUser}, func(opCtx context.Context, op *Op) error {
		defer s.ingestMu.Unlock()
		defer cancel()
		// Stop in Background Processing and eject cancel the operation's
		// context; the panel's own cancel still applies through ctx.
		ctx, stop := context.WithCancel(opCtx)
		defer stop()
		defer context.AfterFunc(panelCtx, stop)()
		op.Step("Collecting and verifying Claude export files", "", -1)
		job, err := collectClaudeExport(ctx, config, request)
		if job != nil && job.Source != nil {
			if reloadErr := s.reloadSources(); err == nil {
				err = reloadErr
			}
		}
		s.claudeImports.mu.Lock()
		s.claudeImports.Running, s.claudeImports.cancel = false, nil
		if job != nil {
			s.claudeImports.ID = job.ID
		}
		if err != nil {
			s.claudeImports.Error = err.Error()
		}
		s.claudeImports.mu.Unlock()
		return err
	})
	if !started {
		cancel()
		s.ingestMu.Unlock()
		s.claudeImports.mu.Lock()
		s.claudeImports.Running = false
		s.claudeImports.cancel = nil
		s.claudeImports.mu.Unlock()
		writeError(w, errServiceStopping, 503)
		return
	}
	writeJSON(w, map[string]any{"started": true}, http.StatusAccepted)
}

func (s *Server) newClaudeDownloads() *claudeDownloads {
	return &claudeDownloads{
		dir: claudeDownloadsFolder(), name: "claude-ai-imports", config: s.Config, now: time.Now,
		open: openClaudeLink,
		// Collection and indexing take the library's ingest lock without
		// cancelling a running update: a download waits safely in Downloads.
		collect: func(ctx context.Context, request claudeImportRequest) (*claudeImportJob, error) {
			if !s.ingestMu.TryLock() {
				return nil, errClaudeImportBusy
			}
			defer s.ingestMu.Unlock()
			ctx, op := s.begin(ctx, OperationSpec{Kind: "capture.import", Variant: "claude-ai", Trigger: triggerSchedule})
			op.Step("Collecting and verifying Claude export files", "", -1)
			job, err := collectClaudeExport(ctx, s.Config(), request)
			if job != nil && job.Source != nil {
				err = errors.Join(err, s.reloadSources())
			}
			op.Finish(err)
			return job, err
		},
		index: func(ctx context.Context, name string) error {
			if !s.ingestMu.TryLock() {
				return errClaudeImportBusy
			}
			defer s.ingestMu.Unlock()
			ctx, op := s.begin(ctx, OperationSpec{Kind: "capture.import", Variant: "claude-ai", Trigger: triggerSchedule})
			err := s.backgroundCapture(ctx, op, "claude-ai", []string{name})
			op.Finish(err)
			return err
		},
	}
}

// Downloads is checked rarely: a person who has just saved a manifest can ask
// for a check now. An export in progress is followed closely until it settles.
var (
	claudeDownloadsStartDelay     = 30 * time.Second
	claudeDownloadsIdleInterval   = 15 * time.Minute
	claudeDownloadsActiveInterval = 5 * time.Second
)

// claudeDownloadsLoop checks Downloads only while this Mac's person has the
// watch turned on for this library. A check that finds nothing records nothing.
func (s *Server) claudeDownloadsLoop(ctx context.Context) {
	wait := claudeDownloadsStartDelay
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		case <-s.claudeWatchWake:
		}
		wait = claudeDownloadsIdleInterval
		config := s.Config()
		if !config.Library || !loadClaudeWatchSetting(config).Enabled || !browserOpenAvailable() {
			continue
		}
		more, err := s.claudeWatch.Check(ctx)
		if err != nil && ctx.Err() == nil {
			fmt.Fprintf(os.Stderr, "Claude export watch: %v\n", err)
		}
		if more {
			wait = claudeDownloadsActiveInterval
		}
	}
}

func (s *Server) claudeWatchDocument() map[string]any {
	config := s.Config()
	setting := loadClaudeWatchSetting(config)
	s.claudeWatch.mu.Lock()
	problem := s.claudeWatch.problem
	s.claudeWatch.mu.Unlock()
	// Browser profiles are listed only once the person has turned the watch
	// on: reading another app's profile list is part of what they agreed to.
	browsers := []browserProfile{}
	if setting.Enabled {
		browsers = append(browsers, listBrowserProfiles()...)
	}
	exports := s.claudeWatch.Fetches()
	for i := range exports {
		exports[i].BrowserLabel = browserProfileLabel(exports[i].Browser, browsers)
	}
	return map[string]any{"enabled": setting.Enabled, "available": browserOpenAvailable(), "folder": s.claudeWatch.dir, "problem": problem,
		"browser": setting.Browser, "browsers": browsers, "exports": exports}
}

func claudeBrowserKnown(key string) bool {
	if key == "" {
		return true
	}
	for _, profile := range listBrowserProfiles() {
		if profile.Key == key {
			return true
		}
	}
	return false
}

// postClaudeWatch turns the Downloads watch on or off for this Mac, chooses
// where links open, asks for a check now, or applies the person's decision
// about one export.
func (s *Server) postClaudeWatch(w http.ResponseWriter, body map[string]any) {
	config := s.Config()
	if !config.Library {
		writeError(w, errNotLibrary(config), 400)
		return
	}
	browser, hasBrowser := body["browser"].(string)
	// Profiles are read only once the watch is on, as for the document.
	if hasBrowser && browser != "" && !loadClaudeWatchSetting(config).Enabled {
		writeError(w, errors.New("turn on Import Claude exports from Downloads before choosing a browser"), 400)
		return
	}
	if hasBrowser && !claudeBrowserKnown(browser) {
		writeError(w, errors.New("choose one of the listed browsers"), 400)
		return
	}
	enabled, hasEnabled := body["enabled"].(bool)
	id, _ := body["id"].(string)
	action, _ := body["action"].(string)
	switch {
	case hasEnabled || hasBrowser && id == "":
		if hasEnabled && enabled && !browserOpenAvailable() {
			writeError(w, errBrowserOpenUnsupported, 400)
			return
		}
		if _, err := updateClaudeWatchSetting(config, func(setting *claudeWatchSetting) {
			if hasEnabled {
				setting.Enabled = enabled
			}
			if hasBrowser {
				setting.Browser = browser
			}
		}); err != nil {
			writeError(w, err, 500)
			return
		}
	case action == "check":
	default:
		var choice *string
		if hasBrowser {
			choice = &browser
		}
		if err := s.claudeWatch.Act(id, action, choice); err != nil {
			writeError(w, err, 400)
			return
		}
	}
	select {
	case s.claudeWatchWake <- struct{}{}:
	default:
	}
	writeJSON(w, s.claudeWatchDocument(), 200)
}
