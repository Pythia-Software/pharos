package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"syscall"
)

var errCatalogIngestBusy = errors.New("source indexing is already running for this catalog; retry when it finishes")

// The service's ingest mutex also participates in CLI replay coordination.
// Keep the file lease for the complete ingest, before any capture/DB locks.
type ingestCoordinator struct {
	mu    sync.Mutex
	path  string
	lease *os.File
}

func lockCatalogIngest(path string) (*os.File, error) {
	lease, err := os.OpenFile(canonical(path)+".ingest.lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(lease.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lease.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errCatalogIngestBusy
		}
		return nil, err
	}
	return lease, nil
}

func (m *ingestCoordinator) TryLock() bool {
	if !m.mu.TryLock() {
		return false
	}
	if m.path != "" {
		lease, err := lockCatalogIngest(m.path)
		if err != nil {
			m.mu.Unlock()
			return false
		}
		m.lease = lease
	}
	return true
}

func (m *ingestCoordinator) Unlock() {
	if m.lease != nil {
		m.lease.Close()
		m.lease = nil
	}
	m.mu.Unlock()
}

const conversationDerivationVersion = "sessions-ledger-instructions-v2-effort"

func physicalPartCount(parts []sourcePart) int {
	count := 0
	for _, part := range parts {
		if !strings.HasPrefix(part.item, "group:") {
			count++
		}
	}
	return count
}

type ingestMode struct {
	deferGit   bool
	plan       *sourceWorkPlan
	planned    func(*sourceWorkPlan, string)
	automatic  bool
	force      bool
	discovered func([]sourcePart, bool)
	written    func(string)
	processed  func([]sourcePart)
}

type ingestModeKey struct{}

func withIngestMode(ctx context.Context, mode ingestMode) context.Context {
	return context.WithValue(ctx, ingestModeKey{}, mode)
}

func modeOf(ctx context.Context) ingestMode {
	mode, _ := ctx.Value(ingestModeKey{}).(ingestMode)
	return mode
}

func (c *Catalog) effectiveIndexVersion(host, source string, adapter Adapter) (string, error) {
	var generation int64
	err := c.DB.QueryRow("SELECT generation FROM sync_generations WHERE host_id=? AND source_name=?", host, source).Scan(&generation)
	if err != nil && err != sql.ErrNoRows {
		return "", err
	}
	version := sourceIndexVersion(adapter)
	if generation != 0 {
		version += fmt.Sprintf("@generation:%d", generation)
	}
	return version, nil
}

func (c *Catalog) advanceSyncGeneration(ctx context.Context, host, source string) error {
	_, err := c.DB.ExecContext(ctx, `INSERT INTO sync_generations(host_id,source_name,generation) VALUES(?,?,1)
		ON CONFLICT(host_id,source_name) DO UPDATE SET generation=generation+1`, host, source)
	return err
}
