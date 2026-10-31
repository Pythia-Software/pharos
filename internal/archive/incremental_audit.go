package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

// An automatic audit verifies a unit of at most automaticAuditBytes, and a
// Conductor unit, whose size counts messages, of at most
// automaticAuditConductorMessages. Tests lower them.
var (
	automaticAuditBytes             = int64(16 << 20)
	automaticAuditConductorMessages = int64(2000)
)

const automaticAuditWall = 5 * time.Second

type verificationSummary struct {
	Checked  int `json:"checked"`
	Passed   int `json:"passed"`
	Mismatch int `json:"mismatch"`
	Deferred int `json:"deferred"`
	Errors   int `json:"errors"`
}

type auditResult struct {
	Outcome       string         `json:"outcome"`
	Source        string         `json:"source,omitempty"`
	Unit          string         `json:"unit,omitempty"`
	Detail        string         `json:"detail,omitempty"`
	Differences   []string       `json:"differences,omitempty"`
	Seconds       float64        `json:"seconds"`
	Discovered    int            `json:"discovered_units"`
	Deferred      int            `json:"deferred_units"`
	Reason        string         `json:"reason,omitempty"`
	EvidenceBytes int64          `json:"evidence_bytes,omitempty"`
	Attempts      []auditAttempt `json:"attempts,omitempty"`
}

type auditDeferral struct {
	Reason, Detail string
}

func (e *auditDeferral) Error() string { return e.Detail }

type auditAttempt struct {
	Source        string  `json:"source"`
	Unit          string  `json:"unit"`
	Outcome       string  `json:"outcome"`
	Reason        string  `json:"reason,omitempty"`
	Detail        string  `json:"detail,omitempty"`
	Seconds       float64 `json:"seconds"`
	EvidenceBytes int64   `json:"evidence_bytes,omitempty"`
}

type auditPool struct {
	catalog        *Catalog
	units          []syncUnit
	checks         map[string]string
	outcomes       map[string]string
	issues         map[string]auditIssue
	retries        map[string]bool
	versions       map[string]string
	progress       map[string]auditProgress
	sourceAttempts map[string]string
	discovered     int
	deferred       int
	failure        error
}

// auditIssue is what an open mismatch was found against.
type auditIssue struct {
	IndexVersion    string `json:"index_version"`
	SourceSignature string `json:"source_signature"`
}

func newAuditPool(catalog *Catalog) *auditPool {
	pool := &auditPool{catalog: catalog, checks: map[string]string{}, outcomes: map[string]string{}, issues: map[string]auditIssue{}, retries: map[string]bool{}, versions: map[string]string{}, progress: map[string]auditProgress{}, sourceAttempts: map[string]string{}}
	rows, err := catalog.reader().Query(`SELECT v.source_name,v.unit,v.outcome,
		COALESCE(p.verified_at,CASE WHEN v.outcome IN ('passed','mismatch') THEN v.checked_at END,''),
		COALESCE(p.attempted_at,v.checked_at),COALESCE(p.next_at,''),COALESCE(p.heavy,0),COALESCE(p.signature,'')
		FROM sync_verifications v LEFT JOIN sync_verification_progress p USING(host_id,source_name,unit)
		WHERE v.host_id=?`, currentHost().ID)
	pool.failure = err
	if err == nil {
		for rows.Next() {
			var source, unit, outcome, verified, attempted, next, signature string
			var heavy int
			if err = rows.Scan(&source, &unit, &outcome, &verified, &attempted, &next, &heavy, &signature); err != nil {
				break
			}
			key := source + "/" + unit
			pool.checks[key], pool.outcomes[key] = verified, outcome
			pool.progress[key] = auditProgress{attempted: attempted, next: next, signature: signature, heavy: heavy != 0}
			pool.sourceAttempts[source] = max(pool.sourceAttempts[source], attempted)
		}
		pool.failure = errors.Join(err, rows.Err(), rows.Close())
	}
	issues, err := queryMaps(catalog.DB, "SELECT source_name,unit,detail_json FROM sync_integrity_issues WHERE host_id=? AND state='open'", currentHost().ID)
	if pool.failure == nil {
		pool.failure = err
	}
	for _, row := range issues {
		var issue auditIssue
		json.Unmarshal([]byte(firstString(row["detail_json"])), &issue)
		pool.issues[firstString(row["source_name"])+"/"+firstString(row["unit"])] = issue
	}
	return pool
}

// retry reports whether a unit's open mismatch was found by other code or in
// another version of its input. Auditing it again may now pass and resolve
// it, so it goes before the least recently verified units. An unchanged
// mismatch stays in the normal rotation. Attempts without a verdict have
// separate backoff and do not advance completed verification.
func (pool *auditPool) retry(unit syncUnit) bool {
	key := unit.Source.Name + "/" + unit.ID
	issue, open := pool.issues[key]
	if !open || pool.outcomes[key] != "mismatch" {
		return false
	}
	version, known := pool.versions[unit.Source.Name]
	if !known {
		version = sourceIndexVersion(mustAuditAdapter(unit.Source))
		pool.versions[unit.Source.Name] = version
	}
	return issue.IndexVersion != version || issue.SourceSignature != partsSignature(unit.Parts)
}

func (pool *auditPool) consider(unit syncUnit) {
	pool.discovered++
	if unit.Source.Kind != "conductor" && time.Since(time.Unix(0, unit.Version)) < 30*time.Second {
		pool.deferred++
		return
	}
	key := func(unit syncUnit) string { return unit.Source.Name + "/" + unit.ID }
	progress := pool.progress[key(unit)]
	if progress.signature == partsSignature(unit.Parts) {
		if next, ok := parseTime(progress.next); progress.heavy || ok && time.Now().Before(next) {
			pool.deferred++
			return
		}
	}
	if pool.retry(unit) {
		pool.retries[key(unit)] = true
	}
	pool.units = append(pool.units, unit)
	sort.SliceStable(pool.units, func(left, right int) bool {
		first, second := key(pool.units[left]), key(pool.units[right])
		if pool.retries[first] != pool.retries[second] {
			return pool.retries[first]
		}
		if a, b := pool.sourceAttempts[pool.units[left].Source.Name], pool.sourceAttempts[pool.units[right].Source.Name]; a != b {
			return a < b
		}
		if pool.checks[first] != pool.checks[second] {
			return pool.checks[first] < pool.checks[second]
		}
		return pool.progress[first].attempted < pool.progress[second].attempted
	})
	if len(pool.units) > 64 {
		seen := map[string]bool{}
		first, rest := []syncUnit{}, []syncUnit{}
		for _, candidate := range pool.units {
			if !seen[candidate.Source.Name] {
				seen[candidate.Source.Name] = true
				first = append(first, candidate)
			} else {
				rest = append(rest, candidate)
			}
		}
		pool.units = append(first, rest...)[:64]
	}
}

func (pool *auditPool) preflightSkipped(source SourceConfig) {
	rows, err := pool.catalog.reader().Query(`SELECT p.item,p.size,p.version,COALESCE(p.signal,'') signal FROM source_item_states p
		LEFT JOIN sync_verifications v ON v.host_id=p.host_id AND v.source_name=p.source_name AND v.unit=p.item
		LEFT JOIN sync_integrity_issues i ON i.host_id=p.host_id AND i.source_name=p.source_name AND i.unit=p.item AND i.state='open'
		LEFT JOIN sync_verification_progress a ON a.host_id=p.host_id AND a.source_name=p.source_name AND a.unit=p.item
		WHERE p.host_id=? AND p.source_name=? ORDER BY i.id IS NULL,COALESCE(a.verified_at,CASE WHEN v.outcome IN ('passed','mismatch') THEN v.checked_at END,''),COALESCE(a.attempted_at,v.checked_at,''),p.item`, currentHost().ID, source.Name)
	if err != nil {
		pool.failure = err
		return
	}
	defer rows.Close()
	// Stream past queued/backed-off inputs so they cannot fill a SQL LIMIT
	// and hide eligible work. consider retains only the bounded candidate set.
	for rows.Next() {
		var part sourcePart
		if err := rows.Scan(&part.item, &part.size, &part.version, &part.signal); err != nil {
			pool.failure = err
			return
		}
		pool.consider(unitOf(source, []sourcePart{part}, false))
	}
	pool.failure = errors.Join(pool.failure, rows.Err())
}

func (c *Catalog) auditPool(ctx context.Context, pool *auditPool, runID string) auditResult {
	if pool.failure != nil {
		return auditResult{Outcome: "read_error", Detail: pool.failure.Error(), Discovered: pool.discovered, Deferred: pool.deferred}
	}
	if len(pool.units) == 0 {
		return auditResult{Outcome: "deferred", Reason: "no_eligible_input", Detail: "no eligible input; changing inputs, queued checks and retries remain pending", Discovered: pool.discovered, Deferred: pool.deferred}
	}
	ctx, cancel := context.WithTimeout(ctx, automaticAuditWall)
	defer cancel()
	started := time.Now()
	attempts := []auditAttempt{}
	deferred := pool.deferred
	result := auditResult{}
	for _, unit := range pool.units[:min(automaticAuditAttempts, len(pool.units))] {
		if ctx.Err() != nil {
			break
		}
		result = c.auditUnit(ctx, unit, runID, automaticAuditBytes)
		attempts = append(attempts, auditAttempt{Source: result.Source, Unit: result.Unit, Outcome: result.Outcome, Reason: result.Reason, Detail: result.Detail, Seconds: result.Seconds, EvidenceBytes: result.EvidenceBytes})
		if result.Outcome == "deferred" {
			deferred++
		}
		if err := c.recordVerification(unit, result); err != nil {
			result.Outcome, result.Detail = "read_error", err.Error()
			break
		}
		if result.Outcome != "deferred" {
			break
		}
	}
	if len(attempts) == 0 {
		reason := "time_budget"
		if ctx.Err() == context.Canceled {
			reason = "cancelled"
		}
		result = auditResult{Outcome: "deferred", Reason: reason, Detail: "verification did not start before cancellation or deadline"}
	}
	result.Discovered, result.Deferred, result.Attempts, result.Seconds = pool.discovered, deferred, attempts, time.Since(started).Seconds()
	return result
}

func partsSignature(parts []sourcePart) string {
	values := []string{}
	for _, part := range parts {
		values = append(values, fmt.Sprintf("%s:%d:%d:%s", part.item, part.size, part.version, part.signal))
	}
	slices.Sort(values)
	return hashBytes([]byte(strings.Join(values, "\n")))
}

func (c *Catalog) auditUnit(ctx context.Context, unit syncUnit, runID string, budget int64) (result auditResult) {
	started := time.Now()
	result = auditResult{Outcome: "read_error", Source: unit.Source.Name, Unit: unit.ID}
	defer func() {
		result.Seconds = time.Since(started).Seconds()
		if result.Outcome == "read_error" && ctx.Err() != nil {
			result.Outcome = "deferred"
			result.Detail = "verification budget or cancellation"
		}
		if result.Outcome == "deferred" && result.Reason == "" {
			switch {
			case ctx.Err() == context.DeadlineExceeded:
				result.Reason = "time_budget"
			case ctx.Err() != nil:
				result.Reason = "cancelled"
			case strings.Contains(result.Detail, "budget") || strings.Contains(result.Detail, "large group"):
				result.Reason = "evidence_size"
			case strings.Contains(result.Detail, "rollout") || strings.Contains(result.Detail, "requires sqlite_record_capture"):
				result.Reason = "unsupported_capture"
			default:
				result.Reason = "source_changed"
			}
		}
		if result.Outcome == "read_error" && result.Reason == "" {
			result.Reason = "read_error"
		}
	}()
	if budget > 0 && (unit.Bytes > budget || (budget == automaticAuditBytes && (len(unit.Parts) > 64 || (unit.Source.Kind == "conductor" && unit.Bytes > automaticAuditConductorMessages)))) {
		result.Outcome, result.Detail = "deferred", "selected input exceeds quick verification budget"
		return
	}
	if !scopedCodexAudit(unit) && (unit.Reader == nil || captureViewOf(unit.Reader) == nil) && retainedAcquisitionEnabled(unit.Source) {
		adapter := unit.Reader
		var err error
		if adapter == nil {
			adapter, err = MakeAdapter(unit.Source)
		}
		if err != nil {
			result.Detail = err.Error()
			return
		}
		var input retainedInput
		if unit.Source.Kind == "codex" && unit.Source.Options["sqlite_record_capture"] != true {
			plan, e := planCapture(unit.Source)
			if e != nil {
				result.Detail = e.Error()
				return
			}
			var bytes int64
			for _, file := range plan.files {
				if info, e := os.Stat(file.path); e == nil {
					bytes += info.Size()
				} else {
					result.Detail = e.Error()
					return
				}
			}
			for _, database := range plan.databases {
				if state, e := sqliteDependencyState(database.path); e == nil {
					bytes += state.Size + state.WALSize
				} else {
					result.Detail = e.Error()
					return
				}
			}
			if budget > 0 && bytes > budget {
				result.Outcome, result.Detail = "deferred", "dependency group exceeds verification budget"
				return
			}
			if len(plan.databases) > 0 {
				result.Outcome, result.Detail = "deferred", "SQLite record verification requires sqlite_record_capture"
				return
			} else {
				input, err = c.acquireRetainedInput(ctx, adapter, nil)
			}
		} else if unit.Source.Kind == "antigravity" && unit.Source.Options["sqlite_record_capture"] != true {
			input, err = c.acquireAuditFilesystemGroup(ctx, unit)
		} else {
			input, err = c.acquireRetainedInput(ctx, adapter, nil)
		}
		if err != nil {
			result.Detail = err.Error()
			if errors.Is(err, errCaptureBusy) || errors.Is(err, errAuditUnstable) {
				result.Outcome = "deferred"
			}
			return
		}
		defer input.Close()
		if captureViewOf(input.Adapter) == nil && isConductorSource(unit.Source.Kind) {
			ctx = context.WithValue(ctx, sqliteAcquisitionKey{}, &sqliteAcquisitionBoundary{root: c.captureRootPath(), reconciled: map[string]bool{}, identities: map[string]map[string]string{}})
		} else if captureViewOf(input.Adapter) == nil {
			// Live SQLite audit never certifies a temporary copy as durable.
			result.Outcome, result.Detail = "deferred", "SQLite acquisition rollout is pending; no retained revision verified"
			return
		}
		unit.Reader, unit.Source = input.Adapter, input.Adapter.Config()
	}
	if unit.Reader != nil && unit.Record == nil {
		view := captureViewOf(unit.Reader)
		// A view over a reader lease already reads immutable links; only a
		// view over the capture directory itself is locked and frozen here.
		if view != nil && !view.lease && view.evidenceRevision == "" && view.recordRevision == nil {
			lock, err := lockCaptureTarget(ctx, view.dir)
			if err != nil {
				result.Outcome, result.Detail = "deferred", err.Error()
				return
			}
			manifest := view.manifest
			// Historical Conductor readers map one superseded database, not
			// the current source root. Lease exactly that generation.
			canonicalRoot, _ := newCaptureView(view.host, view.dir, manifest).captured(manifest.Source.Path)
			if isConductorSource(unit.Source.Kind) && canonicalRoot != unit.Reader.Config().Path {
				original := view.original(unit.Reader.Config().Path)
				rel, err := filepath.Rel(view.dir, unit.Reader.Config().Path)
				if err != nil {
					releaseLockFile(lock)
					result.Detail = err.Error()
					return
				}
				state, _ := view.databaseState(unit.Reader.Config().Path)
				selected := *manifest
				selected.Source.Path = original
				selected.Files = nil
				selected.PathMap = []capturePathMapping{{Original: original, Captured: filepath.ToSlash(rel)}}
				selected.Snapshots = []*capturedSnapshot{{Path: original, Captured: filepath.ToSlash(rel), Source: state}}
				manifest = &selected
			}
			input, err := freezeManifestInput(ctx, captureTarget{Host: view.host, Source: manifest.Source.Name, Dir: view.dir}, SourceConfig{}, len(manifest.Snapshots) == 0 && manifest.RetainedInputCapture, manifest)
			releaseLockFile(lock)
			if err != nil {
				result.Detail = err.Error()
				return
			}
			defer input.Close()
			unit.Reader, unit.Source = input.Adapter, input.Adapter.Config()
		}
	}
	if budget > 0 && unit.Reader != nil && !scopedCodexAudit(unit) {
		if view := captureViewOf(unit.Reader); view != nil {
			var bytes int64
			for _, file := range view.manifest.Files {
				bytes += file.Size
			}
			for _, snapshot := range view.manifest.Snapshots {
				bytes += snapshot.Size
			}
			if bytes > budget {
				result.Outcome, result.Detail = "deferred", "retained dependency group exceeds verification budget"
				return
			}
		}
	}
	dir, err := os.MkdirTemp(filepath.Dir(c.Path), ".pharos-verification-")
	if err != nil {
		result.Detail = err.Error()
		return
	}
	defer os.RemoveAll(dir)
	read := unit.Parts
	compare := func(record WorkspaceRecord) error {
		scope, err := os.MkdirTemp(dir, "scope-")
		if err != nil {
			return err
		}
		differences, err := c.compareRetainedAuditRecord(ctx, unit, record, scope, budget)
		for _, difference := range differences {
			if !slices.Contains(result.Differences, difference) {
				result.Differences = append(result.Differences, difference)
			}
		}
		return err
	}
	if unit.Record != nil {
		err = compare(*unit.Record)
	} else if group, ok := unit.Reader.(*logicalGroupAdapter); ok {
		compared := 0
		err = group.Discover(func(record WorkspaceRecord) error {
			matches := strings.HasPrefix(unit.ID, "sqlite:")
			for _, conversation := range record.Conversations {
				matches = matches || conversation.Origin == unit.ID
			}
			if !matches {
				return nil
			}
			compared++
			return compare(record)
		})
		if err == nil && compared == 0 {
			err = errAuditUnstable
		}
	} else {
		var record *WorkspaceRecord
		if scopedCodexAudit(unit) {
			record, read, result.EvidenceBytes, err = freezeCodexAuditRecord(ctx, unit, filepath.Join(dir, "evidence"), budget)
		} else {
			record, read, err = freezeAuditRecord(ctx, unit, filepath.Join(dir, "evidence"), budget)
		}
		if err == nil && record == nil {
			err = errAuditUnstable
		}
		if err == nil && partsSignature(read) != partsSignature(unit.Parts) {
			err = errAuditUnstable
		}
		if err == nil {
			err = compare(*record)
		}
	}
	if err != nil {
		result.Detail = err.Error()
		var deferred *auditDeferral
		if errors.As(err, &deferred) {
			result.Reason = deferred.Reason
		}
		if deferred != nil || errors.Is(err, errAuditUnstable) || errors.Is(err, errOlderCopy) || ctx.Err() != nil {
			result.Outcome = "deferred"
		}
		return
	}
	if len(result.Differences) > 0 {
		result.Outcome = "mismatch"
		result.Detail = "full reference ingest disagrees with retained catalog rows"
		id := stableID("sync-integrity", unit.hostID(), unit.Source.Name, unit.ID)
		detail := map[string]any{"categories": result.Differences, "run_id": runID, "source_signature": partsSignature(read), "index_version": sourceIndexVersion(mustAuditAdapter(unit.Source)), "comparison": "isolated full reference ingest", "source_kind": unit.Source.Kind}
		_, err := c.DB.Exec(`INSERT INTO sync_integrity_issues(id,host_id,source_name,unit,first_at,last_at,state,detail_json)
			VALUES(?,?,?,?,?,?,'open',?) ON CONFLICT(id) DO UPDATE SET last_at=excluded.last_at,state='open',detail_json=excluded.detail_json,occurrences=occurrences+1,resolved_at=NULL`, id, unit.hostID(), unit.Source.Name, unit.ID, now(), now(), jsonText(detail))
		if err != nil {
			result.Outcome, result.Detail = "read_error", err.Error()
		}
	} else {
		result.Outcome = "passed"
		_, err := c.DB.Exec("UPDATE sync_integrity_issues SET state='resolved',resolved_at=? WHERE host_id=? AND source_name=? AND unit=? AND state='open'", now(), unit.hostID(), unit.Source.Name, unit.ID)
		if err != nil {
			result.Outcome, result.Detail = "read_error", err.Error()
		}
	}
	return
}

func mustAuditAdapter(source SourceConfig) Adapter { adapter, _ := MakeAdapter(source); return adapter }

var errAuditUnstable = errors.New("audit source changed during acquisition")

func readCapturedAuditRecord(ctx context.Context, unit syncUnit) (*WorkspaceRecord, []sourcePart, error) {
	view := captureViewOf(unit.Reader)
	config := unit.Source
	config.Path = unit.Reader.Config().Path
	adapter, err := MakeAdapter(config)
	if err != nil {
		return nil, nil, err
	}
	adapter.(interface{ bindCapture(*captureView) }).bindCapture(view)
	setAdapterContext(adapter, ctx)
	signature := func() (string, error) {
		paths := []string{}
		for _, part := range unit.Parts {
			if strings.HasPrefix(part.item, "group:") {
				continue
			}
			original := strings.TrimPrefix(part.item, "sqlite:")
			if separator := strings.LastIndex(original, ":sessions:"); separator >= 0 {
				original = original[:separator]
			}
			path, ok := view.captured(original)
			if !ok {
				return "", errAuditUnstable
			}
			paths = append(paths, path)
		}
		if antigravity, ok := adapter.(*antigravityAdapter); ok {
			paths = append(paths, filepath.Join(unit.Source.Path, antigravitySummaries))
			paths = append(paths, antigravity.runLogs()...)
		}
		if jsonl, ok := adapter.(*jsonlAdapter); ok {
			files, databases, err := jsonlAuditDependencies(jsonl, paths)
			if err != nil {
				return "", err
			}
			paths = append(paths, files...)
			paths = append(paths, databases...)
		}
		values := []string{}
		for _, path := range paths {
			for _, dependency := range []string{path, path + "-wal"} {
				info, err := os.Stat(dependency)
				if errors.Is(err, os.ErrNotExist) {
					values = append(values, dependency+":missing")
					continue
				}
				if err != nil {
					return "", err
				}
				values = append(values, fmt.Sprintf("%s:%d:%d", dependency, info.Size(), info.ModTime().UnixNano()))
			}
		}
		return hashBytes([]byte(jsonText(values))), nil
	}
	before, err := signature()
	if err != nil {
		return nil, nil, err
	}
	var record *WorkspaceRecord
	var read []sourcePart
	err = adapter.(partialAdapter).discoverParts(func(parts []sourcePart) bool {
		return unitOf(unit.Source, parts, false).ID != unit.ID
	}, func(value *WorkspaceRecord, parts []sourcePart) error {
		if record != nil {
			return errors.New("audit selected more than one captured group")
		}
		retainedRecordIdentity(view, value, parts)
		record, read = value, parts
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	after, err := signature()
	if err != nil || before != after {
		return nil, nil, errAuditUnstable
	}
	return record, read, nil
}

// Naming stores contribute to a transcript's signal even though they are not
// transcript parts. Freeze them too, and include them in captured-read stability
// checks. All known Codex name schemas matter: an empty newest row overrides an
// older name. Claude only needs the indexes beside this unit's transcripts.
func jsonlAuditDependencies(adapter *jsonlAdapter, paths []string) ([]string, []string, error) {
	files := []string{}
	if adapter.provider == "codex" {
		info, err := os.Stat(adapter.config.Path)
		if err != nil || !info.IsDir() {
			return files, nil, err
		}
		index := filepath.Join(adapter.config.Path, "session_index.jsonl")
		if info, err := os.Stat(index); err == nil && info.Mode().IsRegular() {
			files = append(files, index)
		} else if err != nil && !os.IsNotExist(err) {
			return nil, nil, err
		}
		databases, err := codexNamingDatabases(adapter.config.Path)
		return files, databases, err
	}
	seen := map[string]bool{}
	for _, path := range paths {
		index := filepath.Join(filepath.Dir(path), "sessions-index.json")
		if seen[index] {
			continue
		}
		seen[index] = true
		if info, err := os.Stat(index); err == nil && info.Mode().IsRegular() {
			files = append(files, index)
		}
	}
	slices.Sort(files)
	return files, nil, nil
}

func freezeAuditRecord(ctx context.Context, unit syncUnit, dir string, budget int64) (*WorkspaceRecord, []sourcePart, error) {
	if unit.Reader != nil && captureViewOf(unit.Reader) != nil {
		return readCapturedAuditRecord(ctx, unit)
	}
	if unit.Source.Kind == "conductor" {
		adapter, err := MakeAdapter(unit.Source)
		if err != nil {
			return nil, nil, err
		}
		setAdapterContext(adapter, ctx)
		part := unit.Parts[0]
		separator := strings.LastIndex(part.item, ":sessions:")
		if separator < 0 {
			return nil, nil, errors.New("invalid session identity")
		}
		path := strings.TrimPrefix(part.item[:separator], "sqlite:")
		var record *WorkspaceRecord
		var read []sourcePart
		err = adapter.(*conductorAdapter).database(path, map[string]bool{part.item[separator+len(":sessions:"):]: true}, func([]sourcePart) bool { return false }, func(value *WorkspaceRecord, parts []sourcePart) error { record, read = value, parts; return nil })
		return record, read, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, err
	}
	manifest := &captureManifest{Host: currentHost()}
	view := &captureView{host: currentHost(), dir: dir, manifest: manifest, mappings: []capturePathMapping{{Original: unit.Source.Path, Captured: dir}}}
	if info, err := os.Stat(unit.Source.Path); err == nil && !info.IsDir() {
		// Single-file Claude sources can also read an index beside the file.
		view.mappings = []capturePathMapping{{Original: filepath.Dir(unit.Source.Path), Captured: dir}}
	}
	files := []string{}
	databases := []string{}
	for _, part := range unit.Parts {
		if strings.HasPrefix(part.item, "group:") {
			continue
		}
		if strings.HasPrefix(part.item, "sqlite:") {
			databases = append(databases, strings.TrimPrefix(part.item, "sqlite:"))
		} else {
			files = append(files, part.item)
		}
	}
	if unit.Source.Kind == "codex" || unit.Source.Kind == "claude" {
		adapter := &jsonlAdapter{baseAdapter: baseAdapter{config: unit.Source, ctx: ctx}, provider: unit.Source.Kind}
		namingFiles, namingDatabases, err := jsonlAuditDependencies(adapter, files)
		if err != nil {
			return nil, nil, err
		}
		files = append(files, namingFiles...)
		databases = append(databases, namingDatabases...)
	}
	if unit.Source.Kind == "antigravity" {
		adapter := &antigravityAdapter{baseAdapter: baseAdapter{config: unit.Source, ctx: ctx}}
		summary := filepath.Join(unit.Source.Path, antigravitySummaries)
		if _, err := os.Stat(summary); err == nil {
			databases = append(databases, summary)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, nil, err
		}
		ids := map[string]bool{}
		for _, part := range unit.Parts {
			if strings.HasPrefix(part.item, "sqlite:") || strings.HasPrefix(part.item, "group:") {
				continue
			}
			id := filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(part.item))))
			ids[id] = true
			annotation := filepath.Join(unit.Source.Path, "annotations", id+".pbtxt")
			if _, err := os.Stat(annotation); err == nil {
				files = append(files, annotation)
			} else if !errors.Is(err, os.ErrNotExist) {
				return nil, nil, err
			}
		}
		logs, err := adapter.runLogsFor(ids)
		if err != nil {
			return nil, nil, err
		}
		files = append(files, logs...)
	}
	var bytes int64
	for _, path := range files {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, nil, err
		}
		bytes += info.Size()
		if budget > 0 && bytes > budget {
			return nil, nil, fmt.Errorf("%w: evidence exceeds size budget", errAuditUnstable)
		}
		destination, ok := view.captured(path)
		if !ok {
			return nil, nil, errors.New("audit evidence outside configured source")
		}
		if err := copyStableAuditFile(ctx, path, destination, info); err != nil {
			return nil, nil, err
		}
	}
	databaseStates := map[string]sqliteSourceState{}
	for _, path := range databases {
		state, err := sqliteDependencyState(path)
		if err != nil {
			return nil, nil, err
		}
		bytes += state.Size + state.WALSize
		if budget > 0 && bytes > budget {
			return nil, nil, fmt.Errorf("%w: database evidence exceeds size budget", errAuditUnstable)
		}
		destination, ok := view.captured(path)
		if !ok {
			return nil, nil, errors.New("audit dependency outside source")
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			return nil, nil, err
		}
		if err := snapshotSQLite(ctx, path, destination); err != nil {
			return nil, nil, err
		}
		after, err := sqliteDependencyState(path)
		if err != nil || state != after {
			return nil, nil, errAuditUnstable
		}
		databaseStates[path] = state
		rel, _ := filepath.Rel(dir, destination)
		manifest.Snapshots = append(manifest.Snapshots, &capturedSnapshot{Path: path, Captured: filepath.ToSlash(rel), Source: state})
	}
	config := unit.Source
	config.Path = dir
	if info, err := os.Stat(unit.Source.Path); err == nil && !info.IsDir() {
		config.Path = filepath.Join(dir, filepath.Base(unit.Source.Path))
	}
	adapter, err := MakeAdapter(config)
	if err != nil {
		return nil, nil, err
	}
	adapter.(interface{ bindCapture(*captureView) }).bindCapture(view)
	setAdapterContext(adapter, ctx)
	var record *WorkspaceRecord
	var read []sourcePart
	err = adapter.(partialAdapter).discoverParts(func(parts []sourcePart) bool { return unitOf(unit.Source, parts, false).ID != unit.ID }, func(value *WorkspaceRecord, parts []sourcePart) error {
		if record != nil {
			return errors.New("audit selected more than one group")
		}
		record, read = value, parts
		return nil
	})
	for _, path := range files {
		before, err := os.Stat(path)
		if err != nil {
			return nil, nil, err
		}
		copied, ok := view.captured(path)
		if !ok {
			return nil, nil, errAuditUnstable
		}
		after, err := os.Stat(copied)
		if err != nil || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
			return nil, nil, errAuditUnstable
		}
	}
	for path, before := range databaseStates {
		after, err := sqliteDependencyState(path)
		if err != nil || before != after {
			return nil, nil, errAuditUnstable
		}
	}
	return record, read, err
}

func copyStableAuditFile(ctx context.Context, path, destination string, info os.FileInfo) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	source, err := os.Open(path)
	if err != nil {
		return err
	}
	defer source.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	buffer := make([]byte, 64<<10)
	remaining := info.Size()
	for remaining > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		count, err := io.ReadFull(source, buffer[:min(int64(len(buffer)), remaining)])
		if err != nil {
			return errAuditUnstable
		}
		if _, err := out.Write(buffer[:count]); err != nil {
			return err
		}
		remaining -= int64(count)
	}
	after, err := source.Stat()
	if err != nil || !os.SameFile(info, after) || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return errAuditUnstable
	}
	return os.Chtimes(destination, info.ModTime(), info.ModTime())
}

var auditCategories = []string{"workspaces", "workspace_names", "conversations", "conversation_group_membership", "messages", "search", "conversation_documents", "agent_sessions", "agent_session_usage", "agent_session_messages", "model_requests", "tool_calls", "tool_commands", "tool_urls", "metrics", "metric_ledger", "summaries"}
var auditSearchWord = regexp.MustCompile(`[\p{L}\p{N}]{3,24}`)

// Optional read-only probe observer; production leaves it nil.
var auditQueryTiming func(string, time.Duration, int)

func snapshotAuditScope(ctx context.Context, catalog *Catalog, record WorkspaceRecord, budget int64) (map[string][]map[string]any, error) {
	tx, err := catalog.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	id, err := storedWorkspaceID(tx, record)
	if err != nil {
		return nil, err
	}
	if id == "" {
		return map[string][]map[string]any{}, nil
	}
	result := map[string][]map[string]any{}
	tables, err := queryMapsContext(ctx, tx, "SELECT name FROM sqlite_master WHERE type='table' AND sql NOT LIKE 'CREATE VIRTUAL TABLE%' ORDER BY name")
	if err != nil {
		return nil, err
	}
	conditions := map[string]string{
		"workspace_id":     "=?",
		"conversation_id":  " IN (SELECT id FROM conversations WHERE workspace_id=?)",
		"agent_session_id": " IN (SELECT id FROM agent_sessions WHERE workspace_id=?)",
		"session_id":       " IN (SELECT id FROM agent_sessions WHERE workspace_id=?)",
		"message_id":       " IN (SELECT id FROM messages WHERE conversation_id IN (SELECT id FROM conversations WHERE workspace_id=?))",
		"tool_call_id":     " IN (SELECT id FROM tool_calls WHERE workspace_id=?)",
		"work_item_id":     " IN (SELECT id FROM work_items WHERE workspace_id=?)",
		"change_set_id":    " IN (SELECT id FROM change_sets WHERE workspace_id=?)",
	}
	var bytes int64
	for _, table := range tables {
		name := firstString(table["name"])
		// This derived index refers to this catalog's message rowids. Seeding
		// messages in a reference catalog assigns new rowids; its triggers rebuild
		// the metadata instead of copying those physical references.
		if name == "text_search_documents" {
			continue
		}
		// These deferred projections are rebuilt from tool_calls. In particular,
		// tool_query_facts uses physical tool rowids, reassigned during seeding.
		if strings.HasPrefix(name, "sqlite_") || strings.HasPrefix(name, "messages_fts") || strings.HasPrefix(name, "messages_trigram") || strings.HasPrefix(name, "sync_") || name == "source_item_states" || name == "source_record_states" || name == "tool_call_cube" || name == "tool_usage_daily" || name == "tool_query_facts" {
			continue
		}
		columns, err := tableColumns(tx, name)
		if err != nil {
			return nil, err
		}
		where, args := "", []any{}
		switch name {
		case "workspaces":
			where = "id=?"
			args = append(args, id)
		case "repositories":
			where = "1=1"
		case "hosts":
			where = "id IN (SELECT host_id FROM workspace_sightings WHERE workspace_id=?)"
			args = append(args, id)
		case "conversation_identity_links":
			where = "left_id IN (SELECT id FROM conversations WHERE workspace_id=?) AND right_id IN (SELECT id FROM conversations WHERE workspace_id=?)"
			args = append(args, id, id)
		default:
			for _, key := range []string{"workspace_id", "conversation_id", "agent_session_id", "session_id", "message_id", "tool_call_id", "work_item_id", "change_set_id"} {
				if columns[key] {
					where = key + conditions[key]
					args = append(args, id)
					break
				}
			}
		}
		if where == "" {
			continue
		}
		rows, err := queryAuditRows(ctx, tx, "SELECT * FROM \""+name+"\" WHERE "+where, budget, &bytes, args...)
		if err != nil {
			return nil, err
		}
		result[name] = rows
	}
	// Tool output is expected to be absent from the word index (see
	// wordIndexedKind); a catalog still removing its old postings has some.
	// Neither is a difference from the reference ingest.
	search, err := queryAuditRows(ctx, tx, `SELECT m.id AS message_id,f.message_id AS indexed_message,f.text AS fts_text,r.fts_rowid,CASE WHEN tr.rowid IS NULL THEN 0 ELSE 1 END AS trigram_coverage
		FROM messages m LEFT JOIN message_fts_rows r ON r.message_id=m.id LEFT JOIN messages_fts f ON f.rowid=r.fts_rowid
		LEFT JOIN messages_trigram tr ON tr.rowid=r.fts_rowid WHERE m.conversation_id IN (SELECT id FROM conversations WHERE workspace_id=?) AND `+wordIndexedKind("m.")+` ORDER BY m.id`, budget, &bytes, id)
	if err != nil {
		return nil, err
	}
	samples := 0
	for _, row := range search {
		rowID := row["fts_rowid"]
		delete(row, "fts_rowid")
		word := auditSearchWord.FindString(firstString(row["fts_text"]))
		if rowID == nil || word == "" || samples >= 8 {
			continue
		}
		for _, table := range []string{"messages_fts", "messages_trigram"} {
			var hits int
			if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE "+table+" MATCH ? AND rowid=?", `"`+word+`"`, rowID).Scan(&hits); err != nil {
				return nil, err
			}
			row[table+"_sample_hits"] = hits
		}
		samples++
	}
	result["search"] = search
	return result, nil
}

func queryAuditRows(ctx context.Context, query queryer, statement string, budget int64, used *int64, args ...any) ([]map[string]any, error) {
	started, count := time.Now(), 0
	defer func() {
		if auditQueryTiming != nil {
			auditQueryTiming(statement, time.Since(started), count)
		}
	}()
	rows, err := query.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	values, pointers := make([]any, len(columns)), make([]any, len(columns))
	for index := range values {
		pointers[index] = &values[index]
	}
	result := []map[string]any{}
	for rows.Next() {
		if err := rows.Scan(pointers...); err != nil {
			return nil, err
		}
		row := make(map[string]any, len(columns))
		rowBytes := int64(64 + len(columns)*64)
		for index, column := range columns {
			value := values[index]
			if data, ok := value.([]byte); ok {
				value = string(data)
			}
			row[column] = value
			if text, ok := value.(string); ok {
				rowBytes += int64(len(text))
			}
		}
		*used += rowBytes
		if budget > 0 && *used > budget*4 {
			return nil, fmt.Errorf("%w: catalog scope exceeds memory budget", errAuditUnstable)
		}
		result = append(result, row)
		count++
	}
	return result, rows.Err()
}

func seedAuditScope(ctx context.Context, catalog *Catalog, seed map[string][]map[string]any) error {
	catalog.DB.SetMaxOpenConns(1)
	if _, err := catalog.DB.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		return err
	}
	tx, err := catalog.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	tables, err := tableNames(tx)
	if err != nil {
		return err
	}
	for table, rows := range seed {
		if table == "search" || !tables[table] {
			continue
		}
		for _, row := range rows {
			columns := []string{}
			for column := range row {
				columns = append(columns, column)
			}
			slices.Sort(columns)
			args := []any{}
			for _, column := range columns {
				args = append(args, row[column])
			}
			if _, err := tx.ExecContext(ctx, "INSERT OR REPLACE INTO \""+table+"\" (\""+strings.Join(columns, "\",\"")+"\") VALUES("+placeholders(len(args))+")", args...); err != nil {
				return err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	_, err = catalog.DB.ExecContext(ctx, "PRAGMA foreign_keys=ON")
	return err
}

func normalizeAuditRows(table string, rows []map[string]any) string {
	normalized := []string{}
	for _, row := range rows {
		fields := map[string]any{}
		for key, value := range row {
			// Older writers serialized a nil aliases slice as JSON null. It
			// represents the same empty list as []; keep nonempty aliases checked.
			if table == "conversations" && key == "aliases_json" && value == "null" {
				value = "[]"
			}
			// A unit audit verifies source naming evidence. The resolved title
			// also depends on linked works and global authorship classification,
			// neither of which an isolated reference ingest can reconstruct.
			if table == "workspace_names" && !slices.Contains([]string{"workspace_id", "source_title", "source_scope"}, key) {
				continue
			}
			if slices.Contains([]string{"indexed_at", "updated_at", "built_at", "computed_at", "refined_at"}, key) || (table == "summaries" && key == "created_at") {
				continue
			}
			if table == "metrics" && key == "observed_at" {
				continue
			}
			if key == "id" {
				if _, ok := value.(int64); ok {
					continue
				}
			}
			if table == "workspaces" && slices.Contains([]string{"title", "repository_id", "head_ref", "base_ref", "branch", "location_history_json"}, key) {
				continue
			}
			fields[key] = value
		}
		normalized = append(normalized, hashBytes([]byte(jsonText(fields))))
	}
	slices.Sort(normalized)
	return hashBytes([]byte(strings.Join(normalized, "\n")))
}

func (s *Server) auditIntentional(ctx context.Context, runID string, names []string) auditResult {
	pool := newAuditPool(s.Catalog)
	for _, source := range s.Config().Sources {
		if !source.Enabled || !automaticProvider(source.Kind) || (len(names) > 0 && !slices.Contains(names, source.Name)) {
			continue
		}
		adapter, err := MakeAdapter(source)
		if err != nil {
			pool.failure = err
			continue
		}
		setAdapterContext(adapter, ctx)
		err = adapter.(partialAdapter).discoverParts(func(parts []sourcePart) bool { pool.consider(unitOf(source, parts, false)); return true }, func(*WorkspaceRecord, []sourcePart) error { return errors.New("incomplete audit discovery") })
		if err != nil {
			pool.failure = err
		}
	}
	return s.Catalog.auditPool(ctx, pool, runID)
}

// auditOpenIssues audits again every unit of the named sources (all when
// none) with an open mismatch on this host, as a recovery that rebuilt their
// rows must: the sampled audit after it reaches one unit, and an issue
// resolves only when its own unit passes.
func (s *Server) auditOpenIssues(ctx context.Context, runID string, names []string) {
	rows, err := queryMapsContext(ctx, s.Catalog.DB, "SELECT DISTINCT source_name,unit FROM sync_integrity_issues WHERE host_id=? AND state='open'", currentHost().ID)
	if err != nil {
		return
	}
	open := map[string]map[string]bool{}
	for _, row := range rows {
		name := firstString(row["source_name"])
		if open[name] == nil {
			open[name] = map[string]bool{}
		}
		open[name][firstString(row["unit"])] = true
	}
	for _, source := range s.Config().Sources {
		units := open[source.Name]
		if ctx.Err() != nil || len(units) == 0 || !source.Enabled || !automaticProvider(source.Kind) || (len(names) > 0 && !slices.Contains(names, source.Name)) {
			continue
		}
		adapter, err := syncAdapter(source)
		if err != nil {
			continue
		}
		setAdapterContext(adapter, ctx)
		adapter.(partialAdapter).discoverParts(func(parts []sourcePart) bool {
			if unit := unitOf(source, parts, false); units[unit.ID] && ctx.Err() == nil {
				s.Catalog.recordVerification(unit, s.Catalog.auditUnit(ctx, unit, runID, 0))
			}
			return true
		}, func(*WorkspaceRecord, []sourcePart) error {
			return errors.New("issue audit discovery attempted to parse")
		})
	}
}

func (s *Server) startVerification(w http.ResponseWriter, r *http.Request, body map[string]any) {
	names := []string{}
	if value, present := body["sources"]; present {
		values, valid := sliceValue(value)
		if !valid {
			writeError(w, errors.New("sources must be a list of names"), 400)
			return
		}
		names = stringSlice(values)
	}
	if !s.acquireManualIngest() {
		writeError(w, errors.New("a capture or index is already running"), 409)
		return
	}
	variant := "sample"
	if boolValue(body, "all", false) {
		variant = "all"
	}
	_, handle := s.begin(s.life.ctx, OperationSpec{Kind: "index.verify", Variant: variant, Trigger: triggerUser})
	run := s.startRunFor(handle, "verification", nil)
	ctx, end := s.beginIngest(run.ID)
	ctx = withOp(ctx, handle)
	if !s.launch(handle, func(context.Context, *Op) error {
		defer s.ingestMu.Unlock()
		defer end()
		result := auditResult{}
		if boolValue(body, "all", false) {
			summary := s.verifyAllInputs(ctx, run.ID, func(summary verificationSummary) {
				s.updateRun(run.ID, func(run *SyncRun) {
					run.Phase = "verifying retained inputs"
					run.DoneConversations = summary.Checked
					run.setWork(run.Phase, firstString(run.CurrentSource), "input", float64(summary.Checked), 0)
					run.Verification = &summary
				})
			}, names)
			if summary.Errors > 0 {
				result.Outcome, result.Detail = "read_error", fmt.Sprintf("%d inputs could not be read; see verification coverage", summary.Errors)
			} else if summary.Deferred > 0 {
				result.Detail = fmt.Sprintf("%d inputs deferred; see verification coverage", summary.Deferred)
			}
		} else {
			result = s.auditIntentional(ctx, run.ID, names)
		}
		state := "complete"
		if ctx.Err() != nil {
			state = "interrupted"
		} else if result.Outcome == "read_error" {
			state = "failed"
		}
		s.updateRun(run.ID, func(run *SyncRun) {
			run.State, run.Phase = state, state
			run.CompletedAt = now()
			run.Error = nilIfEmpty(result.Detail)
		})
		return nil
	}) {
		end()
		s.ingestMu.Unlock()
		writeError(w, errors.New("service stopping"), 503)
		return
	}
	writeJSON(w, map[string]any{"run_id": run.ID}, 202)
}

func (s *Server) verifyAllInputs(ctx context.Context, runID string, progress func(verificationSummary), scopes ...[]string) verificationSummary {
	summary := verificationSummary{}
	names := []string{}
	if len(scopes) > 0 {
		names = scopes[0]
	}
	seen := map[string]bool{}
	visit := func(adapter Adapter, host string) {
		setAdapterContext(adapter, ctx)
		partial, ok := adapter.(partialAdapter)
		if !ok {
			summary.Deferred++
			return
		}
		err := partial.discoverParts(func(parts []sourcePart) bool {
			if ctx.Err() != nil {
				return true
			}
			unit := unitOf(adapter.Config(), parts, false)
			unit.Host, unit.Reader = host, adapter
			key := host + "/" + unit.Source.Name + "/" + unit.ID + "/" + partsSignature(parts)
			if seen[key] {
				return true
			}
			seen[key] = true
			result := s.Catalog.auditUnit(ctx, unit, runID, 0)
			if err := s.Catalog.recordVerification(unit, result); err != nil {
				result.Outcome = "read_error"
			}
			summary.Checked++
			switch result.Outcome {
			case "passed":
				summary.Passed++
			case "mismatch":
				summary.Mismatch++
			case "deferred":
				summary.Deferred++
			default:
				summary.Errors++
			}
			if progress != nil {
				progress(summary)
			}
			return true
		}, func(*WorkspaceRecord, []sourcePart) error {
			return errors.New("verification discovery attempted to parse")
		})
		if err != nil && ctx.Err() == nil {
			summary.Errors++
		}
	}
	for _, source := range s.Config().Sources {
		if ctx.Err() != nil {
			break
		}
		if !source.Enabled || !automaticProvider(source.Kind) || (len(names) > 0 && !slices.Contains(names, source.Name)) {
			continue
		}
		adapter, err := syncAdapter(source)
		if err != nil {
			summary.Errors++
			continue
		}
		visit(adapter, currentHost().ID)
	}
	targets, err := captureTargets(s.Config().CaptureRoot, nil, true, names)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		summary.Errors++
	}
	for _, target := range targets {
		if ctx.Err() != nil {
			break
		}
		manifest, err := loadCaptureManifest(target.Dir)
		if err != nil {
			summary.Errors++
			continue
		}
		if len(manifest.Snapshots) == 0 || manifest.LatestRecords {
			if _, err := os.Stat(filepath.Join(target.Dir, recordDir, "entities")); err == nil {
				reader, close, err := openSQLiteReplay(ctx, target)
				if err != nil {
					summary.Errors++
					continue
				}
				if reader != nil {
					// Newest evidence gets the first comparison for each native
					// session; history must not replace that observation.
					slices.Reverse(reader.revisions)
					err = reader.Discover(func(record WorkspaceRecord) error {
						id := "sqlite:" + record.SourceID
						if isConductorSource(manifest.Source.Kind) {
							for key := range seen {
								if strings.HasPrefix(key, target.Host.ID+"/"+target.Source+"/") && strings.Contains(key, ":sessions:"+record.SourceID+"/") {
									return nil
								}
							}
						}
						key := target.Host.ID + "/" + target.Source + "/" + id
						if seen[key] {
							return nil
						}
						seen[key] = true
						unit := syncUnit{ID: id, Source: reader.Config(), Host: target.Host.ID, Reader: reader, Record: &record}
						result := s.Catalog.auditUnit(ctx, unit, runID, 0)
						if err := s.Catalog.recordVerification(unit, result); err != nil {
							result.Outcome = "read_error"
						}
						summary.Checked++
						switch result.Outcome {
						case "passed":
							summary.Passed++
						case "mismatch":
							summary.Mismatch++
						case "deferred":
							summary.Deferred++
						default:
							summary.Errors++
						}
						if progress != nil {
							progress(summary)
						}
						return nil
					})
					close()
					if err != nil {
						summary.Errors++
					}
					continue
				}
			}
		}
		adapter, manifest, view, err := openCaptureAdapter(target)
		if err != nil {
			summary.Errors++
			continue
		}
		visit(adapter, target.Host.ID)
		conductor, ok := adapter.(*conductorAdapter)
		if !ok {
			continue
		}
		for _, snapshot := range manifest.Snapshots {
			// Leased (and decoded if stored encoded) like the generations.
			latest, _, releaseLatest, err := leaseCapturedVersion(ctx, target, snapshot.Captured)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) || errors.Is(err, errCaptureBusy) {
					summary.Deferred++
				} else {
					summary.Errors++
				}
				continue
			}
			handled, err := conductorSessionIDsContext(ctx, latest)
			releaseLatest()
			if err != nil {
				summary.Errors++
				continue
			}
			for _, generation := range snapshot.Generations {
				if ctx.Err() != nil {
					break
				}
				// Each generation is leased under the capture lock, so a
				// compression or pruning cannot change it while it is read; an
				// encoded one is decoded in that lease, removed after the check.
				file, dir, release, err := leaseCapturedVersion(ctx, target, generation.Captured)
				if err != nil {
					if errors.Is(err, os.ErrNotExist) || errors.Is(err, errCaptureBusy) {
						summary.Deferred++
					} else {
						summary.Errors++
					}
					continue
				}
				func() {
					defer release()
					ids, err := conductorSessionIDsContext(ctx, file)
					if err != nil {
						if errors.Is(err, os.ErrNotExist) {
							summary.Deferred++
						} else {
							summary.Errors++
						}
						return
					}
					missing := map[string]bool{}
					for id := range ids {
						if !handled[id] {
							missing[id], handled[id] = true, true
						}
					}
					if len(missing) == 0 {
						return
					}
					older := &captureView{host: view.host, dir: dir, lease: true, decoded: map[string]bool{generation.Captured: true}, manifest: manifest, mappings: []capturePathMapping{{Original: snapshot.Path, Captured: file}}}
					config := conductor.config
					config.Path = file
					reader := &conductorAdapter{baseAdapter: baseAdapter{config: config, capability: conductor.capability, view: older}, selected: missing}
					visit(reader, target.Host.ID)
				}()
			}
		}
	}
	if progress != nil {
		progress(summary)
	}
	return summary
}

func (c *Catalog) recordVerification(unit syncUnit, result auditResult) error {
	return c.recordAuditProgress(unit, result)
}
