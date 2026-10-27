package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// Feature flags are booleans in the catalog's ui_preferences, so a flag
// follows its library between Macs like any other preference
// (docs/background-work.md §9). The page reads them from preferences.js;
// Go reads them through Catalog.featureFlag, cached briefly and invalidated
// whenever preferences are saved.

// unifiedActivityFlag gates the unified activity panel, the native menu
// rendered from the status snapshot, and every behaviour change of the
// background-work series. The registry, journal and snapshots run whatever
// its value.
const unifiedActivityFlag = "pharos-unified-activity-v1"

// featureFlagTTL bounds how stale a cached flag may be when another process
// (a second service on the same catalog) saved it.
var featureFlagTTL = 5 * time.Second

type featureFlagCache struct {
	mu     sync.Mutex
	values map[string]featureFlagValue
	// generation counts invalidations, so a read that raced a save never
	// caches the value from before it.
	generation uint64
	// unified is the unified activity flag's last known value, for the
	// work path (Catalog.unifiedActivityNow), which never reads the catalog.
	unified atomic.Bool
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
		if key == unifiedActivityFlag {
			c.flags.unified.Store(on)
		}
	}
	c.flags.mu.Unlock()
	return on
}

// unifiedActivity reports the unified activity flag.
func (c *Catalog) unifiedActivity(ctx context.Context) bool {
	return c.featureFlag(ctx, unifiedActivityFlag)
}

// unifiedActivityNow is the flag's last known value, from memory only: what
// work reads before lowering its IO (§10), so it never waits for a reader
// connection. Saving preferences sets it; every read of the flag (each
// status snapshot, the menu bar's poll) and the service's watchFeatureFlags
// daemon refresh it, the daemon at least every featureFlagTTL, so a save
// by another process on the same catalog arrives within about that.
func (c *Catalog) unifiedActivityNow() bool {
	return c != nil && c.flags.unified.Load()
}

// rememberSaved records the flag's saved value, after a save's
// invalidation, so a read that raced the save cannot overwrite it.
func (f *featureFlagCache) rememberSaved(updates map[string]string, deletes []string) {
	if value, ok := updates[unifiedActivityFlag]; ok {
		on := false
		_ = json.Unmarshal([]byte(value), &on)
		f.unified.Store(on)
	} else if slices.Contains(deletes, unifiedActivityFlag) {
		f.unified.Store(false)
	}
}

// watchFeatureFlags keeps unifiedActivityNow current while the service
// runs, for flags saved by another process on the same catalog. It is a
// daemon (§3.4): it holds nothing and writes nothing.
func (c *Catalog) watchFeatureFlags(ctx context.Context) {
	for {
		c.unifiedActivity(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(featureFlagTTL):
		}
	}
}
