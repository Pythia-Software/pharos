package archive

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"sync"
	"time"
)

// GET /api/status is the status snapshot of this library
// (docs/background-work.md §7.1): the drive and the eject verdict, the
// top-level operation tree, the attention list, and placeholders for
// freshness (phase 3) and resources (phase 4). GET /api/status/stream sends
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
	document := s.operationsDocumentChecked(ctx, fresh)
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
	// Stable keys, filled by later phases.
	document["freshness"] = nil
	document["resources"] = nil
	document["features"] = map[string]any{"unified_activity": s.Catalog.unifiedActivity(ctx)}
	return document
}

// statusTag identifies a document's content apart from when it was made:
// equal tags mean nothing a surface shows has changed.
func statusTag(document map[string]any) string {
	content := maps.Clone(document)
	delete(content, "generated_at")
	data, _ := json.Marshal(content)
	sum := sha256.Sum256(data)
	return fmt.Sprintf("\"%x\"", sum[:12])
}

// statusHub holds the latest polled document for every reader of a
// service's (or the broker's) snapshot.
type statusHub struct {
	mu       sync.Mutex
	tag      string
	data     []byte
	at       time.Time
	revision uint64
}

type statusFrame struct {
	tag  string
	data []byte
}

// frame returns the latest document, computing it only when revision
// changed since or it is older than maxAge, and never more often than once
// per statusCoalesce: a caller that arrives sooner waits for the window and
// shares the result.
func (hub *statusHub) frame(ctx context.Context, maxAge time.Duration, revision func() uint64, compute func() map[string]any) statusFrame {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	current := revision()
	if hub.data != nil && hub.revision == current && time.Since(hub.at) < maxAge {
		return statusFrame{hub.tag, hub.data}
	}
	if wait := statusCoalesce - time.Since(hub.at); hub.data != nil && wait > 0 {
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return statusFrame{hub.tag, hub.data}
		case <-timer.C:
		}
		current = revision()
	}
	document := compute()
	data, _ := json.Marshal(document)
	hub.tag, hub.data, hub.at, hub.revision = statusTag(document), data, time.Now(), current
	return statusFrame{hub.tag, hub.data}
}

// statusCurrent is this service's latest polled document. It is computed
// on the service's lifetime, not the caller's request: the result is
// shared, and a reader that leaves must not spoil it.
func (s *Server) statusCurrent(ctx context.Context, maxAge time.Duration) statusFrame {
	return s.status.frame(ctx, maxAge, s.ops.currentRevision, func() map[string]any { return s.statusDocument(s.life.ctx, false) })
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
	streamStatus(w, r, changes, statusRecheck, func(ctx context.Context) statusFrame { return s.statusCurrent(ctx, statusRecheck) })
}

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
