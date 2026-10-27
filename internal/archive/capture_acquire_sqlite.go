package archive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Grouped SQLite sources remain opt-in until their incremental performance
// gates pass. Codex naming stores are small parsing dependencies; their rows
// are acquired together with the filesystem transcripts, without native backup.
func (c *Catalog) acquireLogicalGroup(ctx context.Context, source SourceConfig, progress ProgressFunc) (retainedInput, error) {
	session, err := beginInputCapture(ctx, Config{CaptureRoot: c.captureRootPath(), CaptureGenerations: 1})
	if err != nil {
		return retainedInput{}, err
	}
	defer session.Close()
	if err = session.writeHost(now(), now()); err != nil {
		return retainedInput{}, err
	}
	plan, err := planCapture(source)
	if err != nil {
		return retainedInput{}, err
	}
	return c.acquireLogicalGroupLocked(ctx, source, session, plan, progress)
}

func (catalog *Catalog) acquireLogicalGroupLocked(ctx context.Context, source SourceConfig, session *captureSession, plan capturePlan, progress ProgressFunc) (retainedInput, error) {
	if !plan.complete || len(plan.warnings) > 0 {
		return retainedInput{}, errors.New("incomplete dependency inventory; input remains pending")
	}
	if progress != nil {
		progress("preserving inputs", 0, 0, 0, 0, 0)
	}
	dir := filepath.Join(session.hostDir, source.Name)
	if !validCaptureName(source.Name) {
		return retainedInput{}, errors.New("invalid capture source name")
	}
	m, err := loadCaptureManifest(dir)
	if err != nil {
		return retainedInput{}, err
	}
	if !m.acceptsSource(session.host, source) {
		return retainedInput{}, errors.New("capture source identity changed; use a new source name")
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return retainedInput{}, err
	}
	c := &sourceCapture{ctx: ctx, dir: dir, session: session, source: source, manifest: m, files: map[string]*capturedFile{}, snapshots: map[string]*capturedSnapshot{}, checkpoint: time.Now(), result: CaptureSourceResult{Name: source.Name, Kind: source.Kind, State: "running", Dir: dir, captureRunRecord: captureRunRecord{StartedAt: now()}}}
	for _, f := range m.Files {
		c.files[f.Captured] = f
	}
	for _, d := range m.Snapshots {
		c.snapshots[d.Captured] = d
	}
	c.mergeMappings(plan)
	if err = c.commit(false); err != nil {
		return retainedInput{}, err
	}
	present := map[string]bool{}
	if err = c.captureFiles(plan.files, present); err != nil {
		return retainedInput{}, err
	}
	if len(c.result.Errors) > 0 {
		return retainedInput{}, captureResultError(c.result)
	}
	for _, db := range plan.databases {
		present[filepath.ToSlash(filepath.Join("files", db.rel))] = true
	}
	c.markMissing(present)
	s := newRecordStore(c)
	// Reuse committed objects for unchanged captured dependencies. No-op
	// ticks must not hash/compress every transcript again.
	c.recordDependencyCache = map[string]recordDependency{}
	for _, database := range plan.databases {
		head, e := s.head(database.path, "database")
		if e != nil {
			return retainedInput{}, e
		}
		if head.Version == 0 {
			continue
		}
		v, e := s.loadRevision(head.Revision)
		if e != nil {
			return retainedInput{}, e
		}
		for _, dependency := range v.Dependencies {
			c.recordDependencyCache[dependency.Path] = dependency
		}
	}
	deps, err := s.dependencies()
	if err != nil {
		return retainedInput{}, err
	}
	var members []retainedSQLiteRevision
	for _, db := range plan.databases {
		state, err := sqliteDependencyState(db.path)
		if err != nil {
			return retainedInput{}, err
		}
		if err = reserveCaptureAcquisition(dir, min(state.Size, recordMaxBytes)); err != nil {
			return retainedInput{}, err
		}
		var ref recordObject
		head, err := s.head(db.path, "database")
		if err != nil {
			return retainedInput{}, err
		}
		last, valid := parseTime(head.LastFullAt)
		if head.Version != 0 && valid && time.Since(last) < recordInterval(source) {
			previous, e := s.loadRevision(head.Revision)
			if e != nil {
				return retainedInput{}, e
			}
			if sameSQLiteContentState(sqliteSourceState(previous.Observed), state) {
				// Reuse verified row chunks, while publishing this acquisition's exact
				// new dependency closure. A discovery tick is not full reconciliation.
				previous.Acquisition = c.result.StartedAt
				previous.AcquiredAt = now()
				previous.Source = m.Source
				previous.Dependencies = deps
				previous.Mappings = m.PathMap
				previous.Installations = m.Installations
				previous.Full = false
				ref, err = s.publish(&previous)
			}
		}
		if ref.Hash == "" && err == nil {
			ref, err = s.exportSQLite(db.path, db.path, "database", state, "", deps)
		}
		if err != nil {
			return retainedInput{}, err
		}
		revision, err := s.loadRevision(ref)
		if err != nil {
			return retainedInput{}, err
		}
		members = append(members, retainedSQLiteRevision{Ref: ref, Revision: revision})
	}
	// Validate membership and every file/database in the cross-input closure.
	// Present-but-unreadable dependencies are errors; absent optional inputs are
	// represented by the immutable inventory, never a live filesystem fallback.
	after, err := planCapture(source)
	if err != nil || !after.complete || len(after.warnings) > 0 || !slices.Equal(after.files, plan.files) || !slices.Equal(after.databases, plan.databases) {
		return retainedInput{}, errors.New("dependency membership changed during acquisition")
	}
	for _, f := range c.files {
		if f.MissingSince != "" {
			continue
		}
		info, err := os.Stat(f.Path)
		if err != nil || info.Size() != f.Size || info.ModTime().UnixNano() != f.MTimeNS {
			return retainedInput{}, errors.New("dependency changed during acquisition")
		}
	}
	for _, r := range members {
		state, err := sqliteDependencyState(r.Revision.Database)
		if err != nil || !sameSQLiteContentState(state, sqliteSourceState(r.Revision.Observed)) {
			return retainedInput{}, errors.New("database changed during group acquisition")
		}
	}
	if len(members) == 0 {
		if err = c.commit(true); err != nil {
			return retainedInput{}, err
		}
		return freezeCaptureInput(ctx, captureTarget{Host: session.host, Source: source.Name, Dir: dir}, source, true)
	}
	group := sqliteRecordGroup{Version: recordFormat, Acquisition: c.result.StartedAt, CompletedAt: now()}
	for _, r := range members {
		group.Revisions = append(group.Revisions, r.Ref)
	}
	b, err := json.Marshal(group)
	if err != nil {
		return retainedInput{}, err
	}
	if err = os.MkdirAll(filepath.Dir(recordGroupPath(dir, group.Acquisition)), 0700); err != nil {
		return retainedInput{}, err
	}
	if err = writeRecordMetadata(recordGroupPath(dir, group.Acquisition), b); err != nil {
		return retainedInput{}, err
	}
	c.manifest.LatestRecords = true
	if err = c.commit(true); err != nil {
		return retainedInput{}, err
	}
	members[0].Companions = members[1:]
	config := source
	config.Path = dir
	reader := &logicalGroupAdapter{sqliteReplayAdapter: sqliteReplayAdapter{baseAdapter: baseAdapter{ctx: ctx, config: config, capability: "retrieval-only", view: newCaptureView(session.host, dir, m)}, store: s, revisions: members[:1]}}
	// Raw objects are immutable and never automatically collected. Durable pins
	// additionally protect this exact closure from future pruning operations.
	reader.view.evidenceRevision = members[0].Ref.Hash
	refs := make([]recordObject, 0, len(members))
	for _, r := range members {
		refs = append(refs, r.Ref)
	}
	closeReader, err := c.pinReader(refs)
	if err != nil {
		return retainedInput{}, err
	}
	return retainedInput{Adapter: reader, Close: closeReader}, nil
}

type logicalGroupAdapter struct{ sqliteReplayAdapter }

func (a *logicalGroupAdapter) partExtractor() string { return a.ExtractorVersion() }
func (a *logicalGroupAdapter) planParts() (*sourceWorkPlan, error) {
	parts := []sourcePart{}
	for _, r := range a.revisions {
		for _, member := range append([]retainedSQLiteRevision{r}, r.Companions...) {
			v := member.Revision
			parts = append(parts, sourcePart{item: "sqlite:" + v.Database, size: v.Observed.Size, version: sqliteSourceState(v.Observed).version(), signal: retainedSQLiteIdentity(member)})
		}
	}
	return &sourceWorkPlan{groups: []plannedPartGroup{{parts: parts, read: func(emit partEmitter) error {
		if err := a.Discover(func(record WorkspaceRecord) error { return emit(&record, nil) }); err != nil {
			return err
		}
		// Checkpoint only after every record has committed.
		return emit(nil, parts)
	}}}}, nil
}
func (a *logicalGroupAdapter) discoverParts(unchanged func([]sourcePart) bool, emit func(*WorkspaceRecord, []sourcePart) error) error {
	plan, err := a.planParts()
	if err != nil {
		return err
	}
	return plan.discover(a.context(), unchanged, emit)
}

func preservationMode(source SourceConfig) string {
	if !retainedAcquisitionEnabled(source) {
		return "live-rollout-gap"
	}
	switch strings.ToLower(source.Kind) {
	case "conductor":
		if source.Options["sqlite_record_capture"] == true {
			return "retained-sessions"
		}
		return "live-sqlite-rollout-gap"
	case "tl1", "antigravity":
		if source.Options["sqlite_record_capture"] == true {
			return "retained-sqlite-group"
		}
		return "live-sqlite-rollout-gap"
	case "codex":
		if source.Options["sqlite_record_capture"] != true {
			return "filesystem-retained-sqlite-gated"
		}
		return "retained-inputs"
	case "claude", "canonical", "tl1-export", "chatgpt", "chatgpt-export":
		return "retained-inputs"
	default:
		return fmt.Sprintf("unsupported:%s", source.Kind)
	}
}
