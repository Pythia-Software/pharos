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
	"sort"
	"strings"
	"time"
)

type retainedSQLiteRevision struct {
	Companions []retainedSQLiteRevision
	Ref        recordObject
	Revision   sqliteRevision
}

func (s *recordStore) revisions() ([]retainedSQLiteRevision, error) {
	entries, e := os.ReadDir(filepath.Join(s.c.dir, recordDir, "entities"))
	if errors.Is(e, os.ErrNotExist) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	var revisions []retainedSQLiteRevision
	for _, entry := range entries {
		if !entry.IsDir() || !validRecordHash(entry.Name()) {
			return nil, errors.New("invalid SQLite records entity inventory")
		}
		files, e := os.ReadDir(filepath.Join(s.c.dir, recordDir, "entities", entry.Name()))
		if e != nil {
			return nil, e
		}
		for _, file := range files {
			if file.Name() == "head.json" {
				var h sqliteRecordHead
				b, e := os.ReadFile(filepath.Join(s.c.dir, recordDir, "entities", entry.Name(), file.Name()))
				if e != nil {
					return nil, e
				}
				if json.Unmarshal(b, &h) != nil || h.Version != recordFormat {
					return nil, errors.New("unsupported SQLite records head; upgrade Pharos")
				}
				continue
			}
			if !strings.HasSuffix(file.Name(), ".json") || !validRecordHash(strings.TrimSuffix(file.Name(), ".json")) {
				continue
			}
			b, e := os.ReadFile(filepath.Join(s.c.dir, recordDir, "entities", entry.Name(), file.Name()))
			if e != nil {
				return nil, e
			}
			var ref recordObject
			if e = json.Unmarshal(b, &ref); e != nil {
				return nil, e
			}
			v, e := s.loadRevision(ref)
			if e != nil {
				return nil, e
			}
			if recordEntityKey(v.Database, v.Entity) != entry.Name() || ref.Hash != strings.TrimSuffix(file.Name(), ".json") {
				return nil, errors.New("SQLite record identity mismatch")
			}
			revisions = append(revisions, retainedSQLiteRevision{Ref: ref, Revision: v})
		}
	}
	grouped := []retainedSQLiteRevision{}
	seenGroups := map[string]bool{}
	for _, r := range revisions {
		if isConductorSource(r.Revision.Source.Kind) {
			grouped = append(grouped, r)
			continue
		}
		key := r.Revision.Acquisition
		if seenGroups[key] {
			continue
		}
		group, e := s.committedGroup(key)
		if errors.Is(e, os.ErrNotExist) {
			continue
		}
		if e != nil {
			return nil, e
		}
		seenGroups[key] = true
		var members []retainedSQLiteRevision
		for _, ref := range group.Revisions {
			v, e := s.loadRevision(ref)
			if e != nil {
				return nil, e
			}
			members = append(members, retainedSQLiteRevision{Ref: ref, Revision: v})
		}
		if len(members) == 0 {
			return nil, errors.New("empty committed SQLite group")
		}
		members[0].Companions = members[1:]
		grouped = append(grouped, members[0])
	}
	revisions = grouped
	// Versions are comparable only within this source host/database/entity.
	sort.Slice(revisions, func(i, j int) bool {
		a, b := revisions[i], revisions[j]
		if a.Revision.Database != b.Revision.Database {
			return a.Revision.Database < b.Revision.Database
		}
		if a.Revision.Observed.version() != b.Revision.Observed.version() {
			return a.Revision.Observed.version() < b.Revision.Observed.version()
		}
		return a.Ref.Hash < b.Ref.Hash
	})
	return revisions, nil
}

// materialize constructs inert tables through trusted SQL, never captured DDL,
// types, constraints, indexes, expressions, triggers or extensions. All values
// are bound. Insert order is the retained row occurrence order. The adapter's
// parser-order inputs are retained separately for ambiguous Conductor queries.
func (s *recordStore) materialize(ref recordObject, destination string) error {
	if e := s.verify(ref); e != nil {
		return e
	}
	v, e := s.loadRevision(ref)
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(destination), 0700); e != nil {
		return e
	}
	f, e := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	f.Close()
	db, e := sql.Open("sqlite", destination)
	if e != nil {
		return e
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, e = db.Exec("PRAGMA trusted_schema=OFF"); e != nil {
		return e
	}
	tx, e := db.BeginTx(s.c.ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	for _, t := range v.Tables {
		// SQLite reserves sqlite_*; its internal tables are opaque evidence and
		// never provider parser inputs. Views are retained in schema, not executed.
		if t.Coverage == "excluded" || strings.HasPrefix(strings.ToLower(t.Name), "sqlite_") {
			continue
		}
		table, e := quoteRecordIdentifier(t.Name)
		if e != nil {
			return e
		}
		cols := make([]string, len(t.Columns))
		for i, c := range t.Columns {
			cols[i], e = quoteRecordIdentifier(c.Name)
			if e != nil {
				return e
			}
		}
		definitions := []string{}
		for i, column := range t.Columns {
			if !column.ImplicitRowID {
				definitions = append(definitions, cols[i]+" "+recordColumnAffinity(column.DeclaredType))
			}
		}

		if _, e = tx.ExecContext(s.c.ctx, "CREATE TABLE "+table+" ("+strings.Join(definitions, ",")+")"); e != nil {
			return e
		}
		insert, e := tx.PrepareContext(s.c.ctx, "INSERT INTO "+table+" ("+strings.Join(cols, ",")+") VALUES ("+placeholders(len(cols))+")")
		if e != nil {
			return e
		}
		e = s.scanRecordRows(t, func(row []sqliteValue) error {
			values := make([]any, len(row))
			for i, x := range row {
				var e error
				values[i], e = x.decode()
				if e != nil {
					return e
				}
			}
			var bytes int64 = 64 << 10
			for _, value := range values {
				switch x := value.(type) {
				case string:
					bytes += int64(len(x)) * 2
				case []byte:
					bytes += int64(len(x)) * 2
				}
			}
			if e := ensureFree(filepath.Dir(destination), bytes); e != nil {
				return e
			}
			_, e := insert.ExecContext(s.c.ctx, values...)
			return e
		})
		insert.Close()
		if e != nil {
			return e
		}
	}
	return tx.Commit()
}

// pinRevision holds the full immutable object closure through the revision.
// Objects have no automatic reclamation. A future collector must traverse
// both committed inventories and active pins, including orphaned heads.
func (s *recordStore) pinRevision(owner string, ref recordObject) error {
	if e := s.verify(ref); e != nil {
		return e
	}
	rel := filepath.ToSlash(filepath.Join(recordDir, "objects", ref.Hash+".gz"))
	info, e := os.Stat(s.objectPath(ref.Hash))
	if e != nil {
		return e
	}
	v, e := s.loadRevision(ref)
	if e != nil {
		return e
	}
	references := []recordObject{v.Schema}
	for _, table := range v.Tables {
		references = append(references, table.Chunks...)
	}
	for _, tableRef := range v.ParserInputs {
		var table recordTable
		if e = s.readJSON(tableRef, &table); e != nil {
			return e
		}
		references = append(references, tableRef)
		references = append(references, table.Chunks...)
	}
	for _, dependency := range v.Dependencies {
		if dependency.Object != nil {
			references = append(references, *dependency.Object)
		}
	}
	if v.Parent != nil {
		references = append(references, *v.Parent)
	}
	paths := []string{}
	for _, reference := range references {
		paths = append(paths, filepath.ToSlash(filepath.Join(recordDir, "objects", reference.Hash+".gz")))
	}
	_, e = s.c.pinVersion(owner, "SQLite evidence reader", capturedVersion{Captured: rel, Size: info.Size()}, paths...)
	return e
}
func timeFromNS(ns int64) time.Time { return time.Unix(0, ns) }
func sourceCaptureForTarget(ctx context.Context, target captureTarget) (*sourceCapture, error) {
	m, e := loadCaptureManifest(target.Dir)
	if e != nil {
		return nil, e
	}
	if m.Version == 0 || m.Host.ID != target.Host.ID || m.Source.Name != target.Source {
		return nil, errors.New("capture identity mismatch")
	}
	c := &sourceCapture{ctx: ctx, dir: target.Dir, manifest: m, session: &captureSession{host: target.Host}, source: SourceConfig{Name: m.Source.Name, Kind: m.Source.Kind, Path: m.Source.Path, Account: m.Source.Account}, files: map[string]*capturedFile{}, snapshots: map[string]*capturedSnapshot{}}
	c.source.Options = map[string]any{"sqlite_record_capture": m.SQLiteRecordCapture}
	for _, f := range m.Files {
		c.files[f.Captured] = f
	}
	for _, v := range m.Snapshots {
		c.snapshots[v.Captured] = v
	}
	return c, nil
}

type sqliteReplayAdapter struct {
	// History verification confines materialization to its reviewed scratch volume
	// and parses Conductor one bounded session at a time. Ordinary replay keeps
	// its existing defaults.
	scratchRoot    string
	maxEntityBytes int64
	entityGap      func(string)
	baseAdapter
	store     *recordStore
	revisions []retainedSQLiteRevision
	snapshots []tl1Snapshot
}

func (a *sqliteReplayAdapter) Fingerprint() (string, error) {
	refs := []string{}
	for _, r := range a.revisions {
		refs = append(refs, r.Ref.Hash)
	}
	return hashBytes([]byte(jsonText(refs))), nil
}
func (a *sqliteReplayAdapter) ExtractorVersion() string {
	ordinary, e := MakeAdapter(a.config)
	if e != nil {
		return recordWriter
	}
	if v, ok := ordinary.(partialAdapter); ok {
		return recordWriter + ":" + v.partExtractor()
	}
	if v, ok := ordinary.(VersionedAdapter); ok {
		return recordWriter + ":" + v.ExtractorVersion()
	}
	return recordWriter
}
func (a *sqliteReplayAdapter) tl1Snapshots() []tl1Snapshot { return a.snapshots }
func (a *sqliteReplayAdapter) Discover(emit func(WorkspaceRecord) error) error {
	for i, r := range a.revisions {
		if e := a.context().Err(); e != nil {
			return e
		}
		v := r.Revision
		// A new catalog cannot pick between unequal observations of the same
		// authority version. Keep both branches and require an explicit rescue.
		for j := i + 1; j < len(a.revisions); j++ {
			other := a.revisions[j].Revision
			if other.Database != v.Database || other.Observed.version() != v.Observed.version() {
				break
			}
			if other.Source.Account != v.Source.Account {
				continue
			}
			conflict := false
			if isConductorSource(v.Source.Kind) {
				for id, identity := range v.EntityIdentities {
					if competing, ok := other.EntityIdentities[id]; ok && identity != competing {
						conflict = true
					}
				}
			} else {
				conflict = other.Entity == v.Entity && sameSQLiteAuthorityVersions(r, a.revisions[j]) && retainedSQLiteIdentity(r) != retainedSQLiteIdentity(a.revisions[j])
			}
			if conflict {
				return fmt.Errorf("ambiguous equal-version SQLite observations for %s; evidence retained, catalog authority unresolved: %w", v.Database, errOlderCopy)
			}
		}
		if e := a.replay(r, emit); e != nil {
			return e
		}
	}
	return nil
}
func (a *sqliteReplayAdapter) replay(r retainedSQLiteRevision, emit func(WorkspaceRecord) error) error {
	v := r.Revision
	if !v.Complete {
		return errors.New("cannot replay incomplete SQLite acquisition")
	}
	if v.Host.ID != a.view.host.ID || v.Source.Name != a.config.Name {
		return errors.New("SQLite revision origin mismatch")
	}
	dir, e := os.MkdirTemp(a.scratchRoot, "pharos-record-replay-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(dir)
	var scratchBytes int64 = 1 << 20
	for _, candidate := range append([]retainedSQLiteRevision{r}, r.Companions...) {
		for _, table := range candidate.Revision.Tables {
			for _, ref := range table.Chunks {
				if ref.Length < 0 || ref.Length > 1<<40 {
					return errors.New("invalid scratch byte estimate")
				}
				scratchBytes += ref.Length * 2
			}
		}
	}
	for _, dependency := range v.Dependencies {
		if dependency.Object != nil {
			scratchBytes += dependency.Object.Length
		}
	}
	if e = ensureFree(dir, scratchBytes); e != nil {
		return e
	}
	// Reuse the retained mapping inventory, replacing only its physical root.
	manifest := &captureManifest{Version: captureManifestVersion, Host: v.Host, Source: v.Source, PathMap: v.Mappings, Installations: v.Installations, UpdatedAt: v.AcquiredAt}
	view := newCaptureView(v.Host, dir, manifest)
	view.evidenceRevision = r.Ref.Hash
	view.evidenceIdentity = retainedSQLiteIdentity(r)
	view.noLocalLookups = true
	view.recordStore = a.store
	view.recordRevision = &v
	database, ok := view.captured(v.Database)
	if !ok {
		return errors.New("SQLite revision does not map its database")
	}
	rel, e := filepath.Rel(dir, database)
	if e != nil || !strings.HasPrefix(filepath.ToSlash(rel), "files/") {
		return errors.New("unsafe replay database mapping")
	}
	if e = a.store.materialize(r.Ref, database); e != nil {
		return e
	}
	manifest.Snapshots = []*capturedSnapshot{{Path: v.Database, Captured: filepath.ToSlash(rel), Source: sqliteSourceState(v.Observed)}}
	for _, other := range r.Companions {
		dest, ok := view.captured(other.Revision.Database)
		if !ok {
			return errors.New("unmapped companion database")
		}
		relative, e := filepath.Rel(dir, dest)
		if e != nil || !strings.HasPrefix(filepath.ToSlash(relative), "files/") {
			return errors.New("unsafe companion database mapping")
		}
		if e = a.store.materialize(other.Ref, dest); e != nil {
			return e
		}
		manifest.Snapshots = append(manifest.Snapshots, &capturedSnapshot{Path: other.Revision.Database, Captured: filepath.ToSlash(relative), Source: sqliteSourceState(other.Revision.Observed)})
	}

	for _, d := range v.Dependencies {
		dest, ok := view.captured(d.Path)
		if !ok || d.Object == nil || d.State != "retained" {
			return errors.New("unresolved replay dependency")
		}
		relative, e := filepath.Rel(dir, dest)
		if e != nil || !strings.HasPrefix(filepath.ToSlash(relative), "files/") {
			return errors.New("unsafe replay dependency mapping")
		}
		if e = os.MkdirAll(filepath.Dir(dest), 0700); e != nil {
			return e
		}
		f, e := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		e = a.store.walkObject(*d.Object, func(rd io.Reader) error { _, e := io.Copy(f, rd); return e })
		f.Close()
		if e != nil {
			return e
		}
		at := timeFromNS(d.MTimeNS)
		if e = os.Chtimes(dest, at, at); e != nil {
			return e
		}
	}
	root, ok := view.captured(v.Source.Path)
	if !ok {
		return errors.New("unmapped source root")
	}
	config := a.config
	config.Name, config.Kind, config.Account = v.Source.Name, v.Source.Kind, v.Source.Account
	config.Path = root
	adapter, e := MakeAdapter(config)
	if e != nil {
		return e
	}
	adapter.(interface{ bindCapture(*captureView) }).bindCapture(view)
	setAdapterContext(adapter, a.context())
	if conductor, ok := adapter.(*conductorAdapter); ok {
		config.Path = database
		conductor.config = config
		if strings.HasPrefix(v.Entity, "session:") {
			conductor.selected = map[string]bool{strings.TrimPrefix(v.Entity, "session:"): true}
		}
	}
	discover := adapter.Discover
	if conductor, ok := adapter.(*conductorAdapter); ok && a.maxEntityBytes > 0 && !strings.HasPrefix(v.Entity, "session:") {
		discover = func(emit func(WorkspaceRecord) error) error {
			ids := make([]string, 0, len(v.ParserInputs))
			for id := range v.ParserInputs {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			if len(ids) == 0 {
				return errors.New("bounded historical replay needs retained session parser inputs")
			}
			for _, id := range ids {
				var table recordTable
				if err := a.store.readJSON(v.ParserInputs[id], &table); err != nil {
					return err
				}
				var bytes int64
				for _, chunk := range table.Chunks {
					bytes += chunk.Length
				}
				if bytes > a.maxEntityBytes {
					if a.entityGap != nil {
						a.entityGap(id)
					}
					continue
				}
				conductor.selected = map[string]bool{id: true}
				if err := conductor.Discover(emit); err != nil {
					return err
				}
			}
			return nil
		}
	}
	if e = discover(func(record WorkspaceRecord) error {
		record.EvidenceIdentity = retainedSQLiteIdentity(r)
		if isConductorSource(v.Source.Kind) {
			identity, ok := v.EntityIdentities[record.SourceID]
			if !ok {
				return errors.New("unresolved Conductor raw authority identity")
			}
			record.EvidenceIdentity = identity
		}
		record.EvidenceRevision = r.Ref.Hash
		return emit(record)
	}); e != nil {
		return e
	}
	if provider, ok := adapter.(tl1SnapshotProvider); ok {
		snapshots := provider.tl1Snapshots()
		for i := range snapshots {
			snapshots[i].EvidenceIdentity = retainedSQLiteIdentity(r)
			snapshots[i].EvidenceRevision = r.Ref.Hash
		}
		a.snapshots = append(a.snapshots, snapshots...)
	}
	return nil
}

// openSQLiteReplay pins committed revisions while holding the capture lock,
// then releases it before decompression, parsing, catalog writes or uploads.
func openSQLiteReplay(ctx context.Context, target captureTarget) (*sqliteReplayAdapter, func(), error) {
	lock, e := lockCaptureReader(ctx, target)
	if e != nil {
		return nil, nil, e
	}
	c, e := sourceCaptureForTarget(ctx, target)
	if e != nil {
		releaseLockFile(lock)
		return nil, nil, e
	}
	s := newRecordStore(c)
	revs, e := s.revisions()
	if e != nil || len(revs) == 0 {
		releaseLockFile(lock)
		return nil, nil, e
	}
	allRefs := []recordObject{}
	for _, r := range revs {
		allRefs = append(allRefs, r.Ref)
		for _, other := range r.Companions {
			allRefs = append(allRefs, other.Ref)
		}
	}
	close, e := c.pinReader(allRefs)
	if e != nil {
		releaseLockFile(lock)
		return nil, nil, e
	}
	releaseLockFile(lock)
	config := c.source
	config.Path = target.Dir
	adapter := &sqliteReplayAdapter{baseAdapter: baseAdapter{config: config, capability: "retrieval-only", view: newCaptureView(target.Host, target.Dir, c.manifest)}, store: s, revisions: revs}
	return adapter, close, nil
}

func retainedSQLiteIdentity(r retainedSQLiteRevision) string {
	if len(r.Companions) == 0 {
		return r.Revision.Identity
	}
	identities := map[string]string{r.Revision.Database: r.Revision.Identity}
	for _, other := range r.Companions {
		identities[other.Revision.Database] = other.Revision.Identity
	}
	return hashBytes([]byte(jsonText(identities)))
}
func sameSQLiteAuthorityVersions(a, b retainedSQLiteRevision) bool {
	versions := func(r retainedSQLiteRevision) string {
		v := map[string]int64{r.Revision.Database: r.Revision.Observed.version()}
		for _, other := range r.Companions {
			v[other.Revision.Database] = other.Revision.Observed.version()
		}
		for _, d := range r.Revision.Dependencies {
			v["file:"+d.Path] = d.MTimeNS
		}
		return jsonText(v)
	}
	return versions(a) == versions(b)
}

// Reproduce only SQLite's five trusted affinity classes. Never interpolate a
// source declaration, constraint or expression into executable replay SQL.
func recordColumnAffinity(declaration string) string {
	d := strings.ToUpper(declaration)
	switch {
	case strings.Contains(d, "INT"):
		return "INTEGER"
	case strings.Contains(d, "CHAR") || strings.Contains(d, "CLOB") || strings.Contains(d, "TEXT"):
		return "TEXT"
	case d == "" || strings.Contains(d, "BLOB"):
		return "BLOB"
	case strings.Contains(d, "REAL") || strings.Contains(d, "FLOA") || strings.Contains(d, "DOUB"):
		return "REAL"
	default:
		return "NUMERIC"
	}
}
