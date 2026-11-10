package archive

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// The result cache is a small SQLite file per library, in this Mac's support
// directory rather than beside the catalog, holding responses in the form
// the frontend receives them. A request that names a policy is answered
// from it, or computed and stored in it; requests that name none never touch
// it. docs/result-cache.md states the rules; this file is all of them.
//
// A domain is one kind of response (resultDomain). Adding one is a
// registerResultDomain call and a handler that calls serveResult.

// Request header naming the policy, and response header naming what was
// served: "fresh" or "stale" from the cache, "live" computed for this request.
const resultCacheHeader = "X-Pharos-Cache"

// Response header: when the served body was computed (RFC 3339).
const resultCacheBuiltHeader = "X-Pharos-Cache-Built"

const (
	// resultCached serves whatever is stored, fresh or stale, and computes
	// only when nothing is: for content that is not being looked at closely.
	resultCached = "cached"
	// resultFresh serves a stored body only while it is fresh, otherwise
	// computes; under load, or when computing fails, it serves the stale one.
	resultFresh = "fresh"
	// resultLive always computes: an explicit refresh.
	resultLive = "live"
)

var (
	// resultCacheSynchronous is the file's durability: a lost cache is rebuilt.
	resultCacheSynchronous = "NORMAL"
	// resultCacheBytes bounds the stored bodies; the least recently used go first.
	resultCacheBytes int64 = 256 << 20
	// resultCacheEntryBytes is the largest body stored.
	resultCacheEntryBytes = 16 << 20
	// resultCacheUnused is how long an entry nobody asks for is kept.
	resultCacheUnused = 14 * 24 * time.Hour
	// resultRefreshRecent bounds the idle sweep to entries asked for lately.
	resultRefreshRecent = 3 * 24 * time.Hour
	// resultReturnAfter is how long after an entry was first stored a
	// request for it counts as coming back to it. Only entries someone came
	// back to are swept: a filter tried once is stored, and refreshed if it
	// is asked for again, but creates no standing background work.
	resultReturnAfter = time.Hour
	// resultRefreshPause is how long the refresher waits for the service to
	// be idle again, and resultSweepEvery how often it looks for stale
	// entries nobody has asked for since they went stale.
	resultRefreshPause = 250 * time.Millisecond
	resultSweepEvery   = 2 * time.Minute
	// resultBuildDeadline bounds one background refresh.
	resultBuildDeadline = 2 * time.Minute
)

// resultDomain is one kind of cached response.
type resultDomain struct {
	name string
	// version changes when the response's shape or meaning does: entries
	// stored under another version are not served.
	version string
	// maxAge is how long an entry whose request reads the clock (a window
	// ending "now") stays fresh while its data does not change.
	maxAge time.Duration
	// stamp names the state of the data a response in scope is computed
	// from. An entry is fresh while its stamp is the current one. An error
	// means the state cannot be named, and the request is answered live.
	stamp func(ctx context.Context, s *Server, scope string) (string, error)
	// build computes the response for a stored request, as of now.
	build func(ctx context.Context, s *Server, replay []byte, now time.Time) ([]byte, error)
	// fail writes a build's error as this kind of response reports errors.
	fail func(w http.ResponseWriter, err error)
}

var resultDomains = map[string]*resultDomain{}

func registerResultDomain(d *resultDomain) *resultDomain {
	resultDomains[d.name] = d
	return d
}

// resultRequest is a request in the form a domain stores it.
type resultRequest struct {
	// replay is everything build needs, without the request's clock; the
	// entry's key is its hash, so equal requests share an entry.
	replay []byte
	// scope is the part of the data the response reads (stamp's argument).
	scope string
	// clock is whether the response depends on when it is computed.
	clock bool
	// now is this request's own clock, for a build made while answering it.
	now time.Time
}

func (r resultRequest) key() string {
	sum := sha256.Sum256(r.replay)
	return hex.EncodeToString(sum[:])
}

type resultEntry struct {
	domain, key, version, scope, stamp string
	clock                              bool
	replay, body                       []byte
	builtAt, usedAt, firstAt           time.Time
	returned                           bool
}

// state is "fresh", "stale", or "" for an entry that cannot be served.
func (d *resultDomain) state(e *resultEntry, stamp string, now time.Time) string {
	switch {
	case e == nil || e.version != d.version:
		return ""
	case e.stamp != stamp || e.clock && now.Sub(e.builtAt) > d.maxAge:
		return "stale"
	}
	return "fresh"
}

type resultKey struct{ domain, key string }

// resultCache is the store and the queue of entries to refresh.
type resultCache struct {
	// identity is the library the file's entries belong to.
	identity string
	path     string
	open     sync.Once
	db       *sql.DB
	err      error
	shut     atomic.Bool

	mu sync.Mutex
	// asked and swept are the stale entries to refresh, next first: those a
	// request was just answered with, in the order they were asked for (a
	// page asks for what is on screen first), then those the sweep found.
	asked, swept []resultKey
	queued       map[resultKey]bool // true: in asked
	wake         chan struct{}
	stores       int
	// missing holds requests whose build was abandoned before anything was
	// stored (the browser gave up), for the refresher to build: without an
	// entry there is nothing else to refresh from.
	missing map[resultKey]*resultEntry
}

// resultMissingLimit bounds the abandoned requests remembered at once.
var resultMissingLimit = 256

// resultCacheLayout is the file's table layout; a file of another layout
// has its entries dropped, since a cache is always safe to lose.
const resultCacheLayout = 2

const resultCacheSchema = `CREATE TABLE IF NOT EXISTS entries (
 domain TEXT NOT NULL, key TEXT NOT NULL, version TEXT NOT NULL, scope TEXT NOT NULL, stamp TEXT NOT NULL,
 clock INTEGER NOT NULL, replay BLOB NOT NULL, body BLOB NOT NULL,
 built_at INTEGER NOT NULL, used_at INTEGER NOT NULL, bytes INTEGER NOT NULL,
 first_at INTEGER NOT NULL, returned INTEGER NOT NULL DEFAULT 0,
 UNIQUE(domain,key));
CREATE INDEX IF NOT EXISTS entries_used_idx ON entries(used_at);
CREATE TABLE IF NOT EXISTS library (id INTEGER PRIMARY KEY CHECK(id=1), identity TEXT NOT NULL)`

// resultCacheIdentity names the library whose responses a cache file holds:
// the library's id and instance (a restore makes a new instance), which
// follow the library wherever its drive is mounted and tell apart two
// libraries opened at one path. A catalog that is no library (a development
// or test catalog) is known by its path. An empty identity means the library
// cannot be identified now, and nothing is cached.
func resultCacheIdentity(config Config) string {
	switch d, err := readLibraryDescriptor(config); {
	case err == nil:
		return "library:" + d.ID + ":" + d.Instance
	case libraryDescriptorMissing(err):
		return "catalog:" + canonical(config.CatalogPath)
	}
	return ""
}

// resultCachePath is the file for one identity, on this Mac rather than the
// library's drive, so an ejected, read-only or slow drive is none of its
// concern. The file also records the identity (see results).
func resultCachePath(identity string) string {
	sum := sha256.Sum256([]byte(identity))
	return filepath.Join(pharosSupportDir(), "result-cache", hex.EncodeToString(sum[:8])+".sqlite3")
}

// results is the result cache of the library this service serves, or nil
// when it cannot be identified or its file cannot be opened: every caller
// then answers as if no policy had been named. A file that turns out to
// belong to another library is emptied before use.
func (s *Server) results() *resultCache {
	c := s.Catalog
	c.resultsOnce.Do(func() {
		identity := resultCacheIdentity(s.config)
		c.resultStore = &resultCache{identity: identity, path: resultCachePath(identity), queued: map[resultKey]bool{}, missing: map[resultKey]*resultEntry{}, wake: make(chan struct{}, 1)}
	})
	cache := c.resultStore
	if cache.identity == "" {
		return nil
	}
	cache.open.Do(func() {
		if cache.err = os.MkdirAll(filepath.Dir(cache.path), 0o700); cache.err != nil {
			return
		}
		dsn := (&url.URL{Scheme: "file", Path: cache.path}).String() + "?_pragma=journal_mode(WAL)&_pragma=synchronous(" + resultCacheSynchronous + ")&_pragma=busy_timeout(2000)"
		db, err := sql.Open("sqlite", dsn)
		if err == nil {
			db.SetMaxOpenConns(4)
			var layout int
			if err = db.QueryRow(`PRAGMA user_version`).Scan(&layout); err == nil && layout != resultCacheLayout {
				_, err = db.Exec(fmt.Sprintf(`DROP TABLE IF EXISTS entries; PRAGMA user_version=%d`, resultCacheLayout))
			}
			if err != nil {
			} else if _, err = db.Exec(resultCacheSchema); err == nil {
				_, err = db.Exec(`DELETE FROM entries WHERE NOT EXISTS(SELECT 1 FROM library WHERE identity=?);
 INSERT INTO library(id,identity) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET identity=excluded.identity`, cache.identity, cache.identity)
			}
			if err != nil {
				db.Close()
			}
		}
		cache.db, cache.err = db, err
	})
	if cache.err != nil || cache.shut.Load() {
		return nil
	}
	return cache
}

func (c *Catalog) closeResults() {
	if cache := c.resultStore; cache != nil && cache.db != nil && !cache.shut.Swap(true) {
		_ = cache.db.Close()
	}
}

func (cache *resultCache) get(ctx context.Context, domain, key string) *resultEntry {
	return cache.read(ctx, domain, key, "body")
}

// request is an entry without its body: what a rebuild needs.
func (cache *resultCache) request(ctx context.Context, domain, key string) *resultEntry {
	return cache.read(ctx, domain, key, "NULL")
}

func (cache *resultCache) read(ctx context.Context, domain, key, body string) *resultEntry {
	e := &resultEntry{domain: domain, key: key}
	var built, used, first int64
	err := cache.db.QueryRowContext(ctx, `SELECT version,scope,stamp,clock,replay,`+body+`,built_at,used_at,first_at,returned FROM entries WHERE domain=? AND key=?`, domain, key).
		Scan(&e.version, &e.scope, &e.stamp, &e.clock, &e.replay, &e.body, &built, &used, &first, &e.returned)
	if err != nil {
		return nil
	}
	e.builtAt, e.usedAt, e.firstAt = time.UnixMilli(built), time.UnixMilli(used), time.UnixMilli(first)
	return e
}

// put stores e, replacing its predecessor. A failure loses only the entry.
func (cache *resultCache) put(e *resultEntry) {
	if len(e.body) > resultCacheEntryBytes {
		return
	}
	now := time.Now().UnixMilli()
	_, err := cache.db.Exec(`INSERT INTO entries(domain,key,version,scope,stamp,clock,replay,body,built_at,used_at,bytes,first_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
 ON CONFLICT(domain,key) DO UPDATE SET version=excluded.version,scope=excluded.scope,stamp=excluded.stamp,clock=excluded.clock,
 replay=excluded.replay,body=excluded.body,built_at=excluded.built_at,bytes=excluded.bytes`,
		e.domain, e.key, e.version, e.scope, e.stamp, e.clock, e.replay, e.body, e.builtAt.UnixMilli(), now, len(e.body)+len(e.replay), now)
	if err != nil {
		return
	}
	cache.mu.Lock()
	cache.stores++
	trim := cache.stores%64 == 1
	cache.mu.Unlock()
	if trim {
		cache.trim()
	}
}

// used records that e was asked for, at most once a minute, and whether
// someone has come back to it (resultReturnAfter).
func (cache *resultCache) used(e *resultEntry) {
	now := time.Now()
	returned := e.returned || now.Sub(e.firstAt) >= resultReturnAfter
	if now.Sub(e.usedAt) > time.Minute || returned != e.returned {
		_, _ = cache.db.Exec(`UPDATE entries SET used_at=?, returned=? WHERE domain=? AND key=?`, now.UnixMilli(), returned, e.domain, e.key)
	}
}

// trim drops entries nobody has asked for in resultCacheUnused, then the
// least recently used until the bodies fit resultCacheBytes.
func (cache *resultCache) trim() {
	_, _ = cache.db.Exec(`DELETE FROM entries WHERE used_at<?`, time.Now().Add(-resultCacheUnused).UnixMilli())
	for range 64 {
		var total sql.NullInt64
		if err := cache.db.QueryRow(`SELECT SUM(bytes) FROM entries`).Scan(&total); err != nil || total.Int64 <= resultCacheBytes {
			return
		}
		if _, err := cache.db.Exec(`DELETE FROM entries WHERE rowid IN (SELECT rowid FROM entries ORDER BY used_at LIMIT 32)`); err != nil {
			return
		}
	}
}

// refreshSoon queues a stale entry for the refresher: after the others a
// request was answered with when asked is set, after everything otherwise.
func (cache *resultCache) refreshSoon(domain, key string, asked bool) {
	id := resultKey{domain, key}
	cache.mu.Lock()
	if wasAsked, queued := cache.queued[id]; !queued {
		cache.queued[id] = asked
		if asked {
			cache.asked = append(cache.asked, id)
		} else {
			cache.swept = append(cache.swept, id)
		}
	} else if asked && !wasAsked {
		cache.queued[id] = true
		cache.swept = slices.DeleteFunc(cache.swept, func(queued resultKey) bool { return queued == id })
		cache.asked = append(cache.asked, id)
	}
	cache.mu.Unlock()
	cache.signal()
}

// buildSoon queues a request that has no entry, as refreshSoon queues one
// that has.
func (cache *resultCache) buildSoon(e *resultEntry) {
	id := resultKey{e.domain, e.key}
	cache.mu.Lock()
	if _, known := cache.missing[id]; !known && len(cache.missing) >= resultMissingLimit {
		cache.mu.Unlock()
		return
	}
	cache.missing[id] = e
	cache.mu.Unlock()
	cache.refreshSoon(e.domain, e.key, true)
}

// woke passes a wake-up on while entries wait: one may stand for many, and
// there is another refresher to share them with. With nothing queued it
// passes nothing, so idle refreshers sleep.
func (cache *resultCache) woke() {
	cache.mu.Lock()
	queued := len(cache.asked)+len(cache.swept) > 0
	cache.mu.Unlock()
	if queued {
		cache.signal()
	}
}

func (cache *resultCache) signal() {
	select {
	case cache.wake <- struct{}{}:
	default:
	}
}

func (cache *resultCache) nextRefresh() (resultKey, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	queue := &cache.asked
	if len(*queue) == 0 {
		queue = &cache.swept
	}
	if len(*queue) == 0 {
		return resultKey{}, false
	}
	id := (*queue)[0]
	*queue = (*queue)[1:]
	delete(cache.queued, id)
	return id, true
}

func writeCachedResult(w http.ResponseWriter, body []byte, state string, built time.Time) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set(resultCacheHeader, state)
	w.Header().Set(resultCacheBuiltHeader, built.UTC().Format(time.RFC3339))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// serveResult answers r with domain d's response to request, by the policy
// r names:
//
//	stored entry   cached            fresh                       live
//	fresh          serve it          serve it                    compute
//	stale          serve it, queue   compute; if the service is  compute
//	               a refresh         busy or computing fails,
//	                                 serve it and queue a refresh
//	none           compute           compute                     compute
//
// Whatever is computed is stored, after the response is written. No policy,
// or no usable cache, computes and stores nothing.
func (s *Server) serveResult(w http.ResponseWriter, r *http.Request, d *resultDomain, request resultRequest) {
	ctx, policy := r.Context(), r.Header.Get(resultCacheHeader)
	var cache *resultCache
	var stamp string
	if policy == resultCached || policy == resultFresh || policy == resultLive {
		if cache = s.results(); cache != nil {
			// Read before computing: a change during the build leaves the
			// entry stale rather than stamped with data it did not read.
			var err error
			if stamp, err = d.stamp(ctx, s, request.scope); err != nil {
				cache = nil
			}
		}
	}
	if cache == nil {
		body, err := d.build(ctx, s, request.replay, request.now)
		if err != nil {
			d.fail(w, err)
			return
		}
		writeCachedResult(w, body, resultLive, time.Now())
		return
	}
	key := request.key()
	var entry *resultEntry
	if policy != resultLive {
		entry = cache.get(ctx, d.name, key)
	}
	state := d.state(entry, stamp, time.Now())
	serve := func() {
		writeCachedResult(w, entry.body, state, entry.builtAt)
		cache.used(entry)
		if state == "stale" {
			cache.refreshSoon(d.name, key, true)
		}
	}
	if state == "fresh" || state == "stale" && (policy == resultCached || heavyQueries.busy()) {
		serve()
		return
	}
	built := time.Now()
	body, err := d.build(ctx, s, request.replay, request.now)
	if err != nil {
		if ctx.Err() != nil {
			// Whoever asked has gone, and took the build with them. It is
			// built when there is capacity, so that the next load of the
			// same view has an answer however long the query takes.
			if state == "stale" {
				cache.refreshSoon(d.name, key, true)
			} else {
				cache.buildSoon(&resultEntry{domain: d.name, key: key, scope: request.scope, clock: request.clock, replay: request.replay})
			}
		}
		if state == "stale" {
			serve()
		} else {
			d.fail(w, err)
		}
		return
	}
	writeCachedResult(w, body, resultLive, built)
	cache.put(&resultEntry{domain: d.name, key: key, version: d.version, scope: request.scope, stamp: stamp, clock: request.clock,
		replay: request.replay, body: body, builtAt: built})
}

// refreshResult recomputes one queued entry if it is still stale, and
// reports whether there was one to look at.
func (s *Server) refreshResult(ctx context.Context) bool {
	cache := s.results()
	if cache == nil {
		return false
	}
	id, ok := cache.nextRefresh()
	if !ok {
		return false
	}
	d := resultDomains[id.domain]
	cache.mu.Lock()
	missing := cache.missing[id]
	delete(cache.missing, id)
	cache.mu.Unlock()
	if d == nil {
		return true
	}
	// The body is not needed to rebuild it, and may be large.
	entry := cache.request(ctx, id.domain, id.key)
	if entry == nil {
		entry = missing
	}
	if entry == nil {
		return true
	}
	stamp, err := d.stamp(ctx, s, entry.scope)
	// An entry is rebuilt when it is stale, was stored by another version
	// of its domain, or was never stored at all.
	if err != nil || entry.version == d.version && d.state(entry, stamp, time.Now()) != "stale" {
		return true
	}
	build, cancel := context.WithTimeout(backgroundQuery(ctx), resultBuildDeadline)
	defer cancel()
	built := time.Now()
	body, err := d.build(build, s, entry.replay, time.Time{})
	if err != nil {
		return true // the stale entry stays; a request for it queues it again
	}
	entry.version, entry.stamp, entry.body, entry.builtAt = d.version, stamp, body, built
	cache.put(entry)
	return true
}

// sweepResults queues the stale entries asked for lately that no request
// has queued: what a page will want the next time it is opened.
func (s *Server) sweepResults(ctx context.Context) {
	cache := s.results()
	if cache == nil {
		return
	}
	rows, err := cache.db.QueryContext(ctx, `SELECT domain,key,version,scope,stamp,clock,built_at FROM entries WHERE returned AND used_at>=? ORDER BY used_at DESC LIMIT 1000`,
		time.Now().Add(-resultRefreshRecent).UnixMilli())
	if err != nil {
		return
	}
	var found []resultEntry
	for rows.Next() {
		var e resultEntry
		var built int64
		if rows.Scan(&e.domain, &e.key, &e.version, &e.scope, &e.stamp, &e.clock, &built) == nil {
			e.builtAt = time.UnixMilli(built)
			found = append(found, e)
		}
	}
	rows.Close()
	stamps := map[[2]string]string{}
	for _, e := range found {
		d := resultDomains[e.domain]
		if d == nil {
			continue
		}
		at := [2]string{e.domain, e.scope}
		if _, read := stamps[at]; !read {
			if stamps[at], err = d.stamp(ctx, s, e.scope); err != nil {
				return
			}
		}
		// An entry of another version is rebuilt too: its request is still good.
		if e.version != d.version || d.state(&e, stamps[at], time.Now()) == "stale" {
			cache.refreshSoon(e.domain, e.key, false)
		}
	}
}

// resultRefreshers is how many entries are recomputed at once: fewer than
// the heavy-query slots, so a page that opens meanwhile finds one free.
const resultRefreshers = 2

// refreshResults is one refresher: it recomputes stale entries, and starts
// one only while no interactive query is running or waiting, so the cache
// catches up when there is capacity and never competes with a page. The
// first of the resultRefreshers also sweeps.
func (s *Server) refreshResults(ctx context.Context, sweeps bool) {
	cache := s.results()
	if cache == nil {
		return
	}
	// A refresh marks its own queries as background, so what is counted
	// here is only the work someone is waiting for.
	free := func() bool { return heavyQueries.interactive() == 0 }
	var sweep *time.Timer
	if sweeps {
		sweep = time.NewTimer(0)
		defer sweep.Stop()
	}
	s.refreshResultsWhile(ctx, cache, free, sweep)
}

func (s *Server) refreshResultsWhile(ctx context.Context, cache *resultCache, free func() bool, sweep *time.Timer) {
	var swept <-chan time.Time
	if sweep != nil {
		swept = sweep.C
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-cache.wake:
			cache.woke()
		case <-swept:
			s.sweepResults(ctx)
			sweep.Reset(resultSweepEvery)
		}
		for ctx.Err() == nil {
			if !free() {
				select {
				case <-ctx.Done():
				case <-time.After(resultRefreshPause):
				}
				continue
			}
			if !s.refreshResult(ctx) {
				break
			}
		}
	}
}
