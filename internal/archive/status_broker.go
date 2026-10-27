package archive

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// GET /api/app/status is the broker's status document
// (docs/background-work.md §7.2): every registered library with its
// service's /api/status snapshot and the eject verdict the broker recomputes
// with its own operations, the broker's operations, app-level state, and the
// union of the libraries' attention lists plus the broker's own rule 8.
// GET /api/app/status/stream sends it as server-sent events. Like
// /api/app/operations, neither takes the broker's main lock.
//
// Unmanaged (a plain pharos serve), the service's own /api/status is the
// whole answer.

// brokerStatusRecheck is how often the broker's stream asks the libraries'
// services again. Each service shares one cached document among its readers,
// so this costs each library one cheap request per interval, whatever the
// number of pages open.
var brokerStatusRecheck = time.Second

func (b *brokerOperations) statusRevision() uint64 {
	b.mu.Lock()
	published := b.published
	b.mu.Unlock()
	return b.reg.currentRevision() + published
}

// statusDocument builds the broker's document.
func (b *brokerOperations) statusDocument(ctx context.Context) map[string]any {
	document := b.documentFrom(ctx, true, "/api/status")
	attention := []attentionItem{}
	entries, _ := document["libraries"].([]map[string]any)
	for _, entry := range entries {
		id, _ := entry["id"].(string)
		name, _ := entry["name"].(string)
		if snapshot, ok := entry["snapshot"].(map[string]any); ok {
			var items []attentionItem
			if data, err := json.Marshal(snapshot["attention"]); err == nil && json.Unmarshal(data, &items) == nil {
				for _, item := range items {
					item.Library = id
					attention = append(attention, item)
				}
			}
		}
		// Rule 8: the broker's part of "the library is unavailable".
		service, _ := entry["service"].(map[string]any)
		state, _ := service["state"].(string)
		switch {
		case entry["error"] != nil:
			attention = append(attention, attentionItem{ID: "library-unreachable", Rule: 8, Severity: "error", Library: id,
				Title: defaultString(name, "A library") + " is not answering", Detail: firstString(entry["error"]),
				Action: attentionAction{Label: "Open Libraries", Path: "/settings/libraries"}})
		case entry["selected"] == true && state == "stopped":
			attention = append(attention, attentionItem{ID: "library-unavailable", Rule: 8, Severity: "error", Library: id,
				Title: defaultString(name, "The selected library") + " is not open", Detail: "Its service is not running.",
				Action: attentionAction{Label: "Open Libraries", Path: "/settings/libraries"}})
		}
	}
	document["attention"] = attention
	return document
}

// statusCurrent is the broker's latest document, shared among its readers.
func (b *brokerOperations) statusCurrent(ctx context.Context, maxAge time.Duration) statusFrame {
	return b.status.frame(ctx, maxAge, b.statusRevision, func() map[string]any {
		// Bounded and detached from the asking request: the result is shared.
		compute, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return b.statusDocument(compute)
	})
}

func (b *brokerOperations) serveStatus(w http.ResponseWriter, r *http.Request) {
	serveFrame(w, r, b.statusCurrent(r.Context(), statusCoalesce))
}

func (b *brokerOperations) serveStatusStream(w http.ResponseWriter, r *http.Request) {
	changes, unsubscribe := b.reg.subscribe()
	defer unsubscribe()
	streamStatus(w, r, changes, brokerStatusRecheck, func(ctx context.Context) statusFrame { return b.statusCurrent(ctx, brokerStatusRecheck) })
}
