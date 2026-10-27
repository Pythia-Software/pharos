package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

type historyReceipt struct {
	OutputBytes int64         `json:"output_bytes"`
	Version     int           `json:"version"`
	Unit        string        `json:"unit"`
	InputHash   string        `json:"input_sha256,omitempty"`
	OracleHash  string        `json:"oracle_sha256,omitempty"`
	Revision    *recordObject `json:"revision,omitempty"`
	Recovery    string        `json:"recovery_sha256,omitempty"`
	Tier        string        `json:"tier"`
	Closed      bool          `json:"dependency_closure"`
	Gap         string        `json:"gap,omitempty"`
	VerifiedAt  string        `json:"verified_at"`
}
type historyJob struct {
	UncertainOutputBytes int64                      `json:"uncertain_output_bytes"`
	AttemptUnit          string                     `json:"attempt_unit,omitempty"`
	AttemptBytes         int64                      `json:"attempt_reserved_bytes"`
	Version              int                        `json:"version"`
	ID                   string                     `json:"id"`
	Plan                 historyPlan                `json:"plan"`
	State                string                     `json:"state"`
	Phase                string                     `json:"phase"`
	Cursor               string                     `json:"cursor,omitempty"`
	Protected            map[string]preservationPin `json:"protected_inputs"`
	Backup               string                     `json:"rollback_destination"`
	Oracle               string                     `json:"oracle_path,omitempty"`
	OracleHash           string                     `json:"oracle_sha256,omitempty"`
	OutputBudget         int64                      `json:"output_budget_bytes"`
	ScratchBudget        int64                      `json:"scratch_budget_bytes"`
	OutputBytes          int64                      `json:"output_bytes"`
	Error                string                     `json:"error,omitempty"`
	UpdatedAt            string                     `json:"updated_at"`
}

func historyJobPath(root, id string) (string, error) {
	if !validRecordHash(id) {
		return "", errors.New("invalid backfill job id")
	}
	return filepath.Join(root, historyDir, "jobs", id), nil
}
func (j *historyJob) dir() string { p, _ := historyJobPath(j.Plan.CaptureRoot, j.ID); return p }
func (j *historyJob) save() error {
	j.UpdatedAt = now()
	return historyWriteJSON(filepath.Join(j.dir(), "job.json"), j)
}
func loadHistoryJob(root, id string) (historyJob, error) {
	var j historyJob
	dir, e := historyJobPath(root, id)
	if e != nil {
		return j, e
	}
	e = historyReadJSON(filepath.Join(dir, "job.json"), &j)
	if e == nil && (j.Version != historyFormat || j.ID != id) {
		e = errors.New("history job version or capture root mismatch")
	}
	if e == nil {
		if j.Plan.ID != id || j.OutputBudget <= 0 || j.OutputBudget > 1<<50 || j.ScratchBudget <= 0 || j.ScratchBudget > 1<<50 || j.UncertainOutputBytes < 0 || j.AttemptBytes < 0 || j.AttemptBytes > j.OutputBudget {
			e = errors.New("invalid history job identity or budget")
		}
		for _, source := range j.Plan.Sources {
			if !validCaptureName(source.Host.ID) || !validCaptureName(source.Source.Name) {
				e = errors.New("invalid history source identity")
			}
		}
		for _, u := range j.Plan.Units {
			if u.Path != "" && (!filepath.IsLocal(filepath.FromSlash(u.Path)) || filepath.ToSlash(filepath.Clean(u.Path)) != u.Path || strings.ContainsAny(u.Path, "\\\x00")) {
				e = errors.New("unsafe history unit path")
			}
		}
		for _, pin := range j.Protected {
			if !safeRetainedPath(pin.Path) || !validRecordHash(pin.SHA256) {
				e = errors.New("unsafe protected history input")
			}
		}
	}
	j.Plan.CaptureRoot = canonical(root)
	return j, e
}
func historyJobLock(dir string) (*os.File, error) {
	f, e := os.OpenFile(filepath.Join(dir, ".job.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		f.Close()
		return nil, errors.New("history job is already running")
	}
	return f, nil
}
func historyFault(point, unit string) error {
	if captureFault != nil {
		return captureFault("history-"+point, unit)
	}
	return nil
}
func (j *historyJob) receiptPath(id string) string {
	return filepath.Join(j.dir(), "receipts", hashBytes([]byte(id))+".json")
}

// Prepare persists a recoverable owner before the first pin/gate. A crash
// halfway through protection is resumable without opening the catalog. The
// executable phase is published only after every selected input is protected.
func prepareHistory(ctx context.Context, config Config, p historyPlan, backup string, output, scratch int64) (historyJob, error) {
	j := historyJob{Version: historyFormat, ID: p.ID, Plan: p, State: "preparing", Phase: "protecting", Protected: map[string]preservationPin{}, Backup: backup, OutputBudget: output, ScratchBudget: scratch}
	if output <= 0 || scratch <= 0 || output > 1<<50 || scratch > 1<<50 {
		return j, errors.New("explicit positive output and scratch byte budgets are required")
	}
	if canonical(config.CaptureRoot) != p.CaptureRoot || canonical(config.CatalogPath) != p.CatalogPath {
		return j, errors.New("plan belongs to a different library")
	}
	// Existing facilities check destination volume, layout and availability.
	if _, e := prepareBackup(config, backup, false); e != nil {
		return j, e
	}
	fresh, e := planHistory(ctx, config, p.Scope)
	if e != nil {
		return j, e
	}
	if fresh.ID != p.ID {
		return j, errors.New("stale history plan; inputs changed before protection, re-plan")
	}
	dir, e := historyJobPath(p.CaptureRoot, j.ID)
	if e != nil {
		return j, e
	}
	rootInfo, err := os.Stat(p.CaptureRoot)
	if err != nil {
		return j, err
	}
	if key, ok := keyOf(rootInfo); ok && fmt.Sprintf("%d:%d", key.device, key.inode) != p.RootIdentity {
		return j, errors.New("capture volume changed after planning")
	}
	if e = os.MkdirAll(dir, 0700); e != nil {
		return j, e
	}
	lock, e := historyJobLock(dir)
	if e != nil {
		return j, e
	}
	defer lock.Close()
	if _, e = os.Stat(filepath.Join(dir, "job.json")); e == nil {
		existing, err := loadHistoryJob(config.CaptureRoot, p.ID)
		if err != nil {
			return j, err
		}
		if canonical(existing.Backup) != canonical(backup) || existing.OutputBudget != output || existing.ScratchBudget != scratch {
			return j, errors.New("history job already exists with different execution options; resume its exact id")
		}
		return existing, nil
	}
	if e = ensureFree(p.CaptureRoot, output+scratch); e != nil {
		return j, e
	}
	if e = j.save(); e != nil {
		return j, e
	}
	e = j.protect(ctx)
	return j, e
}
func (j *historyJob) protect(ctx context.Context) error {
	for _, source := range j.Plan.Sources {
		dir := historySourceDir(j.Plan.CaptureRoot, source)
		lock, e := lockCaptureTarget(ctx, dir)
		if e != nil {
			return e
		}
		e = func() error {
			defer lock.Close()
			target := captureTarget{Host: source.Host, Source: source.Source.Name, Dir: dir}
			c, e := sourceCaptureForTarget(ctx, target)
			if e != nil {
				return e
			}
			// An incomplete previous protection attempt may have upgraded only the
			// writer version. Input hashes and original source identity are rechecked.
			if c.manifest.Source != source.Source {
				return errors.New("source identity changed; re-plan")
			}
			pins, e := loadPreservationPins(dir)
			if e != nil {
				return e
			}
			if pins.NativePauses == nil {
				pins.NativePauses = map[string]string{}
			}
			if len(c.snapshots) > 0 || isConductorSource(c.source.Kind) || strings.EqualFold(c.source.Kind, "tl1") || strings.EqualFold(c.source.Kind, "antigravity") || hasHistorySQLite(j.Plan, source) {
				pins.NativePauses[j.ID] = "prior operator schedule retained; fixed cutoff native acquisition pause"
				if e = c.upgradePreservationWriter(); e != nil {
					return e
				}
				if e = savePreservationPins(dir, pins); e != nil {
					return e
				}
			}
			for _, u := range j.Plan.Units {
				if u.Host != source.Host.ID || u.Source != source.Source.Name || u.Gap != "" {
					continue
				}
				if _, ok := j.Protected[u.ID]; ok {
					continue
				}
				switch u.Kind {
				case "database", "file", "orphan-native":
					pin, e := c.pinVersion(j.ID, "historical backfill "+u.ID, u.Version)
					if e != nil {
						return e
					}
					j.Protected[u.ID] = pin
				case "logical", "logical-orphan":
					if u.Revision == nil {
						return errors.New("missing logical reference")
					}
					if e = newRecordStore(c).pinRevision(j.ID, *u.Revision); e != nil {
						return e
					}
					inventory, e := loadPreservationPins(dir)
					if e != nil {
						return e
					}
					rel := filepath.ToSlash(filepath.Join(recordDir, "objects", u.Revision.Hash+".gz"))
					for _, pin := range inventory.Pins {
						if pin.Owner == j.ID && pin.Path == rel && pin.ReleasedAt == "" {
							j.Protected[u.ID] = pin
						}
					}
				case "view":
					hash, _, e := fileSHA256Context(ctx, filepath.Join(dir, filepath.FromSlash(u.Path)))
					if e != nil || hash != u.Version.SHA256 {
						return errors.New("retained input view changed; reconcile plan")
					}
					// Immutable handoff-2 inventories have no collector. Validate their
					// complete manifest before allowing a receipt or a catalog read.
					m, e := historyLoadManifest(filepath.Dir(filepath.Join(dir, filepath.FromSlash(u.Path))))
					if e != nil {
						return e
					}
					if m.Host.ID != source.Host.ID || m.Source != source.Source {
						return errors.New("retained input view identity changed")
					}
				}
			}
			return nil
		}()
		if e != nil {
			j.Error = e.Error()
			_ = j.save()
			return e
		}
		// Protection may survive a failure to save this cursor. Pin identities are
		// deterministic; resuming claims the same owner instead of another lease.
		if e = j.save(); e != nil {
			return e
		}
	}
	j.State = "prepared"
	j.Phase = "rollback-backup"
	j.Error = ""
	return j.save()
}
func verifyHistoryOracle(ctx context.Context, path string) error {
	db, e := openReadOnlySQLite(path)
	if e != nil {
		return e
	}
	defer db.Close()
	var result string
	if e = db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&result); e != nil {
		return e
	}
	if result != "ok" {
		return fmt.Errorf("rollback catalog integrity: %s", result)
	}
	return nil
}
func (j *historyJob) rollback(ctx context.Context, config Config) error {
	state, err := sqliteDependencyState(config.CatalogPath)
	if err != nil {
		return err
	}
	if !sameSQLiteContentState(state, j.Plan.CatalogState) {
		return errors.New("catalog changed since dry run; re-plan, protected inputs remain pinned")
	}
	job, e := prepareBackup(config, j.Backup, false)
	if e != nil {
		return e
	}
	run := job.Run(ctx, nil)
	if run.State != "complete" {
		return fmt.Errorf("rollback backup %s: %s", run.State, jsonText(run.Errors))
	}
	var m backupManifest
	if e = historyReadJSON(filepath.Join(j.Backup, backupManifestName), &m); e != nil {
		return e
	}
	if !m.Complete || len(m.Missing) > 0 || m.Catalog == nil {
		return errors.New("rollback backup lacks complete catalog and evidence")
	}
	// Verify each pinned input in the backup itself; a catalog-only copy cannot
	// authorize historical export. The ordinary backup uses host shared locks.
	captures := ""
	for _, item := range m.Items {
		if item.Name == "captures" {
			captures = filepath.Join(j.Backup, filepath.FromSlash(item.Path))
		}
	}
	for _, u := range j.Plan.Units {
		pin, ok := j.Protected[u.ID]
		if !ok {
			continue
		}
		hash, size, e := fileSHA256Context(ctx, filepath.Join(captures, u.Host, u.Source, filepath.FromSlash(pin.Path)))
		if e != nil || hash != pin.SHA256 || size != pin.Size {
			return errors.New("rollback backup does not verify a selected pinned input")
		}
	}
	if captures == "" && len(j.Plan.Sources) > 0 {
		return errors.New("rollback lacks selected capture tree")
	}
	for _, u := range j.Plan.Units {
		if u.Gap != "" {
			continue
		}
		if u.Kind == "logical" || u.Kind == "logical-orphan" {
			copyJob := *j
			copyJob.Plan = j.Plan
			copyJob.Plan.CaptureRoot = captures
			r := historyReceipt{Version: historyFormat, Unit: u.ID, Revision: u.Revision, Closed: u.Kind == "logical", VerifiedAt: now()}
			if err := copyJob.verifyReceipt(ctx, u, r); err != nil {
				return fmt.Errorf("rollback logical closure: %w", err)
			}
		} else if u.Kind == "view" {
			copyJob := *j
			copyJob.Plan = j.Plan
			copyJob.Plan.CaptureRoot = captures
			r := historyReceipt{Version: historyFormat, Unit: u.ID, InputHash: u.Version.SHA256, VerifiedAt: now()}
			if err := copyJob.verifyReceipt(ctx, u, r); err != nil {
				return fmt.Errorf("rollback filesystem closure: %w", err)
			}
		}
	}
	j.Oracle = filepath.Join(j.Backup, filepath.FromSlash(m.Catalog.Path))
	if e = verifyHistoryOracle(ctx, j.Oracle); e != nil {
		return e
	}
	j.OracleHash, _, e = fileSHA256Context(ctx, j.Oracle)
	if e != nil {
		return e
	}
	// A changing live catalog produces a new population, never a cursor over
	// half an old plan. The operator re-plans after reconciling the rollback.
	plannedState := j.Plan.CatalogState
	plannedState.Identity, plannedState.WALIdentity = "", ""
	if !sameSQLiteContentState(plannedState, m.Catalog.Source) {
		return errors.New("catalog changed since dry run; protected inputs remain pinned, re-plan required")
	}
	j.Phase = "export"
	j.State = "running"
	return j.save()
}
func (j *historyJob) verifyReceipt(ctx context.Context, u historyUnit, r historyReceipt) error {
	if r.Version != historyFormat || r.Unit != u.ID || r.VerifiedAt == "" {
		return errors.New("invalid historical completion receipt")
	}
	if strings.HasPrefix(u.Kind, "catalog-") && r.Tier == "unresolved" {
		if r.Gap == "" || r.Closed || r.Recovery != "" || r.OracleHash != j.OracleHash || !validRecordHash(r.OracleHash) {
			return errors.New("invalid unresolved catalog recovery receipt")
		}
		return nil
	}
	if r.Recovery != "" {
		var e historyRecovery
		path := historyRecoveryPath(j.Plan.CaptureRoot, r.Recovery)
		hash, _, err := fileSHA256Context(ctx, path)
		if err != nil {
			return err
		}
		if hash != r.Recovery {
			return errors.New("recovery envelope hash mismatch")
		}
		if err = historyReadJSON(path, &e); err != nil {
			return err
		}
		if e.OriginalID != u.Original || e.Kind != u.Kind || e.Tier != "catalog-derived" {
			return errors.New("recovery envelope identity mismatch")
		}
		return nil
	}
	if u.Kind == "view" {
		source, e := j.Plan.source(u.Host, u.Source)
		if e != nil {
			return e
		}
		dir := filepath.Dir(filepath.Join(historySourceDir(j.Plan.CaptureRoot, source), filepath.FromSlash(u.Path)))
		hash, _, e := fileSHA256Context(ctx, filepath.Join(dir, captureManifestName))
		if e != nil {
			return e
		}
		if hash != u.Version.SHA256 || hash != r.InputHash {
			return errors.New("historical view receipt identity mismatch")
		}
		m, e := historyLoadManifest(dir)
		if e != nil {
			return e
		}
		if m.Host.ID != u.Host || m.Source != source.Source || len(m.Snapshots) != 0 {
			return errors.New("unresolved historical view identity/closure")
		}
		for _, f := range m.Files {
			hash, size, e := fileSHA256Context(ctx, filepath.Join(dir, filepath.FromSlash(f.Captured)))
			if e != nil {
				return e
			}
			if hash != f.SHA256 || size != f.Size {
				return errors.New("historical view dependency mismatch")
			}
		}
	}
	if r.Revision != nil {
		source, e := j.Plan.source(u.Host, u.Source)
		if e != nil {
			return e
		}
		dir := historySourceDir(j.Plan.CaptureRoot, source)
		c, e := sourceCaptureForTarget(ctx, captureTarget{Host: source.Host, Source: source.Source.Name, Dir: dir})
		if e != nil {
			return e
		}
		if c.manifest.Source != source.Source {
			return errors.New("history source identity changed; reconcile plan")
		}
		s := newRecordStore(c)
		if u.Kind == "file" || u.Kind == "orphan-native" {
			if r.Revision.Hash != u.Version.SHA256 || r.InputHash != u.Version.SHA256 {
				return errors.New("raw input receipt mismatch")
			}
			return s.walkObject(*r.Revision, func(io.Reader) error { return nil })
		}
		v, e := s.loadRevision(*r.Revision)
		if e != nil {
			return e
		}
		if v.Host.ID != u.Host || v.Source != source.Source {
			return errors.New("logical receipt source mismatch")
		}
		if u.Kind == "logical" || u.Kind == "logical-orphan" {
			if u.Revision == nil || *u.Revision != *r.Revision {
				return errors.New("logical revision changed")
			}
		} else if u.Kind == "database" {
			if v.InputHash != u.Version.SHA256 || r.InputHash != u.Version.SHA256 || v.Database != u.Original || u.Version.Source == nil || v.Observed != recordSourceState(*u.Version.Source) {
				return errors.New("native export receipt identity mismatch")
			}
		}
		if e = s.verify(*r.Revision); e != nil {
			return e
		}
		if r.Closed && !isConductorSource(v.Source.Kind) {
			g, e := s.committedGroup(v.Acquisition)
			if e != nil {
				return e
			}
			found := false
			for _, ref := range g.Revisions {
				found = found || ref == *r.Revision
				if e = s.verify(ref); e != nil {
					return e
				}
			}
			if !found {
				return errors.New("export receipt lacks group membership")
			}
		}
	}

	return nil
}

func historyRecoveryPath(root, hash string) string {
	return filepath.Join(root, historyDir, "objects", hash+".json")
}
func (j *historyJob) exportUnit(ctx context.Context, u historyUnit) (historyReceipt, int64, error) {
	r := historyReceipt{Version: historyFormat, Unit: u.ID, VerifiedAt: now()}
	if u.Gap != "" {
		r.Tier = "unresolved"
		r.Gap = u.Gap
		return r, 0, nil
	}
	if u.Kind == "gap" {
		r.Tier = "unresolved"
		r.Gap = "unknown input inventory"
		return r, 0, nil
	}
	if strings.HasPrefix(u.Kind, "catalog-") {
		// This is the already consistent rollback snapshot, never the live WAL.
		db, e := openReadOnlySQLite(j.Oracle)
		if e != nil {
			return r, 0, e
		}
		defer db.Close()
		conn, e := db.Conn(ctx)
		if e != nil {
			return r, 0, e
		}
		defer conn.Close()
		if _, e = sqlite.Limit(conn, 0, int(historyEntityBytes)); e != nil {
			return r, 0, e
		}
		tx, e := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if e != nil {
			return r, 0, e
		}
		defer tx.Rollback()
		envelope, e := exportHistoryRecovery(ctx, tx, u, j.OracleHash)
		if e != nil {
			var sqliteErr *sqlite.Error
			tooBig := errors.Is(e, errHistoryEntityLimit) || errors.As(e, &sqliteErr) && sqliteErr.Code()&255 == sqlite3.SQLITE_TOOBIG
			// Exclusions are mandatory before replay: never continue without them.
			if ctx.Err() == nil && tooBig && (u.Kind == "catalog-workspace" || u.Kind == "catalog-tl1") {
				r.Tier, r.OracleHash = "unresolved", j.OracleHash
				r.Gap = fmt.Sprintf("catalog recovery exceeds the %d byte per-entity memory limit; preserved in rollback catalog %s (sha256 %s); output/scratch budgets do not change this limit", historyEntityBytes, j.Oracle, j.OracleHash)
				return r, 0, nil
			}
			return r, 0, e
		}
		// Snapshot provenance is in the receipt. Content identity excludes the
		// exporting job/snapshot, so a no-change pass reuses the same payload.
		envelope.OracleHash = ""
		b, e := json.Marshal(envelope)
		if e != nil {
			return r, 0, e
		}
		if int64(len(b)) > j.AttemptBytes {
			return r, 0, errors.New("history output budget exhausted")
		}
		hash := hashBytes(b)
		file := historyRecoveryPath(j.Plan.CaptureRoot, hash)
		if _, e = os.Stat(filepath.Join(j.Plan.CaptureRoot, historyDir)); e != nil {
			return r, 0, errors.New("history capture drive unavailable")
		}
		if e = os.MkdirAll(filepath.Dir(file), 0700); e != nil {
			return r, 0, e
		}
		objectLock, e := historyJobLock(filepath.Dir(file))
		if e != nil {
			return r, 0, e
		}
		defer objectLock.Close()
		if old, _, e := fileSHA256Context(ctx, file); e == nil {
			if old != hash {
				return r, 0, errors.New("existing recovery payload is corrupt")
			}
		} else if !errors.Is(e, os.ErrNotExist) {
			return r, 0, e
		} else if e = historyWriteJSON(file, envelope); e != nil {
			return r, 0, e
		}
		if e = historyFault("evidence", u.ID); e != nil {
			return r, 0, e
		}
		r.Recovery = hash
		r.OracleHash = j.OracleHash
		r.Tier = "catalog-derived"
		r.Closed = true
		return r, int64(len(b)), nil
	}
	source, e := j.Plan.source(u.Host, u.Source)
	if e != nil {
		return r, 0, e
	}
	dir := historySourceDir(j.Plan.CaptureRoot, source)
	lock, e := lockCaptureTarget(ctx, dir)
	if e != nil {
		return r, 0, e
	}
	defer lock.Close()
	c, e := sourceCaptureForTarget(ctx, captureTarget{Host: source.Host, Source: source.Source.Name, Dir: dir})
	if e != nil {
		return r, 0, e
	}
	if c.manifest.Source != source.Source {
		return r, 0, errors.New("history source identity changed; reconcile plan")
	}
	s := newRecordStore(c)
	s.budget.Bytes = min(s.budget.Bytes, j.AttemptBytes)
	switch u.Kind {
	case "logical", "logical-orphan":
		if u.Revision == nil {
			return r, 0, errors.New("missing revision")
		}
		if e = s.verify(*u.Revision); e != nil {
			return r, 0, e
		}
		v, e := s.loadRevision(*u.Revision)
		if e != nil {
			return r, 0, e
		}
		if u.Kind != "logical-orphan" && !isConductorSource(source.Source.Kind) {
			g, e := s.committedGroup(v.Acquisition)
			if e != nil {
				return r, 0, e
			}
			for _, ref := range g.Revisions {
				if e = s.verify(ref); e != nil {
					return r, 0, e
				}
			}
		}
		r.Revision = u.Revision
		r.Tier = "source-faithful"
		r.Closed = true
		if u.Kind == "logical-orphan" {
			r.Tier = "partial"
			r.Closed = false
			r.Gap = "raw row revision retained, historical dependency group unresolved"
		}
	case "view":
		inputDir := filepath.Dir(filepath.Join(dir, filepath.FromSlash(u.Path)))
		m, e := historyLoadManifest(inputDir)
		if e != nil {
			return r, 0, e
		}
		hash, _, e := fileSHA256Context(ctx, filepath.Join(inputDir, captureManifestName))
		if e != nil || hash != u.Version.SHA256 {
			return r, 0, errors.New("historical parsing inventory changed")
		}
		for _, f := range m.Files {
			hash, size, e := fileSHA256Context(ctx, filepath.Join(inputDir, filepath.FromSlash(f.Captured)))
			if e != nil || hash != f.SHA256 || size != f.Size {
				return r, 0, errors.New("historical view dependency failed verification")
			}
		}
		r.Tier = "source-faithful"
		r.InputHash = hash
		r.Closed = true
	case "database", "file", "orphan-native":
		pin, ok := j.Protected[u.ID]
		if !ok {
			return r, 0, errors.New("input is not protected")
		}
		file := filepath.Join(dir, filepath.FromSlash(pin.Path))
		hash, size, e := fileSHA256Context(ctx, file)
		if e != nil || hash != pin.SHA256 || size != pin.Size {
			return r, 0, errors.New("pinned input changed or drive unavailable")
		}
		r.InputHash = hash
		if u.Kind == "database" && u.Version.Source != nil && isConductorSource(source.Source.Kind) {
			if old, ok := c.verifiedSnapshotReceipt(pin.Path, pin.Path); ok {
				r.Revision = &old.Revision
				r.Tier = "source-faithful"
				r.Closed = true
				break
			}
			s.inputCapturedAt = u.Version.CapturedAt
			ref, e := s.exportSQLite(file, u.Original, "database", *u.Version.Source, "", nil)
			if e != nil {
				return r, s.written, e
			}
			if e = s.verify(ref); e != nil {
				return r, s.written, e
			}
			v, e := s.loadRevision(ref)
			if e != nil {
				return r, s.written, e
			}
			receipt := sqliteExportReceipt{Version: recordFormat, Input: pin.Path, InputHash: v.InputHash, Revision: ref, VerifiedAt: now()}
			if e = historyWriteJSON(filepath.Join(dir, recordDir, "receipts", hashBytes([]byte(pin.Path))+".json"), receipt); e != nil {
				return r, s.written, e
			}
			r.Revision = &ref
			r.Tier = "source-faithful"
			r.Closed = true
		} else if u.Kind == "database" { // Never invent historical cross-input closure.
			if old, ok := c.verifiedSnapshotReceipt(pin.Path, pin.Path); ok {
				r.Revision = &old.Revision
				r.Tier = "source-faithful"
				r.Closed = true
				break
			}
			s.inputCapturedAt = u.Version.CapturedAt
			if u.Version.Source != nil {
				ref, err := s.exportSQLite(file, u.Original, "database", *u.Version.Source, "", nil)
				if err != nil {
					return r, s.written, err
				}
				r.Revision = &ref
			}
			r.Tier = "partial"
			r.Gap = "native SQLite rows/bytes pinned; historical database/file group unresolved"
		} else {
			f, e := os.Open(file)
			if e != nil {
				return r, 0, e
			}
			ref, e := s.put(f)
			f.Close()
			if e != nil {
				return r, s.written, e
			}
			r.Revision = &ref
			r.Tier = "partial"
			r.Gap = "original bytes retained; historical parsing dependency group unresolved"
		}
	default:
		return r, 0, errors.New("unsupported historical unit")
	}
	return r, s.written, nil
}
func (j *historyJob) releaseClosed(ctx context.Context, u historyUnit, r historyReceipt) error {
	if !r.Closed || u.Host == "" {
		return nil
	}
	pin, ok := j.Protected[u.ID]
	if !ok {
		return nil
	}
	source, e := j.Plan.source(u.Host, u.Source)
	if e != nil {
		return e
	}
	dir := historySourceDir(j.Plan.CaptureRoot, source)
	lock, e := lockCaptureTarget(ctx, dir)
	if e != nil {
		return e
	}
	defer lock.Close()
	p, e := loadPreservationPins(dir)
	if e != nil {
		return e
	}

	// Release exactly this unit, never another owner's or unresolved unit's pin.
	for i := range p.Pins {
		if p.Pins[i].Owner == j.ID && p.Pins[i].ID == pin.ID {
			p.Pins[i].ReleasedAt = now()
		}
	}
	return savePreservationPins(dir, p)
}
func runHistory(ctx context.Context, config Config, id string) (historyJob, error) {
	j, e := loadHistoryJob(config.CaptureRoot, id)
	if e != nil {
		return j, e
	}
	lock, e := historyJobLock(j.dir())
	if e != nil {
		return j, e
	}
	defer lock.Close()
	fail := func(e error) (historyJob, error) { j.State = "pending"; j.Error = e.Error(); _ = j.save(); return j, e }
	if j.Phase == "protecting" {
		if e = j.protect(ctx); e != nil {
			return fail(e)
		}
	}
	if j.Phase == "rollback-backup" {
		if e = j.rollback(ctx, config); e != nil {
			return fail(e)
		}
	}
	if j.Phase == "export" {
		// Resume trusts neither a replaced backup nor a path whose volume vanished.
		hash, _, err := fileSHA256Context(ctx, j.Oracle)
		if err != nil || hash != j.OracleHash {
			return fail(errors.New("rollback catalog changed or unavailable; reconcile job"))
		}
		if j.AttemptUnit != "" {
			committed := false
			for _, unit := range j.Plan.Units {
				if unit.ID == j.AttemptUnit {
					var receipt historyReceipt
					if err := historyReadJSON(j.receiptPath(unit.ID), &receipt); err == nil && j.verifyReceipt(ctx, unit, receipt) == nil {
						committed = true
					}
					break
				}
			}
			if !committed {
				j.UncertainOutputBytes += j.AttemptBytes
			}
			j.AttemptUnit = ""
			j.AttemptBytes = 0
		}
		j.OutputBytes = j.UncertainOutputBytes
		for _, unit := range j.Plan.Units {
			var receipt historyReceipt
			if err := historyReadJSON(j.receiptPath(unit.ID), &receipt); err == nil {
				if receipt.OutputBytes < 0 || receipt.OutputBytes > j.OutputBudget || j.OutputBytes > j.OutputBudget-receipt.OutputBytes {
					return fail(errors.New("invalid output accounting receipt"))
				}
				j.OutputBytes += receipt.OutputBytes
			} else if !errors.Is(err, os.ErrNotExist) {
				return fail(err)
			}
		}
		if j.OutputBytes > j.OutputBudget {
			return fail(errors.New("history output budget exhausted"))
		}
		for _, u := range j.Plan.Units {
			if e = ctx.Err(); e != nil {
				return fail(e)
			}
			var r historyReceipt
			e = historyReadJSON(j.receiptPath(u.ID), &r)
			if e == nil {
				if e = j.verifyReceipt(ctx, u, r); e != nil {
					return fail(e)
				}
			} else if !errors.Is(e, os.ErrNotExist) {
				return fail(e)
			} else {
				remaining := j.OutputBudget - j.OutputBytes
				allowance := int64(0)
				if u.Gap == "" {
					switch u.Kind {
					case "catalog-workspace", "catalog-tl1", "catalog-exclusions":
						allowance = min(remaining, historyMetadataLimit)
					case "database":
						estimated := recordMaxBytes
						if u.Version.Size < (recordMaxBytes-(1<<20))/4 {
							estimated = max(int64(16<<20), u.Version.Size*4+(1<<20))
						}
						allowance = min(remaining, estimated)
					case "file", "orphan-native":
						allowance = min(remaining, u.Version.Size+(128<<10))
					}
				}
				if allowance <= 0 && u.Gap == "" && (u.Kind == "database" || u.Kind == "file" || u.Kind == "orphan-native" || strings.HasPrefix(u.Kind, "catalog-")) {
					return fail(errors.New("history output budget exhausted; review status and increase the explicit job budget"))
				}
				j.AttemptUnit = u.ID
				j.AttemptBytes = allowance
				if e = j.save(); e != nil {
					return fail(e)
				}
				var bytes int64
				r, bytes, e = j.exportUnit(ctx, u)
				if e != nil {
					return fail(e)
				}
				if e = historyFault("publication", u.ID); e != nil {
					return fail(e)
				}
				if e = j.verifyReceipt(ctx, u, r); e != nil {
					return fail(e)
				}
				r.OutputBytes = bytes
				if e = historyWriteJSON(j.receiptPath(u.ID), r); e != nil {
					return fail(e)
				}
				j.OutputBytes += bytes
				j.AttemptUnit = ""
				j.AttemptBytes = 0
			}
			if e = historyFault("receipt", u.ID); e != nil {
				return fail(e)
			}
			j.Cursor = u.ID
			if e = j.save(); e != nil {
				return fail(e)
			}
			if e = historyFault("cursor", u.ID); e != nil {
				return fail(e)
			}
			if e = j.releaseClosed(ctx, u, r); e != nil {
				return fail(e)
			}
		}
		j.Phase = "verify"
		j.State = "exported"
		j.Error = ""
		if e = j.save(); e != nil {
			return fail(e)
		}
	}
	return j, nil
}

// A safe cancellation keeps unresolved pins and pauses. Only a verified job
// or an explicit operator release may remove this owner's native pause.
func (j *historyJob) finishPauses(ctx context.Context, allowUnresolved bool) error {
	for _, source := range j.Plan.Sources {
		dir := historySourceDir(j.Plan.CaptureRoot, source)
		lock, e := lockCaptureTarget(ctx, dir)
		if e != nil {
			return e
		}
		e = func() error {
			defer lock.Close()
			p, e := loadPreservationPins(dir)
			if e != nil {
				return e
			}
			if !allowUnresolved {
				for _, u := range j.Plan.Units {
					if u.Host != source.Host.ID || u.Source != source.Source.Name {
						continue
					}
					var receipt historyReceipt
					if err := historyReadJSON(j.receiptPath(u.ID), &receipt); err != nil || !receipt.Closed {
						return errors.New("unresolved historical inputs keep native snapshots pending; inspect the job before releasing protection")
					}
				}
			}
			for _, pin := range p.Pins {
				if pin.Owner == j.ID && pin.ReleasedAt == "" && !allowUnresolved {
					return errors.New("unresolved historical inputs remain pinned; native snapshots stay pending")
				}
			}
			if allowUnresolved {
				for i := range p.Pins {
					if p.Pins[i].Owner == j.ID {
						p.Pins[i].ReleasedAt = now()
					}
				}
			}
			delete(p.NativePauses, j.ID)
			return savePreservationPins(dir, p)
		}()
		if e != nil {
			return e
		}
	}
	return nil
}

func hasHistorySQLite(plan historyPlan, source historySource) bool {
	for _, u := range plan.Units {
		if u.Host == source.Host.ID && u.Source == source.Source.Name && (u.Kind == "database" || u.Kind == "logical" || u.Kind == "logical-orphan") {
			return true
		}
	}
	return false
}
