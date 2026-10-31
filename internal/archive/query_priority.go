package archive

import (
	"context"
	"net/http"
	"strings"
	"sync"
)

// queryActivity gives interactive reads the drive while indexing waits at a
// work boundary. Readers never acquire the writer gate or wait for an index.
// This is independent of the activity UI's feature flag and of an index's
// manual/background priority: even a manual index must leave queries usable.
type queryActivity struct {
	mu      sync.Mutex
	active  int
	idle    chan struct{}
	waiting int
}

func (q *queryActivity) begin() func() {
	q.mu.Lock()
	if q.active == 0 {
		q.idle = make(chan struct{})
	}
	q.active++
	q.mu.Unlock()
	return func() {
		q.mu.Lock()
		defer q.mu.Unlock()
		q.active--
		if q.active == 0 {
			close(q.idle)
		}
	}
}

func (q *queryActivity) wait(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		q.mu.Lock()
		if q.active == 0 {
			q.mu.Unlock()
			return nil
		}
		idle := q.idle
		q.waiting++
		q.mu.Unlock()
		select {
		case <-idle:
		case <-ctx.Done():
		}
		q.mu.Lock()
		q.waiting--
		q.mu.Unlock()
	}
}

// Only data reads count as demand. Status/liveness polls and static assets
// must not starve indexing, and mutations must not pause a writer they need.
func interactiveRead(r *http.Request) bool {
	path := r.URL.Path
	if strings.HasPrefix(path, "/api/query/") {
		return r.Method == http.MethodGet || r.Method == http.MethodPost
	}
	if path == "/api/search/text" {
		return r.Method == http.MethodPost
	}
	if r.Method != http.MethodGet {
		return false
	}
	return path == "/api/search" || path == "/api/library/find" ||
		strings.HasPrefix(path, "/api/work/") || strings.HasPrefix(path, "/api/messages/") ||
		strings.HasPrefix(path, "/api/conversation/") || strings.HasPrefix(path, "/api/usage/")
}

type indexQueryWaitKey struct{}

func (c *Catalog) withIndexQueryWait(ctx context.Context) context.Context {
	return context.WithValue(ctx, indexQueryWaitKey{}, c.queries.wait)
}

// Large workspaces may contain thousands of messages. Yield inside the atomic
// record too; WAL readers can still read its previous committed contents.
func waitForIndexQuery(ctx context.Context) error {
	if wait, ok := ctx.Value(indexQueryWaitKey{}).(func(context.Context) error); ok {
		return wait(ctx)
	}
	return ctx.Err()
}
