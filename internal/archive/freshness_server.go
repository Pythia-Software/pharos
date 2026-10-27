package archive

import (
	"context"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
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
	// key names the operations that change them, as last seen.
	key string
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
// snapshot's operations (this service's and other processes').
func (s *Server) freshness(ctx context.Context, views []opView) []freshnessRow {
	config := s.Config()
	in := freshnessInput{host: currentHost().ID, now: time.Now(), sources: []string{}, ops: freshnessOps(views),
		viewStopped: s.Catalog.libraryRefreshStopped(), files: s.freshnessFiles(config, views)}
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
// when they are older than freshnessFilesMaxAge or an operation that changes
// them has started or ended since.
func (s *Server) freshnessFiles(config Config, views []opView) *freshnessFiles {
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
	cache := &s.freshFiles
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.files != nil {
		age := time.Since(cache.files.at)
		if age < freshnessFilesMaxAge && (key == cache.key || age < freshnessFilesMinAge) {
			return cache.files
		}
	}
	files := readFreshnessFiles(s.Catalog, config.CaptureRoot, &config)
	cache.files, cache.key = files, key
	return files
}

// readFreshnessFiles reads backups.json (when config is known) and the
// capture manifests under root.
func readFreshnessFiles(catalog *Catalog, root string, config *Config) *freshnessFiles {
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
	rows := c.freshness(ctx, freshnessInput{host: currentHost().ID, ops: ops, files: readFreshnessFiles(c, c.captureRootPath(), nil)})
	return rows, ops
}

// journalFreshnessOps are this host's open journal rows whose process is
// still alive: the queued and running work of the service and of Terminal
// commands. Work shorter than its kind's journal threshold is not there yet.
func (c *Catalog) journalFreshnessOps(ctx context.Context) []freshnessOp {
	rows, err := c.openJournal(ctx, "")
	if err != nil {
		return []freshnessOp{}
	}
	ops := []freshnessOp{}
	for _, row := range rows {
		if !liveOperation(row.State) || row.PID <= 0 || syscall.Kill(row.PID, 0) == syscall.ESRCH {
			continue
		}
		view := foreignView(leaseInfo{}, row)
		started, _ := parseTime(row.StartedAt)
		affects := view.Spec.Affects
		if affects == nil {
			affects = parseAffects(view.Kind.Affects)
		}
		ops = append(ops, freshnessOp{ID: row.ID, Kind: row.Kind, Subject: row.Subject, Label: view.Kind.label(row.Variant), State: row.State, Affects: affects, Started: started})
	}
	return ops
}
