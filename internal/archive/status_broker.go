package archive

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
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
	return b.statusDocumentChecked(ctx, false)
}

// statusDocumentChecked builds it; fresh (?fresh=1, the panel's Eject in
// the app) asks every library's service for its fresh verdict, at this
// moment, and leaves rule 8 out: the eject check needs only verdicts.
func (b *brokerOperations) statusDocumentChecked(ctx context.Context, fresh bool) map[string]any {
	path := "/api/status"
	if fresh {
		path += "?fresh=1"
	}
	document := b.documentFrom(ctx, true, path)
	records := b.records()
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
		// Rule 8: the broker's part of "the library is unavailable". Not
		// while a broker operation touches the library (a transfer or a
		// delivery stops its service on purpose; a release is one) or its
		// service is starting, and only once two documents in a row found
		// it so: a slow answer or a brief restart is not a problem.
		service, _ := entry["service"].(map[string]any)
		state, _ := service["state"].(string)
		touched := slices.ContainsFunc(records, func(record brokerOperationRecord) bool {
			return !opTerminal(record.State) && operationTouches(record, id)
		})
		entry["state_label"] = libraryStateLabel(state, touched, records, id)
		problem := ""
		switch {
		case touched || state == "starting":
		case entry["error"] != nil:
			problem = "unreachable"
		case entry["selected"] == true && state == "stopped":
			problem = "unavailable"
		}
		if fresh || b.unavailableStreak(id, problem) < 2 {
			continue
		}
		switch problem {
		case "unreachable":
			attention = append(attention, attentionItem{ID: "library-unreachable", Rule: 8, Severity: "error", Library: id,
				Title: defaultString(name, "A library") + " is not answering", Detail: firstString(entry["error"]),
				Action: attentionAction{Label: "Open Libraries", Path: "/settings/libraries"}})
		case "unavailable":
			attention = append(attention, attentionItem{ID: "library-unavailable", Rule: 8, Severity: "error", Library: id,
				Title: defaultString(name, "The selected library") + " is not open", Detail: "Its service is not running.",
				Action: attentionAction{Label: "Open Libraries", Path: "/settings/libraries"}})
		}
	}
	document["attention"] = attention
	return document
}

// unavailableStreak records whether this document found library id with a
// rule 8 problem, and returns how many documents in a row have.
func (b *brokerOperations) unavailableStreak(id, problem string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.unavailable == nil {
		b.unavailable = map[string]int{}
	}
	if problem == "" {
		delete(b.unavailable, id)
		return 0
	}
	b.unavailable[id]++
	return b.unavailable[id]
}

// libraryStateLabel is a library's state in a word or two, for a surface's
// line about another library: the verb of a broker operation touching it,
// else its service's state.
func libraryStateLabel(state string, touched bool, records []brokerOperationRecord, id string) string {
	if touched {
		for _, record := range records {
			if !opTerminal(record.State) && operationTouches(record, id) {
				if kind, ok := lookupOperationKind(record.Kind); ok {
					return kind.Verb
				}
			}
		}
	}
	switch state {
	case "running":
		return "Open"
	case "starting":
		return "Opening"
	}
	return "Not open"
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
	if r.URL.Query().Get("fresh") == "1" {
		compute, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		document := b.statusDocumentChecked(compute, true)
		document["checked"] = "fresh"
		writeJSON(w, document, http.StatusOK)
		return
	}
	serveFrame(w, r, b.statusCurrent(r.Context(), statusCoalesce))
}

func (b *brokerOperations) serveStatusStream(w http.ResponseWriter, r *http.Request) {
	changes, unsubscribe := b.reg.subscribe()
	defer unsubscribe()
	streamStatus(w, r, changes, brokerStatusRecheck, func(ctx context.Context) statusFrame { return b.statusCurrent(ctx, recheckAge(brokerStatusRecheck)) })
}
