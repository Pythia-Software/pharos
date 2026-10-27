package archive

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

type sqliteCoverageStatus struct {
	Overdue         bool   `json:"overdue"`
	IntervalSeconds int64  `json:"interval_seconds"`
	LastAttemptAt   string `json:"last_attempt_at,omitempty"`
	LastCompleteAt  string `json:"last_complete_at,omitempty"`
	Pending         bool   `json:"pending"`
	Reason          string `json:"reason,omitempty"`
}

func recordInterval(source SourceConfig) time.Duration {
	if hours := integer(source.Options["sqlite_reconciliation_hours"]); hours > 0 && hours <= 24 {
		return time.Duration(hours) * time.Hour
	}
	return recordReconcileInterval
}

// A source's file preflight can remain unchanged after reconciliation becomes
// due. Only completed, current coverage may take that shortcut; missing/busy
// or interrupted scans must get another planning pass.
func (c *Catalog) sqliteReconciliationCurrent(source SourceConfig) (bool, error) {
	m, err := loadCaptureManifest(filepath.Join(c.captureRootPath(), currentHost().ID, source.Name))
	if err != nil {
		return false, err
	}
	if len(m.RecordCoverage) == 0 {
		return false, nil
	}
	for _, status := range m.RecordCoverage {
		last, valid := parseTime(status.LastCompleteAt)
		if status.Pending || !valid || time.Since(last) >= recordInterval(source) {
			return false, nil
		}
	}
	return true, nil
}

func beginSQLiteRecordSource(ctx context.Context, root string, source SourceConfig) (*sourceCapture, error) {
	if !validCaptureName(source.Name) {
		return nil, errors.New("invalid SQLite capture source name")
	}
	session, e := beginCapture(Config{CaptureRoot: root, CaptureGenerations: 1})
	if e != nil {
		return nil, e
	}
	fail := func(e error) (*sourceCapture, error) { session.Close(); return nil, e }
	dir := filepath.Join(session.hostDir, source.Name)
	if e = os.MkdirAll(dir, 0700); e != nil {
		return fail(e)
	}
	m, e := loadCaptureManifest(dir)
	if e != nil {
		return fail(e)
	}
	if !m.acceptsSource(session.host, source) {
		return fail(errors.New("SQLite capture source identity changed; choose a separate source name"))
	}
	c := &sourceCapture{ctx: ctx, dir: dir, session: session, source: source, manifest: m, files: map[string]*capturedFile{}, snapshots: map[string]*capturedSnapshot{}}
	c.result.StartedAt = now()
	for _, f := range m.Files {
		c.files[f.Captured] = f
	}
	for _, v := range m.Snapshots {
		c.snapshots[v.Captured] = v
	}
	if _, e = loadPreservationPins(dir); e != nil {
		return fail(e)
	}
	removeCaptureTemporaries(dir)
	rootRel := "files"
	if info, e := os.Stat(source.Path); e != nil {
		return fail(e)
	} else if !info.IsDir() {
		rootRel = filepath.ToSlash(filepath.Join("files", filepath.Base(source.Path)))
	}
	c.mergeMappings(capturePlan{pathMap: []capturePathMapping{{Original: source.Path, Captured: rootRel}}})
	if e = c.commit(false); e != nil {
		return fail(e)
	}
	return c, nil
}
func reconcileSQLiteLive(ctx context.Context, root string, source SourceConfig, database string, force bool) error {
	c, e := beginSQLiteRecordSource(ctx, root, source)
	if e != nil {
		return e
	}
	defer c.session.Close()
	s := newRecordStore(c)
	h, e := s.head(database, "database")
	if e != nil {
		return e
	}
	interval := recordInterval(source)
	last, valid := parseTime(h.LastFullAt)
	if !force && valid && time.Since(last) < interval {
		return nil
	}
	status := sqliteCoverageStatus{IntervalSeconds: int64(interval / time.Second), LastAttemptAt: now(), LastCompleteAt: h.LastFullAt, Pending: true, Reason: "reconciliation in progress"}
	if c.manifest.RecordCoverage == nil {
		c.manifest.RecordCoverage = map[string]sqliteCoverageStatus{}
	}
	c.manifest.RecordCoverage[database] = status
	if e = c.commit(false); e != nil {
		return e
	}
	state, e := sqliteDependencyState(database)
	if e == nil {
		e = reserveCaptureAcquisition(c.dir, min(state.Size, recordMaxBytes))
	}
	if e == nil {
		_, e = s.exportSQLite(database, database, "database", state, "", nil)
	}
	if e == nil {
		head, err := s.head(database, "database")
		if err != nil {
			return err
		}
		status.LastCompleteAt = head.LastFullAt
		status.Pending = false
		status.Reason = ""
	} else {
		status.Reason = "reconciliation incomplete; retry or increase the documented budget"
	}
	c.manifest.RecordCoverage[database] = status
	if save := c.commit(false); save != nil {
		return save
	}
	return e
}

func runSQLiteRecordsCLI(config Config, args []string) error {
	if len(args) < 1 {
		return errors.New("usage: pharos sqlite-records status|verify|replay HOST SOURCE; reconcile SOURCE; pin HOST SOURCE OWNER PATH; release HOST SOURCE OWNER; gate HOST SOURCE OWNER BYTES; ungate HOST SOURCE OWNER")
	}
	if args[0] == "reconcile" {
		if len(args) != 2 {
			return errors.New("usage: pharos sqlite-records reconcile SOURCE")
		}
		sources, e := captureSources(config, args[1:])
		if e != nil {
			return e
		}
		source := sources[0]
		if !isConductorSource(source.Kind) {
			return errors.New("live logical reconciliation currently supports Conductor; use capture SOURCE for a verified database/file group")
		}
		plan, e := planCapture(source)
		if e != nil {
			return e
		}
		if !plan.complete {
			return errors.New("incomplete database discovery")
		}
		if m, err := loadCaptureManifest(filepath.Join(config.CaptureRoot, currentHost().ID, source.Name)); err == nil {
			includeKnownCaptureDatabases(&plan, m, source)
		} else {
			return err
		}
		if len(plan.databases) == 0 {
			return errors.New("no supported SQLite database; no reconciliation receipt issued")
		}

		for _, db := range plan.databases {
			if e = reconcileSQLiteLive(context.Background(), config.CaptureRoot, source, db.path, true); e != nil {
				return e
			}
		}
		return printJSON(map[string]any{"source": source.Name, "state": "complete"})
	}
	if len(args) < 3 || !validCaptureName(args[1]) || !validCaptureName(args[2]) {
		return errors.New("expected HOST SOURCE")
	}
	target := captureTarget{Host: capturedHost(filepath.Join(config.CaptureRoot, args[1]), args[1]), Source: args[2], Dir: filepath.Join(config.CaptureRoot, args[1], args[2])}
	if args[0] == "replay" {
		if len(args) != 3 {
			return errors.New("usage: pharos sqlite-records replay HOST SOURCE")
		}
		// The running service uses this same catalog-wide ingest lease. Take
		// it before opening the catalog or publishing any reader pins.
		ingest, e := lockCatalogIngest(config.CatalogPath)
		if e != nil {
			return e
		}
		defer ingest.Close()
		reader, close, e := openSQLiteReplay(context.Background(), target)
		if e != nil {
			return e
		}
		if reader == nil {
			return errors.New("no committed SQLite records")
		}
		defer close()
		catalog, e := OpenCatalog(config.CatalogPath)
		if e != nil {
			return e
		}
		defer catalog.Close()
		catalog.setCaptureRoot(config.CaptureRoot)
		result := catalog.IngestContext(withIngestMode(context.Background(), ingestMode{force: true}), reader, nil)
		if e = printJSON(result); e != nil {
			return e
		}
		if result.Error != nil {
			return errors.New("SQLite replay failed; evidence retained")
		}
		return nil
	}
	lock, e := lockCaptureTarget(context.Background(), target.Dir)
	if e != nil {
		return e
	}
	defer lock.Close()
	c, e := sourceCaptureForTarget(context.Background(), target)
	if e != nil {
		return e
	}
	s := newRecordStore(c)
	switch args[0] {
	case "resolve":
		if len(args) != 5 {
			return errors.New("usage: pharos sqlite-records resolve HOST SOURCE REVISION LOCATOR")
		}
		field, e := s.resolveLocator(args[3], args[4])
		if e != nil {
			return e
		}
		return printJSON(field)
	case "status", "verify":
		if len(args) != 3 {
			return errors.New("expected HOST SOURCE only")
		}
		revs, e := s.revisions()
		if e != nil {
			return e
		}
		pins, e := loadPreservationPins(c.dir)
		if e != nil {
			return e
		}
		var bytes int64
		owners := map[string]int64{}
		for _, pin := range pins.Pins {
			if pin.ReleasedAt == "" {
				cost := max(pin.ClosureBytes, pin.Size)
				owners[pin.Owner] += cost
				bytes += cost
			}
		}
		if args[0] == "verify" {
			for _, r := range revs {
				if e = s.verify(r.Ref); e != nil {
					return e
				}
				for _, other := range r.Companions {
					if e = s.verify(other.Ref); e != nil {
						return e
					}
				}
			}
		}
		partials, _ := os.ReadDir(filepath.Join(c.dir, recordDir, "partial"))
		return printJSON(map[string]any{"version": recordFormat, "writer": recordWriter, "revisions": len(revs), "coverage": sqliteCoverageForStatus(c.manifest), "active_pin_owners": owners, "reader_cleanup_pending": pins.pendingReaders, "pinned_bytes": bytes, "partial_acquisitions": len(partials), "acquisition_allowances": pins.Gates, "native_snapshot_pauses": pins.NativePauses, "automatic_cutover": "gated; sqlite_record_capture is opt-in"})
	case "pin":
		if len(args) != 5 {
			return errors.New("usage: pharos sqlite-records pin HOST SOURCE OWNER RETAINED_PATH")
		}
		var version *capturedVersion
		for _, snapshot := range c.snapshots {
			v := capturedVersion{Captured: snapshot.Captured, Size: snapshot.Size, CapturedAt: snapshot.CapturedAt, Source: &snapshot.Source}
			if v.Captured == args[4] {
				version = &v
			}
			for _, v := range snapshot.Generations {
				if v.Captured == args[4] {
					version = &v
				}
			}
		}
		if version == nil {
			for _, file := range c.files {
				v := capturedVersion{Captured: file.Captured, Size: file.Size, CapturedAt: file.CapturedAt, MTimeNS: file.MTimeNS, ObservedNS: file.ObservedNS, SHA256: file.SHA256}
				if v.Captured == args[4] {
					version = &v
				}
				for _, v := range file.Previous {
					if v.Captured == args[4] {
						version = &v
					}
				}
			}
		}
		if version == nil {
			return errors.New("pin input is absent from the inventory; reconcile the plan")
		}
		pin, e := c.pinVersion(args[3], "operator preservation job", *version)
		if e != nil {
			return e
		}
		return printJSON(pin)
	case "release":
		if len(args) != 4 || args[3] == "" {
			return errors.New("expected one exact pin owner")
		}
		return releasePreservationOwner(c.dir, args[3])
	case "gate", "ungate":
		if len(args) < 4 || args[3] == "" {
			return errors.New("expected one exact job owner")
		}
		p, e := loadPreservationPins(c.dir)
		if e != nil {
			return e
		}
		if p.Gates == nil {
			p.Gates = map[string]int64{}
		}
		if args[0] == "gate" {
			if len(args) != 5 {
				return errors.New("expected byte allowance (0 pauses acquisition)")
			}
			n, e := strconv.ParseInt(args[4], 10, 64)
			if e != nil || n < 0 {
				return errors.New("invalid capacity allowance")
			}
			p.Gates[args[3]] = n
		} else {
			if len(args) != 4 {
				return errors.New("expected job owner only")
			}
			delete(p.Gates, args[3])
		}
		if e = c.upgradePreservationWriter(); e != nil {
			return e
		}
		return savePreservationPins(c.dir, p)
	default:
		return errors.New("unknown sqlite-records command")
	}
}

func checkSQLiteRecordCompatibility(dir string) error {
	entries, e := os.ReadDir(filepath.Join(dir, "records"))
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil {
		return e
	}
	for _, entry := range entries {
		if entry.Name() != "v1" {
			return errors.New("unsupported SQLite capture format; upgrade Pharos before writing or replaying this source")
		}
	}
	return nil
}

func retainedConductorIdentities(ctx context.Context, root string, source SourceConfig, database string) (map[string]string, error) {
	target := captureTarget{Host: currentHost(), Source: source.Name, Dir: filepath.Join(root, currentHost().ID, source.Name)}
	c, e := sourceCaptureForTarget(ctx, target)
	if e != nil {
		return nil, e
	}
	s := newRecordStore(c)
	head, e := s.head(database, "database")
	if e != nil || head.Version == 0 {
		return nil, e
	}
	v, e := s.loadRevision(head.Revision)
	if e != nil {
		return nil, e
	}
	return v.EntityIdentities, nil
}

func sqliteCoverageForStatus(manifest *captureManifest) map[string]sqliteCoverageStatus {
	result := map[string]sqliteCoverageStatus{}
	for database, status := range manifest.RecordCoverage {
		at, valid := parseTime(status.LastCompleteAt)
		status.Overdue = status.Pending || !valid || time.Since(at) >= time.Duration(status.IntervalSeconds)*time.Second
		result[database] = status
	}
	return result
}
