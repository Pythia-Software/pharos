package archive

import (
	"context"
	"errors"
	"path/filepath"
)

func (c *Catalog) recordInputAcquisition(host string, source SourceConfig, revision string, failure error) error {
	if !retainedAcquisitionEnabled(source) {
		return nil
	} // ordinary live planning errors are indexing diagnostics, not acquisition
	if errors.Is(failure, errNativeSnapshotPaused) {
		return nil
	} // job-owned state is read from its durable overlay
	pending := failure != nil
	var message any
	if failure != nil && !errors.Is(failure, errCaptureBusy) {
		message = failure.Error()
	}
	_, err := c.DB.Exec(`INSERT INTO source_input_states(host_id,source_name,preservation_mode,acquired_revision,acquired_at,pending,error)
 VALUES(?,?,?,?,?,?,?) ON CONFLICT(host_id,source_name) DO UPDATE SET preservation_mode=excluded.preservation_mode,
 acquired_revision=COALESCE(excluded.acquired_revision,source_input_states.acquired_revision),
 acquired_at=COALESCE(excluded.acquired_at,source_input_states.acquired_at),pending=1,error=excluded.error`, host, source.Name, preservationMode(source), nilIfEmpty(revision), conditionalString(!pending && revision != "", now()), boolInt(pending || revision != ""), message)
	return err
}
func (c *Catalog) recordInputIndexed(host, source, revision string) error {
	if revision == "" {
		// Successful live/temporary reads clear old planning errors without
		// claiming a retained revision. A genuinely unindexed acquisition stays
		// pending until that exact revision has been indexed.
		var stale bool
		if err := c.reader().QueryRow(`SELECT EXISTS(SELECT 1 FROM source_input_states WHERE host_id=? AND source_name=? AND (pending<>0 OR error IS NOT NULL) AND COALESCE(acquired_revision,'')=COALESCE(indexed_revision,''))`, host, source).Scan(&stale); err != nil {
			return err
		}
		if !stale {
			return nil
		}
		_, err := c.DB.Exec(`UPDATE source_input_states SET pending=0,error=NULL WHERE host_id=? AND source_name=? AND COALESCE(acquired_revision,'')=COALESCE(indexed_revision,'')`, host, source)
		return err
	}
	_, err := c.DB.Exec(`UPDATE source_input_states SET indexed_revision=?,indexed_at=?,pending=CASE WHEN acquired_revision=? THEN 0 ELSE 1 END,error=NULL WHERE host_id=? AND source_name=?`, revision, now(), revision, host, source)
	return err
}
func (c *Catalog) inputPreservationStatus(ctx context.Context, host, source string) map[string]any {
	// Polling reads only acquisition bookkeeping. Per-copy coverage belongs
	// to an explicit historical-preservation plan, not a scan on every status
	// request (and ordinary live indexing does not promise such coverage).
	status := map[string]any{"status": "untracked", "pending_newer_inputs": false}
	rows, err := queryMapsContext(ctx, c.reader(), "SELECT * FROM source_input_states WHERE host_id=? AND source_name=?", host, source)
	if err == nil && len(rows) > 0 {
		for key, value := range rows[0] {
			status[key] = value
		}
		status["pending_newer_inputs"] = integer(rows[0]["pending"]) != 0
		acquired, indexed := firstString(rows[0]["acquired_revision"]), firstString(rows[0]["indexed_revision"])
		if acquired != "" {
			status["status"] = "acquired"
			if acquired == indexed {
				status["status"] = "indexed"
			}
		}
	}
	if pins, err := loadPreservationPins(filepath.Join(c.captureRootPath(), host, source)); err == nil {
		if len(pins.NativePauses) > 0 {
			status["pending_newer_inputs"], status["native_snapshot_pauses"], status["error"] = true, pins.NativePauses, errNativeSnapshotPaused.Error()
		} else if firstString(status["error"]) == errNativeSnapshotPaused.Error() {
			status["pending_newer_inputs"] = status["acquired_revision"] != status["indexed_revision"]
			status["error"] = nil
		}
	}
	return status
}
