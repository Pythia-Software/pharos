package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func recordTestStore(t *testing.T, config Config, source string) *recordStore {
	t.Helper()
	dir := capturedPath(config, source, "")
	c, e := sourceCaptureForTarget(t.Context(), captureTarget{Host: currentHost(), Source: source, Dir: dir})
	if e != nil {
		t.Fatal(e)
	}
	return newRecordStore(c)
}
func recordTestReplay(t *testing.T, config Config, source string, catalog *Catalog) IngestResult {
	t.Helper()
	target := captureTarget{Host: capturedHost(filepath.Join(config.CaptureRoot, "host-a"), "host-a"), Source: source, Dir: filepath.Join(config.CaptureRoot, "host-a", source)}
	reader, close, e := openSQLiteReplay(t.Context(), target)
	if e != nil {
		t.Fatal(e)
	}
	if reader == nil {
		t.Fatal("no committed SQLite records")
	}
	defer close()
	result := catalog.IngestContext(withIngestMode(t.Context(), ingestMode{force: true}), reader, nil)
	if result.Error != nil {
		t.Fatalf("raw replay: %+v", result)
	}
	return result
}
func TestSQLiteRecordsTypedRoundTrip(t *testing.T) {
	useHost(t, "host-a")
	dir := t.TempDir()
	file := filepath.Join(dir, "source.db")
	db := openWritableSQLite(t, file)
	execSQL(t, db, `CREATE TABLE sessions(id TEXT PRIMARY KEY)`, `CREATE TABLE messages(session_id TEXT,content TEXT)`, `CREATE TABLE "unknown; DROP TABLE sessions;--"(n,empty_text,empty_blob,i,r,b,raw_json,invalid)`)
	tx := seedTx(t, db)
	bad := []byte{0xff, 0, 0x80, 0xfe}
	payload := `{"unknown":{"int":9223372036854775807}, "spacing": [ 1, 2 ]}`
	for range 2 {
		if _, e := tx.Exec(`INSERT INTO "unknown; DROP TABLE sessions;--" VALUES(NULL,'',x'',?,?,?,?,CAST(? AS TEXT))`, int64(math.MaxInt64), math.SmallestNonzeroFloat64, bad, payload, bad); e != nil {
			t.Fatal(e)
		}
	}
	commitSeed(t, tx)
	source := SourceConfig{Name: "conductor", Kind: "conductor", Path: dir, Account: "local", Enabled: true}
	config := captureTestConfig(t, source)
	if run := runCapture(t, config); !run.OK {
		t.Fatal(run)
	}
	s := recordTestStore(t, config, "conductor")
	revs, e := s.revisions()
	if e != nil || len(revs) != 1 {
		t.Fatalf("revisions %d: %v", len(revs), e)
	}
	encoded, e := json.Marshal(revs[0].Revision)
	if e != nil {
		t.Fatal(e)
	}
	var generic map[string]any
	if e = json.Unmarshal(encoded, &generic); e != nil {
		t.Fatal(e)
	}
	observed := generic["source_version"].(map[string]any)
	if _, ok := observed["mtime_ns"].(string); !ok {
		t.Fatal("source authority mtime is not safe for JavaScript JSON round trips")
	}
	encoded, e = json.Marshal(generic)
	if e != nil {
		t.Fatal(e)
	}
	var roundTrip sqliteRevision
	if e = json.Unmarshal(encoded, &roundTrip); e != nil || roundTrip.Observed != revs[0].Revision.Observed {
		t.Fatalf("source authority precision lost: %v", e)
	}
	var unknown recordTable
	for _, table := range revs[0].Revision.Tables {
		if strings.HasPrefix(table.Name, "unknown;") {
			unknown = table
		}
	}
	var retained [][]sqliteValue
	if e = s.scanRecordRows(unknown, func(row []sqliteValue) error { retained = append(retained, row); return nil }); e != nil {
		t.Fatal(e)
	}
	if len(retained) != 2 || !reflect.DeepEqual(retained[0][:8], retained[1][:8]) {
		t.Fatalf("duplicate row occurrences lost: %+v", retained)
	}
	want := []any{nil, "", []byte{}, int64(math.MaxInt64), math.SmallestNonzeroFloat64, bad, payload, string(bad)}
	for i, v := range retained[0][:len(want)] {
		actual, e := v.decode()
		if e != nil || !reflect.DeepEqual(actual, want[i]) {
			t.Fatalf("value %d: %#v want %#v (%v)", i, actual, want[i], e)
		}
	}
	if len(retained[0]) != 9 || !unknown.Columns[8].ImplicitRowID || reflect.DeepEqual(retained[0][8], retained[1][8]) {
		t.Fatal("native keyless row identities were lost")
	}
	db.Close()
	os.RemoveAll(dir)
	rebuilt := filepath.Join(t.TempDir(), "inert.db")
	if e = s.materialize(revs[0].Ref, rebuilt); e != nil {
		t.Fatal(e)
	}
	offline, e := openReadOnlySQLite(rebuilt)
	if e != nil {
		t.Fatal(e)
	}
	defer offline.Close()
	var raw, original []byte
	var integer int64
	if e = offline.QueryRow(`SELECT b,CAST(invalid AS BLOB),i FROM "unknown; DROP TABLE sessions;--" LIMIT 1`).Scan(&raw, &original, &integer); e != nil || !reflect.DeepEqual(raw, bad) || !reflect.DeepEqual(original, bad) || integer != math.MaxInt64 {
		t.Fatalf("offline new extractor: %x %x %d: %v", raw, original, integer, e)
	}
}
func TestSQLiteRecordsRebuildKeepsAbsentSessionsAndLatestMessageSet(t *testing.T) {
	useHost(t, "host-a")
	dir := filepath.Join(t.TempDir(), "conductor")
	exec := conductorFixture(t, dir)
	exec(addConductorSession("deleted", 2)...)
	exec(addConductorSession("survives", 3)...)
	config := captureTestConfig(t, SourceConfig{Name: "conductor", Kind: "conductor", Path: dir, Account: "local", Enabled: true})
	config.CaptureGenerations = 0
	if run := runCapture(t, config); !run.OK {
		t.Fatal(run)
	}
	exec(`DELETE FROM session_messages WHERE session_id='deleted'`, `DELETE FROM sessions WHERE id='deleted'`, `DELETE FROM session_messages WHERE id='survives-m1'`)
	if run := runCapture(t, config); !run.OK {
		t.Fatal(run)
	}
	exec(`UPDATE session_messages SET content='edited without appending' WHERE id='survives-m2'`)
	if run := runCapture(t, config); !run.OK {
		t.Fatal(run)
	}
	sourceDir := capturedPath(config, "conductor", "")
	os.RemoveAll(dir)
	os.RemoveAll(filepath.Join(sourceDir, "files"))
	os.RemoveAll(filepath.Join(sourceDir, "history"))
	useHost(t, "host-b")
	catalog, _ := testCatalog(t)
	results := indexCaptures(t, catalog, config.CaptureRoot, "host-a")
	if len(results) != 1 || conversationMessages(t, catalog, "conductor:deleted") != 2 || conversationMessages(t, catalog, "conductor:survives") != 2 {
		t.Fatalf("offline latest/absent replay: %+v", results)
	}
	if countRows(t, catalog, `SELECT COUNT(*) FROM messages WHERE text='edited without appending'`) != 1 {
		t.Fatal("in-place edit was not retained")
	}
	var origin, host string
	if e := catalog.DB.QueryRow(`SELECT origin,origin_host_id FROM conversations WHERE native_id='conductor:survives'`).Scan(&origin, &host); e != nil || origin != "sqlite:"+filepath.Join(dir, "conductor.db") || host != "host-a" {
		t.Fatalf("origin authority changed: %q %q %v", origin, host, e)
	}
	if countRows(t, catalog, `SELECT COUNT(*) FROM copy_evidence WHERE revision<>''`) < 2 {
		t.Fatal("raw revision authority was not persisted")
	}
}
func TestSQLiteRecordsBudgetAndPublicationRecovery(t *testing.T) {
	for _, stage := range []string{"records-objects", "records-inventory", "records-head", "records-receipt"} {
		t.Run(stage, func(t *testing.T) {
			useHost(t, "host-a")
			dir := filepath.Join(t.TempDir(), "conductor")
			exec := conductorFixture(t, dir)
			exec(addConductorSession("s", 1)...)
			config := captureTestConfig(t, SourceConfig{Name: "conductor", Kind: "conductor", Path: dir, Account: "local", Enabled: true})
			override(t, &captureFault, func(point, rel string) error {
				if point == stage {
					return errors.New("injected interruption")
				}
				return nil
			})
			if run := runCapture(t, config); run.OK {
				t.Fatal("fault was not reached")
			}
			captureFault = nil
			if run := runCapture(t, config); !run.OK {
				t.Fatal(run)
			}
			os.RemoveAll(dir)
			catalog, _ := testCatalog(t)
			recordTestReplay(t, config, "conductor", catalog)
			if conversationMessages(t, catalog, "conductor:s") != 1 {
				t.Fatal("retry lost the committed evidence")
			}
		})
	}
	t.Run("budget", func(t *testing.T) {
		useHost(t, "host-a")
		dir := filepath.Join(t.TempDir(), "conductor")
		exec := conductorFixture(t, dir)
		exec(addConductorSession("s", 2)...)
		config := captureTestConfig(t, SourceConfig{Name: "conductor", Kind: "conductor", Path: dir, Account: "local", Enabled: true})
		config.CaptureGenerations = 0
		override(t, &recordMaxValueBytes, int64(8))
		if run := runCapture(t, config); run.OK {
			t.Fatal("oversized rows got a complete receipt")
		}
		s := recordTestStore(t, config, "conductor")
		if h, e := s.head(filepath.Join(dir, "conductor.db"), "database"); e != nil || h.Version != 0 {
			t.Fatalf("failed scan advanced coverage: %+v %v", h, e)
		}
		exec(`DELETE FROM session_messages`, `DELETE FROM sessions`)
		runCapture(t, config)
		m := readCaptureManifest(t, config, "conductor")
		if len(m.Snapshots[0].Generations) == 0 {
			t.Fatal("unexported generation was pruned at retention zero")
		}

	})
}
func TestSQLiteRecordsPinsSurviveRotationAndOverlappingOwners(t *testing.T) {
	useHost(t, "host-a")
	dir := filepath.Join(t.TempDir(), "conductor")
	exec := conductorFixture(t, dir)
	exec(addConductorSession("s", 1)...)
	config := captureTestConfig(t, SourceConfig{Name: "conductor", Kind: "conductor", Path: dir, Account: "local", Enabled: true})
	config.CaptureGenerations = 0
	if run := runCapture(t, config); !run.OK {
		t.Fatal(run)
	}
	target := captureTarget{Host: currentHost(), Source: "conductor", Dir: capturedPath(config, "conductor", "")}
	lock, e := lockCaptureTarget(t.Context(), target.Dir)
	if e != nil {
		t.Fatal(e)
	}
	c, e := sourceCaptureForTarget(t.Context(), target)
	if e != nil {
		t.Fatal(e)
	}
	snapshot := c.manifest.Snapshots[0]
	v := capturedVersion{Captured: snapshot.Captured, Size: snapshot.Size, CapturedAt: snapshot.CapturedAt, Source: &snapshot.Source}
	first, e := c.pinVersion("job-a", "backfill", v)
	if e != nil {
		t.Fatal(e)
	}
	second, e := c.pinVersion("job-b", "reader", v)
	if e != nil {
		t.Fatal(e)
	}
	if first.Path != second.Path || first.ID == second.ID {
		t.Fatal("owners did not independently pin the same immutable input")
	}
	lock.Close()
	for i := range 3 {
		exec(fmt.Sprintf(`UPDATE session_messages SET content='revision %d'`, i))
		if run := runCapture(t, config); !run.OK {
			t.Fatal(run)
		}
	}
	if _, e = os.Stat(filepath.Join(target.Dir, filepath.FromSlash(first.Path))); e != nil {
		t.Fatal("active pin was pruned", e)
	}
	// Simulate restart and an orphaned generation omitted from the manifest.
	lock, e = lockCaptureTarget(t.Context(), target.Dir)
	if e != nil {
		t.Fatal(e)
	}
	defer lock.Close()
	c, e = sourceCaptureForTarget(t.Context(), target)
	if e != nil {
		t.Fatal(e)
	}
	for _, s := range c.snapshots {
		s.Generations = nil
	}
	if e = c.commit(false); e != nil {
		t.Fatal(e)
	}
	c.prune = []string{first.Path}
	c.removePruned(true)
	if _, e = os.Stat(filepath.Join(target.Dir, filepath.FromSlash(first.Path))); e != nil {
		t.Fatal("unnamed history sweep deleted pin", e)
	}
	if e = releasePreservationOwner(target.Dir, "job-a"); e != nil {
		t.Fatal(e)
	}
	if e = releasePreservationOwner(target.Dir, "job-a"); e != nil {
		t.Fatal(e)
	}
	if pinned, e := pinnedPath(target.Dir, first.Path); e != nil || !pinned {
		t.Fatal("one owner released another owner's pin")
	}
	p, e := loadPreservationPins(target.Dir)
	if e != nil {
		t.Fatal(e)
	}
	p.Gates = map[string]int64{"job-b": 0}
	if e = savePreservationPins(target.Dir, p); e != nil {
		t.Fatal(e)
	}
	if e = reserveCaptureAcquisition(target.Dir, 1); e == nil {
		t.Fatal("exhausted acquisition gate was bypassed")
	}
	// A pre-upgrade writer's maximum version is 1. This source is incompatible.
	m := readCaptureManifest(t, config, "conductor")
	if m.Version <= 1 {
		t.Fatal("pin did not gate incompatible writers")
	}
	m.Version = captureManifestVersion + 1
	b, _ := json.Marshal(m)
	if e = os.WriteFile(filepath.Join(target.Dir, captureManifestName), b, 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = loadCaptureManifest(target.Dir); e == nil {
		t.Fatal("future capture format was accepted")
	}
}
func TestSQLiteRecordsRawAuthority(t *testing.T) {
	useHost(t, "host-a")
	catalog, _ := testCatalog(t)
	record := WorkspaceRecord{SourceID: "s", SourceKind: "conductor", Account: "local", Title: "first", EvidenceIdentity: hashBytes([]byte("raw-a")), EvidenceRevision: hashBytes([]byte("revision-a")), Conversations: []ConversationRecord{{NativeID: "conductor:s", Provider: "claude", Account: "local", Origin: "sqlite:/original/db", Messages: []MessageRecord{{NativeID: "m", Role: "user", Kind: "message", Text: "first", Selected: true}}}}}
	observe(&record, 10)
	apply := func(r WorkspaceRecord) error {
		tx, e := catalog.beginWrite(t.Context())
		if e != nil {
			return e
		}
		defer tx.Rollback()
		if _, _, e = ingestCopy(tx, r, false, "host-a", "conductor", true); e != nil {
			return e
		}
		return tx.Commit()
	}
	if e := apply(record); e != nil {
		t.Fatal(e)
	}
	changed := record
	changed.EvidenceIdentity = hashBytes([]byte("raw-b"))
	changed.Title = "conflict"
	if e := apply(changed); !errors.Is(e, errOlderCopy) {
		t.Fatalf("equal-version conflict applied: %v", e)
	}
	observe(&changed, 9)
	if e := apply(changed); !errors.Is(e, errOlderCopy) {
		t.Fatalf("regressing version applied: %v", e)
	}
	observe(&record, 10)
	record.Conversations[0].Coverage = "new-parser"
	if e := apply(record); e != nil {
		t.Fatalf("identical raw parser upgrade failed: %v", e)
	}
	execSQL(t, catalog.DB, "DELETE FROM copy_evidence")
	if e := apply(record); !errors.Is(e, errOlderCopy) {
		t.Fatalf("legacy unassociated equality applied: %v", e)
	}
	if workspaceTitle(t, catalog, "s") != "first" {
		t.Fatal("rejected branch changed workspace state")
	}
}
func TestSQLiteRecordsLogicalAcquisitionUsesRetainedParserInput(t *testing.T) {
	useHost(t, "host-a")
	dir := filepath.Join(t.TempDir(), "conductor")
	exec := conductorFixture(t, dir)
	exec(addConductorSession("s", 2)...)
	source := SourceConfig{Name: "conductor", Kind: "conductor", Path: dir, Account: "local", Enabled: true}
	config := captureTestConfig(t, source)
	calls := 0
	e := acquireConductorSession(t.Context(), config.CaptureRoot, source, filepath.Join(dir, "conductor.db"), "s", func(r WorkspaceRecord) error {
		calls++
		if r.EvidenceIdentity == "" || r.EvidenceRevision == "" || len(r.Conversations[0].Messages) != 2 {
			return errors.New("missing committed evidence")
		}
		// A parser callback runs after capture lock release; another capture can
		// start and clean its own temporaries without deleting this revision.
		session, e := beginCapture(config)
		if e != nil {
			return e
		}
		session.Close()
		return nil
	})
	if e != nil || calls != 1 {
		t.Fatalf("logical boundary: calls=%d %v", calls, e)
	}
	m := readCaptureManifest(t, config, "conductor")
	if len(m.Snapshots) != 0 {
		t.Fatal("incremental acquisition made a whole DB backup")
	}
	s := recordTestStore(t, config, "conductor")
	revs, e := s.revisions()
	if e != nil || len(revs) != 1 {
		t.Fatalf("logical revisions %d %v", len(revs), e)
	}
	os.RemoveAll(dir)
	rows, e := s.conductorParserRows(revs[0].Revision, "s")
	if e != nil || len(rows) != 2 {
		t.Fatalf("parser ordinal resolution: %d %v", len(rows), e)
	}
	if _, e = s.conductorParserRows(revs[0].Revision, "unresolved"); e == nil {
		t.Fatal("unresolved parser ordinal was guessed")
	}
	useHost(t, "host-b")
	catalog, _ := testCatalog(t)
	results := indexCaptures(t, catalog, config.CaptureRoot, "host-a")
	if len(results) != 1 || conversationMessages(t, catalog, "conductor:s") != 2 {
		t.Fatalf("ordinary indexing lost logical-only capture: %+v", results)
	}
}
func TestSQLiteRecordsCancellationAndReconciliation(t *testing.T) {
	useHost(t, "host-a")
	dir := filepath.Join(t.TempDir(), "conductor")
	exec := conductorFixture(t, dir)
	exec(addConductorSession("s", 1)...)
	source := SourceConfig{Name: "conductor", Kind: "conductor", Path: dir, Account: "local", Enabled: true}
	config := captureTestConfig(t, source)
	if e := reconcileSQLiteLive(t.Context(), config.CaptureRoot, source, filepath.Join(dir, "conductor.db"), true); e != nil {
		t.Fatal(e)
	}
	m := readCaptureManifest(t, config, "conductor")
	first := m.RecordCoverage[filepath.Join(dir, "conductor.db")]
	if first.Pending || first.LastCompleteAt == "" {
		t.Fatal("missing full coverage status")
	}
	override(t, &recordMaxRows, int64(0))
	exec(`UPDATE session_messages SET content='signal unchanged'`)
	if e := reconcileSQLiteLive(t.Context(), config.CaptureRoot, source, filepath.Join(dir, "conductor.db"), true); e == nil {
		t.Fatal("interrupted reconciliation succeeded")
	}
	m = readCaptureManifest(t, config, "conductor")
	second := m.RecordCoverage[filepath.Join(dir, "conductor.db")]
	if !second.Pending || second.LastCompleteAt != first.LastCompleteAt {
		t.Fatal("interrupted scan advanced coverage")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if e := reconcileSQLiteLive(ctx, config.CaptureRoot, source, filepath.Join(dir, "conductor.db"), true); !errors.Is(e, context.Canceled) {
		t.Fatalf("cancelled reconciliation: %v", e)
	}
}

// Synthetic measurements are opt-in, never run over a real library. The same
// format/reader is measured at increasing sizes; no production cutover claim
// is made from extrapolation.
func BenchmarkSQLiteRecordExport(b *testing.B) {
	for _, n := range []int{4096, 16384, 65536} {
		b.Run(fmt.Sprintf("%dKiB", n), func(b *testing.B) {
			root := b.TempDir()
			file := filepath.Join(root, "source.db")
			db, e := sql.Open("sqlite", file)
			if e != nil {
				b.Fatal(e)
			}
			defer db.Close()
			if _, e = db.Exec(`CREATE TABLE evidence(id INTEGER, data BLOB)`); e != nil {
				b.Fatal(e)
			}
			tx, _ := db.Begin()
			for i := range n {
				if _, e = tx.Exec(`INSERT INTO evidence VALUES(?,?)`, i, strings.Repeat("payload", 146)); e != nil {
					b.Fatal(e)
				}
			}
			tx.Commit()
			c := &sourceCapture{ctx: context.Background(), dir: filepath.Join(root, "capture"), session: &captureSession{host: Host{ID: "synthetic"}}, source: SourceConfig{Name: "synthetic", Kind: "conductor", Path: file}, manifest: &captureManifest{}}
			os.MkdirAll(c.dir, 0700)
			state, _ := sqliteDependencyState(file)
			b.SetBytes(int64(n * 1022))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, e = newRecordStore(c).exportSQLite(file, file, "database", state, "", nil); e != nil {
					b.Fatal(e)
				}
			}
		})
	}
}

func TestSQLiteRecordsAntigravityGroupsAndDBOnlyRows(t *testing.T) {
	useHost(t, "host-a")
	dir := antigravityFixture(t)
	orphan := "dddddddd-0000-4000-8000-000000000004"
	antigravityRequestRows(t, dir, orphan, []any{[]uint64{1}, "gemini-unknown", 1, 2, 0, 0})
	source := SourceConfig{Name: "antigravity", Kind: "antigravity", Path: dir, Account: "local", Enabled: true}
	config := captureTestConfig(t, source)
	oracle, _ := testCatalog(t)
	ingestSource(t, oracle, source)
	expected := countRows(t, oracle, "SELECT COUNT(*) FROM messages")
	if run := runCapture(t, config); !run.OK {
		t.Fatal(run)
	}
	s := recordTestStore(t, config, "antigravity")
	groups, e := s.revisions()
	if e != nil || len(groups) != 1 {
		t.Fatalf("complete group %d: %v", len(groups), e)
	}
	found := false
	for _, r := range append(groups, groups[0].Companions...) {
		if filepath.Base(r.Revision.Database) == orphan+".db" {
			found = true
			var rows int64
			for _, table := range r.Revision.Tables {
				rows += table.Rows
			}
			if rows == 0 {
				t.Fatal("DB-only protobuf evidence was empty")
			}
		}
	}
	if !found {
		t.Fatal("DB-only request database omitted")
	}
	t.Run("receipt survives rename recovery", func(t *testing.T) {
		target := captureTarget{Host: currentHost(), Source: source.Name, Dir: capturedPath(config, source.Name, "")}
		lock, e := lockCaptureTarget(t.Context(), target.Dir)
		if e != nil {
			t.Fatal(e)
		}
		defer lock.Close()
		c, e := sourceCaptureForTarget(t.Context(), target)
		if e != nil {
			t.Fatal(e)
		}
		snapshot := c.manifest.Snapshots[0]
		version := capturedVersion{Captured: snapshot.Captured, Size: snapshot.Size, CapturedAt: snapshot.CapturedAt, Source: &snapshot.Source}
		moved, e := c.preserve(version, true)
		if e != nil {
			t.Fatal(e)
		}
		// Recover after a filesystem rename, with history already present but
		// the latest path and newly transferred receipt not yet published.
		for _, file := range []string{filepath.Join(target.Dir, filepath.FromSlash(version.Captured)), filepath.Join(target.Dir, recordDir, "receipts", hashBytes([]byte(moved.Captured))+".json")} {
			if e = os.Remove(file); e != nil {
				t.Fatal(e)
			}
		}
		if _, e = c.preserve(version, false); e != nil {
			t.Fatal(e)
		}
		if !c.canPruneSnapshot(moved.Captured) {
			t.Fatal("verified export association was lost when latest no longer existed")
		}
	})
	config.CaptureGenerations = 0
	for generation := range 2 {
		antigravitySummaryRows(t, dir, []any{antigravityRoot, fmt.Sprintf("title %d", generation), "[]", "", 0, ""})
		if run := runCapture(t, config); !run.OK {
			t.Fatal(run)
		}
		manifest := readCaptureManifest(t, config, source.Name)
		for _, snapshot := range manifest.Snapshots {
			if len(snapshot.Generations) != 0 {
				t.Fatalf("verified snapshot accumulated after rotation: %s has %d generations", snapshot.Path, len(snapshot.Generations))
			}
		}
		if entries, e := os.ReadDir(capturedPath(config, source.Name, captureHistoryDir)); e != nil && !errors.Is(e, os.ErrNotExist) || len(entries) != 0 {
			t.Fatalf("verified native history survived retention zero: %d entries (%v)", len(entries), e)
		}
	}
	os.RemoveAll(dir)
	catalog, _ := testCatalog(t)
	recordTestReplay(t, config, "antigravity", catalog)
	if actual := countRows(t, catalog, "SELECT COUNT(*) FROM messages"); actual != expected {
		t.Fatalf("raw group replay lost usage/transcripts: %d want %d", actual, expected)
	}
}
func TestSQLiteRecordsLocatorsBindDuplicateRowsToObservation(t *testing.T) {
	useHost(t, "host-a")
	dir := t.TempDir()
	file := filepath.Join(dir, "conductor.db")
	db := openWritableSQLite(t, file)
	execSQL(t, db, `CREATE TABLE sessions(id TEXT)`, `CREATE TABLE messages(id TEXT,session_id TEXT,created_at TEXT,content TEXT,full_message TEXT)`, `INSERT INTO sessions VALUES('s')`, `INSERT INTO messages VALUES('duplicate','s','same','first','{"unknown":1}')`, `INSERT INTO messages VALUES('duplicate','s','same','second','{"unknown":2}')`)
	config := captureTestConfig(t, SourceConfig{Name: "conductor", Kind: "conductor", Path: dir, Account: "local", Enabled: true})
	if run := runCapture(t, config); !run.OK {
		t.Fatal(run)
	}
	s := recordTestStore(t, config, "conductor")
	revs, e := s.revisions()
	if e != nil {
		t.Fatal(e)
	}
	db.Close()
	os.RemoveAll(dir)
	for index, want := range []string{"first", "second"} {
		locator := fmt.Sprintf("sqlite:%s:messages:s:content:%d", file, index)
		field, e := s.resolveLocator(revs[0].Ref.Hash, locator)
		if e != nil {
			t.Fatal(e)
		}
		value, e := field.Value.decode()
		if e != nil || value != want {
			t.Fatalf("ordinal %d: %#v want %s (%v)", index, value, want, e)
		}
	}
	field, e := s.resolveLocator(revs[0].Ref.Hash, "sqlite:"+file+":messages:s:content:1:full_message")
	if e != nil {
		t.Fatal(e)
	}
	value, _ := field.Value.decode()
	if value != `{"unknown":2}` {
		t.Fatalf("alternate payload: %v", value)
	}
	if _, e = s.resolveLocator("", "sqlite:"+file+":messages:s:content:1"); e == nil {
		t.Fatal("unassociated legacy ordinal resolved")
	}
}
func TestSQLiteRecordsCrossFileRaceDoesNotCommitGroup(t *testing.T) {
	useHost(t, "host-a")
	dir := antigravityFixture(t)
	config := captureTestConfig(t, SourceConfig{Name: "antigravity", Kind: "antigravity", Path: dir, Account: "local", Enabled: true})
	moved := false
	override(t, &captureFault, func(stage, rel string) error {
		if stage == "records-objects" && !moved {
			moved = true
			writeSourceFile(t, filepath.Join(dir, "annotations", antigravityRoot+".pbtxt"), "changed while reading")
		}
		return nil
	})
	if run := runCapture(t, config); run.OK {
		t.Fatal("moving dependencies received a complete acquisition receipt")
	}
	s := recordTestStore(t, config, "antigravity")
	if revs, e := s.revisions(); e != nil || len(revs) != 0 {
		t.Fatalf("incomplete group was exposed for replay: %d %v", len(revs), e)
	}
	for _, snapshot := range s.c.snapshots {
		if s.c.canPruneSnapshot(snapshot.Captured) {
			t.Fatal("incomplete group allowed native pruning")
		}
	}
}

func BenchmarkSQLiteConductorChangedSession(b *testing.B) {
	for _, rows := range []int{256, 1024, 4096} {
		b.Run(fmt.Sprintf("%dMiB", rows/64), func(b *testing.B) {
			root := b.TempDir()
			file := filepath.Join(root, "conductor.db")
			db, e := sql.Open("sqlite", file)
			if e != nil {
				b.Fatal(e)
			}
			defer db.Close()
			for _, statement := range []string{`CREATE TABLE sessions(id TEXT PRIMARY KEY)`, `CREATE TABLE messages(id INTEGER PRIMARY KEY,session_id TEXT,content TEXT)`, `CREATE INDEX by_session ON messages(session_id)`, `INSERT INTO sessions VALUES('cold'),('hot')`} {
				if _, e = db.Exec(statement); e != nil {
					b.Fatal(e)
				}
			}
			tx, _ := db.Begin()
			padding := strings.Repeat("cold content ", 1260)
			for range rows {
				if _, e = tx.Exec(`INSERT INTO messages(session_id,content) VALUES('cold',?)`, padding); e != nil {
					b.Fatal(e)
				}
			}
			tx.Exec(`INSERT INTO messages(session_id,content) VALUES('hot','small change')`)
			tx.Commit()
			c := &sourceCapture{ctx: context.Background(), dir: filepath.Join(root, "capture"), session: &captureSession{host: Host{ID: "synthetic"}}, source: SourceConfig{Name: "synthetic", Kind: "conductor", Path: file}, manifest: &captureManifest{}}
			os.MkdirAll(c.dir, 0700)
			state, _ := sqliteDependencyState(file)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, e = newRecordStore(c).exportSQLite(file, file, "session:hot", state, "hot", nil); e != nil {
					b.Fatal(e)
				}
			}
		})
	}
}

func TestSQLiteRecordsWholeAndSelectiveObservationsShareEntityAuthority(t *testing.T) {
	useHost(t, "host-a")
	dir := filepath.Join(t.TempDir(), "conductor")
	exec := conductorFixture(t, dir)
	exec(addConductorSession("s", 2)...)
	source := SourceConfig{Name: "conductor", Kind: "conductor", Path: dir, Account: "local", Enabled: true}
	config := captureTestConfig(t, source)
	if run := runCapture(t, config); !run.OK {
		t.Fatal(run)
	}
	if e := acquireConductorSession(t.Context(), config.CaptureRoot, source, filepath.Join(dir, "conductor.db"), "s", func(WorkspaceRecord) error { return nil }); e != nil {
		t.Fatal(e)
	}
	s := recordTestStore(t, config, "conductor")
	revs, e := s.revisions()
	if e != nil || len(revs) != 2 {
		t.Fatalf("acquisitions %d: %v", len(revs), e)
	}
	if revs[0].Revision.EntityIdentities["s"] != revs[1].Revision.EntityIdentities["s"] {
		t.Fatal("whole and selective reads invented different authority for identical raw parser inputs")
	}
	os.RemoveAll(dir)
	catalog, _ := testCatalog(t)
	recordTestReplay(t, config, "conductor", catalog)
	if conversationMessages(t, catalog, "conductor:s") != 2 {
		t.Fatal("equivalent observations failed offline replay")
	}
}

func TestSQLiteRecordsFullReconciliationInvalidatesFastSignals(t *testing.T) {
	useHost(t, "host-a")
	dir := filepath.Join(t.TempDir(), "conductor")
	exec := conductorFixture(t, dir)
	exec(addConductorSession("s", 1)...)
	source := SourceConfig{Name: "conductor", Kind: "conductor", Path: dir, Account: "local", Enabled: true, Options: map[string]any{"sqlite_record_capture": true}}
	config := captureTestConfig(t, source)
	catalog, _ := testCatalog(t)
	catalog.setCaptureRoot(config.CaptureRoot)
	server := &Server{Catalog: catalog}
	ingest := func() bool {
		t.Helper()
		adapter, e := MakeAdapter(source)
		if e != nil {
			t.Fatal(e)
		}
		prepared := preparedSyncSource{adapter: adapter}
		server.prepareSyncSource(t.Context(), source, &prepared)
		if prepared.err != nil {
			t.Fatal(prepared.err)
		}
		if prepared.skipped {
			return false
		}
		result := catalog.IngestContext(withIngestMode(t.Context(), ingestMode{automatic: true, plan: prepared.plan}), adapter, nil)
		if result.Error != nil {
			t.Fatalf("automatic logical acquisition: %+v", result)
		}
		if _, e = catalog.DB.Exec(`INSERT INTO automatic_source_states(host_id,source_name,preflight) VALUES(?,?,?)
			ON CONFLICT(host_id,source_name) DO UPDATE SET preflight=excluded.preflight`, "host-a", source.Name, prepared.preflight); e != nil {
			t.Fatal(e)
		}
		return true
	}
	if !ingest() {
		t.Fatal("initial sync skipped reconciliation")
	}
	manifest := readCaptureManifest(t, config, source.Name)
	if status := manifest.RecordCoverage[filepath.Join(dir, "conductor.db")]; status.LastCompleteAt == "" || status.Pending {
		t.Fatal("planned automatic sync did not complete the initial full reconciliation")
	}
	if ingest() {
		t.Fatal("unchanged source with current reconciliation bypassed preflight")
	}
	exec(`UPDATE session_messages SET content='changed without a signal' WHERE id='s-m0'`)
	ingest()
	if countRows(t, catalog, `SELECT COUNT(*) FROM messages WHERE text='changed without a signal'`) != 0 {
		t.Fatal("fixture unexpectedly changed a fast discovery signal")
	}
	override(t, &recordReconcileInterval, time.Duration(0))
	if !ingest() {
		t.Fatal("overdue reconciliation was skipped by unchanged preflight")
	}
	if countRows(t, catalog, `SELECT COUNT(*) FROM messages WHERE text='changed without a signal'`) != 1 {
		t.Fatal("full reconciliation did not invalidate the unchanged heuristic part")
	}
	os.RemoveAll(dir)
	useHost(t, "host-b")
	rebuilt, _ := testCatalog(t)
	indexCaptures(t, rebuilt, config.CaptureRoot, "host-a")
	if countRows(t, rebuilt, `SELECT COUNT(*) FROM messages WHERE text='changed without a signal'`) != 1 {
		t.Fatal("ordinary indexing lost reconciliation-only capture")
	}
}
