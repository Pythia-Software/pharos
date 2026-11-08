package archive

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// These keys are reconstructible view state. Feedback annotations and drafts,
// saved queries, and actual settings continue to use durable preference writes.
var sessionPreferenceKey = regexp.MustCompile(`^pharos-session:[a-f0-9]{32}:(pharos-history-v1|pharos-nav-open|pharos-feedback-enabled|pharos-ui-feedback-layout-v1|query-table:last:[A-Za-z0-9:_.-]+|pharos-query-rejected:[A-Za-z0-9:_.-]+)$`)

const (
	sessionPreferenceLimit = 128
	sessionPreferenceBytes = 4 << 20
	sessionPreferenceBatch = 16
)

type sessionPreferenceChange struct {
	value   string // JSON-encoded value
	deleted bool
	version uint64
}

type sessionPreferenceBuffer struct {
	mu      sync.Mutex
	flushMu sync.Mutex // serializes background rounds with explicit durable writes
	pending map[string]sessionPreferenceChange
	version uint64
	wake    chan struct{}
	stop    chan struct{}
	done    chan struct{}
	closed  bool
}

// queueSessionPreferences accepts an atomic, bounded batch and coalesces each
// key to its latest value. Acceptance is not a promise of durable storage.
func (c *Catalog) queueSessionPreferences(body map[string]any) error {
	set, _ := body["set"].(map[string]any)
	remove, _ := body["delete"].([]any)
	changes := make(map[string]sessionPreferenceChange, len(set)+len(remove))
	for key, value := range set {
		if !preferenceKey.MatchString(key) || !sessionPreferenceKey.MatchString(key) {
			return fmt.Errorf("%q is not a disposable session preference", key)
		}
		if _, ok := value.(string); !ok {
			return fmt.Errorf("session preference %q must be a string", key)
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return err
		}
		if len(encoded) > maxPreferenceBytes {
			return fmt.Errorf("session preference %q is too large", key)
		}
		changes[key] = sessionPreferenceChange{value: string(encoded)}
	}
	for _, item := range remove {
		key, ok := item.(string)
		if !ok || !preferenceKey.MatchString(key) || !sessionPreferenceKey.MatchString(key) {
			return fmt.Errorf("%q is not a disposable session preference", key)
		}
		changes[key] = sessionPreferenceChange{deleted: true}
	}
	if len(changes) == 0 {
		return nil
	}
	b := &c.sessionPreferences
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return fmt.Errorf("catalog is closing")
	}
	merged := make(map[string]sessionPreferenceChange, len(b.pending)+len(changes))
	for key, change := range b.pending {
		merged[key] = change
	}
	for key, change := range changes {
		merged[key] = change
	}
	bytes := 0
	for key, change := range merged {
		bytes += len(key) + len(change.value)
	}
	if len(merged) > sessionPreferenceLimit || bytes > sessionPreferenceBytes {
		return fmt.Errorf("session preference buffer is full")
	}
	if b.wake == nil {
		b.wake, b.stop, b.done = make(chan struct{}, 1), make(chan struct{}), make(chan struct{})
		go c.runSessionPreferences(b.wake, b.stop, b.done)
	}
	for key, change := range changes {
		b.version++
		change.version = b.version
		merged[key] = change
	}
	b.pending = merged
	select {
	case b.wake <- struct{}{}:
	default:
	}
	return nil
}

func (s *Server) postSessionPreferences(w http.ResponseWriter, body map[string]any) {
	if err := s.Catalog.queueSessionPreferences(body); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"buffered": true}, http.StatusOK)
}

func (c *Catalog) closeSessionPreferences() {
	b := &c.sessionPreferences
	b.mu.Lock()
	if !b.closed {
		b.closed = true
		if b.stop != nil {
			close(b.stop)
		}
	}
	done := b.done
	b.mu.Unlock()
	if done != nil {
		<-done
	}
}

func (c *Catalog) runSessionPreferences(wake, stop, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	var logged time.Time
	for {
		select {
		case <-stop:
			return
		case <-wake:
		case <-ticker.C:
		}
		if err := c.flushSessionPreferences(); err != nil && err != errJournalBusy && time.Since(logged) >= time.Minute {
			fmt.Fprintf(os.Stderr, "Session preferences: will retry: %v\n", err)
			logged = time.Now()
		}
	}
}

// A round never waits for another writer while holding the gate. Like the
// operation journal, it uses a connection-local zero busy timeout and yields
// to queued writers between statements. Failed rounds retain the latest values.
func (c *Catalog) flushSessionPreferences() error {
	b := &c.sessionPreferences
	if !b.flushMu.TryLock() {
		return errJournalBusy
	}
	defer b.flushMu.Unlock()
	b.mu.Lock()
	keys := make([]string, 0, len(b.pending))
	for key := range b.pending {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) > sessionPreferenceBatch {
		keys = keys[:sessionPreferenceBatch]
	}
	batch := make(map[string]sessionPreferenceChange, len(keys))
	for _, key := range keys {
		batch[key] = b.pending[key]
	}
	b.mu.Unlock()
	if len(keys) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), journalConnWait)
	conn, err := c.DB.Conn(ctx)
	cancel()
	if err != nil {
		return errJournalBusy
	}
	defer conn.Close()
	release, ok := c.tryWriteGate()
	if !ok {
		return errJournalBusy
	}
	gateHeld := true
	defer func() {
		if gateHeld {
			release()
		}
	}()
	if c.writerActive() || c.writersWaiting() {
		return errJournalBusy
	}
	ctx = context.Background()
	if _, err = conn.ExecContext(ctx, "PRAGMA busy_timeout=0"); err != nil {
		return err
	}
	defer conn.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout=%d", busyTimeoutMS))
	committed := false
	defer func() {
		if !committed {
			if _, err := conn.ExecContext(ctx, "ROLLBACK"); err != nil && !strings.Contains(err.Error(), "no transaction is active") {
				_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			}
		}
	}()
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		if catalogBusy(err) {
			return errJournalBusy
		}
		return err
	}
	for _, key := range keys {
		if c.writersWaiting() {
			return errJournalBusy
		}
		change := batch[key]
		if change.deleted {
			_, err = conn.ExecContext(ctx, "DELETE FROM ui_preferences WHERE key=?", key)
		} else {
			_, err = conn.ExecContext(ctx, "INSERT INTO ui_preferences(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, change.value)
		}
		if err != nil {
			return err
		}
	}
	if c.writersWaiting() {
		return errJournalBusy
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	committed = true
	release()
	gateHeld = false
	b.mu.Lock()
	for key, change := range batch {
		if current, ok := b.pending[key]; ok && current.version == change.version {
			delete(b.pending, key)
		}
	}
	more := len(b.pending) > 0
	b.mu.Unlock()
	if more {
		select {
		case b.wake <- struct{}{}:
		default:
		}
	}
	return nil
}
