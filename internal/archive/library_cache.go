package archive

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

// Every Library table request (rows, distinct values, metrics) starts
// from the full unfiltered row set, which takes most of a second to build on a
// large catalog. libraryCache keeps the last set until anything it depends on
// may have changed:
//
//   - A change to cached-view inputs, by this process or another (ingest runs
//     as a separate CLI). Transactional triggers advance query_cache_generation;
//     publishing equivalent projections and writing progress do not invalidate it.
//   - The local date, since cost_today_usd prices usage at today's rates.
//
// Cached row maps are shared between requests and must never be modified.
// Code that adds fields to rows copies them first (see libraryPage). The rows
// hold only the fields requests have needed (see libraryFields), which keeps
// the tens of megabytes of purpose text out of memory and out of each rebuild.
// Costs are also left out until a request filters, sorts, or groups on them.
type libraryCache struct {
	mu       sync.Mutex
	versions dataVersionMonitor
	version  int64
	day      string
	fields   libraryFields
	rows     []map[string]any
	// usedAt is the last request's time, which keeps the rows warm.
	usedAt time.Time
}

// libraryUse reports when a request last read the Library rows, and the
// fields the rows hold, which requests have needed.
func (c *Catalog) libraryUse() (time.Time, libraryFields) {
	c.library.mu.Lock()
	defer c.library.mu.Unlock()
	return c.library.usedAt, c.library.fields
}

// clock returns the current time; tests replace now to change the date.
func (c *Catalog) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// cachedLibraryRows returns the unfiltered Library rows with at least fields,
// rebuilding them only after source changes, a date change, or a request for fields
// they lack. Concurrent misses wait for one rebuild.
func (c *Catalog) cachedLibraryRows(ctx context.Context, fields libraryFields) ([]map[string]any, error) {
	if err := c.ensureOrigins(ctx); err != nil {
		return nil, err
	}
	cache := &c.library
	cache.mu.Lock()
	defer cache.mu.Unlock()
	// Read the version before building: source changes during the build leave the
	// rows tagged older than they are, which only costs an extra rebuild.
	version, versionErr := cache.dataVersion(ctx, c)
	day := c.clock().Format("2006-01-02")
	current := versionErr == nil && cache.rows != nil && version == cache.version && day == cache.day
	if current && cache.fields.covers(fields) {
		return slices.Clone(cache.rows), nil
	}
	// Rebuild with the compact fields most requests need, adding long text and
	// costs only once a request uses them; keep fields still-current rows had, so requests
	// alternating between two long-text fields don't rebuild every time.
	fields = libraryCompactFields.union(fields)
	if current {
		fields = fields.union(cache.fields)
	}
	cache.rows, cache.fields = nil, fields
	rows, err := c.computeSearchRows(SearchOptions{ctx: ctx}, fields)
	if err != nil {
		return nil, err
	}
	// Without a version the rows are still correct, just not reusable.
	if versionErr == nil {
		cache.rows, cache.version, cache.day, cache.fields = rows, version, day, fields
	}
	return slices.Clone(rows), nil
}

func (cache *libraryCache) dataVersion(ctx context.Context, c *Catalog) (int64, error) {
	version, err := c.cacheVersion(ctx, &cache.versions)
	if err != nil {
		// A new monitor connection's versions cannot be compared with these rows'.
		cache.rows = nil
	}
	return version, err
}

// close drops the monitor connection and the rows tagged with its versions.
func (cache *libraryCache) close() {
	cache.versions.close()
	cache.rows = nil
}

// dataVersionMonitor reads the transactional cached-view generation on a
// dedicated read-only connection. Older catalogs fall back to data_version.
// Callers serialize access and discard cached values after a read error.
type dataVersionMonitor struct {
	monitor *sql.DB
	conn    *sql.Conn
	// version and readAt are the last read, which cacheVersion repeats during
	// a run.
	version int64
	readAt  time.Time
}

// runCacheHold is how long a cached value may outlive a change while an
// index, sync, or capture runs. Such a run commits record by record, and each
// commit would otherwise send every page back to rebuilding from scratch while
// the run competes for the drive.
const runCacheHold = 60 * time.Second

// cacheVersion is the catalog version caches compare against. While this
// process runs an ingest it repeats the version read up to runCacheHold ago,
// so caches serve what they hold, at most that stale, rather than recompute
// after every commit. A value computed meanwhile is tagged with the repeated
// version, older than its data, which costs only an extra rebuild. Once the run
// ends the next read is current. Only what pages show may come from these
// caches: work that stores results (the authorship rebuild) reads the catalog
// itself. Callers serialize access to m.
func (c *Catalog) cacheVersion(ctx context.Context, m *dataVersionMonitor) (int64, error) {
	now := c.clock()
	if c.ingestRuns.Load() > 0 && m.conn != nil && now.Sub(m.readAt) < runCacheHold {
		return m.version, nil
	}
	version, err := m.read(ctx, c.Path)
	if err == nil {
		m.version, m.readAt = version, now
	}
	return version, err
}

func (m *dataVersionMonitor) read(ctx context.Context, path string) (int64, error) {
	if m.conn == nil {
		if path == "" {
			return 0, errors.New("catalog has no file to monitor")
		}
		monitor, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path}).String()+"?mode=ro&_pragma=busy_timeout(5000)")
		if err != nil {
			return 0, err
		}
		conn, err := monitor.Conn(context.Background())
		if err != nil {
			monitor.Close()
			return 0, err
		}
		m.monitor, m.conn = monitor, conn
	}
	var version int64
	err := m.conn.QueryRowContext(ctx, "SELECT value FROM query_cache_generation WHERE id=1").Scan(&version)
	if err != nil && strings.Contains(err.Error(), "no such table") {
		err = m.conn.QueryRowContext(ctx, "PRAGMA data_version").Scan(&version)
	}
	if err != nil {
		m.close()
		return 0, err
	}
	return version, nil
}

func (m *dataVersionMonitor) close() {
	if m.conn != nil {
		m.conn.Close()
		m.monitor.Close()
	}
	m.monitor, m.conn = nil, nil
	m.version, m.readAt = 0, time.Time{}
}
