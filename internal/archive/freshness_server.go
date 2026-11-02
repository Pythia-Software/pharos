package archive

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// The service's and the MCP process's inputs to the freshness ledger
// (freshness.go): the operations that catch domains up, what this process
// remembers of its runs, and the file-derived inputs, cached.

// freshnessFilesMaxAge is how long the file-derived inputs are reused.
// They are re-read sooner, though not more than once per
// freshnessFilesMinAge, when an operation that changes them (a capture, an
// index of captures, a backup) starts or ends.
var (
	freshnessFilesMaxAge = time.Minute
	freshnessFilesMinAge = 2 * time.Second
)

// freshnessFileCache holds the service's latest file-derived inputs.
type freshnessFileCache struct {
	mu    sync.Mutex
	files *freshnessFiles
	// key names the operations that change them, as last seen; source the
	// capture root and backup history they were read from.
	key, source string
	// reading is true while one reader reads the files.
	reading bool
}

// liveOperation reports whether state is one an operation can still catch a
// domain up from.
func liveOperation(state string) bool {
	return state == opQueued || state == opRunning || state == opBlocked
}

// freshnessOps are the queued and running operations among views, as the
// ledger names them. Tentative daemon passes have done nothing yet.
func freshnessOps(views []opView) []freshnessOp {
	ops := []freshnessOp{}
	for _, view := range views {
		if !liveOperation(view.State) || view.Tentative || view.Kind == nil {
			continue
		}
		affects := view.Spec.Affects
		if affects == nil {
			affects = parseAffects(view.Kind.Affects)
		}
		ops = append(ops, freshnessOp{ID: string(view.ID), Kind: view.Spec.Kind, Subject: view.Spec.Subject, Label: view.Kind.label(view.Spec.Variant),
			State: view.State, Affects: affects, Started: view.Started})
	}
	return ops
}

// freshness is the ledger for this library on this Mac, with views the
// snapshot's operations (this service's and other processes'). cachedOnly
// (the release path's fresh check) never reads the library's files: it
// takes what the cache has, or leaves them unknown.
func (s *Server) freshness(ctx context.Context, views []opView, cachedOnly bool) []freshnessRow {
	config := s.Config()
	in := freshnessInput{host: currentHost().ID, now: time.Now(), sources: []string{}, ops: freshnessOps(views),
		viewStopped: s.Catalog.libraryRefreshStopped(), files: s.freshnessFiles(config, views, cachedOnly)}
	for _, source := range config.Sources {
		if source.Enabled {
			in.sources = append(in.sources, source.Name)
		}
	}
	// The latest finished index left these sources to a running capture.
	s.runsMu.RLock()
	for _, run := range s.runs {
		if run.State == "running" {
			continue
		}
		for _, result := range run.Results {
			if result.Deferred {
				if in.deferredToCapture == nil {
					in.deferredToCapture = map[string]bool{}
				}
				in.deferredToCapture[result.Source] = true
			}
		}
		break
	}
	s.runsMu.RUnlock()
	if _, runs := s.backups.status(); len(runs) > 0 && runs[0].State == "failed" {
		in.backupFailedAt = defaultString(runs[0].UpdatedAt, runs[0].StartedAt)
	}
	return s.Catalog.freshness(ctx, in)
}

// freshnessFiles returns the cached file-derived inputs, reading them again
// when they are older than freshnessFilesMaxAge, the configuration names
// other files, or an operation that changes them has started or ended since.
func (s *Server) freshnessFiles(config Config, views []opView, cachedOnly bool) *freshnessFiles {
	// The files the cache was read from.
	source := config.CaptureRoot + "\x00" + backupRecordPath(config)
	parts := []string{}
	for _, view := range views {
		if view.Tentative {
			continue
		}
		affects := view.Spec.Affects
		if affects == nil && view.Kind != nil {
			affects = parseAffects(view.Kind.Affects)
		}
		if slices.ContainsFunc(affects, func(pattern string) bool {
			return strings.HasPrefix(pattern, "capt:") || pattern == "backup.local" || pattern == "all"
		}) {
			parts = append(parts, string(view.ID)+"="+view.State)
		}
	}
	sort.Strings(parts)
	key := strings.Join(parts, ",")
	// The mutex guards only the cache's fields: the files are read outside
	// it, by one reader at a time, and everyone else (the fresh path always)
	// takes the last completed value at once.
	cache := &s.freshFiles
	cache.mu.Lock()
	sameFiles := cache.files != nil && cache.source == source
	var last *freshnessFiles
	if sameFiles {
		last = cache.files
	}
	if cachedOnly || cache.reading {
		cache.mu.Unlock()
		return last
	}
	if last != nil {
		age := time.Since(last.at)
		if age < freshnessFilesMaxAge && (key == cache.key || age < freshnessFilesMinAge) {
			cache.mu.Unlock()
			return last
		}
	}
	cache.reading = true
	cache.mu.Unlock()
	var previous []freshnessCapture
	if last != nil {
		previous = last.captures
	}
	// Cleared however the read ends, so a panic cannot freeze the cache.
	defer func() {
		cache.mu.Lock()
		cache.reading = false
		cache.mu.Unlock()
	}()
	files := readFreshnessFiles(s.Catalog, config.CaptureRoot, &config, previous)
	cache.mu.Lock()
	cache.files, cache.key, cache.source = files, key, source
	cache.mu.Unlock()
	return files
}

// countUpgradeInBackground counts the upgrade's steps for the freshness
// ledger (§5) once the service is serving, as a registered operation,
// unless the stored count is already this build's. Until it lands the
// ledger says unknown / not-counted.
func (s *Server) countUpgradeInBackground() {
	if !s.upgradeCountStale(s.life.ctx) {
		return
	}
	s.start(OperationSpec{Kind: "maintenance.upgrade-count", Trigger: triggerVersion}, func(ctx context.Context, _ *Op) error {
		s.Catalog.storeUpgradeCount(ctx)
		return nil
	})
}

// upgradeCountStale reports whether the stored count is missing or another
// build's.
func (s *Server) upgradeCountStale(ctx context.Context) bool {
	stored, _ := s.Catalog.metaValue(ctx, upgradeCountKey)
	var count upgradeCount
	return stored == "" || json.Unmarshal([]byte(stored), &count) != nil || count.Build != buildDigest()
}

// readFreshnessFiles reads backups.json (when config is known) and the
// capture manifests under root. When root cannot be read, the captures are
// previous (the last rows read) and unknown; a root that does not exist and
// had no captures has none.
func readFreshnessFiles(catalog *Catalog, root string, config *Config, previous []freshnessCapture) *freshnessFiles {
	files := &freshnessFiles{at: time.Now()}
	if config != nil {
		files.backupKnown = true
		for _, record := range loadBackupHistory(*config).Backups { // newest first
			if !record.Partial {
				files.backupAt = record.FinishedAt
				break
			}
		}
	}
	if root == "" {
		return files
	}
	hosts, err := catalog.capturedHosts(root)
	if err != nil {
		if _, statErr := os.Stat(root); !os.IsNotExist(statErr) || len(previous) > 0 {
			files.captures, files.capturesUnreadable = previous, true
		}
		return files
	}
	for _, host := range hosts {
		sources, _ := host["sources"].([]map[string]any)
		for _, source := range sources {
			files.captures = append(files.captures, freshnessCapture{host: firstString(host["id"]), hostLabel: firstString(host["label"]), source: firstString(source["name"]),
				indexedAt: firstString(source["indexed_at"]), reason: firstString(source["index_reason"]), needsIndex: source["needs_index"] == true})
		}
	}
	return files
}

// mcpFreshness is the ledger as the MCP process can see it: no
// configuration and no registry, so every source the state tables know, the
// operations other processes have open in the journal, and no local backup
// history.
func (c *Catalog) mcpFreshness(ctx context.Context) ([]freshnessRow, []freshnessOp) {
	ops := c.journalFreshnessOps(ctx)
	rows := c.freshness(ctx, freshnessInput{host: currentHost().ID, ops: ops, files: c.mcpFreshnessFiles()})
	return rows, ops
}

// mcpFiles caches the MCP process's capture manifests per catalog for
// freshnessFilesMaxAge, as the service does, so an agent polling the status
// does not read every manifest each time.
var mcpFiles sync.Map // catalog path → *freshnessFiles

func (c *Catalog) mcpFreshnessFiles() *freshnessFiles {
	root := c.captureRootPath()
	key := c.Path + "\x00" + root
	var previous []freshnessCapture
	if value, ok := mcpFiles.Load(key); ok {
		files := value.(*freshnessFiles)
		if time.Since(files.at) < freshnessFilesMaxAge {
			return files
		}
		previous = files.captures
	}
	files := readFreshnessFiles(c, root, nil, previous)
	mcpFiles.Store(key, files)
	return files
}

// journalFreshnessOps are this host's open journal rows whose process is
// still the one that wrote them: alive, and started no later than the row
// (a reused PID started after it). Without the start time it is not
// counted. Work shorter than its kind's journal threshold is not there yet.
func (c *Catalog) journalFreshnessOps(ctx context.Context) []freshnessOp {
	rows, err := c.openJournal(ctx, "")
	if err != nil {
		return []freshnessOp{}
	}
	ops := []freshnessOp{}
	for _, row := range rows {
		started, _ := parseTime(row.StartedAt)
		if !liveOperation(row.State) || row.PID <= 0 || started.IsZero() {
			continue
		}
		if processStarted, ok := processStartTime(row.PID); !ok || processStarted.After(started.Add(time.Second)) {
			continue
		}
		view := foreignView(leaseInfo{}, row)
		affects := view.Spec.Affects
		if affects == nil {
			affects = parseAffects(view.Kind.Affects)
		}
		ops = append(ops, freshnessOp{ID: row.ID, Kind: row.Kind, Subject: row.Subject, Label: view.Kind.label(row.Variant), State: row.State, Affects: affects, Started: started})
	}
	return ops
}
