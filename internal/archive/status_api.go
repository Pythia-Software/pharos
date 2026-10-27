package archive

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// GET /api/status is the status snapshot of this library
// (docs/background-work.md §7.1): the drive and the eject verdict, the
// top-level operation tree, the attention list, the freshness ledger
// (phase 3), and the CPU and memory of its Pharos processes (phase 4). GET /api/status/stream sends
// the same document as server-sent events. Both run whatever the feature
// flag says (§9); only the surfaces that render them are gated.
//
// Every subscriber shares one computation: statusHub keeps the latest
// document and recomputes it at most once per statusCoalesce, so ten open
// pages cost what one does.

// statusCoalesce is the least time between two computed documents, and so
// between two events of a stream.
var statusCoalesce = 500 * time.Millisecond

// statusRecheck is how often a stream recomputes without a registry change:
// leases, the attention list and the verdict's lock evidence change without
// one.
var statusRecheck = 2 * time.Second

// statusKeepalive is the interval of a stream's comment line.
var statusKeepalive = 15 * time.Second

// statusDocument builds the snapshot. fresh runs the release path's full
// lock check (?fresh=1); everything else is polled.
func (s *Server) statusDocument(ctx context.Context, fresh bool) map[string]any {
	document, views := s.operationsDocumentViews(ctx, fresh)
	// Journal history is the operations endpoint's; the panel needs only
	// whether rows were dropped.
	if journal, ok := document["journal"].(map[string]any); ok {
		document["journal"] = map[string]any{"dropped": journal["dropped"]}
	}
	config := s.Config()
	drive := libraryDriveOf(config)
	if library, ok := document["library"].(map[string]any); ok {
		library["drive"] = map[string]any{"name": drive.Name, "ejectable": drive.Ejectable, "location": drive.Location, "mounted": drive.Mounted}
		library["portable"] = config.Library
		library["path"] = libraryDir(config)
	}
	document["attention"] = s.attention(ctx)
	// The freshness ledger (§5), and its one-line summary for the panel.
	freshness := s.freshness(ctx, views, fresh)
	document["freshness"] = freshness
	document["freshness_summary"] = summarizeFreshness(freshness)
	// CPU and memory of this library's Pharos processes on this Mac (§10).
	document["resources"] = s.serviceResources()
	// The words over Show all's two parts: the operations, and the domains.
	document["headings"] = map[string]any{"work": "Work", "data": "Data"}
	document["features"] = map[string]any{"unified_activity": s.Catalog.unifiedActivity(ctx)}
	return document
}

// statusTag identifies a document's content apart from when it was made:
// equal tags mean nothing a surface shows has changed. Every generated_at,
// nested ones included (each child snapshot in the broker's document has
// its own), and every resource reading's sampled_at, is left out.
func statusTag(document map[string]any) string {
	data, _ := json.Marshal(document)
	var content any
	_ = json.Unmarshal(data, &content)
	data, _ = json.Marshal(withoutGeneratedAt(content))
	sum := sha256.Sum256(data)
	return fmt.Sprintf("\"%x\"", sum[:12])
}

func withoutGeneratedAt(value any) any {
	switch value := value.(type) {
	case map[string]any:
		delete(value, "generated_at")
		delete(value, "sampled_at")
		for key, item := range value {
			value[key] = withoutGeneratedAt(item)
		}
	case []any:
		for index, item := range value {
			value[index] = withoutGeneratedAt(item)
		}
	}
	return value
}

// statusHub holds the latest polled document for every reader of a
// service's (or the broker's) snapshot.
type statusHub struct {
	mu   sync.Mutex
	tag  string
	data []byte
	// at is when the cached document's computation started, so an age
	// limit equal to a reader's period is not missed by the time the
	// computation took; published is when it was stored, which the
	// coalescing window counts from, so two documents are never published
	// less than statusCoalesce apart.
	at, published time.Time
	revision      uint64
	// flight is the computation in progress, shared by every reader that
	// arrives while it runs.
	flight *statusFlight
	// computations counts documents computed, for tests.
	computations atomic.Int64
}

type statusFlight struct {
	done     chan struct{}
	frame    statusFrame
	revision uint64
}

type statusFrame struct {
	tag  string
	data []byte
}

// statusRead is how a reader asks the hub for a frame.
type statusRead struct {
	// maxAge is the oldest cached document the reader accepts.
	maxAge time.Duration
	// deadline, when set, is the longest a reader that has a cached
	// document waits for a new one; past it the cached one is returned.
	deadline time.Duration
	revision func() uint64
	compute  func() map[string]any
	// launch, when set, runs the computation in the background, so a
	// reader past its deadline can return while it finishes; it reports
	// false to have it run on the reader's goroutine instead.
	launch func(func()) bool
}

// frame returns the latest document, computing it only when the revision
// changed since or it is older than maxAge, and never publishing two less
// than statusCoalesce apart. The computation runs outside the hub's lock,
// one at a time: a reader that arrives while one runs waits for it, and
// gives up with the cached frame when its request ends or its deadline
// passes.
func (hub *statusHub) frame(ctx context.Context, read statusRead) statusFrame {
	for {
		hub.mu.Lock()
		current := read.revision()
		cached := statusFrame{hub.tag, hub.data}
		if hub.data != nil && hub.revision == current && time.Since(hub.at) < read.maxAge {
			hub.mu.Unlock()
			return cached
		}
		flight := hub.flight
		if flight == nil {
			flight = &statusFlight{done: make(chan struct{})}
			hub.flight = flight
			published, cachedAny := hub.published, hub.data != nil
			hub.mu.Unlock()
			run := func() { hub.run(flight, published, cachedAny, read) }
			if read.launch == nil || !read.launch(run) {
				run()
			}
		} else {
			hub.mu.Unlock()
		}
		var expired <-chan time.Time
		var timer *time.Timer
		if read.deadline > 0 && cached.data != nil {
			timer = time.NewTimer(read.deadline)
			expired = timer.C
		}
		stop := func() {
			if timer != nil {
				timer.Stop()
			}
		}
		select {
		case <-flight.done:
			stop()
			if flight.frame.data != nil && flight.revision >= current {
				return flight.frame
			}
			if flight.frame.data == nil && cached.data != nil {
				return cached
			}
		case <-ctx.Done():
			stop()
			return cached
		case <-expired:
			return cached
		}
	}
}

// run is one computation: it waits out the coalescing window, computes,
// and publishes the result to the flight's waiters and the cache.
func (hub *statusHub) run(flight *statusFlight, published time.Time, cached bool, read statusRead) {
	defer func() {
		hub.mu.Lock()
		if hub.flight == flight {
			hub.flight = nil
		}
		hub.mu.Unlock()
		close(flight.done)
	}()
	if wait := statusCoalesce - time.Since(published); cached && wait > 0 {
		time.Sleep(wait)
	}
	started := time.Now()
	current := read.revision()
	hub.computations.Add(1)
	document := read.compute()
	data, _ := json.Marshal(document)
	frame := statusFrame{statusTag(document), data}
	hub.mu.Lock()
	hub.tag, hub.data, hub.at, hub.published, hub.revision = frame.tag, frame.data, started, time.Now(), current
	flight.frame, flight.revision = frame, current
	hub.mu.Unlock()
}

// statusReadDeadline is the longest a service's reader with a cached
// document waits for a new one, below the broker's 2 s fetch timeout, so a
// slow computation never makes the library look unreachable.
var statusReadDeadline = 1500 * time.Millisecond

// statusComputeTimeout bounds one computation of a service's document: a
// slow drive or probe delays readers by at most this much.
var statusComputeTimeout = 5 * time.Second

// statusCurrent is this service's latest polled document. It is computed
// on the service's lifetime, not the caller's request: the result is
// shared, and a reader that leaves must not spoil it.
func (s *Server) statusCurrent(ctx context.Context, maxAge time.Duration) statusFrame {
	return s.status.frame(ctx, statusRead{maxAge: maxAge, deadline: statusReadDeadline, revision: s.ops.currentRevision,
		compute: func() map[string]any {
			compute, cancel := context.WithTimeout(s.life.ctx, statusComputeTimeout)
			defer cancel()
			return s.statusDocument(compute, false)
		},
		// Counted with the service's workers, as any catalog reader is, so a
		// release waits for it and a vanished drive ends the process cleanly.
		launch: func(run func()) bool { return s.spawn(func(context.Context) { run() }) }})
}

// serveFrame answers a GET of a snapshot with an ETag, or 304 when the
// client's copy is current.
func serveFrame(w http.ResponseWriter, r *http.Request, frame statusFrame) {
	w.Header().Set("ETag", frame.tag)
	w.Header().Set("Cache-Control", "no-store")
	if r.Header.Get("If-None-Match") == frame.tag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(frame.data)
}

func (s *Server) serveStatus(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("fresh") == "1" {
		document := s.statusDocument(r.Context(), true)
		w.Header().Set("ETag", statusTag(document))
		writeJSON(w, document, http.StatusOK)
		return
	}
	serveFrame(w, r, s.statusCurrent(r.Context(), statusCoalesce))
}

// serveStatusStream sends the document on connect, again whenever it
// changes (at most once per statusCoalesce), and a comment line every
// statusKeepalive. It ends when the client goes or the service stops.
func (s *Server) serveStatusStream(w http.ResponseWriter, r *http.Request) {
	changes, unsubscribe := s.ops.subscribe()
	defer unsubscribe()
	streamStatus(w, r, changes, statusRecheck, func(ctx context.Context) statusFrame { return s.statusCurrent(ctx, recheckAge(statusRecheck)) })
}

// recheckAge is the age limit a stream that rechecks every period passes:
// a little under the period, so each tick computes afresh.
func recheckAge(period time.Duration) time.Duration { return period * 3 / 4 }

// streamStatus is the event loop both streams share: next returns the
// current frame, and changes wakes it early.
func streamStatus(w http.ResponseWriter, r *http.Request, changes <-chan struct{}, every time.Duration, next func(context.Context) statusFrame) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, fmt.Errorf("streaming is not supported here; poll the snapshot instead"), http.StatusNotImplemented)
		return
	}
	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-store")
	header.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	ctx := r.Context()
	sent := ""
	send := func() bool {
		frame := next(ctx)
		if ctx.Err() != nil {
			return false
		}
		if frame.tag == sent {
			return true
		}
		sent = frame.tag
		if _, err := fmt.Fprintf(w, "event: snapshot\nid: %s\ndata: %s\n\n", frame.tag, frame.data); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	if !send() {
		return
	}
	recheck := time.NewTicker(every)
	defer recheck.Stop()
	keepalive := time.NewTicker(statusKeepalive)
	defer keepalive.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-keepalive.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
			continue
		case <-changes:
		case <-recheck.C:
		}
		if !send() {
			return
		}
	}
}
