package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"time"
)

// Feature flags are booleans in the catalog's ui_preferences, so a flag
// follows its library between Macs like any other preference
// (docs/background-work.md §9). The page reads them from preferences.js;
// Go reads them through Catalog.featureFlag, cached briefly and invalidated
// whenever preferences are saved.

// pauseIndexingForQueriesFlag opts into yielding ingestion and maintenance
// while interactive reads run. Off by default so browsing cannot starve indexing.
const pauseIndexingForQueriesFlag = "pharos-pause-indexing-for-queries-v1"

// featureFlagTTL bounds how stale a cached flag may be when another process
// (a second service on the same catalog) saved it.
var featureFlagTTL = 5 * time.Second

type featureFlagCache struct {
	mu     sync.Mutex
	values map[string]featureFlagValue
	// generation counts invalidations, so a read that raced a save never
	// caches the value from before it.
	generation uint64
}

type featureFlagValue struct {
	on bool
	at time.Time
}

func (f *featureFlagCache) invalidate() {
	f.mu.Lock()
	f.values = nil
	f.generation++
	f.mu.Unlock()
}

// featureFlag reports whether the boolean preference key is true. Anything
// else (missing, not a boolean, unreadable) is off.
func (c *Catalog) featureFlag(ctx context.Context, key string) bool {
	if c == nil {
		return false
	}
	c.flags.mu.Lock()
	cached, ok := c.flags.values[key]
	generation := c.flags.generation
	c.flags.mu.Unlock()
	if ok && time.Since(cached.at) < featureFlagTTL {
		return cached.on
	}
	var raw string
	on := false
	err := c.reader().QueryRowContext(ctx, "SELECT value FROM ui_preferences WHERE key=?", key).Scan(&raw)
	if err == nil {
		_ = json.Unmarshal([]byte(raw), &on)
	} else if !errors.Is(err, sql.ErrNoRows) {
		// A failed read is not cached: the next caller asks again.
		return false
	}
	c.flags.mu.Lock()
	if c.flags.generation == generation {
		if c.flags.values == nil {
			c.flags.values = map[string]featureFlagValue{}
		}
		c.flags.values[key] = featureFlagValue{on: on, at: time.Now()}
		c.applyFeatureFlag(key, on)
	}
	c.flags.mu.Unlock()
	return on
}

// applyFeatureFlag updates work paths without a catalog read. Call with
// flags.mu held so a stale flag read cannot overwrite a saved value.
func (c *Catalog) applyFeatureFlag(key string, on bool) {
	switch key {
	case pauseIndexingForQueriesFlag:
		c.queries.setEnabled(on)
	}
}

// rememberSavedFlags records the flags' saved values, after a save's
// invalidation, so a read that raced the save cannot overwrite it.
func (c *Catalog) rememberSavedFlags(updates map[string]string, deletes []string) {
	c.flags.mu.Lock()
	defer c.flags.mu.Unlock()
	for _, key := range []string{pauseIndexingForQueriesFlag} {
		if slices.Contains(deletes, key) {
			c.applyFeatureFlag(key, false)
		} else if value, ok := updates[key]; ok {
			on := false
			_ = json.Unmarshal([]byte(value), &on)
			c.applyFeatureFlag(key, on)
		}
	}
}

// watchFeatureFlags keeps work paths' flags current while the service
// runs, for flags saved by another process on the same catalog. It is a
// daemon (§3.4): it holds nothing and writes nothing.
func (c *Catalog) watchFeatureFlags(ctx context.Context) {
	for {
		c.featureFlag(ctx, pauseIndexingForQueriesFlag)
		select {
		case <-ctx.Done():
			return
		case <-time.After(featureFlagTTL):
		}
	}
}
