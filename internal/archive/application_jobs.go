package archive

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"time"
)

func (m *applicationManager) reportJob(state, phase string) {
	m.jobMu.Lock()
	m.job = map[string]any{"state": state, "phase": phase, "updated_at": now()}
	m.jobMu.Unlock()
}

func (m *applicationManager) startOperation(w http.ResponseWriter, r *http.Request, body map[string]any) {
	if !m.operationMu.TryLock() {
		writeError(w, fmt.Errorf("another library operation is running"), 409)
		return
	}
	m.mu.Lock()
	m.busy = true
	m.generation++
	for id := range m.children {
		m.stopChildLocked(id)
	}
	var source, target Config
	var err error
	recovering := r.URL.Path == "/api/app/recover"
	if !recovering {
		a, b := m.registry.library(firstString(body["source"])), m.registry.library(firstString(body["target"]))
		if a == nil || b == nil || a.ID == b.ID {
			err = fmt.Errorf("choose two different libraries")
		} else {
			source, err = a.load()
			if err == nil {
				target, err = b.load()
			}
		}
	}
	m.mu.Unlock()
	if err != nil {
		m.mu.Lock()
		m.busy = false
		m.mu.Unlock()
		m.operationMu.Unlock()
		writeError(w, err, 400)
		return
	}
	m.reportJob("running", "Preparing library operation")
	go func() {
		defer m.operationMu.Unlock()
		defer func() { m.mu.Lock(); m.busy = false; m.mu.Unlock() }()
		progress := func(s string) { m.reportJob("running", s) }
		if recovering {
			var config Config
			config, err = RecoverLibrary(m.ctx, firstString(body["backup"]), firstString(body["path"]), progress)
			if err == nil {
				m.mu.Lock()
				var id string
				id, err = m.registry.register(config, "")
				if err == nil {
					m.registry.Selected = id
					m.generation++
					err = m.registry.save()
				}
				m.mu.Unlock()
			}
		} else {
			_, err = TransferLibrary(m.ctx, source, target, body["move"] == true, progress)
		}
		if err != nil {
			m.reportJob("failed", err.Error())
		} else {
			m.reportJob("complete", "Library operation completed and verified")
		}
	}()
	writeJSON(w, map[string]any{"started": true}, http.StatusAccepted)
}

// An automatic pair is deliberately one explicit whole-library move. It uses
// the same locks and verification boundary as a manual operation. Source
// bindings belong to the pending library and require their own source opt-in.
func (m *applicationManager) deliveryCycle(sourceID, targetID string) {
	if !m.operationMu.TryLock() {
		return
	}
	defer m.operationMu.Unlock()
	m.mu.Lock()
	a, b := m.registry.library(sourceID), m.registry.library(targetID)
	if a == nil || b == nil || m.released || m.suspended[sourceID] {
		m.mu.Unlock()
		return
	}
	source, err := a.load()
	if err != nil {
		m.mu.Unlock()
		m.reportJob("paused", err.Error())
		return
	}
	source.DeliveryTargetInstance = b.Instance
	target, targetErr := b.load()
	if m.suspended[targetID] {
		targetErr = fmt.Errorf("archive is released; pending history stays local until its drive returns")
	}
	m.busy = true
	m.generation++
	m.stopChildLocked(sourceID)
	m.stopChildLocked(targetID)
	m.mu.Unlock()
	defer func() { m.mu.Lock(); m.busy = false; m.mu.Unlock() }()
	progress := func(s string) { m.reportJob("running", s) }
	if _, e := os.Stat(pendingDeliveryPath(source)); os.IsNotExist(e) {
		progress("Capturing approved sources into the pending library")
		err = capturePending(m.ctx, source)
	}
	if err == nil {
		err = targetErr
		if err == nil {
			_, err = TransferLibrary(m.ctx, source, target, true, progress)
		}
	}
	if err != nil {
		m.reportJob("paused", err.Error())
	} else {
		m.reportJob("complete", "Pending history delivered; local history is empty")
	}
}

func capturePending(ctx context.Context, config Config) error {
	config.PendingCapture = true
	if err := config.checkLibrary(libraryVolumeIdentity); err != nil {
		return err
	}
	if err := protectPendingEvidence(config); err != nil {
		return err
	}
	lease, err := fileLease(libraryUseLock(config), true)
	if err != nil {
		return err
	}
	defer lease.Close()
	sources := []SourceConfig{}
	for _, source := range config.Sources {
		if source.Enabled {
			sources = append(sources, source)
		}
	}
	if len(sources) == 0 {
		return nil
	}
	summary, err := Capture(ctx, config, sources, nil)
	if err != nil {
		return err
	}
	if !summary.OK {
		return fmt.Errorf("pending capture is incomplete; all data was retained")
	}
	catalog, err := OpenCatalog(config.CatalogPath)
	if err != nil {
		return err
	}
	defer catalog.Close()
	catalog.setCaptureRoot(config.CaptureRoot)
	if config.DeliveryTargetInstance != "" {
		err = catalog.writeTransaction(ctx, "invalidate-replaced-delivery-proof", func(tx *sql.Tx) error {
			var obsolete int
			if e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM library_delivery_checkpoints WHERE destination_instance<>?)
 OR EXISTS(SELECT 1 FROM library_workspace_delivery_checkpoints WHERE destination_instance<>?)`, config.DeliveryTargetInstance, config.DeliveryTargetInstance).Scan(&obsolete); e != nil {
				return e
			}
			if obsolete == 0 {
				return nil
			}
			_, e := tx.ExecContext(ctx, `DELETE FROM source_record_states; DELETE FROM source_item_states; DELETE FROM source_terminal_states;
 DELETE FROM library_delivery_checkpoints WHERE destination_instance<>?;
 DELETE FROM library_workspace_delivery_checkpoints WHERE destination_instance<>?`, config.DeliveryTargetInstance, config.DeliveryTargetInstance)
			return e
		})
		if err != nil {
			return err
		}
	}
	targets, err := captureTargets(config.CaptureRoot, nil, false, nil)
	if err != nil {
		return err
	}
	for _, target := range targets {
		// An input may have been restored with the same size and timestamp.
		// Capture's delivery fingerprint includes file hashes, so a new payload
		// invalidates metadata-only parser shortcuts. Record digests still keep
		// unchanged sessions out of the otherwise empty pending catalog.
		if _, err = catalog.DB.ExecContext(ctx, `DELETE FROM source_item_states WHERE host_id=? AND source_name=?;
 DELETE FROM source_terminal_states WHERE host_id=? AND source_name=?;
 UPDATE source_states SET fingerprint=NULL WHERE host_id=? AND source_name=?`, target.Host.ID, target.Source, target.Host.ID, target.Source, target.Host.ID, target.Source); err != nil {
			return err
		}
		result := catalog.IndexCapture(ctx, target, nil)
		if result.Error != nil {
			return fmt.Errorf("index pending history: %s", result.Error)
		}
	}
	return nil
}

// The capture cadence is intentionally bounded even when a destination is
// disconnected. Polling for a mount must never repeatedly snapshot large DBs.
const applicationDeliveryInterval = 5 * time.Minute
