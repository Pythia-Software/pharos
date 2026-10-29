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
	if err != nil {
		m.mu.Unlock()
		m.operationMu.Unlock()
		writeError(w, err, 400)
		return
	}
	// Registered before the services stop, so the lock-free snapshot shows
	// it from the start.
	var op *Op
	if recovering {
		op = m.operations.begin(OperationSpec{Kind: "library.recover", Trigger: triggerUser})
	} else {
		variant := "copy"
		if body["move"] == true {
			variant = "move"
		}
		op = m.operations.begin(OperationSpec{Kind: "library.transfer", Variant: variant, Trigger: triggerUser}, firstString(body["source"]), firstString(body["target"]))
	}
	m.busy = true
	m.generation++
	for id := range m.children {
		m.stopChildLocked(id)
	}
	m.mu.Unlock()
	m.reportJob("running", "Preparing library operation")
	op.Step("Preparing library operation", "", -1)
	m.operations.launch(op, func(op *Op) error {
		defer m.operationMu.Unlock()
		defer func() { m.mu.Lock(); m.busy = false; m.publishLocked(); m.mu.Unlock() }()
		progress := func(s string) { m.reportJob("running", s); op.Step(s, "", -1) }
		if recovering {
			var config Config
			config, err = RecoverLibrary(m.ctx, firstString(body["backup"]), firstString(body["path"]), progress)
			if err == nil {
				m.mu.Lock()
				var id string
				id, err = m.registry.register(config, "")
				if err == nil {
					m.selectLocked(id)
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
		return err
	})
	writeJSON(w, map[string]any{"started": true}, http.StatusAccepted)
}

// compactCloseWait is how long a compaction waits for the library's service
// to finish exiting after stopChildLocked gave up on it, before ending it;
// tests shorten it.
var compactCloseWait = 30 * time.Second

// startCompaction compacts one library's catalog (catalog_compact.go). Like a
// transfer it closes every library while it runs, and the startup window
// shows its progress and reopens the library when it finishes.
func (m *applicationManager) startCompaction(w http.ResponseWriter, body map[string]any) {
	if !m.operationMu.TryLock() {
		writeError(w, fmt.Errorf("another library operation is running"), 409)
		return
	}
	m.mu.Lock()
	id := firstString(body["id"])
	var config Config
	var err error
	if l := m.registry.library(id); l == nil {
		err = fmt.Errorf("choose a library")
	} else if m.suspended[id] {
		err = fmt.Errorf("this library has been released; reconnect it first")
	} else {
		config, err = l.load()
	}
	if err != nil {
		m.mu.Unlock()
		m.operationMu.Unlock()
		writeError(w, err, 400)
		return
	}
	op := m.operations.begin(OperationSpec{Kind: "library.compact", Trigger: triggerUser}, id)
	m.busy = true
	m.generation++
	for child := range m.children {
		m.stopChildLocked(child)
	}
	// A service still checkpointing on its way out holds the library.
	closing := m.children[id]
	m.mu.Unlock()
	m.reportJob("running", "Preparing to compact the catalog")
	op.Step("Preparing to compact the catalog", "", -1)
	m.operations.launch(op, func(op *Op) error {
		defer m.operationMu.Unlock()
		defer func() { m.mu.Lock(); m.busy = false; m.publishLocked(); m.mu.Unlock() }()
		if closing != nil {
			select {
			case <-closing.done:
			case <-m.ctx.Done():
				return m.ctx.Err()
			case <-time.After(compactCloseWait):
				// It has ignored SIGTERM this long: end it, as the app's quit
				// does. Its committed writes are kept, as after any crash.
				_ = closing.cmd.Process.Kill()
				select {
				case <-closing.done:
				case <-time.After(5 * time.Second):
					// Fail as a job, so the startup window reopens the library.
					err := fmt.Errorf("the library is still closing; compact again shortly")
					m.reportJob("failed", err.Error())
					return err
				}
			}
			m.mu.Lock()
			if m.children[id] == closing {
				delete(m.children, id)
			}
			m.mu.Unlock()
		}
		step := ""
		receipt, err := CompactLibrary(m.ctx, config, func(phase string, done, total int64) {
			if total > 0 {
				m.reportJob("running", fmt.Sprintf("%s: %s of %s", phase, humanBytes(done), humanBytes(total)))
			} else {
				m.reportJob("running", phase)
			}
			if phase != step {
				step = phase
				unit := ""
				if total > 0 {
					unit = "byte"
				}
				op.Step(phase, unit, total)
			}
			if total > 0 {
				op.progress(phase, "", "byte", float64(done), float64(total), true)
			}
		})
		switch {
		case err != nil:
			m.reportJob("failed", err.Error())
		case receipt.FreeBytes == 0:
			m.reportJob("complete", "The catalog had no free space to give back")
		default:
			m.reportJob("complete", fmt.Sprintf("Compacted the catalog from %s to %s in %s", humanBytes(receipt.BeforeBytes), humanBytes(receipt.AfterBytes), humanDuration(receipt.Duration)))
		}
		return err
	})
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
	// Whether delivery's IO runs at the utility tier is the flag's (§10).
	m.operations.refreshFlag(m.ctx)
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
	deliver := m.operations.begin(OperationSpec{Kind: "library.deliver", Trigger: triggerSchedule}, sourceID, targetID)
	defer func() { deliver.Finish(err) }()
	m.busy = true
	m.generation++
	m.stopChildLocked(sourceID)
	m.stopChildLocked(targetID)
	m.mu.Unlock()
	defer func() { m.mu.Lock(); m.busy = false; m.publishLocked(); m.mu.Unlock() }()
	progress := func(s string) { m.reportJob("running", s); deliver.Step(s, "", -1) }
	if _, e := os.Stat(pendingDeliveryPath(source)); os.IsNotExist(e) {
		progress("Capturing approved sources into the pending library")
		_, capture := deliver.Child(OperationSpec{Kind: "library.deliver.capture", Libraries: []string{sourceID}, Trigger: triggerChained})
		// Delivery is background: with the flag on, its IO runs at the
		// utility tier (§10), as the move's below does.
		deliver.RunIO(func() { err = capturePendingLibrary(m.ctx, source) })
		capture.Finish(err)
	}
	if err == nil {
		err = targetErr
		if err == nil {
			_, move := deliver.Child(OperationSpec{Kind: "library.transfer", Variant: "move", Libraries: []string{sourceID, targetID}, Trigger: triggerChained})
			deliver.RunIO(func() {
				_, err = TransferLibrary(m.ctx, source, target, true, func(s string) { progress(s); move.Step(s, "", -1) })
			})
			move.Finish(err)
		}
	}
	if err != nil {
		m.reportJob("paused", err.Error())
	} else {
		m.reportJob("complete", "Pending history delivered; local history is empty")
	}
}

func capturePendingLibrary(ctx context.Context, config Config) error {
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
